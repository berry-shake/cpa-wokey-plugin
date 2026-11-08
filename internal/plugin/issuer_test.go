package plugin

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestValidIssuerToken(t *testing.T) {
	for token, want := range map[string]bool{
		"wkt_abc123":    true,
		"wkt_":          false,
		"sk-wk-abc":     false,
		"WKT_abc":       false,
		"wkt_a b":       false,
		"":              false,
		" wkt_abc123":   false,
		"wkt_abc\t123":  false,
		testIssuerToken: true,
	} {
		if got := validIssuerToken(token); got != want {
			t.Errorf("validIssuerToken(%q) = %v, want %v", token, got, want)
		}
	}
}

func TestIssuerInfoFrom(t *testing.T) {
	var withProvider, noProvider map[string]any
	_ = json.Unmarshal([]byte(`{"provider":{"availableUsd":"9.56639200","settlementFrozen":true},"referrer":{"availableUsd":"1.25000000"},
		"api":{"enabled":true,"eligible":true,"availableUsd":"0.33","disqualifiedBy":null}}`), &withProvider)
	_ = json.Unmarshal([]byte(`{"provider":null,"referrer":{"availableUsd":"0.00000000"},"api":{"enabled":false}}`), &noProvider)

	info := issuerInfoFrom(withProvider, testIssuerToken)
	if info.ProviderUSD == nil || math.Abs(*info.ProviderUSD-9.566392) > 1e-9 || !info.ProviderFrozen || info.ReferrerUSD != 1.25 {
		t.Fatalf("with provider = %+v", info)
	}
	if info.Account != keyFingerprint(testIssuerToken) || strings.Contains(info.Account, "wkt_") {
		t.Fatalf("account = %q", info.Account)
	}
	info = issuerInfoFrom(noProvider, testIssuerToken2)
	if info.ProviderUSD != nil || info.ProviderFrozen || info.ReferrerUSD != 0 {
		t.Fatalf("without provider = %+v", info)
	}
}

func TestCredentialQuotaIssuerBalance(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	plain := host.seed("wokey-plain.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey})
	earner := host.seed("wokey-earner.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey, "issuer_token": testIssuerToken})
	p2p := host.seed("wokey-p2p.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey, "issuer_token": testIssuerToken2})
	malformed := host.seed("wokey-malformed.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey, "issuer_token": "sk-not-issuing"})
	revoked := host.seed("wokey-revoked.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey, "issuer_token": "wkt_revoked_000000"})

	// Keys without an issuing token never call the issuing API.
	status, out := manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+plain.index, "")
	if status != http.StatusOK || out["issuer"] != nil || out["issuer_error"] != nil || upstream.issuerCallCount() != 0 {
		t.Fatalf("plain: %d %v, issuer calls %d", status, out, upstream.issuerCallCount())
	}

	status, out = manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+earner.index, "")
	issuer, _ := out["issuer"].(map[string]any)
	if status != http.StatusOK || issuer == nil || math.Abs(issuer["provider_usd"].(float64)-9.566392) > 1e-9 || issuer["referrer_usd"] != float64(0) {
		t.Fatalf("earner: %d %v", status, out)
	}
	// Earnings are reported beside the API balance, never added to it.
	if balance, _ := out["balance"].(map[string]any); balance["available_usd"] != 42.5 {
		t.Fatalf("balance changed: %v", out["balance"])
	}

	earnerAccount := issuer["account"]
	status, out = manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+p2p.index, "")
	issuer, _ = out["issuer"].(map[string]any)
	if status != http.StatusOK || issuer == nil || issuer["provider_usd"] != float64(0) || issuer["account"] == earnerAccount {
		t.Fatalf("p2p: %d %v", status, out)
	}
	if upstream.issuerCallCount() != 2 {
		t.Fatalf("issuer calls = %d, want 2", upstream.issuerCallCount())
	}

	// A token that is not a wkt_ token is reported without calling Wokey.
	status, out = manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+malformed.index, "")
	if status != http.StatusOK || out["issuer"] != nil || !strings.Contains(stringValue(out["issuer_error"]), "wkt_") || upstream.issuerCallCount() != 2 {
		t.Fatalf("malformed: %d %v", status, out)
	}
	// A rejected token only fails the earnings, not the balance.
	status, out = manage(t, svc, http.MethodGet, quotaPath+"?auth_index="+revoked.index, "")
	if status != http.StatusOK || out["balance"] == nil || !strings.Contains(stringValue(out["issuer_error"]), "invalid_issuer_token") {
		t.Fatalf("revoked: %d %v", status, out)
	}

	// CPA's quota panel shows spend only and skips the issuing API.
	calls := upstream.issuerCallCount()
	raw := svc.quotaFetch(pluginapi.QuotaFetchRequest{AuthIndex: earner.index})
	if env := decodeEnvelope(t, raw); !env.OK || upstream.issuerCallCount() != calls {
		t.Fatalf("quota panel: ok=%v issuer calls %d -> %d", env.OK, calls, upstream.issuerCallCount())
	}
}

func TestIssuerTokenAddAndEdit(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	name := authFileNameForKey(testGoodKey)

	// A token Wokey rejects is a client error, and nothing is saved.
	status, out := manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`","issuer_token":"wkt_revoked_000000"}`)
	if status != http.StatusBadRequest || out["error"] != "issuer_token_verification_failed" || host.saveCount() != 0 {
		t.Fatalf("rejected token: %d %v", status, out)
	}

	status, out = manage(t, svc, http.MethodPost, credentialsPath, `{"api_key":"`+testGoodKey+`","issuer_token":" `+testIssuerToken+` "}`)
	if status != http.StatusCreated {
		t.Fatalf("add: %d %v", status, out)
	}
	cred := credentialOf(t, out)
	if cred["issuer_token_masked"] != maskKey(testIssuerToken) || strings.Contains(string(mustJSON(t, out)), testIssuerToken) {
		t.Fatalf("token leaked or missing: %v", out)
	}
	if host.fields(name)[keyIssuerToken] != testIssuerToken {
		t.Fatalf("auth file = %v", host.fields(name))
	}
	index := cred["auth_index"].(string)

	// Editing other fields keeps the token and does not re-check it.
	calls := upstream.issuerCallCount()
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","label":"main"}`); status != http.StatusOK {
		t.Fatalf("edit label: %d %v", status, out)
	}
	if host.fields(name)[keyIssuerToken] != testIssuerToken || upstream.issuerCallCount() != calls {
		t.Fatalf("label edit touched the token: %v, calls %d -> %d", host.fields(name), calls, upstream.issuerCallCount())
	}
	// A new token is checked before it is saved.
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","issuer_token":"wkt_revoked_000000"}`); status != http.StatusBadRequest || out["error"] != "issuer_token_verification_failed" {
		t.Fatalf("edit rejected token: %d %v", status, out)
	}
	if host.fields(name)[keyIssuerToken] != testIssuerToken {
		t.Fatalf("rejected token was saved: %v", host.fields(name))
	}
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","issuer_token":"`+testIssuerToken2+`"}`); status != http.StatusOK {
		t.Fatalf("edit token: %d %v", status, out)
	}
	if host.fields(name)[keyIssuerToken] != testIssuerToken2 {
		t.Fatalf("token not replaced: %v", host.fields(name))
	}
	// An empty token removes it.
	if status, out = manage(t, svc, http.MethodPatch, credentialsPath, `{"auth_index":"`+index+`","issuer_token":""}`); status != http.StatusOK {
		t.Fatalf("clear token: %d %v", status, out)
	}
	if _, has := host.fields(name)[keyIssuerToken]; has || credentialOf(t, out)["issuer_token_masked"] != nil {
		t.Fatalf("token not removed: %v / %v", host.fields(name), out)
	}
}

func stringValue(raw any) string {
	text, _ := raw.(string)
	return text
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}
