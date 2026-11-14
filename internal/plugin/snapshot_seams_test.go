package plugin

import (
	"net/url"
	"testing"
	"time"
)

// Snapshot seams: the only place the RPC snapshot tests reach into the
// Service's unexported state. Everything else drives the plugin through
// Service.Call and the host callbacks.

// snapshotProviderID is the auth provider the fake host parses files for.
const snapshotProviderID = ProviderID

// snapshotConcurrentPaths are the upstream routes the plugin calls
// concurrently; Wokey has none.
var snapshotConcurrentPaths []string

// snapshotIssuerPath is where the issuing API lives on the scripted
// upstream.
const snapshotIssuerPath = "/api/issuer/v1"

// snapshotIdleTimeout bounds the wait for stream pumps after a step.
const snapshotIdleTimeout = 10 * time.Second

// seamRefreshTickMethod is the pseudo RPC method a step uses to run one
// iteration of the background catalog loop synchronously.
const seamRefreshTickMethod = "seam.refresh_tick"

// snapshotNewService builds the service the way main.go does, then pins the
// clock, keeps plugin.register from starting the catalog refresh loop and
// points the egress check at the fake upstream. The issuing API is not
// configurable; it is put on the same fake upstream as the egress check.
func snapshotNewService(version string, host HostCaller, egressCheckURLs []string) *Service {
	svc := New(version, host)
	svc.now = func() time.Time { return snapshotClock }
	svc.loopOnce.Do(func() {})
	svc.egressCheckURLs = append([]string(nil), egressCheckURLs...)
	if len(egressCheckURLs) > 0 {
		if upstream, errParse := url.Parse(egressCheckURLs[0]); errParse == nil {
			svc.issuerBase = upstream.Scheme + "://" + upstream.Host + snapshotIssuerPath
		}
	}
	return svc
}

// snapshotCall sends one step's RPC. The pseudo-method seam.refresh_tick
// runs what one background loop iteration runs (a catalog refresh honouring
// the cached ETag, then re-registration of stale auth files) and reports it
// as an RPC envelope.
func snapshotCall(svc *Service, method string, request []byte) []byte {
	if method != seamRefreshTickMethod {
		return svc.Call(method, request)
	}
	snapshot, errRefresh := svc.refreshCatalog(false)
	if errRefresh != nil {
		return ErrorEnvelope("refresh_failed", errRefresh.Error())
	}
	return okEnvelope(map[string]any{"revision": snapshot.Revision, "models": len(snapshot.Models)})
}

// snapshotDropQueuedRefresh discards a queued catalog refresh without
// running it.
func snapshotDropQueuedRefresh(svc *Service) {
	select {
	case <-svc.kick:
	default:
	}
}

// snapshotRunQueuedRefresh runs, synchronously, the refresh the background
// loop would run for a queued kick, and reports whether one was queued.
func snapshotRunQueuedRefresh(svc *Service) bool {
	select {
	case <-svc.kick:
		_, _ = svc.refreshCatalog(false)
		return true
	default:
		return false
	}
}

// snapshotWaitIdle blocks until every background stream pump has closed its
// host stream and written its request log entry.
func snapshotWaitIdle(svc *Service) {
	if !svc.streams.waitTimeout(snapshotIdleTimeout) {
		panic("snapshot: stream pumps did not finish within " + snapshotIdleTimeout.String())
	}
}

// snapshotSkeletons are the generated inputs of the cases.
func snapshotSkeletons(t *testing.T) []snapshotFixture {
	var out []snapshotFixture
	for _, c := range snapshotCases() {
		out = append(out, c.fixture(t))
	}
	return out
}
