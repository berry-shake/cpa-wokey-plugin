package plugin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	statusPath      = managementBase + "/status"
	modelsPath      = managementBase + "/models"
	modelsRefresh   = managementBase + "/models/refresh"
	credentialsPath = managementBase + "/credentials"
	quotaPath       = managementBase + "/credentials/quota"
	logsPath        = managementBase + "/logs"

	// maxManagementBody bounds request bodies of the management routes.
	maxManagementBody = 1 << 20
)

// routeDeclaration is a /v0/management route; CPA checks the management key
// before forwarding the request to the plugin.
type routeDeclaration struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// resourceDeclaration is a /v0/resource route; CPA does not authenticate it.
type resourceDeclaration struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type managementRegistration struct {
	Routes    []routeDeclaration    `json:"routes,omitempty"`
	Resources []resourceDeclaration `json:"resources,omitempty"`
}

func managementRoutes() managementRegistration {
	return managementRegistration{
		Routes: []routeDeclaration{
			{Method: http.MethodGet, Path: statusPath, Description: "Plugin status, model catalog summary and Wokey credentials."},
			{Method: http.MethodGet, Path: modelsPath, Description: "Wokey model catalog with prices per million tokens."},
			{Method: http.MethodPost, Path: modelsRefresh, Description: "Reload the Wokey model catalog and re-register changed models."},
			{Method: http.MethodPost, Path: credentialsPath, Description: `Add a Wokey API key. Body: {"api_key","label","note","models","prefix","proxy_url"|"proxy_id","issuer_token"}.`},
			{Method: http.MethodPatch, Path: credentialsPath, Description: `Edit a Wokey credential. Body: {"auth_index","label","note","models","prefix","proxy_url"|"proxy_id","priority","issuer_token"}.`},
			{Method: http.MethodGet, Path: quotaPath, Description: "Balance and key usage of one Wokey credential (?auth_index=), plus Provider earnings when it has an issuing token."},
			{Method: http.MethodGet, Path: logsPath, Description: "Request log, newest first (?search=&status=all|success|error&from=&to=&request_id=&limit=&offset=), with a summary."},
			{Method: http.MethodDelete, Path: logsPath, Description: "Clear the request log."},
			{Method: http.MethodGet, Path: proxiesPath, Description: "Proxy list (URLs redacted) and CPA's global proxy."},
			{Method: http.MethodPost, Path: proxiesPath, Description: `Add a proxy. Body: {"name","url"}.`},
			{Method: http.MethodPatch, Path: proxiesPath, Description: `Rename a proxy or change its URL. Body: {"id","name","url"}. Credentials using the old URL move to the new one.`},
			{Method: http.MethodDelete, Path: proxiesPath, Description: "Remove a proxy (?id=). Credentials using it keep its URL."},
			{Method: http.MethodPost, Path: proxiesImportPath, Description: "Add the proxies Wokey credentials already use to the list."},
			{Method: http.MethodPost, Path: proxiesCheckPath, Description: `Look up the exit IP and latency of a proxy. Body: {"id"} or {"target":"direct"|"inherit"}.`},
		},
		Resources: []resourceDeclaration{
			{Path: "/manage", Menu: displayName, Description: "Manage Wokey API keys, model selection and egress proxies."},
			{Path: "/logo.svg", Description: "Plugin logo served to the management panel sidebar."},
		},
	}
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// handleManagement serves the authenticated /v0/management routes and the
// unauthenticated page and logo resources.
func (s *Service) handleManagement(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if len(req.Body) > maxManagementBody {
		return jsonResponse(http.StatusRequestEntityTooLarge, errorBody{Error: "body_too_large", Message: "request body is too large"})
	}
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	switch path {
	case logoPath:
		if method == http.MethodGet {
			return logoResponse()
		}
	case pagePath:
		if method == http.MethodGet {
			return pageResponse()
		}
	case statusPath:
		if method == http.MethodGet {
			return jsonResponse(http.StatusOK, s.status())
		}
	case modelsPath:
		if method == http.MethodGet {
			return s.models()
		}
	case modelsRefresh:
		if method == http.MethodPost {
			return s.refreshModels()
		}
	case credentialsPath:
		switch method {
		case http.MethodPost:
			return s.addCredential(req.Body)
		case http.MethodPatch:
			return s.updateCredential(req.Body)
		}
	case quotaPath:
		if method == http.MethodGet {
			return s.credentialQuota(req.Query.Get("auth_index"))
		}
	case logsPath:
		switch method {
		case http.MethodGet:
			limit, _ := strconv.Atoi(req.Query.Get("limit"))
			offset, _ := strconv.Atoi(req.Query.Get("offset"))
			from, errFrom := parseLogTime(req.Query.Get("from"))
			to, errTo := parseLogTime(req.Query.Get("to"))
			if errFrom != nil || errTo != nil {
				return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_range", Message: "from and to must be RFC 3339 times"})
			}
			return jsonResponse(http.StatusOK, s.reqlog.Query(requestlog.Query{
				Search:    req.Query.Get("search"),
				Status:    req.Query.Get("status"),
				RequestID: req.Query.Get("request_id"),
				From:      from,
				To:        to,
				Limit:     limit,
				Offset:    offset,
			}))
		case http.MethodDelete:
			if errClear := s.reqlog.Clear(); errClear != nil {
				return jsonResponse(http.StatusInternalServerError, errorBody{Error: "clear_failed", Message: errClear.Error()})
			}
			return jsonResponse(http.StatusOK, map[string]bool{"ok": true})
		}
	case proxiesPath:
		switch method {
		case http.MethodGet:
			return jsonResponse(http.StatusOK, s.proxiesView())
		case http.MethodPost:
			return s.addProxy(req.Body)
		case http.MethodPatch:
			return s.updateProxy(req.Body)
		case http.MethodDelete:
			return s.deleteProxy(req.Query.Get("id"))
		}
	case proxiesImportPath:
		if method == http.MethodPost {
			return s.importProxies()
		}
	case proxiesCheckPath:
		if method == http.MethodPost {
			return s.checkProxy(req.Body)
		}
	default:
		return jsonResponse(http.StatusNotFound, errorBody{Error: "not_found", Message: "plugin route not found"})
	}
	return jsonResponse(http.StatusMethodNotAllowed, errorBody{Error: "method_not_allowed", Message: "method not allowed on this route"})
}

func jsonResponse(status int, v any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		status = http.StatusInternalServerError
		raw, _ = json.Marshal(errorBody{Error: "marshal_failed", Message: errMarshal.Error()})
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: raw,
	}
}

// parseLogTime reads a request log range bound; empty means unbounded.
func parseLogTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}
