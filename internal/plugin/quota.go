package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	endpointBalance = "/v1/dashboard/balance"
	endpointKeyInfo = "/v1/key"

	accountCallTimeout = 20 * time.Second
)

type rpcQuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// accountInfo is the balance and key usage of one Wokey key. Balance is
// account-wide and shared by every key of the account; usage and the spend
// cap are per key.
type accountInfo struct {
	Balance      *balanceInfo `json:"balance,omitempty"`
	BalanceError string       `json:"balance_error,omitempty"`
	Key          *keyInfo     `json:"key,omitempty"`
	KeyError     string       `json:"key_error,omitempty"`
	FetchedAt    string       `json:"fetched_at"`
}

type balanceInfo struct {
	AvailableUSD float64 `json:"available_usd"`
	ReservedUSD  float64 `json:"reserved_usd"`
}

type keyInfo struct {
	Label           string   `json:"label,omitempty"`
	UsageUSD        float64  `json:"usage_usd"`
	DailyUSD        float64  `json:"daily_usd"`
	WeeklyUSD       float64  `json:"weekly_usd"`
	MonthlyUSD      float64  `json:"monthly_usd"`
	LimitUSD        *float64 `json:"limit_usd,omitempty"`
	LimitRemaining  *float64 `json:"limit_remaining_usd,omitempty"`
	LimitResetLabel string   `json:"limit_reset,omitempty"`
}

// accountGET calls an authenticated Wokey account endpoint with a credential.
func (s *Service) accountGET(ctx context.Context, cred credential, path string) (map[string]any, *pluginabi.Error) {
	setting, errProxy := resolveProxy(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, requestError("invalid_proxy", errProxy.Error())
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, cred.effectiveBaseURL(s.config())+path, nil)
	if errReq != nil {
		return nil, transportError(errReq)
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.userAgent())
	resp, errDo := s.clients.client(setting).Do(req)
	if errDo != nil {
		return nil, transportError(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, transportError(errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, upstreamError(resp.StatusCode, resp.Header, body, path)
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(body, &out); errUnmarshal != nil {
		return nil, &pluginabi.Error{Code: "invalid_upstream_response", Message: "wokey: decode " + path + ": " + errUnmarshal.Error(), HTTPStatus: http.StatusBadGateway}
	}
	return out, nil
}

// fetchAccount loads balance and key usage in parallel. A failure of one call
// is reported next to the other's result; the error is returned only when
// both fail.
func (s *Service) fetchAccount(cred credential) (accountInfo, *pluginabi.Error) {
	ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
	defer cancel()
	var (
		wg                   sync.WaitGroup
		balanceRaw, keyRaw   map[string]any
		balanceErr, keyErrIn *pluginabi.Error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		balanceRaw, balanceErr = s.accountGET(ctx, cred, endpointBalance)
	}()
	go func() {
		defer wg.Done()
		keyRaw, keyErrIn = s.accountGET(ctx, cred, endpointKeyInfo)
	}()
	wg.Wait()

	info := accountInfo{FetchedAt: s.now().UTC().Format(time.RFC3339)}
	if balanceErr != nil {
		info.BalanceError = balanceErr.Message
	} else {
		info.Balance = &balanceInfo{
			AvailableUSD: numberField(balanceRaw, "availableUsd", "available_usd"),
			ReservedUSD:  numberField(balanceRaw, "reservedUsd", "reserved_usd"),
		}
	}
	if keyErrIn != nil {
		info.KeyError = keyErrIn.Message
	} else {
		key := &keyInfo{
			Label:      stringField(keyRaw, "label"),
			UsageUSD:   numberField(keyRaw, "usage"),
			DailyUSD:   numberField(keyRaw, "usage_daily"),
			WeeklyUSD:  numberField(keyRaw, "usage_weekly"),
			MonthlyUSD: numberField(keyRaw, "usage_monthly"),
		}
		if limit, ok := optionalNumber(keyRaw["limit"]); ok {
			key.LimitUSD = &limit
		}
		if remaining, ok := optionalNumber(keyRaw["limit_remaining"]); ok {
			key.LimitRemaining = &remaining
		}
		if reset, ok := keyRaw["limit_reset"].(string); ok {
			key.LimitResetLabel = strings.TrimSpace(reset)
		}
		info.Key = key
	}
	if balanceErr != nil && keyErrIn != nil {
		// Prefer the key endpoint's status: it reflects the key itself.
		return info, keyErrIn
	}
	return info, nil
}

// quotaFromAccount renders account info for CPA's quota panel.
func quotaFromAccount(info accountInfo) pluginapi.QuotaFetchResponse {
	usd := func(key, label string, value float64) pluginapi.QuotaMetric {
		return pluginapi.QuotaMetric{Key: key, Label: label, Value: value, Format: "currency", Currency: "USD"}
	}
	var out pluginapi.QuotaFetchResponse
	if info.Balance != nil {
		out.Summary = append(out.Summary,
			usd("balance", "Balance", info.Balance.AvailableUSD),
			usd("reserved", "Reserved", info.Balance.ReservedUSD))
	}
	if key := info.Key; key != nil {
		out.Summary = append(out.Summary,
			usd("spent_24h", "Key spent 24h", key.DailyUSD),
			usd("spent_7d", "Key spent 7d", key.WeeklyUSD),
			usd("spent_30d", "Key spent 30d", key.MonthlyUSD))
		if key.LimitUSD != nil && key.LimitRemaining != nil {
			out.Summary = append(out.Summary, usd("cap_left", "Key cap left", *key.LimitRemaining))
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
		} else {
			out.Summary = append(out.Summary, usd("spent_total", "Key spent total", key.UsageUSD))
		}
		if key.Label != "" {
			out.Subscription = &pluginapi.QuotaSubscription{TierName: key.Label}
		}
	}
	return out
}

// quotaFetch does not call rememberHost: CPA sends quota requests without the
// host summary, and an empty one would wipe the cached global proxy.
func (s *Service) quotaFetch(req pluginapi.QuotaFetchRequest) []byte {
	cred, _, errCred := credentialFromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil || cred.APIKey == "" {
		// Fall back to the auth file on disk when CPA sends no storage.
		if fileCred, ok := s.credentialByAuthIndex(req.AuthIndex); ok {
			cred = fileCred
		}
	}
	if cred.APIKey == "" {
		return errorEnvelopeFrom(authError("wokey: credential has no api_key"))
	}
	info, perr := s.fetchAccount(cred)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	return okEnvelope(quotaFromAccount(info))
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
