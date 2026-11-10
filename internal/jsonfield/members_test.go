package jsonfield

import (
	"encoding/json"
	"strings"
	"testing"
)

type member struct{ key, value string }

func members(t *testing.T, doc string) ([]member, error) {
	t.Helper()
	var out []member
	errScan := EachMember([]byte(doc), func(key []byte, start, end int) bool {
		out = append(out, member{string(key), doc[start:end]})
		return true
	})
	return out, errScan
}

func TestEachMember(t *testing.T) {
	doc := ` { "model" : "a\"b" , "messages":[{"x":"}]{["},[1,2.5e-3,true,null]],"n":-1.5,"o":{"k":{"z":[]}},` +
		`"stream":false,"esc":"\\","empty":{},"list":[] } `
	got, errScan := members(t, doc)
	if errScan != nil {
		t.Fatal(errScan)
	}
	want := []member{
		{"model", `"a\"b"`}, {"messages", `[{"x":"}]{["},[1,2.5e-3,true,null]]`}, {"n", `-1.5`},
		{"o", `{"k":{"z":[]}}`}, {"stream", `false`}, {"esc", `"\\"`}, {"empty", `{}`}, {"list", `[]`},
	}
	if len(got) != len(want) {
		t.Fatalf("members = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("member %d = %q, want %q", i, got[i], want[i])
		}
	}
	if got, errScan := members(t, "{}"); errScan != nil || len(got) != 0 {
		t.Fatalf("empty object = %q, %v", got, errScan)
	}
}

func TestEachMemberStopsEarly(t *testing.T) {
	// The scan stops before the broken tail once visit asks it to.
	visited := 0
	errScan := EachMember([]byte(`{"model":"a","messages":[`), func([]byte, int, int) bool {
		visited++
		return false
	})
	if errScan != nil || visited != 1 {
		t.Fatalf("visited %d, err %v", visited, errScan)
	}
}

func TestEachMemberRejects(t *testing.T) {
	for _, doc := range []string{``, `  `, `[]`, `"x"`, `null`, `x{}`} {
		if _, errScan := members(t, doc); errScan != ErrNotObject {
			t.Errorf("%q: err = %v, want ErrNotObject", doc, errScan)
		}
	}
	for _, doc := range []string{
		`{`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{"a":1 "b":2}`, `{"a":"x}`, `{"a":[1,2}`, `{"a":{"b":[}]}`,
		`{"a":1}x`, `{a:1}`, `{"a":[`, `{"a\":1}`,
	} {
		if _, errScan := members(t, doc); errScan == nil || errScan == ErrNotObject {
			t.Errorf("%q: err = %v, want a syntax error", doc, errScan)
		}
	}
}

func TestEachMemberMatchesDecoder(t *testing.T) {
	doc := map[string]any{
		"model":    "claude-x",
		"messages": []any{map[string]any{"role": "user", "content": strings.Repeat(`quote " backslash \ newline`+"\n", 50)}},
		"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048},
		"中文":       "值",
	}
	raw, _ := json.Marshal(doc)
	got, errScan := members(t, string(raw))
	if errScan != nil || len(got) != len(doc) {
		t.Fatalf("members = %q, %v", got, errScan)
	}
	for _, m := range got {
		var value any
		if errValue := json.Unmarshal([]byte(m.value), &value); errValue != nil {
			t.Fatalf("%s: %v", m.key, errValue)
		}
		want, _ := json.Marshal(doc[m.key])
		if again, _ := json.Marshal(value); string(again) != string(want) {
			t.Errorf("%s = %s, want %s", m.key, again, want)
		}
	}
}

func TestHas(t *testing.T) {
	cases := []struct {
		doc, key string
		want     bool
	}{
		{`{"type":"text_delta","text":"x"}`, "stop_reason", false},
		{`{"stop_reason":null}`, "stop_reason", true},
		{`{"a":{"stop_reason":1}}`, "stop_reason", false},
		{`"text"`, "stop_reason", false},
		{``, "stop_reason", false},
	}
	for _, tc := range cases {
		if got := Has([]byte(tc.doc), tc.key); got != tc.want {
			t.Errorf("Has(%s, %s) = %v", tc.doc, tc.key, got)
		}
	}
}
