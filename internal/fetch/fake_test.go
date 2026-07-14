package fetch

import (
	"context"
	"errors"
	"testing"
)

func TestFakeQueueOrder(t *testing.T) {
	f := NewFake()
	f.Queue("https://a.example/feed", Result{Body: []byte("one"), StatusCode: 200})
	f.QueueError("https://a.example/feed", StatusError{Code: 503})
	f.Queue("https://b.example/feed", Result{NotModified: true, StatusCode: 304})

	res, err := f.Fetch(context.Background(), "https://a.example/feed", Hints{ETag: `"x"`})
	if err != nil || string(res.Body) != "one" {
		t.Fatalf("first fetch = (%q, %v)", res.Body, err)
	}

	_, err = f.Fetch(context.Background(), "https://a.example/feed", Hints{})
	var se StatusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("second fetch err = %v, want StatusError{503}", err)
	}

	res, err = f.Fetch(context.Background(), "https://b.example/feed", Hints{})
	if err != nil || !res.NotModified {
		t.Fatalf("b fetch = (%+v, %v), want NotModified", res, err)
	}

	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("Calls() len = %d, want 3", len(calls))
	}
	if calls[0].URL != "https://a.example/feed" || calls[0].Hints.ETag != `"x"` {
		t.Errorf("calls[0] = %+v", calls[0])
	}
}

func TestFakeUnqueuedURL(t *testing.T) {
	f := NewFake()
	if _, err := f.Fetch(context.Background(), "https://nothing.example/", Hints{}); err == nil {
		t.Fatal("Fetch succeeded for URL with nothing queued")
	}
}

func TestFakeHonorsContext(t *testing.T) {
	f := NewFake()
	f.Queue("https://a.example/", Result{Body: []byte("x")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx, "https://a.example/", Hints{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
