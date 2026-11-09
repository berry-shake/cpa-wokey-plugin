package streamgate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// ErrIdle is the context cause set when an upstream stream stops sending.
var ErrIdle = errors.New("stream idle timeout")

// streamFailure maps a pump error to the error the stream is closed with.
func streamFailure(ctx context.Context, errPump error) *pluginabi.Error {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, ErrIdle):
		return &pluginabi.Error{Code: "stream_idle_timeout", Message: "wokey: upstream stream stalled", HTTPStatus: http.StatusGatewayTimeout, Retryable: true}
	case errors.Is(cause, context.Canceled):
		return &pluginabi.Error{Code: "plugin_shutdown", Message: "wokey: plugin is shutting down", HTTPStatus: http.StatusServiceUnavailable, Retryable: true}
	default:
		return &pluginabi.Error{Code: "upstream_stream_error", Message: "wokey: " + errPump.Error(), HTTPStatus: http.StatusBadGateway, Retryable: true}
	}
}

// IsEventStream reports whether contentType is text/event-stream.
func IsEventStream(contentType string) bool {
	mediaType, _, errParse := mime.ParseMediaType(contentType)
	return errParse == nil && mediaType == "text/event-stream"
}

// Pump forwards an upstream body. SSE bodies are split into whole
// events with keepalive comments removed; other bodies are forwarded in raw
// chunks. progress is called whenever upstream bytes arrive.
func Pump(body io.Reader, sse bool, emit func([]byte) error, progress func()) error {
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
