package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	rec := &callRecord{log: log, entry: requestlog.Entry{Stream: true, UpstreamStatus: 200}}
	event := func(raw string) *streamgate.Event { return streamgate.NewEvent([]byte(raw)) }
	rec.observe(streamgate.Prelude, event("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"stop_reason\":null,\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":2}}}\n\n"))
	rec.observe(streamgate.Content, event("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
	rec.observe(streamgate.Content, nil)
	rec.observe(streamgate.Terminal, event("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n"))
	rec.finish(nil, "")
	// A buffered call that failed after Wokey answered: no stream fields.
	rec = &callRecord{log: log, entry: requestlog.Entry{UpstreamStatus: 200}}
	rec.observeJSON([]byte(`{"model":"gpt","usage":{"prompt_tokens":20,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":2}}}`))
	rec.finish(&pluginabi.Error{Code: "c", HTTPStatus: 429, RetryAfterSeconds: 5}, "stream")
	// A stream Wokey rejected before any output.
	rec = &callRecord{log: log, entry: requestlog.Entry{Stream: true, UpstreamStatus: 200}}
	rec.finish(&pluginabi.Error{Code: "rate_limit_error", HTTPStatus: 429}, "request")
	items := log.Query(requestlog.Query{}).Items
	r, e, a := items[0], items[1], items[2]
	if a.ServedModel != "claude-x" || a.InputTokens != 12 || a.CachedTokens != 7 || a.CacheWrite != 2 || a.OutputTokens != 9 || a.TTFTMS == nil {
		t.Fatalf("anthropic = %+v", a)
	}
	if a.FinishReason != "end_turn" || a.Frames == nil || *a.Frames != 2 || a.LastFrameMS == nil || *a.LastFrameMS < *a.TTFTMS || a.UsageReported == nil || !*a.UsageReported {
		t.Fatalf("anthropic stream fields = %+v", a)
	}
	if e.Status != "error" || e.ErrorPhase != "stream" || e.HTTPStatus != 429 || e.InputTokens != 20 || e.CachedTokens != 8 || e.ReasoningTokens != 2 {
		t.Fatalf("chat = %+v", e)
	}
	if e.RetryAfter != 5 || e.Frames != nil || e.LastFrameMS != nil || e.UsageReported == nil || !*e.UsageReported {
		t.Fatalf("chat extra fields = %+v", e)
	}
	if r.Frames == nil || *r.Frames != 0 || r.LastFrameMS != nil || r.UsageReported != nil {
		t.Fatalf("rejected stream = %+v", r)
	}
}

func TestObservable(t *testing.T) {
	for data, want := range map[string]bool{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`:              false,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`: false,
		`{"type":"response.output_text.delta","item_id":"i","delta":"x"}`:                                false,
		`{"type":"content_block_stop","index":0}`:                                                        false,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`:        true,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`:                                    true,
		`{"type":"message_start","message":{"model":"m"}}`:                                               true,
		`{"type":"response.completed","response":{}}`:                                                    true,
		`{"choices":[{"delta":{"content":"x"}}]}`:                                                        true,
		`{"object":"response","output":null}`:                                                            true,
		`not json`:                                                                                       false,
	} {
		if got := observable(streamgate.NewEvent([]byte("data: " + data + "\n\n"))); got != want {
			t.Errorf("%s = %v, want %v", data, got, want)
		}
	}
}

func TestFinishReason(t *testing.T) {
	for body, want := range map[string]string{
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`:                                                         "max_tokens",
		`{"type":"message","stop_reason":"tool_use"}`:                                                                           "tool_use",
		`{"type":"message_start","message":{"stop_reason":null}}`:                                                               "",
		`{"choices":[{"delta":{},"finish_reason":"length"}]}`:                                                                   "length",
		`{"choices":[{"delta":{"content":"x"},"finish_reason":null}]}`:                                                          "",
		`{"choices":[],"usage":{"prompt_tokens":1}}`:                                                                            "",
		`{"type":"response.created","response":{"status":"in_progress"}}`:                                                       "",
		`{"type":"response.completed","response":{"status":"completed"}}`:                                                       "completed",
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`: "max_output_tokens",
		`{"object":"response","status":"completed","output":[]}`:                                                                "completed",
		`{"status":"completed"}`: "",
	} {
		var payload map[string]any
		if errJSON := json.Unmarshal([]byte(body), &payload); errJSON != nil {
			t.Fatal(errJSON)
		}
		if got := finishReason(payload); got != want {
			t.Errorf("%s = %q, want %q", body, got, want)
		}
	}
}

func TestBaseEntry(t *testing.T) {
	req := pluginapi.ExecutorRequest{
		AuthID:   "wokey-a.json",
		Model:    "claude-x",
		Headers:  http.Header{"User-Agent": {"claude-cli/2.1 " + strings.Repeat("é", 200)}},
		Metadata: map[string]any{"request_path": "/v1/messages", "selected_auth_index": " 9d3c ", "requested_model": "wokey/claude-x"},
	}
	e := baseEntry(req, "/v1/messages", clientModel(req, req.Model), true, time.Unix(1, 0))
	if e.ClientPath != "/v1/messages" || e.AuthIndex != "9d3c" || e.Model != "wokey/claude-x" || !e.Stream || e.AuthID != "wokey-a.json" {
		t.Fatalf("entry = %+v", e)
	}
	if len(e.UserAgent) > maxUserAgent || !utf8.ValidString(e.UserAgent) || !strings.HasPrefix(e.UserAgent, "claude-cli/2.1 ") {
		t.Fatalf("user agent = %q (%d bytes)", e.UserAgent, len(e.UserAgent))
	}
	if e = baseEntry(pluginapi.ExecutorRequest{}, "/v1/responses", "", false, time.Unix(1, 0)); e.UserAgent != "" || e.ClientPath != "" || e.AuthIndex != "" {
		t.Fatalf("empty request = %+v", e)
	}
}

func TestReasoningEffort(t *testing.T) {
	for body, want := range map[string]string{
		`{"reasoning_effort":"high"}`:                          "high",
		`{"reasoning":{"effort":"low"}}`:                       "low",
		`{"output_config":{"effort":"max"}}`:                   "max",
		`{"thinking":{"type":"enabled","budget_tokens":2048}}`: "budget 2048",
		`{"thinking":{"type":"adaptive"}}`:                     "adaptive",
		`{"messages":[{"reasoning_effort":"low"}]}`:            "",
		`{}`: "",
	} {
		found, errScan := topLevelMembers([]byte(body), requestMembers...)
		if errScan != nil {
			t.Fatalf("%s: %v", body, errScan)
		}
		if got := reasoningEffort([]byte(body), found); got != want {
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
		// Only the start of the original request is read when the model comes first.
		{pluginapi.ExecutorRequest{Model: "claude-opus-5-5", OriginalRequest: []byte(`{"model":"opus","messages":[`)}, "opus"},
	}
	for _, tc := range cases {
		if got := clientModel(tc.req, tc.req.Model); got != tc.want {
			t.Errorf("clientModel = %q, want %q", got, tc.want)
		}
	}
}
