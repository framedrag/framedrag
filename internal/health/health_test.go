package health

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/parse"
	"framedrag.dev/framedrag/internal/state"
)

var now = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

func pfxs(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// goodPrev is the state of a feed that was healthy yesterday with one
// entry (matching baseInput's single prefix, so delta stays quiet).
func goodPrev() state.FeedState {
	return state.FeedState{
		LastGoodAt:     now.Add(-24 * time.Hour),
		LastGoodCount:  1,
		BodySHA256:     "aaa",
		UnchangedSince: now.Add(-24 * time.Hour),
		LastStatus:     "OK",
	}
}

func baseInput() Input {
	return Input{
		Feed:       catalog.Feed{Name: "test_feed", CadenceHours: 24},
		BodySHA256: "bbb",
		Stats:      parse.Stats{LinesSeen: 30001, Parsed: 30000, Rejected: 1},
		Prefixes:   pfxs("203.0.113.0/24"),
		Prev:       goodPrev(),
		Now:        now,
	}
}

func evalDefault(t *testing.T, in Input) Evaluation {
	t.Helper()
	return Evaluate(in, DefaultThresholds())
}

func TestHealthyRunIsOK(t *testing.T) {
	in := baseInput()
	ev := evalDefault(t, in)
	if ev.Status != OK {
		t.Fatalf("status = %v, reasons = %v", ev.Status, ev.Reasons)
	}
	if ev.UseLastGood || ev.DropFeed {
		t.Fatalf("healthy feed flagged: %+v", ev)
	}
	if !slices.Equal(ev.Prefixes, in.Prefixes) {
		t.Fatalf("prefixes altered: %v", ev.Prefixes)
	}
	// Baseline advances.
	if ev.NextState.LastGoodCount != 1 || !ev.NextState.LastGoodAt.Equal(now) {
		t.Fatalf("last-good not advanced: %+v", ev.NextState)
	}
	if ev.NextState.LastStatus != "OK" {
		t.Fatalf("LastStatus = %q", ev.NextState.LastStatus)
	}
	// Content changed, so the staleness clock resets.
	if !ev.NextState.UnchangedSince.Equal(now) || ev.NextState.BodySHA256 != "bbb" {
		t.Fatalf("staleness clock wrong: %+v", ev.NextState)
	}
}

func TestHTTPFailureIsFAILEDWithLastGoodFallback(t *testing.T) {
	in := baseInput()
	in.FetchErr = errors.New("HTTP 503 after 3 retries")
	in.Prefixes = nil
	in.Stats = parse.Stats{}
	ev := evalDefault(t, in)
	if ev.Status != Failed {
		t.Fatalf("status = %v", ev.Status)
	}
	// Never fail open: last good copy is only a day old, use it.
	if !ev.UseLastGood || ev.DropFeed {
		t.Fatalf("want last-good fallback, got %+v", ev)
	}
	// Baseline must NOT advance on failure.
	if ev.NextState.LastGoodCount != goodPrev().LastGoodCount || !ev.NextState.LastGoodAt.Equal(goodPrev().LastGoodAt) {
		t.Fatalf("baseline moved on failure: %+v", ev.NextState)
	}
	if ev.NextState.FailingSince.IsZero() {
		t.Fatal("FailingSince not set")
	}
	if !hasReason(ev, "503") {
		t.Fatalf("reasons should carry the fetch error: %v", ev.Reasons)
	}
}

func TestFailedBeyondStaleMaxDropsFeedLoudly(t *testing.T) {
	in := baseInput()
	in.FetchErr = errors.New("connect timeout")
	in.Prefixes = nil
	in.Prev.LastGoodAt = now.Add(-15 * 24 * time.Hour) // beyond 14d default
	in.Prev.FailingSince = now.Add(-15 * 24 * time.Hour)
	ev := evalDefault(t, in)
	if ev.Status != Failed {
		t.Fatalf("status = %v", ev.Status)
	}
	if ev.UseLastGood {
		t.Fatal("stale threat intel is worse than none: must not serve last-good")
	}
	if !ev.DropFeed {
		t.Fatal("feed must be dropped from output")
	}
}

func TestExistingFailingSinceIsPreserved(t *testing.T) {
	in := baseInput()
	in.FetchErr = errors.New("boom")
	firstFail := now.Add(-3 * 24 * time.Hour)
	in.Prev.FailingSince = firstFail
	ev := evalDefault(t, in)
	if !ev.NextState.FailingSince.Equal(firstFail) {
		t.Fatalf("FailingSince overwritten: %v", ev.NextState.FailingSince)
	}
}

func TestRecoveryClearsFailingSince(t *testing.T) {
	in := baseInput()
	in.Prev.FailingSince = now.Add(-2 * 24 * time.Hour)
	ev := evalDefault(t, in)
	if ev.Status != OK {
		t.Fatalf("status = %v, reasons %v", ev.Status, ev.Reasons)
	}
	if !ev.NextState.FailingSince.IsZero() {
		t.Fatalf("FailingSince not cleared: %v", ev.NextState.FailingSince)
	}
}

// The #1 real-world failure: an error page served with HTTP 200.
func TestZeroEntriesIsFAILED(t *testing.T) {
	in := baseInput()
	in.Prefixes = nil
	in.Stats = parse.Stats{LinesSeen: 120, CommentLines: 0, Parsed: 0, Rejected: 97}
	ev := evalDefault(t, in)
	if ev.Status != Failed {
		t.Fatalf("HTML error page with 200 must be FAILED, got %v", ev.Status)
	}
	if !ev.UseLastGood {
		t.Fatal("want last-good fallback")
	}
}

// A Cloudflare challenge parses to zero entries too.
func TestCloudflareChallengeIsFAILED(t *testing.T) {
	in := baseInput()
	in.Prefixes = nil
	in.Stats = parse.Stats{LinesSeen: 40, Parsed: 0, Rejected: 31}
	ev := evalDefault(t, in)
	if ev.Status != Failed {
		t.Fatalf("status = %v", ev.Status)
	}
}

// 30000 entries yesterday, 3 today: SUSPECT, and the run still uses the
// current (tiny) list, but the baseline does not advance.
func TestCountCollapseIsSUSPECT(t *testing.T) {
	in := baseInput()
	in.Prev.LastGoodCount = 30000
	in.Prefixes = pfxs("198.51.100.0/24", "203.0.113.0/24", "192.0.2.0/24")
	in.Stats = parse.Stats{LinesSeen: 3, Parsed: 3}
	ev := evalDefault(t, in)
	if ev.Status != Suspect {
		t.Fatalf("status = %v", ev.Status)
	}
	if ev.UseLastGood || ev.DropFeed {
		t.Fatalf("SUSPECT still uses current content: %+v", ev)
	}
	if ev.NextState.LastGoodCount != 30000 {
		t.Fatalf("baseline advanced on SUSPECT: %d", ev.NextState.LastGoodCount)
	}
	if !hasReason(ev, "delta") {
		t.Fatalf("reasons: %v", ev.Reasons)
	}
}

func TestCountGrowthBeyondThresholdIsSUSPECT(t *testing.T) {
	in := baseInput()
	in.Prev.LastGoodCount = 30000
	in.Prefixes = make([]netip.Prefix, 0, 45000)
	for i := 0; i < 45000; i++ {
		in.Prefixes = append(in.Prefixes, netip.MustParsePrefix("203.0.113.0/24"))
	}
	in.Stats = parse.Stats{LinesSeen: 45000, Parsed: 45000}
	ev := evalDefault(t, in) // +50% > 40% default
	if ev.Status != Suspect {
		t.Fatalf("status = %v", ev.Status)
	}
}

func TestPerFeedDeltaOverride(t *testing.T) {
	in := baseInput()
	in.Prev.LastGoodCount = 30000
	in.Feed.DeltaThresholdPct = 90 // this feed legitimately swings
	in.Prefixes = make([]netip.Prefix, 0, 45000)
	for i := 0; i < 45000; i++ {
		in.Prefixes = append(in.Prefixes, netip.MustParsePrefix("203.0.113.0/24"))
	}
	in.Stats = parse.Stats{LinesSeen: 45000, Parsed: 45000}
	ev := evalDefault(t, in) // +50% < 90% override
	if ev.Status != OK {
		t.Fatalf("status = %v, reasons %v", ev.Status, ev.Reasons)
	}
}

func TestFirstRunHasNoDeltaBaseline(t *testing.T) {
	in := baseInput()
	in.Prev = state.FeedState{} // first ever run
	in.Stats = parse.Stats{LinesSeen: 1, Parsed: 1}
	ev := evalDefault(t, in)
	if ev.Status != OK {
		t.Fatalf("first run with no baseline must not trip delta: %v %v", ev.Status, ev.Reasons)
	}
}

func TestRejectedRatioIsSUSPECT(t *testing.T) {
	in := baseInput()
	in.Stats = parse.Stats{LinesSeen: 40000, Parsed: 26000, Rejected: 4000} // 13.3%
	in.Prefixes = pfxs("203.0.113.0/24")
	in.Prev.LastGoodCount = 1 // keep delta quiet
	ev := evalDefault(t, in)
	if ev.Status != Suspect {
		t.Fatalf("status = %v", ev.Status)
	}
	if !hasReason(ev, "reject") {
		t.Fatalf("reasons: %v", ev.Reasons)
	}
}

func TestStaleness(t *testing.T) {
	in := baseInput()
	// Same bytes as last run, unchanged for 20 days on a 24h-cadence
	// feed: 20d > 24h + 14d stale_max.
	in.BodySHA256 = "aaa"
	in.NotModified = true
	in.Prefixes = pfxs("203.0.113.0/24")
	in.Prev.LastGoodCount = 1
	in.Prev.UnchangedSince = now.Add(-20 * 24 * time.Hour)
	ev := evalDefault(t, in)
	if ev.Status != Stale {
		t.Fatalf("status = %v, reasons %v", ev.Status, ev.Reasons)
	}
	// Unchanged content keeps the old clock.
	if !ev.NextState.UnchangedSince.Equal(in.Prev.UnchangedSince) {
		t.Fatalf("staleness clock reset wrongly: %+v", ev.NextState)
	}
}

func TestUnchangedWithinCadenceIsOK(t *testing.T) {
	in := baseInput()
	in.BodySHA256 = "aaa"
	in.NotModified = true
	in.Prefixes = pfxs("203.0.113.0/24")
	in.Prev.LastGoodCount = 1
	in.Prev.UnchangedSince = now.Add(-36 * time.Hour) // < 24h + 14d
	ev := evalDefault(t, in)
	if ev.Status != OK {
		t.Fatalf("status = %v, reasons %v", ev.Status, ev.Reasons)
	}
}

func TestSanityFloorDropsPoisonEntries(t *testing.T) {
	in := baseInput()
	in.Own = pfxs("203.0.113.0/24")
	in.Prefixes = pfxs(
		"0.0.0.0/0",       // default route: dropped
		"::/0",            // v6 default route: dropped
		"10.0.0.0/8",      // RFC1918: dropped
		"172.16.0.0/12",   // RFC1918: dropped
		"192.168.1.0/24",  // RFC1918: dropped
		"203.0.113.5/32",  // inside user's own network: dropped
		"198.51.100.0/24", // legitimate: kept
	)
	in.Stats = parse.Stats{LinesSeen: 7, Parsed: 7}
	in.Prev.LastGoodCount = 7
	ev := evalDefault(t, in)
	if ev.Status != Suspect {
		t.Fatalf("a feed that would blackhole your LAN must be SUSPECT, got %v", ev.Status)
	}
	if !slices.Equal(ev.Prefixes, pfxs("198.51.100.0/24")) {
		t.Fatalf("kept = %v", ev.Prefixes)
	}
	if len(ev.Dropped) != 6 {
		t.Fatalf("dropped = %v", ev.Dropped)
	}
}

func TestSanityFloorAllDroppedBecomesFAILED(t *testing.T) {
	in := baseInput()
	in.Prefixes = pfxs("0.0.0.0/0")
	in.Stats = parse.Stats{LinesSeen: 1, Parsed: 1}
	ev := evalDefault(t, in)
	// Nothing usable survived: that is a zero-entry feed.
	if ev.Status != Failed {
		t.Fatalf("status = %v", ev.Status)
	}
	if !ev.UseLastGood {
		t.Fatal("want last-good fallback")
	}
}

func TestSeverityPrecedenceFailedBeatsSuspect(t *testing.T) {
	in := baseInput()
	in.FetchErr = errors.New("timeout")
	in.Stats = parse.Stats{LinesSeen: 100, Parsed: 50, Rejected: 50}
	in.Prefixes = nil
	ev := evalDefault(t, in)
	if ev.Status != Failed {
		t.Fatalf("status = %v", ev.Status)
	}
}

func TestStatusStrings(t *testing.T) {
	for s, want := range map[Status]string{OK: "OK", Suspect: "SUSPECT", Stale: "STALE", Failed: "FAILED"} {
		if s.String() != want {
			t.Fatalf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}

func TestExitCodeContract(t *testing.T) {
	// Only OK is healthy; the CLI exits non-zero for anything else so
	// cron/systemd surfaces it.
	if !OK.Healthy() {
		t.Fatal("OK must be healthy")
	}
	for _, s := range []Status{Suspect, Stale, Failed} {
		if s.Healthy() {
			t.Fatalf("%v must be unhealthy", s)
		}
	}
}

func hasReason(ev Evaluation, sub string) bool {
	for _, r := range ev.Reasons {
		if strings.Contains(strings.ToLower(r), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
