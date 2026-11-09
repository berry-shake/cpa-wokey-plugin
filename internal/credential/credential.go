// Package credential reads and builds Wokey auth files: one file per API key,
// {"type":"wokey","api_key":...}, plus the CPA auth data derived from it.
package credential

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/config"
	"github.com/berry-shake/cpa-wokey-plugin/internal/jsonfield"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Auth file keys. They are snake_case because CPA canonicalizes credential
// metadata keys to snake_case when it persists an auth.
const (
	KeyType          = "type"
	KeyAPIKey        = "api_key"
	KeyLabel         = "label"
	KeyBaseURL       = "base_url"
	KeyPrefix        = "prefix"
	KeyModels        = "models"
	KeyProxyURL      = "proxy_url"
	KeyPriority      = "priority"
	KeyDisabled      = "disabled"
	KeyNote          = "note"
	KeyModelRevision = "model_revision"
	KeyIssuerToken   = "issuer_token"
)

// refreshHorizon is how far ahead auth.parse and auth.refresh schedule the next
// refresh. API keys do not expire, so this only keeps CPA from polling.
const refreshHorizon = 365 * 24 * time.Hour

// Credential is the view of one Wokey auth file the runtime needs.
type Credential struct {
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
	// IssuerToken is an optional Wokey issuing token (wkt_…) of the key's
	// account. Only keys with one query the account's Provider earnings.
	IssuerToken string
}

// FromMaps merges the persisted auth file with CPA's mutable auth
// metadata (metadata wins, as it does when CPA persists the auth) and extracts
// the fields the plugin uses.
func FromMaps(storageJSON []byte, metadata map[string]any) (Credential, map[string]any, error) {
	merged := make(map[string]any)
	if len(bytes.TrimSpace(storageJSON)) > 0 {
		if errUnmarshal := json.Unmarshal(storageJSON, &merged); errUnmarshal != nil {
			return Credential{}, nil, fmt.Errorf("decode auth storage: %w", errUnmarshal)
		}
		if merged == nil {
			merged = make(map[string]any)
		}
	}
	for key, value := range metadata {
		merged[key] = value
	}
	return FromMap(merged), merged, nil
}

// FromMap extracts the credential from an auth file's fields.
func FromMap(fields map[string]any) Credential {
	cred := Credential{
		APIKey:        jsonfield.String(fields, KeyAPIKey, "api-key", "key"),
		Label:         jsonfield.String(fields, KeyLabel),
		BaseURL:       jsonfield.String(fields, KeyBaseURL, "base-url"),
		ProxyURL:      jsonfield.String(fields, KeyProxyURL, "proxy-url"),
		ModelRevision: jsonfield.String(fields, KeyModelRevision),
		IssuerToken:   jsonfield.String(fields, KeyIssuerToken, "issuer-token"),
	}
	if raw, ok := fields[KeyPrefix]; ok && raw != nil {
		if text, okText := raw.(string); okText {
			prefix := config.NormalizePrefix(text)
			cred.Prefix = &prefix
		}
	}
	cred.Models, cred.AllModels = modelSelection(fields[KeyModels])
	return cred
}

// modelSelection reads the models field. Absent, null or a non-array value
// means all catalog models; an array (even empty) is an explicit selection.
func modelSelection(raw any) ([]string, bool) {
	items, ok := raw.([]any)
	if !ok {
		if typed, okTyped := raw.([]string); okTyped {
			return NormalizeModels(typed), false
		}
		return nil, true
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, okText := item.(string); okText {
			out = append(out, text)
		}
	}
	return NormalizeModels(out), false
}

// NormalizeModels trims, de-duplicates and sorts model IDs.
func NormalizeModels(models []string) []string {
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

// IsWokey reports whether an auth file belongs to this plugin.
func IsWokey(fields map[string]any) bool {
	return strings.EqualFold(jsonfield.String(fields, KeyType), wokey.ProviderID)
}

// EffectivePrefix returns the model prefix for a credential.
func (c Credential) EffectivePrefix(cfg config.Config) string {
	return c.prefixOr(cfg.ModelPrefix)
}

// prefixOr returns the file's own prefix, or defaultPrefix when it has none.
func (c Credential) prefixOr(defaultPrefix string) string {
	if c.Prefix != nil {
		return *c.Prefix
	}
	return defaultPrefix
}

// EffectiveBaseURL returns the upstream origin for a credential.
func (c Credential) EffectiveBaseURL(cfg config.Config) string {
	if c.BaseURL != "" {
		if normalized, errBase := config.NormalizeBaseURL(c.BaseURL); errBase == nil {
			return normalized
		}
	}
	return cfg.BaseURL
}

// ParseAuthFile turns a raw auth file into CPA auth data. Files whose type is
// not wokey are left to other providers. defaultPrefix is the plugin's
// model-prefix, used for files without their own prefix field.
func ParseAuthFile(req pluginapi.AuthParseRequest, now time.Time, defaultPrefix string) (pluginapi.AuthParseResponse, error) {
	raw := bytes.TrimSpace(req.RawJSON)
	if len(raw) == 0 {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	var fields map[string]any
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil || fields == nil {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	if !IsWokey(fields) {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	auth, errAuth := AuthData(fields, raw, req.FileName, now, defaultPrefix)
	if errAuth != nil {
		return pluginapi.AuthParseResponse{}, errAuth
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: auth}, nil
}

// AuthData builds CPA auth data. Prefix carries the effective model
// prefix so CPA registers "<prefix>/<model>" with its native prefix handling;
// an empty value lets CPA fall back to the file's own prefix field, which is
// then empty as well.
func AuthData(fields map[string]any, raw []byte, fileName string, now time.Time, defaultPrefix string) (pluginapi.AuthData, error) {
	cred := FromMap(fields)
	if cred.APIKey == "" {
		return pluginapi.AuthData{}, fmt.Errorf("wokey auth file %s has no api_key", strings.TrimSpace(fileName))
	}
	metadata := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		metadata[key] = value
	}
	metadata[KeyType] = wokey.ProviderID
	label := cred.Label
	if label == "" {
		label = fileStem(fileName)
	}
	if label == "" {
		label = MaskKey(cred.APIKey)
	}
	return pluginapi.AuthData{
		Provider:         wokey.ProviderID,
		FileName:         strings.TrimSpace(fileName),
		Label:            label,
		Prefix:           cred.prefixOr(defaultPrefix),
		ProxyURL:         cred.ProxyURL,
		Disabled:         jsonfield.Bool(fields, KeyDisabled),
		StorageJSON:      bytes.Clone(raw),
		Metadata:         metadata,
		NextRefreshAfter: now.Add(refreshHorizon),
	}, nil
}

// MaskKey keeps enough of a key to tell keys apart without revealing it.
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 10 {
		if len(key) <= 2 {
			return "****"
		}
		return key[:2] + "****"
	}
	return key[:6] + "…" + key[len(key)-4:]
}

// Fingerprint is a short, stable, non-reversible identifier for a key. It
// names new auth files so the same key is never added twice.
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])[:10]
}

// FileNameForKey names the auth file for a new key.
func FileNameForKey(key string) string {
	return wokey.ProviderID + "-" + Fingerprint(key) + ".json"
}

// fileStem is an auth file's name without directory and extension
// ("wokey-ab12.json" -> "wokey-ab12"). It is a credential's default label.
func fileStem(fileName string) string {
	name := strings.TrimSpace(fileName)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, path.Ext(name))
}

// DecodeFields decodes an auth file and keeps numbers as json.Number, so
// re-saving a file does not change values the plugin does not own.
func DecodeFields(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]any
	if errDecode := dec.Decode(&fields); errDecode != nil {
		return nil, fmt.Errorf("decode auth file: %w", errDecode)
	}
	if fields == nil {
		return nil, fmt.Errorf("auth file is not a JSON object")
	}
	return fields, nil
}

// SaveName derives the bare file name host.auth.save accepts from an auth
// file's name or path.
func SaveName(name, path string) string {
	for _, candidate := range []string{name, path} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		base := filepath.Base(strings.ReplaceAll(candidate, "\\", "/"))
		if strings.HasSuffix(strings.ToLower(base), ".json") {
			return base
		}
	}
	return ""
}
