package wokey

// The catalog caches Wokey's public model catalog and turns it into CPA
// model registrations.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// maxCatalogBytes bounds the /v1/models response (the real one is ~20 KB).
const maxCatalogBytes = 8 << 20

// Model is one entry of Wokey's public GET /v1/models catalog.
type Model struct {
	ID                  string   `json:"id"`
	OwnedBy             string   `json:"owned_by"`
	Object              string   `json:"object"`
	Created             int64    `json:"created"`
	Name                string   `json:"name"`
	ContextLength       int64    `json:"context_length"`
	MaxCompletionTokens int64    `json:"max_completion_tokens"`
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        struct {
		Modality         string   `json:"modality"`
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	// Pricing values are USD per token, encoded as decimal strings.
	Pricing map[string]string `json:"pricing"`
}

type modelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Snapshot is an immutable view of the cached catalog.
type Snapshot struct {
	Models    []Model
	byID      map[string]Model
	Revision  string
	ETag      string
	FetchedAt time.Time
	CheckedAt time.Time
	LastError string
}

// Empty reports whether no catalog has been loaded.
func (c Snapshot) Empty() bool { return len(c.Models) == 0 }

// Summary describes the snapshot on the management page.
func (c Snapshot) Summary() provider.CatalogSummary {
	return provider.CatalogSummary{Count: len(c.Models), Revision: c.Revision, FetchedAt: c.FetchedAt, CheckedAt: c.CheckedAt, LastError: c.LastError}
}

// Has reports whether a model ID is in the catalog.
func (c Snapshot) Has(id string) bool {
	_, ok := c.byID[id]
	return ok
}

// Lookup finds a model by its upstream ID.
func (c Snapshot) Lookup(id string) (Model, bool) {
	model, ok := c.byID[id]
	return model, ok
}

// Catalog caches Wokey's model list. Refreshes use If-None-Match so an
// unchanged catalog costs a 304.
type Catalog struct {
	mu       sync.RWMutex
	snapshot Snapshot
	// fetchMu serializes upstream fetches so concurrent model.for_auth calls
	// during startup share one request.
	fetchMu sync.Mutex
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{snapshot: Snapshot{byID: map[string]Model{}}}
}

// Current returns the cached snapshot.
func (c *Catalog) Current() provider.Catalog {
	return c.current()
}

func (c *Catalog) current() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// Fetch loads the catalog through client. With force it ignores the cached
// ETag.
func (c *Catalog) Fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (provider.Catalog, error) {
	snapshot, _, errFetch := c.fetch(ctx, client, baseURL, userAgent, force, now)
	return snapshot, errFetch
}

// fetch is Fetch that also reports whether the model set changed.
func (c *Catalog) fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (Snapshot, bool, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	previous := c.current()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+EndpointModels, nil)
	if errReq != nil {
		return previous, false, errReq
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if !force && previous.ETag != "" && !previous.Empty() {
		req.Header.Set("If-None-Match", previous.ETag)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return c.recordError(previous, now, errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		c.mu.Lock()
		c.snapshot.CheckedAt = now
		c.snapshot.LastError = ""
		snapshot := c.snapshot
		c.mu.Unlock()
		return snapshot, false, nil
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes))
	if errRead != nil {
		return c.recordError(previous, now, errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return c.recordError(previous, now, fmt.Errorf("GET /v1/models returned %d: %s", resp.StatusCode, upstream.Truncate(string(body), 300)))
	}
	var list modelList
	if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
		return c.recordError(previous, now, fmt.Errorf("decode /v1/models: %w", errUnmarshal))
	}
	models := make([]Model, 0, len(list.Data))
	byID := make(map[string]Model, len(list.Data))
	for _, model := range list.Data {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		if _, exists := byID[model.ID]; exists {
			continue
		}
		byID[model.ID] = model
		models = append(models, model)
	}
	if len(models) == 0 {
		return c.recordError(previous, now, fmt.Errorf("/v1/models returned no models"))
	}
	sort.Slice(models, func(i, j int) bool { return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID) })
	next := Snapshot{
		Models:    models,
		byID:      byID,
		Revision:  catalogRevision(models),
		ETag:      strings.TrimSpace(resp.Header.Get("ETag")),
		FetchedAt: now,
		CheckedAt: now,
	}
	c.mu.Lock()
	c.snapshot = next
	c.mu.Unlock()
	return next, next.Revision != previous.Revision, nil
}

func (c *Catalog) recordError(previous Snapshot, now time.Time, err error) (Snapshot, bool, error) {
	c.mu.Lock()
	c.snapshot.CheckedAt = now
	c.snapshot.LastError = err.Error()
	snapshot := c.snapshot
	c.mu.Unlock()
	_ = previous
	return snapshot, false, err
}

// catalogRevision hashes the parts of the catalog that end up in CPA's model
// registry. Price changes do not alter it, so they never trigger re-registration.
func catalogRevision(models []Model) string {
	hash := sha256.New()
	for _, model := range models {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%s\x00%s\x00%s\n",
			model.ID, model.Name, model.ContextLength, model.MaxCompletionTokens,
			strings.Join(model.Architecture.InputModalities, ","),
			strings.Join(model.Architecture.OutputModalities, ","),
			strings.Join(model.SupportedParameters, ","))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

// ModelInfos builds the CPA model list for one credential: every catalog
// model when all is set, otherwise the selected IDs. Selected IDs missing
// from the catalog are still registered (with minimal metadata) so a catalog
// outage or a renamed model does not silently drop routes the user picked.
// IDs are the bare upstream IDs; CPA adds the auth's prefix itself.
func (c Snapshot) ModelInfos(all bool, ids []string) []pluginapi.ModelInfo {
	var selected []Model
	if all {
		selected = c.Models
	} else {
		selected = make([]Model, 0, len(ids))
		for _, id := range ids {
			if model, ok := c.Lookup(id); ok {
				selected = append(selected, model)
				continue
			}
			selected = append(selected, Model{ID: id, Object: "model", OwnedBy: ProviderID})
		}
	}
	out := make([]pluginapi.ModelInfo, 0, len(selected))
	for _, model := range selected {
		out = append(out, toModelInfo(model))
	}
	return out
}

func toModelInfo(model Model) pluginapi.ModelInfo {
	displayName := strings.TrimSpace(model.Name)
	if displayName == "" {
		displayName = model.ID
	}
	ownedBy := strings.TrimSpace(model.OwnedBy)
	if ownedBy == "" {
		ownedBy = ProviderID
	}
	return pluginapi.ModelInfo{
		ID:                        model.ID,
		Object:                    "model",
		Created:                   model.Created,
		OwnedBy:                   ownedBy,
		Type:                      ProviderID,
		DisplayName:               displayName,
		Name:                      model.ID,
		Description:               "Wokey · " + displayName,
		InputTokenLimit:           model.ContextLength,
		OutputTokenLimit:          model.MaxCompletionTokens,
		ContextLength:             model.ContextLength,
		MaxCompletionTokens:       model.MaxCompletionTokens,
		SupportedParameters:       append([]string(nil), model.SupportedParameters...),
		SupportedInputModalities:  append([]string(nil), model.Architecture.InputModalities...),
		SupportedOutputModalities: append([]string(nil), model.Architecture.OutputModalities...),
		// UserDefined keeps CPA from stripping reasoning settings for models
		// it has no thinking metadata for; Wokey validates them upstream.
		UserDefined: true,
	}
}

// UpstreamID maps the model CPA hands the executor back to Wokey's ID
// (provider.UpstreamID). Wokey IDs contain no slash, so a prefix we do not
// know (for example from a changed config) is dropped rather than sent
// upstream.
func (c Snapshot) UpstreamID(model, prefix string) string {
	return provider.UpstreamID(model, prefix, c.Has, provider.FlatIDs)
}

// PricePerMillion converts Wokey's per-token decimal string to USD per 1M tokens.
func PricePerMillion(perToken string) (float64, bool) {
	perToken = strings.TrimSpace(perToken)
	if perToken == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(perToken, 64)
	if errParse != nil {
		return 0, false
	}
	return value * 1_000_000, true
}

// modelView is one model of the management page's models view, with prices
// in USD per million tokens.
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

func priceField(pricing map[string]string, key string) *float64 {
	value, ok := PricePerMillion(pricing[key])
	if !ok {
		return nil
	}
	return &value
}

// ModelsView lists the catalog for the management page's models view.
func (c Snapshot) ModelsView() any {
	models := make([]modelView, 0, len(c.Models))
	for _, model := range c.Models {
		models = append(models, modelView{
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
	return models
}
