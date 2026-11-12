package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/berry-shake/cpa-wokey-plugin/internal/config"
	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Adding and editing Wokey credentials from the management page.

// credentialInput is the editable part of a Wokey auth file. Pointer and
// RawMessage fields distinguish "absent" from "clear".
type credentialInput struct {
	AuthIndex  string  `json:"auth_index"`
	Name       string  `json:"name"`
	APIKey     string  `json:"api_key"`
	SkipVerify bool    `json:"skip_verify"`
	Label      *string `json:"label"`
	Note       *string `json:"note"`
	BaseURL    *string `json:"base_url"`
	ProxyURL   *string `json:"proxy_url"`
	// ProxyID picks a listed proxy; it overrides ProxyURL.
	ProxyID *string `json:"proxy_id"`
	// IssuerToken sets the issuing token; "" removes it.
	IssuerToken *string         `json:"issuer_token"`
	Prefix      json.RawMessage `json:"prefix"`
	Models      json.RawMessage `json:"models"`
	Priority    json.RawMessage `json:"priority"`
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
		setOrDelete(fields, credential.KeyLabel, strings.TrimSpace(*input.Label))
	}
	if input.Note != nil {
		setOrDelete(fields, credential.KeyNote, strings.TrimSpace(*input.Note))
	}
	if input.BaseURL != nil {
		base := strings.TrimSpace(*input.BaseURL)
		if base != "" {
			normalized, errBase := config.NormalizeBaseURL(base)
			if errBase != nil {
				return &errorBody{Error: "invalid_base_url", Message: "base_url: " + errBase.Error()}
			}
			base = normalized
		}
		setOrDelete(fields, credential.KeyBaseURL, base)
	}
	if input.ProxyURL != nil {
		proxy := strings.TrimSpace(*input.ProxyURL)
		if errProxy := wokey.ValidateProxy(proxy); errProxy != nil {
			return &errorBody{Error: "invalid_proxy", Message: "proxy_url: " + errProxy.Error()}
		}
		setOrDelete(fields, credential.KeyProxyURL, proxy)
	}
	if input.IssuerToken != nil {
		token := strings.TrimSpace(*input.IssuerToken)
		if token != "" && !wokey.ValidIssuerToken(token) {
			return &errorBody{Error: "invalid_issuer_token", Message: "issuer_token must be a Wokey issuing token starting with " + wokey.IssuerTokenPrefix}
		}
		setOrDelete(fields, credential.KeyIssuerToken, token)
	}
	if len(input.Prefix) > 0 {
		if isJSONNull(input.Prefix) {
			delete(fields, credential.KeyPrefix)
		} else {
			var prefix string
			if errPrefix := json.Unmarshal(input.Prefix, &prefix); errPrefix != nil {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must be a string or null"}
			}
			prefix = config.NormalizePrefix(prefix)
			if strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
				return &errorBody{Error: "invalid_prefix", Message: "prefix must not contain spaces"}
			}
			fields[credential.KeyPrefix] = prefix
		}
	}
	if len(input.Models) > 0 {
		if isJSONNull(input.Models) {
			delete(fields, credential.KeyModels)
		} else {
			var models []string
			if errModels := json.Unmarshal(input.Models, &models); errModels != nil {
				return &errorBody{Error: "invalid_models", Message: "models must be an array of model IDs or null for all models"}
			}
			models = credential.NormalizeModels(models)
			for _, id := range models {
				if strings.IndexFunc(id, unicode.IsSpace) >= 0 {
					return &errorBody{Error: "invalid_models", Message: "model IDs must not contain spaces"}
				}
			}
			fields[credential.KeyModels] = models
		}
	}
	if len(input.Priority) > 0 {
		if isJSONNull(input.Priority) {
			delete(fields, credential.KeyPriority)
		} else {
			var priority int
			if errPriority := json.Unmarshal(input.Priority, &priority); errPriority != nil {
				return &errorBody{Error: "invalid_priority", Message: "priority must be an integer or null"}
			}
			if priority == 0 {
				delete(fields, credential.KeyPriority)
			} else {
				fields[credential.KeyPriority] = priority
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
	if errProxy := s.resolveProxyID(&input); errProxy != nil {
		return jsonResponse(http.StatusBadRequest, *errProxy)
	}
	key := strings.TrimSpace(input.APIKey)
	if key == "" || strings.IndexFunc(key, unicode.IsSpace) >= 0 {
		return jsonResponse(http.StatusBadRequest, errorBody{Error: "invalid_api_key", Message: "api_key is required and must not contain spaces"})
	}
	entries, errList := s.listWokeyAuths()
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "host_unavailable", Message: errList.Error()})
	}
	name := credential.FileNameForKey(key)
	for _, entry := range entries {
		if entry.cred.APIKey == key || strings.EqualFold(credential.SaveName(entry.Name, entry.Path), name) {
			return jsonResponse(http.StatusConflict, errorBody{Error: "duplicate_key", Message: "this API key is already added as " + entry.Name})
		}
	}

	fields := map[string]any{credential.KeyType: ProviderID, credential.KeyAPIKey: key}
	if errApply := applyCredentialInput(fields, input); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	cred := credential.FromMap(fields)
	if !input.SkipVerify {
		ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
		_, perr := s.accountGET(ctx, cred, wokey.EndpointKeyInfo)
		cancel()
		if perr != nil {
			return jsonResponse(verifyStatus(perr), errorBody{Error: "key_verification_failed", Message: perr.Message})
		}
		if resp, failed := s.verifyIssuerToken(cred); failed {
			return resp
		}
	}
	// Load the catalog before saving so the file records the revision its
	// models are registered from and the background sync leaves it alone.
	snapshot := s.catalog.Current()
	if snapshot.Empty() {
		snapshot = s.loadCatalogFor(cred)
	}
	if snapshot.Revision != "" {
		fields[credential.KeyModelRevision] = snapshot.Revision
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
	if errProxy := s.resolveProxyID(&input); errProxy != nil {
		return jsonResponse(http.StatusBadRequest, *errProxy)
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
	name := credential.SaveName(entry.Name, entry.Path)
	if name == "" {
		return jsonResponse(http.StatusConflict, errorBody{Error: "not_file_backed", Message: "this credential has no auth file the plugin can edit"})
	}
	fields := cloneFields(entry.fields)
	if errApply := applyCredentialInput(fields, input); errApply != nil {
		return jsonResponse(http.StatusBadRequest, *errApply)
	}
	if updated := credential.FromMap(fields); !input.SkipVerify && updated.IssuerToken != entry.cred.IssuerToken {
		if resp, failed := s.verifyIssuerToken(updated); failed {
			return resp
		}
	}
	snapshot := s.catalog.Current()
	if snapshot.Revision != "" {
		fields[credential.KeyModelRevision] = snapshot.Revision
	}
	if errSave := s.saveAuthFile(name, fields); errSave != nil {
		return jsonResponse(http.StatusBadGateway, errorBody{Error: "save_failed", Message: errSave.Error()})
	}
	s.logf("info", "wokey: updated credential", map[string]any{"auth": name})
	return jsonResponse(http.StatusOK, credentialResult{OK: true, Credential: s.credentialViewByName(name), UnknownModels: missingModels(credential.FromMap(fields), snapshot)})
}

// verifyIssuerToken checks a newly set issuing token with one balance call.
// It passes when the credential has no token.
func (s *Service) verifyIssuerToken(cred credential.Credential) (pluginapi.ManagementResponse, bool) {
	if cred.IssuerToken == "" {
		return pluginapi.ManagementResponse{}, false
	}
	ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
	defer cancel()
	if _, perr := s.issuerBalance(ctx, cred); perr != nil {
		return jsonResponse(verifyStatus(perr), errorBody{Error: "issuer_token_verification_failed", Message: perr.Message}), true
	}
	return pluginapi.ManagementResponse{}, false
}

// verifyStatus reports a rejected Wokey token as 400 and anything else
// (unreachable, 5xx) as 502.
func verifyStatus(perr *pluginabi.Error) int {
	switch perr.HTTPStatus {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func findEntry(entries []wokeyAuth, authIndex, name string) (wokeyAuth, bool) {
	authIndex = strings.TrimSpace(authIndex)
	name = strings.TrimSpace(name)
	for _, entry := range entries {
		if authIndex != "" && entry.AuthIndex == authIndex {
			return entry, true
		}
		if name != "" && (entry.Name == name || credential.SaveName(entry.Name, entry.Path) == name) {
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
	proxies, _ := s.proxies.List()
	view := credentialViewOf(entry, s.config(), s.catalog.Current(), s.registeredModels(), proxies)
	return &view
}

func (s *Service) credentialQuota(authIndex string) pluginapi.ManagementResponse {
	cred, ok := s.credentialByAuthIndex(authIndex)
	if !ok {
		return jsonResponse(http.StatusNotFound, errorBody{Error: "credential_not_found", Message: "no Wokey credential matches auth_index"})
	}
	info, perr := s.fetchAccount(cred, true)
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
