package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
)

// dashboardLinkLife matches GRID0+'s LoginLinkMaxAge; the dashboard refuses
// a link that claims to live longer.
const dashboardLinkLife = 15 * time.Minute

// dashboardLoginLink must sign exactly as GRID0+'s
// internal/dashboard.LoginLinkSignature does.
func dashboardLoginLink(base, token string, expires time.Time) string {
	exp := expires.Unix()
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("grid0plus-dashboard-login:" + strconv.FormatInt(exp, 10)))
	return fmt.Sprintf("%s/login/link?exp=%d&sig=%s", base, exp, hex.EncodeToString(m.Sum(nil)))
}

func (b *Bot) dashboard(_ context.Context, r reply, _ *discordgo.User) {
	d := b.cfg.Dashboard
	if d.URL == "" {
		r.text("The dashboard link is not set up on this bot.")
		return
	}
	if !slices.Contains(r.i.Member.Roles, d.AdminRoleID) {
		r.text("Only admins can open the dashboard.")
		return
	}
	link := dashboardLoginLink(d.URL, d.Token, time.Now().Add(dashboardLinkLife))
	r.text("Your dashboard login link, valid for 15 minutes. Only you can see this message; don't share it.\n" + link)
}
