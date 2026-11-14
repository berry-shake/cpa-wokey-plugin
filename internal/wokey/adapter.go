package wokey

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/jsonfield"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-wokey-plugin/internal/web"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// KeyIssuerToken is the auth file field holding a key's issuing token
// (issuer-token is read too).
const KeyIssuerToken = "issuer_token"

// Adapter is the Wokey provider for cpa-provider-core.
type Adapter struct {
	catalog *Catalog
	// issuerBase is the issuing API root (DefaultIssuerBaseURL).
	issuerBase string
}

// New returns the Wokey adapter with an empty catalog.
func New() *Adapter {
	return NewWithIssuer(DefaultIssuerBaseURL)
}

// NewWithIssuer is New with another issuing API root; tests point it at a
// fake server.
func NewWithIssuer(issuerBase string) *Adapter {
	return &Adapter{catalog: NewCatalog(), issuerBase: issuerBase}
}

var (
	_ provider.Adapter             = (*Adapter)(nil)
	_ provider.ErrorRemapper       = (*Adapter)(nil)
	_ provider.CredentialExtension = (*Adapter)(nil)
	_ provider.AccountViewer       = (*Adapter)(nil)
	_ provider.QuotaChecker        = (*Adapter)(nil)
)

// Spec describes the plugin.
func (a *Adapter) Spec() provider.Spec {
	return provider.Spec{
		ID:          ProviderID,
		DisplayName: "Wokey",
		Author:      "berry-shake",
		Repository:  "https://github.com/berry-shake/cpa-wokey-plugin",
		Config: config.Profile{
			BaseURL:           "https://api.wokey.ai",
			ModelPrefix:       ProviderID,
			DataDir:           "plugins/wokey-data",
			Database:          "wokey.db",
			BaseURLSuffixes:   []string{"/v1"},
			BaseURLHelp:       "Wokey API origin. Auth files may override it with base_url.",
			ModelPrefixHelp:   `Default CPA prefix for Wokey keys ("wokey" -> wokey/claude-fable-5). Empty adds no prefix. An auth file's own prefix field wins.`,
			ModelsRefreshHelp: "How often to poll Wokey's model catalog; 0 disables polling.",
		},
		Page: web.Page,
		Logo: web.Logo,
		Texts: provider.Texts{
			LoginStart:          "Wokey uses API keys: add one on the Wokey plugin page or upload a wokey auth file",
			ModelsRoute:         "Wokey model catalog with prices per million tokens.",
			ModelsRefreshRoute:  "Reload the Wokey model catalog and re-register changed models.",
			AddCredentialRoute:  `Add a Wokey API key. Body: {"api_key","label","note","models","prefix","proxy_url"|"proxy_id","issuer_token"}.`,
			EditCredentialRoute: `Edit a Wokey credential. Body: {"auth_index","label","note","models","prefix","proxy_url"|"proxy_id","priority","issuer_token"}.`,
			QuotaRoute:          "Balance and key usage of one Wokey credential (?auth_index=), plus Provider earnings when it has an issuing token.",
		},
		// Wokey attributes requests to the client's end user by this header.
		ForwardHeaders: []string{"X-End-User-Id"},
		// Wokey's count_tokens rejects every request with a 400 ("metadata:
		// Extra inputs are not permitted", although the request carries no
		// metadata), so the client gets a quick 501 instead of a slow 400.
		CountTokensRefusal: "token counting is not supported",
	}
}

// Endpoint is where Wokey serves a protocol.
func (a *Adapter) Endpoint(protocol provider.Protocol) string {
	switch protocol {
	case provider.Responses:
		return EndpointResponses
	case provider.Messages:
		return EndpointMessages
	default:
		return EndpointChat
	}
}

// Catalog is Wokey's public model catalog.
func (a *Adapter) Catalog() provider.CatalogSource {
	return a.catalog
}

// VerifyKey checks a new key with one GET /v1/key.
func (a *Adapter) VerifyKey(ctx context.Context, env provider.Env, client *http.Client, cred credential.Credential) *pluginabi.Error {
	_, perr := env.Errors.GetJSON(ctx, client, env.BaseURL(cred)+EndpointKeyInfo, cred.APIKey, env.UserAgent, EndpointKeyInfo)
	return perr
}

// AccountKey is empty: Wokey's account answers are not cached, every quota
// request asks Wokey again.
func (a *Adapter) AccountKey(provider.Env, credential.Credential) string {
	return ""
}

// FetchAccount loads the balance and key usage behind a key for CPA's quota
// panel; it skips the issuing balance.
func (a *Adapter) FetchAccount(env provider.Env, client *http.Client, cred credential.Credential) (any, bool, *pluginabi.Error) {
	return a.fetchAccount(env, client, cred, false)
}

// FetchAccountView is FetchAccount for the management page, plus the
// issuing balance when the key has an issuing token.
func (a *Adapter) FetchAccountView(env provider.Env, client *http.Client, cred credential.Credential) (any, bool, *pluginabi.Error) {
	return a.fetchAccount(env, client, cred, true)
}

// fetchAccount loads balance and key usage, plus the issuing balance when
// withIssuer is set and the key has an issuing token. The calls run one
// after another in a single turn of the key's egress, each with its own
// timeout. A failure of one call is reported next to the others' results;
// the error is returned only when both balance and key usage fail.
func (a *Adapter) fetchAccount(env provider.Env, client *http.Client, cred credential.Credential, withIssuer bool) (AccountInfo, bool, *pluginabi.Error) {
	get := func(path string) (map[string]any, *pluginabi.Error) {
		ctx, cancel := context.WithTimeout(env.Context, provider.AccountCallTimeout)
		defer cancel()
		return env.Errors.GetJSON(ctx, client, env.BaseURL(cred)+path, cred.APIKey, env.UserAgent, path)
	}
	balanceRaw, balanceErr := get(EndpointBalance)
	keyRaw, keyErr := get(EndpointKeyInfo)
	var (
		issuer    *IssuerInfo
		issuerErr *pluginabi.Error
	)
	token := IssuerToken(cred)
	if withIssuer && token != "" {
		ctx, cancel := context.WithTimeout(env.Context, provider.AccountCallTimeout)
		issuer, issuerErr = a.issuerBalance(ctx, env, client, token)
		cancel()
	}

	info := AccountInfo{FetchedAt: env.Now().UTC().Format(time.RFC3339), Issuer: issuer}
	if issuerErr != nil {
		info.IssuerError = issuerErr.Message
	}
	if balanceErr != nil {
		info.BalanceError = balanceErr.Message
	} else {
		info.Balance = BalanceFrom(balanceRaw)
	}
	if keyErr != nil {
		info.KeyError = keyErr.Message
	} else {
		info.Key = KeyInfoFrom(keyRaw)
	}
	if balanceErr != nil && keyErr != nil {
		// Prefer the key endpoint's status: it reflects the key itself.
		return info, false, keyErr
	}
	return info, balanceErr == nil && keyErr == nil && issuerErr == nil, nil
}

// issuerBalance calls GET /api/issuer/v1/balance with an issuing token.
// Wokey also settles voided gift cards whose cooldown has ended on this call.
func (a *Adapter) issuerBalance(ctx context.Context, env provider.Env, client *http.Client, token string) (*IssuerInfo, *pluginabi.Error) {
	if !ValidIssuerToken(token) {
		return nil, &pluginabi.Error{Code: "invalid_issuer_token", Message: "wokey: issuer_token must start with " + IssuerTokenPrefix, HTTPStatus: http.StatusBadRequest}
	}
	raw, perr := env.Errors.GetJSON(ctx, client, a.issuerBase+EndpointIssuerBalance, token, env.UserAgent, EndpointIssuerBalance)
	if perr != nil {
		return nil, perr
	}
	return IssuerInfoFrom(raw, credential.Fingerprint(token)), nil
}

// QuotaError refuses an answer without the balance or the key usage: CPA's
// bar needs both, so say which one is missing instead of showing an empty
// card.
func (a *Adapter) QuotaError(info any) *pluginabi.Error {
	account, _ := info.(AccountInfo)
	switch {
	case account.Balance == nil:
		return &pluginabi.Error{Code: "balance_unavailable", Message: "wokey: read balance: " + orUnknown(account.BalanceError), HTTPStatus: http.StatusBadGateway}
	case account.Key == nil:
		return &pluginabi.Error{Code: "usage_unavailable", Message: "wokey: read key usage: " + orUnknown(account.KeyError), HTTPStatus: http.StatusBadGateway}
	}
	return nil
}

func orUnknown(message string) string {
	if strings.TrimSpace(message) == "" {
		return "no answer"
	}
	return message
}

// Quota turns an account answer into CPA's quota panel response.
func (a *Adapter) Quota(info any) pluginapi.QuotaFetchResponse {
	account, _ := info.(AccountInfo)
	return QuotaFromAccount(account)
}

// IssuerToken is a credential's issuing token, "" without one.
func IssuerToken(cred credential.Credential) string {
	return jsonfield.String(cred.Fields, KeyIssuerToken, "issuer-token")
}

// credentialInput is Wokey's part of a credential body. It is named like
// the core's, so a decode error reads the same as one of the core's fields.
type credentialInput struct {
	// IssuerToken sets the issuing token; "" removes it.
	IssuerToken *string `json:"issuer_token"`
}

// DecodeCredentialInput reads issuer_token from a credential body.
func (a *Adapter) DecodeCredentialInput(body []byte) (any, error) {
	var input credentialInput
	if errUnmarshal := json.Unmarshal(body, &input); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return input, nil
}

// ApplyCredentialInput validates and sets (or removes) the issuing token.
func (a *Adapter) ApplyCredentialInput(fields map[string]any, decoded any) *provider.InputError {
	input, _ := decoded.(credentialInput)
	if input.IssuerToken == nil {
		return nil
	}
	token := strings.TrimSpace(*input.IssuerToken)
	if token != "" && !ValidIssuerToken(token) {
		return &provider.InputError{Code: "invalid_issuer_token", Message: "issuer_token must be a Wokey issuing token starting with " + IssuerTokenPrefix}
	}
	if token == "" {
		delete(fields, KeyIssuerToken)
	} else {
		fields[KeyIssuerToken] = token
	}
	return nil
}

// CheckCredential checks a newly set issuing token with one balance call:
// on a new key that has one, and when an edit changes it to another one.
func (a *Adapter) CheckCredential(cred credential.Credential, previous *credential.Credential) *provider.CredentialCheck {
	token := IssuerToken(cred)
	if token == "" || (previous != nil && token == IssuerToken(*previous)) {
		return nil
	}
	return &provider.CredentialCheck{
		Code: "issuer_token_verification_failed",
		Run: func(ctx context.Context, env provider.Env, client *http.Client) *pluginabi.Error {
			_, perr := a.issuerBalance(ctx, env, client, token)
			return perr
		},
	}
}

// CredentialView shows the masked issuing token after the proxy.
func (a *Adapter) CredentialView(cred credential.Credential) []provider.ViewMember {
	token := IssuerToken(cred)
	if token == "" {
		return nil
	}
	return []provider.ViewMember{{Name: "issuer_token_masked", Value: credential.MaskKey(token), Before: "prefix"}}
}
