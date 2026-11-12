package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func manage(t *testing.T, svc *Service, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := pluginapi.ManagementRequest{Method: method, Path: path, Body: []byte(body)}
	if base, rawQuery, ok := strings.Cut(path, "?"); ok {
		req.Path = base
		query, errQuery := url.ParseQuery(rawQuery)
		if errQuery != nil {
			t.Fatalf("query: %v", errQuery)
		}
		req.Query = query
	}
	resp := svc.handleManagement(req)
	var out map[string]any
	if len(resp.Body) > 0 && strings.HasPrefix(resp.Headers.Get("Content-Type"), "application/json") {
		if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
			t.Fatalf("decode %s %s: %v (%s)", method, path, errUnmarshal, resp.Body)
		}
	}
	return resp.StatusCode, out
}

func credentialOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	cred, ok := out["credential"].(map[string]any)
	if !ok {
		t.Fatalf("response has no credential: %v", out)
	}
	return cred
}

func stringList(raw any) []string {
	items, _ := raw.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestAddCredential(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)

	status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":" `+testGoodKey+` ","models":["model-b","ghost"]}`)
	if status != http.StatusCreated || out["ok"] != true {
		t.Fatalf("add: %d %v", status, out)
	}
	name := credential.FileNameForKey(testGoodKey)
	cred := credentialOf(t, out)
	// Wokey's own key label is not copied: the name defaults to the file name.
	if cred["name"] != name || cred["label"] != "" || cred["key_masked"] != credential.MaskKey(testGoodKey) {
		t.Fatalf("credential = %v", cred)
	}
	if got := stringList(out["unknown_models"]); !reflect.DeepEqual(got, []string{"ghost"}) {
		t.Fatalf("unknown_models = %v", got)
	}
	if cred["registered_models"] != float64(2) || cred["effective_prefix"] != "wokey" {
		t.Fatalf("registration = %v", cred)
	}

	fields := host.fields(name)
	if _, hasLabel := fields[credential.KeyLabel]; hasLabel || fields[credential.KeyAPIKey] != testGoodKey || fields[credential.KeyType] != ProviderID {
		t.Fatalf("auth file = %v", fields)
	}
	if fields[credential.KeyModelRevision] == nil || fields[credential.KeyModelRevision] != svc.catalog.Current().Revision {
		t.Fatalf("model_revision = %v, catalog %s", fields[credential.KeyModelRevision], svc.catalog.Current().Revision)
	}
	// Without force-model-prefix CPA registers both the bare and the prefixed
	// ID; the page still counts two models.
	if got := host.file(name).models; !reflect.DeepEqual(got, []string{"ghost", "model-b", "wokey/ghost", "wokey/model-b"}) {
		t.Fatalf("registered = %v", got)
	}
	if _, ok := fields["model_prefix"]; ok {
		t.Fatalf("model_prefix must not be written: %v", fields)
	}
	if raw := string(host.file(name).raw); strings.Contains(raw, "skip_verify") || strings.Contains(raw, "auth_index") {
		t.Fatalf("request-only fields leaked into the file: %s", raw)
	}

	// The same key again is a duplicate.
	if status, out = manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`"}`); status != http.StatusConflict || out["error"] != "duplicate_key" {
		t.Fatalf("duplicate: %d %v", status, out)
	}
	// A key Wokey rejects is a client error, and nothing is saved.
	saves := host.saveCount()
	if status, out = manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"sk-wrong-key-123456"}`); status != http.StatusBadRequest || out["error"] != "key_verification_failed" {
		t.Fatalf("bad key: %d %v", status, out)
	}
	if host.saveCount() != saves {
		t.Fatal("a rejected key must not be saved")
	}
	// skip_verify saves without calling Wokey.
	status, out = manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"sk-offline-key-123456","skip_verify":true,"prefix":"","proxy_url":"direct"}`)
	if status != http.StatusCreated {
		t.Fatalf("skip_verify: %d %v", status, out)
	}
	cred = credentialOf(t, out)
	if cred["label"] != "" || cred["key_masked"] != credential.MaskKey("sk-offline-key-123456") || cred["effective_prefix"] != "" || cred["proxy_url"] != "direct" || cred["all_models"] != true {
		t.Fatalf("skip_verify credential = %v", cred)
	}
	if got := host.file(credential.FileNameForKey("sk-offline-key-123456")).models; !reflect.DeepEqual(got, []string{"model-a", "model-b"}) {
		t.Fatalf("bare registration = %v", got)
	}
}

func TestAddCredentialValidation(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	cases := []struct {
		body, code string
	}{
		{``, "invalid_json"},
		{`{bad`, "invalid_json"},
		{`{}`, "invalid_api_key"},
		{`{"api_key":"sk a"}`, "invalid_api_key"},
		{`{"api_key":"k","proxy_url":"ftp://x"}`, "invalid_proxy"},
		{`{"api_key":"k","base_url":"not a url"}`, "invalid_base_url"},
		{`{"api_key":"k","models":"model-a"}`, "invalid_models"},
		{`{"api_key":"k","models":["a b"]}`, "invalid_models"},
		{`{"api_key":"k","prefix":5}`, "invalid_prefix"},
		{`{"api_key":"k","prefix":"a b"}`, "invalid_prefix"},
		{`{"api_key":"k","priority":"high"}`, "invalid_priority"},
		{`{"api_key":"k","priority":1.5}`, "invalid_priority"},
		{`{"api_key":"k","issuer_token":"sk-not-issuing"}`, "invalid_issuer_token"},
		{`{"api_key":"k","issuer_token":"wkt_"}`, "invalid_issuer_token"},
		{`{"api_key":"k","issuer_token":"wkt_a b"}`, "invalid_issuer_token"},
	}
	for _, tc := range cases {
		status, out := manage(t, svc, http.MethodPost, credentialsPath, tc.body)
		if status != http.StatusBadRequest || out["error"] != tc.code {
			t.Errorf("%s: %d %v, want %s", tc.body, status, out, tc.code)
		}
	}
	if host.saveCount() != 0 {
		t.Fatal("invalid input must not be saved")
	}
}

func TestUpdateCredential(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	host.forcePrefix = true
	status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	name := credential.FileNameForKey(testGoodKey)
	index := host.file(name).index

	body := `{"auth_index":"` + index + `","label":"main","models":["model-a","ghost"],"prefix":"","proxy_url":"http://user:pass@proxy.example:3128","priority":5,"note":"n"}`
	status, out = manage(t, svc, http.MethodPatch, credentialsPath, body)
	if status != http.StatusOK {
		t.Fatalf("patch: %d %v", status, out)
	}
	cred := credentialOf(t, out)
	if cred["label"] != "main" || cred["priority"] != float64(5) || cred["effective_prefix"] != "" || cred["all_models"] != false {
		t.Fatalf("credential = %v", cred)
	}
	if proxy, _ := cred["proxy_url"].(string); strings.Contains(proxy, "pass") || !strings.Contains(proxy, "proxy.example") {
		t.Fatalf("proxy must be redacted, got %q", proxy)
	}
	if got := stringList(out["unknown_models"]); !reflect.DeepEqual(got, []string{"ghost"}) {
		t.Fatalf("unknown_models = %v", got)
	}
	if got := stringList(cred["missing_models"]); !reflect.DeepEqual(got, []string{"ghost"}) {
		t.Fatalf("missing_models = %v", got)
	}
	if got := host.file(name).models; !reflect.DeepEqual(got, []string{"ghost", "model-a"}) {
		t.Fatalf("registered = %v", got)
	}
	fields := host.fields(name)
	if fields[credential.KeyProxyURL] != "http://user:pass@proxy.example:3128" || fields[credential.KeyAPIKey] != testGoodKey || fields[credential.KeyNote] != "n" {
		t.Fatalf("auth file = %v", fields)
	}
	if fields[credential.KeyPrefix] != "" {
		t.Fatalf("prefix = %#v", fields[credential.KeyPrefix])
	}
	if prefix, ok := cred["prefix"].(string); !ok || prefix != "" {
		t.Fatalf("view prefix = %#v", cred["prefix"])
	}

	// null resets models, prefix and priority; "" clears the proxy. Fields
	// that are not sent stay as they are.
	status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"name":"`+name+`","models":null,"prefix":null,"priority":null,"proxy_url":""}`)
	if status != http.StatusOK {
		t.Fatalf("reset: %d %v", status, out)
	}
	fields = host.fields(name)
	for _, key := range []string{credential.KeyModels, credential.KeyPrefix, credential.KeyPriority, credential.KeyProxyURL} {
		if _, ok := fields[key]; ok {
			t.Errorf("%s not cleared: %v", key, fields[key])
		}
	}
	if fields[credential.KeyLabel] != "main" || fields[credential.KeyNote] != "n" {
		t.Fatalf("untouched fields changed: %v", fields)
	}
	// With force-model-prefix only the prefixed IDs are registered.
	if got := host.file(name).models; !reflect.DeepEqual(got, []string{"wokey/model-a", "wokey/model-b"}) {
		t.Fatalf("registered after reset = %v", got)
	}
	if cred = credentialOf(t, out); cred["prefix"] != nil || cred["effective_prefix"] != "wokey" || cred["registered_models"] != float64(2) {
		t.Fatalf("credential after reset = %v", cred)
	}
	_, statusOut := manage(t, svc, http.MethodGet, statusPath, "")
	if config, _ := statusOut["config"].(map[string]any); config["default_prefix"] != "wokey" || config["force_model_prefix"] != true {
		t.Fatalf("status config = %v", statusOut["config"])
	}

	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","api_key":"sk-other"}`); status != http.StatusBadRequest || out["error"] != "immutable_api_key" {
		t.Fatalf("api_key change: %d %v", status, out)
	}
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","proxy_url":"socks4://x:1"}`); status != http.StatusBadRequest || out["error"] != "invalid_proxy" {
		t.Fatalf("bad proxy: %d %v", status, out)
	}
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"idx-404","label":"x"}`); status != http.StatusNotFound || out["error"] != "credential_not_found" {
		t.Fatalf("missing: %d %v", status, out)
	}
}

func TestUpdateIgnoresOtherProviders(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	other := host.seed("codex-1.json", "codex", map[string]any{"type": "codex", "api_key": "x"})
	status, out := manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+other.index+`","label":"x"}`)
	if status != http.StatusNotFound {
		t.Fatalf("a non-Wokey file must not be editable: %d %v", status, out)
	}
	if host.saveCount() != 0 {
		t.Fatal("non-Wokey file saved")
	}
}

func TestStatusView(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	if _, errRefresh := svc.refreshCatalog(false); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	revision := svc.catalog.Current().Revision
	host.seed("wokey-a.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey, "label": "A", "models": []string{"model-a", "gone"}, "model_revision": revision})
	host.seed("wokey-b.json", ProviderID, map[string]any{"type": "wokey", "api_key": "sk-b-0000000000", "disabled": true, "model_revision": revision})
	host.seed("codex.json", "codex", map[string]any{"type": "codex"})
	broken := host.seed("wokey-c.json", ProviderID, map[string]any{"type": "wokey"})
	host.mu.Lock()
	broken.getFails = true
	host.mu.Unlock()

	status, out := manage(t, svc, http.MethodGet, statusPath, "")
	if status != http.StatusOK || out["plugin"] != PluginID || out["version"] != "test" {
		t.Fatalf("status: %d %v", status, out)
	}
	if catalog, _ := out["catalog"].(map[string]any); catalog["count"] != float64(2) || catalog["revision"] != revision {
		t.Fatalf("catalog = %v", out["catalog"])
	}
	creds, _ := out["credentials"].([]any)
	if len(creds) != 3 {
		t.Fatalf("credentials = %v", creds)
	}
	a := creds[0].(map[string]any)
	if a["label"] != "A" || a["key_masked"] != credential.MaskKey(testGoodKey) || !reflect.DeepEqual(stringList(a["missing_models"]), []string{"gone"}) {
		t.Fatalf("a = %v", a)
	}
	if b := creds[1].(map[string]any); b["disabled"] != true || b["all_models"] != true {
		t.Fatalf("b = %v", b)
	}
	if c := creds[2].(map[string]any); c["read_error"] == "" || c["read_error"] == nil {
		t.Fatalf("c = %v", c)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), testGoodKey) {
		t.Fatal("status leaks an API key")
	}
}

func TestSyncRegistrationsAfterCatalogChange(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	host.forcePrefix = true
	if status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`"}`); status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	enabled := credential.FileNameForKey(testGoodKey)
	first := svc.catalog.Current().Revision
	host.seed("wokey-off.json", ProviderID, map[string]any{"type": "wokey", "api_key": "sk-off-0000000000", "disabled": true, "model_revision": first})

	// An unchanged catalog touches nothing.
	saves := host.saveCount()
	if _, errRefresh := svc.refreshCatalog(true); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if host.saveCount() != saves {
		t.Fatalf("unchanged catalog re-saved %d files", host.saveCount()-saves)
	}

	upstream.setModels([]map[string]any{{"id": "model-a"}, {"id": "model-b"}, {"id": "model-c"}}, `"v2"`)
	snapshot, errRefresh := svc.refreshCatalog(false)
	if errRefresh != nil || snapshot.Revision == first {
		t.Fatalf("refresh: %v", errRefresh)
	}
	if got := host.file(enabled).models; !reflect.DeepEqual(got, []string{"wokey/model-a", "wokey/model-b", "wokey/model-c"}) {
		t.Fatalf("enabled credential not re-registered: %v", got)
	}
	for _, name := range []string{enabled, "wokey-off.json"} {
		if got := host.fields(name)[credential.KeyModelRevision]; got != snapshot.Revision {
			t.Errorf("%s model_revision = %v", name, got)
		}
	}
	if host.file("wokey-off.json").models != nil {
		t.Fatal("a disabled credential must not register models")
	}

	// Once synced, another refresh is a no-op, also for the disabled file.
	saves = host.saveCount()
	if _, errRefresh = svc.refreshCatalog(true); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if host.saveCount() != saves {
		t.Fatalf("synced catalog re-saved %d files", host.saveCount()-saves)
	}
}

func TestModelsRoutes(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, _ := newTestService(t, upstream)
	status, out := manage(t, svc, http.MethodGet, modelsPath, "")
	if status != http.StatusOK {
		t.Fatalf("models: %d %v", status, out)
	}
	models, _ := out["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
	pricing := models[0].(map[string]any)["pricing"].(map[string]any)
	if pricing["input"] != float64(3) || pricing["output"] != float64(15) || pricing["cache_read"] != nil {
		t.Fatalf("pricing = %v", pricing)
	}
	if status, _ = manage(t, svc, http.MethodPost, modelsRefresh, ""); status != http.StatusOK {
		t.Fatalf("refresh: %d", status)
	}
	if full, _ := upstream.catalogCounts(); full != 2 {
		t.Fatalf("forced refresh did not refetch: full=%d", full)
	}

	upstream.handle("/v1/models", func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusServiceUnavailable, map[string]any{"error": "down"})
	})
	if status, out = manage(t, svc, http.MethodPost, modelsRefresh, ""); status != http.StatusBadGateway || out["error"] != "catalog_unavailable" {
		t.Fatalf("refresh failure: %d %v", status, out)
	}
	cold, _ := newTestService(t, upstream)
	if status, out = manage(t, cold, http.MethodGet, modelsPath, ""); status != http.StatusBadGateway {
		t.Fatalf("cold models: %d %v", status, out)
	}
}

func TestCredentialQuotaRoute(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	good := host.seed("wokey-good.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey})
	bad := host.seed("wokey-bad.json", ProviderID, map[string]any{"type": "wokey", "api_key": "sk-revoked-00000000"})

	status, out := manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+good.index, "")
	if status != http.StatusOK {
		t.Fatalf("quota: %d %v", status, out)
	}
	balance, _ := out["balance"].(map[string]any)
	key, _ := out["key"].(map[string]any)
	if balance["available_usd"] != 42.5 || balance["account"] != "user:1234" || balance["reserved_usd"] != 0.75 || key["limit_remaining_usd"] != 8.5 || key["label"] != "test key" {
		t.Fatalf("quota = %v", out)
	}
	// A revoked Wokey key is a 400, so the page does not mistake it for a
	// rejected management key.
	if status, out = manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+bad.index, ""); status != http.StatusBadRequest {
		t.Fatalf("revoked: %d %v", status, out)
	}
	if status, _ = manage(t, svc, http.MethodGet, quotaPath+"?auth_index=idx-404", ""); status != http.StatusNotFound {
		t.Fatalf("missing: %d", status)
	}
	if status, _ = manage(t, svc, http.MethodGet, quotaPath, ""); status != http.StatusNotFound {
		t.Fatalf("no auth_index: %d", status)
	}
}

func TestManagementRouting(t *testing.T) {
	svc, _ := newTestService(t, nil)
	page := svc.handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: pagePath + "/"})
	if page.StatusCode != http.StatusOK || page.Headers.Get("Content-Type") != "text/html; charset=utf-8" ||
		page.Headers.Get("Content-Security-Policy") != pageCSP || page.Headers.Get("Cache-Control") != "no-store" ||
		!strings.Contains(string(page.Body), "<html") {
		t.Fatalf("page: %d %v", page.StatusCode, page.Headers)
	}
	if !strings.Contains(pageCSP, "frame-ancestors 'self'") || strings.Contains(pageCSP, "unsafe-eval") {
		t.Fatalf("page CSP = %s", pageCSP)
	}
	logo := svc.handleManagement(pluginapi.ManagementRequest{Path: logoPath})
	if logo.StatusCode != http.StatusOK || logo.Headers.Get("Content-Type") != "image/svg+xml" ||
		logo.Headers.Get("Content-Security-Policy") != logoCSP || !strings.Contains(string(logo.Body), "<svg") {
		t.Fatalf("logo: %d %v", logo.StatusCode, logo.Headers)
	}
	if status, out := manage(t, svc, http.MethodGet, managementBase+"/nope", ""); status != http.StatusNotFound || out["error"] != "not_found" {
		t.Fatalf("unknown route: %d %v", status, out)
	}
	if status, _ := manage(t, svc, http.MethodDelete, credentialsPath, ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE credentials: %d", status)
	}
	if status, _ := manage(t, svc, http.MethodPost, pagePath, ""); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST page: %d", status)
	}
	big := svc.handleManagement(pluginapi.ManagementRequest{Method: http.MethodPost, Path: credentialsPath, Body: make([]byte, maxManagementBody+1)})
	if big.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d", big.StatusCode)
	}

	routes := managementRoutes()
	seen := map[string]bool{}
	for _, route := range routes.Routes {
		if !strings.HasPrefix(route.Path, managementBase+"/") {
			t.Errorf("route %s is outside %s", route.Path, managementBase)
		}
		seen[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{"GET " + statusPath, "GET " + modelsPath, "POST " + modelsRefresh, "POST " + credentialsPath, "PATCH " + credentialsPath, "GET " + quotaPath} {
		if !seen[want] {
			t.Errorf("route %s not declared", want)
		}
	}
	for _, resource := range routes.Resources {
		// flag.svg needs a country code; the other resources ignore it.
		req := pluginapi.ManagementRequest{Path: resourceBase + resource.Path, Query: url.Values{"cc": {"hk"}}}
		if resp := svc.handleManagement(req); resp.StatusCode != http.StatusOK {
			t.Errorf("resource %s: %d", resource.Path, resp.StatusCode)
		}
	}
}

func TestRedactProxy(t *testing.T) {
	if got := redactProxy("socks5://u:secret@h:1080"); strings.Contains(got, "secret") || !strings.Contains(got, "u:") {
		t.Fatalf("redact = %q", got)
	}
	if got := redactProxy(" direct "); got != "direct" {
		t.Fatalf("direct = %q", got)
	}
}

func TestDefaultPrefixChangeReRegisters(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	host.forcePrefix = true
	if status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`"}`); status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	name := credential.FileNameForKey(testGoodKey)
	if got := host.file(name).models; !reflect.DeepEqual(got, []string{"wokey/model-a", "wokey/model-b"}) {
		t.Fatalf("registered = %v", got)
	}

	// The default prefix is applied when CPA parses the file, so a config
	// change must re-save the file for CPA to pick it up.
	raw, _ := json.Marshal(rpcLifecycleRequest{ConfigYAML: []byte("models-refresh-minutes: 0\nmodel-prefix: wk\nlog-retention: 0\nbase-url: " + upstream.URL + "\n")})
	if envelope := decodeEnvelope(t, svc.Call(pluginabi.MethodPluginReconfigure, raw)); !envelope.OK {
		t.Fatalf("reconfigure: %+v", envelope.Error)
	}
	want := []string{"wk/model-a", "wk/model-b"}
	deadline := time.Now().Add(5 * time.Second)
	for !reflect.DeepEqual(host.file(name).models, want) {
		if time.Now().After(deadline) {
			t.Fatalf("registered after prefix change = %v", host.file(name).models)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := host.fields(name)[credential.KeyPrefix]; ok {
		t.Fatal("the default prefix must not be written into the file")
	}
}
