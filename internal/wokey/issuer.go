package wokey

import (
	"strings"
	"unicode"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
)

// Wokey's issuing API (gift cards and direct top-ups) reports what an account
// can issue from: Provider earnings, referral commission and API credits. It
// takes its own issuing token (wkt_…), which an auth file may carry in
// issuer_token. Keys without one never call it.
const (
	DefaultIssuerBaseURL  = "https://wokey.ai/api/issuer/v1"
	EndpointIssuerBalance = "/balance"
	IssuerTokenPrefix     = "wkt_"
)

// IssuerInfo is the part of the issuing balance the page shows. Provider
// earnings are separate from the API balance and are not added to it.
type IssuerInfo struct {
	// Account identifies the token, so the page counts a token shared by
	// several keys once.
	Account string `json:"account"`
	// ProviderUSD is the withdrawable Provider earnings; nil when the account
	// has no Provider account.
	ProviderUSD    *float64 `json:"provider_usd,omitempty"`
	ProviderFrozen bool     `json:"provider_frozen,omitempty"`
	ReferrerUSD    float64  `json:"referrer_usd"`
}

// ValidIssuerToken reports whether token looks like a Wokey issuing token.
func ValidIssuerToken(token string) bool {
	return len(token) > len(IssuerTokenPrefix) && strings.HasPrefix(token, IssuerTokenPrefix) && strings.IndexFunc(token, unicode.IsSpace) < 0
}

// IssuerInfoFrom reads {"provider":{"availableUsd"}|null,"referrer":{"availableUsd"},...};
// amounts arrive as decimal strings. account is a non-reversible identifier
// of the issuing token.
func IssuerInfoFrom(raw map[string]any, account string) *IssuerInfo {
	info := &IssuerInfo{Account: account}
	if provider, ok := raw["provider"].(map[string]any); ok {
		earnings := numberField(provider, "availableUsd", "available_usd")
		info.ProviderUSD = &earnings
		info.ProviderFrozen = jsonfield.Bool(provider, "settlementFrozen")
	}
	if referrer, ok := raw["referrer"].(map[string]any); ok {
		info.ReferrerUSD = numberField(referrer, "availableUsd", "available_usd")
	}
	return info
}
