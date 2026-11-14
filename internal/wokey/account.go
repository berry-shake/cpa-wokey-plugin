package wokey

import (
	"math"
	"strings"

	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// AccountInfo is the balance and key usage of one Wokey key. Balance is
// account-wide and shared by every key of the account; usage and the spend
// cap are per key.
type AccountInfo struct {
	Balance      *BalanceInfo `json:"balance,omitempty"`
	BalanceError string       `json:"balance_error,omitempty"`
	Key          *KeyInfo     `json:"key,omitempty"`
	KeyError     string       `json:"key_error,omitempty"`
	// Issuer is set only for keys with an issuing token, and only when the
	// management page asks.
	Issuer      *IssuerInfo `json:"issuer,omitempty"`
	IssuerError string      `json:"issuer_error,omitempty"`
	FetchedAt   string      `json:"fetched_at"`
}

// BalanceInfo is the account balance from GET /v1/dashboard/balance.
type BalanceInfo struct {
	// Account identifies whose balance this is, so the management page can
	// add up balances without counting a shared account once per key.
	Account      string  `json:"account,omitempty"`
	AvailableUSD float64 `json:"available_usd"`
	ReservedUSD  float64 `json:"reserved_usd"`
}

// KeyInfo is the key usage and spend cap from GET /v1/key.
type KeyInfo struct {
	Label           string   `json:"label,omitempty"`
	UsageUSD        float64  `json:"usage_usd"`
	DailyUSD        float64  `json:"daily_usd"`
	WeeklyUSD       float64  `json:"weekly_usd"`
	MonthlyUSD      float64  `json:"monthly_usd"`
	LimitUSD        *float64 `json:"limit_usd,omitempty"`
	LimitRemaining  *float64 `json:"limit_remaining_usd,omitempty"`
	LimitResetLabel string   `json:"limit_reset,omitempty"`
}

// BalanceFrom reads a /v1/dashboard/balance response.
func BalanceFrom(raw map[string]any) *BalanceInfo {
	return &BalanceInfo{
		Account:      balanceAccount(raw),
		AvailableUSD: upstream.NumberField(raw, "availableUsd", "available_usd"),
		ReservedUSD:  upstream.NumberField(raw, "reservedUsd", "reserved_usd"),
	}
}

// KeyInfoFrom reads a /v1/key response.
func KeyInfoFrom(raw map[string]any) *KeyInfo {
	key := &KeyInfo{
		Label:      jsonfield.String(raw, "label"),
		UsageUSD:   upstream.NumberField(raw, "usage"),
		DailyUSD:   upstream.NumberField(raw, "usage_daily"),
		WeeklyUSD:  upstream.NumberField(raw, "usage_weekly"),
		MonthlyUSD: upstream.NumberField(raw, "usage_monthly"),
	}
	if limit, ok := upstream.OptionalNumber(raw["limit"]); ok {
		key.LimitUSD = &limit
	}
	if remaining, ok := upstream.OptionalNumber(raw["limit_remaining"]); ok {
		key.LimitRemaining = &remaining
	}
	if reset, ok := raw["limit_reset"].(string); ok {
		key.LimitResetLabel = strings.TrimSpace(reset)
	}
	return key
}

// QuotaFromAccount renders account info for CPA's quota panel: one Wokey
// group with a single monthly bar, the share of the money held at the start
// of the month that is left: balance / (balance + this key's usage_monthly).
// Top-ups during the month and several keys sharing one balance make it
// approximate. Without the balance or the key's usage there is nothing to
// draw. The daily and weekly spend and a key's spend cap stay on the plugin's
// own page.
func QuotaFromAccount(info AccountInfo) pluginapi.QuotaFetchResponse {
	var out pluginapi.QuotaFetchResponse
	if info.Key == nil || info.Balance == nil {
		return out
	}
	balance := math.Max(0, info.Balance.AvailableUSD)
	fraction := 0.0
	if start := balance + math.Max(0, info.Key.MonthlyUSD); start > 0 {
		fraction = balance / start
	}
	out.Groups = []pluginapi.QuotaGroup{{
		DisplayName: "Wokey",
		Buckets:     []pluginapi.QuotaBucket{{Window: "monthly", RemainingFraction: fraction}},
	}}
	return out
}

// balanceAccount names the balance's owner: a procurement order has its own
// prepaid balance, every other key shares its user's account balance.
func balanceAccount(fields map[string]any) string {
	if order := jsonfield.Text(fields["marketOrderId"]); order != "" {
		return "order:" + order
	}
	if user := jsonfield.Text(fields["userId"]); user != "" {
		return "user:" + user
	}
	return ""
}
