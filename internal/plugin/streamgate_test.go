package plugin

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestClassifyStreamEvent(t *testing.T) {
	cases := []struct {
		endpoint string
		event    string
		want     streamEventKind
	}{
		{endpointChat, `data: {"choices":[{"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, streamPrelude},
		{endpointChat, `data: {"choices":[{"delta":{"content":"Hi"}}]}`, streamContent},
		{endpointChat, `data: {"choices":[{"delta":{"reasoning_content":"…"}}]}`, streamContent},
		{endpointChat, `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, streamContent},
		{endpointChat, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`, streamTerminal},
		{endpointChat, `data: {"choices":[],"usage":{"total_tokens":3}}`, streamPrelude},
		{endpointChat, `data: [DONE]`, streamTerminal},
		{endpointChat, `data: {"error":{"code":"rate_limited","message":"slow down"}}`, streamFailed},
		{endpointChat, `data: not json`, streamContent},
		{endpointResponses, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"error\":null}}", streamPrelude},
		{endpointResponses, `data: {"type":"response.output_item.added"}`, streamPrelude},
		{endpointResponses, `data: {"type":"response.output_text.delta","delta":"Hi"}`, streamContent},
		{endpointResponses, `data: {"type":"response.completed","response":{"error":null}}`, streamTerminal},
		{endpointResponses, `data: {"type":"response.incomplete"}`, streamTerminal},
		{endpointResponses, `data: {"type":"response.failed","response":{"error":{"code":"server_error"}}}`, streamFailed},
		{endpointResponses, `data: {"type":"error","code":"rate_limit_exceeded","message":"x"}`, streamFailed},
		{endpointMessages, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}", streamPrelude},
		{endpointMessages, "event: ping\ndata: {\"type\": \"ping\"}", streamPrelude},
		{endpointMessages, `data: {"type":"content_block_start","content_block":{"type":"text","text":""}}`, streamPrelude},
		{endpointMessages, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`, streamContent},
		{endpointMessages, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, streamTerminal},
		{endpointMessages, `data: {"type":"message_delta","delta":{"stop_reason":null}}`, streamContent},
		{endpointMessages, `data: {"type":"message_stop"}`, streamTerminal},
		{endpointMessages, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}", streamFailed},
		{endpointMessages, `event: message_start`, streamPrelude},
	}
	for _, tc := range cases {
		if got, _ := classifyStreamEvent(tc.endpoint, []byte(tc.event+"\n\n")); got != tc.want {
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

func runGate(t *testing.T, endpoint string, events ...string) ([]string, *streamGate) {
	t.Helper()
	var emitted []string
	gate := newStreamGate(endpoint, true, http.Header{}, func(payload []byte) error {
		emitted = append(emitted, string(payload))
		return nil
	})
	for _, event := range events {
		if gate.push([]byte(event)) != nil {
			break
		}
	}
	return emitted, gate
}

func TestStreamGateHoldsPreludeUntilOutput(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	ping := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	delta := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	emitted, gate := runGate(t, endpointMessages, start, ping)
	if len(emitted) != 0 {
		t.Fatalf("prelude was not held: %q", emitted)
	}
	// A stream that stops before any output is truncated; the prelude is dropped.
	if failure := gate.finish(context.Background(), nil); failure == nil || failure.Code != "stream_truncated" {
		t.Fatalf("prelude-only stream = %+v", failure)
	}

	emitted, gate = runGate(t, endpointMessages, start, ping, delta, stop, "data: [DONE]\n\n")
	if strings.Join(emitted, "") != start+ping+delta+stop {
		t.Fatalf("emitted %q", emitted)
	}
	if gate.finish(context.Background(), nil) != nil {
		t.Fatal("complete stream reported an error")
	}

	// Chat completions keep [DONE].
	emitted, _ = runGate(t, endpointChat, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n", "data: [DONE]\n\n")
	if len(emitted) != 2 || emitted[1] != "data: [DONE]\n\n" {
		t.Fatalf("chat emitted %q", emitted)
	}
}

func TestStreamGatePreludeCap(t *testing.T) {
	ping := "event: ping\ndata: {\"type\":\"ping\",\"pad\":\"" + strings.Repeat("x", 1000) + "\"}\n\n"
	events := make([]string, maxPreludeBytes/len(ping)+2)
	for i := range events {
		events[i] = ping
	}
	emitted, gate := runGate(t, endpointMessages, events...)
	if len(emitted) != len(events) || !gate.open {
		t.Fatalf("emitted %d of %d events past the cap", len(emitted), len(events))
	}
}

func TestStreamGateEarlyError(t *testing.T) {
	emitted, gate := runGate(t, endpointMessages,
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	failure := gate.finish(context.Background(), errStreamRejected)
	if len(emitted) != 0 || failure == nil || failure.HTTPStatus != http.StatusServiceUnavailable || failure.Code != "overloaded_error" || !failure.Retryable {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	if !strings.Contains(failure.Message, "Overloaded") {
		t.Fatalf("message = %q", failure.Message)
	}

	_, gate = runGate(t, endpointResponses,
		"data: {\"type\":\"response.created\"}\n\n",
		"data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"slow down\"}}}\n\n",
	)
	if failure = gate.finish(context.Background(), errStreamRejected); failure == nil || failure.HTTPStatus != http.StatusTooManyRequests || failure.Code != "rate_limit_exceeded" {
		t.Fatalf("response.failed = %+v", failure)
	}
}

func TestStreamGateErrorAfterOutputPassesThrough(t *testing.T) {
	delta := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n"
	failed := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	emitted, gate := runGate(t, endpointMessages, delta, failed)
	if len(emitted) != 2 || emitted[1] != failed || gate.finish(context.Background(), nil) != nil {
		t.Fatalf("emitted %q", emitted)
	}
}

func TestStreamGateTruncated(t *testing.T) {
	emitted, gate := runGate(t, endpointChat, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
	failure := gate.finish(context.Background(), nil)
	if len(emitted) != 1 || failure == nil || failure.Code != "stream_truncated" || failure.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("emitted %q, failure %+v", emitted, failure)
	}
	// A finish reason is enough even without [DONE].
	_, gate = runGate(t, endpointChat, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	if failure = gate.finish(context.Background(), nil); failure != nil {
		t.Fatalf("finished chat = %+v", failure)
	}
	// Raw bodies cannot be checked and end normally.
	raw := newStreamGate(endpointChat, false, http.Header{}, func([]byte) error { return nil })
	_ = raw.push([]byte(`{"partial":`))
	if failure = raw.finish(context.Background(), nil); failure != nil {
		t.Fatalf("raw = %+v", failure)
	}
}

func TestExecuteStreamEarlyErrorFailsCall(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(endpointMessages, sseHandler(
		": keepalive\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n",
	))
	svc, host := newTestService(t, upstream)

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("claude", "wokey/model-a", `{"model":"wokey/model-a","stream":true}`), StreamID: "e1"}
	if envelope := decodeEnvelope(t, svc.executeStream(req)); !envelope.OK {
		t.Fatalf("open: %+v", envelope.Error)
	}
	closeReq := waitClosed(t, host, "e1")
	if closeReq.ErrorDetail == nil || closeReq.ErrorDetail.HTTPStatus != http.StatusTooManyRequests || closeReq.ErrorDetail.Code != "rate_limit_error" {
		t.Fatalf("close = %+v", closeReq)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.emits["e1"]) != 0 {
		t.Fatalf("nothing must be emitted before the error: %q", host.emits["e1"])
	}
}

func TestExecuteStreamTruncatedAfterOutput(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(endpointResponses, sseHandler(
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\n\n",
	))
	svc, host := newTestService(t, upstream)

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("openai-response", "wokey/model-a", `{"model":"wokey/model-a","stream":true}`), StreamID: "t1"}
	if envelope := decodeEnvelope(t, svc.executeStream(req)); !envelope.OK {
		t.Fatalf("open: %+v", envelope.Error)
	}
	closeReq := waitClosed(t, host, "t1")
	if closeReq.ErrorDetail == nil || closeReq.ErrorDetail.Code != "stream_truncated" || closeReq.ErrorDetail.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("close = %+v", closeReq)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.emits["t1"]) != 2 {
		t.Fatalf("emitted %q", host.emits["t1"])
	}
}

func TestExecuteStreamWithoutBridgeReportsEarlyError(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(endpointChat, sseHandler(
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"error\":{\"code\":\"upstream_overloaded\",\"message\":\"busy\"}}\n\n",
	))
	svc, _ := newTestService(t, upstream)

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","stream":true}`)}
	envelope := decodeEnvelope(t, svc.executeStream(req))
	if envelope.OK || envelope.Error.HTTPStatus != http.StatusServiceUnavailable || envelope.Error.Code != "upstream_overloaded" {
		t.Fatalf("envelope = %+v / %+v", envelope, envelope.Error)
	}
}
