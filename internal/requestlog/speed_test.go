package requestlog

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func ms(v int64) *int64 { return &v }

func TestSpeeds(t *testing.T) {
	cases := []struct {
		name       string
		stream     bool
		out, total int64
		ttft       *int64
		gen, e2e   float64 // 0 means none
	}{
		{"stream", true, 101, 3000, ms(1000), 50, 101.0 / 3},
		{"buffered response has no first token", false, 100, 2000, nil, 0, 50},
		{"buffered with a TTFT kept by an import", false, 100, 2000, ms(1990), 0, 50},
		{"stream without output", true, 0, 2000, ms(1000), 0, 0},
		{"one token has no generation phase", true, 1, 2000, ms(1000), 0, 0.5},
		{"output in one burst", true, 100, 1030, ms(1000), 0, 100 / 1.03},
		{"too short to time", true, 10, 50, ms(10), 0, 0},
		{"stream that failed before output", true, 0, 500, nil, 0, 0},
	}
	for _, c := range cases {
		gen, e2e := GenerationSpeed(c.stream, c.out, c.total, c.ttft), E2ESpeed(c.out, c.total)
		if (gen == nil) != (c.gen == 0) || gen != nil && math.Abs(*gen-c.gen) > 1e-9 {
			t.Errorf("%s: generation speed = %v, want %v", c.name, deref(gen), c.gen)
		}
		if (e2e == nil) != (c.e2e == 0) || e2e != nil && math.Abs(*e2e-c.e2e) > 1e-9 {
			t.Errorf("%s: end-to-end speed = %v, want %v", c.name, deref(e2e), c.e2e)
		}
	}
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func rate(v float64) *float64 { return &v }

// The summary gives the medians of both speeds over the successful entries.
func TestSummarySpeedMedians(t *testing.T) {
	log := New(func(string) {})
	log.Configure("", 1000, 0)
	defer log.Close()
	now := time.Now()
	add := func(status string, gen, e2e *float64) {
		log.Add(Entry{Time: now, Endpoint: "/v1/messages", Model: "m", Status: status, Stream: gen != nil, OutputTokens: 10, TokensPerSecond: gen, E2ETokensPerSecond: e2e})
	}
	add("success", rate(100), rate(20))
	add("success", rate(300), rate(40))
	add("success", rate(200), rate(10))
	add("success", nil, rate(30))        // buffered: no generation speed
	add("error", rate(9000), rate(9000)) // failures do not count
	add("success", nil, nil)             // no output

	page := log.Query(Query{})
	if page.Error != "" {
		t.Fatal(page.Error)
	}
	sum := page.Summary
	if sum.TokensPerSecond == nil || *sum.TokensPerSecond != 200 {
		t.Fatalf("generation median = %v, want 200 (odd count)", deref(sum.TokensPerSecond))
	}
	if sum.E2ETokensPerSecond == nil || *sum.E2ETokensPerSecond != 25 {
		t.Fatalf("end-to-end median = %v, want 25 (even count)", deref(sum.E2ETokensPerSecond))
	}
	var gen, e2e int
	for _, e := range page.Items {
		if e.TokensPerSecond != nil {
			gen++
		}
		if e.E2ETokensPerSecond != nil {
			e2e++
		}
	}
	if gen != 4 || e2e != 5 {
		t.Fatalf("entries with speeds: %d generation, %d end-to-end", gen, e2e)
	}

	failed := log.Query(Query{Status: "error"}).Summary
	if failed.TokensPerSecond != nil || failed.E2ETokensPerSecond != nil {
		t.Fatalf("a summary of failures has no speeds: %+v", failed)
	}
	searched := log.Query(Query{Search: "no-such-model"}).Summary
	if searched.TokensPerSecond != nil || searched.E2ETokensPerSecond != nil {
		t.Fatalf("an empty summary has no speeds: %+v", searched)
	}
}

// A database from before the end-to-end speed gains the column; its
// entries keep what they had and show no end-to-end speed until refreshed.
func TestOldDatabaseGainsE2EColumn(t *testing.T) {
	dir := t.TempDir()
	old, errOpen := sql.Open("sqlite3", "file:"+filepath.ToSlash(filepath.Join(dir, requestLogDB)))
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	_, errCreate := old.Exec(`CREATE TABLE requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT, time_ms INTEGER NOT NULL, endpoint TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '',
		upstream_model TEXT NOT NULL DEFAULT '', served_model TEXT NOT NULL DEFAULT '', reasoning_effort TEXT NOT NULL DEFAULT '',
		stream INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT '', http_status INTEGER NOT NULL DEFAULT 0,
		credential TEXT NOT NULL DEFAULT '', auth_id TEXT NOT NULL DEFAULT '', duration_ms INTEGER NOT NULL DEFAULT 0, ttft_ms INTEGER,
		input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0,
		cache_write_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0, tokens_per_second REAL,
		error_code TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', error_phase TEXT NOT NULL DEFAULT '');
		INSERT INTO requests (time_ms, status, stream, duration_ms, ttft_ms, output_tokens, tokens_per_second) VALUES (1800000000000, 'success', 1, 3000, 1000, 101, 50.5)`)
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	_ = old.Close()

	log := New(func(message string) { t.Error(message) })
	log.Configure(dir, 1000, 0)
	defer log.Close()
	page := log.Query(Query{})
	if page.Error != "" || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	e := page.Items[0]
	if e.TokensPerSecond == nil || *e.TokensPerSecond != 50.5 || e.E2ETokensPerSecond != nil {
		t.Fatalf("old entry = %+v", e)
	}
	log.Add(Entry{Time: time.Now(), Status: "success", Stream: true, DurationMS: 2000, OutputTokens: 100, E2ETokensPerSecond: rate(50)})
	if got := log.Query(Query{}).Items[0]; got.E2ETokensPerSecond == nil || *got.E2ETokensPerSecond != 50 {
		t.Fatalf("new entry = %+v", got)
	}
}
