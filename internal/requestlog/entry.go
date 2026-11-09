// Package requestlog stores the request log shown on the management page in
// SQLite (wokey.db in the plugin data dir).
package requestlog

import "time"

// Entry is one executed request as shown on the request log tab.
type Entry struct {
	ID string `json:"id"`
	// RequestID is CPA's request ID (the one in CPA's logs); empty for
	// entries written before 0.1.7 learned it or when CPA did not send it.
	RequestID       string    `json:"request_id,omitempty"`
	Time            time.Time `json:"time"`
	Endpoint        string    `json:"endpoint"`
	Model           string    `json:"model"`
	UpstreamModel   string    `json:"upstream_model,omitempty"`
	ServedModel     string    `json:"served_model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Stream          bool      `json:"stream"`
	Status          string    `json:"status"` // "success" or "error"
	HTTPStatus      int       `json:"http_status,omitempty"`
	Credential      string    `json:"credential,omitempty"`
	AuthID          string    `json:"auth_id,omitempty"`
	DurationMS      int64     `json:"duration_ms"`
	TTFTMS          *int64    `json:"ttft_ms,omitempty"`
	InputTokens     int64     `json:"input_tokens"`
	OutputTokens    int64     `json:"output_tokens"`
	CachedTokens    int64     `json:"cached_tokens"`
	CacheWrite      int64     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens int64     `json:"reasoning_tokens,omitempty"`
	TokensPerSecond *float64  `json:"output_tokens_per_second,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	// ErrorPhase is "request" (before any output reached the client) or
	// "stream" (after output started).
	ErrorPhase string `json:"error_phase,omitempty"`
}
