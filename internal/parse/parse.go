// Package parse turns raw feed bodies into prefixes. One parser per
// upstream format, selected per-feed via the catalog, with a Detect
// fallback that sniffs the first non-comment lines.
package parse

import (
	"io"
	"net/netip"
)

// Stats describes what a parser saw. RejectedRatio feeds the
// format-drift health check.
type Stats struct {
	LinesSeen    int // all lines, including blanks and comments
	CommentLines int
	Parsed       int // entries successfully converted to prefixes
	Rejected     int // non-comment, non-blank lines that failed to parse
}

// RejectedRatio returns rejected / (parsed + rejected), or 0 when the
// feed had no candidate lines.
func (s Stats) RejectedRatio() float64 {
	total := s.Parsed + s.Rejected
	if total == 0 {
		return 0
	}
	return float64(s.Rejected) / float64(total)
}

// Parser converts one feed body into prefixes. Both IPv4 and IPv6 are
// supported everywhere; bare IPs become /32 or /128. Parsers must be
// deterministic and must not sort or deduplicate (normalize owns that).
type Parser interface {
	Name() string
	Parse(r io.Reader) ([]netip.Prefix, Stats, error)
}
