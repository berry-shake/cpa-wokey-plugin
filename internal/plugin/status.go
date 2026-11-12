package plugin

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/catalog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/config"
	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/proxystore"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The status and models views of the management page.

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
	AuthIndex     string `json:"auth_index"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Label         string `json:"label"`
	KeyMasked     string `json:"key_masked"`
	Disabled      bool   `json:"disabled"`
	Unavailable   bool   `json:"unavailable"`
	Status        string `json:"status,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Priority      int    `json:"priority"`
	Note          string `json:"note,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`
	ProxyURL      string `json:"proxy_url,omitempty"`
	// ProxyID and ProxyName name the listed proxy whose URL is proxy_url.
	ProxyID          string   `json:"proxy_id,omitempty"`
	ProxyName        string   `json:"proxy_name,omitempty"`
	IssuerToken      string   `json:"issuer_token_masked,omitempty"`
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
		Catalog:     catalogSummary(s.catalog.Current()),
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
	snapshot := s.catalog.Current()
	proxies, _ := s.proxies.List()
	for _, entry := range entries {
		view.Credentials = append(view.Credentials, credentialViewOf(entry, cfg, snapshot, registered, proxies))
	}
	return view
}

func catalogSummary(snapshot catalog.Snapshot) catalogView {
	view := catalogView{Count: len(snapshot.Models), Revision: snapshot.Revision, LastError: snapshot.LastError}
	if !snapshot.FetchedAt.IsZero() {
		view.FetchedAt = snapshot.FetchedAt.UTC().Format(time.RFC3339)
	}
	if !snapshot.CheckedAt.IsZero() {
		view.CheckedAt = snapshot.CheckedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func credentialViewOf(entry wokeyAuth, cfg config.Config, snapshot catalog.Snapshot, registered map[string][]string, proxies proxystore.List) credentialView {
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
		view.RegisteredModels = distinctModels(models, entry.cred.EffectivePrefix(cfg))
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
	view.KeyMasked = credential.MaskKey(cred.APIKey)
	view.BaseURL = cred.BaseURL
	view.ProxyURL = redactProxy(cred.ProxyURL)
	if p, ok := proxies.Match(cred.ProxyURL); ok {
		view.ProxyID, view.ProxyName = p.ID, p.Name
	}
	if cred.IssuerToken != "" {
		view.IssuerToken = credential.MaskKey(cred.IssuerToken)
	}
	view.Prefix = cred.Prefix
	view.EffectivePrefix = cred.EffectivePrefix(cfg)
	view.AllModels = cred.AllModels
	if priority := jsonfield.Int(entry.fields, credential.KeyPriority); priority != 0 {
		view.Priority = priority
	}
	if note := jsonfield.String(entry.fields, credential.KeyNote); note != "" {
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
func missingModels(cred credential.Credential, snapshot catalog.Snapshot) []string {
	if cred.AllModels || snapshot.Empty() {
		return nil
	}
	var out []string
	for _, id := range cred.Models {
		if _, ok := snapshot.Lookup(id); !ok {
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
	value, ok := catalog.PricePerMillion(pricing[key])
	if !ok {
		return nil
	}
	return &value
}

func modelsViewOf(snapshot catalog.Snapshot) modelsView {
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
	snapshot := s.catalog.Current()
	if snapshot.Empty() {
		var errRefresh error
		snapshot, errRefresh = s.refreshCatalog(false)
		if errRefresh != nil && snapshot.Empty() {
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
