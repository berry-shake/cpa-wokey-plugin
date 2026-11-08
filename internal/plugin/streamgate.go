package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// maxPreludeBytes caps how much of a stream is held back while waiting for
// the first output event.
const maxPreludeBytes = 64 << 10

// errStreamRejected stops the pump when Wokey reports an error inside the
// stream before any output was sent.
var errStreamRejected = errors.New("upstream rejected the request inside the stream")

type streamEventKind int

const (
	// streamPrelude carries no output yet (message_start, response.created, a
	// chat chunk with only the role, keepalive pings).
	streamPrelude streamEventKind = iota
	// streamContent carries output.
	streamContent
	// streamTerminal ends the response.
	streamTerminal
	// streamFailed is an error reported inside the stream.
	streamFailed
)

// streamGate sits between the upstream SSE reader and the host stream.
//
// CPA reads a plugin stream until the first chunk with a payload before it
// commits to the credential; an error before that is retried on another key.
// The gate holds the prelude events back until the first output event, so an
// error Wokey sends inside the stream before any output (rate limit, overload)
// still fails the call the same way an HTTP error would.
//
// It also records whether the response reached its terminal event, so a
// stream that just stops is reported as truncated instead of a normal end.
type streamGate struct {
	endpoint  string
	sse       bool
	header    http.Header
	emit      func([]byte) error
	held      [][]byte
	heldBytes int
	open      bool
	done      bool
	failure   *pluginabi.Error
}

func newStreamGate(endpoint string, sse bool, header http.Header, emit func([]byte) error) *streamGate {
	return &streamGate{endpoint: endpoint, sse: sse, header: header, emit: emit}
}

// push takes the next upstream event (or raw chunk for non-SSE bodies).
func (g *streamGate) push(event []byte) error {
	if !g.sse {
		g.open = true
		return g.emit(event)
	}
	kind, detail := classifyStreamEvent(g.endpoint, event)
	switch kind {
	case streamFailed:
		if !g.open {
			g.held = nil
			g.failure = upstreamError(streamErrorStatus(detail), g.header, detail, g.endpoint)
			return errStreamRejected
		}
		// After output has started the client gets the error event as is.
		g.done = true
	case streamTerminal:
		g.done = true
	case streamPrelude:
		if !g.open && g.heldBytes+len(event) <= maxPreludeBytes {
			g.held = append(g.held, event)
			g.heldBytes += len(event)
			return nil
		}
	}
	if errFlush := g.flush(); errFlush != nil {
		return errFlush
	}
	// "[DONE]" is kept for chat completions (CPA handles it there) and dropped
	// for the Responses and Messages protocols, which do not define it.
	if g.endpoint != endpointChat && isDoneEvent(event) {
		return nil
	}
	return g.emit(event)
}

func (g *streamGate) flush() error {
	g.open = true
	held := g.held
	g.held, g.heldBytes = nil, 0
	for _, event := range held {
		if errEmit := g.emit(event); errEmit != nil {
			return errEmit
		}
	}
	return nil
}

// finish reports how the stream ended: nil when the response is complete,
// otherwise the error to close the host stream with.
func (g *streamGate) finish(ctx context.Context, errPump error) *pluginabi.Error {
	switch {
	case g.failure != nil:
		return g.failure
	case g.done:
		return nil
	case errPump != nil:
		return streamFailure(ctx, errPump)
	case !g.sse:
		return nil
	default:
		return streamTruncated()
	}
}

// streamTruncated reports an accepted stream that ended without its terminal
// event. CPA treats the stream_truncated code as a 502 that does not cool the
// credential down.
func streamTruncated() *pluginabi.Error {
	return &pluginabi.Error{
		Code:       "stream_truncated",
		Message:    "wokey: upstream stream ended before the response finished",
		HTTPStatus: http.StatusBadGateway,
		Retryable:  true,
	}
}

// classifyStreamEvent sorts an SSE event by protocol. For streamFailed it
// also returns the error as a JSON body upstreamError can read.
func classifyStreamEvent(endpoint string, event []byte) (streamEventKind, []byte) {
	name, data := sseEventFields(event)
	if len(data) == 0 {
		return streamPrelude, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return streamTerminal, nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		// Unknown data: do not hold it back.
		return streamContent, nil
	}
	eventType, _ := payload["type"].(string)
	if eventType == "" {
		eventType = name
	}
	if eventType == "error" || (payload["error"] != nil && eventType != "response.failed") {
		return streamFailed, data
	}
	switch endpoint {
	case endpointChat:
		return classifyChatChunk(payload), nil
	case endpointResponses:
		switch eventType {
		case "response.completed", "response.incomplete":
			return streamTerminal, nil
		case "response.failed":
			return streamFailed, responseFailure(payload)
		case "response.created", "response.queued", "response.in_progress", "response.output_item.added", "response.content_part.added":
			return streamPrelude, nil
		}
	case endpointMessages:
		switch eventType {
		case "message_stop":
			return streamTerminal, nil
		case "message_delta":
			if delta, _ := payload["delta"].(map[string]any); delta != nil && nonEmpty(delta["stop_reason"]) {
				return streamTerminal, nil
			}
		case "message_start", "ping", "content_block_start":
			return streamPrelude, nil
		}
	}
	return streamContent, nil
}

// classifyChatChunk treats a chunk with a finish reason as terminal and one
// with any delta besides the role as output.
func classifyChatChunk(payload map[string]any) streamEventKind {
	choices, _ := payload["choices"].([]any)
	kind := streamPrelude
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if nonEmpty(choice["finish_reason"]) {
			return streamTerminal
		}
		delta, _ := choice["delta"].(map[string]any)
		for key, value := range delta {
			if key != "role" && nonEmpty(value) {
				kind = streamContent
			}
		}
	}
	return kind
}

// responseFailure turns a response.failed event into {"error":{...}}.
func responseFailure(payload map[string]any) []byte {
	response, _ := payload["response"].(map[string]any)
	failure, _ := response["error"].(map[string]any)
	if failure == nil {
		failure = map[string]any{"code": "response_failed", "message": "response failed"}
	}
	raw, _ := json.Marshal(map[string]any{"error": failure})
	return raw
}

// streamErrorStatus picks the HTTP status an in-stream error stands for, so
// upstreamError maps it the same way as the matching HTTP error.
func streamErrorStatus(body []byte) int {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	scopes := []map[string]any{payload}
	if inner, ok := payload["error"].(map[string]any); ok {
		scopes = append(scopes, inner)
	}
	var names []string
	for _, scope := range scopes {
		for _, key := range []string{"status", "status_code", "http_status", "code"} {
			if number, ok := scope[key].(float64); ok && number >= 400 && number <= 599 {
				return int(number)
			}
		}
		for _, key := range []string{"code", "type"} {
			if value := strings.ToLower(anyString(scope[key])); value != "" {
				names = append(names, value)
			}
		}
	}
	text := strings.Join(names, " ")
	for _, rule := range streamErrorRules {
		for _, word := range rule.words {
			if strings.Contains(text, word) {
				return rule.status
			}
		}
	}
	return http.StatusBadGateway
}

var streamErrorRules = []struct {
	status int
	words  []string
}{
	{http.StatusPaymentRequired, []string{"insufficient_balance", "insufficient_quota", "billing"}},
	{http.StatusTooManyRequests, []string{"rate_limit", "too_many_requests"}},
	{529, []string{"overloaded"}},
	{http.StatusUnauthorized, []string{"authentication", "invalid_api_key", "unauthorized"}},
	{http.StatusForbidden, []string{"request_blocked", "model_denylisted", "account_restricted", "permission"}},
	{http.StatusNotFound, []string{"not_found"}},
	{http.StatusGatewayTimeout, []string{"timeout"}},
	{http.StatusBadRequest, []string{"invalid_request", "context_length", "too_large"}},
}

// sseEventFields returns the event name and the joined data lines.
func sseEventFields(event []byte) (string, []byte) {
	var name string
	var data [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimSpace(line[len("data:"):]))
		}
	}
	return name, bytes.TrimSpace(bytes.Join(data, []byte("\n")))
}

func nonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
