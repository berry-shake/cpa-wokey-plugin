package plugin

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
)

func sseHandler(events ...string) http.HandlerFunc {
	return func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		for _, event := range events {
			_, _ = io.WriteString(rw, event)
			if flusher, ok := rw.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}

func waitClosed(t *testing.T, host *fakeHost, streamID string) rpcStreamCloseRequest {
	t.Helper()
	select {
	case id := <-host.closed:
		if id != streamID {
			t.Fatalf("closed %q, want %q", id, streamID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream was not closed")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.closes[streamID]
}

func TestExecuteStreamMessages(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointMessages, sseHandler(
		": keepalive\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"data: [DONE]\n\n",
	))
	svc, host := newTestService(t, upstream)
	svc.reqlog.Configure("", 10, 0)

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("claude", "wokey/model-a", `{"model":"wokey/model-a","stream":true}`), StreamID: "s1"}
	envelope := decodeEnvelope(t, svc.executeStream(req))
	if !envelope.OK {
		t.Fatalf("stream open failed: %+v", envelope.Error)
	}
	if headers := mustDecode[rpcExecutorStreamResponse](t, envelope.Result).Headers; !strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("headers = %v", headers)
	}
	closeReq := waitClosed(t, host, "s1")
	if closeReq.ErrorDetail != nil {
		t.Fatalf("stream closed with error %+v", closeReq.ErrorDetail)
	}
	host.mu.Lock()
	emitted := host.emits["s1"]
	host.mu.Unlock()
	if len(emitted) != 2 || !strings.HasPrefix(string(emitted[0]), "event: message_start") || !strings.HasPrefix(string(emitted[1]), "event: message_stop") {
		t.Fatalf("emitted %q", emitted)
	}
	svc.streams.Wait() // the entry is written after the stream closes
	e := svc.reqlog.Query(requestlog.Query{}).Items[0]
	if e.Status != "success" || e.UpstreamStatus != 200 || e.Frames == nil || *e.Frames != 0 || e.UsageReported == nil || *e.UsageReported {
		t.Fatalf("log entry = %+v", e)
	}
}

func TestExecuteStreamChatAddsUsageAndBuffersWithoutBridge(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(wokey.EndpointChat, observe(seen, sseHandler("data: {\"id\":1}\n\n", "data: [DONE]\n\n")))
	svc, _ := newTestService(t, upstream)

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("openai", "wokey/model-a", `{"model":"wokey/model-a","stream":true}`)}
	envelope := decodeEnvelope(t, svc.executeStream(req))
	if !envelope.OK {
		t.Fatalf("stream failed: %+v", envelope.Error)
	}
	chunks := mustDecode[rpcExecutorStreamResponse](t, envelope.Result).Chunks
	if len(chunks) != 2 || string(chunks[1].Payload) != "data: [DONE]\n\n" {
		t.Fatalf("chunks = %+v", chunks)
	}
	got := receive(t, seen)
	options, _ := got.Body["stream_options"].(map[string]any)
	if got.Body["model"] != "model-a" || options["include_usage"] != true || got.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("upstream saw %+v", got)
	}
}

func TestExecuteStreamHTTPErrorIsSynchronous(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointResponses, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusPaymentRequired, map[string]any{"error": map[string]any{"code": "insufficient_balance", "message": "top up"}})
	})
	svc, host := newTestService(t, upstream)
	req := rpcExecutorRequest{ExecutorRequest: executorRequest("openai-response", "wokey/model-a", `{}`), StreamID: "s2"}
	envelope := decodeEnvelope(t, svc.executeStream(req))
	if envelope.OK || envelope.Error.HTTPStatus != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %+v", envelope)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.emits["s2"]) != 0 {
		t.Fatal("nothing must be emitted for a failed open")
	}
}

func TestExecuteStreamIdleTimeout(t *testing.T) {
	upstream := newFakeWokey(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	upstream.handle(wokey.EndpointChat, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(rw, "data: {\"id\":1}\n\n")
		rw.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	svc, host := newTestService(t, upstream)
	svc.cfg.StreamIdleTimeout = 200 * time.Millisecond

	req := rpcExecutorRequest{ExecutorRequest: executorRequest("openai", "wokey/model-a", `{}`), StreamID: "s3"}
	if envelope := decodeEnvelope(t, svc.executeStream(req)); !envelope.OK {
		t.Fatalf("open: %+v", envelope.Error)
	}
	closeReq := waitClosed(t, host, "s3")
	if closeReq.ErrorDetail == nil || closeReq.ErrorDetail.Code != "stream_idle_timeout" || closeReq.ErrorDetail.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("close = %+v", closeReq)
	}
}

func TestExecuteStreamEarlyErrorFailsCall(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointMessages, sseHandler(
		": keepalive\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n",
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n",
	))
	svc, host := newTestService(t, upstream)
	svc.reqlog.Configure("", 10, 0)

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
	svc.streams.Wait()
	// Wokey answered 200 and then reported 429 inside the stream.
	e := svc.reqlog.Query(requestlog.Query{}).Items[0]
	if e.UpstreamStatus != 200 || e.HTTPStatus != 429 || e.ErrorPhase != "request" || e.UsageReported != nil {
		t.Fatalf("log entry = %+v", e)
	}
}

func TestExecuteStreamTruncatedAfterOutput(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointResponses, sseHandler(
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
	upstream.handle(wokey.EndpointChat, sseHandler(
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
