package parse

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustPrefixes(t *testing.T, ss []string) []netip.Prefix {
	t.Helper()
	if len(ss) == 0 {
		return nil
	}
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func openTestdata(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func equalPrefixes(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGolden runs each parser over its testdata sample and asserts the
// exact prefixes, in input order, and the exact Stats.
func TestGolden(t *testing.T) {
	tests := []struct {
		name   string
		parser string
		opts   Options
		file   string
		want   []string
		stats  Stats
	}{
		{
			name:   "plain v4+v6 with comments and junk",
			parser: "plain",
			file:   "plain_v4v6.txt",
			want: []string{
				"203.0.113.0/24",
				"198.51.100.7/32",
				"2001:db8:beef::/48",
				"2001:db8::1/128",
				"192.0.2.128/25",
			},
			stats: Stats{LinesSeen: 12, CommentLines: 3, Parsed: 5, Rejected: 2},
		},
		{
			name:   "range feed with mixed plain entries",
			parser: "range",
			file:   "ranges.txt",
			want: []string{
				"192.0.2.0/24",
				"198.51.100.10/31",
				"198.51.100.12/31",
				"203.0.113.5/32",
				"10.0.0.0/8",
				"2001:db8::/126",
			},
			stats: Stats{LinesSeen: 7, CommentLines: 1, Parsed: 5, Rejected: 1},
		},
		{
			name:   "csv with header, empty and junk fields",
			parser: "csv",
			opts:   Options{CSVColumn: 1},
			file:   "feed.csv",
			want: []string{
				"203.0.113.9/32",
				"198.51.100.0/26",
				"2001:db8:bad::/48",
			},
			stats: Stats{LinesSeen: 7, CommentLines: 1, Parsed: 3, Rejected: 2},
		},
		{
			name:   "spamhaus drop",
			parser: "spamhaus",
			file:   "spamhaus_drop.txt",
			want: []string{
				"1.2.3.0/24",
				"5.6.4.0/22",
				"2001:db8:dead::/48",
			},
			stats: Stats{LinesSeen: 6, CommentLines: 3, Parsed: 3, Rejected: 0},
		},
		{
			name:   "emerging threats style plain",
			parser: "plain",
			file:   "emerging_threats.txt",
			want: []string{
				"1.10.16.0/20",
				"1.19.0.0/16",
				"2.56.192.0/22",
				"89.248.165.1/32",
			},
			stats: Stats{LinesSeen: 6, CommentLines: 2, Parsed: 4, Rejected: 0},
		},
		{
			name:   "html error page parses zero entries",
			parser: "plain",
			file:   "html_error.html",
			want:   nil,
			stats:  Stats{LinesSeen: 8, CommentLines: 0, Parsed: 0, Rejected: 8},
		},
		{
			name:   "cloudflare challenge via detect parses zero entries",
			parser: "detect",
			file:   "cloudflare_challenge.html",
			want:   nil,
			stats:  Stats{LinesSeen: 13, CommentLines: 0, Parsed: 0, Rejected: 13},
		},
		{
			name:   "crlf line endings",
			parser: "plain",
			file:   "crlf.txt",
			want: []string{
				"203.0.113.1/32",
				"2001:db8::/32",
			},
			stats: Stats{LinesSeen: 4, CommentLines: 1, Parsed: 2, Rejected: 1},
		},
		{
			name:   "empty file",
			parser: "plain",
			file:   "empty.txt",
			want:   nil,
			stats:  Stats{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Get(tt.parser, tt.opts)
			if err != nil {
				t.Fatalf("Get(%q): %v", tt.parser, err)
			}
			got, stats, err := p.Parse(openTestdata(t, tt.file))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if want := mustPrefixes(t, tt.want); !equalPrefixes(got, want) {
				t.Errorf("prefixes:\n got %v\nwant %v", got, want)
			}
			if stats != tt.stats {
				t.Errorf("stats:\n got %+v\nwant %+v", stats, tt.stats)
			}
		})
	}
}

// TestDetectDelegation checks that detect sniffs the right underlying
// format by producing byte-identical results to the explicit parser.
func TestDetectDelegation(t *testing.T) {
	tests := []struct {
		file   string
		parser string
		opts   Options
	}{
		{"plain_v4v6.txt", "plain", Options{}},
		{"ranges.txt", "range", Options{}},
		{"feed.csv", "csv", Options{CSVColumn: 1}},
		{"spamhaus_drop.txt", "spamhaus", Options{}},
		{"emerging_threats.txt", "plain", Options{}},
		{"html_error.html", "plain", Options{}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			want, err := Get(tt.parser, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			wantP, wantS, err := want.Parse(openTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			det, err := Get("detect", Options{})
			if err != nil {
				t.Fatal(err)
			}
			gotP, gotS, err := det.Parse(openTestdata(t, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if !equalPrefixes(gotP, wantP) {
				t.Errorf("prefixes:\n got %v\nwant %v", gotP, wantP)
			}
			if gotS != wantS {
				t.Errorf("stats:\n got %+v\nwant %+v", gotS, wantS)
			}
		})
	}
}

func TestRangeToPrefixes(t *testing.T) {
	tests := []struct {
		start, end string
		want       []string
	}{
		{"192.0.2.1", "192.0.2.10", []string{
			"192.0.2.1/32", "192.0.2.2/31", "192.0.2.4/30", "192.0.2.8/31", "192.0.2.10/32",
		}},
		{"192.0.2.0", "192.0.2.255", []string{"192.0.2.0/24"}},
		{"10.1.2.3", "10.1.2.3", []string{"10.1.2.3/32"}},
		{"0.0.0.0", "255.255.255.255", []string{"0.0.0.0/0"}},
		{"255.255.255.254", "255.255.255.255", []string{"255.255.255.254/31"}},
		{"2001:db8::", "2001:db8::3", []string{"2001:db8::/126"}},
		{"2001:db8::ffff", "2001:db8::1:0", []string{"2001:db8::ffff/128", "2001:db8::1:0/128"}},
		{"::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", []string{"::/0"}},
		{"2001:db8::", "2001:db8:0:0:ffff:ffff:ffff:ffff", []string{"2001:db8::/64"}},
	}
	for _, tt := range tests {
		got := rangeToPrefixes(netip.MustParseAddr(tt.start), netip.MustParseAddr(tt.end))
		if want := mustPrefixes(t, tt.want); !equalPrefixes(got, want) {
			t.Errorf("range %s-%s:\n got %v\nwant %v", tt.start, tt.end, got, want)
		}
	}
}

func TestParseRangeEntryRejects(t *testing.T) {
	for _, s := range []string{
		"192.0.2.9-192.0.2.5",   // start > end
		"192.0.2.1-2001:db8::1", // mixed families
		"192.0.2.1-",            // missing end
		"-192.0.2.1",            // missing start
		"192.0.2.x-192.0.2.5",   // junk start
		"not a range at all",    // junk
	} {
		if _, ok := parseRangeEntry(s); ok {
			t.Errorf("parseRangeEntry(%q) = ok, want reject", s)
		}
	}
}

func TestLooksLikeHTML(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"<!DOCTYPE html>\n<html>", true},
		{"<html lang=\"en\">", true},
		{"  \r\n\t<HTML>", true},
		{"\xef\xbb\xbf<html>", true},
		{"<?xml version=\"1.0\"?>", true},
		{"<!-- maintenance -->", true},
		{"# comment\n1.2.3.4", false},
		{"1.2.3.0/24\n", false},
		{"", false},
		{"< 1.2.3.4", false},
	}
	for _, tt := range tests {
		if got := LooksLikeHTML([]byte(tt.in)); got != tt.want {
			t.Errorf("LooksLikeHTML(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRejectedRatio(t *testing.T) {
	tests := []struct {
		stats Stats
		want  float64
	}{
		{Stats{}, 0},
		{Stats{LinesSeen: 5, CommentLines: 5}, 0},
		{Stats{Parsed: 3, Rejected: 1}, 0.25},
		{Stats{Parsed: 0, Rejected: 8}, 1},
		{Stats{Parsed: 9, Rejected: 1}, 0.1},
	}
	for _, tt := range tests {
		if got := tt.stats.RejectedRatio(); got != tt.want {
			t.Errorf("RejectedRatio(%+v) = %v, want %v", tt.stats, got, tt.want)
		}
	}
}

func TestInlineCommentsAndWhitespace(t *testing.T) {
	in := "  198.51.100.1  \t# host\n203.0.113.0/24;alias\n\t \n;full comment\n"
	p, err := Get("plain", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, stats, err := p.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := mustPrefixes(t, []string{"198.51.100.1/32", "203.0.113.0/24"})
	if !equalPrefixes(got, want) {
		t.Errorf("prefixes:\n got %v\nwant %v", got, want)
	}
	if wantS := (Stats{LinesSeen: 4, CommentLines: 1, Parsed: 2, Rejected: 0}); stats != wantS {
		t.Errorf("stats:\n got %+v\nwant %+v", stats, wantS)
	}
}

// TestNoTrailingNewline makes sure the last line still counts when the
// feed does not end in a newline.
func TestNoTrailingNewline(t *testing.T) {
	p, err := Get("plain", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, stats, err := p.Parse(strings.NewReader("192.0.2.1\n192.0.2.2"))
	if err != nil {
		t.Fatal(err)
	}
	want := mustPrefixes(t, []string{"192.0.2.1/32", "192.0.2.2/32"})
	if !equalPrefixes(got, want) {
		t.Errorf("prefixes:\n got %v\nwant %v", got, want)
	}
	if wantS := (Stats{LinesSeen: 2, Parsed: 2}); stats != wantS {
		t.Errorf("stats:\n got %+v\nwant %+v", stats, wantS)
	}
}

// TestNoSortNoDedup: parsers must preserve input order and keep exact
// duplicates; normalize owns sorting and deduplication.
func TestNoSortNoDedup(t *testing.T) {
	in := "203.0.113.9\n192.0.2.0/24\n203.0.113.9\n"
	p, err := Get("plain", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := p.Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := mustPrefixes(t, []string{"203.0.113.9/32", "192.0.2.0/24", "203.0.113.9/32"})
	if !equalPrefixes(got, want) {
		t.Errorf("prefixes:\n got %v\nwant %v", got, want)
	}
}

func TestRegistry(t *testing.T) {
	if _, err := Get("nope", Options{}); err == nil {
		t.Error("Get(nope) succeeded, want error")
	}
	for _, name := range []string{"plain", "range", "csv", "spamhaus", "detect"} {
		p, err := Get(name, Options{CSVColumn: 1})
		if err != nil {
			t.Errorf("Get(%q): %v", name, err)
			continue
		}
		if p.Name() != name {
			t.Errorf("Get(%q).Name() = %q", name, p.Name())
		}
	}
}
