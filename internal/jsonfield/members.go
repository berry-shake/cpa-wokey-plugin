package jsonfield

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotObject is returned by EachMember for a document that is not a JSON
// object.
var ErrNotObject = errors.New("not a JSON object")

// EachMember calls visit with the key and the value span [start, end) of
// each top-level member of the JSON object doc, in document order, until
// visit returns false. Keys are unescaped.
//
// It follows the structure (strings, brackets, colons and commas) without
// decoding any value or checking how numbers and literals are spelled, so
// finding a few members of a large request costs a fraction of
// json.Unmarshal. Values that matter still go through encoding/json.
func EachMember(doc []byte, visit func(key []byte, start, end int) bool) error {
	i := skipSpace(doc, 0)
	if i == len(doc) || doc[i] != '{' {
		return ErrNotObject
	}
	i = skipSpace(doc, i+1)
	if i < len(doc) && doc[i] == '}' {
		return trailing(doc, i+1)
	}
	for {
		if i == len(doc) || doc[i] != '"' {
			return syntaxError(i, "an object key")
		}
		keyEnd, errKey := stringEnd(doc, i)
		if errKey != nil {
			return errKey
		}
		key := doc[i+1 : keyEnd-1]
		if bytes.IndexByte(key, '\\') >= 0 {
			var decoded string
			if json.Unmarshal(doc[i:keyEnd], &decoded) != nil {
				return syntaxError(i, "an object key")
			}
			key = []byte(decoded)
		}
		i = skipSpace(doc, keyEnd)
		if i == len(doc) || doc[i] != ':' {
			return syntaxError(i, "':'")
		}
		start := skipSpace(doc, i+1)
		end, errValue := valueEnd(doc, start)
		if errValue != nil {
			return errValue
		}
		if !visit(key, start, end) {
			return nil
		}
		i = skipSpace(doc, end)
		switch {
		case i < len(doc) && doc[i] == ',':
			i = skipSpace(doc, i+1)
		case i < len(doc) && doc[i] == '}':
			return trailing(doc, i+1)
		default:
			return syntaxError(i, "',' or '}'")
		}
	}
}

// Has reports whether doc is a JSON object with a top-level member key.
func Has(doc []byte, key string) bool {
	found := false
	_ = EachMember(doc, func(name []byte, _, _ int) bool {
		found = string(name) == key
		return !found
	})
	return found
}

// valueEnd returns the end of the value that starts at i.
func valueEnd(doc []byte, i int) (int, error) {
	if i == len(doc) {
		return 0, syntaxError(i, "a value")
	}
	switch doc[i] {
	case '"':
		return stringEnd(doc, i)
	case '{', '[':
		return containerEnd(doc, i)
	}
	end := i
	for end < len(doc) && isScalarByte(doc[end]) {
		end++
	}
	if end == i {
		return 0, syntaxError(i, "a value")
	}
	return end, nil
}

// containerEnd returns the end of the object or array that starts at i,
// checking that brackets pair up.
func containerEnd(doc []byte, i int) (int, error) {
	var closersBuf [32]byte
	closers := closersBuf[:0]
	for i < len(doc) {
		switch c := doc[i]; c {
		case '"':
			end, errString := stringEnd(doc, i)
			if errString != nil {
				return 0, errString
			}
			i = end
			continue
		case '{':
			closers = append(closers, '}')
		case '[':
			closers = append(closers, ']')
		case '}', ']':
			if closers[len(closers)-1] != c {
				return 0, syntaxError(i, fmt.Sprintf("%q", closers[len(closers)-1]))
			}
			closers = closers[:len(closers)-1]
			if len(closers) == 0 {
				return i + 1, nil
			}
		}
		i++
	}
	return 0, syntaxError(i, "the end of the value")
}

// stringEnd returns the end of the string whose opening quote is at i.
func stringEnd(doc []byte, i int) (int, error) {
	from := i + 1
	for {
		offset := bytes.IndexByte(doc[from:], '"')
		if offset < 0 {
			return 0, syntaxError(len(doc), "the end of the string")
		}
		quote := from + offset
		// An odd run of backslashes right before the quote escapes it.
		backslashes := 0
		for at := quote - 1; at > i && doc[at] == '\\'; at-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1, nil
		}
		from = quote + 1
	}
}

func isScalarByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '+' || c == '.'
}

func skipSpace(doc []byte, i int) int {
	for i < len(doc) {
		switch doc[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func trailing(doc []byte, i int) error {
	if i = skipSpace(doc, i); i != len(doc) {
		return syntaxError(i, "the end of the document")
	}
	return nil
}

func syntaxError(offset int, want string) error {
	return fmt.Errorf("invalid JSON at offset %d: expected %s", offset, want)
}
