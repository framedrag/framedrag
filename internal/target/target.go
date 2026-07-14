// Package target defines the output backend interface and its
// implementations. v1 ships the file target (lists on disk, optional
// loopback-only HTTP server for URL table aliases). Later targets
// drive firewall APIs directly.
//
// Ownership rule for API targets: framedrag only ever creates,
// modifies, or deletes objects whose names carry the fd_ prefix.
package target

import (
	"context"
	"net/netip"
)

// AliasSet is one consolidated, normalized, sorted list destined for
// one firewall alias.
type AliasSet struct {
	// Name carries the fd_ prefix, e.g. fd_pri1.
	Name string
	// Action is deny, permit, or match.
	Action string
	// Direction is in, out, or both.
	Direction string
	// Prefixes is deduplicated, aggregated, suppressed, and sorted
	// before it reaches any target.
	Prefixes []netip.Prefix
}

// Change describes one concrete difference a target would (DryRun) or
// did (Apply) make.
type Change struct {
	Object string // e.g. "fd_pri1" or a file path
	Kind   string // create, update, delete, unchanged
	Detail string
}

// Report summarizes one Apply or DryRun.
type Report struct {
	Target  string
	DryRun  bool
	Changes []Change
}

// Target applies alias sets to a destination. Apply must be
// idempotent; DryRun must perform no writes and report exactly what
// Apply would do.
type Target interface {
	Name() string
	Apply(ctx context.Context, sets []AliasSet) (Report, error)
	DryRun(ctx context.Context, sets []AliasSet) (Report, error)
}
