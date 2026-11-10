package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/berry-shake/cpa-wokey-plugin/internal/streamgate"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// benchRequestBody builds a Claude Code style Messages request of about size
// bytes: model first, then a long conversation, a system prompt and tools.
func benchRequestBody(size int) []byte {
	type obj = map[string]any
	text := strings.Repeat("The quick brown fox jumps over the \"lazy\" dog;\n中文内容。 ", 20)
	var tools []any
	for i := range 30 {
		tools = append(tools, obj{"name": fmt.Sprintf("Tool%d", i), "description": strings.Repeat("Describe the tool. ", 30),
			"input_schema": obj{"type": "object", "properties": obj{"path": obj{"type": "string"}}, "required": []any{"path"}}})
	}
	var messages []any
	for total := 0; total < size; {
		var message obj
		if len(messages)%2 == 0 {
			message = obj{"role": "user", "content": []any{obj{"type": "tool_result", "tool_use_id": fmt.Sprintf("toolu_%d", len(messages)), "content": text + text}}}
		} else {
			message = obj{"role": "assistant", "content": []any{obj{"type": "text", "text": text}, obj{"type": "tool_use", "id": fmt.Sprintf("toolu_%d", len(messages)), "name": "Read", "input": obj{"path": "/a/b.go", "limit": 2000}}}}
		}
		raw, _ := json.Marshal(message)
		total += len(raw)
		messages = append(messages, message)
	}
	rest, _ := json.Marshal(obj{"messages": messages, "system": []any{obj{"type": "text", "text": text}}, "tools": tools, "metadata": obj{"user_id": "u"}})
	head := `{"model":"wokey/model-a","max_tokens":32000,"stream":true,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},`
	return append([]byte(head), rest[1:]...)
}

// BenchmarkPrepareCall measures the work done on a large request before it
// is sent upstream.
func BenchmarkPrepareCall(b *testing.B) {
	svc := New("test", nil)
	b.Cleanup(svc.Close)
	body := benchRequestBody(800 << 10)
	req := pluginapi.ExecutorRequest{
		AuthID: "wokey-test.json", Model: "model-a", Format: "claude", Stream: true,
		Payload: body, OriginalRequest: body,
		Headers:     http.Header{"User-Agent": {"claude-cli/2.1"}},
		Metadata:    map[string]any{"requested_model": "wokey/model-a"},
		StorageJSON: []byte(`{"type":"wokey","api_key":"wk-bench-0123456789"}`),
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		call, perr := svc.prepareCall(req, wokey.EndpointMessages)
		if perr != nil {
			b.Fatal(perr.Message)
		}
		if rec := svc.startRecord(req, call, true); rec.entry.ReasoningEffort != "high" {
			b.Fatalf("effort = %q", rec.entry.ReasoningEffort)
		}
	}
}

// BenchmarkStreamMessages pumps a Messages stream with 200 text deltas
// through the SSE reader, the gate and the request log observer.
func BenchmarkStreamMessages(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":160000,\"cache_creation_input_tokens\":2,\"output_tokens\":1}}}\n\n")
	sb.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	sb.WriteString("event: ping\ndata: {\"type\": \"ping\"}\n\n")
	for range 200 {
		sb.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world, this is a token\"}}\n\n")
	}
	sb.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	sb.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":200}}\n\n")
	sb.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	stream := sb.String()
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for b.Loop() {
		rec := &callRecord{}
		gate := streamgate.New(wokey.EndpointMessages, true, http.Header{}, func([]byte) error { return nil })
		gate.Tap = rec.observe
		errPump := streamgate.Pump(strings.NewReader(stream), true, gate.Push, func() {})
		if errPump != nil || rec.frames != 201 || rec.usage.output != 200 || rec.entry.FinishReason != "end_turn" {
			b.Fatalf("pump = %v, frames %d, usage %+v, finish %q", errPump, rec.frames, rec.usage, rec.entry.FinishReason)
		}
	}
}
