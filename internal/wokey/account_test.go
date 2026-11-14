package wokey

import (
	"encoding/json"
	"math"
	"testing"
)

// CPA's quota panel gets one Wokey bar: monthly, balance against the
// balance plus this key's spend this month. The 24h / 7d spend and the spend
// cap are not drawn, and no bar carries a description, which the panel would
// show in place of the window name.
func TestQuotaFromAccountMonthlyBar(t *testing.T) {
	limit, remaining := 10.0, 2.5
	resp := QuotaFromAccount(AccountInfo{
		Balance: &BalanceInfo{AvailableUSD: 48.67, ReservedUSD: 0.75},
		Key:     &KeyInfo{Label: "main", UsageUSD: 30, DailyUSD: 15.18, WeeklyUSD: 15.18, MonthlyUSD: 8.74, LimitUSD: &limit, LimitRemaining: &remaining, LimitResetLabel: "monthly"},
	})
	if len(resp.Summary) != 0 || resp.Subscription != nil {
		t.Fatalf("summary %+v subscription %+v", resp.Summary, resp.Subscription)
	}
	if len(resp.Groups) != 1 || resp.Groups[0].DisplayName != "Wokey" || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %+v", resp.Groups)
	}
	bucket := resp.Groups[0].Buckets[0]
	if bucket.Window != "monthly" || math.Abs(bucket.RemainingFraction-48.67/(48.67+8.74)) > 1e-9 || bucket.Description != "" || bucket.ResetTime != "" {
		t.Fatalf("bucket = %+v", bucket)
	}
}

func TestQuotaFromAccountEmptyBalance(t *testing.T) {
	// No money left reads as an empty bar, with or without spend this month.
	for _, spent := range []float64{0, 4} {
		resp := QuotaFromAccount(AccountInfo{Balance: &BalanceInfo{AvailableUSD: -1}, Key: &KeyInfo{MonthlyUSD: spent}})
		if len(resp.Groups) != 1 || resp.Groups[0].Buckets[0].RemainingFraction != 0 {
			t.Fatalf("spent %v: groups = %+v", spent, resp.Groups)
		}
	}
	// A balance with no spend this month is full.
	if resp := QuotaFromAccount(AccountInfo{Balance: &BalanceInfo{AvailableUSD: 5}, Key: &KeyInfo{}}); resp.Groups[0].Buckets[0].RemainingFraction != 1 {
		t.Fatalf("unspent = %+v", resp.Groups)
	}
}

func TestQuotaFromAccountNeedsBoth(t *testing.T) {
	for name, info := range map[string]AccountInfo{
		"no balance": {Key: &KeyInfo{MonthlyUSD: 3}},
		"no usage":   {Balance: &BalanceInfo{AvailableUSD: 5}},
		"nothing":    {},
	} {
		if resp := QuotaFromAccount(info); len(resp.Summary) != 0 || len(resp.Groups) != 0 {
			t.Errorf("%s: %+v", name, resp)
		}
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
