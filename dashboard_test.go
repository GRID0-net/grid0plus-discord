package main

import (
	"testing"
	"time"
)

// The signature GRID0+'s internal/dashboard.LoginLinkSignature gives for
// token "s3cret" and expiry 1800000000. If the two sides drift, every
// link the bot hands out is refused.
func TestDashboardLinkMatchesTheServersSignature(t *testing.T) {
	got := dashboardLoginLink("https://d.example", "s3cret", time.Unix(1800000000, 0))
	want := "https://d.example/login/link?exp=1800000000&sig=92a39fbbdaf8f5069ceb964a9b834ad8546db7e3bab0d21dc0b4e92a09b0354e"
	if got != want {
		t.Fatalf("link %s, want %s", got, want)
	}
}
