package wokey

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamErrorMapping(t *testing.T) {
	retry := http.Header{"Retry-After": []string{"7"}}
	cases := []struct {
		name      string
		status    int
		header    http.Header
		body      string
		want      int
		code      string
		retryable bool
		retryIn   int
	}{
		{"invalid key cools the key", 401, nil, `{"error":{"code":"invalid_api_key","message":"bad key"}}`, 401, "invalid_api_key", false, 0},
		{"no balance cools the key", 402, nil, `{"error":{"type":"insufficient_balance","message":"top up"}}`, 402, "insufficient_balance", false, 0},
		{"blocked request is a client error", 403, nil, `{"error":{"code":"request_blocked","message":"blocked"}}`, 400, "request_blocked", false, 0},
		{"account 403 keeps its status", 403, nil, `{"error":{"code":"key_disabled","message":"disabled"}}`, 403, "key_disabled", false, 0},
		{"unknown model does not cool the key", 404, nil, `{"error":{"code":"model_not_found","message":"no model"}}`, 400, "model_not_found", false, 0},
		{"rate limit keeps Retry-After", 429, retry, `{"code":"rate_limited","message":"slow down"}`, 429, "rate_limited", true, 7},
		{"overloaded is transient", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, 503, "overloaded_error", true, 0},
		{"aborted downstream", 499, nil, ``, 408, "upstream_499", true, 0},
		{"plain text body", 500, nil, `boom`, 500, "upstream_500", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := tc.header
			if header == nil {
				header = http.Header{}
			}
			err := UpstreamError(tc.status, header, []byte(tc.body))
			if err.HTTPStatus != tc.want || err.Code != tc.code || err.Retryable != tc.retryable || err.RetryAfterSeconds != tc.retryIn {
				t.Fatalf("got %+v", err)
			}
			if !strings.HasPrefix(err.Message, "wokey: ") {
				t.Fatalf("message = %q", err.Message)
			}
		})
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := retryAfterSeconds("12", now); got != 12 {
		t.Errorf("delta = %d", got)
	}
	if got := retryAfterSeconds(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90 {
		t.Errorf("date = %d", got)
	}
	for _, raw := range []string{"", "-3", "soon", now.Add(-time.Minute).Format(http.TimeFormat)} {
		if got := retryAfterSeconds(raw, now); got != 0 {
			t.Errorf("retryAfterSeconds(%q) = %d", raw, got)
		}
	}
}
