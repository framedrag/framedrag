package catalog

import "strings"

// Select resolves config references (aliases[].feeds) into concrete
// feeds. Each ref may be a quality tier (PRI1..PRI5, covering both
// address families), a category name (e.g. TOR, BlockListDE, PRI1_6),
// or an individual feed name. Matching is case-insensitive.
//
// Disabled feeds and feeds with an unfilled _API_KEY_ placeholder
// (RequiresKey) are never returned. Refs that match nothing are
// ignored; callers wanting strictness should compare against the refs
// they passed. The result is deduplicated and sorted by feed name.
func (c Catalog) Select(refs []string) []Feed {
	var out []Feed
	for _, f := range c.Feeds { // already sorted by name
		if f.Disabled || f.RequiresKey {
			continue
		}
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			if strings.EqualFold(ref, f.Name) ||
				strings.EqualFold(ref, f.Category) ||
				(f.Tier != "" && strings.EqualFold(ref, f.Tier)) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
