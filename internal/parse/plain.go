package parse

import (
	"io"
	"net/netip"
)

// plainParser handles the common case: one CIDR or bare IP per line.
type plainParser struct{}

func (plainParser) Name() string { return "plain" }

func (plainParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	var out []netip.Prefix
	var st Stats
	err := scanLines(r, &st, func(entry string) lineResult {
		p, ok := parseEntry(entry)
		if !ok {
			return lineRejected
		}
		out = append(out, p)
		return lineParsed
	})
	return out, st, err
}

// spamhausParser handles Spamhaus DROP/EDROP: `CIDR ; SBL#####`. The
// shared scanner strips the `; SBL...` tail as an inline comment, so
// the remaining entry is a CIDR (bare IPs are tolerated).
type spamhausParser struct{}

func (spamhausParser) Name() string { return "spamhaus" }

func (spamhausParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	var out []netip.Prefix
	var st Stats
	err := scanLines(r, &st, func(entry string) lineResult {
		p, ok := parseEntry(entry)
		if !ok {
			return lineRejected
		}
		out = append(out, p)
		return lineParsed
	})
	return out, st, err
}
