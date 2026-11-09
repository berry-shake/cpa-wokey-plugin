package wokey

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
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

// GetJSON GETs a JSON object from Wokey with a bearer token. path names the
// endpoint in errors.
func GetJSON(ctx context.Context, client *http.Client, target, token, userAgent, path string) (map[string]any, *pluginabi.Error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if errReq != nil {
		return nil, TransportError(errReq)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, TransportError(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, TransportError(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, UpstreamError(resp.StatusCode, resp.Header, body, path)
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(body, &out); errUnmarshal != nil {
		return nil, &pluginabi.Error{Code: "invalid_upstream_response", Message: "wokey: decode " + path + ": " + errUnmarshal.Error(), HTTPStatus: http.StatusBadGateway}
	}
	return out, nil
}

// BalanceFrom reads a /v1/dashboard/balance response.
func BalanceFrom(raw map[string]any) *BalanceInfo {
	return &BalanceInfo{
		Account:      balanceAccount(raw),
		AvailableUSD: numberField(raw, "availableUsd", "available_usd"),
		ReservedUSD:  numberField(raw, "reservedUsd", "reserved_usd"),
	}
}

// KeyInfoFrom reads a /v1/key response.
func KeyInfoFrom(raw map[string]any) *KeyInfo {
	key := &KeyInfo{
		Label:      jsonfield.String(raw, "label"),
		UsageUSD:   numberField(raw, "usage"),
		DailyUSD:   numberField(raw, "usage_daily"),
		WeeklyUSD:  numberField(raw, "usage_weekly"),
		MonthlyUSD: numberField(raw, "usage_monthly"),
	}
	if limit, ok := optionalNumber(raw["limit"]); ok {
		key.LimitUSD = &limit
	}
	if remaining, ok := optionalNumber(raw["limit_remaining"]); ok {
		key.LimitRemaining = &remaining
	}
	if reset, ok := raw["limit_reset"].(string); ok {
		key.LimitResetLabel = strings.TrimSpace(reset)
	}
	return key
}

// QuotaFromAccount renders account info for CPA's quota panel. Each rolling
// window (24h / 7d / 30d; Wokey has no shorter one) becomes a bar showing how
// much of the money held at the window's start is left:
// balance / (balance + spent in the window). Top-ups during the window and
// several keys sharing one balance make it approximate. Without a balance the
// spend is shown as plain numbers. A key with a spend cap gets its own bar.
func QuotaFromAccount(info AccountInfo) pluginapi.QuotaFetchResponse {
	var out pluginapi.QuotaFetchResponse
	key := info.Key
	if key == nil {
		return out
	}
	windows := []struct {
		key, label string
		spent      float64
	}{
		{"spent_24h", "24h", key.DailyUSD},
		{"spent_7d", "7d", key.WeeklyUSD},
		{"spent_30d", "30d", key.MonthlyUSD},
	}
	if info.Balance != nil {
		balance := math.Max(0, info.Balance.AvailableUSD)
		group := pluginapi.QuotaGroup{DisplayName: "Spend vs balance"}
		for _, window := range windows {
			fraction := 0.0
			if start := balance + math.Max(0, window.spent); start > 0 {
				fraction = balance / start
			}
			group.Buckets = append(group.Buckets, pluginapi.QuotaBucket{
				Window:            window.label,
				RemainingFraction: fraction,
				Description:       fmt.Sprintf("$%.2f spent · $%.2f balance", window.spent, info.Balance.AvailableUSD),
			})
		}
		out.Groups = append(out.Groups, group)
	} else {
		for _, window := range windows {
			out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: window.key, Label: window.label, Value: window.spent, Format: "currency", Currency: "USD"})
		}
	}
	if key.LimitUSD != nil && key.LimitRemaining != nil {
		fraction := 0.0
		if *key.LimitUSD > 0 {
			fraction = math.Max(0, math.Min(1, *key.LimitRemaining / *key.LimitUSD))
		}
		window := "total"
		if key.LimitResetLabel != "" {
			window = key.LimitResetLabel
		}
		out.Groups = append(out.Groups, pluginapi.QuotaGroup{
			DisplayName: "Key spend cap",
			Buckets: []pluginapi.QuotaBucket{{
				Window:            window,
				RemainingFraction: fraction,
				Description:       fmt.Sprintf("$%.2f of $%.2f left", *key.LimitRemaining, *key.LimitUSD),
			}},
		})
	}
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

func numberField(fields map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := optionalNumber(fields[key]); ok {
			return value
		}
	}
	return 0
}

// optionalNumber accepts JSON numbers and decimal strings; null is absent.
func optionalNumber(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case string:
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if errParse != nil {
			return 0, false
		}
		return parsed, true
	case json.Number:
		parsed, errParse := value.Float64()
		return parsed, errParse == nil
	default:
		return 0, false
	}
}
