package streamgate

import (
	"testing"
)

func TestNewEvent(t *testing.T) {
	cases := []struct {
		raw, name, data string
	}{
		{"event: message_start\ndata: {\"a\":1}\n\n", "message_start", `{"a":1}`},
		{"event: x\r\ndata:  {\"a\":1} \r\n\r\n", "x", `{"a":1}`},
		{"data: {\"a\":\ndata: 1}\n\n", "", "{\"a\":\n1}"},
		{"data: one\ndata: two\ndata: three\n\n", "", "one\ntwo\nthree"},
		{"event: ping\n\n", "ping", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		raw := []byte(tc.raw)
		ev := NewEvent(raw)
		if ev.Name() != tc.name || string(ev.Data()) != tc.data {
			t.Errorf("%q: name %q data %q", tc.raw, ev.Name(), ev.Data())
		}
		if string(raw) != tc.raw {
			t.Errorf("%q: the raw event was changed to %q", tc.raw, raw)
		}
	}
}

func TestEventMembers(t *testing.T) {
	ev := NewEvent([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a\"b"},"error":null}` + "\n\n"))
	if !ev.Object() || ev.stringMember("type") != "content_block_delta" || string(ev.Member("index")) != "0" {
		t.Fatalf("members = %+v", ev.members)
	}
	if ev.present("error") || ev.Member("usage") != nil || ev.decoded {
		t.Fatal("reading members must not decode the event")
	}
	if delta, _ := ev.Payload()["delta"].(map[string]any); delta["text"] != `a"b` {
		t.Fatalf("payload = %v", ev.Payload())
	}
	escaped := NewEvent([]byte(`data: {"type":"t\u0065xt","many":[1,2,3,4,5,6,7,8,9],"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"last":true}`))
	if escaped.stringMember("type") != "text" || string(escaped.Member("last")) != "true" {
		t.Fatalf("escaped = %q, last = %q", escaped.stringMember("type"), escaped.Member("last"))
	}
	for _, raw := range []string{"data: not json", "data: [1]", `data: {"a":1`, ""} {
		if ev := NewEvent([]byte(raw)); ev.Object() || ev.Payload() != nil || ev.Member("a") != nil {
			t.Errorf("%q read as an object", raw)
		}
	}
	// Well formed enough for the scan, but not for the decoder.
	if ev := NewEvent([]byte(`data: {"a":tru}`)); !ev.Object() || ev.Payload() != nil {
		t.Fatalf("payload = %v", ev.Payload())
	}
}
