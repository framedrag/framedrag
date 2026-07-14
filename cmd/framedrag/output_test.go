package main

import (
	"strings"
	"testing"
	"time"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/health"
	"framedrag.dev/framedrag/internal/pipeline"
	"framedrag.dev/framedrag/internal/target"
)

func TestFormatDelta(t *testing.T) {
	cases := []struct {
		entries, prev int
		want          string
	}{
		{100, 0, "-"},
		{0, 0, "-"},
		{150, 100, "+50.0%"},
		{80, 100, "-20.0%"},
		{100, 100, "+0.0%"},
		{0, 100, "-100.0%"},
		{125, 1000, "-87.5%"},
	}
	for _, c := range cases {
		if got := formatDelta(c.entries, c.prev); got != c.want {
			t.Errorf("formatDelta(%d, %d) = %q, want %q", c.entries, c.prev, got, c.want)
		}
	}
}

func TestFormatLastGood(t *testing.T) {
	if got := formatLastGood(time.Time{}); got != "-" {
		t.Errorf("zero time = %q, want -", got)
	}
	ts := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	if got := formatLastGood(ts); got != "2026-07-14T10:00:00Z" {
		t.Errorf("got %q", got)
	}
}

func TestFormatCadence(t *testing.T) {
	cases := []struct {
		hours int
		want  string
	}{
		{0, "unknown"},
		{1, "1h"},
		{12, "12h"},
		{24, "daily"},
		{48, "2d"},
		{168, "weekly"},
	}
	for _, c := range cases {
		if got := formatCadence(c.hours); got != c.want {
			t.Errorf("formatCadence(%d) = %q, want %q", c.hours, got, c.want)
		}
	}
}

func sampleFeeds() []pipeline.FeedResult {
	return []pipeline.FeedResult{
		{
			Feed:        catalog.Feed{Name: "Abuse_Feodo_C2", Tier: "PRI1"},
			Status:      health.OK,
			Entries:     120,
			PrevEntries: 100,
			LastGoodAt:  time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC),
		},
		{
			Feed:    catalog.Feed{Name: "CINS_army"},
			Status:  health.Failed,
			Reasons: []string{"fetch failed: unexpected HTTP status 404"},
			Entries: 30,
		},
	}
}

func TestRenderFeedTable(t *testing.T) {
	got := renderFeedTable(sampleFeeds(), false)
	want := strings.Join([]string{
		"FEED            TIER  STATUS  ENTRIES  DELTA   LAST GOOD",
		"Abuse_Feodo_C2  PRI1  OK      120      +20.0%  2026-07-14T10:00:00Z",
		"CINS_army       -     FAILED  30       -       -",
		"",
	}, "\n")
	if got != want {
		t.Errorf("table mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderFeedTableWithReasons(t *testing.T) {
	got := renderFeedTable(sampleFeeds(), true)
	if !strings.Contains(got, "\n    fetch failed: unexpected HTTP status 404\n") {
		t.Errorf("missing indented reason under unhealthy feed:\n%s", got)
	}
	if strings.Count(got, "fetch failed") != 1 {
		t.Errorf("reason should appear once:\n%s", got)
	}
}

func TestRenderReports(t *testing.T) {
	reports := []target.Report{{
		Target: "file",
		DryRun: true,
		Changes: []target.Change{
			{Object: "/lists/fd_a.txt", Kind: "create", Detail: "2 entries"},
			{Object: "/lists/fd_b.txt", Kind: "update", Detail: "5 entries (+1 -0 vs previous)"},
			{Object: "/lists/fd_c.txt", Kind: "unchanged", Detail: "3 entries"},
		},
	}}
	got := renderReports(reports, false)
	want := "target file (dry run): 1 create, 1 update, 1 unchanged\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	verbose := renderReports(reports, true)
	for _, s := range []string{
		"create    /lists/fd_a.txt (2 entries)",
		"update    /lists/fd_b.txt (5 entries (+1 -0 vs previous))",
		"unchanged /lists/fd_c.txt (3 entries)",
	} {
		if !strings.Contains(verbose, s) {
			t.Errorf("verbose output missing %q:\n%s", s, verbose)
		}
	}
}

func TestRenderReportsEmpty(t *testing.T) {
	got := renderReports([]target.Report{{Target: "file"}}, false)
	if got != "target file: no changes\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenderCatalogList(t *testing.T) {
	feeds := []catalog.Feed{
		{Name: "Abuse_Feodo_C2", Tier: "PRI1", Category: "PRI1", CadenceHours: 1},
		{Name: "Old_Feed", Tier: "PRI1", Category: "PRI1", CadenceHours: 1, Disabled: true},
		{Name: "Pulsedive", Tier: "PRI3", Category: "PRI3", CadenceHours: 1, RequiresKey: true},
		{Name: "Dan_me_TOR", Category: "TOR", CadenceHours: 1},
	}
	got := renderCatalogList(feeds)
	for _, s := range []string{"PRI1 (2 feeds)", "PRI3 (1 feeds)", "TOR (1 feeds)", "[disabled]", "[requires key]"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
	// PRI tiers come before other groups.
	if strings.Index(got, "TOR") < strings.Index(got, "PRI3") {
		t.Errorf("group order wrong:\n%s", got)
	}
}

func TestToRunJSONShapes(t *testing.T) {
	res := pipeline.RunResult{Healthy: true}
	j := toRunJSON(res)
	if j.Feeds == nil || j.Reports == nil {
		t.Error("feeds and reports must be non-nil so JSON emits [] not null")
	}
	h := toHealthJSON(res)
	if h.Feeds == nil {
		t.Error("health feeds must be non-nil")
	}
	if c := toCatalogJSON(nil); c == nil {
		t.Error("catalog feeds must be non-nil")
	}
}
