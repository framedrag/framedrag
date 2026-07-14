// Package catalog loads the vendored pfBlockerNG feed catalog and the
// user's local overlay, and diffs the vendored copy against upstream.
//
// Source of truth: pfblockerng_feeds.json from
// github.com/pfBlockerNG/pfBlockerNG (branch devel, path
// src/usr/local/www/pfblockerng/pfblockerng_feeds.json). The vendored
// copy lives at catalog/feeds.json with the upstream commit SHA
// recorded alongside. We track the catalog; we do not fork it.
package catalog

// Feed is one feed the pipeline can fetch. Fields beyond these may be
// added by the catalog owner; existing fields must not change shape.
type Feed struct {
	// Name uniquely identifies the feed across catalog and overlay.
	Name string `yaml:"name" json:"name"`
	// URL to fetch. May contain an _API_KEY_ placeholder that must be
	// filled from the overlay before fetching; a feed with an unfilled
	// placeholder is skipped, never fetched.
	URL         string `yaml:"url" json:"url"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Category    string `yaml:"category,omitempty" json:"category,omitempty"`
	// Tier is the pfBlockerNG quality tier: PRI1 (most reputable)
	// through PRI5 (may contain false positives).
	Tier string `yaml:"tier,omitempty" json:"tier,omitempty"`
	// Format selects a parser by name; empty means auto-detect.
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	// CSVColumn is the 0-based column holding the address when Format
	// is "csv".
	CSVColumn int `yaml:"csv_column,omitempty" json:"csv_column,omitempty"`
	// CadenceHours is the feed's stated update cadence, used by the
	// staleness health check. 0 means unknown.
	CadenceHours int `yaml:"cadence_hours,omitempty" json:"cadence_hours,omitempty"`
	// DeltaThresholdPct overrides health.Thresholds.DeltaPct for feeds
	// that legitimately swing. 0 means use the global default.
	DeltaThresholdPct int  `yaml:"delta_threshold_pct,omitempty" json:"delta_threshold_pct,omitempty"`
	Disabled          bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}
