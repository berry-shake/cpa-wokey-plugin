package plugin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRequestLogPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	log := newRequestLog(nil)
	log.configure(dir, 3, 0)
	for i := 0; i < 7; i++ {
		log.add(logEntry{Model: "m", Status: "success", InputTokens: 10, CachedTokens: 5})
	}
	if page := log.query(logQuery{}); page.Total != 3 || page.Items[0].ID != "7" || page.Items[2].ID != "5" {
		t.Fatalf("row cap: %+v", page)
	}
	log.close()
	if _, errStat := os.Stat(filepath.Join(dir, requestLogDB)); errStat != nil {
		t.Fatalf("database not on disk: %v", errStat)
	}

	reloaded := newRequestLog(nil)
	defer reloaded.close()
	reloaded.configure(dir, 3, 0)
	page := reloaded.query(logQuery{})
	if page.Total != 3 || page.Items[0].ID != "7" || page.Retention != 3 || page.Error != "" {
		t.Fatalf("page = %+v", page)
	}
	reloaded.add(logEntry{Model: "x", Status: "error", Error: "boom"})
	page = reloaded.query(logQuery{Status: "error"})
	if page.Total != 1 || page.Items[0].ID != "8" {
		t.Fatalf("error page = %+v", page)
	}
	if page = reloaded.query(logQuery{Search: "BOOM"}); page.Total != 1 {
		t.Fatalf("search = %+v", page)
	}
	if page = reloaded.query(logQuery{Search: "%"}); page.Total != 0 {
		t.Fatalf("LIKE wildcards must be literal: %+v", page)
	}
	if page = reloaded.query(logQuery{Status: "success"}); page.Summary.CacheHitRate == nil || *page.Summary.CacheHitRate != 0.5 || page.Summary.Requests != 2 {
		t.Fatalf("summary = %+v", page.Summary)
	}
	if page = reloaded.query(logQuery{Limit: 1, Offset: 1}); len(page.Items) != 1 || page.Items[0].ID != "7" || page.Offset != 1 || page.Total != 3 {
		t.Fatalf("paging = %+v", page)
	}
	if reloaded.clear() != nil || reloaded.query(logQuery{}).Total != 0 {
		t.Fatal("clear")
	}
}

func TestRequestLogRangeAndAge(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	log := newRequestLog(nil)
	log.now = func() time.Time { return now }
	log.configure("", 100, 0)
	defer log.close()
	ttft, rate := int64(120), 42.5
	for _, age := range []time.Duration{40 * 24 * time.Hour, 3 * 24 * time.Hour, 2 * time.Hour, time.Minute} {
		log.add(logEntry{Time: now.Add(-age), Model: "m", Status: "success", OutputTokens: 1, TTFTMS: &ttft, TokensPerSecond: &rate})
	}
	if page := log.query(logQuery{From: now.Add(-24 * time.Hour)}); page.Total != 2 || page.Summary.OutputTokens != 2 {
		t.Fatalf("last 24h = %+v", page)
	}
	page := log.query(logQuery{From: now.Add(-7 * 24 * time.Hour), To: now.Add(-time.Hour)})
	if page.Total != 2 || page.Items[0].Time.Unix() != now.Add(-2*time.Hour).Unix() {
		t.Fatalf("custom range = %+v", page)
	}
	if got := page.Items[0]; got.TTFTMS == nil || *got.TTFTMS != 120 || got.TokensPerSecond == nil || *got.TokensPerSecond != 42.5 {
		t.Fatalf("nullable columns = %+v", got)
	}
	if page = log.query(logQuery{To: now.Add(-time.Minute)}); page.Total != 3 {
		t.Fatalf("to is exclusive: %+v", page)
	}

	// Turning on an age limit prunes right away.
	log.configure("", 100, 30)
	if page = log.query(logQuery{}); page.Total != 3 || page.RetentionDays != 30 {
		t.Fatalf("age prune = %+v", page)
	}
	// Retention 0 stops logging and closes the database.
	log.configure("", 0, 30)
	log.add(logEntry{Model: "m", Status: "success"})
	if page = log.query(logQuery{}); page.Total != 0 || page.Retention != 0 {
		t.Fatalf("disabled = %+v", page)
	}
}

func TestRequestLogImportsJSONL(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"id":"41","time":"2026-03-01T10:00:00Z","endpoint":"/v1/messages","model":"wokey/a","stream":true,"status":"success","ttft_ms":90,"input_tokens":5}
{"id":"42","time":"2026-03-01T11:00:00Z","endpoint":"/v1/chat/completions","model":"wokey/b","stream":false,"status":"error","error":"boom"}
{"id":"43","time":"2026-03-01T1`
	if errWrite := os.WriteFile(filepath.Join(dir, legacyRequestLog), []byte(legacy), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	log := newRequestLog(nil)
	defer log.close()
	log.configure(dir, 100, 0)
	page := log.query(logQuery{})
	if page.Total != 2 || page.Items[0].Model != "wokey/b" || page.Items[1].TTFTMS == nil || !page.Items[1].Stream {
		t.Fatalf("imported = %+v", page)
	}
	if want := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC); !page.Items[1].Time.Equal(want) {
		t.Fatalf("time = %v", page.Items[1].Time)
	}
	if _, errStat := os.Stat(filepath.Join(dir, legacyRequestLog)); !os.IsNotExist(errStat) {
		t.Fatal("legacy log not removed")
	}
}

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
	log := newRequestLog(nil)
	log.configure("", 10, 0)
	rec := &callRecord{log: log}
	rec.observe(streamPrelude, []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":7,\"cache_creation_input_tokens\":2}}}\n\n"))
	rec.observe(streamContent, []byte("data: {\"type\":\"content_block_delta\"}\n\n"))
	rec.observe(streamTerminal, []byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n"))
	rec.finish(nil, "")
	rec = &callRecord{log: log}
	rec.observeJSON([]byte(`{"model":"gpt","usage":{"prompt_tokens":20,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":2}}}`))
	rec.finish(&pluginabi.Error{Code: "c", HTTPStatus: 429}, "stream")
	items := log.query(logQuery{}).Items
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
