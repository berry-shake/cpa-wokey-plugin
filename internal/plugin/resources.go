package plugin

import (
	"net/http"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/web"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pageCSP only allows same-origin framing, so the CPA management panel can
// embed the page and share its remembered management key.
var pageCSP = strings.Join([]string{
	"default-src 'none'",
	"style-src 'unsafe-inline'",
	"script-src 'unsafe-inline'",
	"img-src 'self' data:",
	"connect-src 'self'",
	"base-uri 'none'",
	"form-action 'none'",
	"frame-ancestors 'self'",
}, "; ")

// logoCSP keeps the SVG inert when it is opened directly: no scripts, no
// external fetches.
const logoCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

func pageResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"text/html; charset=utf-8"},
			"Cache-Control":           []string{"no-store"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Content-Security-Policy": []string{pageCSP},
		},
		Body: append([]byte(nil), web.Page()...),
	}
}

func logoResponse() pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":            []string{"image/svg+xml"},
			"Cache-Control":           []string{"public, max-age=86400"},
			"X-Content-Type-Options":  []string{"nosniff"},
			"Content-Security-Policy": []string{logoCSP},
		},
		Body: append([]byte(nil), web.Logo()...),
	}
}
