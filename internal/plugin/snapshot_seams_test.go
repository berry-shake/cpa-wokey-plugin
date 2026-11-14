package plugin

import "time"

// Every access the RPC snapshot runner (snapshot_test.go) makes to the
// plugin's unexported internals lives in this file, so the runner can be
// pointed at a refactored plugin by rewriting only these functions.

// snapshotClock is the plugin clock during snapshot runs: status "now",
// catalog fetched_at/checked_at, auth NextRefreshAfter, quota fetched_at and
// egress check checked_at all read it.
var snapshotClock = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

// seamRefreshTickMethod is the pseudo RPC method a snapshot step uses to run
// one iteration of the background catalog loop synchronously.
const seamRefreshTickMethod = "seam.refresh_tick"

// seamPrepare wires a fresh service for a snapshot run, before the first RPC:
//   - pins the plugin clock;
//   - points the issuing API (not configurable) and the egress check lookups
//     (public IP services) at the scripted upstream;
//   - keeps the background catalog loop from starting, so plugin.register
//     does not fetch the catalog at a random moment; steps drive the loop
//     with seamRefreshTickMethod instead.
func seamPrepare(svc *Service, upstream string) {
	svc.now = func() time.Time { return snapshotClock }
	svc.issuerBase = upstream + testIssuerPath
	svc.egressCheckURLs = []string{upstream + "/ipinfo"}
	svc.loopOnce.Do(func() {})
}

// seamRefreshTick runs what one background loop iteration runs (a catalog
// refresh honouring the cached ETag, then re-registration of stale auth
// files) and reports it as an RPC envelope.
func seamRefreshTick(svc *Service) []byte {
	snapshot, errRefresh := svc.refreshCatalog(false)
	if errRefresh != nil {
		return ErrorEnvelope("refresh_failed", errRefresh.Error())
	}
	return okEnvelope(map[string]any{"revision": snapshot.Revision, "models": len(snapshot.Models)})
}

// seamWaitBackground waits until every stream pump has finished: emitted
// its chunks, closed the host stream and written its request log row.
func seamWaitBackground(svc *Service, timeout time.Duration) bool {
	return svc.streams.waitTimeout(timeout)
}
