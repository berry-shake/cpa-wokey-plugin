// Package web embeds the management page and the plugin logo. The page is
// built into dist by `make web`; without a build a placeholder is served.
package web

import "embed"

//go:embed dist
var dist embed.FS

//go:embed logo.svg
var logo []byte

// Page returns dist/index.html, or dist/placeholder.html when the UI has not been built.
func Page() []byte {
	if page, errRead := dist.ReadFile("dist/index.html"); errRead == nil {
		return page
	}
	page, _ := dist.ReadFile("dist/placeholder.html")
	return page
}

// Logo returns the plugin logo (SVG).
func Logo() []byte {
	return logo
}
