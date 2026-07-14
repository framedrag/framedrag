package parse

import (
	"encoding/json"
	"io"
	"net/netip"
	"strings"
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

// spamhausParser handles both Spamhaus DROP generations: the classic
// `CIDR ; SBL#####` text (the scanner strips the `; SBL...` tail as an
// inline comment) and the JSON-lines format of drop_v4.json /
// drop_v6.json, one object per line with a "cidr" field plus a
// trailing metadata object without one.
type spamhausParser struct{}

func (spamhausParser) Name() string { return "spamhaus" }

func (spamhausParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	var out []netip.Prefix
	var st Stats
	err := scanLines(r, &st, func(entry string) lineResult {
		if strings.HasPrefix(entry, "{") {
			p, res := parseSpamhausJSON(entry)
			if res == lineParsed {
				out = append(out, p)
			}
			return res
		}
		p, ok := parseEntry(entry)
		if !ok {
			return lineRejected
		}
		out = append(out, p)
		return lineParsed
	})
	return out, st, err
}

// parseSpamhausJSON handles one JSON-lines object. Objects without a
// "cidr" field (the metadata trailer) are structural, not rejects.
func parseSpamhausJSON(entry string) (netip.Prefix, lineResult) {
	var obj struct {
		Cidr string `json:"cidr"`
	}
	if err := json.Unmarshal([]byte(entry), &obj); err != nil {
		return netip.Prefix{}, lineRejected
	}
	if obj.Cidr == "" {
		return netip.Prefix{}, lineSkipped
	}
	p, err := netip.ParsePrefix(obj.Cidr)
	if err != nil {
		return netip.Prefix{}, lineRejected
	}
	return p, lineParsed
}
