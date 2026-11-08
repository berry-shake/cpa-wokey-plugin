package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Auth file keys. They are snake_case because CPA canonicalizes credential
// metadata keys to snake_case when it persists an auth.
const (
	keyType          = "type"
	keyAPIKey        = "api_key"
	keyLabel         = "label"
	keyBaseURL       = "base_url"
	keyPrefix        = "prefix"
	keyModels        = "models"
	keyProxyURL      = "proxy_url"
	keyPriority      = "priority"
	keyDisabled      = "disabled"
	keyNote          = "note"
	keyModelRevision = "model_revision"
)

// refreshHorizon is how far ahead auth.parse and auth.refresh schedule the next
// refresh. API keys do not expire, so this only keeps CPA from polling.
const refreshHorizon = 365 * 24 * time.Hour

// credential is the view of one Wokey auth file the runtime needs.
type credential struct {
	APIKey  string
	Label   string
	BaseURL string
	// Prefix is CPA's standard auth file prefix. It is nil when the file has
	// no prefix field; the plugin config default (model-prefix) then applies.
	// CPA itself prepends it to the registered model IDs and strips it again
	// before calling the executor.
	Prefix *string
	// Models lists the selected upstream model IDs. AllModels is true when the
	// file omits models (or sets it to null), meaning every catalog model.
	Models        []string
	AllModels     bool
	ProxyURL      string
	ModelRevision string
}

// credentialFromMaps merges the persisted auth file with CPA's mutable auth
// metadata (metadata wins, as it does when CPA persists the auth) and extracts
// the fields the plugin uses.
func credentialFromMaps(storageJSON []byte, metadata map[string]any) (credential, map[string]any, error) {
	merged := make(map[string]any)
	if len(bytes.TrimSpace(storageJSON)) > 0 {
		if errUnmarshal := json.Unmarshal(storageJSON, &merged); errUnmarshal != nil {
			return credential{}, nil, fmt.Errorf("decode auth storage: %w", errUnmarshal)
		}
		if merged == nil {
			merged = make(map[string]any)
		}
	}
	for key, value := range metadata {
		merged[key] = value
	}
	return credentialFromMap(merged), merged, nil
}

func credentialFromMap(fields map[string]any) credential {
	cred := credential{
		APIKey:        stringField(fields, keyAPIKey, "api-key", "key"),
		Label:         stringField(fields, keyLabel),
		BaseURL:       stringField(fields, keyBaseURL, "base-url"),
		ProxyURL:      stringField(fields, keyProxyURL, "proxy-url"),
		ModelRevision: stringField(fields, keyModelRevision),
	}
	if raw, ok := fields[keyPrefix]; ok && raw != nil {
		if text, okText := raw.(string); okText {
			prefix := normalizePrefix(text)
			cred.Prefix = &prefix
		}
	}
	cred.Models, cred.AllModels = modelSelection(fields[keyModels])
	return cred
}

// modelSelection reads the models field. Absent, null or a non-array value
// means all catalog models; an array (even empty) is an explicit selection.
func modelSelection(raw any) ([]string, bool) {
	items, ok := raw.([]any)
	if !ok {
		if typed, okTyped := raw.([]string); okTyped {
			return normalizeModelList(typed), false
		}
		return nil, true
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, okText := item.(string); okText {
			out = append(out, text)
		}
	}
	return normalizeModelList(out), false
}

func normalizeModelList(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func stringField(fields map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := fields[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func boolField(fields map[string]any, key string) bool {
	switch value := fields[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func intField(fields map[string]any, key string) int {
	switch value := fields[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

// isWokeyAuthJSON reports whether an auth file belongs to this plugin.
func isWokeyAuthJSON(fields map[string]any) bool {
	return strings.EqualFold(stringField(fields, keyType), ProviderID)
}

// effectivePrefix returns the model prefix for a credential.
func (c credential) effectivePrefix(cfg Config) string {
	return c.prefixOr(cfg.ModelPrefix)
}

// prefixOr returns the file's own prefix, or defaultPrefix when it has none.
func (c credential) prefixOr(defaultPrefix string) string {
	if c.Prefix != nil {
		return *c.Prefix
	}
	return defaultPrefix
}

// effectiveBaseURL returns the upstream origin for a credential.
func (c credential) effectiveBaseURL(cfg Config) string {
	if c.BaseURL != "" {
		if normalized, errBase := normalizeBaseURL(c.BaseURL); errBase == nil {
			return normalized
		}
	}
	return cfg.BaseURL
}

// parseAuthFile turns a raw auth file into CPA auth data. Files whose type is
// not wokey are left to other providers. defaultPrefix is the plugin's
// model-prefix, used for files without their own prefix field.
func parseAuthFile(req pluginapi.AuthParseRequest, now time.Time, defaultPrefix string) (pluginapi.AuthParseResponse, error) {
	raw := bytes.TrimSpace(req.RawJSON)
	if len(raw) == 0 {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	var fields map[string]any
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil || fields == nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if !isWokeyAuthJSON(fields) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	auth, errAuth := authDataFromFields(fields, raw, req.FileName, now, defaultPrefix)
	if errAuth != nil {
		return pluginapi.AuthParseResponse{}, errAuth
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: auth}, nil
}

// authDataFromFields builds CPA auth data. Prefix carries the effective model
// prefix so CPA registers "<prefix>/<model>" with its native prefix handling;
// an empty value lets CPA fall back to the file's own prefix field, which is
// then empty as well.
func authDataFromFields(fields map[string]any, raw []byte, fileName string, now time.Time, defaultPrefix string) (pluginapi.AuthData, error) {
	cred := credentialFromMap(fields)
	if cred.APIKey == "" {
		return pluginapi.AuthData{}, fmt.Errorf("wokey auth file %s has no api_key", strings.TrimSpace(fileName))
	}
	metadata := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		metadata[key] = value
	}
	metadata[keyType] = ProviderID
	label := cred.Label
	if label == "" {
		label = maskKey(cred.APIKey)
	}
	return pluginapi.AuthData{
		Provider:         ProviderID,
		FileName:         strings.TrimSpace(fileName),
		Label:            label,
		Prefix:           cred.prefixOr(defaultPrefix),
		ProxyURL:         cred.ProxyURL,
		Disabled:         boolField(fields, keyDisabled),
		StorageJSON:      bytes.Clone(raw),
		Metadata:         metadata,
		NextRefreshAfter: now.Add(refreshHorizon),
	}, nil
}

// maskKey keeps enough of a key to tell keys apart without revealing it.
func maskKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 10 {
		if len(key) <= 2 {
			return "****"
		}
		return key[:2] + "****"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

// keyFingerprint is a short, stable, non-reversible identifier for a key. It
// names new auth files so the same key is never added twice.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])[:10]
}

func authFileNameForKey(key string) string {
	return ProviderID + "-" + keyFingerprint(key) + ".json"
}
