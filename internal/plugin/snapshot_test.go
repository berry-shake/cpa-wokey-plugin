package plugin

// TestSnapshots replays the RPC snapshot fixtures in testdata/snapshots and
// locks the plugin's behaviour at its RPC boundary: every upstream HTTP
// request, every host callback and every RPC response envelope.
//
// A fixture is one plugin instance: setup (version, plugin config, seeded
// auth files), a scripted upstream and RPC steps. Fixtures are the source of
// truth; snapshotCases (snapshot_cases_test.go) only seeds the inputs of
// fixtures that do not exist yet. To accept new outputs:
//
//	go test ./internal/plugin -run TestSnapshots -update
//
// which rewrites only steps[].output (and creates missing fixtures).
//
// Inputs. The runner builds the service with New(setup.version), seeds
// setup.credentials into the fake CPA host, then sends plugin.register with
// setup.config as config_yaml, as CPA v8 does. Placeholders in inputs:
// {{UPSTREAM}} is the scripted upstream's base URL, {{DATA_DIR}} a fresh
// plugin data dir. The pseudo-method seam.refresh_tick runs one background
// catalog loop iteration (see snapshot_seams_test.go).
//
// Representation (lossless, the same for inputs and outputs). Go's
// encoding/json writes []byte fields as base64; the fields named Payload,
// payload, OriginalRequest, StorageJSON, storage_json, RawJSON, Body,
// ResponseBody and config_yaml are written decoded instead: as embedded JSON
// when the bytes are a JSON object or array that re-encodes byte for byte,
// otherwise as a string. The runner re-encodes them before calling the
// plugin, so the plugin receives exactly the bytes CPA sends.
//
// Upstream script. Replies are matched in order per method+path; an
// unmatched request, or a reply left unused at the end, fails the test.
// Every reply is held for snapScriptLatency before its body (or before its
// last chunk), so request durations, and a stream's time after its first
// output, exceed the request log's 50 ms speed window by a margin that
// scheduling delays under parallel load cannot eat: the speeds are then
// present whenever the tokens allow them.
//
// Normalisation of outputs (volatile values only):
//   - the upstream base URL (and its bare host:port) -> {{UPSTREAM}}; the
//     data dir -> {{DATA_DIR}}.
//   - by field name, outside executor payloads: time, duration_ms, ttft_ms,
//     last_frame_ms, output_tokens_per_second, e2e_tokens_per_second,
//     created_at and latency_ms -> "<volatile>" (null stays null). The
//     request log reads the wall clock (time.Now in record.go has no seam),
//     proxies stamp created_at with it and egress checks time the lookup.
//   - id -> "<volatile>" when it is a 12-hex-digit random proxy ID
//     (proxystore.newID reads crypto/rand).
//   - Executor payloads (Payload, payload, OriginalRequest) are client or
//     upstream bytes and are never normalised by field name.
//
// The plugin clock is pinned instead (snapshotClock). After each step the
// runner waits for the stream pumps to finish (stream close and request log
// row) before capturing.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"gopkg.in/yaml.v3"
)

var updateSnapshots = flag.Bool("update", false, "rewrite the outputs of the RPC snapshot fixtures in testdata/snapshots")

const (
	snapDir = "testdata/snapshots"
	// snapScriptLatency holds every scripted reply before its body or last
	// chunk; see the speed window note above.
	snapScriptLatency = 250 * time.Millisecond
	// snapBackgroundTimeout bounds the wait for stream pumps after a step.
	snapBackgroundTimeout = 10 * time.Second
	snapVolatile          = "<volatile>"
)

// snapBytesFields are the []byte fields written decoded in fixtures.
var snapBytesFields = map[string]bool{
	"Payload": true, "payload": true, "OriginalRequest": true,
	"StorageJSON": true, "storage_json": true, "RawJSON": true,
	"Body": true, "ResponseBody": true, "config_yaml": true,
}

// snapPayloadFields hold client or upstream bytes; they are never normalised
// by field name.
var snapPayloadFields = map[string]bool{"Payload": true, "payload": true, "OriginalRequest": true}

// snapVolatileFields are replaced by "<volatile>" in outputs.
var snapVolatileFields = map[string]bool{
	"time": true, "duration_ms": true, "ttft_ms": true, "last_frame_ms": true,
	"output_tokens_per_second": true, "e2e_tokens_per_second": true,
	"created_at": true, "latency_ms": true,
}

var snapRandomProxyID = regexp.MustCompile(`^[0-9a-f]{12}$`)

type snapshotFixture struct {
	Case           string          `json:"case"`
	Description    string          `json:"description"`
	Setup          snapshotSetup   `json:"setup"`
	UpstreamScript []scriptedReply `json:"upstream_script"`
	Steps          []snapshotStep  `json:"steps"`
}

type snapshotSetup struct {
	Version     string               `json:"version"`
	Config      json.RawMessage      `json:"config"`
	Credentials []snapshotCredential `json:"credentials"`
}

type snapshotCredential struct {
	Name   string          `json:"name"`
	Fields json.RawMessage `json:"fields"`
}

type scriptedReply struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
	Chunks []string    `json:"chunks"`
}

type snapshotStep struct {
	RPC    snapshotRPC     `json:"rpc"`
	Output *snapshotOutput `json:"output"`
}

type snapshotRPC struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

type snapshotOutput struct {
	UpstreamRequests []recordedRequest  `json:"upstream_requests"`
	HostCalls        []recordedHostCall `json:"host_calls"`
	Response         json.RawMessage    `json:"response"`
}

type recordedRequest struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Query  string      `json:"query"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

type recordedHostCall struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

func TestSnapshots(t *testing.T) {
	if *updateSnapshots {
		seedMissingFixtures(t)
	}
	files, errGlob := filepath.Glob(filepath.Join(snapDir, "*.json"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	have := map[string]bool{}
	for _, file := range files {
		have[strings.TrimSuffix(filepath.Base(file), ".json")] = true
	}
	for _, def := range snapshotCases() {
		if !have[def.name] {
			t.Errorf("fixture %s.json is missing; run go test ./internal/plugin -run TestSnapshots -update", def.name)
		}
	}
	for _, file := range files {
		fixture := loadFixture(t, file)
		if want := strings.TrimSuffix(filepath.Base(file), ".json"); fixture.Case != want {
			t.Errorf("%s: case %q does not match the file name", file, fixture.Case)
		}
		t.Run(fixture.Case, func(t *testing.T) {
			t.Parallel()
			runFixture(t, &fixture)
			if *updateSnapshots && !t.Failed() {
				writeFixture(t, file, fixture)
			}
		})
	}
}

func loadFixture(t *testing.T, file string) snapshotFixture {
	t.Helper()
	raw, errRead := os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var fixture snapshotFixture
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if errDecode := dec.Decode(&fixture); errDecode != nil {
		t.Fatalf("%s: %v", file, errDecode)
	}
	return fixture
}

func writeFixture(t *testing.T, file string, fixture snapshotFixture) {
	t.Helper()
	if errWrite := os.WriteFile(file, encodeIndented(t, fixture), 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
}

// encodeIndented writes v with two-space indentation and without HTML
// escaping; embedded raw JSON is re-indented as part of the document.
func encodeIndented(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if errEncode := enc.Encode(v); errEncode != nil {
		t.Fatalf("encode: %v", errEncode)
	}
	return buf.Bytes()
}

// snapshotRun is one fixture's plugin instance with its fake host and
// scripted upstream.
type snapshotRun struct {
	t        *testing.T
	svc      *Service
	host     *fakeHost
	upstream *scriptedUpstream
	dataDir  string

	mu    sync.Mutex
	calls []rawHostCall
}

type rawHostCall struct {
	method  string
	request []byte
}

func runFixture(t *testing.T, fixture *snapshotFixture) {
	upstream := newScriptedUpstream(t, fixture.UpstreamScript)
	run := &snapshotRun{t: t, upstream: upstream, dataDir: t.TempDir()}
	run.host = newFakeHost(t)
	run.svc = New(fixture.Setup.Version, run.hostCall)
	run.host.svc = run.svc
	t.Cleanup(run.svc.Close)
	seamPrepare(run.svc, upstream.URL)

	for _, cred := range fixture.Setup.Credentials {
		node := run.inputNode(cred.Fields)
		fields := map[string]any{}
		dec := json.NewDecoder(bytes.NewReader(node.bytes()))
		dec.UseNumber()
		if errDecode := dec.Decode(&fields); errDecode != nil {
			t.Fatalf("credential %s: %v", cred.Name, errDecode)
		}
		provider, _ := fields["type"].(string)
		run.host.seed(cred.Name, provider, fields)
	}
	registerForm, errRegister := registerRequest(fixture.Setup.Config)
	if errRegister != nil {
		t.Fatalf("setup config: %v", errRegister)
	}
	register := run.inputNode(registerForm)
	encodeBytesFields(register)
	if envelope := decodeEnvelope(t, run.svc.Call(pluginabi.MethodPluginRegister, register.bytes())); !envelope.OK {
		t.Fatalf("setup plugin.register: %+v", envelope.Error)
	}
	if !seamWaitBackground(run.svc, snapBackgroundTimeout) {
		t.Fatal("setup: background work did not finish")
	}
	if requests, unmatched := upstream.take(); len(requests) > 0 || len(unmatched) > 0 {
		t.Fatalf("setup made upstream requests: %d (unmatched %v)", len(requests), unmatched)
	}
	run.takeCalls()

	for i := range fixture.Steps {
		step := &fixture.Steps[i]
		got := run.runStep(step.RPC)
		label := fmt.Sprintf("step %d (%s)", i, step.RPC.Method)
		if *updateSnapshots {
			step.Output = got
			continue
		}
		if step.Output == nil {
			t.Errorf("%s has no recorded output; run with -update", label)
			continue
		}
		want, have := encodeIndented(t, step.Output), encodeIndented(t, got)
		if !bytes.Equal(want, have) {
			t.Errorf("%s differs from the fixture (want vs got):\n%s", label, snapDiff(string(want), string(have)))
		}
	}
	if unused := upstream.unused(); len(unused) > 0 {
		t.Errorf("upstream replies never requested: %v", unused)
	}
}

// registerRequest is the plugin.register request CPA v8 sends, with the
// plugin config as YAML, in fixture form (placeholders unresolved).
func registerRequest(config json.RawMessage) (json.RawMessage, error) {
	var cfg map[string]any
	if len(bytes.TrimSpace(config)) > 0 {
		if errUnmarshal := json.Unmarshal(config, &cfg); errUnmarshal != nil {
			return nil, errUnmarshal
		}
	}
	configYAML := ""
	if len(cfg) > 0 {
		raw, errYAML := yaml.Marshal(cfg)
		if errYAML != nil {
			return nil, errYAML
		}
		configYAML = string(raw)
	}
	node := &jnode{kind: '{'}
	node.set("config_yaml", &jnode{kind: 's', str: configYAML})
	node.set("schema_version", &jnode{kind: 'n', str: fmt.Sprint(pluginabi.SchemaVersion)})
	node.set("multi_auth_providers", &jnode{kind: 'n', str: "true"})
	return node.bytes(), nil
}

// inputNode parses fixture input JSON and resolves its placeholders.
func (r *snapshotRun) inputNode(raw json.RawMessage) *jnode {
	r.t.Helper()
	node, errParse := parseJSON(raw)
	if errParse != nil {
		r.t.Fatalf("parse input %s: %v", raw, errParse)
	}
	node.mapStrings(func(s string) string {
		s = strings.ReplaceAll(s, "{{UPSTREAM}}", r.upstream.URL)
		return strings.ReplaceAll(s, "{{DATA_DIR}}", r.dataDir)
	})
	return node
}

func (r *snapshotRun) hostCall(method string, request []byte) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, rawHostCall{method: method, request: bytes.Clone(request)})
	r.mu.Unlock()
	return r.host.call(method, request)
}

func (r *snapshotRun) takeCalls() []rawHostCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

// runStep sends one RPC and captures everything it caused.
func (r *snapshotRun) runStep(rpc snapshotRPC) *snapshotOutput {
	var request []byte
	if trimmed := bytes.TrimSpace(rpc.Request); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		node := r.inputNode(rpc.Request)
		encodeBytesFields(node)
		request = node.bytes()
	}
	var response []byte
	if rpc.Method == seamRefreshTickMethod {
		response = seamRefreshTick(r.svc)
	} else {
		response = r.svc.Call(rpc.Method, request)
	}
	if !seamWaitBackground(r.svc, snapBackgroundTimeout) {
		r.t.Fatalf("%s: stream pumps did not finish", rpc.Method)
	}
	r.drainClosed()

	out := &snapshotOutput{UpstreamRequests: []recordedRequest{}, HostCalls: []recordedHostCall{}}
	requests, unmatched := r.upstream.take()
	for _, req := range requests {
		out.UpstreamRequests = append(out.UpstreamRequests, r.normaliseRequest(req))
	}
	if len(unmatched) > 0 {
		r.t.Errorf("%s: unscripted upstream requests: %v", rpc.Method, unmatched)
	}
	for _, call := range r.takeCalls() {
		out.HostCalls = append(out.HostCalls, recordedHostCall{Method: call.method, Request: r.normaliseJSON(call.request)})
	}
	out.Response = r.normaliseJSON(response)
	return out
}

// drainClosed empties the fake host's stream close notifications so its
// buffered channel never fills up.
func (r *snapshotRun) drainClosed() {
	for {
		select {
		case <-r.host.closed:
		default:
			return
		}
	}
}

func (r *snapshotRun) normaliseString(s string) string {
	s = strings.ReplaceAll(s, r.upstream.URL, "{{UPSTREAM}}")
	s = strings.ReplaceAll(s, strings.TrimPrefix(r.upstream.URL, "http://"), "{{UPSTREAM}}")
	return strings.ReplaceAll(s, r.dataDir, "{{DATA_DIR}}")
}

func (r *snapshotRun) normaliseRequest(req recordedRequest) recordedRequest {
	out := recordedRequest{
		Method: req.Method,
		Path:   r.normaliseString(req.Path),
		Query:  r.normaliseString(req.Query),
		Header: http.Header{},
		Body:   r.normaliseString(req.Body),
	}
	for key, values := range req.Header {
		for _, value := range values {
			out.Header[key] = append(out.Header[key], r.normaliseString(value))
		}
	}
	return out
}

// normaliseJSON turns a raw envelope or host callback request into its
// fixture form: decoded []byte fields, volatile values replaced.
func (r *snapshotRun) normaliseJSON(raw []byte) json.RawMessage {
	node, errParse := parseJSON(raw)
	if errParse != nil {
		node = &jnode{kind: 's', str: string(raw)}
	}
	decodeBytesFields(node)
	normaliseVolatile(node, false)
	node.mapStrings(r.normaliseString)
	return node.bytes()
}

// normaliseVolatile replaces volatile values by field name, except inside
// executor payloads.
func normaliseVolatile(node *jnode, inPayload bool) {
	switch node.kind {
	case '{':
		for i, key := range node.keys {
			child := node.kids[i]
			if !inPayload && child.scalar() && child.str != "null" {
				if snapVolatileFields[key] || (key == "id" && child.kind == 's' && snapRandomProxyID.MatchString(child.str)) {
					node.kids[i] = &jnode{kind: 's', str: snapVolatile}
					continue
				}
			}
			normaliseVolatile(child, inPayload || snapPayloadFields[key])
		}
	case '[':
		for _, child := range node.kids {
			normaliseVolatile(child, inPayload)
		}
	}
}

// decodeBytesFields replaces base64 []byte fields by their decoded form.
// Decoded values are not descended into.
func decodeBytesFields(node *jnode) {
	switch node.kind {
	case '{':
		for i, key := range node.keys {
			child := node.kids[i]
			if snapBytesFields[key] && child.kind == 's' {
				if decoded, errDecode := base64.StdEncoding.DecodeString(child.str); errDecode == nil {
					if replacement := bytesNode(decoded); replacement != nil {
						node.kids[i] = replacement
					}
					continue
				}
			}
			decodeBytesFields(child)
		}
	case '[':
		for _, child := range node.kids {
			decodeBytesFields(child)
		}
	}
}

// bytesNode is the fixture form of raw bytes: embedded JSON when they are an
// object or array that re-encodes byte for byte, else a string; nil when
// they are not text.
func bytesNode(raw []byte) *jnode {
	if inner, errParse := parseJSON(raw); errParse == nil && (inner.kind == '{' || inner.kind == '[') && bytes.Equal(inner.bytes(), raw) {
		return inner
	}
	if utf8.Valid(raw) {
		return &jnode{kind: 's', str: string(raw)}
	}
	return nil
}

// encodeBytesFields is the inverse of decodeBytesFields for inputs.
func encodeBytesFields(node *jnode) {
	switch node.kind {
	case '{':
		for i, key := range node.keys {
			child := node.kids[i]
			if snapBytesFields[key] {
				switch child.kind {
				case '{', '[':
					node.kids[i] = &jnode{kind: 's', str: base64.StdEncoding.EncodeToString(child.bytes())}
					continue
				case 's':
					node.kids[i] = &jnode{kind: 's', str: base64.StdEncoding.EncodeToString([]byte(child.str))}
					continue
				}
			}
			encodeBytesFields(child)
		}
	case '[':
		for _, child := range node.kids {
			encodeBytesFields(child)
		}
	}
}

// scriptedUpstream serves the fixture's upstream script and records every
// request it receives.
type scriptedUpstream struct {
	*httptest.Server

	mu        sync.Mutex
	script    []scriptedReply
	used      []bool
	requests  []recordedRequest
	unmatched []string
}

func newScriptedUpstream(t *testing.T, script []scriptedReply) *scriptedUpstream {
	t.Helper()
	u := &scriptedUpstream{script: script, used: make([]bool, len(script))}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.Close)
	return u
}

func (u *scriptedUpstream) serve(rw http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.requests = append(u.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(body)})
	index := -1
	for i := range u.script {
		if !u.used[i] && u.script[i].Method == r.Method && u.script[i].Path == r.URL.Path {
			index = i
			u.used[i] = true
			break
		}
	}
	if index < 0 {
		u.unmatched = append(u.unmatched, r.Method+" "+r.URL.RequestURI())
	}
	u.mu.Unlock()
	if index < 0 {
		http.Error(rw, "unscripted upstream request", 599)
		return
	}
	reply := u.script[index]
	for key, values := range reply.Header {
		rw.Header()[key] = append([]string(nil), values...)
	}
	status := reply.Status
	if status == 0 {
		status = http.StatusOK
	}
	if len(reply.Chunks) == 0 {
		time.Sleep(snapScriptLatency)
		rw.WriteHeader(status)
		_, _ = io.WriteString(rw, reply.Body)
		return
	}
	flusher, _ := rw.(http.Flusher)
	rw.WriteHeader(status)
	flusher.Flush()
	for i, chunk := range reply.Chunks {
		if i == len(reply.Chunks)-1 {
			time.Sleep(snapScriptLatency)
		}
		_, _ = io.WriteString(rw, chunk)
		flusher.Flush()
	}
}

// take returns and forgets the requests received so far.
func (u *scriptedUpstream) take() ([]recordedRequest, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	requests, unmatched := u.requests, u.unmatched
	u.requests, u.unmatched = nil, nil
	return requests, unmatched
}

func (u *scriptedUpstream) unused() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for i, reply := range u.script {
		if !u.used[i] {
			out = append(out, fmt.Sprintf("#%d %s %s", i, reply.Method, reply.Path))
		}
	}
	return out
}

// snapDiff shows the first difference between two fixture renderings.
func snapDiff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	first := 0
	for first < len(wantLines) && first < len(gotLines) && wantLines[first] == gotLines[first] {
		first++
	}
	window := func(lines []string) string {
		from, to := max(first-3, 0), min(first+12, len(lines))
		var b strings.Builder
		for i := from; i < to; i++ {
			marker := "  "
			if i == first {
				marker = "> "
			}
			fmt.Fprintf(&b, "%s%4d %s\n", marker, i+1, lines[i])
		}
		return b.String()
	}
	return "--- want\n" + window(wantLines) + "--- got\n" + window(gotLines)
}

// jnode is a JSON value that keeps object member order and number literals,
// so fixtures can be rewritten without reordering or reformatting values.
type jnode struct {
	// kind is '{', '[', 's' (string) or 'n' (number, true, false, null).
	kind byte
	keys []string
	kids []*jnode
	// str is a string's value or a literal's text.
	str string
}

func parseJSON(raw []byte) (*jnode, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	node, errParse := parseJSONValue(dec)
	if errParse != nil {
		return nil, errParse
	}
	if _, errMore := dec.Token(); errMore != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return node, nil
}

func parseJSONValue(dec *json.Decoder) (*jnode, error) {
	token, errToken := dec.Token()
	if errToken != nil {
		return nil, errToken
	}
	switch value := token.(type) {
	case json.Delim:
		node := &jnode{kind: byte(value)}
		for dec.More() {
			if value == '{' {
				keyToken, errKey := dec.Token()
				if errKey != nil {
					return nil, errKey
				}
				key, _ := keyToken.(string)
				node.keys = append(node.keys, key)
			}
			child, errChild := parseJSONValue(dec)
			if errChild != nil {
				return nil, errChild
			}
			node.kids = append(node.kids, child)
		}
		if _, errEnd := dec.Token(); errEnd != nil {
			return nil, errEnd
		}
		return node, nil
	case string:
		return &jnode{kind: 's', str: value}, nil
	case json.Number:
		return &jnode{kind: 'n', str: value.String()}, nil
	case bool:
		return &jnode{kind: 'n', str: fmt.Sprint(value)}, nil
	case nil:
		return &jnode{kind: 'n', str: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}

func (n *jnode) scalar() bool { return n.kind == 's' || n.kind == 'n' }

func (n *jnode) set(key string, value *jnode) {
	for i, existing := range n.keys {
		if existing == key {
			n.kids[i] = value
			return
		}
	}
	n.keys = append(n.keys, key)
	n.kids = append(n.kids, value)
}

func (n *jnode) mapStrings(fn func(string) string) {
	switch n.kind {
	case 's':
		n.str = fn(n.str)
	case '{', '[':
		for _, child := range n.kids {
			child.mapStrings(fn)
		}
	}
}

// bytes encodes the node compactly, without HTML escaping.
func (n *jnode) bytes() []byte {
	var buf bytes.Buffer
	n.encode(&buf)
	return buf.Bytes()
}

func (n *jnode) encode(buf *bytes.Buffer) {
	switch n.kind {
	case '{', '[':
		closing := byte('}')
		if n.kind == '[' {
			closing = ']'
		}
		buf.WriteByte(n.kind)
		for i, child := range n.kids {
			if i > 0 {
				buf.WriteByte(',')
			}
			if n.kind == '{' {
				buf.Write(quoteJSON(n.keys[i]))
				buf.WriteByte(':')
			}
			child.encode(buf)
		}
		buf.WriteByte(closing)
	case 's':
		buf.Write(quoteJSON(n.str))
	default:
		buf.WriteString(n.str)
	}
}

func quoteJSON(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// seedMissingFixtures writes the inputs of every case in snapshotCases that
// has no fixture yet; the update run then records its outputs.
func seedMissingFixtures(t *testing.T) {
	t.Helper()
	if errDir := os.MkdirAll(snapDir, 0o755); errDir != nil {
		t.Fatal(errDir)
	}
	for _, def := range snapshotCases() {
		file := filepath.Join(snapDir, def.name+".json")
		if _, errStat := os.Stat(file); errStat == nil {
			continue
		}
		writeFixture(t, file, def.fixture(t))
	}
}
