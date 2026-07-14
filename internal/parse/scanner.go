package parse

import (
	"bufio"
	"io"
	"net/netip"
	"strings"
)

// lineResult is what a per-entry callback tells the scanner about one
// candidate line.
type lineResult int

const (
	lineParsed   lineResult = iota // entry converted to one or more prefixes
	lineRejected                   // candidate line that failed to parse
	lineSkipped                    // structural line (e.g. CSV header), counted in neither
)

// scanLines reads r line by line and drives Stats. Blank lines and
// full-line comments (starting with '#' or ';') are counted and
// skipped. Inline comments after an entry are stripped before fn sees
// the entry. CRLF endings are handled. fn reports whether the entry
// parsed; scanLines updates Parsed/Rejected accordingly.
func scanLines(r io.Reader, stats *Stats, fn func(entry string) lineResult) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			stats.LinesSeen++
			entry, isComment := stripLine(line)
			switch {
			case isComment:
				stats.CommentLines++
			case entry == "":
				// blank line: seen, nothing else
			default:
				switch fn(entry) {
				case lineParsed:
					stats.Parsed++
				case lineRejected:
					stats.Rejected++
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// stripLine trims one raw line down to its entry. It reports a
// full-line comment via isComment; a returned empty entry with
// isComment false means a blank line.
func stripLine(line string) (entry string, isComment bool) {
	s := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
	if s == "" {
		return "", false
	}
	if s[0] == '#' || s[0] == ';' {
		return "", true
	}
	if i := strings.IndexAny(s, "#;"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s, false
}

// parseEntry parses one bare IP or CIDR. Bare IPs become /32 or /128.
// IPv6 zones are stripped and 4-in-6 mapped addresses are unmapped;
// CIDRs are kept verbatim (normalize owns canonicalization).
func parseEntry(s string) (netip.Prefix, bool) {
	if s == "" || strings.ContainsAny(s, " \t") {
		return netip.Prefix{}, false
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p, true
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), true
}
