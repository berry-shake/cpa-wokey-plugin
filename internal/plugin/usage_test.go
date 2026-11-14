package plugin

import (
	"encoding/json"
	"testing"
)

// Thinking tokens are read from every field CPA reads them from, with
// Anthropic's output_tokens_details.thinking_tokens preferred.
func TestUsageReasoningTokens(t *testing.T) {
	cases := []struct {
		name, usage string
		want        int64
	}{
		{"anthropic output_tokens_details.thinking_tokens", `{"input_tokens":10,"output_tokens":50,"output_tokens_details":{"thinking_tokens":30}}`, 30},
		{"responses output_tokens_details.reasoning_tokens", `{"input_tokens":10,"output_tokens":50,"output_tokens_details":{"reasoning_tokens":20}}`, 20},
		{"chat completion_tokens_details.reasoning_tokens", `{"prompt_tokens":10,"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":25}}`, 25},
		{"top-level thinking_tokens", `{"input_tokens":10,"output_tokens":50,"thinking_tokens":15}`, 15},
		{"anthropic field preferred", `{"output_tokens":50,"thinking_tokens":5,"output_tokens_details":{"reasoning_tokens":20,"thinking_tokens":30}}`, 30},
		{"zero does not hide another field", `{"output_tokens":50,"thinking_tokens":15,"output_tokens_details":{"thinking_tokens":0}}`, 15},
		{"no thinking", `{"input_tokens":10,"output_tokens":50}`, 0},
	}
	for _, c := range cases {
		var raw any
		if errUnmarshal := json.Unmarshal([]byte(c.usage), &raw); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		var u usageTotals
		u.merge(raw)
		if u.reasoning != c.want || u.output != 50 {
			t.Errorf("%s: reasoning %d output %d, want reasoning %d output 50", c.name, u.reasoning, u.output, c.want)
		}
	}
}
