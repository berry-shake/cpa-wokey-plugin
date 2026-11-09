package streamgate

import (
	"errors"
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
	if !IsEventStream("text/event-stream; charset=utf-8") || IsEventStream("application/json") || IsEventStream("") {
		t.Fatal("IsEventStream")
	}
}
