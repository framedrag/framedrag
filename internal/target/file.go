package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Server is a Target that can additionally serve its output over
// read-only loopback HTTP for URL table aliases. The file target
// implements it; callers obtain it by type-asserting the Target
// returned from NewFile.
type Server interface {
	Target
	// Serve blocks, serving until ctx is cancelled. It returns nil
	// after a graceful shutdown.
	Serve(ctx context.Context) error
}

// Option configures optional target behavior.
type Option func(*options)

type options struct {
	allowNonLoopback bool
}

// AllowNonLoopback permits a serve address outside 127.0.0.0/8 and
// ::1. Never the default: the HTTP server exists to feed the local
// firewall, not to redistribute feeds (docs/SPEC.md section 9).
func AllowNonLoopback() Option {
	return func(o *options) { o.allowNonLoopback = true }
}

// fileTarget writes one <name>.txt per alias set into dir and owns
// every *.txt file in that directory.
type fileTarget struct {
	dir   string
	serve string
}

// NewFile returns the file target (docs/SPEC.md section 8, v1). dir is
// the output directory; serve is an optional "host:port" loopback bind
// address for the HTTP server ("" disables serving). A non-loopback
// serve address is an error unless AllowNonLoopback is passed.
func NewFile(dir string, serve string, opts ...Option) (Target, error) {
	if dir == "" {
		return nil, errors.New("file target: dir is required")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if serve != "" && !o.allowNonLoopback {
		if err := checkLoopback(serve); err != nil {
			return nil, fmt.Errorf("file target: %w", err)
		}
	}
	return &fileTarget{dir: dir, serve: serve}, nil
}

func checkLoopback(serve string) error {
	host, _, err := net.SplitHostPort(serve)
	if err != nil {
		return fmt.Errorf("serve %q: %v", serve, err)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("serve %q: host must be an IP literal", serve)
	}
	if !a.IsLoopback() {
		return fmt.Errorf("serve %q: bind address must be loopback (127.0.0.0/8 or ::1)", serve)
	}
	return nil
}

func (f *fileTarget) Name() string { return "file" }

// planned pairs a reported Change with the action Apply must take.
type planned struct {
	change  Change
	file    string // absolute path of the file to write or delete
	content []byte // nil unless kind is create or update
}

// plan computes what Apply would do. It only reads; DryRun and Apply
// share it so they always report identical changes.
func (f *fileTarget) plan(sets []AliasSet) ([]planned, error) {
	seen := make(map[string]bool, len(sets))
	plans := make([]planned, 0, len(sets))
	for _, s := range sets {
		if err := checkSetName(s.Name); err != nil {
			return nil, err
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("file target: duplicate alias set %q", s.Name)
		}
		seen[s.Name] = true

		content := render(s.Prefixes)
		file := filepath.Join(f.dir, s.Name+".txt")
		old, err := os.ReadFile(file)
		p := planned{file: file, content: content}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			p.change = Change{Object: file, Kind: "create",
				Detail: fmt.Sprintf("%d entries", len(s.Prefixes))}
		case err != nil:
			return nil, fmt.Errorf("file target: read %s: %w", file, err)
		case bytes.Equal(old, content):
			p.change = Change{Object: file, Kind: "unchanged",
				Detail: fmt.Sprintf("%d entries", len(s.Prefixes))}
			p.content = nil
		default:
			added, removed := diffLines(old, content)
			p.change = Change{Object: file, Kind: "update",
				Detail: fmt.Sprintf("%d entries (+%d -%d vs previous)", len(s.Prefixes), added, removed)}
		}
		plans = append(plans, p)
	}

	// Any *.txt in the directory not named by an incoming set is
	// stale output from a previous run. The target owns the whole
	// dir, but it still only ever deletes *.txt.
	entries, err := os.ReadDir(f.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("file target: read dir %s: %w", f.dir, err)
	}
	var stale []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".txt") {
			continue
		}
		if !seen[strings.TrimSuffix(name, ".txt")] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		file := filepath.Join(f.dir, name)
		n := 0
		if b, err := os.ReadFile(file); err == nil {
			n = countLines(b)
		}
		plans = append(plans, planned{
			file:   file,
			change: Change{Object: file, Kind: "delete", Detail: fmt.Sprintf("stale, %d entries removed", n)},
		})
	}
	return plans, nil
}

// Apply writes each alias set to <dir>/<name>.txt atomically and
// removes stale *.txt files.
func (f *fileTarget) Apply(ctx context.Context, sets []AliasSet) (Report, error) {
	rep := Report{Target: f.Name()}
	plans, err := f.plan(sets)
	if err != nil {
		return rep, err
	}
	if err := os.MkdirAll(f.dir, 0o755); err != nil { // #nosec G301 -- published lists, readable by the URL-table fetcher by design
		return rep, fmt.Errorf("file target: %w", err)
	}
	for _, p := range plans {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		switch p.change.Kind {
		case "create", "update":
			if err := atomicWrite(f.dir, p.file, p.content); err != nil {
				return rep, fmt.Errorf("file target: write %s: %w", p.file, err)
			}
		case "delete":
			if err := os.Remove(p.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return rep, fmt.Errorf("file target: remove %s: %w", p.file, err)
			}
		}
		rep.Changes = append(rep.Changes, p.change)
	}
	return rep, nil
}

// DryRun reports exactly what Apply would do without writing anything.
func (f *fileTarget) DryRun(ctx context.Context, sets []AliasSet) (Report, error) {
	rep := Report{Target: f.Name(), DryRun: true}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	plans, err := f.plan(sets)
	if err != nil {
		return rep, err
	}
	for _, p := range plans {
		rep.Changes = append(rep.Changes, p.change)
	}
	return rep, nil
}

func checkSetName(name string) error {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return fmt.Errorf("file target: invalid alias set name %q", name)
	}
	return nil
}

// render serializes prefixes one per line, input order preserved (the
// pipeline hands over already-sorted sets), with a trailing newline.
func render(prefixes []netip.Prefix) []byte {
	var b bytes.Buffer
	for _, p := range prefixes {
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func lineSet(b []byte) map[string]bool {
	set := make(map[string]bool)
	for _, ln := range strings.Split(string(b), "\n") {
		if ln != "" {
			set[ln] = true
		}
	}
	return set
}

func countLines(b []byte) int { return len(lineSet(b)) }

// diffLines returns the entry-level set diff between old and updated
// file contents: lines added, lines removed.
func diffLines(old, updated []byte) (added, removed int) {
	before, after := lineSet(old), lineSet(updated)
	for ln := range after {
		if !before[ln] {
			added++
		}
	}
	for ln := range before {
		if !after[ln] {
			removed++
		}
	}
	return added, removed
}

// atomicWrite writes content to file via a temp file in dir plus
// rename, so readers never observe a partial list.
func atomicWrite(dir, file string, content []byte) error {
	tmp, err := os.CreateTemp(dir, ".framedrag-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after successful rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Serve serves the output directory read-only over HTTP on the
// configured loopback address until ctx is cancelled. Only *.txt files
// are served; there is no directory listing.
func (f *fileTarget) Serve(ctx context.Context) error {
	if f.serve == "" {
		return errors.New("file target: no serve address configured")
	}
	ln, err := net.Listen("tcp", f.serve)
	if err != nil {
		return fmt.Errorf("file target: %w", err)
	}
	return f.serveListener(ctx, ln)
}

func (f *fileTarget) serveListener(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           f.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			return err
		}
		<-errc // always http.ErrServerClosed after Shutdown
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handler serves exactly /<name>.txt from the output directory:
// no directory listing, no subpaths, no traversal, text/plain only.
func (f *fileTarget) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || name != path.Clean(name) ||
			strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") ||
			!strings.HasSuffix(name, ".txt") {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(f.dir, name)) // #nosec G703 -- name is a validated single path element ending in .txt
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b) // #nosec G705 -- text/plain plus nosniff; bytes are IP lists this process wrote
	})
}
