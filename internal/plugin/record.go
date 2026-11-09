package plugin

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/streamgate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// callRecord collects one call's log entry while it runs.
type callRecord struct {
	log   *requestlog.Log
	start time.Time
	entry requestlog.Entry
	usage usageTotals
	first time.Time
}

func (s *Service) startRecord(call *upstreamCall, stream bool) *callRecord {
	now := time.Now()
	return &callRecord{log: s.reqlog, start: now, entry: requestlog.Entry{
		Time:            now,
		Endpoint:        call.endpoint,
		Model:           call.model,
		UpstreamModel:   call.upstreamModel,
		ReasoningEffort: reasoningEffort(call.body),
		Stream:          stream,
		Credential:      call.label,
		AuthID:          call.authID,
		RequestID:       call.requestID,
	}}
}

// recordFailure logs a call that failed before prepareCall produced a call.
func (s *Service) recordFailure(req pluginapi.ExecutorRequest, endpoint string, stream bool, perr *pluginabi.Error) {
	now := time.Now()
	rec := &callRecord{log: s.reqlog, start: now, entry: requestlog.Entry{
		Time:      now,
		Endpoint:  endpoint,
		Model:     clientModel(req, req.Model),
		Stream:    stream,
		AuthID:    req.AuthID,
		RequestID: cpaRequestID(req),
	}}
	rec.finish(perr, "request")
}

// observe looks at a stream event that went through the gate.
func (r *callRecord) observe(kind streamgate.Kind, event []byte) {
	if kind == streamgate.Content && r.first.IsZero() {
		r.first = time.Now()
	}
	_, data := streamgate.EventFields(event)
	if len(data) > 0 && data[0] == '{' {
		r.observeJSON(data)
	}
}

func (r *callRecord) observeJSON(data []byte) {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	if model := jsonfield.Text(payload["model"]); model != "" {
		r.entry.ServedModel = model
	}
	for _, key := range []string{"message", "response"} {
		if inner, ok := payload[key].(map[string]any); ok {
			if model := jsonfield.Text(inner["model"]); model != "" {
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
	r.log.Add(entry)
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
	if effort := jsonfield.Text(payload["reasoning_effort"]); effort != "" {
		return effort
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		if effort := jsonfield.Text(reasoning["effort"]); effort != "" {
			return effort
		}
	}
	if output, ok := payload["output_config"].(map[string]any); ok {
		if effort := jsonfield.Text(output["effort"]); effort != "" {
			return effort
		}
	}
	if thinking, ok := payload["thinking"].(map[string]any); ok {
		kind := jsonfield.Text(thinking["type"])
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
