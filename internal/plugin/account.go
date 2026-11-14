package plugin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const accountCallTimeout = 20 * time.Second

type rpcQuotaFetchRequest struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// Wokey account queries (balance, key usage, issuing balance and the checks
// made when a key is saved) go out one at a time per egress: a query waits
// for the one ahead of it on the same proxy, or the same direct line, to
// finish. Keys on different egresses are queried in parallel.

// accountTurn resolves the egress for a credential's proxy and waits for
// its turn, giving up when ctx ends. release must be called when done.
func (s *Service) accountTurn(ctx context.Context, proxyURL string) (client *http.Client, release func(), perr *pluginabi.Error) {
	setting, errProxy := wokey.ResolveEgress(proxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, nil, wokey.RequestError("invalid_proxy", errProxy.Error())
	}
	release, errWait := s.accountQueue.Acquire(ctx, setting)
	if errWait != nil {
		return nil, nil, wokey.TransportError(fmt.Errorf("waiting for the egress: %w", errWait))
	}
	return s.clients.Client(setting), release, nil
}

// accountGET checks one account endpoint with a credential, in its egress's turn.
func (s *Service) accountGET(ctx context.Context, cred credential.Credential, path string) (map[string]any, *pluginabi.Error) {
	client, release, perr := s.accountTurn(ctx, cred.ProxyURL)
	if perr != nil {
		return nil, perr
	}
	defer release()
	return wokey.GetJSON(ctx, client, cred.EffectiveBaseURL(s.config())+path, cred.APIKey, s.userAgent(), path)
}

// issuerBalance calls GET /api/issuer/v1/balance with the credential's
// issuing token, in its egress's turn. Wokey also settles voided gift cards
// whose cooldown has ended on this call.
func (s *Service) issuerBalance(ctx context.Context, cred credential.Credential) (*wokey.IssuerInfo, *pluginabi.Error) {
	client, release, perr := s.accountTurn(ctx, cred.ProxyURL)
	if perr != nil {
		return nil, perr
	}
	defer release()
	return s.issuerBalanceWith(ctx, client, cred)
}

func (s *Service) issuerBalanceWith(ctx context.Context, client *http.Client, cred credential.Credential) (*wokey.IssuerInfo, *pluginabi.Error) {
	if !wokey.ValidIssuerToken(cred.IssuerToken) {
		return nil, wokey.RequestError("invalid_issuer_token", "wokey: issuer_token must start with "+wokey.IssuerTokenPrefix)
	}
	raw, perr := wokey.GetJSON(ctx, client, s.issuerBase+wokey.EndpointIssuerBalance, cred.IssuerToken, s.userAgent(), wokey.EndpointIssuerBalance)
	if perr != nil {
		return nil, perr
	}
	return wokey.IssuerInfoFrom(raw, credential.Fingerprint(cred.IssuerToken)), nil
}

// fetchAccount loads balance and key usage, plus the issuing balance when
// withIssuer is set and the key has an issuing token. The calls run one
// after another in a single turn of the key's egress, each with its own
// timeout; time spent waiting for the turn does not count. A failure of
// one call is reported next to the others' results; the error is returned
// only when both balance and key usage fail.
func (s *Service) fetchAccount(cred credential.Credential, withIssuer bool) (wokey.AccountInfo, *pluginabi.Error) {
	client, release, perr := s.accountTurn(s.ctx, cred.ProxyURL)
	if perr != nil {
		return wokey.AccountInfo{FetchedAt: s.now().UTC().Format(time.RFC3339)}, perr
	}
	defer release()
	get := func(path string) (map[string]any, *pluginabi.Error) {
		ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
		defer cancel()
		return wokey.GetJSON(ctx, client, cred.EffectiveBaseURL(s.config())+path, cred.APIKey, s.userAgent(), path)
	}
	balanceRaw, balanceErr := get(wokey.EndpointBalance)
	keyRaw, keyErrIn := get(wokey.EndpointKeyInfo)
	var (
		issuer    *wokey.IssuerInfo
		issuerErr *pluginabi.Error
	)
	if withIssuer && cred.IssuerToken != "" {
		ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
		issuer, issuerErr = s.issuerBalanceWith(ctx, client, cred)
		cancel()
	}

	info := wokey.AccountInfo{FetchedAt: s.now().UTC().Format(time.RFC3339), Issuer: issuer}
	if issuerErr != nil {
		info.IssuerError = issuerErr.Message
	}
	if balanceErr != nil {
		info.BalanceError = balanceErr.Message
	} else {
		info.Balance = wokey.BalanceFrom(balanceRaw)
	}
	if keyErrIn != nil {
		info.KeyError = keyErrIn.Message
	} else {
		info.Key = wokey.KeyInfoFrom(keyRaw)
	}
	if balanceErr != nil && keyErrIn != nil {
		// Prefer the key endpoint's status: it reflects the key itself.
		return info, keyErrIn
	}
	return info, nil
}

// quotaFetch does not call rememberHost: CPA sends quota requests without the
// host summary, and an empty one would wipe the cached global proxy.
func (s *Service) quotaFetch(req pluginapi.QuotaFetchRequest) []byte {
	cred, _, errCred := credential.FromMaps(req.StorageJSON, req.Metadata)
	if errCred != nil || cred.APIKey == "" {
		// Fall back to the auth file on disk when CPA sends no storage.
		if fileCred, ok := s.credentialByAuthIndex(req.AuthIndex); ok {
			cred = fileCred
		}
	}
	if cred.APIKey == "" {
		return errorEnvelopeFrom(wokey.AuthError("wokey: credential has no api_key"))
	}
	// CPA's quota panel shows the balance against this month's spend, so it
	// skips the issuing balance.
	info, perr := s.fetchAccount(cred, false)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	// The bar needs both figures; say which one is missing instead of
	// showing an empty card.
	switch {
	case info.Balance == nil:
		return errorEnvelopeFrom(&pluginabi.Error{Code: "balance_unavailable", Message: "wokey: read balance: " + orUnknown(info.BalanceError), HTTPStatus: http.StatusBadGateway})
	case info.Key == nil:
		return errorEnvelopeFrom(&pluginabi.Error{Code: "usage_unavailable", Message: "wokey: read key usage: " + orUnknown(info.KeyError), HTTPStatus: http.StatusBadGateway})
	}
	return okEnvelope(wokey.QuotaFromAccount(info))
}

func orUnknown(message string) string {
	if strings.TrimSpace(message) == "" {
		return "no answer"
	}
	return message
}
