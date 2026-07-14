package target

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func mustPrefixes(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

func newFileTarget(t *testing.T, dir, serve string, opts ...Option) *fileTarget {
	t.Helper()
	tg, err := NewFile(dir, serve, opts...)
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	return tg.(*fileTarget)
}

// snapshot returns filename -> content for every file in dir, so tests
// can prove DryRun writes nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return m
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = string(b)
	}
	return m
}

func kinds(rep Report) map[string]string {
	m := map[string]string{}
	for _, c := range rep.Changes {
		m[filepath.Base(c.Object)] = c.Kind
	}
	return m
}

func TestNewFileValidation(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		serve   string
		opts    []Option
		wantErr bool
	}{
		{name: "empty dir", dir: "", serve: "", wantErr: true},
		{name: "no serve", dir: "/lists", serve: ""},
		{name: "loopback IPv4", dir: "/lists", serve: "127.0.0.1:8080"},
		{name: "loopback IPv4 non-standard", dir: "/lists", serve: "127.1.2.3:8080"},
		{name: "loopback IPv6", dir: "/lists", serve: "[::1]:8080"},
		{name: "wildcard rejected", dir: "/lists", serve: "0.0.0.0:8080", wantErr: true},
		{name: "LAN rejected", dir: "/lists", serve: "192.168.1.5:8080", wantErr: true},
		{name: "hostname rejected", dir: "/lists", serve: "localhost:8080", wantErr: true},
		{name: "missing port rejected", dir: "/lists", serve: "127.0.0.1", wantErr: true},
		{name: "non-loopback with explicit option", dir: "/lists", serve: "0.0.0.0:8080",
			opts: []Option{AllowNonLoopback()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewFile(tt.dir, tt.serve, tt.opts...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewFile(%q, %q) error = %v, wantErr %v", tt.dir, tt.serve, err, tt.wantErr)
			}
		})
	}
}

func TestApplyFreshDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lists") // does not exist yet
	f := newFileTarget(t, dir, "")
	sets := []AliasSet{
		{Name: "fd_pri1", Action: "deny", Direction: "both",
			Prefixes: mustPrefixes(t, "1.2.3.0/24", "5.6.7.8/32")},
		{Name: "fd_v6", Action: "deny", Direction: "in",
			Prefixes: mustPrefixes(t, "2001:db8::/32", "2001:db8:ffff::1/128")},
	}
	rep, err := f.Apply(context.Background(), sets)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.DryRun {
		t.Error("Apply report has DryRun=true")
	}
	if rep.Target != "file" {
		t.Errorf("Report.Target = %q, want %q", rep.Target, "file")
	}
	want := map[string]string{"fd_pri1.txt": "create", "fd_v6.txt": "create"}
	if got := kinds(rep); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if rep.Changes[0].Detail != "2 entries" {
		t.Errorf("create Detail = %q, want %q", rep.Changes[0].Detail, "2 entries")
	}
	got, err := os.ReadFile(filepath.Join(dir, "fd_pri1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1.2.3.0/24\n5.6.7.8/32\n" {
		t.Errorf("fd_pri1.txt = %q; want one prefix per line, input order, trailing newline", got)
	}
	got6, err := os.ReadFile(filepath.Join(dir, "fd_v6.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got6) != "2001:db8::/32\n2001:db8:ffff::1/128\n" {
		t.Errorf("fd_v6.txt = %q", got6)
	}
}

func TestApplySecondRunUnchanged(t *testing.T) {
	dir := t.TempDir()
	f := newFileTarget(t, dir, "")
	sets := []AliasSet{{Name: "fd_a", Prefixes: mustPrefixes(t, "1.2.3.0/24")}}
	if _, err := f.Apply(context.Background(), sets); err != nil {
		t.Fatal(err)
	}
	rep, err := f.Apply(context.Background(), sets)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Kind != "unchanged" {
		t.Fatalf("second run changes = %+v, want single unchanged", rep.Changes)
	}
	if rep.Changes[0].Detail != "1 entries" {
		t.Errorf("Detail = %q", rep.Changes[0].Detail)
	}
}

func TestApplyUpdateDiffCounts(t *testing.T) {
	dir := t.TempDir()
	f := newFileTarget(t, dir, "")
	old := []AliasSet{{Name: "fd_a",
		Prefixes: mustPrefixes(t, "1.1.1.0/24", "2.2.2.0/24", "3.3.3.0/24")}}
	if _, err := f.Apply(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	// Keep 1.1.1.0/24, drop two, add three.
	updated := []AliasSet{{Name: "fd_a",
		Prefixes: mustPrefixes(t, "1.1.1.0/24", "4.4.4.0/24", "5.5.5.0/24", "2001:db8::/48")}}
	rep, err := f.Apply(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Kind != "update" {
		t.Fatalf("changes = %+v, want single update", rep.Changes)
	}
	want := "4 entries (+3 -2 vs previous)"
	if rep.Changes[0].Detail != want {
		t.Errorf("Detail = %q, want %q", rep.Changes[0].Detail, want)
	}
}

func TestApplyDeletesStaleTxtOnly(t *testing.T) {
	dir := t.TempDir()
	// A stale list from a previous run, plus files that are not ours.
	if err := os.WriteFile(filepath.Join(dir, "fd_old.txt"), []byte("9.9.9.0/24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFileTarget(t, dir, "")
	rep, err := f.Apply(context.Background(), []AliasSet{
		{Name: "fd_new", Prefixes: mustPrefixes(t, "1.2.3.0/24")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"fd_new.txt": "create", "fd_old.txt": "delete"}
	if got := kinds(rep); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "fd_old.txt")); !os.IsNotExist(err) {
		t.Error("stale fd_old.txt still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Error("non-.txt file notes.md was touched")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub.txt")); err != nil {
		t.Error("directory sub.txt was touched")
	}
	for _, c := range rep.Changes {
		if c.Kind == "delete" && c.Detail != "stale, 1 entries removed" {
			t.Errorf("delete Detail = %q", c.Detail)
		}
	}
}

// TestDryRunApplyParity is the property test: across several scenario
// steps, DryRun must write nothing and must report exactly the changes
// the immediately following Apply reports.
func TestDryRunApplyParity(t *testing.T) {
	dir := t.TempDir()
	// Seed a stale file so the first step exercises delete too.
	if err := os.WriteFile(filepath.Join(dir, "fd_stale.txt"), []byte("8.8.8.8/32\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFileTarget(t, dir, "")
	steps := [][]AliasSet{
		{ // create + delete
			{Name: "fd_a", Prefixes: mustPrefixes(t, "1.1.1.0/24", "2.2.2.0/24")},
			{Name: "fd_b", Prefixes: mustPrefixes(t, "2001:db8::/32")},
		},
		{ // unchanged + unchanged
			{Name: "fd_a", Prefixes: mustPrefixes(t, "1.1.1.0/24", "2.2.2.0/24")},
			{Name: "fd_b", Prefixes: mustPrefixes(t, "2001:db8::/32")},
		},
		{ // update + delete + create
			{Name: "fd_a", Prefixes: mustPrefixes(t, "1.1.1.0/24", "3.3.3.0/24")},
			{Name: "fd_c", Prefixes: mustPrefixes(t, "10.0.0.0/8")},
		},
		{ // empty set list: everything deleted
		},
	}
	ctx := context.Background()
	for i, sets := range steps {
		before := snapshot(t, dir)
		dry, err := f.DryRun(ctx, sets)
		if err != nil {
			t.Fatalf("step %d DryRun: %v", i, err)
		}
		if !dry.DryRun {
			t.Errorf("step %d: DryRun report has DryRun=false", i)
		}
		if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
			t.Fatalf("step %d: DryRun modified the directory:\nbefore %v\nafter  %v", i, before, after)
		}
		app, err := f.Apply(ctx, sets)
		if err != nil {
			t.Fatalf("step %d Apply: %v", i, err)
		}
		if app.DryRun {
			t.Errorf("step %d: Apply report has DryRun=true", i)
		}
		if !reflect.DeepEqual(dry.Changes, app.Changes) {
			t.Errorf("step %d: DryRun/Apply changes differ:\ndry:   %+v\napply: %+v", i, dry.Changes, app.Changes)
		}
	}
	if left := snapshot(t, dir); len(left) != 0 {
		t.Errorf("after final empty step, files remain: %v", left)
	}
}

func TestApplyRejectsBadSetNames(t *testing.T) {
	f := newFileTarget(t, t.TempDir(), "")
	for _, name := range []string{"", "../evil", "a/b", ".hidden"} {
		if _, err := f.Apply(context.Background(), []AliasSet{{Name: name}}); err == nil {
			t.Errorf("Apply accepted set name %q", name)
		}
	}
	// Duplicate names are ambiguous: reject.
	sets := []AliasSet{{Name: "fd_a"}, {Name: "fd_a"}}
	if _, err := f.Apply(context.Background(), sets); err == nil {
		t.Error("Apply accepted duplicate set names")
	}
}

func TestReportDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"fd_z.txt", "fd_y.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("9.9.9.9/32\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := newFileTarget(t, dir, "")
	rep, err := f.DryRun(context.Background(), []AliasSet{
		{Name: "fd_b", Prefixes: mustPrefixes(t, "1.1.1.1/32")},
		{Name: "fd_a", Prefixes: mustPrefixes(t, "2.2.2.2/32")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range rep.Changes {
		got = append(got, filepath.Base(c.Object))
	}
	// Sets in input order, then stale deletions sorted by name.
	want := []string{"fd_b.txt", "fd_a.txt", "fd_y.txt", "fd_z.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("change order = %v, want %v", got, want)
	}
	if !sort.StringsAreSorted(got[2:]) {
		t.Errorf("deletions not sorted: %v", got[2:])
	}
}

func TestServeHandler(t *testing.T) {
	dir := t.TempDir()
	f := newFileTarget(t, dir, "127.0.0.1:0")
	if _, err := f.Apply(context.Background(), []AliasSet{
		{Name: "fd_a", Prefixes: mustPrefixes(t, "1.2.3.0/24", "2001:db8::/32")},
	}); err != nil {
		t.Fatal(err)
	}
	// A file outside the served directory that traversal must not reach.
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-.txt file inside the directory that must not be served.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := f.handler()
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "existing list", method: "GET", path: "/fd_a.txt", wantStatus: 200},
		{name: "HEAD existing list", method: "HEAD", path: "/fd_a.txt", wantStatus: 200},
		{name: "root: no listing", method: "GET", path: "/", wantStatus: 404},
		{name: "missing list", method: "GET", path: "/fd_nope.txt", wantStatus: 404},
		{name: "traversal ../", method: "GET", path: "/../secret.txt", wantStatus: 404},
		{name: "traversal encoded", method: "GET", path: "/..%2Fsecret.txt", wantStatus: 404},
		{name: "traversal deep", method: "GET", path: "/x/../../secret.txt", wantStatus: 404},
		{name: "subpath", method: "GET", path: "/sub/fd_a.txt", wantStatus: 404},
		{name: "non-txt file", method: "GET", path: "/state.json", wantStatus: 404},
		{name: "dotfile", method: "GET", path: "/.hidden.txt", wantStatus: 404},
		{name: "POST rejected", method: "POST", path: "/fd_a.txt", wantStatus: 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tt.wantStatus {
				t.Fatalf("%s %s = %d, want %d (body %q)", tt.method, tt.path, rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantStatus == 200 {
				if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
					t.Errorf("Content-Type = %q", ct)
				}
			}
			if rr.Code == 200 && tt.method == "GET" && rr.Body.String() != "1.2.3.0/24\n2001:db8::/32\n" {
				t.Errorf("body = %q", rr.Body.String())
			}
		})
	}
}

func TestServeListenerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	f := newFileTarget(t, dir, "127.0.0.1:0")
	if _, err := f.Apply(context.Background(), []AliasSet{
		{Name: "fd_a", Prefixes: mustPrefixes(t, "1.2.3.0/24")},
	}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.serveListener(ctx, ln) }()

	url := fmt.Sprintf("http://%s/fd_a.txt", ln.Addr())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "1.2.3.0/24\n" {
		t.Errorf("GET = %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serveListener returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveListener did not shut down after cancel")
	}
}

func TestServeWithoutAddressErrors(t *testing.T) {
	f := newFileTarget(t, t.TempDir(), "")
	if err := f.Serve(context.Background()); err == nil {
		t.Error("Serve with empty address returned nil error")
	}
}

// The Target returned by NewFile must also satisfy the Server
// interface so the CLI can start the HTTP server without knowing the
// concrete type.
func TestNewFileImplementsServer(t *testing.T) {
	tg, err := NewFile(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tg.(Server); !ok {
		t.Fatalf("NewFile result (%T) does not implement Server", tg)
	}
}
