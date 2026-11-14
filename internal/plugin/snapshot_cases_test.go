package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// snapshotCases seeds the inputs of RPC snapshot fixtures that do not exist
// yet (see TestSnapshots). Once a fixture is written it is the source of
// truth: editing a case here does not change an existing fixture.
//
// Requests are built from the CPA v8 wire types, so the fixture holds exactly
// what CPA sends. Keys and tokens are fake.

const (
	snapKey1        = "sk-wk-snapshot-fake-0001"
	snapKey2        = "sk-wk-snapshot-fake-0002"
	snapIssuerToken = "wkt_snapshot_fake_0001"
	snapCallbackID  = "cb-snapshot-1"
	snapVersion     = "9.9.9-snapshot"
	snapMgmt        = "/v0/management/plugins/wokey"
	snapResource    = "/v0/resource/plugins/wokey"
)

// The wire types CPA v8 (internal/pluginhost) sends.
type (
	snapExecutorRequest struct {
		pluginapi.ExecutorRequest
		StreamID       string `json:"stream_id,omitempty"`
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapManagementRequest struct {
		pluginapi.ManagementRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapAuthModelRequest struct {
		pluginapi.AuthModelRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapAuthRefreshRequest struct {
		pluginapi.AuthRefreshRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapQuotaFetchRequest struct {
		pluginapi.QuotaFetchRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapInterceptRequest struct {
		pluginapi.RequestInterceptRequest
		HostCallbackID string `json:"host_callback_id,omitempty"`
	}
	snapEmpty struct{}
)

type snapshotCaseDef struct {
	name        string
	description string
	// config is the plugin config subtree; nil means snapConfig().
	config      map[string]any
	credentials []snapCred
	script      []scriptedReply
	steps       []snapStepDef
}

type snapCred struct {
	name   string
	fields map[string]any
}

type snapStepDef struct {
	method  string
	request any
}

// snapRawRequest is a step request that is already in fixture form.
type snapRawRequest json.RawMessage

func snapConfig() map[string]any {
	return map[string]any{"base-url": "{{UPSTREAM}}", "data-dir": "{{DATA_DIR}}"}
}

func (def snapshotCaseDef) fixture(t *testing.T) snapshotFixture {
	t.Helper()
	config := def.config
	if config == nil {
		config = snapConfig()
	}
	fixture := snapshotFixture{
		Case:           def.name,
		Description:    def.description,
		Setup:          snapshotSetup{Version: snapVersion, Config: snapMarshal(t, config), Credentials: []snapshotCredential{}},
		UpstreamScript: []scriptedReply{},
		Steps:          []snapshotStep{},
	}
	for _, cred := range def.credentials {
		fixture.Setup.Credentials = append(fixture.Setup.Credentials, snapshotCredential{Name: cred.name, Fields: snapMarshal(t, cred.fields)})
	}
	for _, reply := range def.script {
		if reply.Header == nil {
			reply.Header = http.Header{}
		}
		if reply.Chunks == nil {
			reply.Chunks = []string{}
		}
		fixture.UpstreamScript = append(fixture.UpstreamScript, reply)
	}
	for _, step := range def.steps {
		if raw, ok := step.request.(snapRawRequest); ok {
			fixture.Steps = append(fixture.Steps, snapshotStep{RPC: snapshotRPC{Method: step.method, Request: json.RawMessage(raw)}})
			continue
		}
		wire := snapMarshal(t, step.request)
		node, errParse := parseJSON(wire)
		if errParse != nil {
			t.Fatalf("%s: %v", def.name, errParse)
		}
		decodeBytesFields(node)
		form := node.bytes()
		back, _ := parseJSON(form)
		encodeBytesFields(back)
		if !bytes.Equal(back.bytes(), wire) {
			t.Fatalf("%s: %s request does not survive the fixture form:\n%s\n%s", def.name, step.method, wire, back.bytes())
		}
		fixture.Steps = append(fixture.Steps, snapshotStep{RPC: snapshotRPC{Method: step.method, Request: form}})
	}
	return fixture
}

func snapMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if errEncode := enc.Encode(v); errEncode != nil {
		t.Fatalf("marshal: %v", errEncode)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// ---- builders --------------------------------------------------------------

const (
	snapCatalogV1 = `{"object":"list","data":[{"id":"model-a","object":"model","created":1767225600,"owned_by":"lab-a","name":"Model A","context_length":200000,"max_completion_tokens":64000,"supported_parameters":["reasoning","tools"],"architecture":{"modality":"text+image->text","input_modalities":["text","image"],"output_modalities":["text"]},"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}},{"id":"model-b","object":"model","owned_by":"lab-b","name":"Model B","context_length":128000,"pricing":{"prompt":"0.000001","completion":"0.000002"}}]}`
	snapCatalogV2 = `{"object":"list","data":[{"id":"model-a","object":"model","created":1767225600,"owned_by":"lab-a","name":"Model A","context_length":200000,"max_completion_tokens":64000,"supported_parameters":["reasoning","tools"],"architecture":{"modality":"text+image->text","input_modalities":["text","image"],"output_modalities":["text"]},"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}},{"id":"model-b","object":"model","owned_by":"lab-b","name":"Model B","context_length":128000,"pricing":{"prompt":"0.000001","completion":"0.000002"}},{"id":"model-c","object":"model","owned_by":"lab-c","name":"Model C","context_length":32000}]}`

	snapBalanceBody = `{"userId":"user-1234","availableUsd":42.5,"reservedUsd":"0.75"}`
	snapKeyBody     = `{"label":"primary key","usage":1.5,"usage_daily":0.25,"usage_weekly":1,"usage_monthly":1.5,"limit":10,"limit_remaining":8.5,"limit_reset":"monthly"}`
	snapIssuerBody  = `{"provider":{"availableUsd":"9.56639200","settlementFrozen":false},"referrer":{"availableUsd":"0.10000000"},"api":{"enabled":true,"eligible":true,"availableUsd":"0.33","disqualifiedBy":null}}`
	snapInvalidKey  = `{"error":{"code":"invalid_api_key","message":"invalid API key"}}`
	snapBadIssuer   = `{"error":{"code":"invalid_issuer_token","message":"invalid_issuer_token","type":"invalid_request_error"}}`

	snapChatBody      = `{"id":"chatcmpl-snap-1","object":"chat.completion","created":1767225600,"model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`
	snapResponsesBody = `{"id":"resp_snap_1","object":"response","status":"completed","model":"model-a","output":[{"type":"message","id":"msg_snap_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!","annotations":[]}]}],"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":8},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":26}}`
	snapMessagesBody  = `{"id":"msg_snap_1","type":"message","role":"assistant","model":"model-a","content":[{"type":"text","text":"Hello!"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":5,"output_tokens":7}}`

	snapChatRequest      = `{"model":"model-a","messages":[{"role":"user","content":"Say hello"}],"max_tokens":64}`
	snapResponsesRequest = `{"model":"model-a","input":"Say hello","reasoning":{"effort":"high"}}`
	snapMessagesRequest  = `{"model":"model-a","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"Say hello"}]}`
)

var snapK1 = snapCred{name: "k1.json", fields: map[string]any{"type": "wokey", "api_key": snapKey1, "label": "primary"}}

// snapK1Storage is k1.json as CPA stores it (the fake host marshals the
// seeded fields with sorted keys).
const snapK1Storage = `{"api_key":"` + snapKey1 + `","label":"primary","type":"wokey"}`

func snapK1Metadata() map[string]any {
	return map[string]any{"type": "wokey", "api_key": snapKey1, "label": "primary"}
}

func snapHeader(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Add(pairs[i], pairs[i+1])
	}
	return header
}

// snapQuery builds a management query.
func snapQuery(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		values.Add(pairs[i], pairs[i+1])
	}
	return values
}

func snapReply(method, path string, status int, body string, header ...string) scriptedReply {
	return scriptedReply{Method: method, Path: path, Status: status, Header: snapHeader(header...), Body: body}
}

func snapJSONReply(method, path string, status int, body string, header ...string) scriptedReply {
	return snapReply(method, path, status, body, append([]string{"Content-Type", "application/json"}, header...)...)
}

func snapStreamReply(path string, chunks []string, header ...string) scriptedReply {
	return scriptedReply{Method: http.MethodPost, Path: path, Status: http.StatusOK, Header: snapHeader(append([]string{"Content-Type", "text/event-stream", "Cache-Control", "no-cache"}, header...)...), Chunks: chunks}
}

func snapCatalogReply(body, etag string) scriptedReply {
	return snapJSONReply(http.MethodGet, "/v1/models", http.StatusOK, body, "Etag", etag)
}

// snapSSE is one SSE event; event may be empty.
func snapSSE(event, data string) string {
	if event == "" {
		return "data: " + data + "\n\n"
	}
	return "event: " + event + "\ndata: " + data + "\n\n"
}

type snapExecOpts struct {
	// format is the CPA translator format: openai, openai-response or claude.
	format   string
	model    string
	client   string
	payload  string
	stream   bool
	streamID string
	headers  http.Header
	auth     string
	index    string
	storage  string
	metadata map[string]any
}

var snapClientPaths = map[string]string{"openai": "/v1/chat/completions", "openai-response": "/v1/responses", "claude": "/v1/messages"}

func snapExec(o snapExecOpts) snapExecutorRequest {
	if o.model == "" {
		o.model = "model-a"
	}
	if o.client == "" {
		o.client = "wokey/" + o.model
	}
	if o.auth == "" {
		o.auth, o.index, o.storage, o.metadata = "k1.json", "idx-1", snapK1Storage, snapK1Metadata()
	}
	headers := snapHeader("User-Agent", "snapshot-client/1.0", "Content-Type", "application/json")
	for key, values := range o.headers {
		headers[key] = values
	}
	original := strings.Replace(o.payload, `"model":"`+o.model+`"`, `"model":"`+o.client+`"`, 1)
	return snapExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			AuthID:          o.auth,
			AuthProvider:    "wokey",
			Model:           o.model,
			Format:          o.format,
			Stream:          o.stream,
			Headers:         headers,
			OriginalRequest: []byte(original),
			SourceFormat:    o.format,
			Payload:         []byte(o.payload),
			Metadata: map[string]any{
				"request_path":        snapClientPaths[o.format],
				"requested_model":     o.client,
				"selected_auth_id":    o.auth,
				"selected_auth_index": o.index,
			},
			StorageJSON:    []byte(o.storage),
			AuthMetadata:   o.metadata,
			AuthAttributes: map[string]string{"path": "/auths/" + o.auth, "source": "file"},
		},
		StreamID:       o.streamID,
		HostCallbackID: snapCallbackID,
	}
}

func snapExecStep(o snapExecOpts) snapStepDef {
	method := pluginabi.MethodExecutorExecute
	if o.stream {
		method = pluginabi.MethodExecutorExecuteStream
	}
	return snapStepDef{method: method, request: snapExec(o)}
}

func snapManage(method, path string, query url.Values, body string) snapStepDef {
	var raw []byte
	if body != "" {
		raw = []byte(body)
	}
	return snapStepDef{method: pluginabi.MethodManagementHandle, request: snapManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{Method: method, Path: path, Headers: snapHeader("Accept", "application/json"), Query: query, Body: raw},
		HostCallbackID:    snapCallbackID,
	}}
}

func snapModelForAuth(auth, storage string, metadata map[string]any, host pluginapi.HostConfigSummary) snapStepDef {
	return snapStepDef{method: pluginabi.MethodModelForAuth, request: snapAuthModelRequest{
		AuthModelRequest: pluginapi.AuthModelRequest{
			Plugin:       pluginapi.Metadata{Name: "Wokey", Version: snapVersion},
			AuthID:       auth,
			AuthProvider: "wokey",
			StorageJSON:  []byte(storage),
			Metadata:     metadata,
			Attributes:   map[string]string{"path": "/auths/" + auth, "source": "file"},
			Host:         host,
		},
		HostCallbackID: snapCallbackID,
	}}
}

func snapQuotaFetch(index, auth, storage string, metadata map[string]any) snapStepDef {
	var raw []byte
	if storage != "" {
		raw = []byte(storage)
	}
	return snapStepDef{method: pluginabi.MethodQuotaFetch, request: snapQuotaFetchRequest{
		QuotaFetchRequest: pluginapi.QuotaFetchRequest{AuthIndex: index, AuthID: auth, Provider: "wokey", StorageJSON: raw, Metadata: metadata},
		HostCallbackID:    snapCallbackID,
	}}
}

func snapIntercept(method, trace string, metadata map[string]any) snapStepDef {
	return snapStepDef{method: method, request: snapInterceptRequest{
		RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			RequestID:      "req-snap-1",
			TraceID:        trace,
			SourceFormat:   "openai",
			ToFormat:       "openai",
			Model:          "model-a",
			RequestedModel: "wokey/model-a",
			Headers:        snapHeader("User-Agent", "snapshot-client/1.0", "X-Wokey-Cpa-Request-Id", "spoofed-by-client"),
			Metadata:       metadata,
		},
		HostCallbackID: snapCallbackID,
	}}
}

func snapEmptyStep(method string) snapStepDef {
	return snapStepDef{method: method, request: snapEmpty{}}
}

// ---- streams ---------------------------------------------------------------

func snapChatChunk(delta, finish string, usage string) string {
	choice := `{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}`
	body := `{"id":"chatcmpl-snap-2","object":"chat.completion.chunk","created":1767225600,"model":"model-a","choices":[` + choice + `]}`
	if usage != "" {
		body = `{"id":"chatcmpl-snap-2","object":"chat.completion.chunk","created":1767225600,"model":"model-a","choices":[],"usage":` + usage + `}`
	}
	return snapSSE("", body)
}

func snapChatStream() []string {
	return []string{
		snapChatChunk(`{"role":"assistant","content":""}`, "null", ""),
		snapChatChunk(`{"content":"Hel"}`, "null", ""),
		": keepalive\n\n",
		snapChatChunk(`{"content":"lo!"}`, "null", ""),
		snapChatChunk(`{}`, `"stop"`, ""),
		snapChatChunk("", "", `{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"completion_tokens_details":{"reasoning_tokens":2}}`),
		snapSSE("", "[DONE]"),
	}
}

func snapResponsesStream(complete bool) []string {
	chunks := []string{
		snapSSE("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_snap_2","object":"response","status":"in_progress","model":"model-a","output":[]}}`),
		snapSSE("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_snap_2","status":"in_progress","role":"assistant","content":[]}}`),
		snapSSE("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_snap_2","output_index":0,"content_index":0,"delta":"Hel"}`),
		snapSSE("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_snap_2","output_index":0,"content_index":0,"delta":"lo!"}`),
	}
	if complete {
		chunks = append(chunks, snapSSE("response.completed", `{"type":"response.completed","sequence_number":4,"response":{"id":"resp_snap_2","object":"response","status":"completed","model":"model-a","output":[{"type":"message","id":"msg_snap_2","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello!","annotations":[]}]}],"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":8},"output_tokens":6,"output_tokens_details":{"reasoning_tokens":3},"total_tokens":26}}}`))
	}
	return chunks
}

func snapMessagesStart() []string {
	return []string{
		snapSSE("message_start", `{"type":"message_start","message":{"id":"msg_snap_2","type":"message","role":"assistant","model":"model-a","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"output_tokens":1}}}`),
		snapSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		snapSSE("ping", `{"type":"ping"}`),
		snapSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`),
	}
}

func snapMessagesStream() []string {
	return append(snapMessagesStart(),
		snapSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo!"}}`),
		snapSSE("content_block_stop", `{"type":"content_block_stop","index":0}`),
		snapSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`),
		snapSSE("message_stop", `{"type":"message_stop"}`),
	)
}

// ---- cases -----------------------------------------------------------------

func snapshotCases() []snapshotCaseDef {
	var cases []snapshotCaseDef
	cases = append(cases, snapExecCases()...)
	cases = append(cases, snapErrorCases()...)
	cases = append(cases, snapCatalogCases()...)
	cases = append(cases, snapCredentialCases()...)
	cases = append(cases, snapQuotaCases()...)
	cases = append(cases, snapManagementCases()...)
	return cases
}

func snapExecCases() []snapshotCaseDef {
	clientHeaders := snapHeader("X-End-User-Id", "end-user-42", "Anthropic-Beta", "interleaved-thinking-2025-05-14", "Anthropic-Version", "2023-06-01", "Authorization", "Bearer client-key-not-forwarded")
	return []snapshotCaseDef{
		{
			name:        "exec_chat_nonstream",
			description: "Chat completions, buffered: model rewrite, forwarded and stripped headers, filtered response headers, usage.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody, "X-Request-Id", "wk-req-chat-1", "X-Internal-Trace", "not-forwarded"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai", payload: snapChatRequest, headers: clientHeaders}),
			},
		},
		{
			name:        "exec_chat_stream",
			description: "Chat completions stream over the host stream bridge: include_usage added, keepalive dropped, [DONE] kept.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/chat/completions", snapChatStream(), "X-Request-Id", "wk-req-chat-2"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai", payload: `{"model":"model-a","messages":[{"role":"user","content":"Say hello"}],"stream":true}`, stream: true, streamID: "stream-1", headers: clientHeaders}),
			},
		},
		{
			name:        "exec_chat_stream_no_bridge",
			description: "Chat completions stream without a stream_id: the whole stream comes back as chunks in the RPC response.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/chat/completions", snapChatStream()),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai", payload: `{"model":"model-a","messages":[{"role":"user","content":"Say hello"}],"stream":true,"stream_options":{"include_usage":true}}`, stream: true}),
			},
		},
		{
			name:        "exec_responses_nonstream",
			description: "Responses API, buffered, with reasoning effort and usage.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/responses", http.StatusOK, snapResponsesBody, "Request-Id", "wk-req-resp-1"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai-response", payload: snapResponsesRequest}),
			},
		},
		{
			name:        "exec_responses_stream",
			description: "Responses API stream: prelude held until output, terminal response.completed with usage.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/responses", snapResponsesStream(true)),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai-response", payload: `{"model":"model-a","input":"Say hello","stream":true}`, stream: true, streamID: "stream-1"}),
			},
		},
		{
			name:        "exec_messages_nonstream",
			description: "Anthropic Messages, buffered: Anthropic-Beta/Version and X-End-User-Id forwarded, thinking budget, cache usage.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/messages", http.StatusOK, snapMessagesBody, "Request-Id", "wk-req-msg-1"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "claude", payload: snapMessagesRequest, headers: clientHeaders}),
			},
		},
		{
			name:        "exec_messages_stream",
			description: "Anthropic Messages stream without client Anthropic headers: default Anthropic-Version, ping held in the prelude.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/messages", snapMessagesStream(), "Request-Id", "wk-req-msg-2"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "claude", payload: `{"model":"model-a","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Say hello"}]}`, stream: true, streamID: "stream-1"}),
			},
		},
		{
			name:        "exec_request_interceptor",
			description: "Request interceptor: only auths seen as Wokey credentials get CPA's request ID; the header never reaches Wokey.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody),
			},
			steps: []snapStepDef{
				snapIntercept(pluginabi.MethodRequestInterceptBefore, "trace-snap-1", nil),
				snapIntercept(pluginabi.MethodRequestInterceptAfter, "trace-snap-1", map[string]any{"selected_auth_id": "k1.json"}),
				snapExecStep(snapExecOpts{format: "openai", payload: snapChatRequest, headers: snapHeader("X-Wokey-Cpa-Request-Id", "trace-snap-1")}),
				snapIntercept(pluginabi.MethodRequestInterceptAfter, "trace-snap-1", map[string]any{"selected_auth_id": "k1.json"}),
				snapIntercept(pluginabi.MethodRequestInterceptAfter, "", map[string]any{"selected_auth_id": "k1.json"}),
				snapIntercept(pluginabi.MethodRequestInterceptAfter, "trace-snap-1", map[string]any{"selected_auth_id": "other.json"}),
			},
		},
	}
}

func snapErrorCases() []snapshotCaseDef {
	chat := snapExecOpts{format: "openai", payload: snapChatRequest}
	chatStream := snapExecOpts{format: "openai", payload: `{"model":"model-a","messages":[{"role":"user","content":"Say hello"}],"stream":true}`, stream: true, streamID: "stream-1"}
	messages := snapExecOpts{format: "claude", payload: snapMessagesRequest}
	return []snapshotCaseDef{
		{
			name:        "error_400",
			description: "Upstream 400 with an OpenAI-style error body keeps its status and code.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusBadRequest, `{"error":{"message":"max_tokens is too large","type":"invalid_request_error","code":"invalid_value"}}`, "X-Request-Id", "wk-req-400"),
			},
			steps: []snapStepDef{snapExecStep(chat)},
		},
		{
			name:        "error_401",
			description: "Upstream 401 keeps its status so CPA cools the key down.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusUnauthorized, snapInvalidKey),
			},
			steps: []snapStepDef{snapExecStep(chat)},
		},
		{
			name:        "error_403_request_blocked",
			description: "403 request_blocked (Anthropic error shape) and model_denylisted become 400; an account-level 403 stays 403.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/messages", http.StatusForbidden, `{"type":"error","error":{"type":"request_blocked","message":"request blocked by policy"}}`),
				snapJSONReply(http.MethodPost, "/v1/messages", http.StatusForbidden, `{"error":{"code":"model_denylisted","message":"model-a is not allowed for this key"}}`),
				snapJSONReply(http.MethodPost, "/v1/messages", http.StatusForbidden, `{"error":{"code":"account_suspended","message":"account suspended"}}`),
			},
			steps: []snapStepDef{snapExecStep(messages), snapExecStep(messages), snapExecStep(messages)},
		},
		{
			name:        "error_429_retry_after",
			description: "429 with Retry-After, buffered and as a synchronous stream error.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, "Retry-After", "17", "X-Request-Id", "wk-req-429"),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, "Retry-After", "3"),
			},
			steps: []snapStepDef{snapExecStep(chat), snapExecStep(chatStream)},
		},
		{
			name:        "error_500",
			description: "Upstream 500 is retryable; 404 becomes 400 and 529 becomes 503.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusInternalServerError, `{"error":{"message":"internal error","type":"server_error"}}`),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusNotFound, `{"error":{"code":"model_not_found","message":"no such model"}}`),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
			},
			steps: []snapStepDef{snapExecStep(chat), snapExecStep(chat), snapExecStep(chat)},
		},
		{
			name:        "error_plain_text",
			description: "A plain-text error body becomes the message.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapReply(http.MethodPost, "/v1/chat/completions", http.StatusBadGateway, "upstream exploded\n", "Content-Type", "text/plain; charset=utf-8"),
			},
			steps: []snapStepDef{snapExecStep(chat)},
		},
		{
			name:        "error_string_error_body",
			description: `A {"error":"msg"} body: the string is the message.`,
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusBadRequest, `{"error":"messages must not be empty"}`),
			},
			steps: []snapStepDef{snapExecStep(chat)},
		},
		{
			name:        "error_stream_mid_way",
			description: "An error event after output started is passed to the client as is and the stream closes normally.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/messages", append(snapMessagesStart(), snapSSE("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "claude", payload: `{"model":"model-a","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Say hello"}]}`, stream: true, streamID: "stream-1"}),
				snapManage(http.MethodGet, snapMgmt+"/logs", nil, ""),
			},
		},
		{
			name:        "error_stream_before_output",
			description: "An error inside the stream before any output closes the stream with a mapped error (429 + Retry-After).",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/chat/completions", []string{
					snapChatChunk(`{"role":"assistant","content":""}`, "null", ""),
					snapSSE("", `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`),
				}, "Retry-After", "5"),
			},
			steps: []snapStepDef{snapExecStep(chatStream), snapManage(http.MethodGet, snapMgmt+"/logs", nil, "")},
		},
		{
			name:        "error_stream_truncated",
			description: "A Responses stream that ends without response.completed closes with stream_truncated.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapStreamReply("/v1/responses", snapResponsesStream(false)),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai-response", payload: `{"model":"model-a","input":"Say hello","stream":true}`, stream: true, streamID: "stream-1"}),
				snapManage(http.MethodGet, snapMgmt+"/logs", nil, ""),
			},
		},
		{
			name:        "error_rejected_locally",
			description: "Requests rejected before calling Wokey: no api_key, unsupported format, payload not an object, no model.",
			credentials: []snapCred{snapK1},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai", payload: snapChatRequest, auth: "broken.json", index: "idx-9", storage: `{"type":"wokey","label":"no key"}`}),
				snapExecStep(snapExecOpts{format: "gemini", payload: snapChatRequest}),
				snapExecStep(snapExecOpts{format: "openai", payload: `[{"model":"model-a"}]`}),
				snapExecStep(snapExecOpts{format: "openai", model: " ", client: " ", payload: `{"messages":[]}`}),
			},
		},
		{
			name:        "count_tokens",
			description: "count_tokens and raw HTTP requests return 501 without calling Wokey.",
			credentials: []snapCred{snapK1},
			steps: []snapStepDef{
				{method: pluginabi.MethodExecutorCountTokens, request: snapExec(snapExecOpts{format: "claude", payload: `{"model":"model-a","messages":[{"role":"user","content":"Say hello"}]}`})},
				{method: pluginabi.MethodExecutorHTTPRequest, request: snapEmpty{}},
			},
		},
	}
}

func snapCatalogCases() []snapshotCaseDef {
	selected := map[string]any{"type": "wokey", "api_key": snapKey1, "label": "picked", "prefix": "wk", "models": []string{"model-b", "missing-model"}}
	return []snapshotCaseDef{
		{
			name:        "catalog_model_rpcs",
			description: "model.static, then model.for_auth loading the catalog once, for all models and for a selection with a missing model.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
			},
			steps: []snapStepDef{
				{method: pluginabi.MethodModelStatic, request: pluginapi.StaticModelRequest{Plugin: pluginapi.Metadata{Name: "Wokey", Version: snapVersion}, MultiProviderModels: true}},
				snapModelForAuth("k1.json", snapK1Storage, snapK1Metadata(), pluginapi.HostConfigSummary{AuthDir: "/auths"}),
				snapModelForAuth("picked.json", `{"api_key":"`+snapKey1+`","label":"picked","models":["model-b","missing-model"],"prefix":"wk","type":"wokey"}`, selected, pluginapi.HostConfigSummary{AuthDir: "/auths", ForceModelPrefix: true}),
			},
		},
		{
			name:        "catalog_refresh_etag",
			description: "Catalog load with ETag, background tick answered 304, forced refresh with a changed catalog re-registering the auth file, then 304 again.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
				snapReply(http.MethodGet, "/v1/models", http.StatusNotModified, "", "Etag", `"cat-v1"`),
				snapCatalogReply(snapCatalogV2, `"cat-v2"`),
				snapReply(http.MethodGet, "/v1/models", http.StatusNotModified, "", "Etag", `"cat-v2"`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/models", nil, ""),
				{method: seamRefreshTickMethod, request: snapEmpty{}},
				snapManage(http.MethodGet, snapMgmt+"/models", nil, ""),
				snapManage(http.MethodPost, snapMgmt+"/models/refresh", nil, ""),
				{method: seamRefreshTickMethod, request: snapEmpty{}},
			},
		},
		{
			name:        "catalog_unknown_prefix",
			description: "UpstreamID: the auth prefix and unknown prefixes are dropped, catalog IDs pass, a thinking suffix is stripped.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody),
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody),
			},
			steps: []snapStepDef{
				snapModelForAuth("k1.json", snapK1Storage, snapK1Metadata(), pluginapi.HostConfigSummary{AuthDir: "/auths"}),
				snapExecStep(snapExecOpts{format: "openai", model: "legacy/model-a", client: "legacy/model-a", payload: `{"model":"legacy/model-a","messages":[]}`}),
				snapExecStep(snapExecOpts{format: "openai", model: "wokey/model-b", client: "wokey/model-b", payload: `{"model":"wokey/model-b","messages":[]}`}),
				snapExecStep(snapExecOpts{format: "openai", model: "vendor/model-z", client: "vendor/model-z", payload: `{"model":"vendor/model-z","messages":[]}`}),
				snapExecStep(snapExecOpts{format: "openai", model: "model-a(high)", client: "wokey/model-a(high)", payload: `{"messages":[]}`}),
			},
		},
		{
			name:        "catalog_refresh_failure",
			description: "The models view reports a catalog that cannot be loaded; status shows the last error.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/models", http.StatusInternalServerError, `{"error":{"message":"catalog down"}}`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/models", nil, ""),
				snapManage(http.MethodGet, snapMgmt+"/status", nil, ""),
			},
		},
	}
}

func snapCredentialCases() []snapshotCaseDef {
	return []snapshotCaseDef{
		{
			name:        "credential_add",
			description: "Add a key: GET /v1/key check, catalog load, auth file save, credential view with unknown models.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`","label":"added","note":"from snapshot","models":["model-b","model-a","missing-model"],"priority":3}`),
			},
		},
		{
			name:        "credential_add_issuer_token",
			description: "Add a key with an issuing token: key check, issuer balance check, then save.",
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusOK, snapIssuerBody),
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`","issuer_token":"`+snapIssuerToken+`","prefix":"/wk/"}`),
			},
		},
		{
			name:        "credential_add_rejected",
			description: "Rejected adds: key 401, issuer token 401, malformed issuer token, duplicate key, bad JSON, missing key, unknown proxy_id.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusUnauthorized, snapInvalidKey),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusUnauthorized, snapBadIssuer),
			},
			steps: []snapStepDef{
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`"}`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`","issuer_token":"`+snapIssuerToken+`"}`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`","issuer_token":"not-an-issuer-token"}`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey1+`"}`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"label":"no key"}`),
				snapManage(http.MethodPost, snapMgmt+"/credentials", nil, `{"api_key":"`+snapKey2+`","proxy_id":"nope"}`),
			},
		},
		{
			name:        "credential_update",
			description: "Edit a credential (issuer token verified), remove its issuer token and selection, then rejected edits and the missing DELETE route.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusOK, snapIssuerBody),
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodPatch, snapMgmt+"/credentials", nil, `{"auth_index":"idx-1","label":"renamed","note":"edited","models":["model-a","model-x"],"prefix":"wk","priority":5,"proxy_url":"direct","issuer_token":"`+snapIssuerToken+`"}`),
				snapManage(http.MethodPatch, snapMgmt+"/credentials", nil, `{"name":"k1.json","issuer_token":"","models":null,"prefix":null,"priority":null,"proxy_url":""}`),
				snapManage(http.MethodPatch, snapMgmt+"/credentials", nil, `{"auth_index":"idx-9","label":"x"}`),
				snapManage(http.MethodPatch, snapMgmt+"/credentials", nil, `{"auth_index":"idx-1","api_key":"`+snapKey2+`"}`),
				snapManage(http.MethodPatch, snapMgmt+"/credentials", nil, `{"auth_index":"idx-1","proxy_url":"ftp://nope"}`),
				snapManage(http.MethodDelete, snapMgmt+"/credentials", snapQuery("auth_index", "idx-1"), ""),
			},
		},
		{
			name:        "credential_auth_rpcs",
			description: "auth.parse (Wokey file, other provider, no key), auth.refresh and the unsupported login flow.",
			steps: []snapStepDef{
				{method: pluginabi.MethodAuthParse, request: pluginapi.AuthParseRequest{Provider: "wokey", Path: "/auths/new.json", FileName: "new.json", RawJSON: []byte(`{"type":"wokey","api_key":"` + snapKey2 + `","prefix":"wk","proxy_url":"direct","disabled":true}`), Host: pluginapi.HostConfigSummary{AuthDir: "/auths"}}},
				{method: pluginabi.MethodAuthParse, request: pluginapi.AuthParseRequest{Provider: "wokey", Path: "/auths/codex.json", FileName: "codex.json", RawJSON: []byte(`{"type":"codex","access_token":"fake"}`)}},
				{method: pluginabi.MethodAuthParse, request: pluginapi.AuthParseRequest{Provider: "wokey", Path: "/auths/empty.json", FileName: "empty.json", RawJSON: []byte(`{"type":"wokey"}`)}},
				{method: pluginabi.MethodAuthRefresh, request: snapAuthRefreshRequest{AuthRefreshRequest: pluginapi.AuthRefreshRequest{AuthID: "k1.json", AuthProvider: "wokey", StorageJSON: []byte(snapK1Storage), Metadata: map[string]any{"label": "from metadata"}, Host: pluginapi.HostConfigSummary{AuthDir: "/auths"}}, HostCallbackID: snapCallbackID}},
				snapEmptyStep(pluginabi.MethodAuthLoginStart),
				snapEmptyStep(pluginabi.MethodAuthLoginPoll),
			},
		},
		{
			name:        "credential_status_view",
			description: "Credential views in the status route: plain, fully configured, disabled, other providers ignored.",
			credentials: []snapCred{
				snapK1,
				{name: "k2.json", fields: map[string]any{"type": "wokey", "api_key": snapKey2, "label": "configured", "note": "has everything", "prefix": "wk", "models": []string{"model-b", "model-a"}, "proxy_url": "socks5://user:secret@127.0.0.1:1080", "issuer_token": snapIssuerToken, "priority": 7, "base_url": "https://eu.example.invalid/v1/"}},
				{name: "k3.json", fields: map[string]any{"type": "wokey", "api_key": "sk-wk-snapshot-fake-0003", "disabled": true}},
				{name: "codex.json", fields: map[string]any{"type": "codex", "access_token": "fake"}},
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/status", nil, ""),
			},
		},
	}
}

func snapQuotaCases() []snapshotCaseDef {
	withIssuer := snapCred{name: "k1.json", fields: map[string]any{"type": "wokey", "api_key": snapKey1, "label": "primary", "issuer_token": snapIssuerToken}}
	return []snapshotCaseDef{
		{
			name:        "quota_fetch",
			description: "quota.fetch: balance and key usage become one monthly bar; describe, identifier and reset.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusOK, snapBalanceBody),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
			},
			steps: []snapStepDef{
				snapEmptyStep(pluginabi.MethodQuotaIdentifier),
				{method: pluginabi.MethodQuotaDescribe, request: pluginapi.QuotaDescribeRequest{}},
				snapQuotaFetch("idx-1", "k1.json", snapK1Storage, snapK1Metadata()),
				snapEmptyStep(pluginabi.MethodQuotaReset),
			},
		},
		{
			name:        "quota_fetch_from_auth_index",
			description: "quota.fetch without storage reads the auth file by auth_index; an unknown index has no key.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusOK, `{"marketOrderId":"order-77","availableUsd":"3.25"}`),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, `{"usage":9,"usage_monthly":"6.75","limit":null}`),
			},
			steps: []snapStepDef{
				snapQuotaFetch("idx-1", "k1.json", "", nil),
				snapQuotaFetch("idx-9", "missing.json", "", nil),
			},
		},
		{
			name:        "quota_fetch_partial_failure",
			description: "quota.fetch when the balance fails, then when both calls fail (the key's status wins).",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusInternalServerError, `{"error":{"message":"balance service down"}}`),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusUnauthorized, snapInvalidKey),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusForbidden, `{"error":{"code":"key_disabled","message":"key disabled"}}`),
			},
			steps: []snapStepDef{
				snapQuotaFetch("idx-1", "k1.json", snapK1Storage, snapK1Metadata()),
				snapQuotaFetch("idx-1", "k1.json", snapK1Storage, snapK1Metadata()),
			},
		},
		{
			name:        "quota_management_view",
			description: "The management quota view: balance, key usage and the issuer balance via the issuing API.",
			credentials: []snapCred{withIssuer},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusOK, snapBalanceBody),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusOK, snapIssuerBody),
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/credentials/quota", snapQuery("auth_index", "idx-1"), ""),
			},
		},
		{
			name:        "quota_management_errors",
			description: "Management quota view: issuer failure next to good figures, unknown auth_index, both account calls rejected.",
			credentials: []snapCred{withIssuer},
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusOK, snapBalanceBody),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusOK, snapKeyBody),
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusUnauthorized, snapBadIssuer),
				snapJSONReply(http.MethodGet, "/v1/dashboard/balance", http.StatusUnauthorized, snapInvalidKey),
				snapJSONReply(http.MethodGet, "/v1/key", http.StatusUnauthorized, snapInvalidKey),
				snapJSONReply(http.MethodGet, "/api/issuer/v1/balance", http.StatusOK, snapIssuerBody),
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/credentials/quota", snapQuery("auth_index", "idx-1"), ""),
				snapManage(http.MethodGet, snapMgmt+"/credentials/quota", snapQuery("auth_index", "idx-9"), ""),
				snapManage(http.MethodGet, snapMgmt+"/credentials/quota", snapQuery("auth_index", "idx-1"), ""),
			},
		},
	}
}

func snapManagementCases() []snapshotCaseDef {
	return []snapshotCaseDef{
		{
			name:        "management_register",
			description: "Lifecycle and static RPCs: plugin.register, management.register, identifiers, resources and routing errors.",
			steps: []snapStepDef{
				{method: pluginabi.MethodPluginRegister, request: snapRawRequest(registerRequestForm(snapConfig()))},
				snapEmptyStep(pluginabi.MethodManagementRegister),
				snapEmptyStep(pluginabi.MethodAuthIdentifier),
				snapEmptyStep(pluginabi.MethodExecutorIdentifier),
				snapEmptyStep(pluginabi.MethodPluginQuiesce),
				snapManage(http.MethodGet, snapResource+"/logo.svg", nil, ""),
				snapManage(http.MethodGet, snapResource+"/flag.svg", snapQuery("cc", "jp"), ""),
				snapManage(http.MethodGet, snapResource+"/flag.svg", snapQuery("cc", "zz"), ""),
				snapManage(http.MethodGet, snapMgmt+"/nope", nil, ""),
				snapManage(http.MethodPut, snapMgmt+"/status", nil, ""),
				snapEmptyStep("plugin.unknown"),
			},
		},
		{
			name:        "management_status",
			description: "Status before CPA sent its host summary, then after model.for_auth (force prefix, redacted global proxy, catalog).",
			config:      map[string]any{"base-url": "{{UPSTREAM}}/v1/", "data-dir": "{{DATA_DIR}}", "model-prefix": "/wk/", "models-refresh-minutes": 15, "request-timeout-seconds": 120, "stream-idle-timeout-seconds": 45, "log-retention": 500, "log-retention-days": 7},
			credentials: []snapCred{{name: "k1.json", fields: map[string]any{"type": "wokey", "api_key": snapKey1, "proxy_url": "direct"}}},
			script: []scriptedReply{
				snapCatalogReply(snapCatalogV1, `"cat-v1"`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/status", nil, ""),
				snapModelForAuth("k1.json", `{"api_key":"`+snapKey1+`","proxy_url":"direct","type":"wokey"}`, map[string]any{"type": "wokey", "api_key": snapKey1, "proxy_url": "direct"}, pluginapi.HostConfigSummary{AuthDir: "/auths", ProxyURL: "http://proxyuser:proxypass@proxy.example.invalid:3128", ForceModelPrefix: true}),
				snapManage(http.MethodGet, snapMgmt+"/status", nil, ""),
			},
		},
		{
			name:        "management_logs",
			description: "Request log after buffered, streamed and failed calls (both speeds, summary), filters, then clearing it.",
			credentials: []snapCred{snapK1},
			script: []scriptedReply{
				snapJSONReply(http.MethodPost, "/v1/chat/completions", http.StatusOK, snapChatBody, "X-Request-Id", "wk-req-log-1"),
				snapStreamReply("/v1/messages", snapMessagesStream(), "Request-Id", "wk-req-log-2"),
				snapJSONReply(http.MethodPost, "/v1/responses", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`, "Retry-After", "30"),
			},
			steps: []snapStepDef{
				snapExecStep(snapExecOpts{format: "openai", payload: `{"model":"model-a","messages":[],"reasoning_effort":"low"}`, headers: snapHeader("X-Wokey-Cpa-Request-Id", "cpa-req-1")}),
				snapExecStep(snapExecOpts{format: "claude", payload: `{"model":"model-a","max_tokens":1024,"stream":true,"messages":[]}`, stream: true, streamID: "stream-1", headers: snapHeader("X-Wokey-Cpa-Request-Id", "cpa-req-2")}),
				snapExecStep(snapExecOpts{format: "openai-response", payload: `{"model":"model-b","input":"hi","stream":true}`, model: "model-b", stream: true, streamID: "stream-2", headers: snapHeader("X-Wokey-Cpa-Request-Id", "cpa-req-3")}),
				snapManage(http.MethodGet, snapMgmt+"/logs", snapQuery("limit", "10"), ""),
				snapManage(http.MethodGet, snapMgmt+"/logs", snapQuery("status", "error"), ""),
				snapManage(http.MethodGet, snapMgmt+"/logs", snapQuery("request_id", "cpa-req-2"), ""),
				snapManage(http.MethodGet, snapMgmt+"/logs", snapQuery("search", "model-b", "limit", "1", "offset", "0"), ""),
				snapManage(http.MethodGet, snapMgmt+"/logs", snapQuery("from", "yesterday"), ""),
				snapManage(http.MethodDelete, snapMgmt+"/logs", nil, ""),
				snapManage(http.MethodGet, snapMgmt+"/logs", nil, ""),
			},
		},
		{
			name:        "management_proxies",
			description: "Proxy list: empty, add, duplicate and invalid adds, unknown delete, import from credentials, list.",
			credentials: []snapCred{
				snapK1,
				{name: "k2.json", fields: map[string]any{"type": "wokey", "api_key": snapKey2, "proxy_url": "http://user:pw@10.0.0.2:8080"}},
			},
			steps: []snapStepDef{
				snapManage(http.MethodGet, snapMgmt+"/proxies", nil, ""),
				snapManage(http.MethodPost, snapMgmt+"/proxies", nil, `{"name":"Tokyo","url":"socks5://user:secret@10.0.0.1:1080"}`),
				snapManage(http.MethodPost, snapMgmt+"/proxies", nil, `{"name":"Again","url":"socks5://user:secret@10.0.0.1:1080"}`),
				snapManage(http.MethodPost, snapMgmt+"/proxies", nil, `{"name":"Bad","url":"ftp://10.0.0.3"}`),
				snapManage(http.MethodDelete, snapMgmt+"/proxies", snapQuery("id", "000000000000"), ""),
				snapManage(http.MethodPost, snapMgmt+"/proxies/import", nil, ""),
				snapManage(http.MethodGet, snapMgmt+"/proxies", nil, ""),
			},
		},
		{
			name:        "management_proxies_check",
			description: "Egress check of the direct line against the (scripted) IP lookup service, kept for the proxy list.",
			script: []scriptedReply{
				snapJSONReply(http.MethodGet, "/ipinfo", http.StatusOK, `{"ip":"203.0.113.7","city":"Tokyo","region":"Tokyo","country":"jp"}`),
			},
			steps: []snapStepDef{
				snapManage(http.MethodPost, snapMgmt+"/proxies/check", nil, `{"target":"direct"}`),
				snapManage(http.MethodGet, snapMgmt+"/proxies", nil, ""),
				snapManage(http.MethodPost, snapMgmt+"/proxies/check", nil, `{"target":"elsewhere"}`),
			},
		},
	}
}

// registerRequestForm is the fixture form of plugin.register for a config.
func registerRequestForm(config map[string]any) json.RawMessage {
	raw, _ := json.Marshal(config)
	form, errForm := registerRequest(raw)
	if errForm != nil {
		panic(errForm)
	}
	return form
}
