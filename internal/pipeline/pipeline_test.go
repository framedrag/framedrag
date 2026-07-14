package pipeline

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/config"
	"framedrag.dev/framedrag/internal/fetch"
	"framedrag.dev/framedrag/internal/health"
	"framedrag.dev/framedrag/internal/state"
	"framedrag.dev/framedrag/internal/target"
)

var now = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// fakeTarget records Apply/DryRun calls.
type fakeTarget struct {
	applied [][]target.AliasSet
	dryRuns [][]target.AliasSet
	err     error
}

func (f *fakeTarget) Name() string { return "fake" }

func (f *fakeTarget) Apply(_ context.Context, sets []target.AliasSet) (target.Report, error) {
	f.applied = append(f.applied, sets)
	return target.Report{Target: "fake"}, f.err
}

func (f *fakeTarget) DryRun(_ context.Context, sets []target.AliasSet) (target.Report, error) {
	f.dryRuns = append(f.dryRuns, sets)
	return target.Report{Target: "fake", DryRun: true}, f.err
}

func feedA() catalog.Feed {
	return catalog.Feed{Name: "feed_a", URL: "https://a.example/list.txt", Format: "plain", CadenceHours: 24}
}

func feedB() catalog.Feed {
	return catalog.Feed{Name: "feed_b", URL: "https://b.example/list.txt", Format: "plain", CadenceHours: 24}
}

func newEnv(t *testing.T) (*fetch.Fake, state.Store, *fakeTarget, Options) {
	t.Helper()
	fk := fetch.NewFake()
	st, err := state.NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tg := &fakeTarget{}
	opts := Options{
		Fetcher: fk,
		Store:   st,
		Targets: []target.Target{tg},
		Now:     func() time.Time { return now },
	}
	return fk, st, tg, opts
}

func alias(name string, feeds ...catalog.Feed) AliasSpec {
	return AliasSpec{
		Alias: config.Alias{Name: name, Action: "deny", Direction: "in"},
		Feeds: feeds,
	}
}

func body(res string) fetch.Result {
	return fetch.Result{Body: []byte(res), StatusCode: 200, ETag: `"v1"`, FetchedAt: now}
}

func TestHealthyRunAppliesAggregatedAlias(t *testing.T) {
	fk, st, tg, opts := newEnv(t)
	fk.Queue(feedA().URL, body("198.51.100.0/25\n198.51.100.128/25\n"))
	fk.Queue(feedB().URL, body("203.0.113.7\n"))

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA(), feedB())})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy {
		t.Fatalf("unhealthy: %+v", res.Feeds)
	}
	if len(tg.applied) != 1 || len(tg.dryRuns) != 0 {
		t.Fatalf("apply calls = %d, dryrun calls = %d", len(tg.applied), len(tg.dryRuns))
	}
	sets := tg.applied[0]
	if len(sets) != 1 || sets[0].Name != "fd_test" || sets[0].Action != "deny" || sets[0].Direction != "in" {
		t.Fatalf("sets = %+v", sets)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/24"), // aggregated from the two /25s
		netip.MustParsePrefix("203.0.113.7/32"),
	}
	if !slices.Equal(sets[0].Prefixes, want) {
		t.Fatalf("prefixes = %v, want %v", sets[0].Prefixes, want)
	}

	// State persisted: validators and last-good snapshot.
	fs, ok, err := st.Load("feed_a")
	if err != nil || !ok {
		t.Fatalf("state missing: %v %v", ok, err)
	}
	if fs.ETag != `"v1"` || fs.LastGoodCount != 2 || !fs.LastGoodAt.Equal(now) || fs.LastStatus != "OK" {
		t.Fatalf("state = %+v", fs)
	}
	// The snapshot holds the parsed prefixes (the health baseline), not
	// the aggregated output.
	lg, err := st.LastGood("feed_a")
	wantLG := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/25"),
		netip.MustParsePrefix("198.51.100.128/25"),
	}
	if err != nil || !slices.Equal(lg, wantLG) {
		t.Fatalf("lastgood = %v (err %v), want %v", lg, err, wantLG)
	}
}

func TestFeedFetchedOnceAcrossAliases(t *testing.T) {
	fk, _, tg, opts := newEnv(t)
	fk.Queue(feedA().URL, body("198.51.100.0/24\n"))
	_, err := Run(context.Background(), opts, []AliasSpec{
		alias("fd_one", feedA()),
		alias("fd_two", feedA()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls := fk.Calls(); len(calls) != 1 {
		t.Fatalf("fetch calls = %d, want 1", len(calls))
	}
	if len(tg.applied[0]) != 2 {
		t.Fatalf("want both aliases applied, got %+v", tg.applied[0])
	}
}

func TestConditionalHintsComeFromState(t *testing.T) {
	fk, st, _, opts := newEnv(t)
	if err := st.Save("feed_a", state.FeedState{ETag: `"old"`, LastModified: "yesterday"}); err != nil {
		t.Fatal(err)
	}
	fk.Queue(feedA().URL, body("198.51.100.0/24\n"))
	if _, err := Run(context.Background(), opts, []AliasSpec{alias("fd_x", feedA())}); err != nil {
		t.Fatal(err)
	}
	calls := fk.Calls()
	if len(calls) != 1 || calls[0].Hints.ETag != `"old"` || calls[0].Hints.LastModified != "yesterday" {
		t.Fatalf("hints = %+v", calls)
	}
}

func TestFailedFeedServesLastGood(t *testing.T) {
	fk, st, tg, opts := newEnv(t)
	seedLastGood(t, st, "feed_a", now.Add(-24*time.Hour), "198.51.100.0/24")
	fk.QueueError(feedA().URL, errors.New("HTTP 503"))
	fk.Queue(feedB().URL, body("203.0.113.7\n"))

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA(), feedB())})
	if err != nil {
		t.Fatal(err)
	}
	if res.Healthy {
		t.Fatal("run with a FAILED feed must be unhealthy")
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/24"), // from last-good cache
		netip.MustParsePrefix("203.0.113.7/32"),
	}
	if !slices.Equal(tg.applied[0][0].Prefixes, want) {
		t.Fatalf("prefixes = %v, want %v", tg.applied[0][0].Prefixes, want)
	}
	fr := findFeed(t, res, "feed_a")
	if fr.Status != health.Failed || !fr.UsedLastGood || fr.Dropped {
		t.Fatalf("feed result = %+v", fr)
	}
}

func TestFailedBeyondStaleMaxIsDropped(t *testing.T) {
	fk, st, tg, opts := newEnv(t)
	seedLastGood(t, st, "feed_a", now.Add(-20*24*time.Hour), "198.51.100.0/24")
	fk.QueueError(feedA().URL, errors.New("HTTP 503"))
	fk.Queue(feedB().URL, body("203.0.113.7\n"))

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA(), feedB())})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("203.0.113.7/32")}
	if !slices.Equal(tg.applied[0][0].Prefixes, want) {
		t.Fatalf("stale feed must be dropped: %v", tg.applied[0][0].Prefixes)
	}
	fr := findFeed(t, res, "feed_a")
	if !fr.Dropped || fr.UsedLastGood {
		t.Fatalf("feed result = %+v", fr)
	}
	if res.Healthy {
		t.Fatal("must be unhealthy")
	}
}

func TestNotModifiedReusesCache(t *testing.T) {
	fk, st, tg, opts := newEnv(t)
	seedLastGood(t, st, "feed_a", now.Add(-2*time.Hour), "198.51.100.0/24")
	fk.Queue(feedA().URL, fetch.Result{NotModified: true, StatusCode: 304, ETag: `"v1"`})

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy {
		t.Fatalf("304 is healthy: %+v", res.Feeds)
	}
	want := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	if !slices.Equal(tg.applied[0][0].Prefixes, want) {
		t.Fatalf("prefixes = %v", tg.applied[0][0].Prefixes)
	}
}

func TestDryRunAppliesNothingAndPersistsNothing(t *testing.T) {
	fk, st, tg, opts := newEnv(t)
	opts.DryRun = true
	fk.Queue(feedA().URL, body("198.51.100.0/24\n"))

	if _, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())}); err != nil {
		t.Fatal(err)
	}
	if len(tg.applied) != 0 || len(tg.dryRuns) != 1 {
		t.Fatalf("apply = %d dryrun = %d", len(tg.applied), len(tg.dryRuns))
	}
	if _, ok, _ := st.Load("feed_a"); ok {
		t.Fatal("dry run must not persist state")
	}
	if lg, _ := st.LastGood("feed_a"); lg != nil {
		t.Fatal("dry run must not write last-good snapshots")
	}
}

func TestPoisonedFeedEntriesDropped(t *testing.T) {
	fk, _, tg, opts := newEnv(t)
	opts.Suppress = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	fk.Queue(feedA().URL, body("0.0.0.0/0\n192.0.2.5\n198.51.100.0/24\n"))

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())})
	if err != nil {
		t.Fatal(err)
	}
	if res.Healthy {
		t.Fatal("sanity-floor drops must make the run unhealthy")
	}
	want := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	if !slices.Equal(tg.applied[0][0].Prefixes, want) {
		t.Fatalf("prefixes = %v", tg.applied[0][0].Prefixes)
	}
	fr := findFeed(t, res, "feed_a")
	if fr.Status != health.Suspect {
		t.Fatalf("status = %v", fr.Status)
	}
}

func TestTargetErrorSurfaces(t *testing.T) {
	fk, _, tg, opts := newEnv(t)
	tg.err = errors.New("disk full")
	fk.Queue(feedA().URL, body("198.51.100.0/24\n"))
	_, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownParserFailsFeedNotRun(t *testing.T) {
	fk, _, _, opts := newEnv(t)
	f := feedA()
	f.Format = "no-such-format"
	fk.Queue(f.URL, body("198.51.100.0/24\n"))
	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", f)})
	if err != nil {
		t.Fatalf("a bad feed must not abort the run: %v", err)
	}
	if res.Healthy {
		t.Fatal("must be unhealthy")
	}
	if fr := findFeed(t, res, "feed_a"); fr.Status != health.Failed {
		t.Fatalf("status = %v", fr.Status)
	}
}

func seedLastGood(t *testing.T, st state.Store, feed string, at time.Time, prefixes ...string) {
	t.Helper()
	ps := make([]netip.Prefix, 0, len(prefixes))
	for _, s := range prefixes {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	if err := st.Save(feed, state.FeedState{
		LastGoodAt: at, LastGoodCount: len(ps), LastStatus: "OK",
		BodySHA256: "seeded", UnchangedSince: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveLastGood(feed, ps); err != nil {
		t.Fatal(err)
	}
}

func findFeed(t *testing.T, res RunResult, name string) FeedResult {
	t.Helper()
	for _, fr := range res.Feeds {
		if fr.Feed.Name == name {
			return fr
		}
	}
	t.Fatalf("feed %q not in results: %+v", name, res.Feeds)
	return FeedResult{}
}
