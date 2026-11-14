package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
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

// upstreamCall is a fully prepared Wokey request.
type upstreamCall struct {
	endpoint string
	url      string
	body     []byte
	header   http.Header
	client   *http.Client
	authID   string
	// model, upstreamModel, effort, keyMasked and egress describe the call
	// for the request log.
	model         string
	upstreamModel string
	effort        string
	keyMasked     string
	egress        string
}

// requestMembers are the top-level request members prepareCall reads or
// edits; the rest of the body is passed through without being decoded.
var requestMembers = []string{"model", "stream_options", "reasoning_effort", "reasoning", "output_config", "thinking"}

// prepareCall resolves the credential, egress and upstream model for one
// executor request.
func (s *Service) prepareCall(req pluginapi.ExecutorRequest, endpoint string) (*upstreamCall, *pluginabi.Error) {
	s.noteWokeyAuth(req.AuthID)
	cred, _, errCred := credential.FromMaps(req.StorageJSON, req.AuthMetadata)
	if errCred != nil {
		return nil, wokey.AuthError("wokey: " + errCred.Error())
	}
	if cred.APIKey == "" {
		return nil, wokey.AuthError("wokey: credential has no api_key")
	}
	cfg := s.config()
	members, errScan := topLevelMembers(req.Payload, requestMembers...)
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = memberString(req.Payload, members, "model")
	}
	if model == "" {
		return nil, wokey.RequestError("invalid_request", "wokey: request has no model")
	}
	upstream := s.catalog.Current().UpstreamID(model, cred.EffectivePrefix(cfg))
	encodedUpstream, _ := json.Marshal(upstream)
	body, errBody := setMember(req.Payload, members, errScan, "model", encodedUpstream)
	if errBody != nil {
		return nil, wokey.RequestError("invalid_request", "wokey: "+errBody.Error())
	}
	if _, present := members["stream_options"]; endpoint == wokey.EndpointChat && req.Stream && !present && errScan == nil {
		// Ask for the final usage chunk so CPA can account the request.
		body = insertTopLevelMember(body, "stream_options", []byte(`{"include_usage":true}`))
	}
	effort := ""
	if errScan == nil {
		effort = reasoningEffort(req.Payload, members)
	}
	setting, errProxy := wokey.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, &pluginabi.Error{Code: "invalid_proxy", Message: "wokey: " + errProxy.Error(), HTTPStatus: http.StatusBadGateway}
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+cred.APIKey)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", s.userAgent())
	if req.Stream {
		header.Set("Accept", "text/event-stream")
	} else {
		header.Set("Accept", "application/json")
	}
	for _, name := range forwardedRequestHeaders {
		if value := strings.TrimSpace(req.Headers.Get(name)); value != "" {
			header.Set(name, value)
		}
	}
	if endpoint == wokey.EndpointMessages {
		if header.Get("Anthropic-Version") == "" {
			header.Set("Anthropic-Version", defaultAnthropicVersion)
		}
	} else {
		header.Del("Anthropic-Beta")
		header.Del("Anthropic-Version")
	}
	return &upstreamCall{
		endpoint: endpoint,
		url:      cred.EffectiveBaseURL(cfg) + endpoint,
		body:     body,
		header:   header,
		client:   s.clients.Client(setting),
		authID:   req.AuthID,

		model:         clientModel(req, model),
		upstreamModel: upstream,
		effort:        effort,
		keyMasked:     credential.MaskKey(cred.APIKey),
		egress:        setting.Label(),
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
	endpoint, ok := wokey.EndpointForFormat(req.Format)
	if !ok {
		return errorEnvelopeFrom(wokey.RequestError("unsupported_format", "wokey: unsupported executor format "+req.Format))
	}
	return s.executeBuffered(req, endpoint)
}

func (s *Service) executeBuffered(req pluginapi.ExecutorRequest, endpoint string) []byte {
	call, perr := s.prepareCall(req, endpoint)
	if perr != nil {
		s.recordFailure(req, endpoint, false, perr)
		return errorEnvelopeFrom(perr)
	}
	rec := s.startRecord(req, call, false)
	fail := func(perr *pluginabi.Error) []byte {
		rec.finish(perr, "request")
		return errorEnvelopeFrom(perr)
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config().RequestTimeout)
	defer cancel()
	httpReq, errReq := call.newRequest(ctx)
	if errReq != nil {
		return fail(wokey.TransportError(errReq))
	}
	resp, errDo := call.client.Do(httpReq)
	if errDo != nil {
		return fail(wokey.TransportError(errDo))
	}
	defer func() { _ = resp.Body.Close() }()
	rec.response(resp)
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if errRead != nil {
		return fail(wokey.TransportError(fmt.Errorf("read response: %w", errRead)))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(wokey.UpstreamError(resp.StatusCode, resp.Header, body))
	}
	rec.observeJSON(body)
	rec.finish(nil, "")
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
