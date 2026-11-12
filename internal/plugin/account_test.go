package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestFetchAccountPartialFailure(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.handle(wokey.EndpointBalance, func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusForbidden, map[string]any{"error": map[string]any{"code": "forbidden", "message": "dashboard scope required"}})
	})
	svc, _ := newTestService(t, upstream)

	info, perr := svc.fetchAccount(credential.Credential{APIKey: testGoodKey}, false)
	if perr != nil {
		t.Fatalf("one failing endpoint must not fail the call: %+v", perr)
	}
	if info.Balance != nil || !strings.Contains(info.BalanceError, "dashboard scope required") {
		t.Fatalf("balance = %+v %q", info.Balance, info.BalanceError)
	}
	if info.Key == nil || info.Key.Label != "test key" || info.Key.LimitUSD == nil || *info.Key.LimitUSD != 10 || info.Key.LimitResetLabel != "monthly" {
		t.Fatalf("key = %+v", info.Key)
	}

	// Both failing returns the key endpoint's error.
	_, perr = svc.fetchAccount(credential.Credential{APIKey: "sk-unknown-0000000"}, false)
	if perr == nil || perr.HTTPStatus != http.StatusUnauthorized || perr.Code != "invalid_api_key" {
		t.Fatalf("both failing: %+v", perr)
	}
}

func TestFetchAccountUsesCredentialEgress(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, _ := newTestService(t, upstream)
	_, perr := svc.fetchAccount(credential.Credential{APIKey: testGoodKey, ProxyURL: "ftp://bad"}, false)
	if perr == nil || perr.Code != "invalid_proxy" {
		t.Fatalf("bad proxy: %+v", perr)
	}
	// A per-key base URL wins over the plugin base URL.
	svc.cfg.BaseURL = "http://127.0.0.1:1"
	if _, perr = svc.fetchAccount(credential.Credential{APIKey: testGoodKey, BaseURL: upstream.URL}, false); perr != nil {
		t.Fatalf("base_url override: %+v", perr)
	}
}

func TestAccountQueriesQueuePerEgress(t *testing.T) {
	upstream := newFakeWokey(t)
	upstream.accountDelay = 20 * time.Millisecond
	svc, _ := newTestService(t, upstream)
	// Three keys on the same egress queried at once, two with issuing tokens.
	creds := []credential.Credential{
		{APIKey: testGoodKey, IssuerToken: testIssuerToken},
		{APIKey: testGoodKey, IssuerToken: testIssuerToken2},
		{APIKey: testGoodKey},
	}
	var wg sync.WaitGroup
	for _, cred := range creds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, perr := svc.fetchAccount(cred, true); perr != nil {
				t.Errorf("fetch: %+v", perr)
			}
		}()
	}
	wg.Wait()
	if calls, peak := upstream.accountLoad(); calls != 8 || peak != 1 {
		t.Fatalf("account calls = %d, at most %d at once; want 8 one at a time", calls, peak)
	}
}

func TestQuotaFetch(t *testing.T) {
	upstream := newFakeWokey(t)
	svc, host := newTestService(t, upstream)
	file := host.seed("wokey-q.json", ProviderID, map[string]any{"type": "wokey", "api_key": testGoodKey})

	call := func(req pluginapi.QuotaFetchRequest) pluginabi.Envelope {
		t.Helper()
		raw, _ := json.Marshal(rpcQuotaFetchRequest{QuotaFetchRequest: req})
		return decodeEnvelope(t, svc.Call(pluginabi.MethodQuotaFetch, raw))
	}

	envelope := call(pluginapi.QuotaFetchRequest{AuthIndex: file.index, StorageJSON: []byte(`{"type":"wokey","api_key":"` + testGoodKey + `"}`)})
	if !envelope.OK {
		t.Fatalf("quota: %+v", envelope.Error)
	}
	resp := mustDecode[pluginapi.QuotaFetchResponse](t, envelope.Result)
	if len(resp.Summary) != 0 || len(resp.Groups) != 2 || len(resp.Groups[0].Buckets) != 3 ||
		resp.Groups[0].Buckets[2].RemainingFraction != 42.5/44 || !strings.Contains(resp.Groups[0].Buckets[0].Description, "$0.25 spent") {
		t.Fatalf("quota = %+v", resp)
	}

	// Without storage the auth file on disk is used.
	if envelope = call(pluginapi.QuotaFetchRequest{AuthIndex: file.index}); !envelope.OK {
		t.Fatalf("file fallback: %+v", envelope.Error)
	}
	envelope = call(pluginapi.QuotaFetchRequest{AuthIndex: "idx-404"})
	if envelope.OK || envelope.Error.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("no credential: %+v", envelope)
	}
	envelope = call(pluginapi.QuotaFetchRequest{StorageJSON: []byte(`{"type":"wokey","api_key":"sk-unknown-0000000"}`)})
	if envelope.OK || envelope.Error.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("revoked key: %+v", envelope)
	}
}
