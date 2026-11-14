// Package wokey holds what the plugin knows about the Wokey API itself:
// endpoints, error mapping, egress (proxies and HTTP clients) and the
// account, key and issuing data it reports.
package wokey

import "strings"

// ProviderID is the CPA provider key and the auth file type.
const ProviderID = "wokey"

const (
	EndpointChat      = "/v1/chat/completions"
	EndpointResponses = "/v1/responses"
	EndpointMessages  = "/v1/messages"

	EndpointBalance = "/v1/dashboard/balance"
	EndpointKeyInfo = "/v1/key"
)

// EndpointForFormat maps a CPA executor format to the Wokey endpoint.
func EndpointForFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return EndpointChat, true
	case "openai-response", "openai-responses", "responses":
		return EndpointResponses, true
	case "claude", "anthropic":
		return EndpointMessages, true
	default:
		return "", false
	}
}
