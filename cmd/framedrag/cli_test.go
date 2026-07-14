package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a hermetic environment: a tiny catalog pointing at
// feedURL, a config with one alias and one file target, and empty
// state and lists dirs.
type fixture struct {
	cfgPath  string
	catPath  string
	stateDir string
	listsDir string
}

func newFixture(t *testing.T, feedURL string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		cfgPath:  filepath.Join(dir, "framedrag.yaml"),
		catPath:  filepath.Join(dir, "feeds.json"),
		stateDir: filepath.Join(dir, "state"),
		listsDir: filepath.Join(dir, "lists"),
	}

	catalogJSON := fmt.Sprintf(`{
  "ipv4": {
    "PRI1": {
      "info": "test tier",
      "description": "test tier",
      "action": "block",
      "cron": "01hour",
      "feeds": [
        {"feed": "Test Feed", "website": "https://example.com/", "url": %q, "header": "Test_Feed"}
      ]
    }
  }
}`, feedURL)
	if err := os.WriteFile(f.catPath, []byte(catalogJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	configYAML := fmt.Sprintf(`state_dir: %s
suppress:
  - 192.168.0.0/16
aliases:
  - name: fd_test
    action: deny
    direction: in
    feeds: [PRI1]
targets:
  - type: file
    dir: %s
`, f.stateDir, f.listsDir)
	if err := os.WriteFile(f.cfgPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// runCLI drives the whole binary in-process and returns exit code,
// stdout, and stderr.
func runCLI(t *testing.T, f fixture, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	full := append(args, "--config", f.cfgPath, "--catalog", f.catPath)
	code := run(full, &out, &errb)
	return code, out.String(), errb.String()
}

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "1.2.3.4\n5.6.7.8\n203.0.113.0/24\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateDryRunHealthyExitsZeroAndPersistsNothing(t *testing.T) {
	srv := healthyServer(t)
	f := newFixture(t, srv.URL+"/list.txt")

	code, out, _ := runCLI(t, f, "update", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout:\n%s", code, out)
	}
	for _, s := range []string{"Test_Feed", "OK", "target file (dry run):", "create"} {
		if !strings.Contains(out, s) {
			t.Errorf("stdout missing %q:\n%s", s, out)
		}
	}
	// Nothing persisted: no list files, no state files.
	if _, err := os.Stat(filepath.Join(f.listsDir, "fd_test.txt")); !os.IsNotExist(err) {
		t.Error("dry run must not write list files")
	}
	entries, _ := os.ReadDir(filepath.Join(f.stateDir, "feeds"))
	if len(entries) != 0 {
		t.Errorf("dry run must not persist feed state, found %d files", len(entries))
	}
}

func TestUpdateAppliesThenHealthReportsBaseline(t *testing.T) {
	srv := healthyServer(t)
	f := newFixture(t, srv.URL+"/list.txt")

	code, out, _ := runCLI(t, f, "update")
	if code != 0 {
		t.Fatalf("update exit code = %d, want 0; stdout:\n%s", code, out)
	}
	list, err := os.ReadFile(filepath.Join(f.listsDir, "fd_test.txt"))
	if err != nil {
		t.Fatalf("list file not written: %v", err)
	}
	for _, p := range []string{"1.2.3.4/32", "5.6.7.8/32", "203.0.113.0/24"} {
		if !strings.Contains(string(list), p+"\n") {
			t.Errorf("list missing %s:\n%s", p, list)
		}
	}

	// health: always a dry run, reports against the persisted baseline.
	code, out, _ = runCLI(t, f, "health")
	if code != 0 {
		t.Fatalf("health exit code = %d, want 0; stdout:\n%s", code, out)
	}
	for _, s := range []string{"FEED", "TIER", "STATUS", "ENTRIES", "DELTA", "LAST GOOD", "+0.0%"} {
		if !strings.Contains(out, s) {
			t.Errorf("health output missing %q:\n%s", s, out)
		}
	}
}

func TestUnhealthyFeedExitsTwo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	f := newFixture(t, srv.URL+"/list.txt")

	code, out, stderr := runCLI(t, f, "update", "--dry-run")
	if code != 2 {
		t.Fatalf("update exit code = %d, want 2; stdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("stdout should show FAILED:\n%s", out)
	}
	if !strings.Contains(stderr, "fetch failed") {
		t.Errorf("stderr should carry the reason:\n%s", stderr)
	}

	code, out, _ = runCLI(t, f, "health")
	if code != 2 {
		t.Fatalf("health exit code = %d, want 2; stdout:\n%s", code, out)
	}
	if !strings.Contains(out, "fetch failed") {
		t.Errorf("health should show reasons indented under unhealthy feeds:\n%s", out)
	}
}

func TestUpdateJSONOutput(t *testing.T) {
	srv := healthyServer(t)
	f := newFixture(t, srv.URL+"/list.txt")

	code, out, _ := runCLI(t, f, "update", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d; stdout:\n%s", code, out)
	}
	var doc struct {
		Feeds []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Entries int    `json:"entries"`
		} `json:"feeds"`
		Reports []struct {
			Target string `json:"target"`
			DryRun bool   `json:"dry_run"`
		} `json:"reports"`
		Healthy bool `json:"healthy"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if !doc.Healthy || len(doc.Feeds) != 1 || doc.Feeds[0].Name != "Test_Feed" ||
		doc.Feeds[0].Status != "OK" || doc.Feeds[0].Entries != 3 {
		t.Errorf("unexpected feeds: %+v healthy=%v", doc.Feeds, doc.Healthy)
	}
	if len(doc.Reports) != 1 || !doc.Reports[0].DryRun || doc.Reports[0].Target != "file" {
		t.Errorf("unexpected reports: %+v", doc.Reports)
	}
}

func TestZeroMatchFeedRefWarns(t *testing.T) {
	srv := healthyServer(t)
	f := newFixture(t, srv.URL+"/list.txt")
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(cfg), "feeds: [PRI1]", "feeds: [PRI1, NOPE]", 1)
	if err := os.WriteFile(f.cfgPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runCLI(t, f, "update", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(stderr, `"NOPE"`) || !strings.Contains(stderr, "matches no enabled feeds") {
		t.Errorf("expected zero-match warning on stderr:\n%s", stderr)
	}
}

func TestCatalogList(t *testing.T) {
	f := newFixture(t, "https://example.com/list.txt")

	code, out, _ := runCLI(t, f, "catalog", "list")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, s := range []string{"PRI1 (1 feeds)", "Test_Feed", "1h"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q:\n%s", s, out)
		}
	}

	code, out, _ = runCLI(t, f, "catalog", "list", "--json")
	if code != 0 {
		t.Fatalf("json exit code = %d", code)
	}
	var feeds []struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal([]byte(out), &feeds); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(feeds) != 1 || feeds[0].Name != "Test_Feed" || feeds[0].Tier != "PRI1" {
		t.Errorf("unexpected feeds: %+v", feeds)
	}
}

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "framedrag dev") {
		t.Errorf("got %q", out.String())
	}

	out.Reset()
	if code := run([]string{"version", "--json"}, &out, &errb); code != 0 {
		t.Fatalf("json exit code = %d", code)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Version != "dev" {
		t.Errorf("bad json version output: %v %q", err, out.String())
	}
}

func TestMissingConfigIsHardError(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"update", "--config", "/nonexistent/framedrag.yaml"}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if errb.Len() == 0 {
		t.Error("expected an error message on stderr")
	}
}
