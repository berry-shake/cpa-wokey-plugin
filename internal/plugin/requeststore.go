package plugin

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go, so the c-shared plugin carries no C symbols that could clash with the host
)

const (
	requestLogDB = "wokey.db"
	// legacyRequestDB is the database name used by early 0.1.7 builds; it is
	// renamed to requestLogDB on open.
	legacyRequestDB = "requests.db"
	// legacyRequestLog is the JSONL file used before SQLite; it is imported
	// once and then removed.
	legacyRequestLog = "requests.jsonl"
	// agePruneEvery is how many inserts pass between age-based prunes.
	agePruneEvery = 100
)

const requestSchema = `
CREATE TABLE IF NOT EXISTS requests (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	time_ms            INTEGER NOT NULL,
	endpoint           TEXT    NOT NULL DEFAULT '',
	model              TEXT    NOT NULL DEFAULT '',
	upstream_model     TEXT    NOT NULL DEFAULT '',
	served_model       TEXT    NOT NULL DEFAULT '',
	reasoning_effort   TEXT    NOT NULL DEFAULT '',
	stream             INTEGER NOT NULL DEFAULT 0,
	status             TEXT    NOT NULL DEFAULT '',
	http_status        INTEGER NOT NULL DEFAULT 0,
	credential         TEXT    NOT NULL DEFAULT '',
	auth_id            TEXT    NOT NULL DEFAULT '',
	duration_ms        INTEGER NOT NULL DEFAULT 0,
	ttft_ms            INTEGER,
	input_tokens       INTEGER NOT NULL DEFAULT 0,
	output_tokens      INTEGER NOT NULL DEFAULT 0,
	cached_tokens      INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
	tokens_per_second  REAL,
	error_code         TEXT    NOT NULL DEFAULT '',
	error              TEXT    NOT NULL DEFAULT '',
	error_phase        TEXT    NOT NULL DEFAULT '',
	request_id         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS requests_time ON requests(time_ms);
`

const requestColumns = `time_ms, endpoint, model, upstream_model, served_model, reasoning_effort, stream, status, http_status,
	credential, auth_id, duration_ms, ttft_ms, input_tokens, output_tokens, cached_tokens, cache_write_tokens,
	reasoning_tokens, tokens_per_second, error_code, error, error_phase, request_id`

var insertRequest = `INSERT INTO requests (` + requestColumns + `) VALUES (?` + strings.Repeat(", ?", strings.Count(requestColumns, ",")) + `)`

// requestMigrations add columns that came after the table was first created;
// CREATE TABLE IF NOT EXISTS leaves an existing table as it is.
var requestMigrations = []struct{ column, ddl string }{
	{"request_id", `ALTER TABLE requests ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`},
}

// requestSearchColumns are matched by the search box.
var requestSearchColumns = []string{"CAST(id AS TEXT)", "request_id", "model", "upstream_model", "served_model", "credential", "auth_id", "endpoint", "error_code", "error", "CAST(http_status AS TEXT)"}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// requestLog stores request log entries in SQLite (wokey.db in the data
// dir; in memory when the dir is empty). It keeps at most retention rows and,
// when days > 0, nothing older than that many days. retention 0 turns logging
// off.
type requestLog struct {
	mu        sync.Mutex // guards the fields below and serialises writes
	db        *sql.DB
	dir       string
	retention int
	days      int
	inserts   int // since the last age prune
	openErr   string
	warn      func(string)
	now       func() time.Time
}

func newRequestLog(warn func(string)) *requestLog {
	return &requestLog{warn: warn, now: time.Now}
}

// configure points the log at a data dir and applies the retention limits,
// opening (or reopening) the database when the dir changes.
func (l *requestLog) configure(dir string, retention, days int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retention, l.days = retention, max(days, 0)
	if retention <= 0 {
		l.closeLocked()
		l.dir, l.openErr = dir, ""
		return
	}
	if l.db == nil || dir != l.dir {
		l.closeLocked()
		l.dir = dir
		l.openLocked()
	}
	l.pruneLocked()
}

func (l *requestLog) openLocked() {
	l.openErr = ""
	dsn := "file::memory:"
	if l.dir != "" {
		if errDir := os.MkdirAll(l.dir, 0o700); errDir != nil {
			l.failLocked("open request log: " + errDir.Error())
			return
		}
		l.renameLegacyDBLocked()
		dsn = "file:" + filepath.ToSlash(filepath.Join(l.dir, requestLogDB))
	}
	db, errOpen := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if errOpen != nil {
		l.failLocked("open request log: " + errOpen.Error())
		return
	}
	if l.dir == "" {
		db.SetMaxOpenConns(1) // every :memory: connection is a separate database
	} else {
		db.SetMaxOpenConns(4)
	}
	if _, errSchema := db.Exec(requestSchema); errSchema != nil {
		_ = db.Close()
		l.failLocked("open request log: " + errSchema.Error())
		return
	}
	if errMigrate := migrateRequestSchema(db); errMigrate != nil {
		_ = db.Close()
		l.failLocked("upgrade request log: " + errMigrate.Error())
		return
	}
	l.db = db
	l.importLegacyLocked()
}

// renameLegacyDBLocked moves a database saved under the old name to the
// current one. The WAL files go first, so a move cut short is finished on the
// next open instead of leaving committed rows behind in an orphaned WAL.
func (l *requestLog) renameLegacyDBLocked() {
	current := filepath.Join(l.dir, requestLogDB)
	legacy := filepath.Join(l.dir, legacyRequestDB)
	if _, errStat := os.Stat(current); !errors.Is(errStat, os.ErrNotExist) {
		return
	}
	if _, errStat := os.Stat(legacy); errStat != nil {
		return
	}
	for _, suffix := range []string{"-wal", "-shm", ""} {
		if errRename := os.Rename(legacy+suffix, current+suffix); errRename != nil && !errors.Is(errRename, os.ErrNotExist) {
			l.warnf("rename old request log: " + errRename.Error())
			return
		}
	}
}

// migrateRequestSchema adds the columns an older database lacks.
func migrateRequestSchema(db *sql.DB) error {
	rows, errRows := db.Query(`SELECT name FROM pragma_table_info('requests')`)
	if errRows != nil {
		return errRows
	}
	have := make(map[string]bool)
	for rows.Next() {
		var name string
		if errScan := rows.Scan(&name); errScan != nil {
			_ = rows.Close()
			return errScan
		}
		have[name] = true
	}
	// Close before altering: the in-memory database has a single connection.
	errIter := rows.Err()
	_ = rows.Close()
	if errIter != nil {
		return errIter
	}
	for _, m := range requestMigrations {
		if !have[m.column] {
			if _, errAlter := db.Exec(m.ddl); errAlter != nil {
				return errAlter
			}
		}
	}
	return nil
}

func (l *requestLog) failLocked(message string) {
	l.openErr = message
	l.warnf(message)
}

// importLegacyLocked moves entries from the old JSONL log into the database
// and removes the file once they are committed.
func (l *requestLog) importLegacyLocked() {
	if l.dir == "" {
		return
	}
	path := filepath.Join(l.dir, legacyRequestLog)
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			l.warnf("import request log: " + errRead.Error())
		}
		return
	}
	tx, errTx := l.db.Begin()
	if errTx != nil {
		l.warnf("import request log: " + errTx.Error())
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var entry logEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue // torn tail or a bad line
		}
		if _, errInsert := tx.Exec(insertRequest, insertArgs(entry)...); errInsert != nil {
			_ = tx.Rollback()
			l.warnf("import request log: " + errInsert.Error())
			return
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		l.warnf("import request log: " + errCommit.Error())
		return
	}
	for _, name := range []string{path, path + ".tmp"} {
		if errRemove := os.Remove(name); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			l.warnf("remove old request log: " + errRemove.Error())
		}
	}
}

func insertArgs(e logEntry) []any {
	var ttft, rate any
	if e.TTFTMS != nil {
		ttft = *e.TTFTMS
	}
	if e.TokensPerSecond != nil {
		rate = *e.TokensPerSecond
	}
	stream := 0
	if e.Stream {
		stream = 1
	}
	return []any{e.Time.UnixMilli(), e.Endpoint, e.Model, e.UpstreamModel, e.ServedModel, e.ReasoningEffort, stream, e.Status, e.HTTPStatus,
		e.Credential, e.AuthID, e.DurationMS, ttft, e.InputTokens, e.OutputTokens, e.CachedTokens, e.CacheWrite,
		e.ReasoningTokens, rate, e.ErrorCode, e.Error, e.ErrorPhase, e.RequestID}
}

func (l *requestLog) add(entry logEntry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil || l.retention <= 0 {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = l.now()
	}
	res, errInsert := l.db.Exec(insertRequest, insertArgs(entry)...)
	if errInsert != nil {
		l.warnf("write request log: " + errInsert.Error())
		return
	}
	// Ids only grow, so everything at or below last-retention is over the cap.
	if last, errID := res.LastInsertId(); errID == nil && last > int64(l.retention) {
		l.execLocked(`DELETE FROM requests WHERE id <= ?`, last-int64(l.retention))
	}
	if l.inserts++; l.inserts >= agePruneEvery {
		l.pruneLocked()
	}
}

// pruneLocked drops rows past the age limit and the row cap.
func (l *requestLog) pruneLocked() {
	l.inserts = 0
	if l.db == nil {
		return
	}
	if l.days > 0 {
		l.execLocked(`DELETE FROM requests WHERE time_ms < ?`, l.now().Add(-time.Duration(l.days)*24*time.Hour).UnixMilli())
	}
	if l.retention > 0 {
		l.execLocked(`DELETE FROM requests WHERE id <= (SELECT MAX(id) FROM requests) - ?`, l.retention)
	}
}

func (l *requestLog) execLocked(query string, args ...any) {
	if _, errExec := l.db.Exec(query, args...); errExec != nil {
		l.warnf("prune request log: " + errExec.Error())
	}
}

func (l *requestLog) clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		return nil
	}
	if _, errDelete := l.db.Exec(`DELETE FROM requests`); errDelete != nil {
		return errDelete
	}
	if _, errVacuum := l.db.Exec(`VACUUM`); errVacuum != nil {
		l.warnf("vacuum request log: " + errVacuum.Error())
	}
	return nil
}

func (l *requestLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
}

func (l *requestLog) closeLocked() {
	if l.db != nil {
		_ = l.db.Close()
		l.db = nil
	}
}

func (l *requestLog) warnf(message string) {
	if l.warn != nil {
		l.warn("wokey: " + message)
	}
}

type logQuery struct {
	Search string
	Status string
	// From and To bound the request start time (From inclusive, To
	// exclusive); zero means unbounded.
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

type logSummary struct {
	Requests        int      `json:"requests"`
	Success         int      `json:"success"`
	Failed          int      `json:"failed"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CachedTokens    int64    `json:"cached_tokens"`
	CacheHitRate    *float64 `json:"cache_hit_rate"`
	TokensPerSecond *float64 `json:"output_tokens_per_second"`
}

type logPage struct {
	Items         []logEntry `json:"items"`
	Total         int        `json:"total"`
	Offset        int        `json:"offset"`
	Retention     int        `json:"retention"`
	RetentionDays int        `json:"retention_days"`
	Summary       logSummary `json:"summary"`
	Error         string     `json:"error,omitempty"`
}

func (q logQuery) where() (string, []any) {
	var conds []string
	var args []any
	if q.Status != "" && q.Status != "all" {
		conds = append(conds, "status = ?")
		args = append(args, q.Status)
	}
	if !q.From.IsZero() {
		conds = append(conds, "time_ms >= ?")
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		conds = append(conds, "time_ms < ?")
		args = append(args, q.To.UnixMilli())
	}
	if search := strings.TrimSpace(q.Search); search != "" {
		pattern := "%" + likeEscaper.Replace(search) + "%"
		parts := make([]string, 0, len(requestSearchColumns))
		for _, column := range requestSearchColumns {
			parts = append(parts, column+` LIKE ? ESCAPE '\'`)
			args = append(args, pattern)
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// query returns matching entries newest first, plus a summary over all
// matching entries.
func (l *requestLog) query(q logQuery) logPage {
	l.mu.Lock()
	db := l.db
	page := logPage{Items: []logEntry{}, Retention: l.retention, RetentionDays: l.days, Error: l.openErr}
	l.mu.Unlock()
	if db == nil {
		return page
	}
	where, args := q.where()
	var success, input, output, cached sql.NullInt64
	var rate sql.NullFloat64
	errSum := db.QueryRow(`SELECT COUNT(*), SUM(status = 'success'), SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens), AVG(tokens_per_second) FROM requests`+where, args...).
		Scan(&page.Total, &success, &input, &output, &cached, &rate)
	if errSum != nil {
		page.Error = "read request log: " + errSum.Error()
		return page
	}
	sum := &page.Summary
	sum.Requests, sum.Success = page.Total, int(success.Int64)
	sum.Failed = sum.Requests - sum.Success
	sum.InputTokens, sum.OutputTokens, sum.CachedTokens = input.Int64, output.Int64, cached.Int64
	if sum.InputTokens > 0 {
		hit := float64(sum.CachedTokens) / float64(sum.InputTokens)
		sum.CacheHitRate = &hit
	}
	if rate.Valid {
		sum.TokensPerSecond = &rate.Float64
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	page.Offset = min(max(q.Offset, 0), page.Total)
	rows, errRows := db.Query(`SELECT id, `+requestColumns+` FROM requests`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, page.Offset)...)
	if errRows != nil {
		page.Error = "read request log: " + errRows.Error()
		return page
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		entry, errScan := scanEntry(rows)
		if errScan != nil {
			page.Error = "read request log: " + errScan.Error()
			break
		}
		page.Items = append(page.Items, entry)
	}
	if errIter := rows.Err(); errIter != nil && page.Error == "" {
		page.Error = "read request log: " + errIter.Error()
	}
	return page
}

func scanEntry(rows *sql.Rows) (logEntry, error) {
	var e logEntry
	var id, timeMS int64
	var stream int
	var ttft sql.NullInt64
	var rate sql.NullFloat64
	errScan := rows.Scan(&id, &timeMS, &e.Endpoint, &e.Model, &e.UpstreamModel, &e.ServedModel, &e.ReasoningEffort, &stream, &e.Status, &e.HTTPStatus,
		&e.Credential, &e.AuthID, &e.DurationMS, &ttft, &e.InputTokens, &e.OutputTokens, &e.CachedTokens, &e.CacheWrite,
		&e.ReasoningTokens, &rate, &e.ErrorCode, &e.Error, &e.ErrorPhase, &e.RequestID)
	if errScan != nil {
		return e, errScan
	}
	e.ID = strconv.FormatInt(id, 10)
	e.Time = time.UnixMilli(timeMS).UTC()
	e.Stream = stream != 0
	if ttft.Valid {
		e.TTFTMS = &ttft.Int64
	}
	if rate.Valid {
		e.TokensPerSecond = &rate.Float64
	}
	return e, nil
}
