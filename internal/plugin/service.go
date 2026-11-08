// Package plugin implements the Wokey provider plugin for CLIProxyAPI.
//
// Each Wokey API key is a CPA auth file ({"type":"wokey","api_key":...}).
// The plugin registers the key's models (all catalog models or a selected
// subset; CPA adds the auth prefix, "wokey/" by default), executes requests natively in
// the three protocols Wokey serves (chat completions, Responses, Anthropic
// Messages), reports balance and key usage through CPA's quota panel and
// serves a management page to add keys and pick models.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	// PluginID is the store/runtime identifier; ProviderID is the CPA provider
	// key and the auth file type. They are the same string.
	PluginID   = "wokey"
	ProviderID = "wokey"

	displayName = "Wokey"
	repository  = "https://github.com/berry-shake/cpa-wokey-plugin"

	managementBase = "/v0/management/plugins/" + PluginID
	resourceBase   = "/v0/resource/plugins/" + PluginID
	logoPath       = resourceBase + "/logo.svg"
	pagePath       = resourceBase + "/manage"

	// catalogLoadTimeout bounds the synchronous catalog load in model.for_auth.
	catalogLoadTimeout = 15 * time.Second
)

// maxSchemaVersion is the newest RPC contract implemented here. Schema 6
// returns management JSON verbatim. It is pinned so an SDK bump cannot
// silently opt into newer contracts.
const maxSchemaVersion = pluginabi.SchemaVersionRawManagementResponse

// HostCaller invokes a CPA host callback and returns the raw response envelope.
type HostCaller func(method string, request []byte) ([]byte, error)

// Service dispatches plugin RPC methods.
type Service struct {
	version string
	host    HostCaller
	now     func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.RWMutex
	cfg       Config
	hostProxy string
	// hostForcePrefix mirrors CPA's routing.force-model-prefix; hostKnown
	// is false until a request carrying the host summary arrives.
	hostForcePrefix bool
	hostKnown       bool

	catalog *catalog
	clients *clientCache
	streams streamGroup
	// issuerBase is the issuing API root; tests point it at a fake server.
	issuerBase string

	loopOnce sync.Once
	kick     chan struct{}
	// registered maps auth ID -> catalog revision its models were built from
	// ("" when built without a catalog), so catalog changes can be pushed to
	// CPA by re-saving only the affected auth files.
	registered sync.Map
	reqlog     *requestLog
}

// New builds a service. host may be nil in tests.
func New(version string, host HostCaller) *Service {
	if strings.TrimSpace(version) == "" {
		version = "0.0.0-dev"
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc := &Service{
		version:    version,
		host:       host,
		now:        time.Now,
		ctx:        ctx,
		cancel:     cancel,
		cfg:        defaultConfig(),
		catalog:    newCatalog(),
		clients:    newClientCache(),
		issuerBase: defaultIssuerBaseURL,
		kick:       make(chan struct{}, 1),
	}
	svc.reqlog = newRequestLog(func(message string) { svc.logf("warn", message, nil) })
	return svc
}

// Close stops background work and in-flight streams.
func (s *Service) Close() {
	s.cancel()
	s.streams.waitTimeout(3 * time.Second)
	s.clients.closeIdle()
}

type rpcLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type rpcCapabilities struct {
	AuthProvider          bool     `json:"auth_provider"`
	ModelProvider         bool     `json:"model_provider"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
	ManagementAPI         bool     `json:"management_api"`
	QuotaProvider         bool     `json:"quota_provider"`
}

type rpcRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  rpcCapabilities    `json:"capabilities"`
}

type rpcIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcAuthModelRequest struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcAuthRefreshRequest struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// Call handles one plugin RPC method and always returns a JSON envelope.
func (s *Service) Call(method string, request []byte) []byte {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(s.register(request))
	case pluginabi.MethodPluginQuiesce:
		return okEnvelope(struct{}{})
	case pluginabi.MethodPluginShutdown:
		s.Close()
		return okEnvelope(struct{}{})

	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier:
		return okEnvelope(rpcIdentifierResponse{Identifier: ProviderID})
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode auth parse request: "+errUnmarshal.Error())
		}
		s.rememberHost(req.Host)
		resp, errParse := parseAuthFile(req, s.now(), s.config().ModelPrefix)
		if errParse != nil {
			return ErrorEnvelope("invalid_auth", errParse.Error())
		}
		return okEnvelope(resp)
	case pluginabi.MethodAuthLoginStart:
		return errorEnvelopeFrom(&pluginabi.Error{
			Code:       "unsupported_login",
			Message:    "Wokey uses API keys: add one on the Wokey plugin page or upload a wokey auth file",
			HTTPStatus: http.StatusBadRequest,
		})
	case pluginabi.MethodAuthLoginPoll:
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatus("error"), Message: "Wokey uses API keys; there is no login flow"})
	case pluginabi.MethodAuthRefresh:
		var req rpcAuthRefreshRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode auth refresh request: "+errUnmarshal.Error())
		}
		return s.refreshAuth(req.AuthRefreshRequest)

	case pluginabi.MethodModelStatic:
		return okEnvelope(pluginapi.ModelResponse{Provider: ProviderID})
	case pluginabi.MethodModelForAuth:
		var req rpcAuthModelRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode model request: "+errUnmarshal.Error())
		}
		return okEnvelope(s.modelsForAuth(req.AuthModelRequest))

	case pluginabi.MethodExecutorExecute:
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.execute(req.ExecutorRequest)
	case pluginabi.MethodExecutorExecuteStream:
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.executeStream(req)
	case pluginabi.MethodExecutorCountTokens:
		var req rpcExecutorRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode executor request: "+errUnmarshal.Error())
		}
		return s.countTokens(req.ExecutorRequest)
	case pluginabi.MethodExecutorHTTPRequest:
		return errorEnvelopeFrom(&pluginabi.Error{Code: "unsupported_operation", Message: "wokey: raw HTTP requests are not supported", HTTPStatus: http.StatusNotImplemented})

	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRoutes())
	case pluginabi.MethodManagementHandle:
		var req rpcManagementRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode management request: "+errUnmarshal.Error())
		}
		return okEnvelope(s.handleManagement(req.ManagementRequest))

	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{SupportedProviders: []string{ProviderID}, DisplayName: displayName})
	case pluginabi.MethodQuotaFetch:
		var req rpcQuotaFetchRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return ErrorEnvelope("invalid_request", "decode quota request: "+errUnmarshal.Error())
		}
		return s.quotaFetch(req.QuotaFetchRequest)
	case pluginabi.MethodQuotaReset:
		return errorEnvelopeFrom(&pluginabi.Error{Code: "unsupported_operation", Message: "wokey: quota reset is not supported", HTTPStatus: http.StatusNotImplemented})

	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method)
	}
}

func (s *Service) register(request []byte) rpcRegistration {
	var lifecycle rpcLifecycleRequest
	_ = json.Unmarshal(request, &lifecycle)
	cfg, warnings := parseConfig(lifecycle.ConfigYAML)
	s.mu.Lock()
	previous := s.cfg
	s.cfg = cfg
	s.mu.Unlock()
	for _, warning := range warnings {
		s.logf("warn", "wokey: "+warning, nil)
	}
	s.reqlog.configure(cfg.DataDir, cfg.LogRetention)
	s.loopOnce.Do(func() { go s.refreshLoop() })
	if previous.ModelPrefix != cfg.ModelPrefix || previous.BaseURL != cfg.BaseURL {
		// The default auth prefix is applied when CPA parses an auth file,
		// so re-save every file to push the new prefix to CPA.
		s.registered.Range(func(key, _ any) bool {
			s.registered.Store(key, "stale")
			return true
		})
	}
	s.kickRefresh()
	return rpcRegistration{
		SchemaVersion: negotiateSchemaVersion(lifecycle.SchemaVersion),
		Metadata: pluginapi.Metadata{
			Name:             displayName,
			Version:          s.version,
			Author:           "berry-shake",
			GitHubRepository: repository,
			Logo:             logoPath,
			ConfigFields:     configFields(),
		},
		Capabilities: rpcCapabilities{
			AuthProvider:          true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  executorFormats,
			ExecutorOutputFormats: executorFormats,
			ManagementAPI:         true,
			QuotaProvider:         true,
		},
	}
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "base-url", Type: pluginapi.ConfigFieldTypeString, Default: defaultBaseURL, Description: "Wokey API origin. Auth files may override it with base_url."},
		{Name: "model-prefix", Type: pluginapi.ConfigFieldTypeString, Default: defaultModelPrefix, Description: `Default CPA prefix for Wokey keys ("wokey" -> wokey/claude-fable-5). Empty adds no prefix. An auth file's own prefix field wins.`},
		{Name: "models-refresh-minutes", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultModelsRefreshMinutes, Description: "How often to poll Wokey's model catalog; 0 disables polling."},
		{Name: "request-timeout-seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultRequestTimeoutSeconds, Description: "Timeout for non-streaming requests and for stream response headers."},
		{Name: "stream-idle-timeout-seconds", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultStreamIdleSeconds, Description: "Abort a stream after this long without upstream bytes."},
		{Name: "data-dir", Type: pluginapi.ConfigFieldTypeString, Default: defaultDataDir, Description: "Directory for plugin data such as the request log, relative to CPA's working directory."},
		{Name: "log-retention", Type: pluginapi.ConfigFieldTypeInteger, Default: defaultLogRetention, Description: "How many requests the request log keeps; 0 disables it."},
	}
}

// negotiateSchemaVersion answers with the newest schema both sides support;
// a host that advertises nothing only knows schema 1.
func negotiateSchemaVersion(host uint32) uint32 {
	if host == 0 {
		return 1
	}
	return min(host, maxSchemaVersion)
}

func (s *Service) config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// rememberHost caches CPA's global proxy-url and force-model-prefix, which
// only arrive with auth, model and quota requests.
func (s *Service) rememberHost(host pluginapi.HostConfigSummary) {
	proxyURL := strings.TrimSpace(host.ProxyURL)
	s.mu.Lock()
	s.hostProxy = proxyURL
	s.hostForcePrefix = host.ForceModelPrefix
	s.hostKnown = true
	s.mu.Unlock()
}

// forceModelPrefix reports CPA's routing.force-model-prefix and whether it
// is known yet.
func (s *Service) forceModelPrefix() (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostForcePrefix, s.hostKnown
}

func (s *Service) globalProxy() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostProxy
}

func (s *Service) userAgent() string {
	return "cpa-wokey-plugin/" + s.version
}

func (s *Service) refreshAuth(req pluginapi.AuthRefreshRequest) []byte {
	s.rememberHost(req.Host)
	_, merged, errCred := credentialFromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil {
		return ErrorEnvelope("invalid_auth", errCred.Error())
	}
	raw, _ := json.Marshal(merged)
	auth, errAuth := authDataFromFields(merged, raw, "", s.now(), s.config().ModelPrefix)
	if errAuth != nil {
		return ErrorEnvelope("invalid_auth", errAuth.Error())
	}
	auth.ID = req.AuthID
	return okEnvelope(pluginapi.AuthRefreshResponse{Auth: auth, NextRefreshAfter: auth.NextRefreshAfter})
}

// modelsForAuth lists the CPA models for one Wokey credential.
func (s *Service) modelsForAuth(req pluginapi.AuthModelRequest) pluginapi.ModelResponse {
	s.rememberHost(req.Host)
	cred, _, errCred := credentialFromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil {
		s.logf("warn", "wokey: cannot read credential for model registration", map[string]any{"auth_id": req.AuthID, "error": errCred.Error()})
		return pluginapi.ModelResponse{Provider: ProviderID}
	}
	snapshot := s.catalog.current()
	if snapshot.empty() {
		snapshot = s.loadCatalogFor(cred)
	}
	models := modelInfosForCredential(snapshot, cred)
	if req.AuthID != "" {
		s.registered.Store(req.AuthID, snapshot.Revision)
	}
	return pluginapi.ModelResponse{Provider: ProviderID, Models: models}
}

// loadCatalogFor fetches the catalog synchronously through the credential's
// own egress, so a key that needs a proxy can still discover models.
func (s *Service) loadCatalogFor(cred credential) catalogSnapshot {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	setting, errProxy := resolveProxy(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return s.catalog.current()
	}
	snapshot, _, errFetch := s.catalog.fetch(ctx, s.clients.client(setting), cred.effectiveBaseURL(s.config()), s.userAgent(), false, s.now())
	if errFetch != nil {
		s.logf("warn", "wokey: model catalog unavailable", map[string]any{"error": errFetch.Error()})
	}
	return snapshot
}

func (s *Service) kickRefresh() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// refreshLoop polls the catalog and pushes changes to CPA.
func (s *Service) refreshLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
		case <-timer.C:
		}
		s.refreshCatalog(false)
		if interval := s.config().ModelsRefreshInterval; interval > 0 {
			timer.Reset(interval)
		}
	}
}

// refreshCatalog fetches the catalog (trying the global egress first, then
// each distinct credential proxy) and re-registers credentials whose models
// were built from an older catalog.
func (s *Service) refreshCatalog(force bool) (catalogSnapshot, error) {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	entries, _ := s.listWokeyAuths()
	proxies := []string{""}
	seen := map[string]bool{"": true}
	for _, entry := range entries {
		if entry.readError == "" && !seen[entry.cred.ProxyURL] {
			seen[entry.cred.ProxyURL] = true
			proxies = append(proxies, entry.cred.ProxyURL)
		}
	}
	var (
		snapshot catalogSnapshot
		errFetch error
	)
	for _, proxy := range proxies {
		setting, errProxy := resolveProxy(proxy, s.globalProxy())
		if errProxy != nil {
			continue
		}
		snapshot, _, errFetch = s.catalog.fetch(ctx, s.clients.client(setting), s.config().BaseURL, s.userAgent(), force, s.now())
		if errFetch == nil {
			break
		}
	}
	if errFetch != nil {
		s.logf("warn", "wokey: model catalog refresh failed", map[string]any{"error": errFetch.Error()})
		return snapshot, errFetch
	}
	s.syncRegistrations(snapshot, entries)
	return snapshot, nil
}

// syncRegistrations re-saves auth files whose registered models predate the
// current catalog. Saving makes CPA re-parse the file and call model.for_auth
// again. model_revision records the catalog a file was last synced with; the
// in-memory registered map catches enabled credentials whose models were built
// from an older catalog. Disabled credentials are not registered by CPA, so
// only their file revision is checked and they are touched once per change.
func (s *Service) syncRegistrations(snapshot catalogSnapshot, entries []wokeyAuth) {
	if snapshot.Revision == "" {
		return
	}
	for _, entry := range entries {
		if entry.readError != "" || !isWokeyAuthJSON(entry.fields) {
			continue
		}
		stale := entry.cred.ModelRevision != snapshot.Revision
		if !entry.Disabled {
			if registered, known := s.registered.Load(entry.ID); known && registered != snapshot.Revision {
				stale = true
			}
		}
		if !stale {
			continue
		}
		name := authSaveName(entry.Name, entry.Path)
		if name == "" {
			continue
		}
		fields := cloneFields(entry.fields)
		fields[keyModelRevision] = snapshot.Revision
		if errSave := s.saveAuthFile(name, fields); errSave != nil {
			s.logf("warn", "wokey: model sync save failed", map[string]any{"auth": name, "error": errSave.Error()})
			continue
		}
		s.logf("info", "wokey: refreshed registered models", map[string]any{"auth": name, "revision": snapshot.Revision})
	}
}

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		out[key] = value
	}
	return out
}

// hostCall invokes a host callback and decodes its result into out (if non-nil).
func (s *Service) hostCall(method string, request any, out any) error {
	if s.host == nil {
		return fmt.Errorf("host callbacks are unavailable")
	}
	raw, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return errMarshal
	}
	resp, errCall := s.host(method, raw)
	if errCall != nil {
		return errCall
	}
	var envelope pluginabi.Envelope
	if errUnmarshal := json.Unmarshal(resp, &envelope); errUnmarshal != nil {
		return fmt.Errorf("decode %s response: %w", method, errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return fmt.Errorf("%s: %s", method, envelope.Error.Message)
		}
		return fmt.Errorf("%s failed", method)
	}
	if out != nil && len(envelope.Result) > 0 {
		if errResult := json.Unmarshal(envelope.Result, out); errResult != nil {
			return fmt.Errorf("decode %s result: %w", method, errResult)
		}
	}
	return nil
}

type rpcHostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// logf writes to CPA's log. Fields must never contain API keys or proxy
// credentials.
func (s *Service) logf(level, message string, fields map[string]any) {
	_ = s.hostCall(pluginabi.MethodHostLog, rpcHostLogRequest{Level: level, Message: message, Fields: fields}, nil)
}

func okEnvelope(result any) []byte {
	rawResult, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return ErrorEnvelope("marshal_failed", errMarshal.Error())
	}
	raw, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: rawResult})
	return raw
}

// ErrorEnvelope builds a failure envelope for RPC and ABI initialization errors.
func ErrorEnvelope(code, message string) []byte {
	return errorEnvelopeFrom(&pluginabi.Error{Code: code, Message: message})
}
