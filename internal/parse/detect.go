package parse

import (
	"bufio"
	"bytes"
	"io"
	"net/netip"
	"strings"
)

// sniffLines is how many non-comment candidate lines detect inspects
// before committing to a parser.
const sniffLines = 20

// detectParser is the fallback for feeds with no declared format: it
// sniffs the first sniffLines candidate lines and delegates to the
// best-matching registered parser. When nothing matches (an HTML error
// page, say) it falls back to plain, which rejects every line and
// parses zero entries, exactly what the health layer needs to see.
type detectParser struct {
	opts Options
}

// NewDetect returns the format-sniffing fallback parser. Options are
// used when detection lands on csv and column sniffing is
// inconclusive.
func NewDetect(opts Options) Parser { return &detectParser{opts: opts} }

func (*detectParser) Name() string { return "detect" }

func (d *detectParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Stats{}, err
	}
	return d.sniff(data).Parse(bytes.NewReader(data))
}

// sniff picks a parser from the first sniffLines candidate lines.
// Every line gets exactly one label; the winner is the parser covering
// the most lines, knowing that range also handles plain and spamhaus
// entries, and that spamhaus lines are plain entries once the scanner
// strips the `; SBL...` tail.
func (d *detectParser) sniff(data []byte) Parser {
	var plainV, rangeV, spamV, csvV int
	colVotes := map[int]int{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	seen := 0
	for sc.Scan() && seen < sniffLines {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || raw[0] == '#' || raw[0] == ';' {
			continue
		}
		seen++
		if looksSpamhaus(raw) {
			spamV++
			continue
		}
		entry, _ := stripLine(raw)
		if _, ok := parseEntry(entry); ok {
			plainV++
			continue
		}
		if _, ok := parseRangeEntry(entry); ok {
			rangeV++
			continue
		}
		if col, ok := sniffCSVColumn(entry); ok {
			csvV++
			colVotes[col]++
		}
	}

	lineCover := rangeV + plainV + spamV
	switch {
	case lineCover == 0 && csvV == 0:
		return plainParser{}
	case lineCover >= csvV:
		if rangeV > 0 {
			return rangeParser{}
		}
		if spamV > 0 {
			return spamhausParser{}
		}
		return plainParser{}
	default:
		return NewCSV(bestColumn(colVotes, d.opts.CSVColumn))
	}
}

// looksSpamhaus reports whether a raw candidate line matches the
// Spamhaus DROP shape `CIDR ; SBL#####`.
func looksSpamhaus(s string) bool {
	left, right, found := strings.Cut(s, ";")
	if !found || !strings.HasPrefix(strings.TrimSpace(right), "SBL") {
		return false
	}
	_, err := netip.ParsePrefix(strings.TrimSpace(left))
	return err == nil
}

// sniffCSVColumn reports the first CSV field of the line that parses
// as an address or CIDR.
func sniffCSVColumn(entry string) (int, bool) {
	if !strings.Contains(entry, ",") {
		return 0, false
	}
	for i := 0; ; i++ {
		field, ok := csvField(entry, i)
		if !ok {
			return 0, false
		}
		if _, ok := parseEntry(field); ok {
			return i, true
		}
	}
}

// bestColumn picks the most-voted column, lowest index on ties, or the
// configured fallback when there are no votes.
func bestColumn(votes map[int]int, fallback int) int {
	best, bestN := fallback, 0
	for col, n := range votes {
		if n > bestN || (n == bestN && bestN > 0 && col < best) {
			best, bestN = col, n
		}
	}
	return best
}
