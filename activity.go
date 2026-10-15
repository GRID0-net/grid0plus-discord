package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

var networkActivityInterval = 15 * time.Second

// Only one loop polls; reconnect events coalesce. Unchanged counts never send
// a Gateway presence update, and failures replace stale counts with unknown.
func runNetworkActivity(ctx context.Context, api *SwitchNet, log *slog.Logger, ready <-chan struct{}, publish func(string) error) {
	ticker := time.NewTicker(networkActivityInterval)
	defer ticker.Stop()
	last := ""
	refresh := func(force bool) {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		counts, err := api.PresenceCount(requestCtx)
		cancel()
		text := "GRID0+ status unavailable"
		if err == nil {
			text = fmt.Sprintf("%d on GRID0+ · %d playing", counts.Total, counts.Playing)
		}
		if force || text != last {
			if sendErr := publish(text); sendErr != nil {
				log.Warn("update network activity", "error", sendErr)
				return
			}
			last = text
			log.Info("network activity updated", "activity", text)
		}
		if err != nil {
			log.Warn("read network activity", "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ready:
			refresh(true)
		case <-ticker.C:
			refresh(false)
		}
	}
}

type PresenceCount struct {
	Online  int `json:"online"`
	Playing int `json:"playing"`
	Total   int `json:"total"`
}

func (c *SwitchNet) PresenceCount(ctx context.Context) (PresenceCount, error) {
	var out PresenceCount
	err := c.doJSON(ctx, "GET", "/bot/v1/presence-count", "", nil, &out)
	return out, err
}
