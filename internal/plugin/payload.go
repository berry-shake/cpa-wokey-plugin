package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Request bodies are edited in place at the top level only, so members the
// plugin does not touch reach Wokey byte for byte.

// topLevelString returns a string member of a JSON object, or "".
func topLevelString(body []byte, key string) string {
	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &fields); errUnmarshal != nil {
		return ""
	}
	var value string
	if errValue := json.Unmarshal(fields[key], &value); errValue != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// topLevelValueSpan finds the byte range of a top-level member value without
// re-encoding the document, so the payload reaches Wokey byte-for-byte except
// for the edited member.
func topLevelValueSpan(body []byte, key string) (start, end int, found bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	token, errToken := dec.Token()
	if errToken != nil {
		return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errToken)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return 0, 0, false, fmt.Errorf("payload is not a JSON object")
	}
	for dec.More() {
		keyToken, errKey := dec.Token()
		if errKey != nil {
			return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errKey)
		}
		name, _ := keyToken.(string)
		var raw json.RawMessage
		if errValue := dec.Decode(&raw); errValue != nil {
			return 0, 0, false, fmt.Errorf("payload is not JSON: %w", errValue)
		}
		if name == key {
			end = int(dec.InputOffset())
			return end - len(raw), end, true, nil
		}
	}
	return 0, 0, false, nil
}

// setTopLevelString sets a top-level string member, inserting it when absent.
func setTopLevelString(body []byte, key, value string) ([]byte, error) {
	encoded, _ := json.Marshal(value)
	start, end, found, errSpan := topLevelValueSpan(body, key)
	if errSpan != nil {
		return nil, errSpan
	}
	if found {
		out := make([]byte, 0, len(body)-(end-start)+len(encoded))
		out = append(out, body[:start]...)
		out = append(out, encoded...)
		out = append(out, body[end:]...)
		return out, nil
	}
	return insertTopLevelMember(body, key, encoded), nil
}

// ensureTopLevelObject adds key with rawValue when the member is absent.
func ensureTopLevelObject(body []byte, key, rawValue string) []byte {
	_, _, found, errSpan := topLevelValueSpan(body, key)
	if errSpan != nil || found {
		return body
	}
	return insertTopLevelMember(body, key, []byte(rawValue))
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
