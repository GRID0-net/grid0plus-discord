package main

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Downtime alerts: the bot reads GRID0+'s health (the same /status the
// command shows) on a timer and tells staff when a service goes down and when
// it comes back, with how long it was gone.

// apiService names GRID0+'s bot API itself, for when /status does not answer.
const apiService = "GRID0+ (not answering)"

type serviceHealth struct {
	failures  int       // reads in a row it was not up
	downSince time.Time // first failed read of the current run
	reported  bool      // an outage was posted for this run
}

// alertChange is one thing worth posting.
type alertChange struct {
	Service string
	Down    bool
	Detail  string
	For     time.Duration // how long it was down, on recovery
}

// healthTracker turns health reads into alerts. A service is reported down
// only after downAfter failed reads in a row -- a deploy restarts every
// service for a few seconds -- and reported back only if it was reported
// down.
type healthTracker struct {
	downAfter int
	services  map[string]*serviceHealth
}

func newHealthTracker(downAfter int) *healthTracker {
	return &healthTracker{downAfter: downAfter, services: map[string]*serviceHealth{}}
}

// observe takes one read: each service's status ("ok", "unknown", "degraded",
// "down") and detail. A service missing from a read keeps its state.
func (t *healthTracker) observe(now time.Time, statuses map[string]ServiceStatus) []alertChange {
	var out []alertChange
	for name, st := range statuses {
		h := t.services[name]
		if h == nil {
			h = &serviceHealth{}
			t.services[name] = h
		}
		// "unknown" is a service that cannot be probed from here, not one
		// that is down.
		if st.Status == "ok" || st.Status == "unknown" {
			if h.reported {
				out = append(out, alertChange{Service: name, For: now.Sub(h.downSince)})
			}
			*h = serviceHealth{}
			continue
		}
		if h.failures == 0 {
			h.downSince = now
		}
		h.failures++
		if h.failures >= t.downAfter && !h.reported {
			h.reported = true
			out = append(out, alertChange{Service: name, Down: true, Detail: st.Status + detailSuffix(st.Detail)})
		}
	}
	return out
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return ": " + d
}

func (b *Bot) runAlerts(ctx context.Context, s *discordgo.Session, ready <-chan struct{}) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	tracker := newHealthTracker(b.cfg.Alerts.DownAfter)
	tick := time.NewTicker(time.Duration(b.cfg.Alerts.IntervalSeconds) * time.Second)
	defer tick.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		st, err := b.api.Status(rctx)
		cancel()
		statuses := map[string]ServiceStatus{}
		if err != nil {
			statuses[apiService] = ServiceStatus{Name: apiService, Status: "down", Detail: err.Error()}
		} else {
			statuses[apiService] = ServiceStatus{Name: apiService, Status: "ok"}
			for _, svc := range st.Services {
				statuses[svc.Name] = svc
			}
		}
		for _, c := range tracker.observe(time.Now(), statuses) {
			b.postAlert(s, c)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bot) postAlert(s *discordgo.Session, c alertChange) {
	msg := &discordgo.MessageSend{AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if c.Down {
		msg.Content = fmt.Sprintf("🔴 **%s** is down (%s).", c.Service, c.Detail)
		if role := b.cfg.Alerts.PingRoleID; role != "" {
			msg.Content = "<@&" + role + "> " + msg.Content
			msg.AllowedMentions.Roles = []string{role}
		}
	} else {
		msg.Content = fmt.Sprintf("🟢 **%s** is back, after %s down.", c.Service, formatDowntime(c.For))
	}
	if _, err := s.ChannelMessageSendComplex(b.cfg.Alerts.ChannelID, msg); err != nil {
		b.log.Error("post downtime alert", "error", err)
		return
	}
	b.log.Info("downtime alert", "service", c.Service, "down", c.Down)
}

// formatDowntime says how long something was down, to the minute.
func formatDowntime(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}
