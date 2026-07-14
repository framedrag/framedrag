package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"framedrag.dev/framedrag/internal/fetch"
)

// fakeFetcher serves a canned body (or error) without any network.
type fakeFetcher struct {
	body   []byte
	err    error
	gotURL string
	notMod bool
}

func (f *fakeFetcher) Fetch(_ context.Context, url string, _ fetch.Hints) (fetch.Result, error) {
	f.gotURL = url
	if f.err != nil {
		return fetch.Result{}, f.err
	}
	return fetch.Result{
		Body:        f.body,
		StatusCode:  200,
		NotModified: f.notMod,
		FetchedAt:   time.Now(),
	}, nil
}

// mutateFixture loads testdata/feeds.json, hands the decoded document
// to fn for editing, and re-encodes it as the fake upstream body.
func mutateFixture(t *testing.T, fn func(doc map[string]any)) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		fn(doc)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func category(t *testing.T, doc map[string]any, section, cat string) map[string]any {
	t.Helper()
	node, ok := doc[section].(map[string]any)[cat].(map[string]any)
	if !ok {
		t.Fatalf("fixture has no %s/%s", section, cat)
	}
	return node
}

func syncWith(t *testing.T, body []byte) Diff {
	t.Helper()
	c := mustLoad(t, "")
	ff := &fakeFetcher{body: body}
	d, err := c.Sync(context.Background(), ff)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if ff.gotURL != UpstreamURL {
		t.Errorf("Sync fetched %q, want UpstreamURL", ff.gotURL)
	}
	return d
}

func TestSyncNoChange(t *testing.T) {
	d := syncWith(t, mutateFixture(t, nil))
	if !d.Empty() {
		t.Fatalf("expected empty diff, got: %s", d)
	}
	if !strings.Contains(d.String(), "matches upstream") {
		t.Errorf("empty diff String() = %q", d.String())
	}
}

func TestSyncAdded(t *testing.T) {
	d := syncWith(t, mutateFixture(t, func(doc map[string]any) {
		node := category(t, doc, "ipv4", "TOR")
		node["feeds"] = append(node["feeds"].([]any), map[string]any{
			"feed":    "New Provider",
			"website": "https://new.example.com/",
			"url":     "https://new.example.com/list.txt",
			"header":  "New_Feed",
		})
	}))
	if len(d.Added) != 1 || d.Added[0].Name != "New_Feed" || len(d.Removed)+len(d.Changed) != 0 {
		t.Fatalf("diff = %s", d)
	}
	if !strings.Contains(d.String(), "+ New_Feed") {
		t.Errorf("String() = %q", d.String())
	}
}

func TestSyncRemoved(t *testing.T) {
	d := syncWith(t, mutateFixture(t, func(doc map[string]any) {
		delete(doc["ipv4"].(map[string]any), "TOR")
	}))
	if len(d.Removed) != 1 || d.Removed[0].Name != "Dan_me_TOR" || len(d.Added)+len(d.Changed) != 0 {
		t.Fatalf("diff = %s", d)
	}
	if !strings.Contains(d.String(), "- Dan_me_TOR") {
		t.Errorf("String() = %q", d.String())
	}
}

func TestSyncChanged(t *testing.T) {
	d := syncWith(t, mutateFixture(t, func(doc map[string]any) {
		feeds := category(t, doc, "ipv4", "TOR")["feeds"].([]any)
		entry := feeds[0].(map[string]any)
		entry["url"] = "https://www.dan.me.uk/torlist/v2"
		entry["status"] = "discontinued"
	}))
	if len(d.Changed) != 1 || len(d.Added)+len(d.Removed) != 0 {
		t.Fatalf("diff = %s", d)
	}
	ch := d.Changed[0]
	if ch.Name != "Dan_me_TOR" {
		t.Errorf("changed feed = %q", ch.Name)
	}
	if !slices.Contains(ch.Fields, "url") || !slices.Contains(ch.Fields, "disabled") {
		t.Errorf("changed fields = %v, want url and disabled", ch.Fields)
	}
	s := d.String()
	if !strings.Contains(s, "~ Dan_me_TOR") || !strings.Contains(s, "torlist/v2") {
		t.Errorf("String() = %q", s)
	}
}

func TestSyncDiffJSON(t *testing.T) {
	d := syncWith(t, mutateFixture(t, func(doc map[string]any) {
		delete(doc["ipv4"].(map[string]any), "TOR")
	}))
	out, err := d.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var round Diff
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("Diff JSON does not round-trip: %v", err)
	}
	if len(round.Removed) != 1 || round.Removed[0].Name != "Dan_me_TOR" {
		t.Fatalf("round-tripped diff = %+v", round)
	}
}

func TestSyncFetchError(t *testing.T) {
	c := mustLoad(t, "")
	wantErr := errors.New("boom")
	_, err := c.Sync(context.Background(), &fakeFetcher{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Sync error = %v, want wrapped boom", err)
	}
}

func TestSyncNotModified(t *testing.T) {
	c := mustLoad(t, "")
	d, err := c.Sync(context.Background(), &fakeFetcher{notMod: true})
	if err != nil || !d.Empty() {
		t.Fatalf("Sync on 304 = %s, %v; want empty, nil", d, err)
	}
}

func TestSyncBadUpstreamBody(t *testing.T) {
	c := mustLoad(t, "")
	_, err := c.Sync(context.Background(), &fakeFetcher{body: []byte("<html>error page</html>")})
	if err == nil {
		t.Fatal("Sync accepted a non-JSON upstream body")
	}
}

func TestSyncIgnoresOverlay(t *testing.T) {
	// User-added feeds and local disables must not read as upstream drift.
	c := mustLoad(t, "testdata/overlay.yaml")
	d, err := c.Sync(context.Background(), &fakeFetcher{body: mutateFixture(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Empty() {
		t.Fatalf("overlay leaked into sync diff: %s", d)
	}
}
