package streamgate

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
)

func TestClassifyStreamEvent(t *testing.T) {
	cases := []struct {
		endpoint string
		event    string
		want     Kind
	}{
		{wokey.EndpointChat, `data: {"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, Prelude},
		{wokey.EndpointChat, `data: {"choices":[{"delta":{"content":"Hi"}}]}`, Content},
		{wokey.EndpointChat, `data: {"choices":[{"delta":{"reasoning_content":"…"}}]}`, Content},
		{wokey.EndpointChat, `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, Content},
		{wokey.EndpointChat, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, Terminal},
		{wokey.EndpointChat, `data: {"choices":[],"usage":{"total_tokens":3}}`, Prelude},
		{wokey.EndpointChat, `data: [DONE]`, Terminal},
		{wokey.EndpointChat, `data: {"error":{"code":"rate_limited","message":"slow down"}}`, Failed},
		{wokey.EndpointChat, `data: not json`, Content},
		{wokey.EndpointResponses, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"error\":null}}", Prelude},
		{wokey.EndpointResponses, `data: {"type":"response.output_item.added"}`, Prelude},
		{wokey.EndpointResponses, `data: {"type":"response.output_text.delta","delta":"Hi"}`, Content},
		{wokey.EndpointResponses, `data: {"type":"response.completed","response":{"error":null}}`, Terminal},
		{wokey.EndpointResponses, `data: {"type":"response.incomplete"}`, Terminal},
		{wokey.EndpointResponses, `data: {"type":"response.failed","response":{"error":{"code":"server_error"}}}`, Failed},
		{wokey.EndpointResponses, `data: {"type":"error","code":"rate_limit_exceeded","message":"x"}`, Failed},
		{wokey.EndpointMessages, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}", Prelude},
		{wokey.EndpointMessages, "event: ping\ndata: {\"type\": \"ping\"}", Prelude},
		{wokey.EndpointMessages, `data: {"type":"content_block_start","content_block":{"type":"text","text":""}}`, Prelude},
		{wokey.EndpointMessages, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`, Content},
		{wokey.EndpointMessages, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, Terminal},
		{wokey.EndpointMessages, `data: {"type":"message_delta","delta":{"stop_reason":null}}`, Content},
		{wokey.EndpointMessages, `data: {"type":"message_stop"}`, Terminal},
		{wokey.EndpointMessages, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}", Failed},
		{wokey.EndpointMessages, `event: message_start`, Prelude},
	}
	for _, tc := range cases {
		if got, _ := classifyStreamEvent(tc.endpoint, NewEvent([]byte(tc.event+"\n\n"))); got != tc.want {
			t.Errorf("%s %q = %d, want %d", tc.endpoint, tc.event, got, tc.want)
		}
	}
}

func TestStreamErrorStatus(t *testing.T) {
	for body, want := range map[string]int{
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`:     529,
		`{"type":"error","error":{"type":"rate_limit_error","message":"x"}}`:              http.StatusTooManyRequests,
		`{"error":{"code":"rate_limited","message":"x"}}`:                                 http.StatusTooManyRequests,
		`{"type":"error","code":"rate_limit_exceeded","message":"x"}`:                     http.StatusTooManyRequests,
		`{"error":{"code":"insufficient_balance"}}`:                                       http.StatusPaymentRequired,
		`{"error":{"code":"request_blocked","type":"invalid_request_error"}}`:             http.StatusForbidden,
		`{"error":{"type":"invalid_request_error","message":"prompt is too long"}}`:       http.StatusBadRequest,
		`{"error":{"code":"upstream_timeout"}}`:                                           http.StatusGatewayTimeout,
		`{"error":{"code":"server_error","message":"x"}}`:                                 http.StatusBadGateway,
		`{"error":{"code":429,"message":"x"}}`:                                            http.StatusTooManyRequests,
		`{"error":{"message":"x","status":503}}`:                                          http.StatusServiceUnavailable,
		`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`: http.StatusBadGateway,
		`not json`: http.StatusBadGateway,
	} {
		if got := streamErrorStatus([]byte(body)); got != want {
			t.Errorf("streamErrorStatus(%s) = %d, want %d", body, got, want)
		}
	}
}

func runGate(t *testing.T, endpoint string, events ...string) ([]string, *Gate) {
	t.Helper()
	var emitted []string
	gate := New(endpoint, true, http.Header{}, func(payload []byte) error {
		emitted = append(emitted, string(payload))
		return nil
	})
	for _, event := range events {
		if gate.Push([]byte(event)) != nil {
			break
		}
	}
	return emitted, gate
}

func TestGateHoldsPreludeUntilOutput(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	ping := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	delta := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	emitted, gate := runGate(t, wokey.EndpointMessages, start, ping)
	if len(emitted) != 0 {
		t.Fatalf("prelude was not held: %q", emitted)
	}
	// A stream that stops before any output is truncated; the prelude is dropped.
	if failure := gate.Finish(context.Background(), nil); failure == nil || failure.Code != "stream_truncated" {
		t.Fatalf("prelude-only stream = %+v", failure)
	}

	emitted, gate = runGate(t, wokey.EndpointMessages, start, ping, delta, stop, "data: [DONE]\n\n")
	if strings.Join(emitted, "") != start+ping+delta+stop {
		t.Fatalf("emitted %q", emitted)
	}
	if gate.Finish(context.Background(), nil) != nil {
		t.Fatal("complete stream reported an error")
	}

	// Chat completions keep [DONE].
	emitted, _ = runGate(t, wokey.EndpointChat, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n", "data: [DONE]\n\n")
	if len(emitted) != 2 || emitted[1] != "data: [DONE]\n\n" {
		t.Fatalf("chat emitted %q", emitted)
	}
}

func TestGatePreludeCap(t *testing.T) {
	ping := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" + strings.Repeat("x", 1000) + "\"}\n\n"
	events := make([]string, maxPreludeBytes/len(ping)+2)
	for i := range events {
		events[i] = ping
	}
	emitted, gate := runGate(t, wokey.EndpointMessages, events...)
	if len(emitted) != len(events) || !gate.open {
		t.Fatalf("emitted %d of %d events past the cap", len(emitted), len(events))
	}
}

func TestGateEarlyError(t *testing.T) {
	emitted, gate := runGate(t, wokey.EndpointMessages,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	failure := gate.Finish(context.Background(), errStreamRejected)
	if len(emitted) != 0 || failure == nil || failure.HTTPStatus != http.StatusServiceUnavailable || failure.Code != "overloaded_error" || !failure.Retryable {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	if !strings.Contains(failure.Message, "Overloaded") {
		t.Fatalf("message = %q", failure.Message)
	}

	_, gate = runGate(t, wokey.EndpointResponses,
		"data: {\"type\":\"response.created\"}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}}}\n\n",
	)
	if failure = gate.Finish(context.Background(), errStreamRejected); failure == nil || failure.HTTPStatus != http.StatusTooManyRequests || failure.Code != "rate_limit_exceeded" {
		t.Fatalf("response.failed = %+v", failure)
	}
}

func TestGateErrorAfterOutputPassesThrough(t *testing.T) {
	delta := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	failed := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	emitted, gate := runGate(t, wokey.EndpointMessages, delta, failed)
	if len(emitted) != 2 || emitted[1] != failed || gate.Finish(context.Background(), nil) != nil {
		t.Fatalf("emitted %q", emitted)
	}
}

func TestGateTruncated(t *testing.T) {
	emitted, gate := runGate(t, wokey.EndpointChat, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
	failure := gate.Finish(context.Background(), nil)
	if len(emitted) != 1 || failure == nil || failure.Code != "stream_truncated" || failure.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	// A finish reason is enough even without [DONE].
	_, gate = runGate(t, wokey.EndpointChat, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	if failure = gate.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("finished chat = %+v", failure)
	}
	// Raw bodies cannot be checked and end normally.
	raw := New(wokey.EndpointChat, false, http.Header{}, func([]byte) error { return nil })
	_ = raw.Push([]byte(`{"partial":`))
	if failure = raw.Finish(context.Background(), nil); failure != nil {
		t.Fatalf("raw = %+v", failure)
	}
}
