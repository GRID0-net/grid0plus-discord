package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// A role is resolved explicitly, never from an incomplete gateway member cache.
// Keep the resolved roster so later Discord role edits cannot silently change a booking.
func (b *Bot) tournamentResolveOrganizers(ctx context.Context, s *discordgo.Session, guild, actor, users, role string) ([]string, string, error) {
	users = strings.TrimSpace(users)
	if strings.HasPrefix(users, "<@&") && strings.HasSuffix(users, ">") && !strings.Contains(users, " ") {
		if role != "" {
			return nil, "", fmt.Errorf("choose organizer users or one organizer role, not both")
		}
		role = strings.TrimSuffix(strings.TrimPrefix(users, "<@&"), ">")
		users = ""
	}
	if (users == "") == (role == "") {
		return nil, "", fmt.Errorf("provide either organizers (user mentions/IDs) or organizer_role")
	}
	if role == "" {
		ids, e := tournamentOrganizerIDs(users)
		if e != nil {
			return nil, "", e
		}
		for _, id := range ids {
			m, e := s.GuildMember(guild, id, discordgo.WithContext(ctx))
			if e != nil || m.User == nil || m.User.Bot || !b.tournamentOrganizerAdmin(s, guild, m) {
				return nil, "", fmt.Errorf("every organizer must be a human member with tournament-admin access")
			}
		}
		return ids, "", nil
	}
	roles, e := s.GuildRoles(guild, discordgo.WithContext(ctx))
	if e != nil {
		return nil, "", fmt.Errorf("could not verify the organizer role")
	}
	trusted := false
	exists := false
	for _, r := range roles {
		if r.ID == role {
			exists = true
			trusted = r.Permissions&discordgo.PermissionAdministrator != 0
			for _, id := range b.cfg.Tournaments.AdminRoleIDs {
				if id == role {
					trusted = true
				}
			}
		}
	}
	if !exists || role == guild {
		return nil, "", fmt.Errorf("choose an existing organizer role, not @everyone")
	}
	if !trusted {
		return nil, "", fmt.Errorf("organizer role must have Administrator or be listed in tournaments.admin_role_ids; command access alone does not grant tournament control")
	}
	var ids []string
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 100; page++ {
		members, e := s.GuildMembers(guild, after, 1000, discordgo.WithContext(ctx))
		if e != nil {
			return nil, "", fmt.Errorf("could not list role members: enable Server Members Intent in the Discord Developer Portal, then retry (or use explicit user mentions)")
		}
		for _, m := range members {
			if m.User == nil || m.User.Bot {
				continue
			}
			for _, r := range m.Roles {
				if r == role && !seen[m.User.ID] {
					ids = append(ids, m.User.ID)
					seen[m.User.ID] = true
				}
			}
		}
		if len(ids) > 32 {
			return nil, "", fmt.Errorf("at most 32 organizers are supported; choose a smaller role")
		}
		if len(members) < 1000 {
			break
		}
		last := members[len(members)-1]
		if last.User == nil || last.User.ID == after {
			return nil, "", fmt.Errorf("Discord returned an incomplete member page; no roster was saved")
		}
		after = last.User.ID
		if page == 99 {
			return nil, "", fmt.Errorf("server member scan limit reached; use explicit user mentions")
		}
	}
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("that role has no human members")
	}
	sort.Strings(ids)
	for j, id := range ids {
		if id == actor {
			ids = append([]string{actor}, append(ids[:j], ids[j+1:]...)...)
			break
		}
	}
	return ids, role, nil
}

func tournamentCommand(data discordgo.ApplicationCommandInteractionData) (string, map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	if len(data.Options) > 0 && data.Options[0].Name == "modify" && data.Options[0].Type == discordgo.ApplicationCommandOptionSubCommandGroup {
		data.Options = data.Options[0].Options
	}
	return subcommand(data)
}

func tournamentModifyOptions(str func(string, string, bool) *discordgo.ApplicationCommandOption, integer func(string, string, bool) *discordgo.ApplicationCommandOption, id func() *discordgo.ApplicationCommandOption, sub func(string, string, ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption, existing []*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
	group := &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommandGroup, Name: "modify", Description: "Edit tournament details, registration, organizers, bookings and results"}
	group.Options = []*discordgo.ApplicationCommandOption{
		sub("settings", "Update details or dates; existing bookings/results stay protected", id(), str("title", "Tournament title (1..100 characters)", false), str("prizes", "Prize description (1..1000 characters)", false), str("date", "Move all rounds to this first date, keeping their local hours", false), str("rounds", "Replace round windows: YYYY-MM-DD HH:MM/HH:MM; ...", false), str("registration_closes", "New registration deadline (local date/time or RFC3339)", false), str("timezone", "IANA timezone for future inputs; existing instants are preserved", false), integer("slot_minutes", "Reserved best-of-three minutes (15..180); release bookings first", false), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Announcement channel in this server", ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}}),
		sub("organizers", "Replace/refresh organizers from users or one trusted role", id(), str("organizers", "User mentions/IDs in priority order (or a role mention)", false), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionRole, Name: "organizer_role", Description: "Resolve current human members of this trusted organizer role"}),
		sub("registration", "Reopen or close entry before the format is locked", id(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "action", Description: "Reopen requires a future deadline; close also rechecks GRID0+ eligibility", Required: true, Choices: []*discordgo.ApplicationCommandOptionChoice{{Name: "Open", Value: "open"}, {Name: "Close", Value: "close"}}}),
		sub("view", "Show editable details, organizer roster and round windows", id()),
	}
	for _, o := range existing {
		if o.Name != "create" {
			group.Options = append(group.Options, o)
		}
	}
	return group
}

func tournamentSetOrganizers(t *tournament, ids []string, role string) error {
	if t.State == "complete" || t.State == "cancelled" {
		return fmt.Errorf("event is finished")
	}
	if len(ids) == 0 || len(ids) > 32 {
		return fmt.Errorf("name between one and 32 organizers")
	}
	keep := map[string]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	for _, m := range t.Matches {
		if (m.State == "scheduled" || m.State == "playing") && !keep[m.Organizer] {
			return fmt.Errorf("organizer <@%s> is assigned to %s; reschedule/unschedule it before removing them", m.Organizer, m.ID)
		}
	}
	old := map[string]tournamentOrganizer{}
	for _, o := range t.Organizers {
		old[o.ID] = o
	}
	roster := make([]tournamentOrganizer, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return fmt.Errorf("organizer IDs must be distinct and nonempty")
		}
		seen[id] = true
		o, ok := old[id]
		if !ok {
			o = tournamentOrganizer{ID: id, Zone: t.Zone, From: 12, Until: 24}
		}
		roster = append(roster, o)
	}
	t.Organizers = roster
	t.OrganizerRole = role
	return nil
}

func tournamentEditSettings(d *tournamentData, t *tournament, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, channel, actor string, now time.Time) error {
	if t.State == "complete" || t.State == "cancelled" {
		return fmt.Errorf("event is finished")
	}
	var changed []string
	for _, key := range []string{"title", "prizes", "date", "rounds", "registration_closes", "timezone", "slot_minutes", "channel"} {
		if opts[key] != nil {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		return fmt.Errorf("choose at least one setting to modify")
	}
	if opts["date"] != nil && opts["rounds"] != nil {
		return fmt.Errorf("choose date (move all rounds) or rounds (replace windows), not both")
	}
	if o := opts["title"]; o != nil {
		title := strings.TrimSpace(o.StringValue())
		if n := len([]rune(title)); n < 1 || n > 100 {
			return fmt.Errorf("title must be 1..100 characters")
		}
		t.Title = title
	}
	if o := opts["prizes"]; o != nil {
		prize := strings.TrimSpace(o.StringValue())
		if n := len([]rune(prize)); n < 1 || n > 1000 {
			return fmt.Errorf("prizes must be 1..1000 characters")
		}
		t.Prize = prize
	}
	if o := opts["timezone"]; o != nil {
		if _, e := time.LoadLocation(o.StringValue()); e != nil {
			return fmt.Errorf("invalid IANA timezone")
		}
		t.Zone = o.StringValue()
	}
	if o := opts["rounds"]; o != nil {
		windows, e := tournamentParseWindows(o.StringValue(), t.Zone)
		if e != nil {
			return e
		}
		t.Windows = windows
	}
	if o := opts["date"]; o != nil {
		date, e := time.Parse("2006-01-02", o.StringValue())
		if e != nil {
			return fmt.Errorf("date must be YYYY-MM-DD")
		}
		loc := mustTournamentZone(t.Zone)
		start := t.Windows[0].Start.In(loc)
		base := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		days := int(date.Sub(base) / (24 * time.Hour))
		for j, w := range t.Windows {
			a, b := w.Start.In(loc), w.End.In(loc)
			nextA, nextB := a.AddDate(0, 0, days), b.AddDate(0, 0, days)
			if nextA.Hour() != a.Hour() || nextA.Minute() != a.Minute() || nextB.Hour() != b.Hour() || nextB.Minute() != b.Minute() {
				return fmt.Errorf("date shift crosses a nonexistent local time; supply explicit round windows instead")
			}
			t.Windows[j] = tournamentWindow{Start: nextA, End: nextB}
		}
	}
	if o := opts["registration_closes"]; o != nil {
		deadline, e := tournamentParseTime(o.StringValue(), t.Zone)
		if e != nil {
			return e
		}
		if !deadline.After(now) {
			return fmt.Errorf("new deadline must be in the future")
		}
		if t.State != "registration" && t.State != "closed" {
			return fmt.Errorf("registration deadline is locked after format selection")
		}
		t.Deadline = deadline
	}
	if o := opts["slot_minutes"]; o != nil {
		slot := int(o.IntValue())
		if slot < 15 || slot > 180 {
			return fmt.Errorf("slot_minutes must be 15..180")
		}
		if slot != t.SlotMinutes {
			for _, m := range t.Matches {
				if m.State == "scheduled" || m.State == "playing" || len(m.Games) > 0 {
					return fmt.Errorf("match duration cannot change with bookings or played games")
				}
			}
		}
		t.SlotMinutes = slot
	}
	for _, m := range t.Matches {
		if m.Round > len(t.Windows) {
			return fmt.Errorf("round windows cannot remove a bracket round")
		}
		if (opts["date"] != nil || opts["rounds"] != nil) && (m.State == "complete" || m.State == "forfeit") {
			return fmt.Errorf("round dates cannot be rewritten after matches finish; extend an unplayed round with modify window")
		}
	}
	if e := tournamentValidateWindows(t); e != nil {
		return e
	}
	if channel != "" {
		old := t.Channel
		t.Channel = channel
		for j := range d.Notices {
			if d.Notices[j].Tournament == t.ID && !d.Notices[j].Sent {
				d.Notices[j].Channel = channel
			}
		}
		if old != channel {
			tournamentAddNotice(d, t, "", "Tournament **"+t.Title+"** moved to <#"+channel+">.")
			d.Notices[len(d.Notices)-1].Channel = old
		}
	}
	tournamentAudit(t, actor, "modify settings: "+strings.Join(changed, ", "), now)
	tournamentAddNotice(d, t, "", fmt.Sprintf("**%s** (`%s`) updated: %s. Registration closes <t:%d:F>; first round <t:%d:F>. Prizes: %s", t.Title, t.ID, strings.Join(changed, ", "), t.Deadline.Unix(), t.Windows[0].Start.Unix(), t.Prize))
	for uid := range t.People {
		tournamentAddNotice(d, t, uid, fmt.Sprintf("Tournament **%s** (`%s`) updated: %s. Check the tournament channel and /tournament status for the current details. Existing match times change only through explicit rescheduling.", t.Title, t.ID, strings.Join(changed, ", ")))
	}
	return nil
}
func tournamentSettingsText(t *tournament) string {
	text := fmt.Sprintf("**%s** `%s` — %s\nPrizes: %s\nInput timezone: %s; %d minutes per series\nRegistration closes <t:%d:F>; channel <#%s>\n", t.Title, t.ID, t.State, t.Prize, t.Zone, t.SlotMinutes, t.Deadline.Unix(), t.Channel)
	if t.OrganizerRole != "" {
		text += "Organizer role snapshot: <@&" + t.OrganizerRole + "> (refresh via modify organizers)\n"
	}
	for _, o := range t.Organizers {
		text += fmt.Sprintf("Organizer <@%s>: %02d:00–%02d:00 %s\n", o.ID, o.From, o.Until, o.Zone)
	}
	for j, w := range t.Windows {
		text += fmt.Sprintf("Round %d: <t:%d:F> – <t:%d:t>\n", j+1, w.Start.Unix(), w.End.Unix())
	}
	return text
}

// Delay moves only a booked, unstarted series. It cannot pause a game on a console.
func tournamentDelay(d *tournamentData, t *tournament, id string, minutes int, reason, actor string, now time.Time) error {
	if t.State != "ready" {
		return fmt.Errorf("tournament is not active")
	}
	index, e := tournamentFindMatch(t, id)
	if e != nil {
		return e
	}
	m := &t.Matches[index]
	if m.State != "scheduled" || m.At.IsZero() {
		return fmt.Errorf("only scheduled, unstarted matches can be delayed; this does not pause an in-game match")
	}
	if minutes < 1 || minutes > 10080 {
		return fmt.Errorf("delay must be between one minute and seven days")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("a reason is required")
	}
	old := m.At
	base := old
	if now.After(base) {
		base = now
	}
	at := base.Add(time.Duration(minutes) * time.Minute)
	w := t.Windows[m.Round-1]
	if at.Before(w.Start) || at.Add(time.Duration(t.SlotMinutes)*time.Minute).After(w.End) {
		return fmt.Errorf("delayed match would leave its round window; extend the window with modify window first")
	}
	var organizer *tournamentOrganizer
	for j := range t.Organizers {
		if t.Organizers[j].ID == m.Organizer {
			organizer = &t.Organizers[j]
			break
		}
	}
	if organizer == nil || !tournamentSlotOK(t, m, at, *organizer) || !tournamentAcrossEventsOK(d, t, m, at, *organizer) {
		return fmt.Errorf("delay conflicts with player/organizer availability or another booking; use modify reschedule or release that booking first")
	}
	tournamentInvalidateNotices(d, t, m.ID)
	m.At = at
	text := fmt.Sprintf("**%s** — match `%s` delayed from <t:%d:F> to <t:%d:F>. %s vs %s, organizer <@%s>. Reason: %s", t.Title, m.ID, old.Unix(), at.Unix(), m.A, m.B, m.Organizer, reason)
	tournamentAddNotice(d, t, "", text)
	seen := map[string]bool{}
	for _, uid := range append(append([]string{m.Organizer}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
		if !seen[uid] {
			tournamentAddNotice(d, t, uid, text)
			seen[uid] = true
		}
	}
	tournamentAudit(t, actor, fmt.Sprintf("delay %s from %s to %s: %s", m.ID, old.Format(time.RFC3339), at.Format(time.RFC3339), reason), now)
	return nil
}
