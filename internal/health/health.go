// Package health is the core of framedrag: silent-failure detection
// for feeds (docs/SPEC.md section 6). A blocklist that quietly stopped
// updating is visually indistinguishable from one that works; this
// package is the machine replacement for the human who used to notice.
//
// Failure policy: never fail open, never fail catastrophically. A
// FAILED feed falls back to its last-good cached copy; once the last
// good run is older than the stale_max cutoff the feed is dropped from
// output entirely, loudly. Stale threat intel is worse than none.
package health

import (
	"fmt"
	"net/netip"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/parse"
	"framedrag.dev/framedrag/internal/state"
)

// Status is a feed's health after one run. Order is severity.
type Status int

const (
	OK Status = iota
	Suspect
	Stale
	Failed
)

func (s Status) String() string {
	switch s {
	case OK:
		return "OK"
	case Suspect:
		return "SUSPECT"
	case Stale:
		return "STALE"
	case Failed:
		return "FAILED"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Healthy reports whether the status needs no attention. Anything but
// OK makes the run exit non-zero so cron/systemd surfaces it.
func (s Status) Healthy() bool { return s == OK }

// Thresholds are the global health knobs (config section `health`).
type Thresholds struct {
	// DeltaPct flags a SUSPECT when the entry count moved more than
	// this percentage versus the last good run. Overridden per feed by
	// catalog.Feed.DeltaThresholdPct.
	DeltaPct int
	// RejectedRatio flags a SUSPECT when more than this fraction of
	// candidate lines failed to parse (format drift).
	RejectedRatio float64
	// StaleMax bounds two clocks: content byte-identical for longer
	// than the feed's cadence plus StaleMax is STALE, and a FAILED
	// feed whose last good run is older than StaleMax is dropped.
	StaleMax time.Duration
}

// DefaultThresholds mirrors the spec defaults: 40% delta, 10% rejected
// lines, 14 days stale_max.
func DefaultThresholds() Thresholds {
	return Thresholds{DeltaPct: 40, RejectedRatio: 0.10, StaleMax: 14 * 24 * time.Hour}
}

// Input is everything known about one feed's current run. The pipeline
// maps fetch/parse results into this; health has no I/O of its own.
type Input struct {
	Feed catalog.Feed
	// FetchErr is any transport-level failure: non-2xx, timeout, TLS.
	FetchErr error
	// NotModified means the server answered 304; Prefixes/Stats then
	// describe the cached body the pipeline re-parsed (or the pipeline
	// passes the previous count via Prev).
	NotModified bool
	// BodySHA256 of the fetched content, for the staleness clock.
	BodySHA256 string
	Stats      parse.Stats
	// Prefixes as parsed, before normalization.
	Prefixes []netip.Prefix
	// Prev is the persisted state from earlier runs; zero on first run.
	Prev state.FeedState
	Now  time.Time
	// Own is the user's own networks (config `suppress`), part of the
	// sanity floor: a feed shipping them would blackhole the LAN.
	Own []netip.Prefix
}

// Evaluation is the verdict for one feed.
type Evaluation struct {
	Status  Status
	Reasons []string
	// Prefixes is the input list minus sanity-floor drops. This is
	// what proceeds to normalization when the feed is usable.
	Prefixes []netip.Prefix
	// Dropped lists sanity-floor removals; the caller logs every one.
	Dropped []netip.Prefix
	// UseLastGood: the feed FAILED but its cached last-good copy is
	// recent enough to keep blocking with.
	UseLastGood bool
	// DropFeed: FAILED past stale_max; remove from output, loudly.
	DropFeed bool
	// NextState is what the pipeline persists for the next run.
	NextState state.FeedState
}

// sanityFloor are networks no reputable feed overlaps: RFC1918 space,
// plus the user's own networks appended per run. Default routes
// (0.0.0.0/0, ::/0) are handled separately by bit length, since every
// prefix overlaps them.
var sanityFloor = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// Evaluate runs every check from docs/SPEC.md section 6 for one feed.
func Evaluate(in Input, th Thresholds) Evaluation {
	if th.DeltaPct == 0 {
		th.DeltaPct = 40
	}
	if th.RejectedRatio == 0 {
		th.RejectedRatio = 0.10
	}
	if th.StaleMax == 0 {
		th.StaleMax = 14 * 24 * time.Hour
	}

	ev := Evaluation{Status: OK, NextState: in.Prev}
	raise := func(s Status, format string, args ...any) {
		if s > ev.Status {
			ev.Status = s
		}
		ev.Reasons = append(ev.Reasons, fmt.Sprintf(format, args...))
	}

	// Check 6: sanity floor. Runs even alongside other findings so the
	// dropped entries are always reported.
	floor := append(append([]netip.Prefix{}, sanityFloor...), in.Own...)
	for _, p := range in.Prefixes {
		poisoned := p.Bits() == 0 // a default route would blackhole everything
		for _, f := range floor {
			if poisoned {
				break
			}
			if p.Overlaps(f) {
				poisoned = true
			}
		}
		if poisoned {
			ev.Dropped = append(ev.Dropped, p)
		} else {
			ev.Prefixes = append(ev.Prefixes, p)
		}
	}
	if len(ev.Dropped) > 0 {
		raise(Suspect, "sanity floor dropped %d entries (default route, RFC1918, or own networks)", len(ev.Dropped))
	}

	entryCount := len(ev.Prefixes)

	switch {
	// Check 1: HTTP failure.
	case in.FetchErr != nil:
		raise(Failed, "fetch failed: %v", in.FetchErr)

	// Check 2: zero entries. The #1 real-world failure: an error page
	// or challenge served with HTTP 200 parses to nothing.
	case entryCount == 0 && !in.NotModified:
		raise(Failed, "parsed 0 usable entries from %d lines (%d rejected): error page served with 200?",
			in.Stats.LinesSeen, in.Stats.Rejected)

	default:
		// Check 3: count delta versus last good run.
		deltaPct := th.DeltaPct
		if in.Feed.DeltaThresholdPct > 0 {
			deltaPct = in.Feed.DeltaThresholdPct
		}
		if prev := in.Prev.LastGoodCount; prev > 0 {
			delta := 100 * float64(entryCount-prev) / float64(prev)
			if delta > float64(deltaPct) || delta < -float64(deltaPct) {
				raise(Suspect, "entry count delta %+.1f%% vs last good (%d -> %d, threshold %d%%)",
					delta, prev, entryCount, deltaPct)
			}
		}

		// Check 4: rejected-line ratio (format drift).
		if r := in.Stats.RejectedRatio(); r > th.RejectedRatio {
			raise(Suspect, "rejected-line ratio %.1f%% exceeds %.0f%% (format drift?)",
				100*r, 100*th.RejectedRatio)
		}

		// Check 5: staleness. Content byte-identical for longer than
		// the stated cadence plus stale_max.
		unchanged := in.NotModified || (in.BodySHA256 != "" && in.BodySHA256 == in.Prev.BodySHA256)
		if unchanged && !in.Prev.UnchangedSince.IsZero() && in.Feed.CadenceHours > 0 {
			cadence := time.Duration(in.Feed.CadenceHours) * time.Hour
			if age := in.Now.Sub(in.Prev.UnchangedSince); age > cadence+th.StaleMax {
				raise(Stale, "content unchanged for %s (cadence %s + stale_max %s)",
					age.Round(time.Hour), cadence, th.StaleMax)
			}
		}
	}

	// Failure policy: never fail open, never fail catastrophically.
	if ev.Status == Failed {
		lastGoodAge := time.Duration(0)
		hasLastGood := !in.Prev.LastGoodAt.IsZero()
		if hasLastGood {
			lastGoodAge = in.Now.Sub(in.Prev.LastGoodAt)
		}
		switch {
		case hasLastGood && lastGoodAge <= th.StaleMax:
			ev.UseLastGood = true
			ev.Reasons = append(ev.Reasons, fmt.Sprintf("serving last-good copy from %s ago", lastGoodAge.Round(time.Hour)))
		default:
			ev.DropFeed = true
			ev.Reasons = append(ev.Reasons, fmt.Sprintf("no usable last-good copy within %s: feed dropped from output", th.StaleMax))
		}
		if in.Prev.FailingSince.IsZero() {
			ev.NextState.FailingSince = in.Now
		}
	} else {
		ev.NextState.FailingSince = time.Time{}
	}

	// Staleness clock: reset on new bytes, keep ticking on identical.
	if in.BodySHA256 != "" && in.BodySHA256 != in.Prev.BodySHA256 {
		ev.NextState.BodySHA256 = in.BodySHA256
		ev.NextState.UnchangedSince = in.Now
	} else if in.Prev.UnchangedSince.IsZero() {
		ev.NextState.UnchangedSince = in.Now
		ev.NextState.BodySHA256 = in.BodySHA256
	}

	// The last-good baseline advances only on a fully healthy run, so
	// a SUSPECT swing keeps being measured against known-good ground.
	if ev.Status == OK {
		ev.NextState.LastGoodAt = in.Now
		ev.NextState.LastGoodCount = entryCount
	}
	ev.NextState.LastStatus = ev.Status.String()

	return ev
}
