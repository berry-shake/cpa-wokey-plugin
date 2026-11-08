package plugin

import (
	"context"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func fetchTestCatalog(t *testing.T, upstream *fakeWokey) (*catalog, catalogSnapshot) {
	t.Helper()
	c := newCatalog()
	snapshot, changed, errFetch := c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || !changed {
		t.Fatalf("fetch: changed=%v err=%v", changed, errFetch)
	}
	return c, snapshot
}

func TestCatalogFetchUsesETag(t *testing.T) {
	upstream := newFakeWokey(t)
	c, first := fetchTestCatalog(t, upstream)
	if len(first.Models) != 2 || first.Revision == "" || first.ETag != `"v1"` {
		t.Fatalf("first snapshot = %+v", first)
	}

	second, changed, errFetch := c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || changed || second.Revision != first.Revision {
		t.Fatalf("304 refresh: changed=%v err=%v", changed, errFetch)
	}
	if full, notModified := upstream.catalogCounts(); full != 1 || notModified != 1 {
		t.Fatalf("full=%d notModified=%d", full, notModified)
	}

	// force ignores the ETag; an identical catalog keeps its revision.
	_, changed, errFetch = c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now())
	if full, _ := upstream.catalogCounts(); errFetch != nil || changed || full != 2 {
		t.Fatalf("forced refresh: changed=%v err=%v full=%d", changed, errFetch, full)
	}
}

func TestCatalogRevisionIgnoresPrices(t *testing.T) {
	upstream := newFakeWokey(t)
	c, first := fetchTestCatalog(t, upstream)

	models := []map[string]any{
		{"id": "model-a", "name": "Model A", "owned_by": "lab-a", "context_length": 200000, "max_completion_tokens": 64000,
			"architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
			"pricing":      map[string]any{"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "model-b", "name": "Model B", "owned_by": "lab-b", "context_length": 128000},
	}
	upstream.setModels(models, `"v2"`)
	priced, _, errFetch := c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || priced.Revision != first.Revision {
		t.Fatalf("price change altered the revision: %v %s vs %s", errFetch, priced.Revision, first.Revision)
	}

	upstream.setModels(append(models, map[string]any{"id": "model-c"}), `"v3"`)
	grown, changed, errFetch := c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", false, time.Now())
	if errFetch != nil || !changed || grown.Revision == first.Revision || len(grown.Models) != 3 {
		t.Fatalf("new model not picked up: changed=%v err=%v", changed, errFetch)
	}
}

func TestCatalogFetchErrorKeepsPreviousModels(t *testing.T) {
	upstream := newFakeWokey(t)
	c, first := fetchTestCatalog(t, upstream)
	upstream.handle("/v1/models", func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusBadGateway, map[string]any{"error": "down"})
	})
	snapshot, _, errFetch := c.fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now())
	if errFetch == nil {
		t.Fatal("expected an error")
	}
	if snapshot.Revision != first.Revision || len(snapshot.Models) != 2 || snapshot.LastError == "" {
		t.Fatalf("snapshot after error = %+v", snapshot)
	}

	upstream.handle("/v1/models", func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"data": []any{}})
	})
	if _, _, errFetch = newCatalog().fetch(context.Background(), http.DefaultClient, upstream.URL, "test", true, time.Now()); errFetch == nil {
		t.Fatal("an empty catalog must be an error")
	}
}

func TestModelInfosForCredential(t *testing.T) {
	upstream := newFakeWokey(t)
	_, snapshot := fetchTestCatalog(t, upstream)

	all := modelInfosForCredential(snapshot, credential{AllModels: true})
	if len(all) != 2 || all[0].ID != "model-a" || all[1].ID != "model-b" {
		t.Fatalf("all models = %+v", all)
	}
	a := all[0]
	if a.Name != "model-a" || a.DisplayName != "Model A" || a.ContextLength != 200000 || a.OutputTokenLimit != 64000 || !a.UserDefined || a.Type != ProviderID {
		t.Fatalf("model info = %+v", a)
	}
	if !reflect.DeepEqual(a.SupportedInputModalities, []string{"text", "image"}) {
		t.Fatalf("modalities = %v", a.SupportedInputModalities)
	}

	picked := modelInfosForCredential(snapshot, credential{Models: []string{"ghost", "model-b"}})
	if len(picked) != 2 || picked[0].ID != "ghost" || picked[1].ID != "model-b" || picked[0].OwnedBy != ProviderID {
		t.Fatalf("selection = %+v", picked)
	}

	if got := modelInfosForCredential(catalogSnapshot{}, credential{AllModels: true}); len(got) != 0 {
		t.Fatalf("empty catalog registered %d models", len(got))
	}
}

func TestUpstreamModelID(t *testing.T) {
	upstream := newFakeWokey(t)
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
		if got := upstreamModelID(tc.model, tc.prefix, snapshot); got != tc.want {
			t.Errorf("upstreamModelID(%q, %q) = %q, want %q", tc.model, tc.prefix, got, tc.want)
		}
	}
}

func TestPricePerMillion(t *testing.T) {
	if got, ok := pricePerMillion("0.000003"); !ok || math.Abs(got-3) > 1e-9 {
		t.Fatalf("price = %v %v", got, ok)
	}
	for _, bad := range []string{"", " ", "free"} {
		if _, ok := pricePerMillion(bad); ok {
			t.Errorf("pricePerMillion(%q) accepted", bad)
		}
	}
}
