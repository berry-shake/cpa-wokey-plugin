package plugin

import (
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/streamgate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestParseLogTime(t *testing.T) {
	if got, errParse := parseLogTime(""); errParse != nil || !got.IsZero() {
		t.Fatalf("empty = %v, %v", got, errParse)
	}
	if got, errParse := parseLogTime("2026-03-01T10:00:00.000Z"); errParse != nil || got.Unix() != time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("iso = %v, %v", got, errParse)
	}
	if _, errParse := parseLogTime("yesterday"); errParse == nil {
		t.Fatal("bad time accepted")
	}
}

func TestCallRecordUsage(t *testing.T) {
	log := requestlog.New(nil)
	log.Configure("", 10, 0)
	rec := &callRecord{log: log}
	rec.observe(streamgate.Prelude, []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":2}}}\n\n"))
	rec.observe(streamgate.Content, []byte("data: {\"type\":\"content_block_delta\"}\n\n"))
	rec.observe(streamgate.Terminal, []byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n"))
	rec.finish(nil, "")
	rec = &callRecord{log: log}
	rec.observeJSON([]byte(`{"model":"gpt","usage":{"prompt_tokens":20,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":2}}}`))
	rec.finish(&pluginabi.Error{Code: "c", HTTPStatus: 429}, "stream")
	items := log.Query(requestlog.Query{}).Items
	e, a := items[0], items[1]
	if a.ServedModel != "claude-x" || a.InputTokens != 12 || a.CachedTokens != 7 || a.CacheWrite != 2 || a.OutputTokens != 9 || a.TTFTMS == nil {
		t.Fatalf("anthropic = %+v", a)
	}
	if e.Status != "error" || e.ErrorPhase != "stream" || e.HTTPStatus != 429 || e.InputTokens != 20 || e.CachedTokens != 8 || e.ReasoningTokens != 2 {
		t.Fatalf("chat = %+v", e)
	}
}

func TestReasoningEffort(t *testing.T) {
	for body, want := range map[string]string{
		`{"reasoning_effort":"high"}`:                          "high",
		`{"reasoning":{"effort":"low"}}`:                       "low",
		`{"output_config":{"effort":"max"}}`:                   "max",
		`{"thinking":{"type":"enabled","budget_tokens":2048}}`: "budget 2048",
		`{"thinking":{"type":"adaptive"}}`:                     "adaptive",
		`{}`:                                                   "",
	} {
		if got := reasoningEffort([]byte(body)); got != want {
			t.Errorf("%s = %q", body, got)
		}
	}
}

func TestClientModel(t *testing.T) {
	cases := []struct {
		req  pluginapi.ExecutorRequest
		want string
	}{
		{pluginapi.ExecutorRequest{Model: "claude-opus-5-5", Metadata: map[string]any{"requested_model": "wokey/claude-opus-5-5"}, OriginalRequest: []byte(`{"model":"other"}`)}, "wokey/claude-opus-5-5"},
		{pluginapi.ExecutorRequest{Model: "claude-opus-5-5", OriginalRequest: []byte(`{"model":"opus"}`)}, "opus"},
		{pluginapi.ExecutorRequest{Model: "claude-opus-5-5", OriginalRequest: []byte(`not json`)}, "claude-opus-5-5"},
	}
	for _, tc := range cases {
		if got := clientModel(tc.req, tc.req.Model); got != tc.want {
			t.Errorf("clientModel = %q, want %q", got, tc.want)
		}
	}
}
