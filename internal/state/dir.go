package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// dirStore is the on-disk Store. Layout:
//
//	<dir>/feeds/<safe-feed-name>.json    stable, indented FeedState
//	<dir>/lastgood/<safe-feed-name>.txt  one canonical prefix per line,
//	                                     sorted, trailing newline
//
// All writes are atomic (temp file in the same directory + rename).
type dirStore struct {
	feeds    string
	lastgood string
}

// NewDir opens (creating if needed) an on-disk Store rooted at dir.
func NewDir(dir string) (Store, error) {
	s := &dirStore{
		feeds:    filepath.Join(dir, "feeds"),
		lastgood: filepath.Join(dir, "lastgood"),
	}
	for _, d := range []string{s.feeds, s.lastgood} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("state: create %s: %w", d, err)
		}
	}
	return s, nil
}

func (s *dirStore) feedPath(feed string) string {
	return filepath.Join(s.feeds, safeName(feed)+".json")
}

func (s *dirStore) lastGoodPath(feed string) string {
	return filepath.Join(s.lastgood, safeName(feed)+".txt")
}

// Load returns the stored FeedState for feed. When no state exists it
// returns (zero, false, nil). A corrupt state file is an error, never
// a silent reset.
func (s *dirStore) Load(feed string) (FeedState, bool, error) {
	path := s.feedPath(feed)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FeedState{}, false, nil
	}
	if err != nil {
		return FeedState{}, false, fmt.Errorf("state: read %s: %w", path, err)
	}
	var st FeedState
	if err := json.Unmarshal(data, &st); err != nil {
		return FeedState{}, false, fmt.Errorf("state: corrupt feed state for %q at %s: %w", feed, path, err)
	}
	return st, true, nil
}

// Save writes the FeedState for feed atomically as stable, indented
// JSON.
func (s *dirStore) Save(feed string, st FeedState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode feed state for %q: %w", feed, err)
	}
	data = append(data, '\n')
	return writeAtomic(s.feedPath(feed), data)
}

// LastGood returns the cached prefix list from the last healthy run.
// When no snapshot exists it returns (nil, nil). Every line must be a
// canonical CIDR prefix; a bare IP or unmasked prefix is an error.
func (s *dirStore) LastGood(feed string) ([]netip.Prefix, error) {
	path := s.lastGoodPath(feed)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	prefixes := make([]netip.Prefix, 0, len(lines))
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			continue // trailing newline
		}
		p, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, fmt.Errorf("state: corrupt last-good snapshot %s line %d: %w", path, i+1, err)
		}
		if p.Masked() != p {
			return nil, fmt.Errorf("state: corrupt last-good snapshot %s line %d: %q is not a canonical prefix", path, i+1, line)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

// SaveLastGood writes the prefix snapshot for feed atomically:
// canonical (masked) prefixes, deduplicated, sorted, one per line with
// a trailing newline.
func (s *dirStore) SaveLastGood(feed string, prefixes []netip.Prefix) error {
	canon := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		if !p.IsValid() {
			return fmt.Errorf("state: invalid prefix %v for feed %q", p, feed)
		}
		canon = append(canon, p.Masked())
	}
	slices.SortFunc(canon, comparePrefix)
	canon = slices.Compact(canon)

	var b strings.Builder
	for _, p := range canon {
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	return writeAtomic(s.lastGoodPath(feed), []byte(b.String()))
}

// comparePrefix orders by address (IPv4 before IPv6), then by prefix
// length.
func comparePrefix(a, b netip.Prefix) int {
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c
	}
	return a.Bits() - b.Bits()
}

// writeAtomic writes data to path via a temp file in the same
// directory plus rename, so readers never observe a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("state: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("state: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("state: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("state: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// safeName maps a feed name to a filesystem-safe file stem. Lowercase
// letters, digits, '.', '-' and '_' pass through (lowercased, so names
// stay distinct on case-insensitive filesystems); everything else
// becomes '_'. Whenever the result differs from the original, a short
// hash of the original is appended so distinct feed names can never
// collide.
func safeName(feed string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(feed) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	const maxLen = 100
	if len(name) > maxLen {
		name = name[:maxLen]
	}
	if name == "" || name[0] == '.' {
		name = "_" + name
	}
	if name == feed {
		return name
	}
	sum := sha256.Sum256([]byte(feed))
	return name + "-" + hex.EncodeToString(sum[:4])
}
