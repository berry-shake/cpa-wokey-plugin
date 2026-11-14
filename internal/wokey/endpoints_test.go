package wokey

import (
	"testing"

	"github.com/berry-shake/cpa-provider-core/provider"
)

func TestEndpoint(t *testing.T) {
	a := New()
	for protocol, want := range map[provider.Protocol]string{
		provider.Chat:      EndpointChat,
		provider.Responses: EndpointResponses,
		provider.Messages:  EndpointMessages,
	} {
		if got := a.Endpoint(protocol); got != want {
			t.Errorf("Endpoint(%s) = %q, want %q", protocol, got, want)
		}
	}
}
