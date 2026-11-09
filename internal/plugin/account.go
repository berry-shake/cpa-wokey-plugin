package plugin

import (
	"context"
	"sync"
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

// accountGET calls an authenticated Wokey account endpoint with a credential.
func (s *Service) accountGET(ctx context.Context, cred credential.Credential, path string) (map[string]any, *pluginabi.Error) {
	return s.getJSON(ctx, cred.ProxyURL, cred.EffectiveBaseURL(s.config())+path, cred.APIKey, path)
}

// getJSON GETs a JSON object from Wokey with a bearer token through the
// credential's egress. path names the endpoint in errors.
func (s *Service) getJSON(ctx context.Context, proxyURL, target, token, path string) (map[string]any, *pluginabi.Error) {
	setting, errProxy := wokey.ResolveEgress(proxyURL, s.globalProxy())
	if errProxy != nil {
		return nil, wokey.RequestError("invalid_proxy", errProxy.Error())
	}
	return wokey.GetJSON(ctx, s.clients.Client(setting), target, token, s.userAgent(), path)
}

// fetchAccount loads balance and key usage in parallel, plus the issuing
// balance when withIssuer is set and the key has an issuing token. A failure
// of one call is reported next to the others' results; the error is returned
// only when both balance and key usage fail.
func (s *Service) fetchAccount(cred credential.Credential, withIssuer bool) (wokey.AccountInfo, *pluginabi.Error) {
	ctx, cancel := context.WithTimeout(s.ctx, accountCallTimeout)
	defer cancel()
	var (
		wg                   sync.WaitGroup
		balanceRaw, keyRaw   map[string]any
		balanceErr, keyErrIn *pluginabi.Error
		issuer               *wokey.IssuerInfo
		issuerErr            *pluginabi.Error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		balanceRaw, balanceErr = s.accountGET(ctx, cred, wokey.EndpointBalance)
	}()
	go func() {
		defer wg.Done()
		keyRaw, keyErrIn = s.accountGET(ctx, cred, wokey.EndpointKeyInfo)
	}()
	if withIssuer && cred.IssuerToken != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			issuer, issuerErr = s.issuerBalance(ctx, cred)
		}()
	}
	wg.Wait()

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

// issuerBalance calls GET /api/issuer/v1/balance with the credential's issuing
// token through the credential's egress. Wokey also settles voided gift cards
// whose cooldown has ended on this call.
func (s *Service) issuerBalance(ctx context.Context, cred credential.Credential) (*wokey.IssuerInfo, *pluginabi.Error) {
	if !wokey.ValidIssuerToken(cred.IssuerToken) {
		return nil, wokey.RequestError("invalid_issuer_token", "wokey: issuer_token must start with "+wokey.IssuerTokenPrefix)
	}
	raw, perr := s.getJSON(ctx, cred.ProxyURL, s.issuerBase+wokey.EndpointIssuerBalance, cred.IssuerToken, wokey.EndpointIssuerBalance)
	if perr != nil {
		return nil, perr
	}
	return wokey.IssuerInfoFrom(raw, credential.Fingerprint(cred.IssuerToken)), nil
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
	// CPA's quota panel shows spend only, so it skips the issuing balance.
	info, perr := s.fetchAccount(cred, false)
	if perr != nil {
		return errorEnvelopeFrom(perr)
	}
	return okEnvelope(wokey.QuotaFromAccount(info))
}
