package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/proxystore"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The proxy list of the management page and its egress check.

const (
	proxiesPath       = managementBase + "/proxies"
	proxiesCheckPath  = managementBase + "/proxies/check"
	proxiesImportPath = managementBase + "/proxies/import"

	// egressCheckUserAgent is sent by the egress check: IP lookup services
	// answer curl with plain JSON.
	egressCheckUserAgent = "curl/8.7.1"
	// egressCheckTimeout bounds one lookup through the egress.
	egressCheckTimeout = 10 * time.Second
)

// defaultEgressCheckURLs are tried in order until one reports the egress IP.
var defaultEgressCheckURLs = []string{"https://ipinfo.io/json", "https://api.ip.sb/geoip"}

type proxyView struct {
	ID string `json:"id"`
	// Name is the display name; URL is redacted and Label is scheme://host:port.
	Name      string `json:"name"`
	URL       string `json:"url"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at,omitempty"`
	// LastCheck is the latest egress check of the current URL.
	LastCheck *proxystore.Check `json:"last_check,omitempty"`
}

type proxiesView struct {
	Proxies     []proxyView `json:"proxies"`
	GlobalProxy string      `json:"global_proxy,omitempty"`
	// BuiltinChecks holds the last checks of "direct" and "inherit".
	BuiltinChecks map[string]proxystore.Check `json:"builtin_checks,omitempty"`
	LoadError     string                      `json:"load_error,omitempty"`
}

type proxyResult struct {
	OK    bool      `json:"ok"`
	Proxy proxyView `json:"proxy"`
	// UpdatedCredentials counts the auth files moved to a changed URL;
	// FailedCredentials names those that could not be saved.
	UpdatedCredentials int      `json:"updated_credentials"`
	FailedCredentials  []string `json:"failed_credentials,omitempty"`
	CredentialsError   string   `json:"credentials_error,omitempty"`
}

type proxyInput struct {
	ID     string  `json:"id"`
	Name   *string `json:"name"`
	URL    *string `json:"url"`
	Target string  `json:"target"`
}

func proxyViewOf(p proxystore.Proxy) proxyView {
	return proxyView{ID: p.ID, Name: p.Name, URL: redactProxy(p.URL), Label: p.Label(), CreatedAt: p.CreatedAt, LastCheck: p.LastCheck}
}

func (s *Service) proxiesView() proxiesView {
	list, loadErr := s.proxies.List()
	view := proxiesView{Proxies: make([]proxyView, 0, len(list)), GlobalProxy: redactProxy(s.globalProxy()), LoadError: loadErr}
	for _, p := range list {
		view.Proxies = append(view.Proxies, proxyViewOf(p))
	}
	builtin := s.proxies.BuiltinChecks()
	if check, ok := builtin[proxystore.BuiltinInherit]; ok {
		// The inherited egress follows CPA's global proxy; a check made
		// through another one no longer applies.
		if setting, errProxy := wokey.ResolveEgress("", s.globalProxy()); errProxy != nil || setting.Label() != check.Egress {
			delete(builtin, proxystore.BuiltinInherit)
		}
	}
	if len(builtin) > 0 {
		view.BuiltinChecks = builtin
	}
	return view
}

func decodeProxyInput(body []byte) (proxyInput, *errorBody) {
	var input proxyInput
	if len(bytes.TrimSpace(body)) == 0 {
		return input, &errorBody{Error: "invalid_json", Message: "request body is required"}
	}
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return input, &errorBody{Error: "invalid_json", Message: errUnmarshal.Error()}
	}
	return input, nil
}

// proxyStoreError maps a store error to a management response.
func proxyStoreError(err error) pluginapi.ManagementResponse {
	var input *proxystore.InputError
	switch {
	case errors.As(err, &input):
		code := "invalid_proxy"
		if input.Field == "name" {
			code = "invalid_name"
		}
		return jsonResponse(http.StatusBadRequest, errorBody{Error: code, Message: input.Error()})
	case errors.Is(err, proxystore.ErrNotFound):
		return jsonResponse(http.StatusNotFound, errorBody{Error: "proxy_not_found", Message: "no listed proxy has this id"})
	case errors.Is(err, proxystore.ErrDuplicate):
		return jsonResponse(http.StatusConflict, errorBody{Error: "duplicate_proxy", Message: "this proxy URL is already in the list"})
	}
	return jsonResponse(http.StatusInternalServerError, errorBody{Error: "save_failed", Message: err.Error()})
}

func (s *Service) addProxy(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeProxyInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	name, rawURL := "", ""
	if input.Name != nil {
		name = *input.Name
	}
	if input.URL != nil {
		rawURL = *input.URL
	}
	p, errAdd := s.proxies.Add(name, rawURL)
	if errAdd != nil {
		return proxyStoreError(errAdd)
	}
	s.logf("info", "wokey: added proxy", map[string]any{"proxy": p.Label()})
	return jsonResponse(http.StatusCreated, proxyResult{OK: true, Proxy: proxyViewOf(p)})
}

// updateProxy renames a proxy or changes its URL. Credentials using the old
// URL are moved to the new one, so they keep using the listed proxy.
func (s *Service) updateProxy(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeProxyInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	if input.URL != nil && strings.TrimSpace(*input.URL) == "" {
		// An empty URL field on the page means "keep the current URL".
		input.URL = nil
	}
	before, after, errUpdate := s.proxies.Update(input.ID, input.Name, input.URL)
	if errUpdate != nil {
		return proxyStoreError(errUpdate)
	}
	result := proxyResult{OK: true, Proxy: proxyViewOf(after)}
	if before.URL != after.URL {
		result.UpdatedCredentials, result.FailedCredentials, result.CredentialsError = s.repointCredentials(before.URL, after.URL)
		s.logf("info", "wokey: changed proxy URL", map[string]any{"proxy": after.Label(), "credentials": result.UpdatedCredentials})
	}
	return jsonResponse(http.StatusOK, result)
}

// repointCredentials moves every Wokey credential whose proxy_url is oldURL
// to newURL. It returns how many were saved, the names that failed and an
// error when the credentials could not be listed.
func (s *Service) repointCredentials(oldURL, newURL string) (int, []string, string) {
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return 0, nil, errList.Error()
	}
	revision := s.catalog.Current().Revision
	updated := 0
	var failed []string
	for _, entry := range entries {
		if entry.readError != "" || strings.TrimSpace(entry.cred.ProxyURL) != oldURL {
			continue
		}
		name := credential.SaveName(entry.Name, entry.Path)
		if name == "" {
			failed = append(failed, entry.Name)
			continue
		}
		fields := cloneFields(entry.fields)
		fields[credential.KeyProxyURL] = newURL
		if revision != "" {
			fields[credential.KeyModelRevision] = revision
		}
		if errSave := s.saveAuthFile(name, fields); errSave != nil {
			s.logf("warn", "wokey: cannot move credential to the new proxy URL", map[string]any{"auth": name, "error": errSave.Error()})
			failed = append(failed, name)
			continue
		}
		updated++
	}
	return updated, failed, ""
}

// deleteProxy removes a proxy from the list. Credentials using it keep its
// URL as their own proxy_url.
func (s *Service) deleteProxy(id string) pluginapi.ManagementResponse {
	removed, errDelete := s.proxies.Delete(id)
	if errDelete != nil {
		return proxyStoreError(errDelete)
	}
	s.logf("info", "wokey: deleted proxy", map[string]any{"proxy": removed.Label()})
	return jsonResponse(http.StatusOK, map[string]bool{"ok": true})
}

// importProxies adds the proxies credentials already use to the list.
func (s *Service) importProxies() pluginapi.ManagementResponse {
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	var urls []string
	for _, entry := range entries {
		if entry.readError == "" {
			urls = append(urls, entry.cred.ProxyURL)
		}
	}
	added, errAdd := s.proxies.AddMissing(urls)
	if errAdd != nil {
		return proxyStoreError(errAdd)
	}
	view := s.proxiesView()
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "added": added, "proxies": view.Proxies})
}

// resolveProxyID turns a credential's proxy_id into its proxy_url.
func (s *Service) resolveProxyID(input *credentialInput) *errorBody {
	if input.ProxyID == nil || strings.TrimSpace(*input.ProxyID) == "" {
		return nil
	}
	list, _ := s.proxies.List()
	p, ok := list.Get(*input.ProxyID)
	if !ok {
		return &errorBody{Error: "proxy_not_found", Message: "proxy_id does not match a listed proxy"}
	}
	proxyURL := p.URL
	input.ProxyURL = &proxyURL
	return nil
}

// checkProxy looks up the exit address of a listed proxy ({"id"}), the
// direct line ({"target":"direct"}) or what credentials without a proxy
// use ({"target":"inherit"}: CPA's global proxy, else the environment).
func (s *Service) checkProxy(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeProxyInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	var (
		proxyURL string
		save     func(proxystore.Check) error
	)
	switch {
	case strings.TrimSpace(input.ID) != "":
		list, _ := s.proxies.List()
		p, ok := list.Get(input.ID)
		if !ok {
			return proxyStoreError(proxystore.ErrNotFound)
		}
		proxyURL = p.URL
		save = func(check proxystore.Check) error { return s.proxies.SetCheck(p.ID, p.URL, check) }
	case input.Target == proxystore.BuiltinDirect || input.Target == proxystore.BuiltinInherit:
		if input.Target == proxystore.BuiltinDirect {
			proxyURL = "direct"
		}
		target := input.Target
		save = func(check proxystore.Check) error { return s.proxies.SetBuiltinCheck(target, check) }
	default:
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_target", Message: `send {"id"} or {"target":"direct"|"inherit"}`})
	}
	check := s.checkEgress(s.ctx, proxyURL)
	// The result is kept so the list shows it later; a failure to save it
	// does not fail the check.
	if errSave := save(check); errSave != nil {
		s.logf("warn", "wokey: cannot save the egress check", map[string]any{"egress": check.Egress, "error": errSave.Error()})
	}
	return jsonResponse(http.StatusOK, check)
}

// checkEgress asks IP lookup services, through the egress, which address
// the request came from. Each check opens a fresh connection, so the
// latency includes reaching the proxy.
func (s *Service) checkEgress(ctx context.Context, proxyURL string) (result proxystore.Check) {
	defer func() { result.CheckedAt = s.now().UTC().Format(time.RFC3339) }()
	setting, errProxy := wokey.ResolveEgress(proxyURL, s.globalProxy())
	if errProxy != nil {
		result.Error = errProxy.Error()
		return result
	}
	result.Egress = setting.Label()
	transport := wokey.NewTransport(setting)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	var failures []string
	for _, target := range s.egressCheckURLs {
		info, latency, errLookup := lookupEgress(ctx, client, target)
		source := target
		if parsed, errParse := url.Parse(target); errParse == nil && parsed.Host != "" {
			source = parsed.Host
		}
		if errLookup != nil {
			failures = append(failures, source+": "+errLookup.Error())
			continue
		}
		info.OK, info.Egress, info.Source = true, result.Egress, source
		info.LatencyMS = max(latency.Milliseconds(), 1)
		result = info
		return result
	}
	result.Error = strings.Join(failures, "; ")
	return result
}

func lookupEgress(ctx context.Context, client *http.Client, target string) (proxystore.Check, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, egressCheckTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errReq != nil {
		return proxystore.Check{}, 0, errReq
	}
	req.Header.Set("User-Agent", egressCheckUserAgent)
	req.Header.Set("Accept", "*/*")
	started := time.Now()
	resp, errDo := client.Do(req)
	if errDo != nil {
		return proxystore.Check{}, 0, unwrapURLError(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	latency := time.Since(started)
	if errRead != nil {
		return proxystore.Check{}, 0, unwrapURLError(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return proxystore.Check{}, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	info, errParse := parseEgressInfo(raw)
	if errParse != nil {
		return proxystore.Check{}, 0, errParse
	}
	return info, latency, nil
}

// unwrapURLError drops the "Get <url>:" prefix; the caller names the service.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s", egressCheckTimeout)
		}
		return urlErr.Err
	}
	return err
}

// parseEgressInfo reads the IP, country and city from an ipinfo.io or ip.sb
// geoip response.
func parseEgressInfo(raw []byte) (proxystore.Check, error) {
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil {
		return proxystore.Check{}, fmt.Errorf("unexpected response: %w", errUnmarshal)
	}
	text := func(key string) string {
		switch value := body[key].(type) {
		case string:
			return strings.TrimSpace(value)
		case float64:
			return fmt.Sprintf("%.0f", value)
		}
		return ""
	}
	info := proxystore.Check{IP: text("ip"), Region: text("region"), City: text("city")}
	if net.ParseIP(info.IP) == nil {
		return proxystore.Check{}, errors.New("no IP address in the response")
	}
	// ipinfo.io puts the two-letter code in "country", ip.sb in "country_code".
	info.Country = text("country_code")
	if info.Country == "" {
		info.Country = text("country")
	}
	if len(info.Country) == 2 {
		info.Country = strings.ToUpper(info.Country)
	}
	return info, nil
}
