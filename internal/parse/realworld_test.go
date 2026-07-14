package parse

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// Spamhaus moved DROP to JSON lines (drop_v4.json / drop_v6.json):
// one object per line with a "cidr" field, plus a trailing metadata
// object without one. Observed live 2026-07-14.
func TestSpamhausParsesJSONLines(t *testing.T) {
	body := `{"cidr":"1.10.16.0/20","sblid":"SBL256894","rir":"apnic"}
{"cidr":"1.19.0.0/16","sblid":"SBL434604","rir":"apnic"}
{"cidr":"2a06:e480::/29","sblid":"SBL399299","rir":"ripencc"}
{"type":"metadata","timestamp":1752526722,"size":1679}
`
	p, err := Get("spamhaus", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, st, err := p.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("1.10.16.0/20"),
		netip.MustParsePrefix("1.19.0.0/16"),
		netip.MustParsePrefix("2a06:e480::/29"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if st.Parsed != 3 || st.Rejected != 0 {
		t.Fatalf("stats = %+v (metadata line must not count as rejected)", st)
	}
}

func TestSpamhausClassicStillWorks(t *testing.T) {
	body := "1.2.3.0/24 ; SBL12345\n"
	p, _ := Get("spamhaus", Options{})
	got, st, err := p.Parse(strings.NewReader(body))
	if err != nil || len(got) != 1 || got[0] != netip.MustParsePrefix("1.2.3.0/24") || st.Parsed != 1 {
		t.Fatalf("got %v stats %+v err %v", got, st, err)
	}
}

func TestDetectPicksSpamhausForJSONLines(t *testing.T) {
	body := `{"cidr":"1.10.16.0/20","sblid":"SBL256894","rir":"apnic"}
{"cidr":"1.19.0.0/16","sblid":"SBL434604","rir":"apnic"}
`
	p, _ := Get("detect", Options{})
	got, _, err := p.Parse(strings.NewReader(body))
	if err != nil || len(got) != 2 {
		t.Fatalf("detect on JSON lines: got %v err %v", got, err)
	}
}

// ISC/DShield block.txt: tab-separated "start end netmask ..." rows
// after a comment banner. Observed live 2026-07-14.
func TestRangeParsesTabularStartEndNetmask(t *testing.T) {
	body := "#   DShield.org Recommended Block List\n" +
		"#    updated: 2026-07-14T20:45:40\n" +
		"198.51.100.0\t198.51.100.255\t24\t2076\tSomeNet\tUS\tabuse@example.com\n" +
		"203.0.113.0\t203.0.113.255\t24\t1550\tOtherNet\tCN\t\n"
	p, err := Get("range", Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, st, err := p.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if st.Parsed != 2 || st.Rejected != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestDetectPicksRangeForTabular(t *testing.T) {
	body := "198.51.100.0\t198.51.100.255\t24\t2076\tSomeNet\tUS\tabuse@example.com\n"
	p, _ := Get("detect", Options{})
	got, _, err := p.Parse(strings.NewReader(body))
	if err != nil || len(got) != 1 || got[0] != netip.MustParsePrefix("198.51.100.0/24") {
		t.Fatalf("detect on tabular: got %v err %v", got, err)
	}
}

func TestTabularJunkStillRejected(t *testing.T) {
	body := "banana\tapple\t24\n198.51.100.0\t203.0.113.255\tnope\n"
	p, _ := Get("range", Options{})
	got, st, err := p.Parse(strings.NewReader(body))
	if err != nil || len(got) != 0 || st.Rejected != 2 {
		t.Fatalf("got %v stats %+v err %v", got, st, err)
	}
}
