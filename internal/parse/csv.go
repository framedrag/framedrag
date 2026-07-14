package parse

import (
	"encoding/csv"
	"io"
	"net/netip"
	"strings"
)

// csvParser handles CSV feeds with a header row. The address column
// index is configured per feed via Options.CSVColumn.
type csvParser struct {
	column int
}

// NewCSV returns a csv parser reading addresses from the given
// zero-based column. The first non-comment, non-blank line is treated
// as the header and counted in neither Parsed nor Rejected.
func NewCSV(column int) Parser { return &csvParser{column: column} }

func (*csvParser) Name() string { return "csv" }

func (p *csvParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	var out []netip.Prefix
	var st Stats
	header := true
	err := scanLines(r, &st, func(entry string) lineResult {
		if header {
			header = false
			return lineSkipped
		}
		field, ok := csvField(entry, p.column)
		if !ok {
			return lineRejected
		}
		pfx, ok := parseEntry(field)
		if !ok {
			return lineRejected
		}
		out = append(out, pfx)
		return lineParsed
	})
	return out, st, err
}

// csvField extracts one column from a single CSV record line,
// honoring quoting.
func csvField(line string, column int) (string, bool) {
	if column < 0 {
		return "", false
	}
	cr := csv.NewReader(strings.NewReader(line))
	cr.FieldsPerRecord = -1
	rec, err := cr.Read()
	if err != nil || column >= len(rec) {
		return "", false
	}
	return strings.TrimSpace(rec[column]), true
}
