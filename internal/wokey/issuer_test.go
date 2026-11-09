package wokey

import (
	"encoding/json"
	"math"
	"testing"
)

// testIssuerToken is a fake issuing token shaped like a real one.
const testIssuerToken = "wkt_test_good_00000000"

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
		if got := ValidIssuerToken(token); got != want {
			t.Errorf("ValidIssuerToken(%q) = %v, want %v", token, got, want)
		}
	}
}

func TestIssuerInfoFrom(t *testing.T) {
	var withProvider, noProvider map[string]any
	_ = json.Unmarshal([]byte(`{"provider":{"availableUsd":"9.56639200","settlementFrozen":true},"referrer":{"availableUsd":"1.25000000"},
		"api":{"enabled":true,"eligible":true,"availableUsd":"0.33","disqualifiedBy":null}}`), &withProvider)
	_ = json.Unmarshal([]byte(`{"provider":null,"referrer":{"availableUsd":"0.00000000"},"api":{"enabled":false}}`), &noProvider)

	info := IssuerInfoFrom(withProvider, "acct-1")
	if info.ProviderUSD == nil || math.Abs(*info.ProviderUSD-9.566392) > 1e-9 || !info.ProviderFrozen || info.ReferrerUSD != 1.25 {
		t.Fatalf("with provider = %+v", info)
	}
	if info.Account != "acct-1" {
		t.Fatalf("account = %q", info.Account)
	}
	info = IssuerInfoFrom(noProvider, "acct-2")
	if info.ProviderUSD != nil || info.ProviderFrozen || info.ReferrerUSD != 0 {
		t.Fatalf("without provider = %+v", info)
	}
}
