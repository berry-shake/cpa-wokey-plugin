package plugin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func proxyOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	p, ok := out["proxy"].(map[string]any)
	if !ok {
		t.Fatalf("response has no proxy: %v", out)
	}
	return p
}

func proxyList(t *testing.T, svc *Service) []map[string]any {
	t.Helper()
	status, out := manage(t, svc, http.MethodGet, proxiesPath, "")
	if status != http.StatusOK {
		t.Fatalf("list proxies: %d %v", status, out)
	}
	items, _ := out["proxies"].([]any)
	list := make([]map[string]any, 0, len(items))
	for _, item := range items {
		list = append(list, item.(map[string]any))
	}
	return list
}

func TestProxyCRUD(t *testing.T) {
	svc, _ := newTestService(t, nil)

	status, out := manage(t, svc, http.MethodPost, proxiesPath, `{"name":"HK","url":"socks5://user:secret@10.0.0.1:1080"}`)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	added := proxyOf(t, out)
	id, _ := added["id"].(string)
	if id == "" || added["name"] != "HK" || added["label"] != "socks5://10.0.0.1:1080" {
		t.Fatalf("added = %v", added)
	}
	if url, _ := added["url"].(string); strings.Contains(url, "secret") || !strings.Contains(url, "10.0.0.1:1080") {
		t.Fatalf("url must be redacted, got %q", url)
	}

	for body, want := range map[string]int{
		`{"url":"socks5://user:other@10.0.0.1:1080"}`:  http.StatusCreated,
		`{"url":"socks5://user:secret@10.0.0.1:1080"}`: http.StatusConflict,
		`{"url":"direct"}`:            http.StatusBadRequest,
		`{"url":""}`:                  http.StatusBadRequest,
		`{"url":"ftp://10.0.0.1:21"}`: http.StatusBadRequest,
		`not json`:                    http.StatusBadRequest,
	} {
		if status, out := manage(t, svc, http.MethodPost, proxiesPath, body); status != want {
			t.Fatalf("add %s: %d %v, want %d", body, status, out, want)
		}
	}
	if got := len(proxyList(t, svc)); got != 2 {
		t.Fatalf("list has %d proxies, want 2", got)
	}

	status, out = manage(t, svc, http.MethodPatch, proxiesPath, `{"id":"`+id+`","name":"Hong Kong","url":""}`)
	if status != http.StatusOK || proxyOf(t, out)["name"] != "Hong Kong" || proxyOf(t, out)["label"] != "socks5://10.0.0.1:1080" {
		t.Fatalf("rename: %d %v", status, out)
	}
	if status, out := manage(t, svc, http.MethodPatch, proxiesPath, `{"id":"nope","name":"x"}`); status != http.StatusNotFound || out["error"] != "proxy_not_found" {
		t.Fatalf("patch unknown: %d %v", status, out)
	}
	if status, out := manage(t, svc, http.MethodDelete, proxiesPath+"?id="+id, ""); status != http.StatusOK {
		t.Fatalf("delete: %d %v", status, out)
	}
	if status, _ := manage(t, svc, http.MethodDelete, proxiesPath+"?id="+id, ""); status != http.StatusNotFound {
		t.Fatalf("second delete: %d", status)
	}
	if got := len(proxyList(t, svc)); got != 1 {
		t.Fatalf("list has %d proxies after delete, want 1", got)
	}
}

func TestCredentialProxyIDAndURLChange(t *testing.T) {
	svc, host := newTestService(t, newFakeWokey(t))
	// With the catalog loaded, saving a file does not fetch it through the
	// credential's (unreachable) proxy.
	if _, errRefresh := svc.refreshCatalog(true); errRefresh != nil {
		t.Fatalf("load catalog: %v", errRefresh)
	}
	const oldURL = "socks5://user:secret@10.0.0.1:1080"
	status, out := manage(t, svc, http.MethodPost, proxiesPath, `{"name":"HK","url":"`+oldURL+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("add proxy: %d %v", status, out)
	}
	id := proxyOf(t, out)["id"].(string)

	a := host.seed("wokey-a.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-a"})
	host.seed("wokey-b.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-b", "proxy_url": "http://10.0.0.2:3128"})
	host.seed("wokey-c.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-c", "proxy_url": oldURL})

	status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+a.index+`","proxy_id":"`+id+`","proxy_url":"direct"}`)
	if status != http.StatusOK {
		t.Fatalf("pick proxy: %d %v", status, out)
	}
	if cred := credentialOf(t, out); cred["proxy_id"] != id || cred["proxy_name"] != "HK" {
		t.Fatalf("credential = %v", cred)
	}
	if got := host.fields("wokey-a.json")[credential.KeyProxyURL]; got != oldURL {
		t.Fatalf("proxy_url = %v, want the listed proxy's URL", got)
	}
	if status, out := manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+a.index+`","proxy_id":"nope"}`); status != http.StatusBadRequest || out["error"] != "proxy_not_found" {
		t.Fatalf("unknown proxy_id: %d %v", status, out)
	}

	const newURL = "socks5://user:secret@10.0.0.9:1080"
	status, out = manage(t, svc, http.MethodPatch, proxiesPath, `{"id":"`+id+`","url":"`+newURL+`"}`)
	if status != http.StatusOK || out["updated_credentials"] != float64(2) || out["failed_credentials"] != nil {
		t.Fatalf("change URL: %d %v", status, out)
	}
	for name, want := range map[string]string{"wokey-a.json": newURL, "wokey-b.json": "http://10.0.0.2:3128", "wokey-c.json": newURL} {
		if got := host.fields(name)[credential.KeyProxyURL]; got != want {
			t.Fatalf("%s proxy_url = %v, want %s", name, got, want)
		}
	}
	view := svc.status()
	named := 0
	for _, cred := range view.Credentials {
		if cred.ProxyID == id {
			named++
		}
		if strings.Contains(cred.ProxyURL, "secret") {
			t.Fatalf("status leaks the proxy password: %q", cred.ProxyURL)
		}
	}
	if named != 2 {
		t.Fatalf("%d credentials name the proxy, want 2", named)
	}

	// Deleting the proxy leaves the credentials on its URL.
	if status, _ := manage(t, svc, http.MethodDelete, proxiesPath+"?id="+id, ""); status != http.StatusOK {
		t.Fatalf("delete: %d", status)
	}
	if got := host.fields("wokey-c.json")[credential.KeyProxyURL]; got != newURL {
		t.Fatalf("proxy_url after delete = %v", got)
	}
}

func TestImportProxies(t *testing.T) {
	svc, host := newTestService(t, nil)
	host.seed("wokey-a.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-a", "proxy_url": "http://10.0.0.2:3128"})
	host.seed("wokey-b.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-b", "proxy_url": "direct"})
	host.seed("wokey-c.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-c", "proxy_url": "http://10.0.0.2:3128"})
	host.seed("wokey-d.json", ProviderID, map[string]any{"type": ProviderID, "api_key": "sk-wokey-d"})
	host.seed("other.json", "codex", map[string]any{"type": "codex", "proxy_url": "http://10.0.0.7:3128"})

	status, out := manage(t, svc, http.MethodPost, proxiesImportPath, "")
	if status != http.StatusOK || out["added"] != float64(1) {
		t.Fatalf("import: %d %v", status, out)
	}
	if status, out := manage(t, svc, http.MethodPost, proxiesImportPath, ""); status != http.StatusOK || out["added"] != float64(0) {
		t.Fatalf("second import: %d %v", status, out)
	}
	list := proxyList(t, svc)
	if len(list) != 1 || list[0]["name"] != "10.0.0.2:3128" {
		t.Fatalf("list = %v", list)
	}
}

// ipService answers like an IP lookup service and records User-Agents. It
// also serves as an HTTP forward proxy: requests in absolute form are
// answered with proxyIP.
type ipService struct {
	*httptest.Server
	mu      sync.Mutex
	agents  []string
	proxied int
}

func newIPService(t *testing.T, directIP, proxyIP string) *ipService {
	t.Helper()
	svc := &ipService{}
	svc.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		svc.mu.Lock()
		svc.agents = append(svc.agents, r.UserAgent())
		ip := directIP
		if r.URL.IsAbs() {
			svc.proxied++
			ip = proxyIP
		}
		svc.mu.Unlock()
		switch r.URL.Path {
		case "/json":
			writeJSON(rw, http.StatusOK, map[string]any{"ip": ip, "city": "Hong Kong", "region": "Hong Kong", "country": "HK", "org": "AS9312 xTom"})
		case "/limited":
			writeJSON(rw, http.StatusTooManyRequests, map[string]any{"error": "rate limited"})
		case "/geoip":
			writeJSON(rw, http.StatusOK, map[string]any{"ip": ip, "country": "Japan", "country_code": "JP", "city": "Tokyo", "asn": 2516, "asn_organization": "KDDI"})
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(svc.Close)
	return svc
}

func TestCheckProxy(t *testing.T) {
	ips := newIPService(t, "203.0.113.5", "198.51.100.7")
	svc, _ := newTestService(t, nil)
	svc.egressCheckURLs = []string{ips.URL + "/json"}

	status, out := manage(t, svc, http.MethodPost, proxiesCheckPath, `{"target":"direct"}`)
	if status != http.StatusOK || out["ok"] != true || out["ip"] != "203.0.113.5" || out["egress"] != "direct" ||
		out["country"] != "HK" || out["city"] != "Hong Kong" || out["org"] != nil || out["source"] == "" || out["checked_at"] == "" {
		t.Fatalf("direct check: %d %v", status, out)
	}
	if latency, _ := out["latency_ms"].(float64); latency < 1 {
		t.Fatalf("latency_ms = %v", out["latency_ms"])
	}

	// A listed proxy: the lookup goes out through it.
	status, out = manage(t, svc, http.MethodPost, proxiesPath, `{"url":"`+ips.URL+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("add proxy: %d %v", status, out)
	}
	id := proxyOf(t, out)["id"].(string)
	status, out = manage(t, svc, http.MethodPost, proxiesCheckPath, `{"id":"`+id+`"}`)
	if status != http.StatusOK || out["ok"] != true || out["ip"] != "198.51.100.7" || out["egress"] != ips.URL {
		t.Fatalf("proxy check: %d %v", status, out)
	}

	// Inherit follows CPA's global proxy.
	svc.rememberHost(pluginapi.HostConfigSummary{ProxyURL: ips.URL})
	status, out = manage(t, svc, http.MethodPost, proxiesCheckPath, `{"target":"inherit"}`)
	if status != http.StatusOK || out["ip"] != "198.51.100.7" || out["egress"] != ips.URL {
		t.Fatalf("inherit check: %d %v", status, out)
	}

	ips.mu.Lock()
	agents, proxied := append([]string(nil), ips.agents...), ips.proxied
	ips.mu.Unlock()
	if proxied != 2 {
		t.Fatalf("%d lookups went through the proxy, want 2", proxied)
	}
	for _, agent := range agents {
		if agent != egressCheckUserAgent {
			t.Fatalf("User-Agent = %q, want %q", agent, egressCheckUserAgent)
		}
	}

	if status, _ := manage(t, svc, http.MethodPost, proxiesCheckPath, `{"id":"nope"}`); status != http.StatusNotFound {
		t.Fatalf("unknown id: %d", status)
	}
	if status, _ := manage(t, svc, http.MethodPost, proxiesCheckPath, `{"target":"elsewhere"}`); status != http.StatusBadRequest {
		t.Fatalf("bad target: %d", status)
	}
}

func TestCheckProxyFallsBackAndReportsFailures(t *testing.T) {
	ips := newIPService(t, "203.0.113.5", "198.51.100.7")
	svc, _ := newTestService(t, nil)
	svc.egressCheckURLs = []string{ips.URL + "/limited", ips.URL + "/geoip"}

	status, out := manage(t, svc, http.MethodPost, proxiesCheckPath, `{"target":"direct"}`)
	if status != http.StatusOK || out["ok"] != true || out["country"] != "JP" || out["city"] != "Tokyo" || out["org"] != nil {
		t.Fatalf("fallback check: %d %v", status, out)
	}

	svc.egressCheckURLs = []string{ips.URL + "/limited", ips.URL + "/missing"}
	status, out = manage(t, svc, http.MethodPost, proxiesCheckPath, `{"target":"direct"}`)
	message, _ := out["error"].(string)
	if status != http.StatusOK || out["ok"] != false || !strings.Contains(message, "HTTP 429") || !strings.Contains(message, "HTTP 404") || out["ip"] != nil {
		t.Fatalf("failed check: %d %v", status, out)
	}

	// An unreachable proxy fails without an IP.
	status, out = manage(t, svc, http.MethodPost, proxiesPath, `{"url":"http://127.0.0.1:1"}`)
	if status != http.StatusCreated {
		t.Fatalf("add proxy: %d %v", status, out)
	}
	status, out = manage(t, svc, http.MethodPost, proxiesCheckPath, `{"id":"`+proxyOf(t, out)["id"].(string)+`"}`)
	if status != http.StatusOK || out["ok"] != false || out["egress"] != "http://127.0.0.1:1" || out["error"] == "" {
		t.Fatalf("unreachable proxy: %d %v", status, out)
	}
}

func TestParseEgressInfo(t *testing.T) {
	if _, errParse := parseEgressInfo([]byte(`{"ip":"not-an-ip"}`)); errParse == nil {
		t.Fatal("accepted a response without an IP")
	}
	if _, errParse := parseEgressInfo([]byte(`<html>`)); errParse == nil {
		t.Fatal("accepted HTML")
	}
	info, errParse := parseEgressInfo([]byte(`{"ip":"2001:db8::1","country":"Germany","region":"Hesse"}`))
	if errParse != nil || info.IP != "2001:db8::1" || info.Country != "Germany" || info.Region != "Hesse" {
		t.Fatalf("info = %+v, %v", info, errParse)
	}
	info, errParse = parseEgressInfo([]byte(`{"ip":"203.0.113.9","country":"hk","city":"Kwai Chung"}`))
	if errParse != nil || info.Country != "HK" || info.City != "Kwai Chung" {
		t.Fatalf("info = %+v, %v", info, errParse)
	}
}

func TestCheckResultsAreKept(t *testing.T) {
	ips := newIPService(t, "203.0.113.5", "198.51.100.7")
	svc, _ := newTestService(t, nil)
	svc.egressCheckURLs = []string{ips.URL + "/json"}
	svc.rememberHost(pluginapi.HostConfigSummary{ProxyURL: ips.URL})
	status, out := manage(t, svc, http.MethodPost, proxiesPath, `{"name":"HK","url":"`+ips.URL+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("add proxy: %d %v", status, out)
	}
	id := proxyOf(t, out)["id"].(string)
	for _, body := range []string{`{"id":"` + id + `"}`, `{"target":"direct"}`, `{"target":"inherit"}`} {
		if status, out := manage(t, svc, http.MethodPost, proxiesCheckPath, body); status != http.StatusOK || out["ok"] != true {
			t.Fatalf("check %s: %d %v", body, status, out)
		}
	}

	status, out = manage(t, svc, http.MethodGet, proxiesPath, "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %v", status, out)
	}
	last, _ := proxyList(t, svc)[0]["last_check"].(map[string]any)
	if last["ip"] != "198.51.100.7" || last["country"] != "HK" || last["checked_at"] == "" {
		t.Fatalf("last_check = %v", last)
	}
	builtin, _ := out["builtin_checks"].(map[string]any)
	direct, _ := builtin["direct"].(map[string]any)
	inherit, _ := builtin["inherit"].(map[string]any)
	if direct["ip"] != "203.0.113.5" || inherit["ip"] != "198.51.100.7" {
		t.Fatalf("builtin_checks = %v", builtin)
	}

	// CPA's global proxy changed: the inherited check no longer applies.
	svc.rememberHost(pluginapi.HostConfigSummary{ProxyURL: "direct"})
	_, out = manage(t, svc, http.MethodGet, proxiesPath, "")
	builtin, _ = out["builtin_checks"].(map[string]any)
	if _, ok := builtin["inherit"]; ok || builtin["direct"] == nil {
		t.Fatalf("builtin_checks after the global proxy changed = %v", builtin)
	}

	// A new URL drops the proxy's check.
	if status, out := manage(t, svc, http.MethodPatch, proxiesPath, `{"id":"`+id+`","url":"http://127.0.0.1:1"}`); status != http.StatusOK {
		t.Fatalf("change URL: %d %v", status, out)
	}
	if last := proxyList(t, svc)[0]["last_check"]; last != nil {
		t.Fatalf("last_check after the URL changed = %v", last)
	}
}

func TestFlagResource(t *testing.T) {
	svc, _ := newTestService(t, nil)
	resp := svc.handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: flagPath, Query: url.Values{"cc": {"hk"}}})
	if resp.StatusCode != http.StatusOK || resp.Headers.Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(string(resp.Body), "<svg") {
		t.Fatalf("flag: %d %v %.40s", resp.StatusCode, resp.Headers, resp.Body)
	}
	if csp := resp.Headers.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || resp.Headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("flag headers = %v", resp.Headers)
	}
	for _, code := range []string{"", "zz", "hkg"} {
		resp := svc.handleManagement(pluginapi.ManagementRequest{Method: http.MethodGet, Path: flagPath, Query: url.Values{"cc": {code}}})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("flag %q: %d", code, resp.StatusCode)
		}
	}
	routes := managementRoutes()
	found := false
	for _, r := range routes.Resources {
		found = found || r.Path == "/flag.svg"
	}
	if !found {
		t.Fatal("flag.svg is not declared as a resource")
	}
}

func TestReorderProxies(t *testing.T) {
	svc, _ := newTestService(t, nil)
	var ids []string
	for _, url := range []string{"http://10.0.0.1:8080", "http://10.0.0.2:8080", "http://10.0.0.3:8080"} {
		status, out := manage(t, svc, http.MethodPost, proxiesPath, `{"url":"`+url+`"}`)
		if status != http.StatusCreated {
			t.Fatalf("add: %d %v", status, out)
		}
		ids = append(ids, proxyOf(t, out)["id"].(string))
	}
	status, out := manage(t, svc, http.MethodPost, proxiesOrderPath, `{"ids":["`+ids[2]+`","`+ids[0]+`","`+ids[1]+`"]}`)
	if status != http.StatusOK || out["ok"] != true {
		t.Fatalf("reorder: %d %v", status, out)
	}
	items, _ := out["proxies"].([]any)
	if len(items) != 3 || items[0].(map[string]any)["id"] != ids[2] {
		t.Fatalf("reorder response = %v", out)
	}
	list := proxyList(t, svc)
	if list[0]["id"] != ids[2] || list[1]["id"] != ids[0] || list[2]["id"] != ids[1] {
		t.Fatalf("list = %v", list)
	}
	for _, body := range []string{"", "not json", `{"ids":"x"}`} {
		if status, _ := manage(t, svc, http.MethodPost, proxiesOrderPath, body); status != http.StatusBadRequest {
			t.Fatalf("reorder %q: %d", body, status)
		}
	}
}
