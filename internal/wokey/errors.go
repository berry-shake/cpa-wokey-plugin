package wokey

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// MaxErrorBodyBytes bounds the upstream error text copied into a plugin error.
const MaxErrorBodyBytes = 2048

// UpstreamError converts a non-2xx Wokey response into a plugin error whose
// HTTP status drives CPA's credential cooldown:
//
//   - 401/402 and account-level 403s keep their status, so CPA cools the key down.
//   - 403 request_blocked / model_denylisted and 404 are request problems, not
//     key problems, so they become 400 and do not cool the key (a 404 would
//     cool the model on this key for 12 hours).
//   - 529 upstream_overloaded becomes 503 (transient).
//   - 429 keeps Retry-After so CPA waits exactly that long.
func UpstreamError(status int, header http.Header, body []byte, endpoint string) *pluginabi.Error {
	code, message := parseUpstreamErrorBody(body)
	mapped := status
	switch {
	case status == http.StatusForbidden && (code == "request_blocked" || code == "model_denylisted"):
		mapped = http.StatusBadRequest
	case status == http.StatusNotFound && endpoint != EndpointCountTokens:
		mapped = http.StatusBadRequest
	case status == 529:
		mapped = http.StatusServiceUnavailable
	case status == 499:
		// downstream_aborted: the caller went away; report it as a timeout-like
		// transient failure rather than a client error on a healthy key.
		mapped = http.StatusRequestTimeout
	case status < 400 || status > 599:
		mapped = http.StatusBadGateway
	}
	if code == "" {
		code = "upstream_" + strconv.Itoa(status)
	}
	if message == "" {
		message = truncate(string(body), MaxErrorBodyBytes)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	err := &pluginabi.Error{
		Code:       code,
		Message:    "wokey: " + message,
		HTTPStatus: mapped,
		Retryable:  mapped == http.StatusTooManyRequests || mapped == http.StatusRequestTimeout || mapped >= 500,
	}
	if mapped == http.StatusTooManyRequests {
		err.RetryAfterSeconds = retryAfterSeconds(header.Get("Retry-After"), time.Now())
	}
	return err
}

// parseUpstreamErrorBody extracts a code and message from the error shapes
// Wokey uses: OpenAI style {"error":{"code","type","message"}}, Anthropic
// style {"type":"error","error":{"type","message"}} and flat {"code","message"}.
func parseUpstreamErrorBody(body []byte) (string, string) {
	var parsed struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return "", ""
	}
	code := jsonfield.Text(parsed.Code)
	message := strings.TrimSpace(parsed.Message)
	switch inner := parsed.Error.(type) {
	case map[string]any:
		if value := jsonfield.Text(inner["code"]); value != "" {
			code = value
		} else if value := jsonfield.Text(inner["type"]); value != "" && code == "" {
			code = value
		}
		if value, ok := inner["message"].(string); ok && strings.TrimSpace(value) != "" {
			message = strings.TrimSpace(value)
		}
	case string:
		if message == "" {
			message = strings.TrimSpace(inner)
		}
	}
	return code, truncate(message, MaxErrorBodyBytes)
}

// retryAfterSeconds parses Retry-After as delta seconds or an HTTP date.
func retryAfterSeconds(raw string, now time.Time) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, errAtoi := strconv.Atoi(raw); errAtoi == nil {
		if seconds < 0 {
			return 0
		}
		return seconds
	}
	if when, errParse := http.ParseTime(raw); errParse == nil {
		if delta := when.Sub(now); delta > 0 {
			return int(delta.Round(time.Second) / time.Second)
		}
	}
	return 0
}

// TransportError reports a network failure reaching Wokey as a transient 502.
func TransportError(err error) *pluginabi.Error {
	return &pluginabi.Error{
		Code:       "upstream_unreachable",
		Message:    "wokey: " + err.Error(),
		HTTPStatus: http.StatusBadGateway,
		Retryable:  true,
	}
}

// RequestError reports a request the plugin rejects before calling Wokey.
func RequestError(code, message string) *pluginabi.Error {
	return &pluginabi.Error{Code: code, Message: message, HTTPStatus: http.StatusBadRequest}
}

// AuthError reports an unusable credential, so CPA cools it down.
func AuthError(message string) *pluginabi.Error {
	return &pluginabi.Error{Code: "invalid_credential", Message: message, HTTPStatus: http.StatusUnauthorized}
}

// truncate trims text and cuts it to limit bytes, marking the cut.
func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
