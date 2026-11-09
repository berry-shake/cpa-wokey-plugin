package plugin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// wokeyAuth is one Wokey credential known to CPA together with its auth file.
type wokeyAuth struct {
	pluginapi.HostAuthFileEntry
	fields map[string]any
	cred   credential.Credential
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
			item.cred = credential.FromMap(fields)
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
	fields, errDecode := credential.DecodeFields(resp.JSON)
	if errDecode != nil {
		return nil, "", errDecode
	}
	name := credential.SaveName(resp.Name, resp.Path)
	if name == "" {
		return nil, "", fmt.Errorf("auth file name unavailable for auth_index %s", authIndex)
	}
	return fields, name, nil
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
func (s *Service) credentialByAuthIndex(authIndex string) (credential.Credential, bool) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return credential.Credential{}, false
	}
	fields, _, errGet := s.authFile(authIndex)
	if errGet != nil || !credential.IsWokey(fields) {
		return credential.Credential{}, false
	}
	cred := credential.FromMap(fields)
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
