package requestlog

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

	// SQLite compiled into the plugin. CPA loads plugins with RTLD_LOCAL, so
	// its C symbols stay private to this library.
	_ "github.com/mattn/go-sqlite3"
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
	request_id         TEXT    NOT NULL DEFAULT '',
	client_path        TEXT    NOT NULL DEFAULT '',
	user_agent         TEXT    NOT NULL DEFAULT '',
	auth_index         TEXT    NOT NULL DEFAULT '',
	egress             TEXT    NOT NULL DEFAULT '',
	upstream_status    INTEGER NOT NULL DEFAULT 0,
	upstream_request_id TEXT   NOT NULL DEFAULT '',
	retry_after        INTEGER NOT NULL DEFAULT 0,
	finish_reason      TEXT    NOT NULL DEFAULT '',
	frames             INTEGER,
	last_frame_ms      INTEGER,
	usage_reported     INTEGER
);
CREATE INDEX IF NOT EXISTS requests_time ON requests(time_ms);
`

// requestIndexes are created after the migrations, since they cover columns
// an older table gains there.
const requestIndexes = `CREATE INDEX IF NOT EXISTS requests_request_id ON requests(request_id);`

const requestColumns = `time_ms, endpoint, model, upstream_model, served_model, reasoning_effort, stream, status, http_status,
	credential, auth_id, duration_ms, ttft_ms, input_tokens, output_tokens, cached_tokens, cache_write_tokens,
	reasoning_tokens, tokens_per_second, error_code, error, error_phase, request_id,
	client_path, user_agent, auth_index, egress, upstream_status, upstream_request_id, retry_after, finish_reason,
	frames, last_frame_ms, usage_reported`

// attemptsColumn counts the entries of the same CPA request.
const attemptsColumn = `CASE WHEN request_id = '' THEN 1 ELSE (SELECT COUNT(*) FROM requests AS same WHERE same.request_id = requests.request_id) END`

var insertRequest = `INSERT INTO requests (` + requestColumns + `) VALUES (?` + strings.Repeat(", ?", strings.Count(requestColumns, ",")) + `)`

// requestMigrations add columns that came after the table was first created;
// CREATE TABLE IF NOT EXISTS leaves an existing table as it is.
var requestMigrations = []struct{ column, ddl string }{
	{"request_id", `ALTER TABLE requests ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`},
	{"client_path", `ALTER TABLE requests ADD COLUMN client_path TEXT NOT NULL DEFAULT ''`},
	{"user_agent", `ALTER TABLE requests ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`},
	{"auth_index", `ALTER TABLE requests ADD COLUMN auth_index TEXT NOT NULL DEFAULT ''`},
	{"egress", `ALTER TABLE requests ADD COLUMN egress TEXT NOT NULL DEFAULT ''`},
	{"upstream_status", `ALTER TABLE requests ADD COLUMN upstream_status INTEGER NOT NULL DEFAULT 0`},
	{"upstream_request_id", `ALTER TABLE requests ADD COLUMN upstream_request_id TEXT NOT NULL DEFAULT ''`},
	{"retry_after", `ALTER TABLE requests ADD COLUMN retry_after INTEGER NOT NULL DEFAULT 0`},
	{"finish_reason", `ALTER TABLE requests ADD COLUMN finish_reason TEXT NOT NULL DEFAULT ''`},
	{"frames", `ALTER TABLE requests ADD COLUMN frames INTEGER`},
	{"last_frame_ms", `ALTER TABLE requests ADD COLUMN last_frame_ms INTEGER`},
	{"usage_reported", `ALTER TABLE requests ADD COLUMN usage_reported INTEGER`},
}

// requestSearchColumns are matched by the search box.
var requestSearchColumns = []string{"CAST(id AS TEXT)", "request_id", "model", "upstream_model", "served_model", "credential", "auth_id", "endpoint", "error_code", "error", "CAST(http_status AS TEXT)",
	"upstream_request_id", "client_path", "user_agent", "auth_index", "finish_reason"}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Log stores request log entries in SQLite (wokey.db in the data
// dir; in memory when the dir is empty). It keeps at most retention rows and,
// when days > 0, nothing older than that many days. retention 0 turns logging
// off.
type Log struct {
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

// New returns a log that is off until Configure is called; warn receives
// storage problems.
func New(warn func(string)) *Log {
	return &Log{warn: warn, now: time.Now}
}

// Configure points the log at a data dir and applies the retention limits,
// opening (or reopening) the database when the dir changes.
func (l *Log) Configure(dir string, retention, days int) {
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

func (l *Log) openLocked() {
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
	db, errOpen := sql.Open("sqlite3", dsn+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL")
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
func (l *Log) renameLegacyDBLocked() {
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
	_, errIndex := db.Exec(requestIndexes)
	return errIndex
}

func (l *Log) failLocked(message string) {
	l.openErr = message
	l.warnf(message)
}

// importLegacyLocked moves entries from the old JSONL log into the database
// and removes the file once they are committed.
func (l *Log) importLegacyLocked() {
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
		var entry Entry
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

func insertArgs(e Entry) []any {
	var rate, reported any
	if e.TokensPerSecond != nil {
		rate = *e.TokensPerSecond
	}
	if e.UsageReported != nil {
		reported = boolInt(*e.UsageReported)
	}
	return []any{e.Time.UnixMilli(), e.Endpoint, e.Model, e.UpstreamModel, e.ServedModel, e.ReasoningEffort, boolInt(e.Stream), e.Status, e.HTTPStatus,
		e.Credential, e.AuthID, e.DurationMS, nullInt(e.TTFTMS), e.InputTokens, e.OutputTokens, e.CachedTokens, e.CacheWrite,
		e.ReasoningTokens, rate, e.ErrorCode, e.Error, e.ErrorPhase, e.RequestID,
		e.ClientPath, e.UserAgent, e.AuthIndex, e.Egress, e.UpstreamStatus, e.UpstreamRequestID, e.RetryAfter, e.FinishReason,
		nullInt(e.Frames), nullInt(e.LastFrameMS), reported}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func intPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// Add stores one entry, pruning old rows as needed.
func (l *Log) Add(entry Entry) {
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
func (l *Log) pruneLocked() {
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

func (l *Log) execLocked(query string, args ...any) {
	if _, errExec := l.db.Exec(query, args...); errExec != nil {
		l.warnf("prune request log: " + errExec.Error())
	}
}

// Clear deletes every entry.
func (l *Log) Clear() error {
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

// Close closes the database.
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
}

func (l *Log) closeLocked() {
	if l.db != nil {
		_ = l.db.Close()
		l.db = nil
	}
}

func (l *Log) warnf(message string) {
	if l.warn != nil {
		l.warn("wokey: " + message)
	}
}

// Query selects entries for the request log tab.
type Query struct {
	Search string
	Status string
	// From and To bound the request start time (From inclusive, To
	// exclusive); zero means unbounded.
	From time.Time
	To   time.Time
	// RequestID, when set, selects the entries of one CPA request (one per
	// credential CPA tried).
	RequestID string
	Limit     int
	Offset    int
}

// Summary totals every entry matching a query.
type Summary struct {
	Requests        int      `json:"requests"`
	Success         int      `json:"success"`
	Failed          int      `json:"failed"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	CachedTokens    int64    `json:"cached_tokens"`
	CacheHitRate    *float64 `json:"cache_hit_rate"`
	TokensPerSecond *float64 `json:"output_tokens_per_second"`
}

// Page is one page of query results.
type Page struct {
	Items         []Entry `json:"items"`
	Total         int     `json:"total"`
	Offset        int     `json:"offset"`
	Retention     int     `json:"retention"`
	RetentionDays int     `json:"retention_days"`
	Summary       Summary `json:"summary"`
	Error         string  `json:"error,omitempty"`
}

func (q Query) where() (string, []any) {
	var conds []string
	var args []any
	if id := strings.TrimSpace(q.RequestID); id != "" {
		conds = append(conds, "request_id = ?")
		args = append(args, id)
	}
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

// Query returns matching entries newest first, plus a summary over all
// matching entries.
func (l *Log) Query(q Query) Page {
	l.mu.Lock()
	db := l.db
	page := Page{Items: []Entry{}, Retention: l.retention, RetentionDays: l.days, Error: l.openErr}
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
	rows, errRows := db.Query(`SELECT id, `+requestColumns+`, `+attemptsColumn+` FROM requests`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, page.Offset)...)
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

func scanEntry(rows *sql.Rows) (Entry, error) {
	var e Entry
	var id, timeMS int64
	var stream int
	var ttft, frames, lastFrame, reported sql.NullInt64
	var rate sql.NullFloat64
	errScan := rows.Scan(&id, &timeMS, &e.Endpoint, &e.Model, &e.UpstreamModel, &e.ServedModel, &e.ReasoningEffort, &stream, &e.Status, &e.HTTPStatus,
		&e.Credential, &e.AuthID, &e.DurationMS, &ttft, &e.InputTokens, &e.OutputTokens, &e.CachedTokens, &e.CacheWrite,
		&e.ReasoningTokens, &rate, &e.ErrorCode, &e.Error, &e.ErrorPhase, &e.RequestID,
		&e.ClientPath, &e.UserAgent, &e.AuthIndex, &e.Egress, &e.UpstreamStatus, &e.UpstreamRequestID, &e.RetryAfter, &e.FinishReason,
		&frames, &lastFrame, &reported, &e.Attempts)
	if errScan != nil {
		return e, errScan
	}
	e.ID = strconv.FormatInt(id, 10)
	e.Time = time.UnixMilli(timeMS).UTC()
	e.Stream = stream != 0
	e.TTFTMS, e.Frames, e.LastFrameMS = intPtr(ttft), intPtr(frames), intPtr(lastFrame)
	if reported.Valid {
		r := reported.Int64 != 0
		e.UsageReported = &r
	}
	if rate.Valid {
		e.TokensPerSecond = &rate.Float64
	}
	return e, nil
}
