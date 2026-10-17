package main

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ---- voting by reaction -----------------------------------------------------------

// onReactionAdd is a vote: a reaction on a vote post joins that team. The
// first pick is final (GRID0+ keeps it), so a reaction for another team is
// taken back and the person told. Removing a reaction changes nothing.
func (b *Bot) onReactionAdd(s *discordgo.Session, r *discordgo.MessageReactionAdd) {
	if !slices.Contains(b.cfg.Discord.GuildIDs, r.GuildID) || (s.State.User != nil && r.UserID == s.State.User.ID) {
		return
	}
	if r.Member != nil && r.Member.User != nil && r.Member.User.Bot {
		return
	}
	b.fest.mu.Lock()
	festID, ok := b.fest.byMessage[r.MessageID]
	b.fest.mu.Unlock()
	if !ok {
		return
	}
	f, _ := b.cachedFest(festID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b.vote(ctx, s, r.GuildID, f, r.UserID, r.Emoji)
}

// vote handles one reaction on a vote post, live or found on catching up.
func (b *Bot) vote(ctx context.Context, s *discordgo.Session, guildID string, f Fest, userID string, emoji discordgo.Emoji) {
	takeBack := func() {
		if err := s.MessageReactionRemove(f.ChannelID, f.MessageID, emoji.APIName(), userID); err != nil {
			b.log.Warn("remove reaction (does the bot have Manage Messages?)", "fest", f.FestID, "error", err)
		}
	}
	if f.over(time.Now()) {
		takeBack()
		return
	}
	var picked FestTeam
	found := false
	for _, t := range f.Teams {
		if emojiKey(t.Emoji) == reactionKey(emoji) {
			picked, found = t, true
		}
	}
	if !found {
		takeBack()
		return
	}
	team, created, err := b.api.EnterFest(ctx, userID, f.FestID, picked.Team)
	switch {
	case IsCode(err, "not_registered"):
		takeBack()
		b.dm(s, userID, "To join a team in **"+f.Title+"** you need a GRID0+ account: run `/register` in the server, then react again.")
		return
	case IsCode(err, "fest_over"):
		takeBack()
		return
	case err != nil:
		b.log.Error("fest vote", "fest", f.FestID, "user", userID, "error", err)
		takeBack()
		b.dm(s, userID, "Something went wrong saving your team in **"+f.Title+"**. Try reacting again in a minute.")
		return
	}
	b.giveTeamRole(s, guildID, f, userID, team)
	if team != picked.Team {
		takeBack()
		b.dm(s, userID, "You're already on "+teamLabel(f, team)+" in **"+f.Title+"**: the first pick is final, just like in game.")
		return
	}
	if created {
		b.log.Info("fest vote", "fest", f.FestID, "user", userID, "team", team)
	}
}

// giveTeamRole gives a person their team's role and takes away the other
// teams'. Done once per person per run, unless their team changes.
func (b *Bot) giveTeamRole(s *discordgo.Session, guildID string, f Fest, userID, team string) {
	key := f.FestID + "/" + userID
	b.fest.mu.Lock()
	done := b.fest.roles[key] == team
	b.fest.mu.Unlock()
	if done || time.Now().After(f.Timetable.Close) {
		return
	}
	t, ok := f.team(team)
	if !ok {
		return
	}
	if guildID == "" {
		guildID = b.festGuild(s, f)
	}
	if guildID == "" {
		return
	}
	member, err := s.GuildMember(guildID, userID)
	if err != nil {
		// Not in the server (any more), or Discord is unhappy: nothing to do.
		b.log.Info("team role: member not found", "fest", f.FestID, "user", userID, "error", err)
		return
	}
	if !slices.Contains(member.Roles, t.RoleID) {
		if err := s.GuildMemberRoleAdd(guildID, userID, t.RoleID); err != nil {
			b.log.Error("give team role (does the bot have Manage Roles, above the team roles?)",
				"fest", f.FestID, "role", t.RoleID, "error", err)
			return
		}
	}
	for _, other := range f.Teams {
		if other.Team != team && slices.Contains(member.Roles, other.RoleID) {
			_ = s.GuildMemberRoleRemove(guildID, userID, other.RoleID)
		}
	}
	b.fest.mu.Lock()
	b.fest.roles[key] = team
	b.fest.mu.Unlock()
}

// festGuild finds the server a fest's vote post is in.
func (b *Bot) festGuild(s *discordgo.Session, f Fest) string {
	if f.ChannelID == "" {
		if len(b.cfg.Discord.GuildIDs) == 1 {
			return b.cfg.Discord.GuildIDs[0]
		}
		return ""
	}
	if ch, err := s.State.Channel(f.ChannelID); err == nil {
		return ch.GuildID
	}
	if ch, err := s.Channel(f.ChannelID); err == nil {
		return ch.GuildID
	}
	return ""
}

// ---- votes made in game -------------------------------------------------------------

// runFestEvents keeps GRID0+'s fest event stream open, so a player who picks
// a team in Splatoon 3 gets its role at once. Each time it connects it
// catches up once on what it missed; nothing here reads on a timer.
func (b *Bot) runFestEvents(ctx context.Context, s *discordgo.Session, ready <-chan struct{}) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := b.api.FestEvents(ctx,
			func() {
				b.log.Info("fest event stream connected")
				go b.festCatchUp(ctx, s)
			},
			func(e FestEvent) {
				f, ok := b.cachedFest(e.FestID)
				if !ok {
					rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					_ = b.refreshFests(rctx)
					cancel()
					if f, ok = b.cachedFest(e.FestID); !ok {
						return
					}
				}
				b.log.Info("fest vote in game", "fest", e.FestID, "user", e.DiscordUserID, "team", e.Team)
				b.giveTeamRole(s, "", f, e.DiscordUserID, e.Team)
			})
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 5*time.Minute {
			backoff = 5 * time.Second
		}
		wait := backoff
		if IsCode(err, "no_event_bus") {
			// GRID0+ has no Redis: in-game votes cannot reach the bot at all,
			// and asking again soon will not change that.
			wait = 10 * time.Minute
		}
		b.log.Warn("fest event stream lost; reconnecting", "error", err, "in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// festCatchUp settles what happened while the bot was not listening: votes
// made in game (roles) and reactions made on vote posts (votes). Once per
// connection.
func (b *Bot) festCatchUp(ctx context.Context, s *discordgo.Session) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := b.refreshFests(ctx); err != nil {
		b.log.Warn("fest catch-up: list fests", "error", err)
		return
	}
	now := time.Now()
	for _, f := range b.cachedFests() {
		if now.After(f.Timetable.Close) {
			continue
		}
		entries, err := b.api.FestEntries(ctx, f.FestID)
		if err != nil {
			b.log.Warn("fest catch-up: entries", "fest", f.FestID, "error", err)
			continue
		}
		teamOf := map[string]string{}
		guild := b.festGuild(s, f)
		for _, e := range entries {
			teamOf[e.DiscordUserID] = e.Team
			b.giveTeamRole(s, guild, f, e.DiscordUserID, e.Team)
		}
		if f.MessageID == "" || f.over(now) {
			continue
		}
		for _, t := range f.Teams {
			users, err := b.reactors(s, f, t.Emoji)
			if err != nil {
				b.log.Warn("fest catch-up: reactions", "fest", f.FestID, "error", err)
				continue
			}
			emoji := discordgo.Emoji{Name: reactionName(t.Emoji)}
			if m := customEmojiPattern.FindStringSubmatch(t.Emoji); m != nil {
				emoji = discordgo.Emoji{Name: m[1], ID: m[2]}
			}
			for _, u := range users {
				if u.Bot || teamOf[u.ID] == t.Team {
					continue
				}
				b.vote(ctx, s, guild, f, u.ID, emoji)
			}
		}
	}
}

// reactors lists everyone who reacted with one emoji on a vote post.
func (b *Bot) reactors(s *discordgo.Session, f Fest, emoji string) ([]*discordgo.User, error) {
	var out []*discordgo.User
	after := ""
	for {
		page, err := s.MessageReactions(f.ChannelID, f.MessageID, reactionName(emoji), 100, "", after)
		if err != nil {
			return out, err
		}
		out = append(out, page...)
		if len(page) < 100 {
			return out, nil
		}
		after = page[len(page)-1].ID
	}
}

// ---- announcements ----------------------------------------------------------------------

// dueFestPosts is which announcements a fest is owed at now. Each is only
// worth making in its own window: coming back after the halftime report's
// window has passed, the bot skips it rather than posting it late.
func dueFestPosts(f Fest, now time.Time) []string {
	t := f.Timetable
	var due []string
	add := func(kind string, from, until time.Time) {
		if _, done := f.Posted[kind]; done {
			return
		}
		if !now.Before(from) && (until.IsZero() || now.Before(until)) {
			due = append(due, kind)
		}
	}
	add("started", t.Start, t.Mid)
	add("halftime", t.Mid, t.End)
	add("ended", t.End, t.Close)
	add("results", t.Close, time.Time{})
	return due
}

// nextFestBoundary is the next phase change of any fest, for the announcer
// to sleep until.
func nextFestBoundary(fests []Fest, now time.Time) time.Time {
	var next time.Time
	for _, f := range fests {
		for _, at := range []time.Time{f.Timetable.Start, f.Timetable.Mid, f.Timetable.End, f.Timetable.Close} {
			if at.After(now) && (next.IsZero() || at.Before(next)) {
				next = at
			}
		}
	}
	return next
}

// runFestPosts makes each fest's announcements as its phases change. It
// sleeps until the next phase change, rereading the fest list (one request)
// at most every ten minutes in case a fest was changed elsewhere.
func (b *Bot) runFestPosts(ctx context.Context, s *discordgo.Session, ready <-chan struct{}) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	for ctx.Err() == nil {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := b.refreshFests(rctx); err != nil {
			b.log.Warn("fest announcer: list fests", "error", err)
		}
		cancel()
		// Drain the wake the refresh itself just sent.
		select {
		case <-b.fest.wake:
		default:
		}
		now := time.Now()
		fests := b.cachedFests()
		for _, f := range fests {
			for _, kind := range dueFestPosts(f, now) {
				b.festAnnounce(ctx, s, f, kind)
			}
		}
		wait := 10 * time.Minute
		if next := nextFestBoundary(fests, now); !next.IsZero() && time.Until(next) < wait {
			wait = time.Until(next) + 2*time.Second
		}
		select {
		case <-time.After(wait):
		case <-b.fest.wake:
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bot) festAnnounce(ctx context.Context, s *discordgo.Session, f Fest, kind string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if f.ChannelID == "" {
		return
	}
	channel, voteRef := b.festAnnounceTarget(s, f)
	claimed, err := b.api.ClaimFestPost(ctx, f.FestID, kind)
	if err != nil || !claimed {
		if err != nil {
			b.log.Warn("claim fest post", "fest", f.FestID, "kind", kind, "error", err)
		}
		return
	}
	msg := &discordgo.MessageSend{AllowedMentions: &discordgo.MessageAllowedMentions{}}
	unix := func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
	switch kind {
	case "started":
		text := "🎉 **" + f.Title + "** has started! Play until <t:" + unix(f.Timetable.End) + ":F>."
		text += "\nNo team yet? React on " + voteRef
		if !f.DiscordOnly {
			text += " or pick one in Splatoon 3"
		}
		text += ". Playing over LAN? Report each game with `/fest submit`."
		msg.Content = text
	case "halftime", "results":
		st, err := b.api.FestStandings(ctx, f.FestID)
		if err != nil {
			b.log.Error("fest standings for announcement", "fest", f.FestID, "error", err)
			return
		}
		if kind == "halftime" {
			msg.Embeds = []*discordgo.MessageEmbed{halftimeEmbed(f, st)}
		} else {
			msg.Embeds = []*discordgo.MessageEmbed{resultsEmbed(f, st)}
		}
	case "ended":
		msg.Content = "⏱️ **" + f.Title + "** is over: no more games count. Results <t:" + unix(f.Timetable.Close) + ":R>."
	}
	if f.MessageID != "" && channel == f.ChannelID {
		msg.Reference = &discordgo.MessageReference{MessageID: f.MessageID, ChannelID: f.ChannelID}
	}
	if _, err := s.ChannelMessageSendComplex(channel, msg); err != nil {
		b.log.Error("fest announcement", "fest", f.FestID, "kind", kind, "error", err)
		return
	}
	b.log.Info("fest announcement", "fest", f.FestID, "kind", kind)
	if kind == "results" {
		b.removeTeamRoles(ctx, s, f)
	}
}

// festAnnounceTarget is the channel a fest's announcements go to, and how a
// post there refers to the vote post: "the vote post above" when replying to
// it, a link when it lives in another channel. Discord cannot reply across
// channels.
func (b *Bot) festAnnounceTarget(s *discordgo.Session, f Fest) (channel, voteRef string) {
	channel = b.cfg.Fest.AnnounceChannelID
	if channel == "" || channel == f.ChannelID {
		return f.ChannelID, "the vote post above"
	}
	guild := b.festGuild(s, f)
	if f.MessageID == "" || guild == "" {
		return channel, "the vote post in <#" + f.ChannelID + ">"
	}
	return channel, "[the vote post](https://discord.com/channels/" + guild + "/" + f.ChannelID + "/" + f.MessageID + ")"
}

// removeTeamRoles takes the team roles back after the results. From GRID0+'s
// entries rather than a member scan, which would need the privileged members
// intent.
func (b *Bot) removeTeamRoles(ctx context.Context, s *discordgo.Session, f Fest) {
	entries, err := b.api.FestEntries(ctx, f.FestID)
	if err != nil {
		b.log.Error("list fest entries to remove roles", "fest", f.FestID, "error", err)
		return
	}
	guild := b.festGuild(s, f)
	removed := 0
	for _, e := range entries {
		if t, ok := f.team(e.Team); ok {
			if err := s.GuildMemberRoleRemove(guild, e.DiscordUserID, t.RoleID); err == nil {
				removed++
			}
		}
	}
	b.log.Info("fest team roles removed", "fest", f.FestID, "count", removed, "of", len(entries))
}
