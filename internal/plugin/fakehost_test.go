package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/config"
	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakeAuthFile is one auth file held by fakeHost.
type fakeAuthFile struct {
	index    string
	name     string
	provider string
	raw      []byte
	label    string
	disabled bool
	models   []string
	// getFails makes host.auth.get fail for this file.
	getFails bool
}

// fakeHost is an in-memory stand-in for the CPA host callbacks the plugin
// uses. Saving a file re-parses it through the plugin and re-registers its
// models with CPA's prefix rules, as CPA does.
type fakeHost struct {
	t   *testing.T
	svc *Service
	// forcePrefix mirrors CPA's routing.force-model-prefix.
	forcePrefix bool

	mu        sync.Mutex
	files     []*fakeAuthFile
	nextIndex int
	saves     []string
	emits     map[string][][]byte
	closes    map[string]rpcStreamCloseRequest
	closed    chan string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	return &fakeHost{
		t:      t,
		emits:  map[string][][]byte{},
		closes: map[string]rpcStreamCloseRequest{},
		closed: make(chan string, 16),
	}
}

func okResult(v any) []byte {
	return okEnvelope(v)
}

func failResult(message string) []byte {
	return ErrorEnvelope("host_error", message)
}

func (h *fakeHost) call(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodHostLog:
		return okResult(struct{}{}), nil
	case pluginabi.MethodHostAuthList:
		h.mu.Lock()
		defer h.mu.Unlock()
		files := make([]pluginapi.HostAuthFileEntry, 0, len(h.files))
		for _, file := range h.files {
			files = append(files, pluginapi.HostAuthFileEntry{
				ID:        file.name,
				AuthIndex: file.index,
				Name:      file.name,
				Type:      file.provider,
				Provider:  file.provider,
				Label:     file.label,
				Disabled:  file.disabled,
				Source:    "file",
				Path:      filepath.Join("/auths", file.name),
			})
		}
		return okResult(rpcHostAuthListResponse{Files: files}), nil
	case pluginabi.MethodHostAuthGet:
		var req pluginapi.HostAuthGetRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return failResult(errUnmarshal.Error()), nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, file := range h.files {
			if file.index == req.AuthIndex {
				if file.getFails {
					return failResult("read failed"), nil
				}
				return okResult(pluginapi.HostAuthGetResponse{AuthIndex: file.index, Name: file.name, Path: filepath.Join("/auths", file.name), JSON: file.raw}), nil
			}
		}
		return failResult("auth not found"), nil
	case pluginabi.MethodHostAuthSave:
		var req pluginapi.HostAuthSaveRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return failResult(errUnmarshal.Error()), nil
		}
		return h.save(req)
	case pluginabi.MethodHostAuthModels:
		h.mu.Lock()
		defer h.mu.Unlock()
		var out pluginapi.HostAuthModelsResponse
		for _, file := range h.files {
			if file.disabled {
				continue
			}
			out.Credentials = append(out.Credentials, pluginapi.HostAuthModels{ID: file.name, AuthIndex: file.index, Provider: file.provider, Models: append([]string(nil), file.models...)})
		}
		return okResult(out), nil
	case pluginabi.MethodHostStreamEmit:
		var req rpcStreamEmitRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return failResult(errUnmarshal.Error()), nil
		}
		h.mu.Lock()
		h.emits[req.StreamID] = append(h.emits[req.StreamID], req.Payload)
		h.mu.Unlock()
		return okResult(struct{}{}), nil
	case pluginabi.MethodHostStreamClose:
		var req rpcStreamCloseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return failResult(errUnmarshal.Error()), nil
		}
		h.mu.Lock()
		h.closes[req.StreamID] = req
		h.mu.Unlock()
		h.closed <- req.StreamID
		return okResult(struct{}{}), nil
	default:
		return failResult("unexpected host method " + method), nil
	}
}

// save mimics host.auth.save: the plugin parses the file, then CPA asks it
// for the credential's models. The fake lock is released while calling into
// the plugin, which may call back into the host.
func (h *fakeHost) save(req pluginapi.HostAuthSaveRequest) ([]byte, error) {
	name := filepath.Base(req.Name)
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return failResult("auth file name must end with .json"), nil
	}
	defaultPrefix := config.Default().ModelPrefix
	if h.svc != nil {
		defaultPrefix = h.svc.config().ModelPrefix
	}
	parsed, errParse := credential.ParseAuthFile(pluginapi.AuthParseRequest{FileName: name, RawJSON: req.JSON}, time.Now(), defaultPrefix)
	if errParse != nil {
		return failResult(errParse.Error()), nil
	}
	if !parsed.Handled {
		return failResult("no provider handled the auth file"), nil
	}
	var models []string
	if !parsed.Auth.Disabled && h.svc != nil {
		resp := h.svc.modelsForAuth(pluginapi.AuthModelRequest{AuthID: name, AuthProvider: ProviderID, StorageJSON: req.JSON, Host: pluginapi.HostConfigSummary{ForceModelPrefix: h.forcePrefix}})
		models = cpaPrefixedIDs(resp.Models, cpaAuthPrefix(parsed.Auth), h.forcePrefix)
		sort.Strings(models)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.saves = append(h.saves, name)
	for _, file := range h.files {
		if file.name == name {
			file.raw = append([]byte(nil), req.JSON...)
			file.label = parsed.Auth.Label
			file.disabled = parsed.Auth.Disabled
			file.models = models
			return okResult(pluginapi.HostAuthSaveResponse{Name: name, Path: filepath.Join("/auths", name)}), nil
		}
	}
	h.nextIndex++
	h.files = append(h.files, &fakeAuthFile{
		index:    fmt.Sprintf("idx-%d", h.nextIndex),
		name:     name,
		provider: ProviderID,
		raw:      append([]byte(nil), req.JSON...),
		label:    parsed.Auth.Label,
		disabled: parsed.Auth.Disabled,
		models:   models,
	})
	return okResult(pluginapi.HostAuthSaveResponse{Name: name, Path: filepath.Join("/auths", name)}), nil
}

// cpaAuthPrefix mirrors how CPA resolves an auth's prefix: the plugin's
// AuthData.Prefix, else the file's own prefix field.
func cpaAuthPrefix(auth pluginapi.AuthData) string {
	if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
		return prefix
	}
	if prefix, ok := auth.Metadata["prefix"].(string); ok {
		return strings.Trim(strings.TrimSpace(prefix), "/")
	}
	return ""
}

// cpaPrefixedIDs mirrors CPA's applyModelPrefixes: without force-model-prefix
// both the bare and the prefixed ID are registered.
func cpaPrefixedIDs(models []pluginapi.ModelInfo, prefix string, force bool) []string {
	var out []string
	for _, model := range models {
		if prefix == "" {
			out = append(out, model.ID)
			continue
		}
		if !force {
			out = append(out, model.ID)
		}
		out = append(out, prefix+"/"+model.ID)
	}
	return out
}

// seed stores an auth file directly, as if it had been uploaded earlier.
func (h *fakeHost) seed(name, provider string, fields map[string]any) *fakeAuthFile {
	h.t.Helper()
	raw, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		h.t.Fatalf("marshal seed: %v", errMarshal)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextIndex++
	file := &fakeAuthFile{index: fmt.Sprintf("idx-%d", h.nextIndex), name: name, provider: provider, raw: raw, disabled: jsonfield.Bool(fields, credential.KeyDisabled)}
	h.files = append(h.files, file)
	return file
}

func (h *fakeHost) file(name string) *fakeAuthFile {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, file := range h.files {
		if file.name == name {
			copied := *file
			return &copied
		}
	}
	return nil
}

func (h *fakeHost) fields(name string) map[string]any {
	h.t.Helper()
	file := h.file(name)
	if file == nil {
		h.t.Fatalf("auth file %s not found", name)
	}
	fields, errDecode := credential.DecodeFields(file.raw)
	if errDecode != nil {
		h.t.Fatalf("decode %s: %v", name, errDecode)
	}
	return fields
}

func (h *fakeHost) saveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.saves)
}

// fakeWokey is an httptest stand-in for the Wokey API.
type fakeWokey struct {
	*httptest.Server

	mu            sync.Mutex
	models        []map[string]any
	etag          string
	catalogFull   int
	catalogNotMod int
	keys          map[string]map[string]any
	issuers       map[string]map[string]any
	issuerCalls   int
	handlers      map[string]http.HandlerFunc
	// Account calls (balance, key usage, issuing balance): how many were
	// served, how many at most at once, and how long each one takes.
	accountCalls, accountBusy, accountPeak int
	accountDelay                           time.Duration
}

const (
	testGoodKey    = "sk-test-good-0000000000"
	testIssuerPath = "/api/issuer/v1"
	// testIssuerToken answers with the first sample response from Wokey's
	// issuing API, testIssuerToken2 with the second.
	testIssuerToken  = "wkt_test_good_00000000"
	testIssuerToken2 = "wkt_test_p2p_00000000"
)

func newFakeWokey(t *testing.T) *fakeWokey {
	t.Helper()
	w := &fakeWokey{
		models: []map[string]any{
			{"id": "model-a", "name": "Model A", "owned_by": "lab-a", "context_length": 200000, "max_completion_tokens": 64000,
				"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
				"pricing":      map[string]any{"prompt": "0.000003", "completion": "0.000015"}},
			{"id": "model-b", "name": "Model B", "owned_by": "lab-b", "context_length": 128000},
		},
		etag: `"v1"`,
		keys: map[string]map[string]any{
			testGoodKey: {"label": "test key", "usage": 1.5, "usage_daily": 0.25, "usage_weekly": 1, "usage_monthly": 1.5, "limit": 10, "limit_remaining": 8.5, "limit_reset": "monthly"},
		},
		issuers: map[string]map[string]any{
			testIssuerToken: {
				"provider": map[string]any{"availableUsd": "9.56639200"},
				"referrer": map[string]any{"availableUsd": "0.00000000"},
				"api":      map[string]any{"enabled": true, "eligible": true, "availableUsd": "0.33", "disqualifiedBy": nil},
			},
			testIssuerToken2: {
				"provider": map[string]any{"availableUsd": "0.00000000"},
				"referrer": map[string]any{"availableUsd": "0.00000000"},
				"api": map[string]any{"enabled": true, "eligible": false, "availableUsd": "20.47",
					"disqualifiedBy": map[string]any{"kind": "p2p", "at": "2026-11-05T12:01:20.870Z"}},
			},
		},
		handlers: map[string]http.HandlerFunc{},
	}
	w.Server = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.Close)
	return w
}

func (w *fakeWokey) setModels(models []map[string]any, etag string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.models = models
	w.etag = etag
}

// issuerCallCount returns how many issuing balance requests were served.
func (w *fakeWokey) issuerCallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.issuerCalls
}

// catalogCounts returns how many full and 304 catalog responses were served.
func (w *fakeWokey) catalogCounts() (full, notModified int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.catalogFull, w.catalogNotMod
}

func (w *fakeWokey) handle(path string, handler http.HandlerFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[path] = handler
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

// accountLoad returns how many account calls were served and the most
// that were in progress at the same time.
func (w *fakeWokey) accountLoad() (calls, peak int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.accountCalls, w.accountPeak
}

func (w *fakeWokey) serve(rw http.ResponseWriter, r *http.Request) {
	if p := r.URL.Path; p == "/v1/key" || p == "/v1/dashboard/balance" || strings.HasPrefix(p, testIssuerPath) {
		w.mu.Lock()
		w.accountCalls++
		w.accountBusy++
		w.accountPeak = max(w.accountPeak, w.accountBusy)
		delay := w.accountDelay
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			w.accountBusy--
			w.mu.Unlock()
		}()
		time.Sleep(delay)
	}
	w.mu.Lock()
	handler := w.handlers[r.URL.Path]
	w.mu.Unlock()
	if handler != nil {
		handler(rw, r)
		return
	}
	switch r.URL.Path {
	case "/v1/models":
		w.mu.Lock()
		etag := w.etag
		models := w.models
		if r.Header.Get("If-None-Match") == etag {
			w.catalogNotMod++
			w.mu.Unlock()
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		w.catalogFull++
		w.mu.Unlock()
		rw.Header().Set("ETag", etag)
		writeJSON(rw, http.StatusOK, map[string]any{"object": "list", "data": models})
	case "/v1/key", "/v1/dashboard/balance":
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.mu.Lock()
		info, ok := w.keys[key]
		w.mu.Unlock()
		if !ok {
			writeJSON(rw, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "invalid_api_key", "message": "invalid API key"}})
			return
		}
		if r.URL.Path == "/v1/key" {
			writeJSON(rw, http.StatusOK, info)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"userId": "1234", "availableUsd": 42.5, "reservedUsd": "0.75"})
	case testIssuerPath + wokey.EndpointIssuerBalance:
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.mu.Lock()
		w.issuerCalls++
		info, ok := w.issuers[token]
		w.mu.Unlock()
		if !ok {
			writeJSON(rw, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "invalid_issuer_token", "message": "invalid_issuer_token", "type": "invalid_request_error"}})
			return
		}
		writeJSON(rw, http.StatusOK, info)
	default:
		writeJSON(rw, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found", "message": "no route " + r.URL.Path}})
	}
}

// newTestService wires a service to a fake host and a fake Wokey without
// starting the background refresh loop.
func newTestService(t *testing.T, upstream *fakeWokey) (*Service, *fakeHost) {
	t.Helper()
	host := newFakeHost(t)
	svc := New("test", host.call)
	host.svc = svc
	if upstream != nil {
		svc.cfg.BaseURL = upstream.URL
		svc.issuerBase = upstream.URL + testIssuerPath
	}
	t.Cleanup(svc.Close)
	return svc, host
}

func decodeEnvelope(t *testing.T, raw []byte) pluginabi.Envelope {
	t.Helper()
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("decode envelope: %v (%s)", errUnmarshal, raw)
	}
	return envelope
}

func mustDecode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		t.Fatalf("decode %T: %v (%s)", out, errUnmarshal, raw)
	}
	return out
}

// observedRequest is what a fake upstream handler saw.
type observedRequest struct {
	Header http.Header
	Body   map[string]any
}

// observe reports each request on ch (a channel gives the test a
// happens-before edge with the handler goroutine) and then calls next.
func observe(ch chan<- observedRequest, next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		select {
		case ch <- observedRequest{Header: r.Header.Clone(), Body: body}:
		default:
		}
		next(rw, r)
	}
}

func receive(t *testing.T, ch <-chan observedRequest) observedRequest {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not called")
		return observedRequest{}
	}
}
