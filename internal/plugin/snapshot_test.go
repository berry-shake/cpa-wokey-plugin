package plugin

// TestSnapshots locks the plugin's behaviour at its RPC boundary. Every
// fixture in testdata/snapshots is replayed through Service.Call against a
// fake CPA host and a scripted httptest upstream (snapshot_host_test.go), and
// every upstream request, host callback and RPC response is compared with
// the fixture. The format is shared by the provider plugins and
// cpa-provider-core's coretest package; see its README.
//
//	go test ./internal/plugin -run TestSnapshots           # compare
//	go test ./internal/plugin -run TestSnapshots -update   # rewrite outputs
//
// Fixtures are the source of truth: -update rewrites only steps[].output
// (and writes a skeleton for a case of snapshotSkeletons that has no fixture
// yet); setup, upstream_script and steps[].rpc are never touched.
//
// Setup (not recorded): a Service is built with setup.version and the fake
// host, its clock pinned and its refresh loop held back
// (snapshot_seams_test.go); plugin.register is called with setup.config as
// YAML (the subtree CPA passes) and schema_version pluginabi.SchemaVersion;
// the catalog refresh register queues is dropped, so every case starts with
// an empty catalog; setup.credentials are put in the fake host's auth
// directory without any RPC (auth_index idx-1, idx-2, ... in order). Setup
// must not reach the upstream.
//
// Steps: each rpc is sent with Service.Call (through snapshotCall, which
// also serves the binding's pseudo-methods; see snapshot_seams_test.go).
// The runner then waits for background stream pumps to finish and, when the
// RPC queued a catalog refresh (plugin.register / plugin.reconfigure), runs
// it synchronously before capturing the step's output, or drops it when
// setup.drop_queued_refresh is set.
//
// Upstream script: replies are matched in order per method+path (the query
// is ignored); an unscripted request and a reply nobody asked for both fail
// the test.
//
// Representation (lossless, the same for inputs and outputs):
//   - Fields CPA carries as []byte, base64 in the RPC JSON (Payload,
//     OriginalRequest, StorageJSON, storage_json, RawJSON, config_yaml, Body,
//     ResponseBody, payload), are shown decoded: as a JSON object or array
//     when the bytes are JSON that re-encodes byte for byte with
//     encoding/json, otherwise as a string of the raw text. The runner
//     re-encodes them before calling the plugin.
//   - A response or host-call request that is not JSON, or does not
//     re-encode byte for byte, is kept as a raw string.
//   - Upstream request headers are exactly what the httptest server received
//     (canonical keys, sorted); bodies are exact raw strings.
//
// Normalisation (the only values that may differ between runs):
//   - The httptest upstream URL is written as {{UPSTREAM}}, its bare
//     host:port as {{UPSTREAM_HOST}} and the per-run data directory as
//     {{DATA_DIR}}, in inputs and outputs alike.
//   - In management.handle responses: time, duration_ms, ttft_ms,
//     last_frame_ms (request log wall clock and timers), created_at,
//     proxy_id and the id of a proxy object (under "proxy" or "proxies")
//     (proxy store clock and random IDs) and latency_ms (egress check) are
//     replaced with "<volatile>" by name. output_tokens_per_second and
//     e2e_tokens_per_second are removed from request log entries (objects
//     with a duration_ms member; an entry only has them when its call took
//     over 50 ms) and replaced with "<volatile>" elsewhere (the log
//     summary's medians). Inside strings the same names are replaced
//     textually.
//   - A run of consecutive upstream requests to one of
//     snapshotConcurrentPaths (made concurrently by the plugin) is sorted by
//     body.
//   - Nothing else: the service clock is pinned to snapshotClock, so status,
//     catalog, account and auth refresh times are compared exactly.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"gopkg.in/yaml.v3"
)

var updateSnapshots = flag.Bool("update", false, "rewrite the outputs of the RPC snapshot fixtures")

const snapshotDir = "testdata/snapshots"

// snapshotClock is the time the service clock is pinned to. It shows up as
// the status "now", catalog and account fetch times and the auth refresh
// horizon, and it keeps account caches from expiring inside a case.
var snapshotClock = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type snapshotFixture struct {
	Case           string          `json:"case"`
	Description    string          `json:"description"`
	Setup          snapshotSetup   `json:"setup"`
	UpstreamScript []snapshotReply `json:"upstream_script"`
	Steps          []snapshotStep  `json:"steps"`
}

type snapshotSetup struct {
	Version     string               `json:"version"`
	Config      json.RawMessage      `json:"config"`
	Credentials []snapshotCredential `json:"credentials"`
	// DropQueuedRefresh drops the catalog refresh a step queues instead of
	// running it after the step.
	DropQueuedRefresh bool `json:"drop_queued_refresh,omitempty"`
}

type snapshotCredential struct {
	Name   string          `json:"name"`
	Fields json.RawMessage `json:"fields"`
}

type snapshotStep struct {
	RPC    snapshotRPC     `json:"rpc"`
	Output json.RawMessage `json:"output"`
}

type snapshotRPC struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

type snapshotOutput struct {
	UpstreamRequests []snapshotUpstreamRequest `json:"upstream_requests"`
	HostCalls        []snapshotHostCall        `json:"host_calls"`
	Response         json.RawMessage           `json:"response"`
}

type snapshotHostCall struct {
	Method  string          `json:"method"`
	Request json.RawMessage `json:"request"`
}

// cpaLifecycleRequest mirrors CPA's rpcLifecycleRequest.
type cpaLifecycleRequest struct {
	ConfigYAML         []byte `json:"config_yaml"`
	SchemaVersion      uint32 `json:"schema_version"`
	MultiAuthProviders bool   `json:"multi_auth_providers,omitempty"`
}

func TestSnapshots(t *testing.T) {
	if *updateSnapshots {
		writeMissingSnapshots(t)
	}
	paths, errGlob := filepath.Glob(filepath.Join(snapshotDir, "*.json"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("no snapshot fixtures; run go test ./internal/plugin -run TestSnapshots -update")
	}
	seen := map[string]bool{}
	for _, file := range paths {
		fixture := loadSnapshot(t, file)
		if want := strings.TrimSuffix(filepath.Base(file), ".json"); fixture.Case != want {
			t.Errorf("%s: case is %q, want %q", file, fixture.Case, want)
		}
		seen[fixture.Case] = true
		t.Run(fixture.Case, func(t *testing.T) { runSnapshot(t, file, fixture) })
	}
	for _, fixture := range snapshotSkeletons(t) {
		if !seen[fixture.Case] {
			t.Errorf("case %s has no fixture; run go test ./internal/plugin -run TestSnapshots -update", fixture.Case)
		}
	}
}

func loadSnapshot(t *testing.T, file string) snapshotFixture {
	t.Helper()
	raw, errRead := os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var fixture snapshotFixture
	if errUnmarshal := json.Unmarshal(raw, &fixture); errUnmarshal != nil {
		t.Fatalf("%s: %v", file, errUnmarshal)
	}
	return fixture
}

// writeMissingSnapshots writes a skeleton for every case without a fixture.
func writeMissingSnapshots(t *testing.T) {
	t.Helper()
	if errDir := os.MkdirAll(snapshotDir, 0o755); errDir != nil {
		t.Fatal(errDir)
	}
	seen := map[string]bool{}
	for _, fixture := range snapshotSkeletons(t) {
		if seen[fixture.Case] {
			t.Fatalf("case %s is defined twice", fixture.Case)
		}
		seen[fixture.Case] = true
		file := filepath.Join(snapshotDir, fixture.Case+".json")
		if _, errStat := os.Stat(file); errStat == nil {
			continue
		}
		writeSnapshot(t, file, fixture)
	}
}

func writeSnapshot(t *testing.T, file string, fixture snapshotFixture) {
	t.Helper()
	raw, errMarshal := json.Marshal(fixture)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	pretty := prettyJSON(t, raw)
	if current, errRead := os.ReadFile(file); errRead == nil && bytes.Equal(current, pretty) {
		return
	}
	if errWrite := os.WriteFile(file, pretty, 0o644); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func runSnapshot(t *testing.T, file string, fixture snapshotFixture) {
	run := newSnapshotRun(t, fixture)
	outputs := make([]json.RawMessage, len(fixture.Steps))
	for i, step := range fixture.Steps {
		outputs[i] = run.step(i, step.RPC)
	}
	for _, key := range run.upstream.leftover() {
		t.Errorf("scripted upstream reply for %s was never requested", key)
	}
	if *updateSnapshots {
		if t.Failed() {
			return
		}
		for i := range fixture.Steps {
			fixture.Steps[i].Output = outputs[i]
		}
		writeSnapshot(t, file, fixture)
		return
	}
	for i, step := range fixture.Steps {
		if isNullJSON(step.Output) {
			t.Errorf("step %d (%s) has no recorded output; run with -update", i, step.RPC.Method)
			continue
		}
		want, got := string(prettyJSON(t, step.Output)), string(prettyJSON(t, outputs[i]))
		if want != got {
			t.Errorf("step %d (%s) output differs (-fixture +actual):\n%s", i, step.RPC.Method, lineDiff(want, got))
		}
	}
}

// snapshotRun is one replay of a fixture.
type snapshotRun struct {
	t        *testing.T
	svc      *Service
	host     *snapshotHost
	upstream *snapshotUpstream
	drop     bool
	// toActual fills the placeholders of inputs; toPlaceholder puts them
	// back into outputs.
	toActual, toPlaceholder *strings.Replacer
}

func newSnapshotRun(t *testing.T, fixture snapshotFixture) *snapshotRun {
	t.Helper()
	dataDir := t.TempDir()
	upstream := newSnapshotUpstream(fixture.UpstreamScript)
	t.Cleanup(upstream.Close)
	host := &snapshotHost{providerID: snapshotProviderID}
	svc := snapshotNewService(fixture.Setup.Version, host.call, []string{upstream.URL + "/json"})
	host.plugin = svc
	t.Cleanup(svc.Close)

	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")
	replacements := []string{upstream.URL, "{{UPSTREAM}}", upstreamHost, "{{UPSTREAM_HOST}}", dataDir, "{{DATA_DIR}}"}
	if resolved, errResolve := filepath.EvalSymlinks(dataDir); errResolve == nil && resolved != dataDir {
		replacements = append(replacements, resolved, "{{DATA_DIR}}")
	}
	run := &snapshotRun{
		t:             t,
		svc:           svc,
		host:          host,
		upstream:      upstream,
		drop:          fixture.Setup.DropQueuedRefresh,
		toActual:      strings.NewReplacer("{{UPSTREAM}}", upstream.URL, "{{UPSTREAM_HOST}}", upstreamHost, "{{DATA_DIR}}", dataDir),
		toPlaceholder: strings.NewReplacer(replacements...),
	}

	config := run.configYAML(fixture.Setup.Config)
	register, _ := json.Marshal(cpaLifecycleRequest{ConfigYAML: config, SchemaVersion: pluginabi.SchemaVersion})
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(svc.Call(pluginabi.MethodPluginRegister, register), &envelope); errUnmarshal != nil || !envelope.OK {
		t.Fatalf("setup plugin.register failed: %+v %v", envelope.Error, errUnmarshal)
	}
	snapshotWaitIdle(svc)
	snapshotDropQueuedRefresh(svc)
	for _, cred := range fixture.Setup.Credentials {
		host.seed(cred.Name, run.compactInput(cred.Fields))
	}
	host.takeCalls()
	if requests, _ := upstream.take(); len(requests) > 0 {
		t.Fatalf("setup reached the upstream: %+v", requests)
	}
	return run
}

// configYAML turns the fixture's config object into the YAML CPA passes.
func (r *snapshotRun) configYAML(raw json.RawMessage) []byte {
	if isNullJSON(raw) {
		return nil
	}
	var config map[string]any
	if errUnmarshal := json.Unmarshal([]byte(r.toActual.Replace(string(raw))), &config); errUnmarshal != nil {
		r.t.Fatalf("setup.config: %v", errUnmarshal)
	}
	out, errMarshal := yaml.Marshal(config)
	if errMarshal != nil {
		r.t.Fatalf("setup.config: %v", errMarshal)
	}
	return out
}

// compactInput fills the placeholders of a JSON input and compacts it,
// keeping its key order.
func (r *snapshotRun) compactInput(raw json.RawMessage) []byte {
	tree, errDecode := decodeOrdered([]byte(r.toActual.Replace(string(raw))))
	if errDecode != nil {
		r.t.Fatalf("decode input: %v", errDecode)
	}
	return marshalTree(tree, true)
}

func (r *snapshotRun) step(index int, rpc snapshotRPC) json.RawMessage {
	r.t.Helper()
	var request []byte
	if !isNullJSON(rpc.Request) {
		tree, errDecode := decodeOrdered([]byte(r.toActual.Replace(string(rpc.Request))))
		if errDecode != nil {
			r.t.Fatalf("step %d: decode request: %v", index, errDecode)
		}
		request = marshalTree(packByteFields(tree, ""), true)
	}
	response := snapshotCall(r.svc, rpc.Method, request)
	snapshotWaitIdle(r.svc)
	if r.drop {
		snapshotDropQueuedRefresh(r.svc)
	} else if snapshotRunQueuedRefresh(r.svc) {
		snapshotWaitIdle(r.svc)
	}

	requests, unmatched := r.upstream.take()
	for _, key := range unmatched {
		r.t.Errorf("step %d (%s): unscripted upstream request %s", index, rpc.Method, key)
	}
	out := snapshotOutput{UpstreamRequests: []snapshotUpstreamRequest{}, HostCalls: []snapshotHostCall{}}
	for _, req := range requests {
		header := http.Header{}
		for name, values := range req.Header {
			for _, value := range values {
				header[name] = append(header[name], r.toPlaceholder.Replace(value))
			}
		}
		req.Header = header
		req.Query = r.toPlaceholder.Replace(req.Query)
		req.Body = r.toPlaceholder.Replace(req.Body)
		out.UpstreamRequests = append(out.UpstreamRequests, req)
	}
	sortConcurrentRequests(out.UpstreamRequests)
	for _, call := range r.host.takeCalls() {
		out.HostCalls = append(out.HostCalls, snapshotHostCall{Method: call.method, Request: r.canonical(call.request, false)})
	}
	out.Response = r.canonical(response, rpc.Method == pluginabi.MethodManagementHandle)
	raw, errMarshal := json.Marshal(out)
	if errMarshal != nil {
		r.t.Fatal(errMarshal)
	}
	return raw
}

// sortConcurrentRequests orders each run of consecutive requests to one of
// snapshotConcurrentPaths by body; the plugin makes them concurrently.
func sortConcurrentRequests(requests []snapshotUpstreamRequest) {
	for i := 0; i < len(requests); {
		j := i
		for j < len(requests) && requests[j].Method == requests[i].Method && requests[j].Path == requests[i].Path && slices.Contains(snapshotConcurrentPaths, requests[i].Method+" "+requests[i].Path) {
			j++
		}
		if j-i > 1 {
			run := requests[i:j]
			sort.SliceStable(run, func(a, b int) bool { return run[a].Body < run[b].Body })
		}
		i = max(j, i+1)
	}
}

// canonical renders an RPC response or host-call request for the fixture.
func (r *snapshotRun) canonical(raw []byte, management bool) json.RawMessage {
	value := unpackByteFields(losslessJSON(raw), "")
	value = r.placeholders(value)
	if management {
		value = normaliseVolatile(value, "")
	}
	return marshalTree(value, true)
}

func (r *snapshotRun) placeholders(value any) any {
	switch typed := value.(type) {
	case string:
		return r.toPlaceholder.Replace(typed)
	case []any:
		for i := range typed {
			typed[i] = r.placeholders(typed[i])
		}
	case *orderedObject:
		for _, key := range typed.keys {
			typed.values[key] = r.placeholders(typed.values[key])
		}
	}
	return value
}

// byteFields are the members CPA's RPC types carry as []byte.
var byteFields = map[string]bool{
	"Payload": true, "OriginalRequest": true, "StorageJSON": true, "storage_json": true,
	"RawJSON": true, "config_yaml": true, "Body": true, "ResponseBody": true, "payload": true,
}

// unpackByteFields decodes the base64 of byte fields. It does not descend
// into the decoded content.
func unpackByteFields(value any, key string) any {
	switch typed := value.(type) {
	case string:
		if !byteFields[key] {
			return typed
		}
		decoded, errDecode := base64.StdEncoding.DecodeString(typed)
		if errDecode != nil || !utf8.Valid(decoded) {
			return typed
		}
		switch tree := losslessJSON(decoded).(type) {
		case *orderedObject, []any:
			return tree
		}
		return string(decoded)
	case []any:
		for i := range typed {
			typed[i] = unpackByteFields(typed[i], key)
		}
	case *orderedObject:
		for _, member := range typed.keys {
			typed.values[member] = unpackByteFields(typed.values[member], member)
		}
	}
	return value
}

// packByteFields is the inverse of unpackByteFields for fixture inputs.
func packByteFields(value any, key string) any {
	if byteFields[key] {
		switch typed := value.(type) {
		case string:
			return base64.StdEncoding.EncodeToString([]byte(typed))
		case *orderedObject, []any:
			return base64.StdEncoding.EncodeToString(marshalTree(typed, true))
		}
		return value
	}
	switch typed := value.(type) {
	case []any:
		for i := range typed {
			typed[i] = packByteFields(typed[i], key)
		}
	case *orderedObject:
		for _, member := range typed.keys {
			typed.values[member] = packByteFields(typed.values[member], member)
		}
	}
	return value
}

// volatileFields are replaced in management responses; see the top comment.
var volatileFields = map[string]bool{
	"time": true, "duration_ms": true, "ttft_ms": true, "last_frame_ms": true,
	"output_tokens_per_second": true, "e2e_tokens_per_second": true,
	"created_at": true, "proxy_id": true, "latency_ms": true,
}

// speedFields are dropped from request log entries.
var speedFields = map[string]bool{"output_tokens_per_second": true, "e2e_tokens_per_second": true}

var volatileText = regexp.MustCompile(`"(time|duration_ms|ttft_ms|last_frame_ms|output_tokens_per_second|e2e_tokens_per_second|created_at|proxy_id|latency_ms)":("(?:[^"\\]|\\.)*"|[^,}\]]*)`)

const volatileMarker = "<volatile>"

func normaliseVolatile(value any, parent string) any {
	switch typed := value.(type) {
	case string:
		return volatileText.ReplaceAllString(typed, `"$1":"`+volatileMarker+`"`)
	case []any:
		for i := range typed {
			typed[i] = normaliseVolatile(typed[i], parent+"[]")
		}
	case *orderedObject:
		if _, logEntry := typed.values["duration_ms"]; logEntry {
			typed.remove(speedFields)
		}
		for _, key := range typed.keys {
			switch {
			case volatileFields[key]:
				typed.values[key] = volatileMarker
			case key == "id" && (parent == "proxy" || parent == "proxies[]"):
				typed.values[key] = volatileMarker
			default:
				typed.values[key] = normaliseVolatile(typed.values[key], key)
			}
		}
	}
	return value
}

func isNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// snapshotCaseJSON renders a Go value (an RPC request as CPA builds it) as
// fixture JSON, byte fields decoded.
func snapshotCaseJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	if value == nil {
		return json.RawMessage("null")
	}
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	return marshalTree(unpackByteFields(tree, ""), true)
}

// orderedObject is a JSON object that keeps its member order.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: map[string]any{}}
}

func (o *orderedObject) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// remove deletes the members named in drop.
func (o *orderedObject) remove(drop map[string]bool) {
	keys := o.keys[:0]
	for _, key := range o.keys {
		if drop[key] {
			delete(o.values, key)
			continue
		}
		keys = append(keys, key)
	}
	o.keys = keys
}

func (o *orderedObject) MarshalJSON() ([]byte, error) {
	return marshalTree(o, true), nil
}

// decodeOrdered decodes JSON into nil, bool, json.Number, string, []any and
// *orderedObject values.
func decodeOrdered(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, errValue := decodeOrderedValue(decoder)
	if errValue != nil {
		return nil, errValue
	}
	if _, errEnd := decoder.Token(); !errors.Is(errEnd, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, errToken := decoder.Token()
	if errToken != nil {
		return nil, errToken
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		object := newOrderedObject()
		for decoder.More() {
			keyToken, errKey := decoder.Token()
			if errKey != nil {
				return nil, errKey
			}
			key, _ := keyToken.(string)
			value, errValue := decodeOrderedValue(decoder)
			if errValue != nil {
				return nil, errValue
			}
			object.set(key, value)
		}
		_, errEnd := decoder.Token()
		return object, errEnd
	case '[':
		array := []any{}
		for decoder.More() {
			value, errValue := decodeOrderedValue(decoder)
			if errValue != nil {
				return nil, errValue
			}
			array = append(array, value)
		}
		_, errEnd := decoder.Token()
		return array, errEnd
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// losslessJSON decodes raw when it re-encodes byte for byte and otherwise
// returns it as a string.
func losslessJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil || !bytes.Equal(marshalTree(tree, true), raw) {
		return string(raw)
	}
	return tree
}

// marshalTree encodes a decoded tree compactly. With escapeHTML it matches
// encoding/json.Marshal.
func marshalTree(value any, escapeHTML bool) []byte {
	var buf bytes.Buffer
	writeTree(&buf, value, escapeHTML)
	return buf.Bytes()
}

func writeTree(buf *bytes.Buffer, value any, escapeHTML bool) {
	switch typed := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if typed {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(typed.String())
	case string:
		writeJSONString(buf, typed, escapeHTML)
	case []any:
		buf.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeTree(buf, item, escapeHTML)
		}
		buf.WriteByte(']')
	case *orderedObject:
		buf.WriteByte('{')
		for i, key := range typed.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, key, escapeHTML)
			buf.WriteByte(':')
			writeTree(buf, typed.values[key], escapeHTML)
		}
		buf.WriteByte('}')
	default:
		raw, _ := json.Marshal(typed)
		buf.Write(raw)
	}
}

func writeJSONString(buf *bytes.Buffer, text string, escapeHTML bool) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	_ = encoder.Encode(text)
	buf.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
}

// prettyJSON renders JSON the way fixtures are stored: member order kept,
// two-space indent, no HTML escaping, trailing newline.
func prettyJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	if isNullJSON(raw) {
		return []byte("null\n")
	}
	tree, errDecode := decodeOrdered(raw)
	if errDecode != nil {
		t.Fatalf("decode fixture JSON: %v", errDecode)
	}
	var out bytes.Buffer
	if errIndent := json.Indent(&out, marshalTree(tree, false), "", "  "); errIndent != nil {
		t.Fatalf("indent fixture JSON: %v", errIndent)
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// lineDiff shows the differing region of two texts with some context.
func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	endA, endB := len(a)-1, len(b)-1
	for endA >= start && endB >= start && a[endA] == b[endB] {
		endA--
		endB--
	}
	const context, limit = 3, 120
	var out []string
	for i := max(0, start-context); i < start; i++ {
		out = append(out, "  "+a[i])
	}
	for i := start; i <= endA; i++ {
		out = append(out, "- "+a[i])
	}
	for i := start; i <= endB; i++ {
		out = append(out, "+ "+b[i])
	}
	for i := endA + 1; i < min(len(a), endA+1+context); i++ {
		out = append(out, "  "+a[i])
	}
	if len(out) > limit {
		out = append(out[:limit], fmt.Sprintf("... %d more lines", len(out)-limit))
	}
	return strings.Join(out, "\n")
}
