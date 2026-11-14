package wokey

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-provider-core/config"
	"github.com/berry-shake/cpa-provider-core/credential"
	"github.com/berry-shake/cpa-provider-core/provider"
	"github.com/berry-shake/cpa-provider-core/upstream"
)

// The core runs these hooks (its service tests cover the plumbing with a
// Wokey-shaped test provider); these tests pin Wokey's side of them.

const (
	testGoodKey      = "sk-test-good-0000000000"
	testIssuerPath   = "/api/issuer/v1"
	testIssuerToken2 = "wkt_test_p2p_00000000"
)

// fakeAccountAPI serves Wokey's balance, key and issuing balance endpoints.
type fakeAccountAPI struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	fail  map[string]int
}

func newFakeAccountAPI(t *testing.T) *fakeAccountAPI {
	t.Helper()
	f := &fakeAccountAPI{fail: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		status := f.fail[r.URL.Path]
		f.mu.Unlock()
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		reply := func(status int, body string) {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(body))
		}
		switch {
		case status != 0:
			reply(status, `{"error":{"code":"forbidden","message":"dashboard scope required"}}`)
		case r.URL.Path == EndpointBalance && token == testGoodKey:
			reply(http.StatusOK, `{"userId":"1234","availableUsd":42.5,"reservedUsd":"0.75"}`)
		case r.URL.Path == EndpointKeyInfo && token == testGoodKey:
			reply(http.StatusOK, `{"label":"test key","usage":1.5,"usage_monthly":1.5,"limit":10,"limit_remaining":8.5,"limit_reset":"monthly"}`)
		case r.URL.Path == testIssuerPath+EndpointIssuerBalance && token == testIssuerToken:
			reply(http.StatusOK, `{"provider":{"availableUsd":"9.56639200"},"referrer":{"availableUsd":"0.00000000"}}`)
		case r.URL.Path == EndpointBalance || r.URL.Path == EndpointKeyInfo:
			reply(http.StatusUnauthorized, `{"error":{"code":"invalid_api_key","message":"invalid API key"}}`)
		default:
			reply(http.StatusUnauthorized, `{"error":{"code":"invalid_issuer_token","message":"invalid_issuer_token","type":"invalid_request_error"}}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAccountAPI) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// testEnv is the Env the core hands the adapter, pointed at f.
func testEnv(a *Adapter, f *fakeAccountAPI) provider.Env {
	cfg := config.Default(a.Spec().Config)
	cfg.BaseURL = f.URL
	return provider.Env{
		Context:   context.Background(),
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		UserAgent: "cpa-wokey-plugin/test",
		Config:    cfg,
		Errors:    upstream.Errors{Prefix: "wokey: ", Remap: a.RemapError},
	}
}

func credWith(key, issuerToken string) credential.Credential {
	fields := map[string]any{"type": ProviderID, "api_key": key}
	if issuerToken != "" {
		fields[KeyIssuerToken] = issuerToken
	}
	return credential.FromMap(fields)
}

func TestSpecKnobs(t *testing.T) {
	a := New()
	spec := a.Spec()
	if len(spec.ForwardHeaders) != 1 || spec.ForwardHeaders[0] != "X-End-User-Id" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Config.Database != "wokey.db" || spec.Config.DataDir != "plugins/wokey-data" {
		t.Fatalf("config profile = %+v", spec.Config)
	}
	// Count_tokens is refused by the core (no TokenCounter).
	if _, counts := any(a).(provider.TokenCounter); counts {
		t.Fatal("Wokey must not forward count_tokens")
	}
	// Wokey's account answers are not cached.
	if key := a.AccountKey(provider.Env{}, credWith(testGoodKey, "")); key != "" {
		t.Fatalf("AccountKey = %q", key)
	}
}

// CPA's quota panel gets balance and key usage only; the management page
// also gets the issuing balance of keys with an issuing token.
func TestFetchAccountAndView(t *testing.T) {
	f := newFakeAccountAPI(t)
	a := NewWithIssuer(f.URL + testIssuerPath)
	env := testEnv(a, f)
	cred := credWith(testGoodKey, testIssuerToken)

	info, complete, perr := a.FetchAccount(env, f.Client(), cred)
	account := info.(AccountInfo)
	if perr != nil || !complete || account.Issuer != nil || account.Balance == nil || account.Balance.Account != "user:1234" || account.Key == nil || *account.Key.LimitRemaining != 8.5 {
		t.Fatalf("quota account = %+v %v %+v", account, complete, perr)
	}
	if calls := f.calls(); strings.Join(calls, ",") != EndpointBalance+","+EndpointKeyInfo {
		t.Fatalf("quota calls = %v", calls)
	}
	info, _, perr = a.FetchAccountView(env, f.Client(), cred)
	account = info.(AccountInfo)
	if perr != nil || account.Issuer == nil || account.Issuer.ProviderUSD == nil || *account.Issuer.ProviderUSD != 9.566392 || account.Issuer.Account != credential.Fingerprint(testIssuerToken) {
		t.Fatalf("view account = %+v %+v", account.Issuer, perr)
	}
	if account.FetchedAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("fetched_at = %q", account.FetchedAt)
	}
	// A rejected issuing token is reported next to the balance.
	info, _, perr = a.FetchAccountView(env, f.Client(), credWith(testGoodKey, testIssuerToken2))
	if account = info.(AccountInfo); perr != nil || account.Issuer != nil || !strings.Contains(account.IssuerError, "invalid_issuer_token") || account.Balance == nil {
		t.Fatalf("bad issuer = %+v %+v", account, perr)
	}
}

func TestFetchAccountFailures(t *testing.T) {
	f := newFakeAccountAPI(t)
	a := NewWithIssuer(f.URL + testIssuerPath)
	env := testEnv(a, f)
	f.fail[EndpointBalance] = http.StatusForbidden
	info, complete, perr := a.FetchAccount(env, f.Client(), credWith(testGoodKey, ""))
	account := info.(AccountInfo)
	if perr != nil || complete || account.Balance != nil || !strings.Contains(account.BalanceError, "dashboard scope required") || account.Key == nil {
		t.Fatalf("one failing endpoint = %+v %v %+v", account, complete, perr)
	}
	if qerr := a.QuotaError(account); qerr == nil || qerr.Code != "balance_unavailable" || qerr.HTTPStatus != http.StatusBadGateway ||
		qerr.Message != "wokey: read balance: wokey: dashboard scope required" {
		t.Fatalf("quota error = %+v", qerr)
	}
	// Both failing returns the key endpoint's error.
	_, _, perr = a.FetchAccount(env, f.Client(), credWith("sk-unknown-0000000", ""))
	if perr == nil || perr.HTTPStatus != http.StatusUnauthorized || perr.Code != "invalid_api_key" {
		t.Fatalf("both failing: %+v", perr)
	}
}

func TestQuotaError(t *testing.T) {
	a := New()
	if qerr := a.QuotaError(AccountInfo{Balance: &BalanceInfo{}}); qerr == nil || qerr.Code != "usage_unavailable" || qerr.Message != "wokey: read key usage: no answer" {
		t.Fatalf("no usage = %+v", qerr)
	}
	if qerr := a.QuotaError(AccountInfo{Balance: &BalanceInfo{}, Key: &KeyInfo{}}); qerr != nil {
		t.Fatalf("complete = %+v", qerr)
	}
}

func TestCredentialInput(t *testing.T) {
	a := New()
	// A type error reads as it did when issuer_token was one of the
	// plugin's own credential fields.
	if _, errDecode := a.DecodeCredentialInput([]byte(`{"issuer_token":5}`)); errDecode == nil ||
		errDecode.Error() != "json: cannot unmarshal number into Go struct field credentialInput.issuer_token of type string" {
		t.Fatalf("decode error = %v", errDecode)
	}
	apply := func(body string, fields map[string]any) (map[string]any, *provider.InputError) {
		t.Helper()
		input, errDecode := a.DecodeCredentialInput([]byte(body))
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		return fields, a.ApplyCredentialInput(fields, input)
	}
	if _, ierr := apply(`{"issuer_token":"sk-not-an-issuer"}`, map[string]any{}); ierr == nil || ierr.Code != "invalid_issuer_token" ||
		ierr.Message != "issuer_token must be a Wokey issuing token starting with wkt_" {
		t.Fatalf("invalid token = %+v", ierr)
	}
	if fields, ierr := apply(`{"issuer_token":" `+testIssuerToken+` "}`, map[string]any{}); ierr != nil || fields[KeyIssuerToken] != testIssuerToken {
		t.Fatalf("set = %v %+v", fields, ierr)
	}
	if fields, ierr := apply(`{"issuer_token":""}`, map[string]any{KeyIssuerToken: testIssuerToken}); ierr != nil || fields[KeyIssuerToken] != nil {
		t.Fatalf("clear = %v %+v", fields, ierr)
	}
	if fields, ierr := apply(`{"label":"x"}`, map[string]any{KeyIssuerToken: testIssuerToken}); ierr != nil || fields[KeyIssuerToken] != testIssuerToken {
		t.Fatalf("absent = %v %+v", fields, ierr)
	}
}

func TestCheckCredential(t *testing.T) {
	f := newFakeAccountAPI(t)
	a := NewWithIssuer(f.URL + testIssuerPath)
	with, without := credWith(testGoodKey, testIssuerToken), credWith(testGoodKey, "")
	if a.CheckCredential(without, nil) != nil || a.CheckCredential(with, &with) != nil || a.CheckCredential(without, &with) != nil {
		t.Fatal("checks a key without a new issuing token")
	}
	check := a.CheckCredential(with, &without)
	if check == nil || check.Code != "issuer_token_verification_failed" {
		t.Fatalf("check = %+v", check)
	}
	if perr := check.Run(context.Background(), testEnv(a, f), f.Client()); perr != nil {
		t.Fatalf("good token: %+v", perr)
	}
	bad := a.CheckCredential(credWith(testGoodKey, testIssuerToken2), nil)
	if perr := bad.Run(context.Background(), testEnv(a, f), f.Client()); perr == nil || perr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("bad token: %+v", perr)
	}
	// A malformed token from an auth file is refused without a call.
	before := len(f.calls())
	malformed := a.CheckCredential(credWith(testGoodKey, "not-a-token"), nil)
	if perr := malformed.Run(context.Background(), testEnv(a, f), f.Client()); perr == nil || perr.Code != "invalid_issuer_token" || len(f.calls()) != before {
		t.Fatalf("malformed token: %+v", perr)
	}
}

func TestCredentialView(t *testing.T) {
	a := New()
	if members := a.CredentialView(credWith(testGoodKey, "")); members != nil {
		t.Fatalf("without token = %+v", members)
	}
	// issuer-token is read too.
	cred := credential.FromMap(map[string]any{"type": ProviderID, "api_key": testGoodKey, "issuer-token": testIssuerToken})
	members := a.CredentialView(cred)
	raw, _ := json.Marshal(members)
	if len(members) != 1 || members[0].Name != "issuer_token_masked" || members[0].Before != "prefix" || members[0].Value != credential.MaskKey(testIssuerToken) {
		t.Fatalf("members = %s", raw)
	}
}
