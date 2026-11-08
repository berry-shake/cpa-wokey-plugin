package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	statusPath      = managementBase + "/status"
	modelsPath      = managementBase + "/models"
	modelsRefresh   = managementBase + "/models/refresh"
	credentialsPath = managementBase + "/credentials"
	quotaPath       = managementBase + "/credentials/quota"

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
			{Method: http.MethodPost, Path: credentialsPath, Description: `Add a Wokey API key. Body: {"api_key","label","models","prefix","proxy_url"}.`},
			{Method: http.MethodPatch, Path: credentialsPath, Description: `Edit a Wokey credential. Body: {"auth_index","label","models","prefix","proxy_url","priority"}.`},
			{Method: http.MethodGet, Path: quotaPath, Description: "Balance and key usage of one Wokey credential (?auth_index=)."},
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
	default:
		return jsonResponse(http.StatusNotFound, errorBody{Error: "not_found", Message: "plugin route not found"})
	}
	return jsonResponse(http.StatusMethodNotAllowed, errorBody{Error: "method_not_allowed", Message: "method not allowed on this route"})
}

type statusView struct {
	Plugin      string           `json:"plugin"`
	Version     string           `json:"version"`
	Now         string           `json:"now"`
	Config      configView       `json:"config"`
	Catalog     catalogView      `json:"catalog"`
	Credentials []credentialView `json:"credentials"`
	ListError   string           `json:"list_error,omitempty"`
}

type configView struct {
	BaseURL       string `json:"base_url"`
	DefaultPrefix string `json:"default_prefix"`
	// ForceModelPrefix mirrors CPA's routing.force-model-prefix: when false
	// CPA registers the bare model ID next to the prefixed one. Omitted until
	// CPA has sent its config summary.
	ForceModelPrefix         *bool  `json:"force_model_prefix,omitempty"`
	ModelsRefreshMinutes     int    `json:"models_refresh_minutes"`
	RequestTimeoutSeconds    int    `json:"request_timeout_seconds"`
	StreamIdleTimeoutSeconds int    `json:"stream_idle_timeout_seconds"`
	GlobalProxy              string `json:"global_proxy,omitempty"`
}

type catalogView struct {
	Count     int    `json:"count"`
	Revision  string `json:"revision,omitempty"`
	FetchedAt string `json:"fetched_at,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type credentialView struct {
	AuthIndex        string   `json:"auth_index"`
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Label            string   `json:"label"`
	KeyMasked        string   `json:"key_masked"`
	Disabled         bool     `json:"disabled"`
	Unavailable      bool     `json:"unavailable"`
	Status           string   `json:"status,omitempty"`
	StatusMessage    string   `json:"status_message,omitempty"`
	Priority         int      `json:"priority"`
	Note             string   `json:"note,omitempty"`
	BaseURL          string   `json:"base_url,omitempty"`
	ProxyURL         string   `json:"proxy_url,omitempty"`
	Prefix           *string  `json:"prefix"`
	EffectivePrefix  string   `json:"effective_prefix"`
	AllModels        bool     `json:"all_models"`
	Models           []string `json:"models"`
	MissingModels    []string `json:"missing_models,omitempty"`
	RegisteredModels int      `json:"registered_models"`
	RegisteredKnown  bool     `json:"registered_known"`
	Success          int64    `json:"success"`
	Failed           int64    `json:"failed"`
	ReadError        string   `json:"read_error,omitempty"`
}

func (s *Service) status() statusView {
	cfg := s.config()
	view := statusView{
		Plugin:  PluginID,
		Version: s.version,
		Now:     s.now().UTC().Format(time.RFC3339),
		Config: configView{
			BaseURL:                  cfg.BaseURL,
			DefaultPrefix:            cfg.ModelPrefix,
			ModelsRefreshMinutes:     int(cfg.ModelsRefreshInterval / time.Minute),
			RequestTimeoutSeconds:    int(cfg.RequestTimeout / time.Second),
			StreamIdleTimeoutSeconds: int(cfg.StreamIdleTimeout / time.Second),
			GlobalProxy:              redactProxy(s.globalProxy()),
		},
		Catalog:     catalogSummary(s.catalog.current()),
		Credentials: []credentialView{},
	}
	if force, known := s.forceModelPrefix(); known {
		view.Config.ForceModelPrefix = &force
	}
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		view.ListError = errList.Error()
		return view
	}
	registered := s.registeredModels()
	snapshot := s.catalog.current()
	for _, entry := range entries {
		view.Credentials = append(view.Credentials, credentialViewOf(entry, cfg, snapshot, registered))
	}
	return view
}

func catalogSummary(snapshot catalogSnapshot) catalogView {
	view := catalogView{Count: len(snapshot.Models), Revision: snapshot.Revision, LastError: snapshot.LastError}
	if !snapshot.FetchedAt.IsZero() {
		view.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}
	if !snapshot.CheckedAt.IsZero() {
		view.CheckedAt = snapshot.CheckedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func credentialViewOf(entry wokeyAuth, cfg Config, snapshot catalogSnapshot, registered map[string][]string) credentialView {
	view := credentialView{
		AuthIndex:     entry.AuthIndex,
		ID:            entry.ID,
		Name:          entry.Name,
		Label:         entry.Label,
		Disabled:      entry.Disabled,
		Unavailable:   entry.Unavailable,
		Status:        entry.Status,
		StatusMessage: entry.StatusMessage,
		Priority:      entry.Priority,
		Note:          entry.Note,
		Success:       entry.Success,
		Failed:        entry.Failed,
		Models:        []string{},
		ReadError:     entry.readError,
	}
	if models, ok := registered[entry.AuthIndex]; ok {
		view.RegisteredModels = distinctModels(models, entry.cred.effectivePrefix(cfg))
		view.RegisteredKnown = true
	} else if registered != nil {
		view.RegisteredKnown = true
	}
	if entry.readError != "" {
		return view
	}
	cred := entry.cred
	// label is the auth file's own label (possibly empty); the page falls
	// back to the masked key for display.
	view.Label = cred.Label
	view.KeyMasked = maskKey(cred.APIKey)
	view.BaseURL = cred.BaseURL
	view.ProxyURL = redactProxy(cred.ProxyURL)
	view.Prefix = cred.Prefix
	view.EffectivePrefix = cred.effectivePrefix(cfg)
	view.AllModels = cred.AllModels
	if priority := intField(entry.fields, keyPriority); priority != 0 {
		view.Priority = priority
	}
	if note := stringField(entry.fields, keyNote); note != "" {
		view.Note = note
	}
	if !cred.AllModels {
		view.Models = append(view.Models, cred.Models...)
	}
	view.MissingModels = missingModels(cred, snapshot)
	return view
}

// distinctModels counts registered models once each: unless CPA forces the
// prefix, it registers every model both bare and as "<prefix>/<model>".
func distinctModels(ids []string, prefix string) int {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if prefix != "" {
			id = strings.TrimPrefix(id, prefix+"/")
		}
		seen[id] = struct{}{}
	}
	return len(seen)
}

// missingModels lists the selected models that are not in the catalog. It is
// empty for all-model credentials and while no catalog is loaded.
func missingModels(cred credential, snapshot catalogSnapshot) []string {
	if cred.AllModels || snapshot.empty() {
		return nil
	}
	var out []string
	for _, id := range cred.Models {
		if _, ok := snapshot.lookup(id); !ok {
			out = append(out, id)
		}
	}
	return out
}

// redactProxy hides proxy passwords for display.
func redactProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.User == nil {
		return raw
	}
	return parsed.Redacted()
}

type modelView struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name,omitempty"`
	OwnedBy             string        `json:"owned_by,omitempty"`
	ContextLength       int64         `json:"context_length,omitempty"`
	MaxCompletionTokens int64         `json:"max_completion_tokens,omitempty"`
	InputModalities     []string      `json:"input_modalities,omitempty"`
	OutputModalities    []string      `json:"output_modalities,omitempty"`
	Pricing             modelPriceUSD `json:"pricing"`
}

// modelPriceUSD holds prices in USD per million tokens; nil means unknown.
type modelPriceUSD struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

type modelsView struct {
	Catalog catalogView `json:"catalog"`
	Models  []modelView `json:"models"`
}

func priceField(pricing map[string]string, key string) *float64 {
	value, ok := pricePerMillion(pricing[key])
	if !ok {
		return nil
	}
	return &value
}

func modelsViewOf(snapshot catalogSnapshot) modelsView {
	view := modelsView{Catalog: catalogSummary(snapshot), Models: make([]modelView, 0, len(snapshot.Models))}
	for _, model := range snapshot.Models {
		view.Models = append(view.Models, modelView{
			ID:                  model.ID,
			Name:                model.Name,
			OwnedBy:             model.OwnedBy,
			ContextLength:       model.ContextLength,
			MaxCompletionTokens: model.MaxCompletionTokens,
			InputModalities:     model.Architecture.InputModalities,
			OutputModalities:    model.Architecture.OutputModalities,
			Pricing: modelPriceUSD{
				Input:      priceField(model.Pricing, "prompt"),
				Output:     priceField(model.Pricing, "completion"),
				CacheRead:  priceField(model.Pricing, "input_cache_read"),
				CacheWrite: priceField(model.Pricing, "input_cache_write"),
			},
		})
	}
	return view
}

func (s *Service) models() pluginapi.ManagementResponse {
	snapshot := s.catalog.current()
	if snapshot.empty() {
		var errRefresh error
		snapshot, errRefresh = s.refreshCatalog(false)
		if errRefresh != nil && snapshot.empty() {
			return jsonResponse(http.StatusBadGateway, errorBody{Error: "catalog_unavailable", Message: errRefresh.Error()})
		}
	}
	return jsonResponse(http.StatusOK, modelsViewOf(snapshot))
}

func (s *Service) refreshModels() pluginapi.ManagementResponse {
	snapshot, errRefresh := s.refreshCatalog(true)
	if errRefresh != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "catalog_unavailable", Message: errRefresh.Error()})
	}
	return jsonResponse(http.StatusOK, modelsViewOf(snapshot))
}

// credentialInput is the editable part of a Wokey auth file. Pointer and
// RawMessage fields distinguish "absent" from "clear".
type credentialInput struct {
	AuthIndex  string          `json:"auth_index"`
	Name       string          `json:"name"`
	APIKey     string          `json:"api_key"`
	SkipVerify bool            `json:"skip_verify"`
	Label      *string         `json:"label"`
	Note       *string         `json:"note"`
	BaseURL    *string         `json:"base_url"`
	ProxyURL   *string         `json:"proxy_url"`
	Prefix     json.RawMessage `json:"prefix"`
	Models     json.RawMessage `json:"models"`
	Priority   json.RawMessage `json:"priority"`
}

type credentialResult struct {
	OK            bool            `json:"ok"`
	Credential    *credentialView `json:"credential,omitempty"`
	UnknownModels []string        `json:"unknown_models,omitempty"`
}

func decodeCredentialInput(body []byte) (credentialInput, *errorBody) {
	var input credentialInput
	if len(bytes.TrimSpace(body)) == 0 {
		return input, &errorBody{Error: "invalid_json", Message: "request body is required"}
	}
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return input, &errorBody{Error: "invalid_json", Message: errUnmarshal.Error()}
	}
	return input, nil
}

// applyCredentialInput validates input and writes it into an auth file's fields.
func applyCredentialInput(fields map[string]any, input credentialInput) *errorBody {
	if input.Label != nil {
		setOrDelete(fields, keyLabel, strings.TrimSpace(*input.Label))
	}
	if input.Note != nil {
		setOrDelete(fields, keyNote, strings.TrimSpace(*input.Note))
	}
	if input.BaseURL != nil {
		base := strings.TrimSpace(*input.BaseURL)
		if base != "" {
			normalized, errBase := normalizeBaseURL(base)
			if errBase != nil {
				return &errorBody{Error: "invalid_base_url", Message: "base_url: " + errBase.Error()}
			}
			base = normalized
		}
		setOrDelete(fields, keyBaseURL, base)
	}
	if input.ProxyURL != nil {
		proxy := strings.TrimSpace(*input.ProxyURL)
		if errProxy := validateProxyValue(proxy); errProxy != nil {
			return &errorBody{Error: "invalid_proxy", Message: "proxy_url: " + errProxy.Error()}
		}
		setOrDelete(fields, keyProxyURL, proxy)
	}
	if len(input.Prefix) > 0 {
		if isJSONNull(input.Prefix) {
			delete(fields, keyPrefix)
		} else {
			var prefix string
			if errPrefix := json.Unmarshal(input.Prefix, &prefix); errPrefix != nil {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must be a string or null"}
			}
			prefix = normalizePrefix(prefix)
			if strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must not contain spaces"}
			}
			fields[keyPrefix] = prefix
		}
	}
	if len(input.Models) > 0 {
		if isJSONNull(input.Models) {
			delete(fields, keyModels)
		} else {
			var models []string
			if errModels := json.Unmarshal(input.Models, &models); errModels != nil {
				return &errorBody{Error: "invalid_models", Message: "models must be an array of model IDs or null for all models"}
			}
			models = normalizeModelList(models)
			for _, id := range models {
				if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
					return &errorBody{Error: "invalid_models", Message: "model IDs must not contain spaces"}
				}
			}
			fields[keyModels] = models
		}
	}
	if len(input.Priority) > 0 {
		if isJSONNull(input.Priority) {
			delete(fields, keyPriority)
		} else {
			var priority int
			if errPriority := json.Unmarshal(input.Priority, &priority); errPriority != nil {
				return &errorBody{Error: "invalid_priority", Message: "priority must be an integer or null"}
			}
			if priority == 0 {
				delete(fields, keyPriority)
			} else {
				fields[keyPriority] = priority
			}
		}
	}
	return nil
}

func setOrDelete(fields map[string]any, key, value string) {
	if value == "" {
		delete(fields, key)
		return
	}
	fields[key] = value
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// addCredential validates a new API key against Wokey and saves it as an
// auth file named after the key's fingerprint.
func (s *Service) addCredential(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeCredentialInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_api_key", Message: "api_key is required and must not contain spaces"})
	}
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	name := authFileNameForKey(key)
	for _, entry := range entries {
		if entry.cred.APIKey == key || strings.EqualFold(authSaveName(entry.Name, entry.Path), name) {
			return jsonResponse(http.StatusConflict, errorBody{Error: "duplicate_key", Message: "this API key is already added as " + entry.Name})
		}
	}

	fields := map[string]any{keyType: ProviderID, keyAPIKey: key}
	if errApply := applyCredentialInput(fields, input); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	cred := credentialFromMap(fields)
	if !input.SkipVerify {
		ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
		_, perr := s.accountGET(ctx, cred, endpointKeyInfo)
		cancel()
		if perr != nil {
			status := http.StatusBadGateway
			if perr.HTTPStatus == http.StatusUnauthorized || perr.HTTPStatus == http.StatusForbidden || perr.HTTPStatus == http.StatusBadRequest {
				status = http.StatusBadRequest
			}
			return jsonResponse(status, errorBody{Error: "key_verification_failed", Message: perr.Message})
		}
	}
	// Load the catalog before saving so the file records the revision its
	// models are registered from and the background sync leaves it alone.
	snapshot := s.catalog.current()
	if snapshot.empty() {
		snapshot = s.loadCatalogFor(cred)
	}
	if snapshot.Revision != "" {
		fields[keyModelRevision] = snapshot.Revision
	}
	if errSave := s.saveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	s.logf("info", "wokey: added credential", map[string]any{"auth": name})
	return jsonResponse(http.StatusCreated, credentialResult{OK: true, Credential: s.credentialViewByName(name), UnknownModels: missingModels(cred, snapshot)})
}

// updateCredential edits label, models, prefix, proxy, base URL, note or
// priority of an existing Wokey auth file. The API key itself is immutable;
// add a new credential instead.
func (s *Service) updateCredential(body []byte) pluginapi.ManagementResponse {
	input, errInput := decodeCredentialInput(body)
	if errInput != nil {
		return jsonResponse(http.StatusBadRequest, *errInput)
	}
	if strings.TrimSpace(input.APIKey) != "" {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "immutable_api_key", Message: "the API key cannot be changed; add a new credential instead"})
	}
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	entry, found := findEntry(entries, input.AuthIndex, input.Name)
	if !found {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no Wokey credential matches auth_index/name"})
	}
	if entry.readError != "" {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "read_failed", Message: entry.readError})
	}
	name := authSaveName(entry.Name, entry.Path)
	if name == "" {
		return jsonResponse(http.StatusConflict, errorBody{Error: "not_file_backed", Message: "this credential has no auth file the plugin can edit"})
	}
	fields := cloneFields(entry.fields)
	if errApply := applyCredentialInput(fields, input); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	snapshot := s.catalog.current()
	if snapshot.Revision != "" {
		fields[keyModelRevision] = snapshot.Revision
	}
	if errSave := s.saveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	s.logf("info", "wokey: updated credential", map[string]any{"auth": name})
	return jsonResponse(http.StatusOK, credentialResult{OK: true, Credential: s.credentialViewByName(name), UnknownModels: missingModels(credentialFromMap(fields), snapshot)})
}

func findEntry(entries []wokeyAuth, authIndex, name string) (wokeyAuth, bool) {
	authIndex = strings.TrimSpace(authIndex)
	name = strings.TrimSpace(name)
	for _, entry := range entries {
		if authIndex != "" && entry.AuthIndex == authIndex {
			return entry, true
		}
		if name != "" && (entry.Name == name || authSaveName(entry.Name, entry.Path) == name) {
			return entry, true
		}
	}
	return wokeyAuth{}, false
}

// credentialViewByName re-reads one credential after a save, so the response
// shows what CPA actually registered.
func (s *Service) credentialViewByName(name string) *credentialView {
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return nil
	}
	entry, found := findEntry(entries, "", name)
	if !found {
		return nil
	}
	view := credentialViewOf(entry, s.config(), s.catalog.current(), s.registeredModels())
	return &view
}

func (s *Service) credentialQuota(authIndex string) pluginapi.ManagementResponse {
	cred, ok := s.credentialByAuthIndex(authIndex)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no Wokey credential matches auth_index"})
	}
	info, perr := s.fetchAccount(cred)
	if perr != nil {
		// Upstream 401/403 describe the Wokey key, not the management key;
		// keep them out of the page's "management key rejected" handling.
		status := http.StatusBadGateway
		if perr.HTTPStatus >= 400 && perr.HTTPStatus < 500 {
			status = http.StatusBadRequest
		}
		return jsonResponse(status, errorBody{Error: perr.Code, Message: perr.Message})
	}
	return jsonResponse(http.StatusOK, info)
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
