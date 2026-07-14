package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"
)

// Defaults for NewHTTP. Overridable via options.
const (
	// DefaultTimeout is the per-request timeout.
	DefaultTimeout = 60 * time.Second
	// DefaultRetries is the number of additional attempts after the
	// first request fails transiently.
	DefaultRetries = 2
	// DefaultBackoff is the wait before the first retry; it doubles on
	// each subsequent retry.
	DefaultBackoff = 1 * time.Second
	// DefaultMaxBodySize caps how many response bytes are read (64 MiB).
	DefaultMaxBodySize = 64 << 20
	// DefaultUserAgent identifies framedrag to feed providers.
	DefaultUserAgent = "framedrag/dev (+https://framedrag.dev)"
)

// StatusError reports a non-2xx, non-304 HTTP response, after retries
// for 5xx were exhausted. The health layer uses it to distinguish HTTP
// failure from transport failure.
type StatusError struct {
	Code int
}

func (e StatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %d", e.Code)
}

// Sentinel errors returned by the HTTP Fetcher. Both are permanent:
// they are never retried.
var (
	// ErrTooLarge means the response body exceeded the configured cap.
	ErrTooLarge = errors.New("response body exceeds size limit")
	// ErrInsecureRedirect means the server tried to redirect from
	// https to plain http.
	ErrInsecureRedirect = errors.New("refusing redirect from https to http")
)

// errPermanent marks an error that must not be retried.
type errPermanent struct{ err error }

func (e errPermanent) Error() string { return e.err.Error() }
func (e errPermanent) Unwrap() error { return e.err }

// Option configures the Fetcher returned by NewHTTP.
type Option func(*httpFetcher)

// WithTimeout sets the per-request timeout (each attempt gets the full
// timeout; the caller's ctx still bounds the whole fetch).
func WithTimeout(d time.Duration) Option {
	return func(f *httpFetcher) { f.timeout = d }
}

// WithRetries sets how many additional attempts are made after a
// transient failure (5xx, timeout, connection error). 0 disables
// retries.
func WithRetries(n int) Option {
	return func(f *httpFetcher) { f.retries = n }
}

// WithBackoff sets the wait before the first retry; it doubles per
// retry.
func WithBackoff(d time.Duration) Option {
	return func(f *httpFetcher) { f.backoff = d }
}

// WithMaxBodySize caps how many response bytes are read.
func WithMaxBodySize(n int64) Option {
	return func(f *httpFetcher) { f.maxBody = n }
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(f *httpFetcher) { f.userAgent = ua }
}

// WithTransport overrides the underlying RoundTripper (tests use this
// to trust httptest TLS certificates).
func WithTransport(rt http.RoundTripper) Option {
	return func(f *httpFetcher) { f.transport = rt }
}

type httpFetcher struct {
	client    *http.Client
	transport http.RoundTripper
	timeout   time.Duration
	retries   int
	backoff   time.Duration
	maxBody   int64
	userAgent string
}

// NewHTTP returns a Fetcher backed by net/http with per-request
// timeouts, bounded retries with backoff for transient failures, and
// conditional-request support via Hints. It never follows a redirect
// from https to http.
func NewHTTP(opts ...Option) Fetcher {
	f := &httpFetcher{
		timeout:   DefaultTimeout,
		retries:   DefaultRetries,
		backoff:   DefaultBackoff,
		maxBody:   DefaultMaxBodySize,
		userAgent: DefaultUserAgent,
	}
	for _, o := range opts {
		o(f)
	}
	f.client = &http.Client{
		Transport: f.transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Scheme == "http" {
				for _, prev := range via {
					if prev.URL.Scheme == "https" {
						return ErrInsecureRedirect
					}
				}
			}
			return nil
		},
	}
	return f
}

func (f *httpFetcher) Fetch(ctx context.Context, url string, hints Hints) (Result, error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return Result{}, fmt.Errorf("fetch %s: %w", url, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{}, fmt.Errorf("fetch %s: unsupported scheme %q", url, u.Scheme)
	}

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			wait := f.backoff << (attempt - 1)
			select {
			case <-ctx.Done():
				return Result{}, fmt.Errorf("fetch %s: %w", url, ctx.Err())
			case <-time.After(wait):
			}
		}
		res, err := f.fetchOnce(ctx, url, hints)
		if err == nil {
			return res, nil
		}
		// Stop when the parent context is done, the error is
		// permanent, or retries are exhausted.
		if ctx.Err() != nil || !retryable(err) || attempt >= f.retries {
			return Result{}, fmt.Errorf("fetch %s: %w", url, err)
		}
	}
}

// retryable reports whether err is a transient failure worth retrying:
// 5xx statuses, timeouts, and connection errors. 4xx and the sentinel
// errors are permanent.
func retryable(err error) bool {
	var pe errPermanent
	if errors.As(err, &pe) {
		return false
	}
	var se StatusError
	if errors.As(err, &se) {
		return se.Code >= 500
	}
	if errors.Is(err, ErrInsecureRedirect) || errors.Is(err, ErrTooLarge) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Everything else out of the transport: timeouts, refused
	// connections, resets, truncated bodies.
	return true
}

func (f *httpFetcher) fetchOnce(ctx context.Context, url string, hints Hints) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, errPermanent{err}
	}
	req.Header.Set("User-Agent", f.userAgent)
	if hints.ETag != "" {
		req.Header.Set("If-None-Match", hints.ETag)
	}
	if hints.LastModified != "" {
		req.Header.Set("If-Modified-Since", hints.LastModified)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	now := time.Now()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Result{
			StatusCode:   resp.StatusCode,
			NotModified:  true,
			ETag:         headerOr(resp, "ETag", hints.ETag),
			LastModified: headerOr(resp, "Last-Modified", hints.LastModified),
			FetchedAt:    now,
		}, nil

	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if resp.ContentLength > f.maxBody {
			return Result{}, fmt.Errorf("%w (content-length %d > %d)", ErrTooLarge, resp.ContentLength, f.maxBody)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBody+1))
		if err != nil {
			return Result{}, err
		}
		if int64(len(body)) > f.maxBody {
			return Result{}, fmt.Errorf("%w (> %d bytes)", ErrTooLarge, f.maxBody)
		}
		return Result{
			Body:         body,
			StatusCode:   resp.StatusCode,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			FetchedAt:    now,
		}, nil

	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Result{}, StatusError{Code: resp.StatusCode}
	}
}

func headerOr(resp *http.Response, key, fallback string) string {
	if v := resp.Header.Get(key); v != "" {
		return v
	}
	return fallback
}
