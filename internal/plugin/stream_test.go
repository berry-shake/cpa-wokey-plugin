package plugin

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func collectEvents(t *testing.T, body string, sse bool) []string {
	t.Helper()
	var events []string
	errPump := pumpStream(strings.NewReader(body), sse, func(payload []byte) error {
		events = append(events, string(payload))
		return nil
	}, func() {})
	if errPump != nil {
		t.Fatalf("pump: %v", errPump)
	}
	return events
}

func TestPumpStreamSSE(t *testing.T) {
	body := ": keepalive\n\n" +
		"event: message_start\r\ndata: {\"a\":1}\r\n\r\n" +
		": ping\n" +
		"data: {\"b\":2}\n\n\n" +
		"data: [DONE]\n\n" +
		"data: {\"tail\":true}"
	got := collectEvents(t, body, true)
	want := []string{
		"event: message_start\ndata: {\"a\":1}\n\n",
		"data: {\"b\":2}\n\n",
		"data: [DONE]\n\n",
		"data: {\"tail\":true}\n\n",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q", got)
	}
}

func TestPumpStreamRaw(t *testing.T) {
	got := collectEvents(t, `{"not":"sse"}`, false)
	if strings.Join(got, "") != `{"not":"sse"}` {
		t.Fatalf("raw = %q", got)
	}
}

func TestPumpStreamStopsOnEmitError(t *testing.T) {
	stop := errors.New("client gone")
	calls := 0
	errPump := pumpStream(strings.NewReader("data: 1\n\ndata: 2\n\n"), true, func([]byte) error {
		calls++
		return stop
	}, func() {})
	if !errors.Is(errPump, stop) || calls != 1 {
		t.Fatalf("err=%v calls=%d", errPump, calls)
	}
}

func TestIsDoneEvent(t *testing.T) {
	for _, event := range []string{"data: [DONE]\n\n", "data:[DONE]", "  data: [DONE]  \n"} {
		if !isDoneEvent([]byte(event)) {
			t.Errorf("isDoneEvent(%q) = false", event)
		}
	}
	for _, event := range []string{"event: done\ndata: [DONE]\n\n", "data: {}\n\n", "data: [DONE] x"} {
		if isDoneEvent([]byte(event)) {
			t.Errorf("isDoneEvent(%q) = true", event)
		}
	}
}

func TestIsEventStream(t *testing.T) {
	if !isEventStream("text/event-stream; charset=utf-8") || isEventStream("application/json") || isEventStream("") {
		t.Fatal("isEventStream")
	}
}

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
	upstream.handle(endpointMessages, sseHandler(
		": keepalive\n\n",
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"data: [DONE]\n\n",
	))
	svc, host := newTestService(t, upstream)

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
}

func TestExecuteStreamChatAddsUsageAndBuffersWithoutBridge(t *testing.T) {
	upstream := newFakeWokey(t)
	seen := make(chan observedRequest, 1)
	upstream.handle(endpointChat, observe(seen, sseHandler("data: {\"id\":1}\n\n", "data: [DONE]\n\n")))
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
	upstream.handle(endpointResponses, func(rw http.ResponseWriter, _ *http.Request) {
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
	upstream.handle(endpointChat, func(rw http.ResponseWriter, r *http.Request) {
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
