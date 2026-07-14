// Package state persists per-feed history between runs: fetch
// validators, last-good snapshots, and health bookkeeping. Everything
// lives under the configured state_dir; the format is stable JSON plus
// one cached list file per feed.
package state

import (
	"net/netip"
	"time"
)

// FeedState is the durable record for one feed.
type FeedState struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	LastGoodAt   time.Time `json:"last_good_at,omitzero"`
	// LastGoodCount is the entry count of the last healthy run, the
	// baseline for the count-delta health check.
	LastGoodCount int `json:"last_good_count,omitempty"`
	// BodySHA256 of the last fetched content, for staleness detection
	// (byte-identical content across runs).
	BodySHA256 string `json:"body_sha256,omitempty"`
	// UnchangedSince is when BodySHA256 was first seen.
	UnchangedSince time.Time `json:"unchanged_since,omitzero"`
	// FailingSince is zero while healthy; set on first FAILED run.
	FailingSince time.Time `json:"failing_since,omitzero"`
	LastStatus   string    `json:"last_status,omitempty"`
}

// Store loads and saves feed state and last-good prefix snapshots.
type Store interface {
	Load(feed string) (FeedState, bool, error)
	Save(feed string, s FeedState) error
	// LastGood returns the cached prefix list from the last healthy
	// run, used when a feed FAILs (never fail open).
	LastGood(feed string) ([]netip.Prefix, error)
	SaveLastGood(feed string, prefixes []netip.Prefix) error
}
