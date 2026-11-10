package streamgate

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func collectEvents(t *testing.T, body string, sse bool) []string {
	t.Helper()
	var events []string
	errPump := Pump(strings.NewReader(body), sse, func(payload []byte) error {
		events = append(events, string(payload))
		return nil
	}, func() {})
	if errPump != nil {
		t.Fatalf("pump: %v", errPump)
	}
	return events
}

func TestPumpSSE(t *testing.T) {
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

func TestPumpLongLine(t *testing.T) {
	// A data line several times the read buffer arrives whole.
	text := strings.Repeat("x", 200<<10)
	got := collectEvents(t, "data: {\"text\":\""+text+"\"}\n\ndata: {\"b\":2}\n\n", true)
	if len(got) != 2 || got[0] != "data: {\"text\":\""+text+"\"}\n\n" || got[1] != "data: {\"b\":2}\n\n" {
		t.Fatalf("got %d events, first %d bytes", len(got), len(got[0]))
	}
}

func TestPumpRaw(t *testing.T) {
	got := collectEvents(t, `{"not":"sse"}`, false)
	if strings.Join(got, "") != `{"not":"sse"}` {
		t.Fatalf("raw = %q", got)
	}
}

func TestPumpStopsOnEmitError(t *testing.T) {
	stop := errors.New("client gone")
	calls := 0
	errPump := Pump(strings.NewReader("data: 1\n\ndata: 2\n\n"), true, func([]byte) error {
		calls++
		return stop
	}, func() {})
	if !errors.Is(errPump, stop) || calls != 1 {
		t.Fatalf("err=%v calls=%d", errPump, calls)
	}
}

func TestIsDoneEvent(t *testing.T) {
	for _, event := range []string{"data: [DONE]\n\n", "data:[DONE]", "  data: [DONE]  \n", "data: [DONE]\ndata: [DONE]\n\n", ""} {
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
	if !IsEventStream("text/event-stream; charset=utf-8") || IsEventStream("application/json") || IsEventStream("") {
		t.Fatal("IsEventStream")
	}
}

func TestStreamFailure(t *testing.T) {
	errPump := errors.New("read: connection reset")
	idle, cancelIdle := context.WithCancelCause(context.Background())
	cancelIdle(ErrIdle)
	closed, cancelClosed := context.WithCancelCause(context.Background())
	cancelClosed(nil)
	cases := []struct {
		name    string
		ctx     context.Context
		code    string
		status  int
		message string
	}{
		{"idle", idle, "stream_idle_timeout", http.StatusGatewayTimeout, "stalled"},
		{"shutdown", closed, "plugin_shutdown", http.StatusServiceUnavailable, "CPA is restarting"},
		{"upstream", context.Background(), "upstream_stream_error", http.StatusBadGateway, "connection reset"},
	}
	for _, tc := range cases {
		got := streamFailure(tc.ctx, errPump)
		if got.Code != tc.code || got.HTTPStatus != tc.status || !got.Retryable || !strings.Contains(got.Message, tc.message) {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}
