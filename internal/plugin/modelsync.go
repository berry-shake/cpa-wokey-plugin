package plugin

import (
	"context"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/catalog"
	"github.com/berry-shake/cpa-wokey-plugin/internal/credential"
	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
)

// loadCatalogFor fetches the catalog synchronously through the credential's
// own egress, so a key that needs a proxy can still discover models.
func (s *Service) loadCatalogFor(cred credential.Credential) catalog.Snapshot {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	setting, errProxy := wokey.ResolveEgress(cred.ProxyURL, s.globalProxy())
	if errProxy != nil {
		return s.catalog.Current()
	}
	snapshot, _, errFetch := s.catalog.Fetch(ctx, s.clients.Client(setting), cred.EffectiveBaseURL(s.config()), s.userAgent(), false, s.now())
	if errFetch != nil {
		s.logf("warn", "wokey: model catalog unavailable", map[string]any{"error": errFetch.Error()})
	}
	return snapshot
}

func (s *Service) kickRefresh() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// refreshLoop polls the catalog and pushes changes to CPA.
func (s *Service) refreshLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.kick:
		case <-timer.C:
		}
		s.refreshCatalog(false)
		if interval := s.config().ModelsRefreshInterval; interval > 0 {
			timer.Reset(interval)
		}
	}
}

// refreshCatalog fetches the catalog (trying the global egress first, then
// each distinct credential proxy) and re-registers credentials whose models
// were built from an older catalog.
func (s *Service) refreshCatalog(force bool) (catalog.Snapshot, error) {
	ctx, cancel := context.WithTimeout(s.ctx, catalogLoadTimeout)
	defer cancel()
	entries, _ := s.listWokeyAuths()
	proxies := []string{""}
	seen := map[string]bool{"": true}
	for _, entry := range entries {
		if entry.readError == "" && !seen[entry.cred.ProxyURL] {
			seen[entry.cred.ProxyURL] = true
			proxies = append(proxies, entry.cred.ProxyURL)
		}
	}
	var (
		snapshot catalog.Snapshot
		errFetch error
	)
	for _, proxy := range proxies {
		setting, errProxy := wokey.ResolveEgress(proxy, s.globalProxy())
		if errProxy != nil {
			continue
		}
		snapshot, _, errFetch = s.catalog.Fetch(ctx, s.clients.Client(setting), s.config().BaseURL, s.userAgent(), force, s.now())
		if errFetch == nil {
			break
		}
	}
	if errFetch != nil {
		s.logf("warn", "wokey: model catalog refresh failed", map[string]any{"error": errFetch.Error()})
		return snapshot, errFetch
	}
	s.syncRegistrations(snapshot, entries)
	return snapshot, nil
}

// syncRegistrations re-saves auth files whose registered models predate the
// current catalog. Saving makes CPA re-parse the file and call model.for_auth
// again. model_revision records the catalog a file was last synced with; the
// in-memory registered map catches enabled credentials whose models were built
// from an older catalog. Disabled credentials are not registered by CPA, so
// only their file revision is checked and they are touched once per change.
func (s *Service) syncRegistrations(snapshot catalog.Snapshot, entries []wokeyAuth) {
	if snapshot.Revision == "" {
		return
	}
	for _, entry := range entries {
		if entry.readError != "" || !credential.IsWokey(entry.fields) {
			continue
		}
		stale := entry.cred.ModelRevision != snapshot.Revision
		if !entry.Disabled {
			if registered, known := s.registered.Load(entry.ID); known && registered != snapshot.Revision {
				stale = true
			}
		}
		if !stale {
			continue
		}
		name := credential.SaveName(entry.Name, entry.Path)
		if name == "" {
			continue
		}
		fields := cloneFields(entry.fields)
		fields[credential.KeyModelRevision] = snapshot.Revision
		if errSave := s.saveAuthFile(name, fields); errSave != nil {
			s.logf("warn", "wokey: model sync save failed", map[string]any{"auth": name, "error": errSave.Error()})
			continue
		}
		s.logf("info", "wokey: refreshed registered models", map[string]any{"auth": name, "revision": snapshot.Revision})
	}
}

func cloneFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		out[key] = value
	}
	return out
}
