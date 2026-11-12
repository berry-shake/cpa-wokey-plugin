// Package flags serves country flag icons by ISO 3166-1 alpha-2 code.
//
// The SVGs are the regional-indicator flags of Twemoji
// (https://github.com/jdecked/twemoji), copyright Twitter, Inc. and other
// contributors, licensed under CC-BY 4.0; see LICENSE-twemoji. Each file is
// named after its two-letter code, lower case.
package flags

import (
	"embed"
	"strings"
)

//go:embed svg/*.svg
var files embed.FS

// SVG returns the flag for a two-letter country code, in any case.
func SVG(code string) ([]byte, bool) {
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'a' || code[0] > 'z' || code[1] < 'a' || code[1] > 'z' {
		return nil, false
	}
	raw, errRead := files.ReadFile("svg/" + code + ".svg")
	if errRead != nil {
		return nil, false
	}
	return raw, true
}
