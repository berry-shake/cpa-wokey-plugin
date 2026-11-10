package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
)

// Request bodies are edited in place at the top level only, so members the
// plugin does not touch reach Wokey byte for byte. Requests can run to
// megabytes, so they are scanned for the few members the plugin needs
// instead of being decoded.

// span is the byte range of a member value inside a body.
type span struct{ start, end int }

// topLevelMembers finds the wanted top-level members of a JSON object and
// keeps the first occurrence of each. The scan stops once all of them are
// found, so a member near the start is cheap even on a large body. errScan
// reports a body that is not a well-formed object, met before the scan
// stopped; the members found up to that point are still returned.
func topLevelMembers(body []byte, keys ...string) (found map[string]span, errScan error) {
	found = make(map[string]span, len(keys))
	errScan = jsonfield.EachMember(body, func(key []byte, start, end int) bool {
		for _, want := range keys {
			if string(key) == want {
				if _, seen := found[want]; !seen {
					found[want] = span{start, end}
				}
				break
			}
		}
		return len(found) < len(keys)
	})
	if errScan != nil {
		errScan = payloadError(errScan)
	}
	return found, errScan
}

func payloadError(errScan error) error {
	if errors.Is(errScan, jsonfield.ErrNotObject) {
		return fmt.Errorf("payload is not a JSON object")
	}
	return fmt.Errorf("payload is not JSON: %w", errScan)
}

// topLevelString returns a string member of a JSON object, or "".
func topLevelString(body []byte, key string) string {
	found, _ := topLevelMembers(body, key)
	return memberString(body, found, key)
}

// memberString decodes a string member found by topLevelMembers, or "".
func memberString(body []byte, found map[string]span, key string) string {
	at, ok := found[key]
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(body[at.start:at.end], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// memberValue decodes a member found by topLevelMembers; ok is false when
// it is absent or not valid JSON.
func memberValue(body []byte, found map[string]span, key string) (value any, ok bool) {
	at, present := found[key]
	if !present || json.Unmarshal(body[at.start:at.end], &value) != nil {
		return nil, false
	}
	return value, true
}

// setMember replaces the value of a member found by topLevelMembers. A
// member the scan did not find is inserted, unless the scan stopped on a
// malformed body, which is returned as the error.
func setMember(body []byte, found map[string]span, errScan error, key string, rawValue []byte) ([]byte, error) {
	if at, ok := found[key]; ok {
		out := make([]byte, 0, len(body)-(at.end-at.start)+len(rawValue))
		out = append(out, body[:at.start]...)
		out = append(out, rawValue...)
		out = append(out, body[at.end:]...)
		return out, nil
	}
	if errScan != nil {
		return nil, errScan
	}
	return insertTopLevelMember(body, key, rawValue), nil
}

// setTopLevelString sets a top-level string member, inserting it when absent.
func setTopLevelString(body []byte, key, value string) ([]byte, error) {
	encoded, _ := json.Marshal(value)
	found, errScan := topLevelMembers(body, key)
	return setMember(body, found, errScan, key, encoded)
}

func insertTopLevelMember(body []byte, key string, rawValue []byte) []byte {
	open := bytes.IndexByte(body, '{')
	if open < 0 {
		return body
	}
	encodedKey, _ := json.Marshal(key)
	member := append(append(append([]byte{}, encodedKey...), ':'), rawValue...)
	rest := bytes.TrimLeft(body[open+1:], " \t\r\n")
	out := make([]byte, 0, len(body)+len(member)+1)
	out = append(out, body[:open+1]...)
	out = append(out, member...)
	if len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	out = append(out, body[open+1:]...)
	return out
}
