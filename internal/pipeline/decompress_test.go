package pipeline

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/netip"
	"testing"

	"framedrag.dev/framedrag/internal/fetch"
)

func gzipBody(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipBody(t *testing.T, name, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Several catalog feeds ship compressed (.gz, .zip); pfBlockerNG
// decompresses transparently and so do we.
func TestGzipBodyIsDecompressed(t *testing.T) {
	fk, _, tg, opts := newEnv(t)
	fk.Queue(feedA().URL, fetch.Result{Body: gzipBody(t, "198.51.100.0/24\n"), StatusCode: 200, FetchedAt: now})

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy {
		t.Fatalf("gzip feed unhealthy: %+v", res.Feeds)
	}
	want := []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	if !bytes.Equal([]byte(tg.applied[0][0].Prefixes[0].String()), []byte(want[0].String())) {
		t.Fatalf("prefixes = %v", tg.applied[0][0].Prefixes)
	}
}

func TestZipBodyIsDecompressed(t *testing.T) {
	fk, _, tg, opts := newEnv(t)
	fk.Queue(feedA().URL, fetch.Result{Body: zipBody(t, "list.txt", "203.0.113.0/24\n"), StatusCode: 200, FetchedAt: now})

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA())})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy {
		t.Fatalf("zip feed unhealthy: %+v", res.Feeds)
	}
	if got := tg.applied[0][0].Prefixes; len(got) != 1 || got[0] != netip.MustParsePrefix("203.0.113.0/24") {
		t.Fatalf("prefixes = %v", got)
	}
}

// A corrupt archive must degrade to a FAILED feed, never abort the run.
func TestCorruptGzipFailsFeedOnly(t *testing.T) {
	fk, _, _, opts := newEnv(t)
	fk.Queue(feedA().URL, fetch.Result{Body: []byte{0x1f, 0x8b, 0xff, 0x00}, StatusCode: 200, FetchedAt: now})
	fk.Queue(feedB().URL, body("198.51.100.0/24\n"))

	res, err := Run(context.Background(), opts, []AliasSpec{alias("fd_test", feedA(), feedB())})
	if err != nil {
		t.Fatalf("corrupt archive must not abort the run: %v", err)
	}
	if res.Healthy {
		t.Fatal("must be unhealthy")
	}
	fr := findFeed(t, res, "feed_a")
	if fr.Status.Healthy() {
		t.Fatalf("feed_a = %+v", fr)
	}
}
