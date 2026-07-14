package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastOpts keeps retry-path tests quick.
func fastOpts(extra ...Option) []Option {
	opts := []Option{WithBackoff(time.Millisecond)}
	return append(opts, extra...)
}

func TestFetchSuccess(t *testing.T) {
	var gotUA, gotINM, gotIMS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotINM = r.Header.Get("If-None-Match")
		gotIMS = r.Header.Get("If-Modified-Since")
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Mon, 13 Jul 2026 10:00:00 GMT")
		_, _ = w.Write([]byte("1.2.3.0/24\n"))
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts()...)
	before := time.Now()
	res, err := f.Fetch(context.Background(), srv.URL, Hints{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "1.2.3.0/24\n" {
		t.Errorf("Body = %q", res.Body)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d", res.StatusCode)
	}
	if res.NotModified {
		t.Error("NotModified = true, want false")
	}
	if res.ETag != `"v1"` {
		t.Errorf("ETag = %q", res.ETag)
	}
	if res.LastModified != "Mon, 13 Jul 2026 10:00:00 GMT" {
		t.Errorf("LastModified = %q", res.LastModified)
	}
	if res.FetchedAt.Before(before) || res.FetchedAt.After(time.Now()) {
		t.Errorf("FetchedAt = %v out of range", res.FetchedAt)
	}
	if gotUA != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, DefaultUserAgent)
	}
	if gotINM != "" || gotIMS != "" {
		t.Errorf("conditional headers sent with empty hints: If-None-Match=%q If-Modified-Since=%q", gotINM, gotIMS)
	}
}

func TestFetchNotModified(t *testing.T) {
	const etag = `"v7"`
	const lastMod = "Sun, 12 Jul 2026 09:00:00 GMT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag && r.Header.Get("If-Modified-Since") == lastMod {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte("fresh"))
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts()...)
	res, err := f.Fetch(context.Background(), srv.URL, Hints{ETag: etag, LastModified: lastMod})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.NotModified {
		t.Fatal("NotModified = false, want true")
	}
	if res.StatusCode != http.StatusNotModified {
		t.Errorf("StatusCode = %d, want 304", res.StatusCode)
	}
	if len(res.Body) != 0 {
		t.Errorf("Body = %q, want empty", res.Body)
	}
	// Validators from the hints survive a 304 without headers.
	if res.ETag != etag || res.LastModified != lastMod {
		t.Errorf("validators = (%q, %q), want (%q, %q)", res.ETag, res.LastModified, etag, lastMod)
	}
}

func TestFetchRetryThenSuccess(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			http.Error(w, "flaky", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts(WithRetries(3))...)
	res, err := f.Fetch(context.Background(), srv.URL, Hints{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "ok" {
		t.Errorf("Body = %q", res.Body)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

func TestFetch4xxNoRetry(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts(WithRetries(3))...)
	_, err := f.Fetch(context.Background(), srv.URL, Hints{})
	var se StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Fatalf("err = %v, want StatusError{404}", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestFetch5xxRetriesExhausted(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts(WithRetries(2))...)
	_, err := f.Fetch(context.Background(), srv.URL, Hints{})
	var se StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want StatusError{503}", err)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("requests = %d, want 3 (1 + 2 retries)", got)
	}
}

func TestFetchTimeout(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts(WithTimeout(50*time.Millisecond), WithRetries(1))...)
	start := time.Now()
	_, err := f.Fetch(context.Background(), srv.URL, Hints{})
	if err == nil {
		t.Fatal("Fetch succeeded, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("requests = %d, want 2 (timeouts are retried)", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v, per-request timeout not honored", elapsed)
	}
}

func TestFetchParentContextStopsRetries(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := NewHTTP(fastOpts(WithRetries(5))...)
	_, err := f.Fetch(ctx, srv.URL, Hints{})
	if err == nil {
		t.Fatal("Fetch succeeded with cancelled context")
	}
	if got := n.Load(); got > 1 {
		t.Errorf("requests = %d, want <= 1 after parent cancellation", got)
	}
}

func TestFetchSizeCap(t *testing.T) {
	var n atomic.Int32
	body := make([]byte, 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	f := NewHTTP(fastOpts(WithMaxBodySize(10), WithRetries(2))...)
	_, err := f.Fetch(context.Background(), srv.URL, Hints{})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (size cap is permanent)", got)
	}
}

func TestFetchRefusesHTTPSToHTTPRedirect(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write([]byte("insecure"))
	}))
	defer plain.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tlsSrv.Close()

	f := NewHTTP(fastOpts(WithTransport(tlsSrv.Client().Transport), WithRetries(2))...)
	_, err := f.Fetch(context.Background(), tlsSrv.URL, Hints{})
	if !errors.Is(err, ErrInsecureRedirect) {
		t.Fatalf("err = %v, want ErrInsecureRedirect", err)
	}
	if got := plainHits.Load(); got != 0 {
		t.Errorf("plain-http server was hit %d times, want 0", got)
	}
}

func TestFetchFollowsHTTPSToHTTPSRedirect(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			_, _ = w.Write([]byte("moved"))
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer tlsSrv.Close()

	f := NewHTTP(fastOpts(WithTransport(tlsSrv.Client().Transport))...)
	res, err := f.Fetch(context.Background(), tlsSrv.URL, Hints{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(res.Body) != "moved" {
		t.Errorf("Body = %q", res.Body)
	}
}

func TestFetchUnsupportedScheme(t *testing.T) {
	f := NewHTTP(fastOpts()...)
	if _, err := f.Fetch(context.Background(), "ftp://example.invalid/feed", Hints{}); err == nil {
		t.Fatal("Fetch succeeded for ftp URL")
	}
}
