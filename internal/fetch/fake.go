package fetch

import (
	"context"
	"fmt"
	"sync"
)

// Fake is a scriptable in-memory Fetcher for tests in other packages.
// Queue responses or errors per URL; each Fetch pops the next entry
// for that URL in FIFO order. Fetching a URL with nothing queued is an
// error. Fake is safe for concurrent use.
type Fake struct {
	mu    sync.Mutex
	queue map[string][]fakeStep
	calls []FakeCall
}

type fakeStep struct {
	res Result
	err error
}

// FakeCall records one Fetch invocation.
type FakeCall struct {
	URL   string
	Hints Hints
}

// NewFake returns an empty Fake with nothing queued.
func NewFake() *Fake {
	return &Fake{queue: make(map[string][]fakeStep)}
}

// Queue appends a successful Result for url.
func (f *Fake) Queue(url string, res Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue[url] = append(f.queue[url], fakeStep{res: res})
}

// QueueError appends an error for url (e.g. StatusError{Code: 503}).
func (f *Fake) QueueError(url string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue[url] = append(f.queue[url], fakeStep{err: err})
}

// Calls returns a copy of every Fetch invocation so far, in order.
func (f *Fake) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// Fetch implements Fetcher. It honors ctx cancellation, records the
// call, and pops the next queued step for url.
func (f *Fake) Fetch(ctx context.Context, url string, hints Hints) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{URL: url, Hints: hints})
	steps := f.queue[url]
	if len(steps) == 0 {
		return Result{}, fmt.Errorf("fetch.Fake: no response queued for %q", url)
	}
	step := steps[0]
	f.queue[url] = steps[1:]
	return step.res, step.err
}

var _ Fetcher = (*Fake)(nil)
