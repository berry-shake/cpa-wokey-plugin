package catalog

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/berry-shake/cpa-wokey-plugin/internal/wokey"
)

// fakeModels serves GET /v1/models with an ETag, like Wokey.
type fakeModels struct {
	*httptest.Server

	mu           sync.Mutex
	models       []map[string]any
	etag         string
	full, notMod int
	override     http.HandlerFunc
}

func newFakeModels(t *testing.T) *fakeModels {
	t.Helper()
	f := &fakeModels{
		models: []map[string]any{
			{"id": "model-a", "name": "Model A", "owned_by": "lab-a", "context_length": 200000, "max_completion_tokens": 64000,
				"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
				"pricing":      map[string]any{"prompt": "0.000003", "completion": "0.000015"}},
			{"id": "model-b", "name": "Model B", "owned_by": "lab-b", "context_length": 128000},
		},
		etag: `"v1"`,
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeModels) serve(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	override := f.override
	f.mu.Unlock()
	if override != nil {
		override(rw, r)
		return
	}
	if r.URL.Path != "/v1/models" {
		writeJSON(rw, http.StatusNotFound, map[string]any{"error": "no route " + r.URL.Path})
		return
	}
	f.mu.Lock()
	etag, models := f.etag, f.models
	if r.Header.Get("If-None-Match") == etag {
		f.notMod++
		f.mu.Unlock()
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	f.full++
	f.mu.Unlock()
	rw.Header().Set("ETag", etag)
	writeJSON(rw, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (f *fakeModels) setModels(models []map[string]any, etag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models, f.etag = models, etag
}

func (f *fakeModels) setOverride(handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override = handler
}

// counts returns how many full and 304 responses were served.
func (f *fakeModels) counts() (full, notModified int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.full, f.notMod
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func fetchTestCatalog(t *testing.T, upstream *fakeModels) (*Catalog, Snapshot) {
	t.Helper()
	c := New()
	snapshot, changed, errFetch := c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || !changed {
		t.Fatalf("fetch: changed=%v err=%v", changed, errFetch)
	}
	return c, snapshot
}

func TestCatalogFetchUsesETag(t *testing.T) {
	upstream := newFakeModels(t)
	c, first := fetchTestCatalog(t, upstream)
	if len(first.Models) != 2 || first.Revision == "" || first.ETag != `"v1"` {
		t.Fatalf("first snapshot = %+v", first)
	}

	second, changed, errFetch := c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || changed || second.Revision != first.Revision {
		t.Fatalf("304 refresh: changed=%v err=%v", changed, errFetch)
	}
	if full, notModified := upstream.counts(); full != 1 || notModified != 1 {
		t.Fatalf("full=%d notModified=%d", full, notModified)
	}

	// force ignores the ETag; an identical catalog keeps its revision.
	_, changed, errFetch = c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now())
	if full, _ := upstream.counts(); errFetch != nil || changed || full != 2 {
		t.Fatalf("forced refresh: changed=%v err=%v full=%d", changed, errFetch, full)
	}
}

func TestCatalogRevisionIgnoresPrices(t *testing.T) {
	upstream := newFakeModels(t)
	c, first := fetchTestCatalog(t, upstream)

	models := []map[string]any{
		{"id": "model-a", "name": "Model A", "owned_by": "lab-a", "context_length": 200000, "max_completion_tokens": 64000,
			"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
			"pricing":      map[string]any{"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "model-b", "name": "Model B", "owned_by": "lab-b", "context_length": 128000},
	}
	upstream.setModels(models, `"v2"`)
	priced, _, errFetch := c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || priced.Revision != first.Revision {
		t.Fatalf("price change altered the revision: %v %s vs %s", errFetch, priced.Revision, first.Revision)
	}

	upstream.setModels(append(models, map[string]any{"id": "model-c"}), `"v3"`)
	grown, changed, errFetch := c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || !changed || grown.Revision == first.Revision || len(grown.Models) != 3 {
		t.Fatalf("new model not picked up: changed=%v err=%v", changed, errFetch)
	}
}

func TestCatalogFetchErrorKeepsPreviousModels(t *testing.T) {
	upstream := newFakeModels(t)
	c, first := fetchTestCatalog(t, upstream)
	upstream.setOverride(func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusBadGateway, map[string]any{"error": "down"})
	})
	snapshot, _, errFetch := c.Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now())
	if errFetch == nil {
		t.Fatal("expected an error")
	}
	if snapshot.Revision != first.Revision || len(snapshot.Models) != 2 || snapshot.LastError == "" {
		t.Fatalf("snapshot after error = %+v", snapshot)
	}

	upstream.setOverride(func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"data": []any{}})
	})
	if _, _, errFetch = New().Fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now()); errFetch == nil {
		t.Fatal("an empty catalog must be an error")
	}
}

func TestModelInfos(t *testing.T) {
	upstream := newFakeModels(t)
	_, snapshot := fetchTestCatalog(t, upstream)

	all := snapshot.ModelInfos(true, nil)
	if len(all) != 2 || all[0].ID != "model-a" || all[1].ID != "model-b" {
		t.Fatalf("all models = %+v", all)
	}
	a := all[0]
	if a.Name != "model-a" || a.DisplayName != "Model A" || a.ContextLength != 200000 || a.OutputTokenLimit != 64000 || !a.UserDefined || a.Type != wokey.ProviderID {
		t.Fatalf("model info = %+v", a)
	}
	if !reflect.DeepEqual(a.SupportedInputModalities, []string{"text", "image"}) {
		t.Fatalf("modalities = %v", a.SupportedInputModalities)
	}

	picked := snapshot.ModelInfos(false, []string{"ghost", "model-b"})
	if len(picked) != 2 || picked[0].ID != "ghost" || picked[1].ID != "model-b" || picked[0].OwnedBy != wokey.ProviderID {
		t.Fatalf("selection = %+v", picked)
	}

	if got := (Snapshot{}).ModelInfos(true, nil); len(got) != 0 {
		t.Fatalf("empty catalog registered %d models", len(got))
	}
}

func TestUpstreamID(t *testing.T) {
	upstream := newFakeModels(t)
	_, snapshot := fetchTestCatalog(t, upstream)
	cases := []struct {
		model, prefix, want string
	}{
		{"wokey/model-a", "wokey", "model-a"},
		{"model-a", "wokey", "model-a"},
		{"model-a", "", "model-a"},
		{"team/model-b", "team", "model-b"},
		{"old/model-a", "wokey", "model-a"},
		{"wokey/model-a(high)", "wokey", "model-a"},
		{"unknown", "wokey", "unknown"},
	}
	for _, tc := range cases {
		if got := snapshot.UpstreamID(tc.model, tc.prefix); got != tc.want {
			t.Errorf("UpstreamID(%q, %q) = %q, want %q", tc.model, tc.prefix, got, tc.want)
		}
	}
}

func TestPricePerMillion(t *testing.T) {
	if got, ok := PricePerMillion("0.000003"); !ok || math.Abs(got-3) > 1e-9 {
		t.Fatalf("price = %v %v", got, ok)
	}
	for _, bad := range []string{"", " ", "free"} {
		if _, ok := PricePerMillion(bad); ok {
			t.Errorf("PricePerMillion(%q) accepted", bad)
		}
	}
}
