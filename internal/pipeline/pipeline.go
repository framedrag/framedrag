// Package pipeline orchestrates one framedrag run: fetch every feed
// once, parse, evaluate health, normalize per alias, and hand the
// result to the targets. It owns the fail-closed policy wiring: FAILED
// feeds serve their last-good snapshot, feeds failed past stale_max
// are dropped loudly, and the run reports unhealthy whenever any feed
// needs attention.
package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/config"
	"framedrag.dev/framedrag/internal/fetch"
	"framedrag.dev/framedrag/internal/health"
	"framedrag.dev/framedrag/internal/normalize"
	"framedrag.dev/framedrag/internal/parse"
	"framedrag.dev/framedrag/internal/state"
	"framedrag.dev/framedrag/internal/target"
)

// AliasSpec pairs one configured alias with its resolved catalog feeds
// (the CLI resolves `aliases[].feeds` references via catalog.Select).
type AliasSpec struct {
	Alias config.Alias
	Feeds []catalog.Feed
}

// Options wires the pipeline's collaborators. Everything is an
// interface or value; the pipeline itself performs no direct I/O.
type Options struct {
	Fetcher    fetch.Fetcher
	Store      state.Store
	Targets    []target.Target
	Thresholds health.Thresholds
	// Suppress is the user's own networks (config `suppress`).
	Suppress []netip.Prefix
	// DryRun routes to Target.DryRun and leaves all state untouched.
	DryRun bool
	Now    func() time.Time
	// Logf receives suppression and drop notices (spec: log every
	// suppression). Defaults to a no-op.
	Logf func(format string, args ...any)
}

// FeedResult is the per-feed outcome, sized for the health table.
type FeedResult struct {
	Feed         catalog.Feed
	Status       health.Status
	Reasons      []string
	Entries      int
	PrevEntries  int
	LastGoodAt   time.Time
	UsedLastGood bool
	Dropped      bool
}

// RunResult is the whole run's outcome.
type RunResult struct {
	Feeds   []FeedResult
	Reports []target.Report
	// Healthy is true only when every feed came back OK. The CLI maps
	// !Healthy to a non-zero exit so cron/systemd surfaces it.
	Healthy bool
}

// Run executes one update cycle.
func Run(ctx context.Context, opts Options, aliases []AliasSpec) (RunResult, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Thresholds == (health.Thresholds{}) {
		opts.Thresholds = health.DefaultThresholds()
	}
	now := opts.Now()

	// Fetch and evaluate every distinct feed exactly once.
	outcomes := map[string]feedOutcome{}
	order := []string{}
	res := RunResult{Healthy: true}

	for _, as := range aliases {
		for _, feed := range as.Feeds {
			if _, done := outcomes[feed.Name]; done {
				continue
			}
			out, err := runFeed(ctx, opts, feed, now)
			if err != nil {
				return res, err
			}
			outcomes[feed.Name] = out
			order = append(order, feed.Name)
			if !out.result.Status.Healthy() {
				res.Healthy = false
			}
		}
	}
	for _, name := range order {
		res.Feeds = append(res.Feeds, outcomes[name].result)
	}

	// Build one normalized AliasSet per alias, in config order.
	sets := make([]target.AliasSet, 0, len(aliases))
	for _, as := range aliases {
		var merged []netip.Prefix
		for _, feed := range as.Feeds {
			merged = append(merged, outcomes[feed.Name].prefixes...)
		}
		norm := normalize.Normalize(merged, opts.Suppress)
		for _, s := range norm.Suppressed {
			opts.Logf("alias %s: suppressed %s (overlaps own networks)", as.Alias.Name, s)
		}
		sets = append(sets, target.AliasSet{
			Name:      as.Alias.Name,
			Action:    as.Alias.Action,
			Direction: as.Alias.Direction,
			Prefixes:  norm.Prefixes,
		})
	}

	for _, tg := range opts.Targets {
		var rep target.Report
		var err error
		if opts.DryRun {
			rep, err = tg.DryRun(ctx, sets)
		} else {
			rep, err = tg.Apply(ctx, sets)
		}
		if err != nil {
			return res, fmt.Errorf("target %s: %w", tg.Name(), err)
		}
		res.Reports = append(res.Reports, rep)
	}
	return res, nil
}

// feedOutcome is one feed's contribution to the run.
type feedOutcome struct {
	result   FeedResult
	prefixes []netip.Prefix // what aliases consume; nil when dropped
}

func runFeed(ctx context.Context, opts Options, feed catalog.Feed, now time.Time) (out feedOutcome, err error) {
	prev, _, err := opts.Store.Load(feed.Name)
	if err != nil {
		return out, fmt.Errorf("load state for %s: %w", feed.Name, err)
	}

	in := health.Input{Feed: feed, Prev: prev, Now: now, Own: opts.Suppress}

	fr, ferr := opts.Fetcher.Fetch(ctx, feed.URL, fetch.Hints{ETag: prev.ETag, LastModified: prev.LastModified})
	switch {
	case ferr != nil:
		in.FetchErr = ferr
	case fr.NotModified:
		in.NotModified = true
		in.BodySHA256 = prev.BodySHA256
		cached, lerr := opts.Store.LastGood(feed.Name)
		if lerr != nil {
			return out, fmt.Errorf("last-good for %s: %w", feed.Name, lerr)
		}
		in.Prefixes = cached
	default:
		sum := sha256.Sum256(fr.Body)
		in.BodySHA256 = hex.EncodeToString(sum[:])
		format := feed.Format
		if format == "" {
			format = "detect"
		}
		parser, perr := parse.Get(format, parse.Options{CSVColumn: feed.CSVColumn})
		if perr != nil {
			in.FetchErr = fmt.Errorf("parser: %w", perr)
		} else {
			prefixes, stats, parseErr := parser.Parse(bytes.NewReader(fr.Body))
			if parseErr != nil {
				in.FetchErr = fmt.Errorf("parse: %w", parseErr)
			} else {
				in.Prefixes = prefixes
				in.Stats = stats
			}
		}
	}

	ev := health.Evaluate(in, opts.Thresholds)
	for _, d := range ev.Dropped {
		opts.Logf("feed %s: dropped poison entry %s (sanity floor)", feed, d)
	}
	for _, r := range ev.Reasons {
		opts.Logf("feed %s: %s [%s]", feed, r, ev.Status)
	}

	// Decide what aliases consume.
	switch {
	case ev.DropFeed:
		out.prefixes = nil
	case ev.UseLastGood, in.NotModified:
		cached, lerr := opts.Store.LastGood(feed.Name)
		if lerr != nil {
			return out, fmt.Errorf("last-good for %s: %w", feed.Name, lerr)
		}
		out.prefixes = cached
	default:
		out.prefixes = ev.Prefixes
	}

	// Persist, except on dry runs (spec: everything except Apply, and
	// a rehearsal must not move the baselines either).
	if !opts.DryRun {
		next := ev.NextState
		if ferr == nil {
			if fr.ETag != "" {
				next.ETag = fr.ETag
			}
			if fr.LastModified != "" {
				next.LastModified = fr.LastModified
			}
		}
		if err := opts.Store.Save(feed.Name, next); err != nil {
			return out, fmt.Errorf("save state for %s: %w", feed.Name, err)
		}
		if ev.Status == health.OK && !in.NotModified && len(ev.Prefixes) > 0 {
			if err := opts.Store.SaveLastGood(feed.Name, ev.Prefixes); err != nil {
				return out, fmt.Errorf("save last-good for %s: %w", feed.Name, err)
			}
		}
	}

	lastGoodAt := ev.NextState.LastGoodAt
	out.result = FeedResult{
		Feed:         feed,
		Status:       ev.Status,
		Reasons:      ev.Reasons,
		Entries:      len(out.prefixes),
		PrevEntries:  prev.LastGoodCount,
		LastGoodAt:   lastGoodAt,
		UsedLastGood: ev.UseLastGood,
		Dropped:      ev.DropFeed,
	}
	return out, nil
}
