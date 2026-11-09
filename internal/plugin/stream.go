package plugin

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/streamgate"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
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

// executeStream opens the upstream stream synchronously, so HTTP errors still
// reach CPA as a failed call it can retry on another credential, then pumps
// the body to the host stream in the background. The pump goes through a
// streamgate.Gate, which turns an error inside the stream before any output into
// the same failed call and reports a stream that stops early as truncated.
func (s *Service) executeStream(req rpcExecutorRequest) []byte {
	endpoint, ok := wokey.EndpointForFormat(req.Format)
	if !ok {
		return errorEnvelopeFrom(wokey.RequestError("unsupported_format", "wokey: unsupported executor format "+req.Format))
	}
	req.Stream = true
	call, perr := s.prepareCall(req.ExecutorRequest, endpoint)
	if perr != nil {
		s.recordFailure(req.ExecutorRequest, endpoint, true, perr)
		return errorEnvelopeFrom(perr)
	}
	rec := s.startRecord(call, true)
	fail := func(perr *pluginabi.Error) []byte {
		rec.finish(perr, "request")
		return errorEnvelopeFrom(perr)
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	httpReq, errReq := call.newRequest(ctx)
	if errReq != nil {
		cancel(nil)
		return fail(wokey.TransportError(errReq))
	}
	// Bound the wait for response headers; the body is bounded by the idle timer.
	headerTimer := time.AfterFunc(s.config().RequestTimeout, func() { cancel(streamgate.ErrIdle) })
	resp, errDo := call.client.Do(httpReq)
	headerTimer.Stop()
	if errDo != nil {
		cancel(nil)
		return fail(wokey.TransportError(errDo))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, wokey.MaxErrorBodyBytes*4))
		_ = resp.Body.Close()
		cancel(nil)
		return fail(wokey.UpstreamError(resp.StatusCode, resp.Header, body, endpoint))
	}
	headers := filterHeaders(resp.Header, forwardedResponseHeaders)
	sse := streamgate.IsEventStream(resp.Header.Get("Content-Type"))

	if req.StreamID == "" {
		// Hosts without a stream bridge: buffer the whole stream.
		defer cancel(nil)
		defer func() { _ = resp.Body.Close() }()
		var chunks []pluginapi.ExecutorStreamChunk
		gate := streamgate.New(endpoint, sse, resp.Header, func(payload []byte) error {
			chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: payload})
			return nil
		})
		gate.Tap = rec.observe
		if failure := gate.Finish(ctx, streamgate.Pump(resp.Body, sse, gate.Push, func() {})); failure != nil {
			return fail(failure)
		}
		rec.finish(nil, "")
		return okEnvelope(rpcExecutorStreamResponse{Headers: headers, Chunks: chunks})
	}

	s.streams.Add(1)
	go func() {
		defer s.streams.Done()
		defer cancel(nil)
		defer func() { _ = resp.Body.Close() }()
		idle := s.config().StreamIdleTimeout
		idleTimer := time.AfterFunc(idle, func() { cancel(streamgate.ErrIdle) })
		defer idleTimer.Stop()
		var emitErr error
		gate := streamgate.New(endpoint, sse, resp.Header, func(payload []byte) error {
			if errEmit := s.emitStream(req.StreamID, payload); errEmit != nil {
				emitErr = errEmit
				return errEmit
			}
			return nil
		})
		gate.Tap = rec.observe
		errPump := streamgate.Pump(resp.Body, sse, gate.Push, func() { idleTimer.Reset(idle) })
		if emitErr != nil {
			// The client is gone or CPA closed the stream; nothing to report.
			s.closeStream(req.StreamID, nil)
			rec.finish(&pluginabi.Error{Code: "client_closed", Message: "client closed the stream: " + emitErr.Error()}, gate.Phase())
			return
		}
		failure := gate.Finish(ctx, errPump)
		s.closeStream(req.StreamID, failure)
		rec.finish(failure, gate.Phase())
	}()
	return okEnvelope(rpcExecutorStreamResponse{Headers: headers})
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
