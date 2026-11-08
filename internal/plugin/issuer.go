package plugin

import (
	"context"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// Wokey's issuing API (gift cards and direct top-ups) reports what an account
// can issue from: Provider earnings, referral commission and API credits. It
// takes its own issuing token (wkt_…), which an auth file may carry in
// issuer_token. Keys without one never call it.
const (
	defaultIssuerBaseURL  = "https://wokey.ai/api/issuer/v1"
	endpointIssuerBalance = "/balance"
	issuerTokenPrefix     = "wkt_"
)

// issuerInfo is the part of the issuing balance the page shows. Provider
// earnings are separate from the API balance and are not added to it.
type issuerInfo struct {
	// Account identifies the token, so the page counts a token shared by
	// several keys once.
	Account string `json:"account"`
	// ProviderUSD is the withdrawable Provider earnings; nil when the account
	// has no Provider account.
	ProviderUSD    *float64 `json:"provider_usd,omitempty"`
	ProviderFrozen bool     `json:"provider_frozen,omitempty"`
	ReferrerUSD    float64  `json:"referrer_usd"`
}

// validIssuerToken reports whether token looks like a Wokey issuing token.
func validIssuerToken(token string) bool {
	return len(token) > len(issuerTokenPrefix) && strings.HasPrefix(token, issuerTokenPrefix) && strings.IndexFunc(token, unicode.IsSpace) < 0
}

// issuerBalance calls GET /api/issuer/v1/balance with the credential's issuing
// token through the credential's egress. Wokey also settles voided gift cards
// whose cooldown has ended on this call.
func (s *Service) issuerBalance(ctx context.Context, cred credential) (*issuerInfo, *pluginabi.Error) {
	if !validIssuerToken(cred.IssuerToken) {
		return nil, requestError("invalid_issuer_token", "wokey: issuer_token must start with "+issuerTokenPrefix)
	}
	raw, perr := s.getJSON(ctx, cred.ProxyURL, s.issuerBase+endpointIssuerBalance, cred.IssuerToken, endpointIssuerBalance)
	if perr != nil {
		return nil, perr
	}
	return issuerInfoFrom(raw, cred.IssuerToken), nil
}

// issuerInfoFrom reads {"provider":{"availableUsd"}|null,"referrer":{"availableUsd"},...};
// amounts arrive as decimal strings.
func issuerInfoFrom(raw map[string]any, token string) *issuerInfo {
	info := &issuerInfo{Account: keyFingerprint(token)}
	if provider, ok := raw["provider"].(map[string]any); ok {
		earnings := numberField(provider, "availableUsd", "available_usd")
		info.ProviderUSD = &earnings
		info.ProviderFrozen = boolField(provider, "settlementFrozen")
	}
	if referrer, ok := raw["referrer"].(map[string]any); ok {
		info.ReferrerUSD = numberField(referrer, "availableUsd", "available_usd")
	}
	return info
}
