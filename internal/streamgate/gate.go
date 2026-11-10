// Package streamgate reads Wokey streaming responses: it splits SSE bodies
// into events and holds back the start of a stream until it is known not to
// be an error, so CPA can still retry it on another credential.
package streamgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// maxPreludeBytes caps how much of a stream is held back while waiting for
// the first output event.
const maxPreludeBytes = 64 << 10

// errStreamRejected stops the pump when Wokey reports an error inside the
// stream before any output was sent.
var errStreamRejected = errors.New("upstream rejected the request inside the stream")

// Kind classifies a stream event.
type Kind int

const (
	// Prelude carries no output yet (message_start, response.created, a
	// chat chunk with only the role, keepalive pings).
	Prelude Kind = iota
	// Content carries output.
	Content
	// Terminal ends the response.
	Terminal
	// Failed is an error reported inside the stream.
	Failed
)

// Gate sits between the upstream SSE reader and the host stream.
//
// CPA reads a plugin stream until the first chunk with a payload before it
// commits to the credential; an error before that is retried on another key.
// The gate holds the prelude events back until the first output event, so an
// error Wokey sends inside the stream before any output (rate limit, overload)
// still fails the call the same way an HTTP error would.
//
// It also records whether the response reached its terminal event, so a
// stream that just stops is reported as truncated instead of a normal end.
type Gate struct {
	endpoint  string
	sse       bool
	header    http.Header
	emit      func([]byte) error
	held      [][]byte
	heldBytes int
	open      bool
	done      bool
	failure   *pluginabi.Error
	// Tap, when set, sees every event with its classification. The event
	// is nil for raw chunks of a body that is not SSE.
	Tap func(Kind, *Event)
}

// New returns a gate for one stream; emit forwards events to the client.
func New(endpoint string, sse bool, header http.Header, emit func([]byte) error) *Gate {
	return &Gate{endpoint: endpoint, sse: sse, header: header, emit: emit}
}

// Push takes the next upstream event (or raw chunk for non-SSE bodies).
func (g *Gate) Push(event []byte) error {
	if !g.sse {
		g.open = true
		if g.Tap != nil {
			g.Tap(Content, nil)
		}
		return g.emit(event)
	}
	ev := NewEvent(event)
	kind, detail := classifyStreamEvent(g.endpoint, ev)
	if g.Tap != nil {
		g.Tap(kind, ev)
	}
	switch kind {
	case Failed:
		if !g.open {
			g.held = nil
			g.failure = wokey.UpstreamError(streamErrorStatus(detail), g.header, detail, g.endpoint)
			return errStreamRejected
		}
		// After output has started the client gets the error event as is.
		g.done = true
	case Terminal:
		g.done = true
	case Prelude:
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
	if g.endpoint != wokey.EndpointChat && isDoneEvent(event) {
		return nil
	}
	return g.emit(event)
}

func (g *Gate) flush() error {
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

// Finish reports how the stream ended: nil when the response is complete,
// otherwise the error to close the host stream with.
func (g *Gate) Finish(ctx context.Context, errPump error) *pluginabi.Error {
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

// classifyStreamEvent sorts an SSE event by protocol. For Failed it
// also returns the error as a JSON body wokey.UpstreamError can read.
//
// Messages and Responses events are told apart by their type, so only the
// few that need more (message_delta, response.failed) are decoded; chat
// chunks always are.
func classifyStreamEvent(endpoint string, ev *Event) (Kind, []byte) {
	data := ev.Data()
	if len(data) == 0 {
		return Prelude, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return Terminal, nil
	}
	if !ev.Object() {
		// Unknown data: do not hold it back.
		return Content, nil
	}
	eventType := ev.stringMember("type")
	if eventType == "" {
		eventType = ev.Name()
	}
	if eventType == "error" || (ev.present("error") && eventType != "response.failed") {
		return Failed, data
	}
	switch endpoint {
	case wokey.EndpointChat:
		payload := ev.Payload()
		if payload == nil {
			return Content, nil
		}
		return classifyChatChunk(payload), nil
	case wokey.EndpointResponses:
		switch eventType {
		case "response.completed", "response.incomplete":
			return Terminal, nil
		case "response.failed":
			payload := ev.Payload()
			if payload == nil {
				return Content, nil
			}
			return Failed, responseFailure(payload)
		case "response.created", "response.queued", "response.in_progress", "response.output_item.added", "response.content_part.added":
			return Prelude, nil
		}
	case wokey.EndpointMessages:
		switch eventType {
		case "message_stop":
			return Terminal, nil
		case "message_delta":
			payload := ev.Payload()
			if delta, _ := payload["delta"].(map[string]any); delta != nil && nonEmpty(delta["stop_reason"]) {
				return Terminal, nil
			}
		case "message_start", "ping", "content_block_start":
			return Prelude, nil
		}
	}
	return Content, nil
}

// classifyChatChunk treats a chunk with a finish reason as terminal and one
// with any delta besides the role as output.
func classifyChatChunk(payload map[string]any) Kind {
	choices, _ := payload["choices"].([]any)
	kind := Prelude
	for _, raw := range choices {
		choice, _ := raw.(map[string]any)
		if nonEmpty(choice["finish_reason"]) {
			return Terminal
		}
		delta, _ := choice["delta"].(map[string]any)
		for key, value := range delta {
			if key != "role" && nonEmpty(value) {
				kind = Content
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
// wokey.UpstreamError maps it the same way as the matching HTTP error.
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
			if value := strings.ToLower(jsonfield.Text(scope[key])); value != "" {
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

// Phase tells the request log whether output had reached the client.
func (g *Gate) Phase() string {
	if g.open {
		return "stream"
	}
	return "request"
}
