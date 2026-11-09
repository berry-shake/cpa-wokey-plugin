package wokey

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func metricValues(resp pluginapi.QuotaFetchResponse) map[string]float64 {
	out := map[string]float64{}
	for _, metric := range resp.Summary {
		out[metric.Key] = metric.Value
	}
	return out
}

func TestQuotaFromAccountWithCap(t *testing.T) {
	limit, remaining := 10.0, 2.5
	resp := QuotaFromAccount(AccountInfo{
		Balance: &BalanceInfo{AvailableUSD: 42.5, ReservedUSD: 0.75},
		Key:     &KeyInfo{Label: "main", UsageUSD: 7.5, DailyUSD: 0.5, WeeklyUSD: 3, MonthlyUSD: 7.5, LimitUSD: &limit, LimitRemaining: &remaining, LimitResetLabel: "monthly"},
	})
	// With a balance the windows are bars, not plain numbers.
	if len(resp.Summary) != 0 {
		t.Fatalf("summary = %+v", resp.Summary)
	}
	if len(resp.Groups) != 2 || len(resp.Groups[0].Buckets) != 3 || len(resp.Groups[1].Buckets) != 1 {
		t.Fatalf("groups = %+v", resp.Groups)
	}
	want := []struct {
		window   string
		fraction float64
		spent    string
	}{{"24h", 42.5 / 43, "$0.50 spent"}, {"7d", 42.5 / 45.5, "$3.00 spent"}, {"30d", 0.85, "$7.50 spent"}}
	for i, w := range want {
		bucket := resp.Groups[0].Buckets[i]
		if bucket.Window != w.window || math.Abs(bucket.RemainingFraction-w.fraction) > 1e-9 ||
			!strings.Contains(bucket.Description, w.spent) || !strings.Contains(bucket.Description, "$42.50 balance") {
			t.Errorf("bucket %d = %+v", i, bucket)
		}
	}
	bucket := resp.Groups[1].Buckets[0]
	if resp.Groups[1].DisplayName != "Key spend cap" || bucket.Window != "monthly" || bucket.RemainingFraction != 0.25 || !strings.Contains(bucket.Description, "$2.50 of $10.00") {
		t.Fatalf("cap bucket = %+v", bucket)
	}
	if resp.Subscription != nil {
		t.Fatalf("subscription = %+v", resp.Subscription)
	}
}

func TestQuotaFromAccountEmptyBalance(t *testing.T) {
	// No money left reads as an empty bar, with or without recent spend.
	for _, spent := range []float64{0, 4} {
		resp := QuotaFromAccount(AccountInfo{Balance: &BalanceInfo{AvailableUSD: -1}, Key: &KeyInfo{DailyUSD: spent, WeeklyUSD: spent, MonthlyUSD: spent}})
		if len(resp.Groups) != 1 {
			t.Fatalf("spent %v: groups = %+v", spent, resp.Groups)
		}
		for _, bucket := range resp.Groups[0].Buckets {
			if bucket.RemainingFraction != 0 {
				t.Errorf("spent %v: bucket = %+v", spent, bucket)
			}
		}
	}
}

func TestQuotaFromAccountWithoutCap(t *testing.T) {
	resp := QuotaFromAccount(AccountInfo{Key: &KeyInfo{UsageUSD: 12, DailyUSD: 1, WeeklyUSD: 2, MonthlyUSD: 3}})
	got := metricValues(resp)
	if len(got) != 3 || got["spent_24h"] != 1 || got["spent_7d"] != 2 || got["spent_30d"] != 3 || len(resp.Groups) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	// The balance alone renders nothing in CPA's quota panel.
	if resp = QuotaFromAccount(AccountInfo{Balance: &BalanceInfo{AvailableUSD: 5}}); len(resp.Summary) != 0 {
		t.Fatalf("balance only = %+v", resp)
	}

	// A zero cap and an overspent key clamp to an empty bucket.
	limit, remaining := 0.0, -1.0
	resp = QuotaFromAccount(AccountInfo{Key: &KeyInfo{LimitUSD: &limit, LimitRemaining: &remaining}})
	if len(resp.Groups) != 1 || resp.Groups[0].Buckets[0].RemainingFraction != 0 || resp.Groups[0].Buckets[0].Window != "total" {
		t.Fatalf("zero cap = %+v", resp.Groups)
	}
	if resp = QuotaFromAccount(AccountInfo{}); len(resp.Summary) != 0 || len(resp.Groups) != 0 {
		t.Fatalf("empty info = %+v", resp)
	}
}

func TestOptionalNumber(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{" 0.75 ", 0.75, true},
		{json.Number("3"), 3, true},
		{nil, 0, false},
		{"n/a", 0, false},
		{true, 0, false},
	}
	for _, tc := range cases {
		if got, ok := optionalNumber(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("optionalNumber(%#v) = %v, %v", tc.in, got, ok)
		}
	}
	if got := numberField(map[string]any{"available_usd": "4"}, "availableUsd", "available_usd"); got != 4 {
		t.Fatalf("numberField fallback key = %v", got)
	}
}

func TestBalanceAccount(t *testing.T) {
	cases := []struct {
		fields map[string]any
		want   string
	}{
		{map[string]any{"userId": "1234"}, "user:1234"},
		{map[string]any{"userId": float64(1234)}, "user:1234"},
		{map[string]any{"userId": "1234", "source": "market_order", "marketOrderId": "mo-9"}, "order:mo-9"},
		{map[string]any{}, ""},
	}
	for _, c := range cases {
		if got := balanceAccount(c.fields); got != c.want {
			t.Errorf("balanceAccount(%v) = %q, want %q", c.fields, got, c.want)
		}
	}
}
