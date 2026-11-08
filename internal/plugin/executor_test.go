package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

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

func TestEnsureTopLevelObject(t *testing.T) {
	got := ensureTopLevelObject([]byte(`{"model":"a"}`), "stream_options", `{"include_usage":true}`)
	if string(got) != `{"stream_options":{"include_usage":true},"model":"a"}` {
		t.Fatalf("insert = %s", got)
	}
	existing := `{"stream_options":{"include_usage":false}}`
	if got = ensureTopLevelObject([]byte(existing), "stream_options", `{"include_usage":true}`); string(got) != existing {
		t.Fatalf("an existing member must be kept: %s", got)
	}
}

func TestEndpointForFormat(t *testing.T) {
	cases := map[string]string{
		"openai":           endpointChat,
		"chat-completions": endpointChat,
		"openai-response":  endpointResponses,
		"responses":        endpointResponses,
		"claude":           endpointMessages,
		"Anthropic":        endpointMessages,
	}
	for format, want := range cases {
		if got, ok := endpointForFormat(format); !ok || got != want {
			t.Errorf("endpointForFormat(%q) = %q, %v", format, got, ok)
		}
	}
	if _, ok := endpointForFormat("gemini"); ok {
		t.Error("gemini accepted")
	}
}

func TestUpstreamErrorMapping(t *testing.T) {
	retry := http.Header{"Retry-After": []string{"7"}}
	cases := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		endpoint  string
		want      int
		code      string
		retryable bool
		retryIn   int
	}{
		{"invalid key cools the key", 401, nil, `{"error":{"code":"invalid_api_key","message":"bad key"}}`, endpointChat, 401, "invalid_api_key", false, 0},
		{"no balance cools the key", 402, nil, `{"error":{"type":"insufficient_balance","message":"top up"}}`, endpointMessages, 402, "insufficient_balance", false, 0},
		{"blocked request is a client error", 403, nil, `{"error":{"code":"request_blocked","message":"blocked"}}`, endpointChat, 400, "request_blocked", false, 0},
		{"account 403 keeps its status", 403, nil, `{"error":{"code":"key_disabled","message":"disabled"}}`, endpointChat, 403, "key_disabled", false, 0},
		{"unknown model does not cool the key", 404, nil, `{"error":{"code":"model_not_found","message":"no model"}}`, endpointResponses, 400, "model_not_found", false, 0},
		{"count_tokens 404 stays 404", 404, nil, `{}`, endpointCountTokens, 404, "upstream_404", false, 0},
		{"rate limit keeps Retry-After", 429, retry, `{"code":"rate_limited","message":"slow down"}`, endpointChat, 429, "rate_limited", true, 7},
		{"overloaded is transient", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, endpointMessages, 503, "overloaded_error", true, 0},
		{"aborted downstream", 499, nil, ``, endpointChat, 408, "upstream_499", true, 0},
		{"plain text body", 500, nil, `boom`, endpointChat, 500, "upstream_500", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := tc.header
			if header == nil {
				header = http.Header{}
			}
			err := upstreamError(tc.status, header, []byte(tc.body), tc.endpoint)
			if err.HTTPStatus != tc.want || err.Code != tc.code || err.Retryable != tc.retryable || err.RetryAfterSeconds != tc.retryIn {
				t.Fatalf("got %+v", err)
			}
			if !strings.HasPrefix(err.Message, "wokey: ") {
				t.Fatalf("message = %q", err.Message)
			}
		})
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := retryAfterSeconds("12", now); got != 12 {
		t.Errorf("delta = %d", got)
	}
	if got := retryAfterSeconds(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90 {
		t.Errorf("date = %d", got)
	}
	for _, raw := range []string{"", "-3", "soon", now.Add(-time.Minute).Format(http.TimeFormat)} {
		if got := retryAfterSeconds(raw, now); got != 0 {
			t.Errorf("retryAfterSeconds(%q) = %d", raw, got)
		}
	}
}

func TestResolveProxy(t *testing.T) {
	cases := []struct {
		auth, global, key string
	}{
		{"", "", "env"},
		{"", "http://g.example:8080", "http://g.example:8080"},
		{"socks5://u:p@a.example:1080", "http://g.example:8080", "socks5://u:p@a.example:1080"},
		{"direct", "http://g.example:8080", "direct"},
		{"NONE", "", "direct"},
		{"", "direct", "direct"},
	}
	for _, tc := range cases {
		setting, errProxy := resolveProxy(tc.auth, tc.global)
		if errProxy != nil || setting.key != tc.key {
			t.Errorf("resolveProxy(%q, %q) = %+v, %v; want %s", tc.auth, tc.global, setting, errProxy, tc.key)
		}
	}
	if _, errProxy := resolveProxy("ftp://x.example", ""); errProxy == nil {
		t.Error("ftp proxy accepted")
	}
	for _, ok := range []string{"", "direct", "none", "http://h:1", "https://h:1", "socks5h://h:1"} {
		if errProxy := validateProxyValue(ok); errProxy != nil {
			t.Errorf("validateProxyValue(%q) = %v", ok, errProxy)
		}
	}
	for _, bad := range []string{"h:1080", "socks4://h:1", "http://"} {
		if errProxy := validateProxyValue(bad); errProxy == nil {
			t.Errorf("validateProxyValue(%q) accepted", bad)
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
	upstream.handle(endpointChat, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("X-Request-Id", "req-1")
		rw.Header().Set("X-Internal", "secret")
		writeJSON(rw, http.StatusOK, map[string]any{"id": "c1", "object": "chat.completion"})
	}))
	svc, _ := newTestService(t, upstream)

	envelope := decodeEnvelope(t, svc.execute(executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","messages":[]}`)))
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
	if beta := got.Header.Get("Anthropic-Beta"); beta != "" {
		t.Fatalf("Anthropic headers must not reach the OpenAI endpoints, got %q", beta)
	}
	if ua := got.Header.Get("User-Agent"); ua != "cpa-wokey-plugin/test" {
		t.Fatalf("User-Agent = %q", ua)
	}
}

func TestExecuteMessagesSetsAnthropicHeaders(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(endpointMessages, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
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
	upstream.handle(endpointResponses, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Retry-After", "3")
		writeJSON(rw, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": "rate_limited", "message": "slow"}})
	})
	svc, _ := newTestService(t, upstream)

	envelope := decodeEnvelope(t, svc.execute(executorRequest("openai-response", "wokey/model-a", `{"model":"x"}`)))
	if envelope.OK || envelope.Error.HTTPStatus != 429 || envelope.Error.RetryAfterSeconds != 3 {
		t.Fatalf("429: %+v", envelope.Error)
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

func TestCountTokens(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(endpointCountTokens, observe(seen, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"input_tokens": 12})
	}))
	svc, _ := newTestService(t, upstream)

	envelope := decodeEnvelope(t, svc.countTokens(executorRequest("claude", "wokey/model-a", `{"model":"wokey/model-a"}`)))
	if !envelope.OK {
		t.Fatalf("count_tokens: %+v", envelope.Error)
	}
	if got := receive(t, seen); got.Body["model"] != "model-a" || got.Header.Get("Accept") != "application/json" {
		t.Fatalf("upstream saw %+v", got)
	}
	envelope = decodeEnvelope(t, svc.countTokens(executorRequest("openai", "wokey/model-a", `{}`)))
	if envelope.OK || envelope.Error.HTTPStatus != http.StatusNotImplemented {
		t.Fatalf("openai count_tokens: %+v", envelope.Error)
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
	raw, _ := json.Marshal(rpcLifecycleRequest{ConfigYAML: []byte("models-refresh-minutes: 0\nbase-url: http://127.0.0.1:1\n"), SchemaVersion: 99})
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
	if reg.Metadata.Logo != logoPath || len(reg.Metadata.ConfigFields) != 5 {
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
	if revision, _ := svc.registered.Load("w.json"); revision != svc.catalog.current().Revision {
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
