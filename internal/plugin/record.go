package plugin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/streamgate"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// maxUserAgent bounds the client User-Agent kept in the log.
const maxUserAgent = 256

// callRecord collects one call's log entry while it runs.
type callRecord struct {
	log   *requestlog.Log
	start time.Time
	entry requestlog.Entry
	usage usageTotals
	// first and last are when the first and the latest output event
	// arrived; frames counts output events.
	first, last time.Time
	frames      int64
}

// baseEntry fills in what the executor request tells about a call; model is
// the name the client asked for.
func baseEntry(req pluginapi.ExecutorRequest, endpoint, model string, stream bool, now time.Time) requestlog.Entry {
	path, _ := req.Metadata["request_path"].(string)
	index, _ := req.Metadata["selected_auth_index"].(string)
	return requestlog.Entry{
		Time:       now,
		Endpoint:   endpoint,
		Model:      model,
		Stream:     stream,
		AuthID:     req.AuthID,
		AuthIndex:  strings.TrimSpace(index),
		RequestID:  cpaRequestID(req),
		ClientPath: strings.TrimSpace(path),
		UserAgent:  clip(strings.TrimSpace(req.Headers.Get("User-Agent")), maxUserAgent),
	}
}

func (s *Service) startRecord(req pluginapi.ExecutorRequest, call *upstreamCall, stream bool) *callRecord {
	now := time.Now()
	entry := baseEntry(req, call.endpoint, call.model, stream, now)
	entry.UpstreamModel = call.upstreamModel
	entry.ReasoningEffort = call.effort
	entry.Credential = call.keyMasked
	entry.Egress = call.egress
	return &callRecord{log: s.reqlog, start: now, entry: entry}
}

// recordFailure logs a call that failed before prepareCall produced a call.
func (s *Service) recordFailure(req pluginapi.ExecutorRequest, endpoint string, stream bool, perr *pluginabi.Error) {
	now := time.Now()
	rec := &callRecord{log: s.reqlog, start: now, entry: baseEntry(req, endpoint, clientModel(req, req.Model), stream, now)}
	rec.finish(perr, "request")
}

// response notes Wokey's status and request ID.
func (r *callRecord) response(resp *http.Response) {
	if r == nil {
		return
	}
	r.entry.UpstreamStatus = resp.StatusCode
	r.entry.UpstreamRequestID = firstNonEmpty(resp.Header.Get("X-Request-Id"), resp.Header.Get("Request-Id"))
}

// observedMembers are the top-level event members observePayload reads,
// besides delta.stop_reason.
var observedMembers = []string{"model", "message", "response", "usage", "stop_reason", "choices", "output"}

// observe looks at a stream event that went through the gate; ev is nil for
// raw chunks of a body that is not SSE.
func (r *callRecord) observe(kind streamgate.Kind, ev *streamgate.Event) {
	if kind == streamgate.Content {
		now := time.Now()
		if r.first.IsZero() {
			r.first = now
		}
		r.last = now
		r.frames++
	}
	if ev != nil && observable(ev) {
		if payload := ev.Payload(); payload != nil {
			r.observePayload(payload)
		}
	}
}

// observable reports whether an event carries anything observePayload
// reads. Text and tool deltas, the bulk of a stream, carry none of it and are
// not decoded.
func observable(ev *streamgate.Event) bool {
	for _, key := range observedMembers {
		if ev.Member(key) != nil {
			return true
		}
	}
	// Messages: message_delta reports the stop reason inside delta.
	return jsonfield.Has(ev.Member("delta"), "stop_reason")
}

// observeJSON looks at a buffered response body.
func (r *callRecord) observeJSON(data []byte) {
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return
	}
	r.observePayload(payload)
}

func (r *callRecord) observePayload(payload map[string]any) {
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
	if reason := finishReason(payload); reason != "" {
		r.entry.FinishReason = reason
	}
}

// finishReason reads why the model stopped from a response or stream event.
func finishReason(payload map[string]any) string {
	// Messages: message_delta carries delta.stop_reason, a buffered message
	// has it at the top.
	if delta, ok := payload["delta"].(map[string]any); ok {
		if reason := jsonfield.Text(delta["stop_reason"]); reason != "" {
			return reason
		}
	}
	if reason := jsonfield.Text(payload["stop_reason"]); reason != "" {
		return reason
	}
	// Chat Completions, buffered or streamed.
	if choices, ok := payload["choices"].([]any); ok {
		for _, raw := range choices {
			if choice, ok := raw.(map[string]any); ok {
				if reason := jsonfield.Text(choice["finish_reason"]); reason != "" {
					return reason
				}
			}
		}
	}
	// Responses: the response object is the body when buffered and sits
	// under "response" in stream events.
	response, ok := payload["response"].(map[string]any)
	if !ok {
		if _, buffered := payload["output"]; !buffered {
			return ""
		}
		response = payload
	}
	if details, ok := response["incomplete_details"].(map[string]any); ok {
		if reason := jsonfield.Text(details["reason"]); reason != "" {
			return reason
		}
	}
	switch status := jsonfield.Text(response["status"]); status {
	case "completed", "incomplete", "failed", "cancelled":
		return status
	}
	return ""
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
	entry.TokensPerSecond = requestlog.GenerationSpeed(entry.Stream, entry.OutputTokens, entry.DurationMS, entry.TTFTMS)
	entry.E2ETokensPerSecond = requestlog.E2ESpeed(entry.OutputTokens, entry.DurationMS)
	if answered := entry.UpstreamStatus >= 200 && entry.UpstreamStatus <= 299; answered {
		if entry.Stream {
			frames := r.frames
			entry.Frames = &frames
			if !r.last.IsZero() {
				last := r.last.Sub(r.start).Milliseconds()
				entry.LastFrameMS = &last
			}
		}
		// Usage is expected once a response is being delivered; an error
		// inside the stream before any output has none to report.
		if perr == nil || phase == "stream" {
			reported := r.usage.seen
			entry.UsageReported = &reported
		}
	}
	if perr != nil {
		entry.Status = "error"
		entry.HTTPStatus = perr.HTTPStatus
		entry.ErrorCode = perr.Code
		entry.Error = perr.Message
		entry.ErrorPhase = phase
		entry.RetryAfter = perr.RetryAfterSeconds
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
	// seen is set once any usage object arrived.
	seen bool
}

func (u *usageTotals) merge(raw any) {
	usage, ok := raw.(map[string]any)
	if !ok {
		return
	}
	u.seen = true
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

// reasoningEffort describes the requested thinking level, if any, from the
// request members found by topLevelMembers. Only those members are decoded.
func reasoningEffort(body []byte, found map[string]span) string {
	payload := make(map[string]any, 4)
	for _, key := range []string{"reasoning_effort", "reasoning", "output_config", "thinking"} {
		if value, ok := memberValue(body, found, key); ok {
			payload[key] = value
		}
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
// alias it used), falling back to the model CPA passed to the executor. The
// original request is only read when CPA did not report the name.
func clientModel(req pluginapi.ExecutorRequest, fallback string) string {
	if requested, _ := req.Metadata["requested_model"].(string); strings.TrimSpace(requested) != "" {
		return strings.TrimSpace(requested)
	}
	return firstNonEmpty(topLevelString(req.OriginalRequest, "model"), fallback)
}

// clip cuts text to at most limit bytes without splitting a character.
func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
