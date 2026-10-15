package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkActivityDoesNotSpamUnchangedCountsAndClearsStaleCounts(t *testing.T) {
	old := networkActivityInterval
	networkActivityInterval = 5 * time.Millisecond
	defer func() { networkActivityInterval = old }()
	var failed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot/v1/presence-count" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected count request: %s", r.URL.Path)
		}
		if failed.Load() {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"online":2,"playing":1,"total":3}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{}, 1)
	activity := make(chan string, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runNetworkActivity(ctx, NewSwitchNet(server.URL, "test-token"), slog.New(slog.NewTextHandler(io.Discard, nil)), ready, func(s string) error { activity <- s; return nil })
	}()
	defer func() { cancel(); <-done }()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-activity:
			if got != want {
				t.Fatalf("activity = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("activity not updated")
		}
	}
	ready <- struct{}{}
	expect("3 on GRID0+ · 1 playing")
	select {
	case got := <-activity:
		t.Fatalf("unchanged count was republished: %s", got)
	case <-time.After(40 * time.Millisecond):
	}
	failed.Store(true)
	expect("GRID0+ status unavailable")
	failed.Store(false)
	expect("3 on GRID0+ · 1 playing")
	ready <- struct{}{}
	expect("3 on GRID0+ · 1 playing")
}
