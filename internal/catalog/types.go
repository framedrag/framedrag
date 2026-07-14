// Package catalog loads the vendored pfBlockerNG feed catalog and the
// user's local overlay, and diffs the vendored copy against upstream.
//
// Source of truth: pfblockerng_feeds.json from
// github.com/pfBlockerNG/pfBlockerNG (branch devel, path
// src/usr/local/www/pfblockerng/pfblockerng_feeds.json). The vendored
// copy lives at catalog/feeds.json with the upstream commit SHA
// recorded alongside. We track the catalog; we do not fork it.
package catalog

import "strings"

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
	// Website is the feed provider's homepage, carried over from the
	// upstream catalog for attribution and debugging.
	Website string `yaml:"website,omitempty" json:"website,omitempty"`
	// IPVersion is "ipv4" or "ipv6", from the upstream catalog section
	// the feed was defined under. Empty means unspecified (user feeds).
	IPVersion string `yaml:"ip_version,omitempty" json:"ip_version,omitempty"`
	// RequiresKey is computed by Load: true when URL still contains an
	// unfilled _API_KEY_ placeholder after overlay application. Such a
	// feed is excluded from Select and must never be fetched. It is
	// not settable from the overlay.
	RequiresKey bool `yaml:"-" json:"requires_key,omitempty"`

	// apiKey is the secret the overlay substituted into URL. It is
	// retained only so String and RedactedURL can redact it; it is
	// never serialized and must never be logged.
	apiKey string
}

// RedactedURL returns URL safe for logs and error messages: any API
// key substituted by the overlay is replaced with _REDACTED_.
func (f Feed) RedactedURL() string {
	if f.apiKey == "" {
		return f.URL
	}
	return strings.ReplaceAll(f.URL, f.apiKey, "_REDACTED_")
}

// String renders the feed for humans. It never exposes API keys; use
// it (or RedactedURL) in all log and error output instead of URL.
func (f Feed) String() string {
	return f.Name + " (" + f.RedactedURL() + ")"
}
