// Package jsonfield reads loosely typed values out of decoded JSON objects
// (auth files, Wokey responses, stream events).
package jsonfield

import (
	"encoding/json"
	"strconv"
	"strings"
)

// String returns the first non-blank string among keys, trimmed.
func String(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := fields[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// Bool accepts a JSON boolean or the string "true" (any case).
func Bool(fields map[string]any, key string) bool {
	switch value := fields[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

// Int accepts a JSON number, decoded as float64, int or json.Number.
func Int(fields map[string]any, key string) int {
	switch value := fields[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// Text renders a scalar sent as a JSON string or number, such as an ID or an
// error code; other values give "".
func Text(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}
