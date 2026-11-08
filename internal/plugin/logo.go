package plugin

import (
	_ "embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed logo.svg
var logoSVG []byte

// logoCSP keeps the SVG inert when it is opened directly: no scripts, no
// external fetches.
const logoCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

func logoResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"image/svg+xml"},
			"Cache-Control":           []string{"public, max-age=86400"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Content-Security-Policy": []string{logoCSP},
		},
		Body: append([]byte(nil), logoSVG...),
	}
}
