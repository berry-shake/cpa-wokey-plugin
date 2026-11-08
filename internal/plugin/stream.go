package plugin

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcExecutorStreamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID    string           `json:"stream_id"`
	Error       string           `json:"error,omitempty"`
	ErrorDetail *pluginabi.Error `json:"error_detail,omitempty"`
}

var errStreamIdle = errors.New("stream idle timeout")

// executeStream opens the upstream stream synchronously, so HTTP errors still
// reach CPA as a failed call it can retry on another credential, then pumps
// the body to the host stream in the background. The pump goes through a
// streamGate, which turns an error inside the stream before any output into
// the same failed call and reports a stream that stops early as truncated.
func (s *Service) executeStream(req rpcExecutorRequest) []byte {
	endpoint, ok := endpointForFormat(req.Format)
	if !ok {
		return errorEnvelopeFrom(requestError("unsupported_format", "wokey: unsupported executor format "+req.Format))
	}
	req.Stream = true
	call, perr := s.prepareCall(req.ExecutorRequest, endpoint)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	httpReq, errReq := call.newRequest(ctx)
	if errReq != nil {
		cancel(nil)
		return errorEnvelopeFrom(transportError(errReq))
	}
	// Bound the wait for response headers; the body is bounded by the idle timer.
	headerTimer := time.AfterFunc(s.config().RequestTimeout, func() { cancel(errStreamIdle) })
	resp, errDo := call.client.Do(httpReq)
	headerTimer.Stop()
	if errDo != nil {
		cancel(nil)
		return errorEnvelopeFrom(transportError(errDo))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes*4))
		_ = resp.Body.Close()
		cancel(nil)
		return errorEnvelopeFrom(upstreamError(resp.StatusCode, resp.Header, body, endpoint))
	}
	headers := filterHeaders(resp.Header, forwardedResponseHeaders)
	sse := isEventStream(resp.Header.Get("Content-Type"))

	if req.StreamID == "" {
		// Hosts without a stream bridge: buffer the whole stream.
		defer cancel(nil)
		defer func() { _ = resp.Body.Close() }()
		var chunks []pluginapi.ExecutorStreamChunk
		gate := newStreamGate(endpoint, sse, resp.Header, func(payload []byte) error {
			chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
			return nil
		})
		if failure := gate.finish(ctx, pumpStream(resp.Body, sse, gate.push, func() {})); failure != nil {
			return errorEnvelopeFrom(failure)
		}
		return okEnvelope(rpcExecutorStreamResponse{Headers: headers, Chunks: chunks})
	}

	s.streams.Add(1)
	go func() {
		defer s.streams.Done()
		defer cancel(nil)
		defer func() { _ = resp.Body.Close() }()
		idle := s.config().StreamIdleTimeout
		idleTimer := time.AfterFunc(idle, func() { cancel(errStreamIdle) })
		defer idleTimer.Stop()
		var emitErr error
		gate := newStreamGate(endpoint, sse, resp.Header, func(payload []byte) error {
			if errEmit := s.emitStream(req.StreamID, payload); errEmit != nil {
				emitErr = errEmit
				return errEmit
			}
			return nil
		})
		errPump := pumpStream(resp.Body, sse, gate.push, func() { idleTimer.Reset(idle) })
		if emitErr != nil {
			// The client is gone or CPA closed the stream; nothing to report.
			s.closeStream(req.StreamID, nil)
			return
		}
		s.closeStream(req.StreamID, gate.finish(ctx, errPump))
	}()
	return okEnvelope(rpcExecutorStreamResponse{Headers: headers})
}

func streamFailure(ctx context.Context, errPump error) *pluginabi.Error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, errStreamIdle):
		return &pluginabi.Error{Code: "stream_idle_timeout", Message: "wokey: upstream stream stalled", HTTPStatus: http.StatusGatewayTimeout, Retryable: true}
	case errors.Is(cause, context.Canceled):
		return &pluginabi.Error{Code: "plugin_shutdown", Message: "wokey: plugin is shutting down", HTTPStatus: http.StatusServiceUnavailable, Retryable: true}
	default:
		return &pluginabi.Error{Code: "upstream_stream_error", Message: "wokey: " + errPump.Error(), HTTPStatus: http.StatusBadGateway, Retryable: true}
	}
}

func isEventStream(contentType string) bool {
	mediaType, _, errParse := mime.ParseMediaType(contentType)
	return errParse == nil && mediaType == "text/event-stream"
}

// pumpStream forwards an upstream body. SSE bodies are split into whole
// events with keepalive comments removed; other bodies are forwarded in raw
// chunks. progress is called whenever upstream bytes arrive.
func pumpStream(body io.Reader, sse bool, emit func([]byte) error, progress func()) error {
	if !sse {
		buf := make([]byte, 32<<10)
		for {
			n, errRead := body.Read(buf)
			if n > 0 {
				progress()
				if errEmit := emit(bytes.Clone(buf[:n])); errEmit != nil {
					return errEmit
				}
			}
			if errors.Is(errRead, io.EOF) {
				return nil
			}
			if errRead != nil {
				return errRead
			}
		}
	}
	reader := newSSEReader(body)
	for {
		event, errRead := reader.next(progress)
		if len(event) > 0 {
			if errEmit := emit(event); errEmit != nil {
				return errEmit
			}
		}
		if errors.Is(errRead, io.EOF) {
			return nil
		}
		if errRead != nil {
			return errRead
		}
	}
}

// sseReader yields complete SSE events terminated by a blank line.
type sseReader struct {
	reader *bufio.Reader
}

func newSSEReader(body io.Reader) *sseReader {
	return &sseReader{reader: bufio.NewReaderSize(body, 64<<10)}
}

// next returns the next event with comment lines removed and normalized to
// "\n" line endings plus a terminating blank line. Comment-only events
// (keepalives) are skipped. At EOF a trailing partial event is returned with
// io.EOF.
func (r *sseReader) next(progress func()) ([]byte, error) {
	var event bytes.Buffer
	for {
		line, errRead := r.reader.ReadBytes('\n')
		if len(line) > 0 {
			progress()
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		switch {
		case len(trimmed) == 0 && len(line) > 0:
			if event.Len() > 0 {
				event.WriteByte('\n')
				return event.Bytes(), nil
			}
		case len(trimmed) > 0 && trimmed[0] == ':':
			// SSE comment (Wokey sends ": keepalive" before the first token).
		case len(trimmed) > 0:
			event.Write(trimmed)
			event.WriteByte('\n')
		}
		if errRead != nil {
			if event.Len() > 0 {
				event.WriteByte('\n')
				return event.Bytes(), errRead
			}
			return nil, errRead
		}
	}
}

// isDoneEvent reports whether an event is the OpenAI "[DONE]" sentinel.
func isDoneEvent(event []byte) bool {
	for _, line := range bytes.Split(bytes.TrimSpace(event), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			return false
		}
		if !bytes.Equal(bytes.TrimSpace(line[len("data:"):]), []byte("[DONE]")) {
			return false
		}
	}
	return true
}

func (s *Service) emitStream(streamID string, payload []byte) error {
	return s.hostCall(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: streamID, Payload: payload}, nil)
}

func (s *Service) closeStream(streamID string, detail *pluginabi.Error) {
	req := rpcStreamCloseRequest{StreamID: streamID}
	if detail != nil {
		req.Error = detail.Message
		req.ErrorDetail = detail
	}
	if errClose := s.hostCall(pluginabi.MethodHostStreamClose, req, nil); errClose != nil {
		s.logf("debug", "wokey: close stream failed", map[string]any{"error": errClose.Error()})
	}
}

// streamGroup tracks background stream pumps.
type streamGroup struct {
	sync.WaitGroup
}

func (g *streamGroup) waitTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
