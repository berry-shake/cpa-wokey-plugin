package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	endpointChat        = "/v1/chat/completions"
	endpointResponses   = "/v1/responses"
	endpointMessages    = "/v1/messages"
	endpointCountTokens = "/v1/messages/count_tokens"

	defaultAnthropicVersion = "2023-06-01"

	// maxResponseBytes bounds a buffered (non-streaming) upstream response.
	maxResponseBytes = 64 << 20
)

// executorFormats are the protocols Wokey serves natively. Declaring all three
// as both input and output lets CPA pass requests through untranslated.
var executorFormats = []string{"chat-completions", "responses", "anthropic"}

// forwardedRequestHeaders are the client headers that change upstream
// behavior and are safe to pass on. Client credentials are never forwarded.
var forwardedRequestHeaders = []string{"Anthropic-Beta", "Anthropic-Version", "X-End-User-Id"}

// forwardedResponseHeaders are the upstream headers returned to CPA.
var forwardedResponseHeaders = []string{"Content-Type", "X-Request-Id", "Request-Id", "Retry-After"}

// endpointForFormat maps a CPA executor format to the Wokey endpoint.
func endpointForFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return endpointChat, true
	case "openai-response", "openai-responses", "responses":
		return endpointResponses, true
	case "claude", "anthropic":
		return endpointMessages, true
	default:
		return "", false
	}
}

// upstreamCall is a fully prepared Wokey request.
type upstreamCall struct {
	endpoint string
	url      string
	body     []byte
	header   http.Header
	client   *http.Client
	authID   string
	// model, upstreamModel and label describe the call for the request log.
	model         string
	upstreamModel string
	label         string
}

// prepareCall resolves the credential, egress and upstream model for one
// executor request.
func (s *Service) prepareCall(req pluginapi.ExecutorRequest, endpoint string) (*upstreamCall, *pluginabi.Error) {
	cred, _, errCred := credentialFromMaps(req.StorageJSON, req.AuthMetadata)
	if errCred != nil {
		return nil, authError("wokey: " + errCred.Error())
	}
	if cred.APIKey == "" {
		return nil, authError("wokey: credential has no api_key")
	}
	cfg := s.config()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = topLevelString(req.Payload, "model")
	}
	if model == "" {
		return nil, requestError("invalid_request", "wokey: request has no model")
	}
	upstream := upstreamModelID(model, cred.effectivePrefix(cfg), s.catalog.current())
	body, errBody := setTopLevelString(req.Payload, "model", upstream)
	if errBody != nil {
		return nil, requestError("invalid_request", "wokey: "+errBody.Error())
	}
	if endpoint == endpointChat && req.Stream {
		// Ask for the final usage chunk so CPA can account the request.
		body = ensureTopLevelObject(body, "stream_options", `{"include_usage":true}`)
	}
	setting, errProxy := resolveProxy(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, &pluginabi.Error{Code: "invalid_proxy", Message: "wokey: " + errProxy.Error(), HTTPStatus: http.StatusBadGateway}
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cred.APIKey)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", s.userAgent())
	if req.Stream && endpoint != endpointCountTokens {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	for _, name := range forwardedRequestHeaders {
		if value := strings.TrimSpace(req.Headers.Get(name)); value != "" {
			header.Set(name, value)
		}
	}
	if endpoint == endpointMessages || endpoint == endpointCountTokens {
		if header.Get("Anthropic-Version") == "" {
			header.Set("Anthropic-Version", defaultAnthropicVersion)
		}
	} else {
		header.Del("Anthropic-Beta")
		header.Del("Anthropic-Version")
	}
	return &upstreamCall{
		endpoint: endpoint,
		url:      cred.effectiveBaseURL(cfg) + endpoint,
		body:     body,
		header:   header,
		client:   s.clients.client(setting),
		authID:   req.AuthID,

		model:         clientModel(req, model),
		upstreamModel: upstream,
		label:         firstNonEmpty(cred.Label, req.AuthID),
	}, nil
}

func (c *upstreamCall) newRequest(ctx context.Context) (*http.Request, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(c.body))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header = c.header.Clone()
	return httpReq, nil
}

// execute performs a buffered request and returns the upstream body as is.
func (s *Service) execute(req pluginapi.ExecutorRequest) []byte {
	endpoint, ok := endpointForFormat(req.Format)
	if !ok {
		return errorEnvelopeFrom(requestError("unsupported_format", "wokey: unsupported executor format "+req.Format))
	}
	return s.executeBuffered(req, endpoint)
}

// countTokens serves Anthropic count_tokens; Wokey has no equivalent for the
// OpenAI protocols.
func (s *Service) countTokens(req pluginapi.ExecutorRequest) []byte {
	endpoint, ok := endpointForFormat(req.Format)
	if !ok || endpoint != endpointMessages {
		return errorEnvelopeFrom(&pluginabi.Error{
			Code:       "unsupported_operation",
			Message:    "wokey: count_tokens is only available for the Anthropic messages format",
			HTTPStatus: http.StatusNotImplemented,
		})
	}
	return s.executeBuffered(req, endpointCountTokens)
}

func (s *Service) executeBuffered(req pluginapi.ExecutorRequest, endpoint string) []byte {
	call, perr := s.prepareCall(req, endpoint)
	if perr != nil {
		s.recordFailure(clientModel(req, req.Model), endpoint, false, req.AuthID, perr)
		return errorEnvelopeFrom(perr)
	}
	var rec *callRecord
	if endpoint != endpointCountTokens {
		rec = s.startRecord(call, false)
	}
	fail := func(perr *pluginabi.Error) []byte {
		rec.finish(perr, "request")
		return errorEnvelopeFrom(perr)
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config().RequestTimeout)
	defer cancel()
	httpReq, errReq := call.newRequest(ctx)
	if errReq != nil {
		return fail(transportError(errReq))
	}
	resp, errDo := call.client.Do(httpReq)
	if errDo != nil {
		return fail(transportError(errDo))
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if errRead != nil {
		return fail(transportError(fmt.Errorf("read response: %w", errRead)))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(upstreamError(resp.StatusCode, resp.Header, body, endpoint))
	}
	if rec != nil {
		rec.observeJSON(body)
		rec.finish(nil, "")
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: body,
		Headers: filterHeaders(resp.Header, forwardedResponseHeaders),
	})
}

func filterHeaders(source http.Header, names []string) http.Header {
	out := http.Header{}
	for _, name := range names {
		if values := source.Values(name); len(values) > 0 {
			out[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	return out
}

// topLevelString returns a string member of a JSON object, or "".
func topLevelString(body []byte, key string) string {
	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &fields); errUnmarshal != nil {
		return ""
	}
	var value string
	if errValue := json.Unmarshal(fields[key], &value); errValue != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// topLevelValueSpan finds the byte range of a top-level member value without
// re-encoding the document, so the payload reaches Wokey byte-for-byte except
// for the edited member.
func topLevelValueSpan(body []byte, key string) (start, end int, found bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	token, errToken := dec.Token()
	if errToken != nil {
		return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errToken)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return 0, 0, false, fmt.Errorf("payload is not a JSON object")
	}
	for dec.More() {
		keyToken, errKey := dec.Token()
		if errKey != nil {
			return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errKey)
		}
		name, _ := keyToken.(string)
		var raw json.RawMessage
		if errValue := dec.Decode(&raw); errValue != nil {
			return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errValue)
		}
		if name == key {
			end = int(dec.InputOffset())
			return end - len(raw), end, true, nil
		}
	}
	return 0, 0, false, nil
}

// setTopLevelString sets a top-level string member, inserting it when absent.
func setTopLevelString(body []byte, key, value string) ([]byte, error) {
	encoded, _ := json.Marshal(value)
	start, end, found, errSpan := topLevelValueSpan(body, key)
	if errSpan != nil {
		return nil, errSpan
	}
	if found {
		out := make([]byte, 0, len(body)-(end-start)+len(encoded))
		out = append(out, body[:start]...)
		out = append(out, encoded...)
		out = append(out, body[end:]...)
		return out, nil
	}
	return insertTopLevelMember(body, key, encoded), nil
}

// ensureTopLevelObject adds key with rawValue when the member is absent.
func ensureTopLevelObject(body []byte, key, rawValue string) []byte {
	_, _, found, errSpan := topLevelValueSpan(body, key)
	if errSpan != nil || found {
		return body
	}
	return insertTopLevelMember(body, key, []byte(rawValue))
}

func insertTopLevelMember(body []byte, key string, rawValue []byte) []byte {
	open := bytes.IndexByte(body, '{')
	if open < 0 {
		return body
	}
	encodedKey, _ := json.Marshal(key)
	member := append(append(append([]byte{}, encodedKey...), ':'), rawValue...)
	rest := bytes.TrimLeft(body[open+1:], " \t\r\n")
	out := make([]byte, 0, len(body)+len(member)+1)
	out = append(out, body[:open+1]...)
	out = append(out, member...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	out = append(out, body[open+1:]...)
	return out
}
