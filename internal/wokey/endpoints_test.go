package wokey

import "testing"

func TestEndpointForFormat(t *testing.T) {
	cases := map[string]string{
		"openai":           EndpointChat,
		"chat-completions": EndpointChat,
		"openai-response":  EndpointResponses,
		"responses":        EndpointResponses,
		"claude":           EndpointMessages,
		"Anthropic":        EndpointMessages,
	}
	for format, want := range cases {
		if got, ok := EndpointForFormat(format); !ok || got != want {
			t.Errorf("EndpointForFormat(%q) = %q, %v", format, got, ok)
		}
	}
	if _, ok := EndpointForFormat("gemini"); ok {
		t.Error("gemini accepted")
	}
}
