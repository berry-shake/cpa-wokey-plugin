package plugin

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

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
