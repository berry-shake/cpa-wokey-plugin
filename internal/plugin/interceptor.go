package plugin

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// cpaRequestIDHeader carries CPA's request ID to the executor. CPA hands its
// request ID only to request interceptors, so the after-auth interceptor puts
// it on requests that CPA routed to a Wokey credential. The executor forwards
// an allowlist of headers upstream, so this one never reaches Wokey.
const cpaRequestIDHeader = "X-Wokey-Cpa-Request-Id"

// selectedAuthMetadataKey is the request metadata key CPA sets to the auth it
// picked before running after-auth interceptors.
const selectedAuthMetadataKey = "selected_auth_id"

type rpcRequestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// interceptsRequests reports whether the negotiated schema lets the request
// interceptor skip the request body. Older hosts would send every request
// body to the plugin twice, so the interceptor stays off there.
func interceptsRequests(schema uint32) bool {
	return schema >= pluginabi.SchemaVersionRequestInterceptorOmitBody
}

// interceptAfterAuth stamps CPA's request ID on requests bound for a Wokey
// credential, replacing any value the client sent.
func (s *Service) interceptAfterAuth(req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	authID, _ := req.Metadata[selectedAuthMetadataKey].(string)
	if !s.isWokeyAuth(authID) {
		return pluginapi.RequestInterceptResponse{}
	}
	if id := strings.TrimSpace(req.TraceID); id != "" {
		return pluginapi.RequestInterceptResponse{Headers: http.Header{cpaRequestIDHeader: {id}}}
	}
	return pluginapi.RequestInterceptResponse{ClearHeaders: []string{cpaRequestIDHeader}}
}

// noteWokeyAuth remembers an auth ID that belongs to this plugin.
func (s *Service) noteWokeyAuth(authID string) {
	if authID = strings.TrimSpace(authID); authID != "" {
		s.authIDs.Store(authID, struct{}{})
	}
}

func (s *Service) isWokeyAuth(authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	_, ok := s.authIDs.Load(authID)
	return ok
}

// cpaRequestID reads the request ID the interceptor attached.
func cpaRequestID(req pluginapi.ExecutorRequest) string {
	return strings.TrimSpace(req.Headers.Get(cpaRequestIDHeader))
}
