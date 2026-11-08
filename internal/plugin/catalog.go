package plugin

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

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// maxCatalogBytes bounds the /v1/models response (the real one is ~20 KB).
const maxCatalogBytes = 8 << 20

// upstreamModel is one entry of Wokey's public GET /v1/models catalog.
type upstreamModel struct {
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

type upstreamModelList struct {
	Object string          `json:"object"`
	Data   []upstreamModel `json:"data"`
}

// catalogSnapshot is an immutable view of the cached catalog.
type catalogSnapshot struct {
	Models    []upstreamModel
	byID      map[string]upstreamModel
	Revision  string
	ETag      string
	FetchedAt time.Time
	CheckedAt time.Time
	LastError string
}

func (c catalogSnapshot) empty() bool { return len(c.Models) == 0 }

func (c catalogSnapshot) lookup(id string) (upstreamModel, bool) {
	model, ok := c.byID[id]
	return model, ok
}

// catalog caches Wokey's model list. Refreshes use If-None-Match so an
// unchanged catalog costs a 304.
type catalog struct {
	mu       sync.RWMutex
	snapshot catalogSnapshot
	// fetchMu serializes upstream fetches so concurrent model.for_auth calls
	// during startup share one request.
	fetchMu sync.Mutex
}

func newCatalog() *catalog {
	return &catalog{snapshot: catalogSnapshot{byID: map[string]upstreamModel{}}}
}

func (c *catalog) current() catalogSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// fetch loads the catalog through client. With force it ignores the cached
// ETag. It returns the new snapshot and whether the model set changed.
func (c *catalog) fetch(ctx context.Context, client *http.Client, baseURL, userAgent string, force bool, now time.Time) (catalogSnapshot, bool, error) {
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	previous := c.current()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if errReq != nil {
		return previous, false, errReq
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if !force && previous.ETag != "" && !previous.empty() {
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
		return c.recordError(previous, now, fmt.Errorf("GET /v1/models returned %d: %s", resp.StatusCode, truncate(string(body), 300)))
	}
	var list upstreamModelList
	if errUnmarshal := json.Unmarshal(body, &list); errUnmarshal != nil {
		return c.recordError(previous, now, fmt.Errorf("decode /v1/models: %w", errUnmarshal))
	}
	models := make([]upstreamModel, 0, len(list.Data))
	byID := make(map[string]upstreamModel, len(list.Data))
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
	next := catalogSnapshot{
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

func (c *catalog) recordError(previous catalogSnapshot, now time.Time, err error) (catalogSnapshot, bool, error) {
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
func catalogRevision(models []upstreamModel) string {
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

// modelInfosForCredential builds the CPA model list for one credential: every
// catalog model, or the selected subset. Selected IDs missing from the catalog
// are still registered (with minimal metadata) so a catalog outage or a
// renamed model does not silently drop routes the user picked. IDs are the
// bare upstream IDs; CPA adds the auth's prefix itself.
func modelInfosForCredential(snapshot catalogSnapshot, cred credential) []pluginapi.ModelInfo {
	var selected []upstreamModel
	if cred.AllModels {
		selected = snapshot.Models
	} else {
		selected = make([]upstreamModel, 0, len(cred.Models))
		for _, id := range cred.Models {
			if model, ok := snapshot.lookup(id); ok {
				selected = append(selected, model)
				continue
			}
			selected = append(selected, upstreamModel{ID: id, Object: "model", OwnedBy: ProviderID})
		}
	}
	out := make([]pluginapi.ModelInfo, 0, len(selected))
	for _, model := range selected {
		out = append(out, toModelInfo(model))
	}
	return out
}

func toModelInfo(model upstreamModel) pluginapi.ModelInfo {
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

// upstreamModelID maps the model CPA hands the executor back to Wokey's ID.
// CPA strips the auth prefix before calling the executor; the prefix is
// still removed here in case a model arrives with it.
func upstreamModelID(model, prefix string, snapshot catalogSnapshot) string {
	model = stripThinkingSuffix(strings.TrimSpace(model))
	if prefix != "" && strings.HasPrefix(model, prefix+"/") {
		return strings.TrimPrefix(model, prefix+"/")
	}
	if _, ok := snapshot.lookup(model); ok {
		return model
	}
	// Wokey IDs contain no slash; a prefix we do not know (for example from a
	// changed config) is dropped rather than sent upstream.
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

// stripThinkingSuffix removes a trailing CPA reasoning suffix such as
// "(high)" or "(8192)". CPA normally strips it before calling the executor.
func stripThinkingSuffix(model string) string {
	if !strings.HasSuffix(model, ")") {
		return model
	}
	open := strings.LastIndex(model, "(")
	if open <= 0 {
		return model
	}
	return strings.TrimSpace(model[:open])
}

// pricePerMillion converts Wokey's per-token decimal string to USD per 1M tokens.
func pricePerMillion(perToken string) (float64, bool) {
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

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
