package wokey_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/berry-shake/cpa-provider-core/core"
	"github.com/berry-shake/cpa-provider-core/coretest"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// TestSnapshots replays testdata/snapshots through the plugin (see
// cpa-provider-core's coretest package and README).
//
//	go test ./internal/wokey -run TestSnapshots           # compare
//	go test ./internal/wokey -run TestSnapshots -update   # rewrite outputs
func TestSnapshots(t *testing.T) {
	coretest.RunSnapshots(t, coretest.Binding{
		ProviderID: wokey.ProviderID,
		NewService: func(version string, host func(string, []byte) ([]byte, error), upstreamURL string) coretest.Plugin {
			// The issuing API is put on the scripted upstream, next to the
			// egress check.
			return seamPlugin{core.NewWithOptions(wokey.NewWithIssuer(upstreamURL+snapshotIssuerPath), version, host, core.Options{
				Now:             func() time.Time { return coretest.Clock },
				ManualRefresh:   true,
				EgressCheckURLs: []string{upstreamURL + "/json"},
			})}
		},
		Cases: func(t *testing.T) []coretest.Fixture {
			var out []coretest.Fixture
			for _, c := range snapshotCases() {
				out = append(out, c.fixture(t))
			}
			return out
		},
	})
}

// snapshotIssuerPath is where the issuing API lives on the scripted
// upstream.
const snapshotIssuerPath = "/api/issuer/v1"

// snapshotIdleTimeout bounds the wait for stream pumps after a step.
const snapshotIdleTimeout = 10 * time.Second

// seamRefreshTickMethod is the pseudo RPC method a step uses to run one
// iteration of the background catalog loop synchronously.
const seamRefreshTickMethod = "seam.refresh_tick"

// seamPlugin serves seam.refresh_tick and bounds the wait for stream pumps.
type seamPlugin struct {
	*core.Service
}

// Call sends one step's RPC. The pseudo-method seam.refresh_tick runs what
// one background loop iteration runs (a catalog refresh honouring the cached
// ETag, then re-registration of stale auth files) and reports it as an RPC
// envelope.
func (p seamPlugin) Call(method string, request []byte) []byte {
	if method != seamRefreshTickMethod {
		return p.Service.Call(method, request)
	}
	snapshot, errRefresh := p.RefreshTick()
	if errRefresh != nil {
		return core.ErrorEnvelope("refresh_failed", errRefresh.Error())
	}
	summary := snapshot.Summary()
	result, _ := json.Marshal(map[string]any{"revision": summary.Revision, "models": summary.Count})
	raw, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	return raw
}

// WaitIdle blocks until every background stream pump has closed its host
// stream and written its request log entry.
func (p seamPlugin) WaitIdle() {
	if !p.WaitIdleFor(snapshotIdleTimeout) {
		panic("snapshot: stream pumps did not finish within " + snapshotIdleTimeout.String())
	}
}

// The case table (snapshot_cases_test.go) predates coretest; these names
// keep it as it was written.
type (
	snapshotFixture     = coretest.Fixture
	snapshotSetup       = coretest.Setup
	snapshotCredential  = coretest.Credential
	snapshotStep        = coretest.Step
	snapshotRPC         = coretest.RPC
	snapshotReply       = coretest.Reply
	cpaLifecycleRequest = coretest.LifecycleRequest
)

var snapshotCaseJSON = coretest.CaseJSON
