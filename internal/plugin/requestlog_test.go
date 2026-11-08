package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestRequestLogPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	log := newRequestLog(nil)
	log.configure(dir, 3)
	for i := 0; i < 7; i++ {
		log.add(logEntry{Model: "m", Status: "success", InputTokens: 10, CachedTokens: 5})
	}
	raw, _ := os.ReadFile(filepath.Join(dir, requestLogFile))
	if lines := strings.Count(string(raw), "\n"); lines > 6 {
		t.Fatalf("file not rewritten: %d lines", lines)
	}
	reloaded := newRequestLog(nil)
	reloaded.configure(dir, 3)
	page := reloaded.query(logQuery{})
	if page.Total != 3 || page.Items[0].ID != "7" {
		t.Fatalf("page = %+v", page)
	}
	reloaded.add(logEntry{Model: "x", Status: "error", Error: "boom"})
	page = reloaded.query(logQuery{Status: "error"})
	if page.Total != 1 || page.Items[0].ID != "8" {
		t.Fatalf("error page = %+v", page)
	}
	page = reloaded.query(logQuery{Search: "BOOM"})
	if page.Total != 1 {
		t.Fatalf("search = %+v", page)
	}
	if page = reloaded.query(logQuery{Status: "success"}); page.Summary.CacheHitRate == nil || *page.Summary.CacheHitRate != 0.5 {
		t.Fatalf("summary = %+v", page.Summary)
	}
	if reloaded.clear() != nil || reloaded.query(logQuery{}).Total != 0 {
		t.Fatal("clear")
	}
	if _, err := os.Stat(filepath.Join(dir, requestLogFile)); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
}

func TestCallRecordUsage(t *testing.T) {
	log := newRequestLog(nil)
	log.configure("", 10)
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
