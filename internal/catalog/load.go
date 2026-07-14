package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIKeyPlaceholder is the literal token upstream embeds in the URL of
// registration-gated feeds. The overlay's api_keys section fills it in;
// a feed whose URL still contains it after overlay application gets
// RequiresKey set and is never selected or fetched.
const APIKeyPlaceholder = "_API_KEY_"

// Catalog is the loaded feed catalog: the vendored upstream copy with
// the user's local overlay applied.
type Catalog struct {
	// Feeds is the effective feed list after overlay application,
	// sorted by name. Feeds may be Disabled or RequiresKey; Select
	// filters those out.
	Feeds []Feed

	// vendored is the catalog exactly as parsed from the vendored
	// copy, before any overlay. Sync diffs upstream against this, so
	// user-added feeds and local disables never show up as drift.
	vendored []Feed
}

// upstream* mirror pfblockerng_feeds.json. Top level is
// {description, copyright, ipv4, ipv6, dnsbl}; each of ipv4/ipv6/dnsbl
// maps category name -> category node. DNS blocking is out of scope
// for framedrag, so the dnsbl section is deliberately not decoded.
type upstreamCatalog struct {
	IPv4 map[string]upstreamCategory `json:"ipv4"`
	IPv6 map[string]upstreamCategory `json:"ipv6"`
}

type upstreamCategory struct {
	Info        string         `json:"info"`
	Description string         `json:"description"`
	Action      string         `json:"action"`
	Cron        string         `json:"cron"`
	Status      string         `json:"status"` // "discontinued" disables the whole category
	Feeds       []upstreamFeed `json:"feeds"`
}

type upstreamFeed struct {
	Feed      string              `json:"feed"` // human title, e.g. "Abuse Feodo Tracker"
	Website   string              `json:"website"`
	URL       string              `json:"url"`
	Header    string              `json:"header"` // unique id, e.g. "Abuse_Feodo_C2"; becomes Feed.Name
	Status    string              `json:"status"` // "discontinued" / "Suspended"
	Info      string              `json:"info"`
	Alternate []upstreamAlternate `json:"alternate"`
}

type upstreamAlternate struct {
	URL    string `json:"url"`
	Header string `json:"header"`
	Info   string `json:"info"`
}

// overlay is the shape of feeds.local.yaml. See feeds.local.yaml.example.
type overlay struct {
	// Disable lists feed names (Feed.Name) to mark Disabled.
	Disable []string `yaml:"disable"`
	// Feeds are user-defined feeds appended to the catalog.
	Feeds []Feed `yaml:"feeds"`
	// APIKeys maps feed name -> secret substituted for _API_KEY_ in
	// that feed's URL.
	APIKeys map[string]string `yaml:"api_keys"`
}

// Load parses the vendored upstream catalog at vendoredPath and, if
// overlayPath is non-empty, applies the local overlay: user feeds are
// added, listed feeds disabled, and API keys substituted into
// _API_KEY_ URL placeholders. Feeds whose placeholder is still
// unfilled afterwards are marked RequiresKey and skipped by Select.
func Load(vendoredPath, overlayPath string) (Catalog, error) {
	data, err := os.ReadFile(vendoredPath)
	if err != nil {
		return Catalog{}, fmt.Errorf("catalog: read vendored catalog: %w", err)
	}
	vendored, err := parseUpstream(data)
	if err != nil {
		return Catalog{}, err
	}

	feeds := slices.Clone(vendored)
	if overlayPath != "" {
		ov, err := readOverlay(overlayPath)
		if err != nil {
			return Catalog{}, err
		}
		feeds, err = applyOverlay(feeds, ov)
		if err != nil {
			return Catalog{}, err
		}
	}
	for i := range feeds {
		feeds[i].RequiresKey = strings.Contains(feeds[i].URL, APIKeyPlaceholder)
	}
	slices.SortFunc(feeds, func(a, b Feed) int { return strings.Compare(a.Name, b.Name) })

	return Catalog{Feeds: feeds, vendored: vendored}, nil
}

// parseUpstream maps pfBlockerNG's feeds.json into the flat []Feed the
// pipeline works with. Alternates become their own feeds; the dnsbl
// section is skipped (out of scope). The result is sorted by name.
func parseUpstream(data []byte) ([]Feed, error) {
	var uc upstreamCatalog
	if err := json.Unmarshal(data, &uc); err != nil {
		return nil, fmt.Errorf("catalog: parse feeds.json: %w", err)
	}
	if len(uc.IPv4)+len(uc.IPv6) == 0 {
		return nil, fmt.Errorf("catalog: feeds.json has no ipv4/ipv6 sections; not the pfBlockerNG catalog?")
	}

	var feeds []Feed
	seen := make(map[string]string) // name -> category, for duplicate detection
	add := func(f Feed) error {
		if f.Name == "" || f.URL == "" {
			return fmt.Errorf("catalog: feed in category %s is missing header or url", f.Category)
		}
		if prev, dup := seen[f.Name]; dup {
			return fmt.Errorf("catalog: duplicate feed name %q (categories %s and %s)", f.Name, prev, f.Category)
		}
		seen[f.Name] = f.Category
		feeds = append(feeds, f)
		return nil
	}

	sections := []struct {
		ipVersion  string
		categories map[string]upstreamCategory
	}{
		{"ipv4", uc.IPv4},
		{"ipv6", uc.IPv6},
	}
	for _, sect := range sections {
		for _, cat := range slices.Sorted(maps.Keys(sect.categories)) {
			node := sect.categories[cat]
			catDisabled := node.Status != ""
			for _, uf := range node.Feeds {
				disabled := catDisabled || uf.Status != ""
				base := Feed{
					Name:         uf.Header,
					URL:          uf.URL,
					Description:  describe(uf.Feed, uf.Info),
					Category:     cat,
					Tier:         tierFor(cat),
					CadenceHours: cadenceHours(node.Cron),
					Disabled:     disabled,
					Website:      uf.Website,
					IPVersion:    sect.ipVersion,
				}
				if err := add(base); err != nil {
					return nil, err
				}
				for _, alt := range uf.Alternate {
					f := base
					f.Name = alt.Header
					f.URL = alt.URL
					f.Description = describe(uf.Feed+" (alternate)", alt.Info)
					if err := add(f); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	slices.SortFunc(feeds, func(a, b Feed) int { return strings.Compare(a.Name, b.Name) })
	return feeds, nil
}

func describe(title, info string) string {
	if info == "" {
		return title
	}
	if title == "" {
		return info
	}
	return title + " - " + info
}

var tierRE = regexp.MustCompile(`^PRI([1-5])(_6)?$`)

// tierFor maps a category name to its quality tier. Upstream names the
// IPv6 primary tiers PRI1_6 etc.; both map to the bare tier so that
// selecting PRI1 covers both address families.
func tierFor(category string) string {
	m := tierRE.FindStringSubmatch(category)
	if m == nil {
		return ""
	}
	return "PRI" + m[1]
}

// cadenceHours converts upstream cron labels ("01hour", "08hours",
// "EveryDay", "Weekly") into hours. Unknown labels map to 0 (unknown).
func cadenceHours(cron string) int {
	switch cron {
	case "EveryDay":
		return 24
	case "Weekly":
		return 168
	}
	if s, ok := strings.CutSuffix(cron, "hours"); ok {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	if s, ok := strings.CutSuffix(cron, "hour"); ok {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return 0
}

func readOverlay(path string) (overlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return overlay{}, fmt.Errorf("catalog: read overlay: %w", err)
	}
	var ov overlay
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&ov); err != nil {
		return overlay{}, fmt.Errorf("catalog: parse overlay %s: %w", path, err)
	}
	return ov, nil
}

// applyOverlay applies additions, then disables, then API keys, so a
// user can add their own gated feed and supply its key in one file.
// Error messages never include API key values.
func applyOverlay(feeds []Feed, ov overlay) ([]Feed, error) {
	index := make(map[string]int, len(feeds))
	for i, f := range feeds {
		index[f.Name] = i
	}

	for _, nf := range ov.Feeds {
		if nf.Name == "" || nf.URL == "" {
			return nil, fmt.Errorf("catalog: overlay feed needs both name and url (got name=%q)", nf.Name)
		}
		if _, dup := index[nf.Name]; dup {
			return nil, fmt.Errorf("catalog: overlay feed %q collides with an existing feed", nf.Name)
		}
		index[nf.Name] = len(feeds)
		feeds = append(feeds, nf)
	}

	for _, name := range ov.Disable {
		i, ok := index[name]
		if !ok {
			return nil, fmt.Errorf("catalog: overlay disables unknown feed %q", name)
		}
		feeds[i].Disabled = true
	}

	for _, name := range slices.Sorted(maps.Keys(ov.APIKeys)) {
		key := ov.APIKeys[name]
		if key == "" {
			return nil, fmt.Errorf("catalog: overlay has empty api key for feed %q", name)
		}
		i, ok := index[name]
		if !ok {
			return nil, fmt.Errorf("catalog: overlay supplies api key for unknown feed %q", name)
		}
		if !strings.Contains(feeds[i].URL, APIKeyPlaceholder) {
			return nil, fmt.Errorf("catalog: feed %q has no %s placeholder to fill", name, APIKeyPlaceholder)
		}
		feeds[i].URL = strings.ReplaceAll(feeds[i].URL, APIKeyPlaceholder, key)
		feeds[i].apiKey = key
	}

	return feeds, nil
}
