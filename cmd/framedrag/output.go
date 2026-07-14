package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/pipeline"
	"framedrag.dev/framedrag/internal/target"
)

// This file holds the pure rendering helpers: tables and JSON shapes.
// Commands stay thin; everything here is unit-testable with plain
// string comparisons.

// ---- generic table -------------------------------------------------

// row is one table line: cells, plus optional indented detail lines
// printed underneath (outside the column grid).
type row struct {
	cells   []string
	details []string
}

// renderTable formats header and rows into aligned columns separated
// by two spaces. Detail lines are indented and never affect widths.
func renderTable(header []string, rows []row) string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r.cells {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	var b strings.Builder
	line := func(cells []string) {
		var l strings.Builder
		for i, c := range cells {
			if i > 0 {
				l.WriteString("  ")
			}
			l.WriteString(c + strings.Repeat(" ", widths[i]-len(c)))
		}
		b.WriteString(strings.TrimRight(l.String(), " "))
		b.WriteByte('\n')
	}
	line(header)
	for _, r := range rows {
		line(r.cells)
		for _, d := range r.details {
			b.WriteString("    " + d + "\n")
		}
	}
	return b.String()
}

// ---- feed table (update and health) --------------------------------

// formatDelta renders the entry count movement versus the last good
// baseline as a signed percentage, or "-" when there is no baseline.
func formatDelta(entries, prev int) string {
	if prev == 0 {
		return "-"
	}
	return fmt.Sprintf("%+.1f%%", 100*float64(entries-prev)/float64(prev))
}

// formatLastGood renders the last-good timestamp, or "-" when the feed
// has never had a good run.
func formatLastGood(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderFeedTable renders the per-feed status table. When withReasons
// is set, each unhealthy feed's reasons are indented underneath it.
func renderFeedTable(feeds []pipeline.FeedResult, withReasons bool) string {
	header := []string{"FEED", "TIER", "STATUS", "ENTRIES", "DELTA", "LAST GOOD"}
	rows := make([]row, 0, len(feeds))
	for _, f := range feeds {
		r := row{cells: []string{
			f.Feed.Name,
			orDash(f.Feed.Tier),
			f.Status.String(),
			strconv.Itoa(f.Entries),
			formatDelta(f.Entries, f.PrevEntries),
			formatLastGood(f.LastGoodAt),
		}}
		if withReasons && !f.Status.Healthy() {
			r.details = f.Reasons
		}
		rows = append(rows, r)
	}
	return renderTable(header, rows)
}

// ---- target reports ------------------------------------------------

// renderReports summarizes each target's changes by kind; with verbose
// it also lists every change.
func renderReports(reports []target.Report, verbose bool) string {
	var b strings.Builder
	for _, rep := range reports {
		counts := map[string]int{}
		for _, c := range rep.Changes {
			counts[c.Kind]++
		}
		parts := make([]string, 0, 4)
		for _, kind := range []string{"create", "update", "delete", "unchanged"} {
			if counts[kind] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[kind], kind))
			}
		}
		summary := "no changes"
		if len(parts) > 0 {
			summary = strings.Join(parts, ", ")
		}
		label := ""
		if rep.DryRun {
			label = " (dry run)"
		}
		fmt.Fprintf(&b, "target %s%s: %s\n", rep.Target, label, summary)
		if verbose {
			for _, c := range rep.Changes {
				fmt.Fprintf(&b, "  %-9s %s (%s)\n", c.Kind, c.Object, c.Detail)
			}
		}
	}
	return b.String()
}

// ---- catalog list --------------------------------------------------

// formatCadence renders a feed cadence in hours for humans.
func formatCadence(hours int) string {
	switch {
	case hours == 0:
		return "unknown"
	case hours == 24:
		return "daily"
	case hours == 168:
		return "weekly"
	case hours%24 == 0:
		return fmt.Sprintf("%dd", hours/24)
	default:
		return fmt.Sprintf("%dh", hours)
	}
}

// renderCatalogList prints feeds grouped by tier (PRI1..PRI5 first,
// then the remaining groups by category name).
func renderCatalogList(feeds []catalog.Feed) string {
	groups := map[string][]catalog.Feed{}
	for _, f := range feeds {
		key := f.Tier
		if key == "" {
			key = f.Category
		}
		if key == "" {
			key = "other"
		}
		groups[key] = append(groups[key], f)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	// PRI tiers first in order, then everything else alphabetically.
	rank := func(name string) string {
		if strings.HasPrefix(name, "PRI") && len(name) == 4 {
			return "0" + name
		}
		return "1" + name
	}
	sort.Slice(names, func(i, j int) bool { return rank(names[i]) < rank(names[j]) })

	var b strings.Builder
	for gi, name := range names {
		if gi > 0 {
			b.WriteByte('\n')
		}
		fs := groups[name]
		fmt.Fprintf(&b, "%s (%d feeds)\n", name, len(fs))
		rows := make([]row, 0, len(fs))
		for _, f := range fs {
			var marks []string
			if f.Disabled {
				marks = append(marks, "[disabled]")
			}
			if f.RequiresKey {
				marks = append(marks, "[requires key]")
			}
			rows = append(rows, row{cells: []string{
				"  " + f.Name,
				formatCadence(f.CadenceHours),
				strings.Join(marks, " "),
			}})
		}
		b.WriteString(renderTable([]string{"  NAME", "CADENCE", ""}, rows))
	}
	return b.String()
}

// ---- JSON shapes ---------------------------------------------------

// feedJSON is one feed's outcome in --json output. URLs are always
// redacted.
type feedJSON struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Tier         string   `json:"tier,omitempty"`
	Status       string   `json:"status"`
	Reasons      []string `json:"reasons,omitempty"`
	Entries      int      `json:"entries"`
	PrevEntries  int      `json:"prev_entries"`
	LastGoodAt   string   `json:"last_good_at,omitempty"`
	UsedLastGood bool     `json:"used_last_good,omitempty"`
	Dropped      bool     `json:"dropped,omitempty"`
}

func toFeedJSON(f pipeline.FeedResult) feedJSON {
	out := feedJSON{
		Name:         f.Feed.Name,
		URL:          f.Feed.RedactedURL(),
		Tier:         f.Feed.Tier,
		Status:       f.Status.String(),
		Reasons:      f.Reasons,
		Entries:      f.Entries,
		PrevEntries:  f.PrevEntries,
		UsedLastGood: f.UsedLastGood,
		Dropped:      f.Dropped,
	}
	if !f.LastGoodAt.IsZero() {
		out.LastGoodAt = f.LastGoodAt.UTC().Format(time.RFC3339)
	}
	return out
}

type changeJSON struct {
	Object string `json:"object"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

type reportJSON struct {
	Target  string       `json:"target"`
	DryRun  bool         `json:"dry_run"`
	Changes []changeJSON `json:"changes"`
}

func toReportJSON(r target.Report) reportJSON {
	out := reportJSON{Target: r.Target, DryRun: r.DryRun, Changes: make([]changeJSON, 0, len(r.Changes))}
	for _, c := range r.Changes {
		out.Changes = append(out.Changes, changeJSON{Object: c.Object, Kind: c.Kind, Detail: c.Detail})
	}
	return out
}

// runJSON is the update command's --json document.
type runJSON struct {
	Feeds   []feedJSON   `json:"feeds"`
	Reports []reportJSON `json:"reports"`
	Healthy bool         `json:"healthy"`
}

func toRunJSON(res pipeline.RunResult) runJSON {
	out := runJSON{
		Feeds:   make([]feedJSON, 0, len(res.Feeds)),
		Reports: make([]reportJSON, 0, len(res.Reports)),
		Healthy: res.Healthy,
	}
	for _, f := range res.Feeds {
		out.Feeds = append(out.Feeds, toFeedJSON(f))
	}
	for _, r := range res.Reports {
		out.Reports = append(out.Reports, toReportJSON(r))
	}
	return out
}

// healthJSON is the health command's --json document.
type healthJSON struct {
	Feeds   []feedJSON `json:"feeds"`
	Healthy bool       `json:"healthy"`
}

func toHealthJSON(res pipeline.RunResult) healthJSON {
	out := healthJSON{Feeds: make([]feedJSON, 0, len(res.Feeds)), Healthy: res.Healthy}
	for _, f := range res.Feeds {
		out.Feeds = append(out.Feeds, toFeedJSON(f))
	}
	return out
}

// catalogFeedJSON is one catalog entry in --json output. It mirrors
// catalog.Feed but with the URL redacted, so API keys never reach
// stdout.
type catalogFeedJSON struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Description  string `json:"description,omitempty"`
	Category     string `json:"category,omitempty"`
	Tier         string `json:"tier,omitempty"`
	Format       string `json:"format,omitempty"`
	CadenceHours int    `json:"cadence_hours,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
	RequiresKey  bool   `json:"requires_key,omitempty"`
	Website      string `json:"website,omitempty"`
	IPVersion    string `json:"ip_version,omitempty"`
}

func toCatalogJSON(feeds []catalog.Feed) []catalogFeedJSON {
	out := make([]catalogFeedJSON, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, catalogFeedJSON{
			Name:         f.Name,
			URL:          f.RedactedURL(),
			Description:  f.Description,
			Category:     f.Category,
			Tier:         f.Tier,
			Format:       f.Format,
			CadenceHours: f.CadenceHours,
			Disabled:     f.Disabled,
			RequiresKey:  f.RequiresKey,
			Website:      f.Website,
			IPVersion:    f.IPVersion,
		})
	}
	return out
}

// writeJSON writes v as indented JSON with a trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
