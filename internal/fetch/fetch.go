// Package fetch retrieves feed bodies over HTTP with conditional
// requests (ETag / Last-Modified), bounded retries, and timeouts.
// Unit tests never touch the network; they use a fake Fetcher.
package fetch

import (
	"context"
	"time"
)

// Hints carries the validators from the previous successful fetch so
// the server can answer 304 Not Modified.
type Hints struct {
	ETag         string
	LastModified string
}

// Result is the outcome of one fetch. A non-2xx status is reported as
// an error by Fetcher implementations, not as a Result.
type Result struct {
	Body         []byte
	StatusCode   int
	NotModified  bool // 304; Body is empty, caller reuses cached copy
	ETag         string
	LastModified string
	FetchedAt    time.Time
}

// Fetcher retrieves one URL. Implementations must honor ctx deadlines
// and must not follow redirects off-scheme (https -> http).
type Fetcher interface {
	Fetch(ctx context.Context, url string, hints Hints) (Result, error)
}
