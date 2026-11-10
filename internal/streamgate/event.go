package streamgate

import (
	"bytes"
	"encoding/json"

	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
)

// Event is one SSE event as the gate and its Tap read it. The data is
// scanned for its top-level members on first use and decoded at most once,
// and only when a reader needs more than those members: text deltas, most of
// a stream, are never decoded.
type Event struct {
	name, data []byte

	scanned, object bool
	members         []member
	memberBuf       [8]member

	decoded bool
	payload map[string]any
}

type member struct {
	key        []byte
	start, end int
}

// NewEvent splits an event into its name and its data lines, joined.
func NewEvent(raw []byte) *Event {
	e := &Event{}
	dataLines := 0
	for rest := raw; len(rest) > 0; {
		line := rest
		if newline := bytes.IndexByte(rest, '\n'); newline >= 0 {
			line, rest = rest[:newline], rest[newline+1:]
		} else {
			rest = nil
		}
		line = bytes.TrimRight(line, "\r")
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			e.name = bytes.TrimSpace(line[len("event:"):])
		case bytes.HasPrefix(line, []byte("data:")):
			value := bytes.TrimSpace(line[len("data:"):])
			if dataLines++; dataLines == 1 {
				e.data = value
				continue
			}
			if dataLines == 2 {
				// Copy before appending: the first line still points into raw.
				e.data = append([]byte(nil), e.data...)
			}
			e.data = append(append(e.data, '\n'), value...)
		}
	}
	e.data = bytes.TrimSpace(e.data)
	return e
}

// Name is the event field, or "".
func (e *Event) Name() string { return string(e.name) }

// Data is the joined data lines.
func (e *Event) Data() []byte { return e.data }

// Object reports whether the data is a JSON object.
func (e *Event) Object() bool {
	if !e.scanned {
		e.scanned = true
		e.members = e.memberBuf[:0]
		errScan := jsonfield.EachMember(e.data, func(key []byte, start, end int) bool {
			e.members = append(e.members, member{key, start, end})
			return true
		})
		e.object = errScan == nil
	}
	return e.object
}

// Member returns the raw value of a top-level member of the data, or nil
// when the data is not an object or has no such member.
func (e *Event) Member(key string) []byte {
	if !e.Object() {
		return nil
	}
	for _, m := range e.members {
		if string(m.key) == key {
			return e.data[m.start:m.end]
		}
	}
	return nil
}

// present reports a member with a value other than null.
func (e *Event) present(key string) bool {
	value := e.Member(key)
	return value != nil && string(value) != "null"
}

// stringMember returns a top-level string member, or "".
func (e *Event) stringMember(key string) string {
	value := e.Member(key)
	if len(value) < 2 || value[0] != '"' {
		return ""
	}
	if bytes.IndexByte(value, '\\') < 0 {
		return string(value[1 : len(value)-1])
	}
	var decoded string
	_ = json.Unmarshal(value, &decoded)
	return decoded
}

// Payload decodes the data, or returns nil when it is not a JSON object.
func (e *Event) Payload() map[string]any {
	if !e.decoded {
		e.decoded = true
		if e.Object() && json.Unmarshal(e.data, &e.payload) != nil {
			e.payload = nil
		}
	}
	return e.payload
}
