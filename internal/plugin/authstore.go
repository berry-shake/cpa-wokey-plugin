package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// wokeyAuth is one Wokey credential known to CPA together with its auth file.
type wokeyAuth struct {
	pluginapi.HostAuthFileEntry
	fields map[string]any
	cred   credential
	// readError is set when the auth file could not be read; fields and cred
	// are then empty.
	readError string
}

type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

func isWokeyEntry(entry pluginapi.HostAuthFileEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.Provider), ProviderID) ||
		strings.EqualFold(strings.TrimSpace(entry.Type), ProviderID)
}

// listWokeyAuths lists Wokey credentials with their auth file contents.
func (s *Service) listWokeyAuths() ([]wokeyAuth, error) {
	var resp rpcHostAuthListResponse
	if errList := s.hostCall(pluginabi.MethodHostAuthList, struct{}{}, &resp); errList != nil {
		return nil, errList
	}
	out := make([]wokeyAuth, 0, len(resp.Files))
	for _, entry := range resp.Files {
		if !isWokeyEntry(entry) || entry.RuntimeOnly || strings.TrimSpace(entry.AuthIndex) == "" {
			continue
		}
		item := wokeyAuth{HostAuthFileEntry: entry}
		fields, _, errGet := s.authFile(entry.AuthIndex)
		if errGet != nil {
			item.readError = errGet.Error()
		} else {
			item.fields = fields
			item.cred = credentialFromMap(fields)
		}
		out = append(out, item)
	}
	return out, nil
}

// authFile reads an auth file through the host and returns its fields and the
// file name host.auth.save expects.
func (s *Service) authFile(authIndex string) (map[string]any, string, error) {
	var resp pluginapi.HostAuthGetResponse
	if errGet := s.hostCall(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex}, &resp); errGet != nil {
		return nil, "", errGet
	}
	fields, errDecode := decodeAuthFields(resp.JSON)
	if errDecode != nil {
		return nil, "", errDecode
	}
	name := authSaveName(resp.Name, resp.Path)
	if name == "" {
		return nil, "", fmt.Errorf("auth file name unavailable for auth_index %s", authIndex)
	}
	return fields, name, nil
}

// decodeAuthFields keeps numbers as json.Number so re-saving a file does not
// change values the plugin does not own.
func decodeAuthFields(raw []byte) (map[string]any, error) {
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

// authSaveName derives the bare file name host.auth.save accepts.
func authSaveName(name, path string) string {
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

// saveAuthFile writes an auth file through the host, which re-parses it and
// re-registers the credential's models.
func (s *Service) saveAuthFile(name string, fields map[string]any) error {
	raw, errMarshal := json.MarshalIndent(fields, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	return s.hostCall(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: raw}, nil)
}

// credentialByAuthIndex loads a Wokey credential from its auth file.
func (s *Service) credentialByAuthIndex(authIndex string) (credential, bool) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return credential{}, false
	}
	fields, _, errGet := s.authFile(authIndex)
	if errGet != nil || !isWokeyAuthJSON(fields) {
		return credential{}, false
	}
	cred := credentialFromMap(fields)
	return cred, cred.APIKey != ""
}

// registeredModels returns the model IDs CPA registered per auth index.
func (s *Service) registeredModels() map[string][]string {
	var resp pluginapi.HostAuthModelsResponse
	if errModels := s.hostCall(pluginabi.MethodHostAuthModels, pluginapi.HostAuthModelsRequest{}, &resp); errModels != nil {
		return nil
	}
	out := make(map[string][]string, len(resp.Credentials))
	for _, entry := range resp.Credentials {
		if strings.EqualFold(entry.Provider, ProviderID) {
			out[entry.AuthIndex] = entry.Models
		}
	}
	return out
}
