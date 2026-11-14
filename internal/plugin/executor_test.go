package plugin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestSetTopLevelString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"model":"wokey/a","messages":[{"model":"keep"}]}`, `{"model":"a","messages":[{"model":"keep"}]}`},
		{`{ "messages": [], "model" : "x", "n": 1.50 }`, `{ "messages": [], "model" : "a", "n": 1.50 }`},
		{`{"messages":[]}`, `{"model":"a","messages":[]}`},
		{`{}`, `{"model":"a"}`},
		{` { } `, ` {"model":"a" } `},
	}
	for _, tc := range cases {
		got, errSet := setTopLevelString([]byte(tc.in), "model", "a")
		if errSet != nil || string(got) != tc.want {
			t.Errorf("setTopLevelString(%s) = %s, %v; want %s", tc.in, got, errSet, tc.want)
		}
		if !json.Valid(got) {
			t.Errorf("invalid JSON: %s", got)
		}
	}
	for _, bad := range []string{``, `[]`, `"x"`, `{"model":}`} {
		if _, errSet := setTopLevelString([]byte(bad), "model", "a"); errSet == nil {
			t.Errorf("setTopLevelString(%q) accepted", bad)
		}
	}
}

func TestPrepareCallBody(t *testing.T) {
	svc, _ := newTestService(t, nil)
	cases := []struct {
		format, endpoint, in, want, effort string
		stream                             bool
	}{
		// A chat stream asks for the usage chunk unless the client set it.
		{"openai", wokey.EndpointChat, `{"model":"wokey/model-a","reasoning_effort":"low"}`, `{"stream_options":{"include_usage":true},"model":"model-a","reasoning_effort":"low"}`, "low", true},
		{"openai", wokey.EndpointChat, `{"model":"x","stream_options":{"include_usage":false}}`, `{"model":"model-a","stream_options":{"include_usage":false}}`, "", true},
		{"openai", wokey.EndpointChat, `{"model":"x"}`, `{"model":"model-a"}`, "", false},
		{"claude", wokey.EndpointMessages, `{"messages":[],"thinking":{"type":"enabled","budget_tokens":2048}}`, `{"model":"model-a","messages":[],"thinking":{"type":"enabled","budget_tokens":2048}}`, "budget 2048", true},
		// A broken tail after the model: the model is still rewritten, as
		// before, but nothing is inserted and no effort is read.
		{"openai", wokey.EndpointChat, `{"model":"x","reasoning_effort":"low",}`, `{"model":"model-a","reasoning_effort":"low",}`, "", true},
	}
	for _, tc := range cases {
		req := executorRequest(tc.format, "wokey/model-a", tc.in)
		req.Stream = tc.stream
		call, perr := svc.prepareCall(req, tc.endpoint)
		if perr != nil {
			t.Fatalf("%s: %+v", tc.in, perr)
		}
		if string(call.body) != tc.want || call.effort != tc.effort {
			t.Errorf("%s: body %s, effort %q; want %s, %q", tc.in, call.body, call.effort, tc.want, tc.effort)
		}
	}
	for _, bad := range []string{`[]`, `{"messages":[}`} {
		if _, perr := svc.prepareCall(executorRequest("openai", "wokey/model-a", bad), wokey.EndpointChat); perr == nil || perr.Code != "invalid_request" {
			t.Errorf("%s: %+v", bad, perr)
		}
	}
}

func executorRequest(format, model, payload string) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{
		AuthID:      "wokey-test.json",
		Model:       model,
		Format:      format,
		Payload:     []byte(payload),
		Headers:     http.Header{"Anthropic-Beta": []string{"tools-1"}},
		StorageJSON: []byte(`{"type":"wokey","api_key":"` + testGoodKey + `"}`),
	}
}

func TestExecuteRewritesModelAndForwards(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(wokey.EndpointChat, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("X-Request-Id", "req-1")
		rw.Header().Set("X-Internal", "secret")
		writeJSON(rw, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion", "choices": []any{map[string]any{"finish_reason": "stop"}}})
	}))
	svc, _ := newTestService(t, upstream)
	svc.reqlog.Configure("", 10, 0)

	req := executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","messages":[]}`)
	req.Headers.Set(cpaRequestIDHeader, "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	req.Headers.Set("User-Agent", "codex_cli_rs/0.50")
	req.Metadata = map[string]any{"request_path": "/v1/chat/completions", "selected_auth_index": "9d3c937e38fb865a"}
	envelope := decodeEnvelope(t, svc.execute(req))
	if !envelope.OK {
		t.Fatalf("execute failed: %+v", envelope.Error)
	}
	resp := mustDecode[pluginapi.ExecutorResponse](t, envelope.Result)
	if !strings.Contains(string(resp.Payload), `"chat.completion"`) {
		t.Fatalf("payload = %s", resp.Payload)
	}
	if resp.Headers.Get("X-Request-Id") != "req-1" || resp.Headers.Get("X-Internal") != "" {
		t.Fatalf("headers = %v", resp.Headers)
	}
	got := receive(t, seen)
	if got.Header.Get("Authorization") != "Bearer "+testGoodKey || got.Header.Get("Accept") != "application/json" || got.Body["model"] != "model-a" {
		t.Fatalf("upstream saw %+v", got)
	}
	if leaked := got.Header.Get(cpaRequestIDHeader); leaked != "" {
		t.Fatalf("the CPA request ID header reached Wokey: %q", leaked)
	}
	if beta := got.Header.Get("Anthropic-Beta"); beta != "" {
		t.Fatalf("Anthropic headers must not reach the OpenAI endpoints, got %q", beta)
	}
	if ua := got.Header.Get("User-Agent"); ua != "cpa-wokey-plugin/test" {
		t.Fatalf("User-Agent = %q", ua)
	}
	items := svc.reqlog.Query(requestlog.Query{}).Items
	if len(items) != 1 || items[0].Credential != credential.MaskKey(testGoodKey) || items[0].AuthID != "wokey-test.json" || items[0].Model != "wokey/model-a" || items[0].UpstreamModel != "model-a" || items[0].RequestID != "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatalf("log entries = %+v", items)
	}
	e := items[0]
	if e.UpstreamStatus != 200 || e.UpstreamRequestID != "req-1" || e.FinishReason != "stop" || e.ClientPath != "/v1/chat/completions" || e.UserAgent != "codex_cli_rs/0.50" || e.AuthIndex != "9d3c937e38fb865a" || e.Egress != "env" {
		t.Fatalf("log entry = %+v", e)
	}
	// No usage object came back, and buffered calls have no stream fields.
	if e.UsageReported == nil || *e.UsageReported || e.Frames != nil {
		t.Fatalf("usage and frames = %+v", e)
	}
}

func TestExecuteMessagesSetsAnthropicHeaders(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(wokey.EndpointMessages, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"type": "message"})
	}))
	svc, _ := newTestService(t, upstream)
	if envelope := decodeEnvelope(t, svc.execute(executorRequest("claude", "wokey/model-a", `{"model":"x"}`))); !envelope.OK {
		t.Fatalf("execute failed: %+v", envelope.Error)
	}
	got := receive(t, seen)
	if got.Header.Get("Anthropic-Beta") != "tools-1" || got.Header.Get("Anthropic-Version") != defaultAnthropicVersion || got.Body["model"] != "model-a" {
		t.Fatalf("upstream saw %+v", got)
	}
}

func TestExecuteErrors(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointResponses, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Retry-After", "3")
		writeJSON(rw, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": "rate_limited", "message": "slow"}})
	})
	upstream.handle(wokey.EndpointMessages, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, 529, map[string]any{"type": "error", "error": map[string]any{"type": "overloaded_error", "message": "busy"}})
	})
	svc, _ := newTestService(t, upstream)
	svc.reqlog.Configure("", 10, 0)

	envelope := decodeEnvelope(t, svc.execute(executorRequest("openai-response", "wokey/model-a", `{"model":"x"}`)))
	if envelope.OK || envelope.Error.HTTPStatus != 429 || envelope.Error.RetryAfterSeconds != 3 {
		t.Fatalf("429: %+v", envelope.Error)
	}
	if e := svc.reqlog.Query(requestlog.Query{}).Items[0]; e.UpstreamStatus != 429 || e.HTTPStatus != 429 || e.RetryAfter != 3 || e.UsageReported != nil {
		t.Fatalf("429 entry = %+v", e)
	}
	// CPA gets 503 for Wokey's 529; the log keeps both.
	envelope = decodeEnvelope(t, svc.execute(executorRequest("claude", "wokey/model-a", `{"model":"x"}`)))
	if envelope.OK || envelope.Error.HTTPStatus != 503 {
		t.Fatalf("529: %+v", envelope.Error)
	}
	if e := svc.reqlog.Query(requestlog.Query{}).Items[0]; e.UpstreamStatus != 529 || e.HTTPStatus != 503 || e.RetryAfter != 0 {
		t.Fatalf("529 entry = %+v", e)
	}

	envelope = decodeEnvelope(t, svc.execute(executorRequest("gemini", "wokey/model-a", `{}`)))
	if envelope.OK || envelope.Error.Code != "unsupported_format" {
		t.Fatalf("format: %+v", envelope.Error)
	}

	req := executorRequest("openai", "", `{"messages":[]}`)
	if envelope = decodeEnvelope(t, svc.execute(req)); envelope.OK || envelope.Error.HTTPStatus != 400 {
		t.Fatalf("no model: %+v", envelope.Error)
	}

	req = executorRequest("openai", "wokey/model-a", `{}`)
	req.StorageJSON = []byte(`{"type":"wokey"}`)
	if envelope = decodeEnvelope(t, svc.execute(req)); envelope.OK || envelope.Error.HTTPStatus != 401 {
		t.Fatalf("no key: %+v", envelope.Error)
	}

	req = executorRequest("openai", "wokey/model-a", `{}`)
	req.StorageJSON = []byte(`{"type":"wokey","api_key":"k","proxy_url":"ftp://bad"}`)
	if envelope = decodeEnvelope(t, svc.execute(req)); envelope.OK || envelope.Error.Code != "invalid_proxy" {
		t.Fatalf("bad proxy: %+v", envelope.Error)
	}

	// An unreachable upstream is a retryable 502.
	svc.cfg.BaseURL = "http://127.0.0.1:1"
	envelope = decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{}`)))
	if envelope.OK || envelope.Error.HTTPStatus != 502 || !envelope.Error.Retryable {
		t.Fatalf("unreachable: %+v", envelope.Error)
	}
}

// Wokey's count_tokens rejects every request, so the plugin answers 501
// without calling it.
func TestCountTokensUnsupported(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle("/v1/messages/count_tokens", observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"input_tokens": 12})
	}))
	svc, _ := newTestService(t, upstream)

	for _, format := range []string{"claude", "openai"} {
		request, _ := json.Marshal(rpcExecutorRequest{ExecutorRequest: executorRequest(format, "wokey/model-a", `{"model":"wokey/model-a"}`)})
		envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodExecutorCountTokens, request))
		if envelope.OK || envelope.Error.HTTPStatus != http.StatusNotImplemented || envelope.Error.Code != "unsupported_operation" {
			t.Fatalf("%s count_tokens: %+v", format, envelope.Error)
		}
	}
	select {
	case got := <-seen:
		t.Fatalf("count_tokens reached Wokey: %+v", got)
	default:
	}
	if page := svc.reqlog.Query(requestlog.Query{}); len(page.Items) != 0 {
		t.Fatalf("count_tokens was logged: %+v", page.Items)
	}
}

func TestCallDispatch(t *testing.T) {
	svc, _ := newTestService(t, nil)
	for _, method := range []string{pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier} {
		envelope := decodeEnvelope(t, svc.Call(method, []byte(`{}`)))
		if !envelope.OK || mustDecode[rpcIdentifierResponse](t, envelope.Result).Identifier != ProviderID {
			t.Errorf("%s: %+v", method, envelope)
		}
	}
	if envelope := decodeEnvelope(t, svc.Call("no.such.method", nil)); envelope.OK || envelope.Error.Code != "unknown_method" {
		t.Errorf("unknown method: %+v", envelope)
	}
	if envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodAuthLoginStart, []byte(`{}`))); envelope.OK || envelope.Error.HTTPStatus != 400 {
		t.Errorf("login.start: %+v", envelope)
	}
	if envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodExecutorExecute, []byte(`{bad`))); envelope.OK || envelope.Error.Code != "invalid_request" {
		t.Errorf("bad request: %+v", envelope)
	}

	raw, _ := json.Marshal(pluginapi.AuthParseRequest{FileName: "w.json", RawJSON: []byte(`{"type":"wokey","api_key":"k"}`), Host: pluginapi.HostConfigSummary{ProxyURL: "http://g.example:3128"}})
	envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodAuthParse, raw))
	if !envelope.OK || !mustDecode[pluginapi.AuthParseResponse](t, envelope.Result).Handled {
		t.Fatalf("auth.parse: %+v", envelope)
	}
	if svc.globalProxy() != "http://g.example:3128" {
		t.Fatalf("global proxy not remembered: %q", svc.globalProxy())
	}
}

func TestRegisterCapabilities(t *testing.T) {
	svc, _ := newTestService(t, nil)
	config := "models-refresh-minutes: 0\nbase-url: http://127.0.0.1:1\ndata-dir: " + strconv.Quote(t.TempDir()) + "\n"
	raw, _ := json.Marshal(rpcLifecycleRequest{ConfigYAML: []byte(config), SchemaVersion: 99})
	envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodPluginRegister, raw))
	if !envelope.OK {
		t.Fatalf("register: %+v", envelope.Error)
	}
	reg := mustDecode[rpcRegistration](t, envelope.Result)
	if reg.SchemaVersion != maxSchemaVersion {
		t.Errorf("schema = %d", reg.SchemaVersion)
	}
	caps := reg.Capabilities
	if !caps.AuthProvider || !caps.ModelProvider || !caps.Executor || !caps.ManagementAPI || !caps.QuotaProvider {
		t.Errorf("capabilities = %+v", caps)
	}
	if !caps.RequestInterceptor || !caps.RequestInterceptorOmitBodyBefore || !caps.RequestInterceptorOmitBodyAfter {
		t.Errorf("request interceptor = %+v", caps)
	}
	// A host that cannot leave out request bodies gets no interceptor.
	raw, _ = json.Marshal(rpcLifecycleRequest{ConfigYAML: []byte(config), SchemaVersion: pluginabi.SchemaVersionRawManagementResponse})
	if old := mustDecode[rpcRegistration](t, decodeEnvelope(t, svc.Call(pluginabi.MethodPluginRegister, raw)).Result); old.Capabilities.RequestInterceptor || old.SchemaVersion != pluginabi.SchemaVersionRawManagementResponse {
		t.Errorf("schema 6 registration = %+v", old)
	}
	if reg.Metadata.Logo != logoPath || len(reg.Metadata.ConfigFields) != 8 {
		t.Errorf("metadata = %+v", reg.Metadata)
	}
	if negotiateSchemaVersion(0) != 1 || negotiateSchemaVersion(2) != 2 {
		t.Error("schema negotiation")
	}
}

func TestCallDispatchRPCs(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, _ := newTestService(t, upstream)
	call := func(method string, req any) pluginabi.Envelope {
		t.Helper()
		raw, _ := json.Marshal(req)
		return decodeEnvelope(t, svc.Call(method, raw))
	}
	storage := []byte(`{"type":"wokey","api_key":"` + testGoodKey + `","models":["model-b"],"prefix":"team"}`)

	// Model IDs are bare; CPA adds the auth prefix itself.
	host := pluginapi.HostConfigSummary{ProxyURL: "http://global.example:3128", ForceModelPrefix: true}
	envelope := call(pluginabi.MethodModelForAuth, rpcAuthModelRequest{AuthModelRequest: pluginapi.AuthModelRequest{AuthID: "w.json", StorageJSON: storage, Host: host}})
	models := mustDecode[pluginapi.ModelResponse](t, envelope.Result)
	if !envelope.OK || models.Provider != ProviderID || len(models.Models) != 1 || models.Models[0].ID != "model-b" {
		t.Fatalf("model.for_auth: %+v %+v", envelope, models)
	}
	if force, known := svc.forceModelPrefix(); !force || !known || svc.globalProxy() != host.ProxyURL {
		t.Fatalf("host summary not remembered: force=%v known=%v proxy=%q", force, known, svc.globalProxy())
	}
	// Quota requests carry no host summary and must not clear it.
	_ = call(pluginabi.MethodQuotaFetch, rpcQuotaFetchRequest{QuotaFetchRequest: pluginapi.QuotaFetchRequest{StorageJSON: storage}})
	if svc.globalProxy() != host.ProxyURL {
		t.Fatalf("quota.fetch cleared the global proxy: %q", svc.globalProxy())
	}

	envelope = call(pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{FileName: "w.json", RawJSON: storage})
	if parsed := mustDecode[pluginapi.AuthParseResponse](t, envelope.Result); !envelope.OK || parsed.Auth.Prefix != "team" {
		t.Fatalf("auth.parse prefix: %+v", envelope)
	}
	if revision, _ := svc.registered.Load("w.json"); revision != svc.catalog.Current().Revision {
		t.Fatalf("registered revision = %v", revision)
	}

	envelope = call(pluginabi.MethodAuthRefresh, rpcAuthRefreshRequest{AuthRefreshRequest: pluginapi.AuthRefreshRequest{AuthID: "w.json", StorageJSON: storage, Metadata: map[string]any{"label": "meta"}}})
	refreshed := mustDecode[pluginapi.AuthRefreshResponse](t, envelope.Result)
	if !envelope.OK || refreshed.Auth.ID != "w.json" || refreshed.Auth.Label != "meta" || refreshed.Auth.Prefix != "team" || refreshed.NextRefreshAfter.IsZero() {
		t.Fatalf("auth.refresh: %+v %+v", envelope, refreshed.Auth)
	}
	if envelope = call(pluginabi.MethodAuthRefresh, rpcAuthRefreshRequest{AuthRefreshRequest: pluginapi.AuthRefreshRequest{StorageJSON: []byte(`{"type":"wokey"}`)}}); envelope.OK {
		t.Fatal("auth.refresh without api_key succeeded")
	}

	envelope = call(pluginabi.MethodManagementHandle, rpcManagementRequest{ManagementRequest: pluginapi.ManagementRequest{Method: http.MethodGet, Path: statusPath}})
	if managed := mustDecode[pluginapi.ManagementResponse](t, envelope.Result); !envelope.OK || managed.StatusCode != http.StatusOK {
		t.Fatalf("management.handle: %+v", envelope)
	}
	if envelope = call(pluginabi.MethodManagementRegister, struct{}{}); !envelope.OK || len(mustDecode[managementRegistration](t, envelope.Result).Routes) == 0 {
		t.Fatalf("management.register: %+v", envelope)
	}
	if envelope = call(pluginabi.MethodQuotaDescribe, struct{}{}); !envelope.OK {
		t.Fatalf("quota.describe: %+v", envelope)
	}
	for _, method := range []string{pluginabi.MethodQuotaReset, pluginabi.MethodExecutorHTTPRequest} {
		if envelope = call(method, struct{}{}); envelope.OK || envelope.Error.HTTPStatus != http.StatusNotImplemented {
			t.Errorf("%s: %+v", method, envelope)
		}
	}
	if envelope = call(pluginabi.MethodModelStatic, struct{}{}); !envelope.OK || len(mustDecode[pluginapi.ModelResponse](t, envelope.Result).Models) != 0 {
		t.Fatalf("model.static: %+v", envelope)
	}
}

func TestRequestInterceptorStampsWokeyRequests(t *testing.T) {
	svc, _ := newTestService(t, nil)
	call := func(method string, req any) pluginapi.RequestInterceptResponse {
		t.Helper()
		raw, _ := json.Marshal(req)
		envelope := decodeEnvelope(t, svc.Call(method, raw))
		if !envelope.OK {
			t.Fatalf("%s: %+v", method, envelope.Error)
		}
		return mustDecode[pluginapi.RequestInterceptResponse](t, envelope.Result)
	}
	intercept := func(authID, traceID string) pluginapi.RequestInterceptResponse {
		t.Helper()
		return call(pluginabi.MethodRequestInterceptAfter, rpcRequestInterceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID: "lifecycle-id",
			TraceID:   traceID,
			Headers:   http.Header{"Authorization": {"Bearer client"}},
			Metadata:  map[string]any{selectedAuthMetadataKey: authID, "requested_model": "wokey/model-a"},
		}})
	}

	if resp := call(pluginabi.MethodRequestInterceptBefore, rpcRequestInterceptRequest{}); len(resp.Headers) != 0 || len(resp.ClearHeaders) != 0 || resp.Terminate {
		t.Fatalf("before auth = %+v", resp)
	}
	// Until the auth is known as a Wokey credential, requests are left alone.
	if resp := intercept("w.json", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"); len(resp.Headers) != 0 || len(resp.ClearHeaders) != 0 {
		t.Fatalf("unknown auth = %+v", resp)
	}
	svc.modelsForAuth(pluginapi.AuthModelRequest{AuthID: "w.json", StorageJSON: []byte(`{"type":"wokey","api_key":"k"}`)})
	resp := intercept("w.json", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b")
	if got := resp.Headers.Values(cpaRequestIDHeader); len(got) != 1 || got[0] != "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" || len(resp.Headers) != 1 {
		t.Fatalf("wokey auth = %+v", resp)
	}
	// Without a trace ID a client-sent value is dropped instead of logged.
	if resp = intercept("w.json", ""); len(resp.Headers) != 0 || len(resp.ClearHeaders) != 1 || resp.ClearHeaders[0] != cpaRequestIDHeader {
		t.Fatalf("no trace ID = %+v", resp)
	}
	if resp = intercept("codex-a.json", "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"); len(resp.Headers) != 0 || len(resp.ClearHeaders) != 0 {
		t.Fatalf("other provider = %+v", resp)
	}
	// Executing on an auth also marks it, so a reloaded plugin catches up.
	svc.prepareCall(executorRequest("openai", "wokey/model-a", `{}`), wokey.EndpointChat)
	if !svc.isWokeyAuth("wokey-test.json") {
		t.Fatal("executor auth not remembered")
	}
	if envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodRequestInterceptAfter, []byte(`{bad`))); envelope.OK || envelope.Error.Code != "invalid_request" {
		t.Fatalf("bad request: %+v", envelope)
	}
}
