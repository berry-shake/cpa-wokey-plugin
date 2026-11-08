package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const requestLogFile = "requests.jsonl"

// logEntry is one executed request as shown on the request log tab.
type logEntry struct {
	ID              string    `json:"id"`
	Time            time.Time `json:"time"`
	Endpoint        string    `json:"endpoint"`
	Model           string    `json:"model"`
	UpstreamModel   string    `json:"upstream_model,omitempty"`
	ServedModel     string    `json:"served_model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Stream          bool      `json:"stream"`
	Status          string    `json:"status"` // "success" or "error"
	HTTPStatus      int       `json:"http_status,omitempty"`
	Credential      string    `json:"credential,omitempty"`
	AuthID          string    `json:"auth_id,omitempty"`
	DurationMS      int64     `json:"duration_ms"`
	TTFTMS          *int64    `json:"ttft_ms,omitempty"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CachedTokens    int64     `json:"cached_tokens"`
	CacheWrite      int64     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens int64     `json:"reasoning_tokens,omitempty"`
	TokensPerSecond *float64  `json:"output_tokens_per_second,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	// ErrorPhase is "request" (before any output reached the client) or
	// "stream" (after output started).
	ErrorPhase string `json:"error_phase,omitempty"`
}

// requestLog keeps the newest entries in memory and appends each entry to a
// JSONL file in the data dir. The file is rewritten with only the retained
// entries once it holds twice the retention.
type requestLog struct {
	mu        sync.Mutex
	dir       string
	retention int
	entries   []logEntry
	lines     int
	seq       uint64
	warn      func(string)
}

func newRequestLog(warn func(string)) *requestLog {
	return &requestLog{warn: warn}
}

// configure points the log at a data dir and retention, loading what is on
// disk when the dir changes. retention 0 disables logging.
func (l *requestLog) configure(dir string, retention int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if dir != l.dir {
		l.dir = dir
		l.entries, l.lines = nil, 0
		l.load()
	}
	l.retention = retention
	l.trimLocked()
}

func (l *requestLog) load() {
	if l.dir == "" {
		return
	}
	raw, errRead := os.ReadFile(filepath.Join(l.dir, requestLogFile))
	if errRead != nil {
		if !errors.Is(errRead, os.ErrNotExist) {
			l.warnf("read request log: " + errRead.Error())
		}
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var entry logEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue // torn tail or a bad line
		}
		l.entries = append(l.entries, entry)
		l.lines++
		if n, errID := strconv.ParseUint(entry.ID, 10, 64); errID == nil && n > l.seq {
			l.seq = n
		}
	}
}

func (l *requestLog) add(entry logEntry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retention <= 0 {
		return
	}
	l.seq++
	entry.ID = strconv.FormatUint(l.seq, 10)
	l.entries = append(l.entries, entry)
	l.trimLocked()
	if l.dir == "" {
		return
	}
	line, _ := json.Marshal(entry)
	if l.lines >= 2*l.retention {
		l.rewriteLocked()
		return
	}
	if errAppend := l.appendLocked(append(line, '\n')); errAppend != nil {
		l.warnf("write request log: " + errAppend.Error())
		return
	}
	l.lines++
}

func (l *requestLog) trimLocked() {
	if l.retention > 0 && len(l.entries) > l.retention {
		l.entries = append([]logEntry(nil), l.entries[len(l.entries)-l.retention:]...)
	}
}

func (l *requestLog) appendLocked(line []byte) error {
	if errDir := os.MkdirAll(l.dir, 0o700); errDir != nil {
		return errDir
	}
	file, errOpen := os.OpenFile(filepath.Join(l.dir, requestLogFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return errOpen
	}
	_, errWrite := file.Write(line)
	if errClose := file.Close(); errWrite == nil {
		errWrite = errClose
	}
	return errWrite
}

// rewriteLocked replaces the file with the retained entries.
func (l *requestLog) rewriteLocked() {
	var buf bytes.Buffer
	for _, entry := range l.entries {
		line, _ := json.Marshal(entry)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if errDir := os.MkdirAll(l.dir, 0o700); errDir != nil {
		l.warnf("write request log: " + errDir.Error())
		return
	}
	path := filepath.Join(l.dir, requestLogFile)
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, buf.Bytes(), 0o600); errWrite != nil {
		l.warnf("write request log: " + errWrite.Error())
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		l.warnf("write request log: " + errRename.Error())
		return
	}
	l.lines = len(l.entries)
}

func (l *requestLog) clear() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries, l.lines = nil, 0
	if l.dir == "" {
		return nil
	}
	if errRemove := os.Remove(filepath.Join(l.dir, requestLogFile)); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return errRemove
	}
	return nil
}

func (l *requestLog) warnf(message string) {
	if l.warn != nil {
		l.warn("wokey: " + message)
	}
}

type logQuery struct {
	Search string
	Status string
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
	Items     []logEntry `json:"items"`
	Total     int        `json:"total"`
	Offset    int        `json:"offset"`
	Retention int        `json:"retention"`
	Summary   logSummary `json:"summary"`
}

// query returns matching entries newest first, plus a summary over all
// matching entries.
func (l *requestLog) query(q logQuery) logPage {
	l.mu.Lock()
	defer l.mu.Unlock()
	search := strings.ToLower(strings.TrimSpace(q.Search))
	matched := make([]logEntry, 0, len(l.entries))
	var summary logSummary
	var rateSum float64
	var rateCount int
	for i := len(l.entries) - 1; i >= 0; i-- {
		entry := l.entries[i]
		if q.Status != "" && q.Status != "all" && entry.Status != q.Status {
			continue
		}
		if search != "" && !entry.matches(search) {
			continue
		}
		matched = append(matched, entry)
		summary.Requests++
		if entry.Status == "success" {
			summary.Success++
		} else {
			summary.Failed++
		}
		summary.InputTokens += entry.InputTokens
		summary.OutputTokens += entry.OutputTokens
		summary.CachedTokens += entry.CachedTokens
		if entry.TokensPerSecond != nil {
			rateSum += *entry.TokensPerSecond
			rateCount++
		}
	}
	if summary.InputTokens > 0 {
		rate := float64(summary.CachedTokens) / float64(summary.InputTokens)
		summary.CacheHitRate = &rate
	}
	if rateCount > 0 {
		rate := rateSum / float64(rateCount)
		summary.TokensPerSecond = &rate
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := min(max(q.Offset, 0), len(matched))
	end := min(offset+limit, len(matched))
	return logPage{Items: matched[offset:end], Total: len(matched), Offset: offset, Retention: l.retention, Summary: summary}
}

func (e logEntry) matches(search string) bool {
	for _, field := range []string{e.ID, e.Model, e.UpstreamModel, e.ServedModel, e.Credential, e.AuthID, e.Endpoint, e.ErrorCode, e.Error, strconv.Itoa(e.HTTPStatus)} {
		if strings.Contains(strings.ToLower(field), search) {
			return true
		}
	}
	return false
}

// callRecord collects one call's log entry while it runs.
type callRecord struct {
	log   *requestLog
	start time.Time
	entry logEntry
	usage usageTotals
	first time.Time
}

func (s *Service) startRecord(call *upstreamCall, stream bool) *callRecord {
	now := time.Now()
	return &callRecord{log: s.reqlog, start: now, entry: logEntry{
		Time:            now,
		Endpoint:        call.endpoint,
		Model:           call.model,
		UpstreamModel:   call.upstreamModel,
		ReasoningEffort: reasoningEffort(call.body),
		Stream:          stream,
		Credential:      call.label,
		AuthID:          call.authID,
	}}
}

// recordFailure logs a call that failed before prepareCall produced a call.
func (s *Service) recordFailure(model string, endpoint string, stream bool, authID string, perr *pluginabi.Error) {
	rec := &callRecord{log: s.reqlog, start: time.Now(), entry: logEntry{Time: time.Now(), Endpoint: endpoint, Model: model, Stream: stream, AuthID: authID}}
	rec.finish(perr, "request")
}

// observe looks at a stream event that went through the gate.
func (r *callRecord) observe(kind streamEventKind, event []byte) {
	if kind == streamContent && r.first.IsZero() {
		r.first = time.Now()
	}
	_, data := sseEventFields(event)
	if len(data) > 0 && data[0] == '{' {
		r.observeJSON(data)
	}
}

func (r *callRecord) observeJSON(data []byte) {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	if model := anyString(payload["model"]); model != "" {
		r.entry.ServedModel = model
	}
	for _, key := range []string{"message", "response"} {
		if inner, ok := payload[key].(map[string]any); ok {
			if model := anyString(inner["model"]); model != "" {
				r.entry.ServedModel = model
			}
			r.usage.merge(inner["usage"])
		}
	}
	r.usage.merge(payload["usage"])
}

func (r *callRecord) finish(perr *pluginabi.Error, phase string) {
	if r == nil || r.log == nil {
		return
	}
	end := time.Now()
	entry := r.entry
	entry.DurationMS = end.Sub(r.start).Milliseconds()
	if !r.first.IsZero() {
		ttft := r.first.Sub(r.start).Milliseconds()
		entry.TTFTMS = &ttft
	}
	entry.InputTokens, entry.OutputTokens = r.usage.input, r.usage.output
	entry.CachedTokens, entry.CacheWrite, entry.ReasoningTokens = r.usage.cached, r.usage.cacheWrite, r.usage.reasoning
	if entry.OutputTokens > 0 {
		from := r.start
		if !r.first.IsZero() {
			from = r.first
		}
		if seconds := end.Sub(from).Seconds(); seconds > 0.05 {
			rate := float64(entry.OutputTokens) / seconds
			entry.TokensPerSecond = &rate
		}
	}
	if perr != nil {
		entry.Status = "error"
		entry.HTTPStatus = perr.HTTPStatus
		entry.ErrorCode = perr.Code
		entry.Error = perr.Message
		entry.ErrorPhase = phase
	} else {
		entry.Status = "success"
		entry.HTTPStatus = 200
	}
	r.log.add(entry)
}

// usageTotals reads the usage objects of the three protocols. Later events
// overwrite earlier non-zero values (Messages reports input in message_start
// and output in message_delta).
type usageTotals struct {
	input, output, cached, cacheWrite, reasoning int64
}

func (u *usageTotals) merge(raw any) {
	usage, ok := raw.(map[string]any)
	if !ok {
		return
	}
	set := func(target *int64, value int64) {
		if value > 0 {
			*target = value
		}
	}
	anthropicInput := number(usage["input_tokens"])
	cacheRead := number(usage["cache_read_input_tokens"])
	cacheCreate := number(usage["cache_creation_input_tokens"])
	if _, anthropic := usage["cache_read_input_tokens"]; anthropic {
		// Anthropic input_tokens excludes cached tokens.
		set(&u.input, anthropicInput+cacheRead+cacheCreate)
		set(&u.cached, cacheRead)
		set(&u.cacheWrite, cacheCreate)
	} else {
		set(&u.input, anthropicInput)
	}
	set(&u.input, number(usage["prompt_tokens"]))
	set(&u.output, number(usage["output_tokens"]))
	set(&u.output, number(usage["completion_tokens"]))
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if details, ok := usage[key].(map[string]any); ok {
			set(&u.cached, number(details["cached_tokens"]))
		}
	}
	for _, key := range []string{"completion_tokens_details", "output_tokens_details"} {
		if details, ok := usage[key].(map[string]any); ok {
			set(&u.reasoning, number(details["reasoning_tokens"]))
		}
	}
}

func number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		n, _ := typed.Int64()
		return n
	}
	return 0
}

// reasoningEffort describes the requested thinking level, if any.
func reasoningEffort(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	if effort := anyString(payload["reasoning_effort"]); effort != "" {
		return effort
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if effort := anyString(reasoning["effort"]); effort != "" {
			return effort
		}
	}
	if output, ok := payload["output_config"].(map[string]any); ok {
		if effort := anyString(output["effort"]); effort != "" {
			return effort
		}
	}
	if thinking, ok := payload["thinking"].(map[string]any); ok {
		kind := anyString(thinking["type"])
		if budget := number(thinking["budget_tokens"]); budget > 0 {
			return "budget " + strconv.FormatInt(budget, 10)
		}
		if kind != "" {
			return kind
		}
	}
	return ""
}

// clientModel is the model name the client asked for (with the prefix or
// alias it used), falling back to the model CPA passed to the executor.
func clientModel(req pluginapi.ExecutorRequest, fallback string) string {
	requested, _ := req.Metadata["requested_model"].(string)
	return firstNonEmpty(requested, topLevelString(req.OriginalRequest, "model"), fallback)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
