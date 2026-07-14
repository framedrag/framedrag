package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"framedrag.dev/framedrag/internal/fetch"
)

// UpstreamURL is the raw location of the pfBlockerNG feed catalog we
// track. Provenance of the vendored copy is recorded in catalog/UPSTREAM.
const UpstreamURL = "https://raw.githubusercontent.com/pfBlockerNG/pfBlockerNG/devel/src/usr/local/www/pfblockerng/pfblockerng_feeds.json"

// Diff is the result of comparing the vendored catalog against
// upstream. It marshals to stable JSON for --json output.
type Diff struct {
	Added   []Feed       `json:"added,omitempty"`
	Removed []Feed       `json:"removed,omitempty"`
	Changed []FeedChange `json:"changed,omitempty"`
}

// FeedChange is one feed present on both sides with differing fields.
// Feeds are keyed by name; a URL change on the same name is a change,
// not a remove+add.
type FeedChange struct {
	Name   string   `json:"name"`
	Fields []string `json:"fields"` // names of the fields that differ
	Old    Feed     `json:"old"`
	New    Feed     `json:"new"`
}

// Sync fetches the upstream catalog, parses it, and diffs it against
// the vendored copy this Catalog was loaded from. The overlay never
// participates: user-added feeds, disables, and API keys are invisible
// to the diff, so no secrets can appear in its output.
func (c Catalog) Sync(ctx context.Context, f fetch.Fetcher) (Diff, error) {
	res, err := f.Fetch(ctx, UpstreamURL, fetch.Hints{})
	if err != nil {
		return Diff{}, fmt.Errorf("catalog: fetch upstream catalog: %w", err)
	}
	if res.NotModified {
		return Diff{}, nil
	}
	upstream, err := parseUpstream(res.Body)
	if err != nil {
		return Diff{}, err
	}
	return diffFeeds(c.vendored, upstream), nil
}

// diffFeeds compares two catalogs keyed by feed name. Both inputs are
// sorted by name, which makes the Diff deterministic.
func diffFeeds(old, new []Feed) Diff {
	oldByName := make(map[string]Feed, len(old))
	for _, f := range old {
		oldByName[f.Name] = f
	}
	newByName := make(map[string]Feed, len(new))
	for _, f := range new {
		newByName[f.Name] = f
	}

	var d Diff
	for _, f := range new {
		prev, ok := oldByName[f.Name]
		if !ok {
			d.Added = append(d.Added, f)
			continue
		}
		if fields := changedFields(prev, f); len(fields) > 0 {
			d.Changed = append(d.Changed, FeedChange{Name: f.Name, Fields: fields, Old: prev, New: f})
		}
	}
	for _, f := range old {
		if _, ok := newByName[f.Name]; !ok {
			d.Removed = append(d.Removed, f)
		}
	}
	return d
}

// changedFields lists every exported Feed field that differs. Any
// difference counts as a change.
func changedFields(a, b Feed) []string {
	var fields []string
	diff := func(name string, changed bool) {
		if changed {
			fields = append(fields, name)
		}
	}
	diff("url", a.URL != b.URL)
	diff("description", a.Description != b.Description)
	diff("category", a.Category != b.Category)
	diff("tier", a.Tier != b.Tier)
	diff("format", a.Format != b.Format)
	diff("csv_column", a.CSVColumn != b.CSVColumn)
	diff("cadence_hours", a.CadenceHours != b.CadenceHours)
	diff("delta_threshold_pct", a.DeltaThresholdPct != b.DeltaThresholdPct)
	diff("disabled", a.Disabled != b.Disabled)
	diff("website", a.Website != b.Website)
	diff("ip_version", a.IPVersion != b.IPVersion)
	return fields
}

// Empty reports whether vendored and upstream are identical.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// String renders the diff for humans. All URLs go through
// Feed.RedactedURL, so API keys can never leak into logs or issues.
func (d Diff) String() string {
	if d.Empty() {
		return "catalog: vendored copy matches upstream"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "catalog: %d added, %d removed, %d changed vs upstream\n",
		len(d.Added), len(d.Removed), len(d.Changed))
	for _, f := range d.Added {
		fmt.Fprintf(&b, "  + %s\n", f)
	}
	for _, f := range d.Removed {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	for _, ch := range d.Changed {
		fmt.Fprintf(&b, "  ~ %s: %s\n", ch.Name, strings.Join(ch.Fields, ", "))
		for _, field := range ch.Fields {
			if field == "url" {
				fmt.Fprintf(&b, "      url: %s -> %s\n", ch.Old.RedactedURL(), ch.New.RedactedURL())
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// JSON renders the diff as indented, stable JSON for --json output and
// machine consumers.
func (d Diff) JSON() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}
