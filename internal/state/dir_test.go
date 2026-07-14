package state

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewDir(dir)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	return s, dir
}

func TestFeedStateRoundTrip(t *testing.T) {
	s, dir := newTestStore(t)
	want := FeedState{
		ETag:           `"abc"`,
		LastModified:   "Mon, 13 Jul 2026 10:00:00 GMT",
		LastGoodAt:     time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC),
		LastGoodCount:  30000,
		BodySHA256:     "deadbeef",
		UnchangedSince: time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		LastStatus:     "OK",
	}
	if err := s.Save("spamhaus_drop", want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := s.Load("spamhaus_drop")
	if err != nil || !ok {
		t.Fatalf("Load = (_, %v, %v), want (_, true, nil)", ok, err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// Stable, indented JSON with a trailing newline for diffability.
	data, err := os.ReadFile(filepath.Join(dir, "feeds", "spamhaus_drop.json"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(data), "\n  \"etag\"") {
		t.Errorf("state file not indented:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("state file missing trailing newline")
	}
}

func TestLoadMissingFeed(t *testing.T) {
	s, _ := newTestStore(t)
	got, ok, err := s.Load("never_seen")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok {
		t.Error("ok = true, want false")
	}
	if got != (FeedState{}) {
		t.Errorf("got %+v, want zero FeedState", got)
	}
	prefixes, err := s.LastGood("never_seen")
	if err != nil || prefixes != nil {
		t.Errorf("LastGood = (%v, %v), want (nil, nil)", prefixes, err)
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	s, dir := newTestStore(t)
	path := filepath.Join(dir, "feeds", "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load("bad"); err == nil {
		t.Fatal("Load of corrupt JSON succeeded, want error")
	}
}

func TestLastGoodRoundTrip(t *testing.T) {
	s, dir := newTestStore(t)
	in := []netip.Prefix{
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("10.0.0.0/8"), // duplicate
		netip.MustParsePrefix("2001:db8:ffff::/48"),
	}
	if err := s.SaveLastGood("mixed", in); err != nil {
		t.Fatalf("SaveLastGood: %v", err)
	}
	got, err := s.LastGood("mixed")
	if err != nil {
		t.Fatalf("LastGood: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2001:db8:ffff::/48"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d prefixes %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prefix[%d] = %v, want %v", i, got[i], want[i])
		}
	}

	// File format: sorted, one per line, trailing newline.
	data, err := os.ReadFile(filepath.Join(dir, "lastgood", "mixed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantFile := "10.0.0.0/8\n192.0.2.0/24\n2001:db8::/32\n2001:db8:ffff::/48\n"
	if string(data) != wantFile {
		t.Errorf("file = %q, want %q", data, wantFile)
	}
}

func TestLastGoodCanonicalizesOnSave(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SaveLastGood("f", []netip.Prefix{netip.MustParsePrefix("192.0.2.77/24")}); err != nil {
		t.Fatalf("SaveLastGood: %v", err)
	}
	got, err := s.LastGood("f")
	if err != nil {
		t.Fatalf("LastGood: %v", err)
	}
	if len(got) != 1 || got[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Errorf("got %v, want [192.0.2.0/24]", got)
	}
}

func TestLastGoodRejectsBareIP(t *testing.T) {
	s, dir := newTestStore(t)
	path := filepath.Join(dir, "lastgood", "bare.txt")
	if err := os.WriteFile(path, []byte("10.0.0.0/8\n192.0.2.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastGood("bare"); err == nil {
		t.Fatal("LastGood accepted a bare-IP line, want error")
	}
}

func TestLastGoodRejectsUnmaskedPrefix(t *testing.T) {
	s, dir := newTestStore(t)
	path := filepath.Join(dir, "lastgood", "unmasked.txt")
	if err := os.WriteFile(path, []byte("192.0.2.77/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastGood("unmasked"); err == nil {
		t.Fatal("LastGood accepted a non-canonical prefix, want error")
	}
}

func TestNameSanitizationAndCollisions(t *testing.T) {
	s, dir := newTestStore(t)

	// Names that sanitize to the same base must not collide.
	pairs := []struct{ a, b string }{
		{"a/b", "a_b"},
		{"Feed", "feed"}, // case-insensitive filesystems
		{"x:y", "x/y"},
		{"../../etc/passwd", ".._.._etc_passwd"},
	}
	for _, p := range pairs {
		if err := s.Save(p.a, FeedState{LastStatus: "A"}); err != nil {
			t.Fatalf("Save(%q): %v", p.a, err)
		}
		if err := s.Save(p.b, FeedState{LastStatus: "B"}); err != nil {
			t.Fatalf("Save(%q): %v", p.b, err)
		}
		ga, _, err := s.Load(p.a)
		if err != nil {
			t.Fatalf("Load(%q): %v", p.a, err)
		}
		gb, _, err := s.Load(p.b)
		if err != nil {
			t.Fatalf("Load(%q): %v", p.b, err)
		}
		if ga.LastStatus != "A" || gb.LastStatus != "B" {
			t.Errorf("collision between %q and %q: got %q/%q", p.a, p.b, ga.LastStatus, gb.LastStatus)
		}
	}

	// Every file stays inside <dir>/feeds.
	entries, err := os.ReadDir(filepath.Join(dir, "feeds"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("unexpected directory %q in feeds/", e.Name())
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			t.Errorf("unexpected file %q in feeds/", e.Name())
		}
	}
}

func TestSaveOverExisting(t *testing.T) {
	s, dir := newTestStore(t)
	if err := s.Save("feed", FeedState{LastStatus: "OK", LastGoodCount: 1}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := s.Save("feed", FeedState{LastStatus: "FAILED", LastGoodCount: 2}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, ok, err := s.Load("feed")
	if err != nil || !ok {
		t.Fatalf("Load = (_, %v, %v)", ok, err)
	}
	if got.LastStatus != "FAILED" || got.LastGoodCount != 2 {
		t.Errorf("got %+v, want the second write", got)
	}

	if err := s.SaveLastGood("feed", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); err != nil {
		t.Fatalf("first SaveLastGood: %v", err)
	}
	if err := s.SaveLastGood("feed", []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}); err != nil {
		t.Fatalf("second SaveLastGood: %v", err)
	}
	prefixes, err := s.LastGood("feed")
	if err != nil {
		t.Fatalf("LastGood: %v", err)
	}
	if len(prefixes) != 1 || prefixes[0] != netip.MustParsePrefix("192.0.2.0/24") {
		t.Errorf("got %v, want the second write", prefixes)
	}

	// Atomic writes leave no temp files behind.
	for _, sub := range []string{"feeds", "lastgood"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				t.Errorf("leftover temp file %s/%s", sub, e.Name())
			}
		}
	}
}

func TestEmptyLastGood(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SaveLastGood("empty", nil); err != nil {
		t.Fatalf("SaveLastGood(nil): %v", err)
	}
	got, err := s.LastGood("empty")
	if err != nil {
		t.Fatalf("LastGood: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}
