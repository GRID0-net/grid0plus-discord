package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func tournamentCommands() []*discordgo.ApplicationCommand {
	str := func(name, desc string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: desc, Required: required, MaxLength: 1500}
	}
	integer := func(name, desc string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: name, Description: desc, Required: required}
	}
	id := func() *discordgo.ApplicationCommandOption {
		return str("tournament", "Tournament ID from its announcement", true)
	}
	sub := func(name, desc string, opts ...*discordgo.ApplicationCommandOption) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: desc, Options: opts}
	}
	user := &discordgo.ApplicationCommand{Name: "tournament", Description: "Enter GRID0+ tournaments and manage your entry", Options: []*discordgo.ApplicationCommandOption{
		sub("participate", "Register, optionally inviting a teammate (they must accept)", id(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: "team", Description: "Invite this registered GRID0+ player to stay with you in 2v2"}, str("timezone", "IANA timezone, e.g. America/New_York (default Europe/Brussels)", false), integer("from_hour", "Available daily from this local hour (default 12)", false), integer("until_hour", "Available until this local hour, exclusive (default 24)", false)),
		sub("accept", "Accept or decline your teammate invitation", id(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionBoolean, Name: "accept", Description: "Accept (default true), or decline"}, str("timezone", "Your IANA timezone (default Europe/Brussels)", false), integer("from_hour", "Available from local hour (default 12)", false), integer("until_hour", "Available until local hour (default 24)", false)),
		sub("leave", "Withdraw before registration closes", id()),
		sub("availability", "Update your timezone and daily availability", id(), str("timezone", "IANA timezone", true), integer("from_hour", "First available local hour, 0..23", true), integer("until_hour", "End local hour, exclusive, 1..24", true)),
		sub("status", "Show teams, match IDs, schedule and scores", id()),
		sub("match", "Show one match, its players and its next game ID", id(), str("match", "Match ID from the schedule", true)),
	}}
	admin := &discordgo.ApplicationCommand{Name: "tournament-admin", Description: "Admins: manage tournaments and best-of-three matches", DefaultMemberPermissions: ptr(int64(discordgo.PermissionAdministrator)), Options: []*discordgo.ApplicationCommandOption{
		sub("create", "Open registration and announce a tournament", str("title", "Tournament title (max 100 characters)", true), str("date", "First tournament date: YYYY-MM-DD in the organizer timezone", true), str("registration_closes", "YYYY-MM-DD HH:MM in organizer timezone, or RFC3339", true), str("organizers", "Organizer mentions or space-separated Discord IDs; first has scheduling priority", true), str("rounds", "Round windows: YYYY-MM-DD HH:MM/HH:MM; ... (one per elimination round)", true), str("prizes", "Winner/prize description", true), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Announcement channel (default this channel)", ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}}, str("timezone", "Organizer IANA timezone (default Europe/Brussels)", false), integer("slot_minutes", "Reserved minutes per best-of-three match (default 45, 15..180)", false)),
		sub("close", "Close entry, recheck GRID0+ eligibility, and recommend formats", id()),
		sub("format", "Lock teams/bracket and schedule matches; 1 or 2 players per side", id(), integer("players_per_team", "1 for 1v1, 2 for 2v2; locked after choosing", true)),
		sub("window", "Add or extend a round window without displacing booked matches", id(), integer("round", "Round number, starting at 1", true), str("window", "YYYY-MM-DD HH:MM/HH:MM in organizer timezone", true)),
		sub("organizer", "Set an organizer's actual timezone and available hours", id(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "An organizer named at creation", Required: true}, str("timezone", "IANA timezone", true), integer("from_hour", "First available local hour", true), integer("until_hour", "End local hour, exclusive", true)),
		sub("schedule", "Schedule all ready matches using current availability", id()),
		sub("reschedule", "Set a ready match's time and organizer, respecting all availability", id(), str("match", "Match ID", true), str("start", "YYYY-MM-DD HH:MM in organizer timezone, or RFC3339", true), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: "organizer", Description: "Organizer attending this match", Required: true}),
		sub("unschedule", "Release a scheduled slot so availability can be updated", id(), str("match", "Match ID", true), str("reason", "Why this schedule is being released", true)),
		sub("undo", "Correct the last game before a later round has used its result", id(), str("match", "Match ID", true), str("reason", "Why the last result is incorrect", true)),
		sub("start", "Assigned organizer: mark a scheduled match as playing", id(), str("match", "Match ID", true)),
		sub("report", "Assigned organizer: submit one game of the best-of-three", id(), str("match", "Match ID", true), str("game", "Exact next game ID, e.g. ...-M1-G1 (prevents duplicate submissions)", true), str("winner", "Winning team ID, e.g. T1 (not a player name)", true), str("note", "Optional result/evidence note", false)),
		sub("withdraw", "Withdraw an entrant/team; recorded forfeits advance existing brackets", id(), &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Entrant to withdraw", Required: true}, str("reason", "Reason retained in the audit trail", true)),
		sub("cancel", "Cancel the tournament and notify entrants", id(), str("reason", "Why the tournament is cancelled", true)),
		sub("retry-notifications", "Retry failed public/DM notifications", id()),
		sub("audit", "Show recent organizer/admin actions", id()),
	}}
	return []*discordgo.ApplicationCommand{user, admin}
}

func (b *Bot) tournamentAdmin(m *discordgo.Member) bool {
	if m == nil || m.User == nil {
		return false
	}
	if m.Permissions&discordgo.PermissionAdministrator != 0 {
		return true
	}
	for _, role := range m.Roles {
		for _, allowed := range b.cfg.Tournaments.AdminRoleIDs {
			if role == allowed {
				return true
			}
		}
	}
	return false
}
func tournamentValue(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, key string) string {
	if o := opts[key]; o != nil {
		return o.StringValue()
	}
	return ""
}
func tournamentInt(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, key string, def int) int {
	if o := opts[key]; o != nil {
		return int(o.IntValue())
	}
	return def
}
func tournamentUser(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, key string) string {
	if o := opts[key]; o != nil {
		v, _ := o.Value.(string)
		return v
	}
	return ""
}
func tournamentParseTime(value, zone string) (time.Time, error) {
	if t, e := time.Parse(time.RFC3339, value); e == nil {
		return t, nil
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q", zone)
	}
	t, e := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if e != nil || t.Format("2006-01-02 15:04") != value {
		return time.Time{}, fmt.Errorf("invalid local date/time %q; use YYYY-MM-DD HH:MM or RFC3339", value)
	}
	return t, nil
}
func tournamentParseWindows(raw, zone string) ([]tournamentWindow, error) {
	if _, e := time.LoadLocation(zone); e != nil {
		return nil, fmt.Errorf("invalid IANA timezone")
	}
	var out []tournamentWindow
	for _, part := range strings.Split(raw, ";") {
		bits := strings.Split(strings.TrimSpace(part), "/")
		if len(bits) != 2 {
			return nil, fmt.Errorf("round window must be YYYY-MM-DD HH:MM/HH:MM")
		}
		start, e := tournamentParseTime(strings.TrimSpace(bits[0]), zone)
		if e != nil {
			return nil, e
		}
		endText := strings.TrimSpace(bits[1])
		if len(endText) == 5 {
			endText = start.In(mustTournamentZone(zone)).Format("2006-01-02") + " " + endText
		}
		end, e := tournamentParseTime(endText, zone)
		if e != nil {
			return nil, e
		}
		if !start.Before(end) || end.Sub(start) > 12*time.Hour {
			return nil, fmt.Errorf("each round window must last between 1 minute and 12 hours")
		}
		if len(out) > 0 && start.Before(out[len(out)-1].End) {
			return nil, fmt.Errorf("round windows must be ordered and not overlap")
		}
		out = append(out, tournamentWindow{Start: start, End: end})
	}
	if len(out) > 7 {
		return nil, fmt.Errorf("at most seven rounds (128 solo entrants)")
	}
	return out, nil
}
func mustTournamentZone(zone string) *time.Location { loc, _ := time.LoadLocation(zone); return loc }

var tournamentMention = regexp.MustCompile(`^(?:<@!?)?([0-9]{5,22})>?$`)

func tournamentOrganizerIDs(raw string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for _, token := range strings.Fields(strings.ReplaceAll(raw, ",", " ")) {
		m := tournamentMention.FindStringSubmatch(token)
		if m == nil {
			return nil, fmt.Errorf("organizers must be user mentions or Discord IDs")
		}
		if seen[m[1]] {
			continue
		}
		ids = append(ids, m[1])
		seen[m[1]] = true
	}
	if len(ids) < 1 || len(ids) > 8 {
		return nil, fmt.Errorf("name between one and eight organizers")
	}
	return ids, nil
}

func (b *Bot) onTournamentCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if b.tournaments == nil {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Content: "Tournament commands are not configured on this bot.", Flags: discordgo.MessageFlagsEphemeral}})
		return
	}
	sub, opts := subcommand(i.ApplicationCommandData())
	admin := i.ApplicationCommandData().Name == "tournament-admin"
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}})
	r := reply{s: s, i: i}
	if admin && !b.tournamentAdmin(i.Member) {
		r.text("Only tournament admins may use this command.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	actor := i.Member.User.ID
	now := time.Now()
	id := tournamentValue(opts, "tournament")
	answer := "Saved."
	if admin && sub == "create" {
		t, e := b.tournamentCreate(s, i, opts, now)
		if e != nil {
			r.text(e.Error())
			return
		}
		e = b.tournaments.transaction(func(d *tournamentData) error {
			active := 0
			for _, existing := range d.Tournaments {
				if existing.State != "complete" && existing.State != "cancelled" {
					active++
				}
			}
			if active >= 200 {
				return fmt.Errorf("active tournament limit reached; finish or cancel an event first")
			}
			d.Tournaments[t.ID] = t
			tournamentAddNotice(d, t, "", fmt.Sprintf("**%s** — `%s`\nRegistration closes <t:%d:F>. First round <t:%d:F>.\nEnter with `/tournament participate tournament:%s` (optional `team` invitation). GRID0+ account required. Accepted pairs stay together in 2v2; 1v1 splits them into individual entrants.\nPrizes: %s\nTimezone: %s. Format selected after registration closes.", t.Title, t.ID, t.Deadline.Unix(), t.Windows[0].Start.Unix(), t.ID, t.Prize, t.Zone))
			tournamentAudit(t, actor, "created", now)
			return nil
		})
		if e != nil {
			r.text("Could not save tournament: " + e.Error())
			return
		}
		r.text("Created tournament `" + t.ID + "`. Its announcement is queued; use status to check delivery.")
		return
	}
	// Account ownership always comes from the authenticated GRID0+ bot API.
	person := tournamentPerson{ID: actor}
	if !admin && (sub == "participate" || sub == "accept") {
		accept := opts["accept"] == nil || opts["accept"].BoolValue()
		if sub != "accept" || accept {
			me, e := b.api.Me(ctx, actor)
			if e != nil || me.UserID == "" {
				r.text("Could not verify your GRID0+ account. Register/link first, or retry if GRID0+ is unavailable.")
				return
			}
			zone := tournamentValue(opts, "timezone")
			if zone == "" {
				zone = "Europe/Brussels"
			}
			person = tournamentPerson{ID: actor, Account: me.UserID, Zone: zone, From: tournamentInt(opts, "from_hour", 12), Until: tournamentInt(opts, "until_hour", 24)}
		}
	}
	partner := tournamentUser(opts, "team")
	if partner != "" {
		member, e := s.GuildMember(i.GuildID, partner)
		if e != nil || member.User == nil || member.User.Bot {
			r.text("Choose a human member of this server.")
			return
		}
		me, e := b.api.Me(ctx, partner)
		if e != nil || me.UserID == "" {
			r.text("Your invited teammate must register/link a GRID0+ account first.")
			return
		}
	}
	// Recheck every entrant before freezing eligibility, without holding the file lock during HTTP.
	eligible := map[string]string{}
	if admin && (sub == "close" || sub == "format") {
		var entrants []string
		e := b.tournaments.transaction(func(d *tournamentData) error {
			t, e := tournamentFind(d, i.GuildID, id)
			if e != nil {
				return e
			}
			for uid := range t.People {
				entrants = append(entrants, uid)
			}
			return nil
		})
		if e != nil {
			r.text(e.Error())
			return
		}
		for _, uid := range entrants {
			me, e := b.api.Me(ctx, uid)
			if e != nil || me.UserID == "" {
				r.text("Could not verify all entrants against GRID0+. No registration/format change was made. Resolve unavailable/unlinked accounts and retry.")
				return
			}
			eligible[uid] = me.UserID
		}
	}
	err := b.tournaments.transaction(func(d *tournamentData) error {
		t, e := tournamentFind(d, i.GuildID, id)
		if e != nil {
			return e
		}
		if !admin {
			switch sub {
			case "participate":
				if e = tournamentRegister(t, person, partner, now); e != nil {
					return e
				}
				answer = "Registered. Daily availability: " + strconv.Itoa(person.From) + ":00–" + strconv.Itoa(person.Until) + ":00 in " + person.Zone + ". Use /tournament availability to change it."
				if partner != "" {
					tournamentAddNotice(d, t, partner, fmt.Sprintf("<@%s> invited you to their team for **%s** (`%s`). In the tournament's server, run `/tournament accept tournament:%s` to accept, or add `accept:false` to decline. No entry is made for you until you accept.", actor, t.Title, t.ID, t.ID))
					d.Notices[len(d.Notices)-1].Inviter = actor
					answer += " Team invitation queued; your partner must accept before registration closes."
				}
			case "accept":
				accept := opts["accept"] == nil || opts["accept"].BoolValue()
				from := t.Invites[actor]
				if e = tournamentAccept(t, person, accept, now); e != nil {
					return e
				}
				answer = "Invitation declined."
				if accept {
					answer = "Accepted: both of you are entered, and will stay together if 2v2 is selected."
				}
				if from != "" {
					tournamentAddNotice(d, t, from, fmt.Sprintf("<@%s>: %s Tournament `%s`.", actor, answer, t.ID))
				}
			case "leave":
				partner := t.People[actor].Partner
				if e = tournamentLeave(t, actor, now); e != nil {
					return e
				}
				answer = "You withdrew. An accepted partner remains entered as a solo player."
				if partner != "" {
					tournamentAddNotice(d, t, partner, "Your accepted teammate withdrew from **"+t.Title+"**. You remain registered as a solo entrant.")
				}
			case "availability":
				p, ok := t.People[actor]
				if !ok {
					return fmt.Errorf("register for this tournament first")
				}
				if t.State != "registration" && t.State != "closed" && t.State != "ready" {
					return fmt.Errorf("availability cannot be changed now")
				}
				p.Zone = tournamentValue(opts, "timezone")
				p.From = tournamentInt(opts, "from_hour", 12)
				p.Until = tournamentInt(opts, "until_hour", 24)
				if e = tournamentPersonValid(p); e != nil {
					return e
				}
				for _, m := range t.Matches {
					if m.State == "scheduled" || m.State == "playing" {
						for _, uid := range append(append([]string{}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
							if uid == actor {
								return fmt.Errorf("you already have a scheduled match; ask an admin to unschedule before changing availability")
							}
						}
					}
				}
				t.People[actor] = p
			case "status":
				answer = tournamentSummary(t, d)
			case "match":
				index, e := tournamentFindMatch(t, tournamentValue(opts, "match"))
				if e != nil {
					return e
				}
				answer = tournamentMatchText(t, t.Matches[index])
				for _, team := range t.Teams {
					if team.ID == t.Matches[index].A || team.ID == t.Matches[index].B {
						answer += "\n" + team.ID + ": " + tournamentTeamMentions(t, team.ID)
					}
				}
			default:
				return fmt.Errorf("unknown tournament command")
			}
			return nil
		}
		switch sub {
		case "close":
			if t.State != "registration" {
				return fmt.Errorf("registration already closed")
			}
			for uid, p := range t.People {
				if eligible[uid] != p.Account {
					return fmt.Errorf("entries changed during eligibility checks; retry close")
				}
			}
			t.State = "closed"
			t.Invites = map[string]string{}
			answer = tournamentFormats(t)
			tournamentAddNotice(d, t, "", "Registration closed for **"+t.Title+"**. "+answer+" An admin will select the format.")
		case "format":
			for uid, p := range t.People {
				if eligible[uid] != p.Account {
					return fmt.Errorf("account eligibility changed; retry")
				}
			}
			if e = tournamentConfigure(t, tournamentInt(opts, "players_per_team", 0), now); e != nil {
				return e
			}
			tournamentAddNotice(d, t, "", tournamentTeamText(t))
			tournamentSchedule(d, t, now)
			answer = tournamentSummary(t, d)
		case "window":
			if t.State != "registration" && t.State != "closed" && t.State != "ready" {
				return fmt.Errorf("round windows cannot be changed for a finished event")
			}
			round := tournamentInt(opts, "round", 0)
			windows, e := tournamentParseWindows(tournamentValue(opts, "window"), t.Zone)
			if e != nil {
				return e
			}
			if round < 1 || round > len(t.Windows)+1 || round > 7 || len(windows) != 1 {
				return fmt.Errorf("choose an existing round or the next round")
			}
			if round == len(t.Windows)+1 {
				t.Windows = append(t.Windows, windows[0])
			} else {
				t.Windows[round-1] = windows[0]
			}
			if e = tournamentValidateWindows(t); e != nil {
				return e
			}
		case "organizer":
			if t.State != "registration" && t.State != "closed" && t.State != "ready" {
				return fmt.Errorf("event is finished")
			}
			uid := tournamentUser(opts, "user")
			for _, m := range t.Matches {
				if m.Organizer == uid && (m.State == "scheduled" || m.State == "playing") {
					return fmt.Errorf("unschedule this organizer's booked matches before changing their availability")
				}
			}
			zone := tournamentValue(opts, "timezone")
			from, until := tournamentInt(opts, "from_hour", 12), tournamentInt(opts, "until_hour", 24)
			if e = tournamentPersonValid(tournamentPerson{ID: uid, Account: "organizer", Zone: zone, From: from, Until: until}); e != nil {
				return e
			}
			found := false
			for j := range t.Organizers {
				if t.Organizers[j].ID == uid {
					t.Organizers[j] = tournamentOrganizer{ID: uid, Zone: zone, From: from, Until: until}
					found = true
				}
			}
			if !found {
				return fmt.Errorf("user is not an organizer of this event")
			}
		case "schedule":
			if t.State != "ready" {
				return fmt.Errorf("select the format of an active event first")
			}
			tournamentSchedule(d, t, now)
			answer = tournamentSummary(t, d)
		case "reschedule":
			if t.State != "ready" {
				return fmt.Errorf("tournament is not active")
			}
			index, e := tournamentFindMatch(t, tournamentValue(opts, "match"))
			if e != nil {
				return e
			}
			m := &t.Matches[index]
			if m.State != "waiting" && m.State != "scheduled" {
				return fmt.Errorf("only waiting or scheduled matches can be rescheduled")
			}
			if m.A == "" || m.B == "" {
				return fmt.Errorf("opponents are not known yet")
			}
			at, e := tournamentParseTime(tournamentValue(opts, "start"), t.Zone)
			if e != nil {
				return e
			}
			w := t.Windows[m.Round-1]
			if at.Before(now) || at.Before(w.Start) || at.Add(time.Duration(t.SlotMinutes)*time.Minute).After(w.End) {
				return fmt.Errorf("start must be in this round's future window")
			}
			found := false
			for _, org := range t.Organizers {
				if org.ID == tournamentUser(opts, "organizer") && tournamentSlotOK(t, m, at, org) && tournamentAcrossEventsOK(d, t, m, at, org) {
					m.At = at
					m.Organizer = org.ID
					m.State = "scheduled"
					found = true
				}
			}
			if !found {
				return fmt.Errorf("organizer/players are unavailable or already assigned another match")
			}
			tournamentInvalidateNotices(d, t, m.ID)
			text := fmt.Sprintf("Match `%s` rescheduled to <t:%d:F>, organizer <@%s>.", m.ID, at.Unix(), m.Organizer)
			tournamentAddNotice(d, t, "", text)
			for _, uid := range append(append(append([]string{}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...), m.Organizer) {
				tournamentAddNotice(d, t, uid, text)
			}
		case "unschedule":
			index, e := tournamentFindMatch(t, tournamentValue(opts, "match"))
			if e != nil {
				return e
			}
			m := &t.Matches[index]
			if t.State != "ready" || m.State != "scheduled" {
				return fmt.Errorf("only scheduled matches can be released")
			}
			tournamentInvalidateNotices(d, t, m.ID)
			text := "Schedule withdrawn for `" + m.ID + "`: " + tournamentValue(opts, "reason")
			for _, uid := range append(append([]string{m.Organizer}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
				tournamentAddNotice(d, t, uid, text)
			}
			tournamentAddNotice(d, t, "", text)
			m.State = "waiting"
			m.At = time.Time{}
			m.Organizer = ""
		case "undo":
			index, e := tournamentFindMatch(t, tournamentValue(opts, "match"))
			if e != nil {
				return e
			}
			next := t.Matches[index].Next
			var recipients []string
			nextID := ""
			if next >= 0 && t.Matches[index].Winner != "" && !t.Matches[next].At.IsZero() {
				m := t.Matches[next]
				nextID = m.ID
				recipients = append(append([]string{m.Organizer}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...)
			}
			if e = tournamentUndo(t, tournamentValue(opts, "match"), actor, tournamentValue(opts, "reason"), now); e != nil {
				return e
			}
			tournamentInvalidateNotices(d, t, tournamentValue(opts, "match"))
			if nextID != "" {
				tournamentInvalidateNotices(d, t, nextID)
				for _, uid := range recipients {
					if uid != "" {
						tournamentAddNotice(d, t, uid, "Schedule for `"+nextID+"` withdrawn pending a corrected earlier result.")
					}
				}
			}
			tournamentAddNotice(d, t, "", "Correction: last game of `"+tournamentValue(opts, "match")+"` withdrawn. "+tournamentValue(opts, "reason"))
			answer = "Last game withdrawn; report the corrected result using the same game ID. Any dependent schedule has been released."
		case "start":
			if e = tournamentStart(t, tournamentValue(opts, "match"), actor, now); e != nil {
				return e
			}
			index, _ := tournamentFindMatch(t, tournamentValue(opts, "match"))
			answer = "Match marked playing. Next game: `" + t.Matches[index].ID + "-G1`. Report with winner team ID."
		case "report":
			if e = tournamentReport(t, tournamentValue(opts, "match"), tournamentValue(opts, "game"), tournamentValue(opts, "winner"), actor, tournamentValue(opts, "note"), now); e != nil {
				return e
			}
			index, _ := tournamentFindMatch(t, tournamentValue(opts, "match"))
			m := t.Matches[index]
			answer = tournamentMatchText(t, m)
			tournamentAddNotice(d, t, "", answer)
			if t.State == "complete" {
				tournamentAddNotice(d, t, "", fmt.Sprintf("**%s** finished! Winners: %s. Prizes: %s", t.Title, tournamentTeamMentions(t, t.Champion), t.Prize))
			}
			tournamentSchedule(d, t, now)
		case "withdraw":
			previousWinners := map[string]string{}
			for _, m := range t.Matches {
				previousWinners[m.ID] = m.Winner
			}
			if e = tournamentWithdraw(t, tournamentUser(opts, "user"), actor, tournamentValue(opts, "reason"), now); e != nil {
				return e
			}
			tournamentAddNotice(d, t, "", fmt.Sprintf("Entrant withdrawn from **%s**: <@%s>. Reason: %s", t.Title, tournamentUser(opts, "user"), tournamentValue(opts, "reason")))
			for _, m := range t.Matches {
				if m.State == "forfeit" && previousWinners[m.ID] == "" {
					tournamentInvalidateNotices(d, t, m.ID)
					text := "Match `" + m.ID + "` ended by forfeit; winner " + m.Winner + "."
					for _, uid := range append(append([]string{m.Organizer}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
						if uid != "" {
							tournamentAddNotice(d, t, uid, text)
						}
					}
				}
			}
			if t.State == "complete" {
				tournamentAddNotice(d, t, "", fmt.Sprintf("**%s** finished! Winners: %s. Prizes: %s", t.Title, tournamentTeamMentions(t, t.Champion), t.Prize))
			}
			tournamentSchedule(d, t, now)
		case "cancel":
			if t.State == "complete" || t.State == "cancelled" {
				return fmt.Errorf("event is already finished")
			}
			t.State = "cancelled"
			for j := range d.Notices {
				if d.Notices[j].Tournament == t.ID && !d.Notices[j].Sent {
					d.Notices[j].Sent = true
					d.Notices[j].Error = "superseded by cancellation"
				}
			}
			text := "Tournament **" + t.Title + "** cancelled: " + tournamentValue(opts, "reason")
			tournamentAddNotice(d, t, "", text)
			for uid := range t.People {
				tournamentAddNotice(d, t, uid, text)
			}
		case "retry-notifications":
			for j := range d.Notices {
				n := &d.Notices[j]
				if n.Tournament == t.ID && !n.Sent {
					n.Error = ""
				}
			}
			answer = "Failed/queued notifications will retry. Status lists delivery failures."
		case "audit":
			start := len(t.Audit) - 15
			if start < 0 {
				start = 0
			}
			answer = "```\n" + strings.Join(t.Audit[start:], "\n") + "\n```"
		default:
			return fmt.Errorf("unknown admin command")
		}
		tournamentAudit(t, actor, sub+" "+tournamentValue(opts, "match")+" "+tournamentValue(opts, "reason"), now)
		return nil
	})
	if err != nil {
		r.text(err.Error())
		return
	}
	if len([]rune(answer)) > 1800 {
		answer = string([]rune(answer)[:1800]) + "\n… See the tournament channel for the full team/schedule posts."
	}
	r.text(answer)
}

func (b *Bot) tournamentCreate(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, now time.Time) (*tournament, error) {
	zone := tournamentValue(opts, "timezone")
	if zone == "" {
		zone = "Europe/Brussels"
	}
	if _, e := time.LoadLocation(zone); e != nil {
		return nil, fmt.Errorf("invalid IANA organizer timezone")
	}
	windows, e := tournamentParseWindows(tournamentValue(opts, "rounds"), zone)
	if e != nil {
		return nil, e
	}
	date := tournamentValue(opts, "date")
	if _, e = time.Parse("2006-01-02", date); e != nil || windows[0].Start.In(mustTournamentZone(zone)).Format("2006-01-02") != date {
		return nil, fmt.Errorf("date must match the first round window's YYYY-MM-DD")
	}
	deadline, e := tournamentParseTime(tournamentValue(opts, "registration_closes"), zone)
	if e != nil {
		return nil, e
	}
	if !deadline.After(now) || !deadline.Before(windows[0].Start) {
		return nil, fmt.Errorf("registration must close in the future, before the first round")
	}
	slot := tournamentInt(opts, "slot_minutes", 45)
	if slot < 15 || slot > 180 {
		return nil, fmt.Errorf("slot_minutes must be between 15 and 180")
	}
	ids, e := tournamentOrganizerIDs(tournamentValue(opts, "organizers"))
	if e != nil {
		return nil, e
	}
	orgs := []tournamentOrganizer{}
	for _, id := range ids {
		m, e := s.GuildMember(i.GuildID, id)
		if e != nil || m.User == nil || m.User.Bot || !b.tournamentOrganizerAdmin(s, i.GuildID, m) {
			return nil, fmt.Errorf("every organizer must be a human server member with tournament-admin access")
		}
		orgs = append(orgs, tournamentOrganizer{ID: id, Zone: zone, From: 12, Until: 24})
	}
	title, prize := tournamentValue(opts, "title"), tournamentValue(opts, "prizes")
	if len([]rune(title)) < 1 || len([]rune(title)) > 100 || len([]rune(prize)) > 1000 {
		return nil, fmt.Errorf("title must be 1..100 characters; prizes at most 1000")
	}
	channel := i.ChannelID
	if c := opts["channel"]; c != nil {
		channel = tournamentUser(opts, "channel")
	}
	ch, e := s.Channel(channel)
	if e != nil || ch.GuildID != i.GuildID || (ch.Type != discordgo.ChannelTypeGuildText && ch.Type != discordgo.ChannelTypeGuildNews) {
		return nil, fmt.Errorf("announcement channel must be a text channel in this server")
	}
	t := &tournament{ID: tournamentID(), Guild: i.GuildID, Channel: channel, Title: title, Prize: prize, Zone: zone, State: "registration", CreatedBy: i.Member.User.ID, Deadline: deadline, Windows: windows, SlotMinutes: slot, Organizers: orgs, People: map[string]tournamentPerson{}, Invites: map[string]string{}}
	return t, tournamentValidateWindows(t)
}
func tournamentValidateWindows(t *tournament) error {
	if len(t.Windows) == 0 || !t.Deadline.Before(t.Windows[0].Start) {
		return fmt.Errorf("registration must close before the first round")
	}
	for _, m := range t.Matches {
		if (m.State == "scheduled" || m.State == "playing") && m.Round <= len(t.Windows) {
			w := t.Windows[m.Round-1]
			if m.At.Before(w.Start) || m.At.Add(time.Duration(t.SlotMinutes)*time.Minute).After(w.End) {
				return fmt.Errorf("unschedule booked matches before moving their round window")
			}
		}
	}
	for j, w := range t.Windows {
		if !w.Start.Before(w.End) || w.End.Sub(w.Start) < time.Duration(t.SlotMinutes)*time.Minute {
			return fmt.Errorf("each round window must fit at least one full match slot")
		}
		if j > 0 && w.Start.Before(t.Windows[j-1].End) {
			return fmt.Errorf("round windows must be ordered without overlap")
		}
	}
	return nil
}
func tournamentTeamText(t *tournament) string {
	var out strings.Builder
	fmt.Fprintf(&out, "**%s — teams (%dv%d)**\n", t.Title, t.Size, t.Size)
	for _, team := range t.Teams {
		names := []string{}
		for _, id := range team.Players {
			names = append(names, "<@"+id+">")
		}
		fmt.Fprintf(&out, "`%s` %s\n", team.ID, strings.Join(names, " + "))
	}
	return out.String()
}
func tournamentTeamMentions(t *tournament, team string) string {
	var names []string
	for _, id := range tournamentPlayers(t, team) {
		names = append(names, "<@"+id+">")
	}
	return strings.Join(names, " + ")
}
func tournamentInvalidateNotices(d *tournamentData, t *tournament, match string) {
	for j := range d.Notices {
		n := &d.Notices[j]
		if n.Tournament == t.ID && !n.Sent && strings.Contains(n.Text, "`"+match+"`") {
			n.Sent = true
			n.Error = "superseded by schedule/result correction"
		}
	}
}
func tournamentMatchText(t *tournament, m tournamentMatch) string {
	scoreA, scoreB := 0, 0
	for _, g := range m.Games {
		if g.Winner == m.A {
			scoreA++
		} else {
			scoreB++
		}
	}
	when := "not scheduled (waiting for opponents/availability)"
	if !m.At.IsZero() {
		when = fmt.Sprintf("<t:%d:F>, organizer <@%s>", m.At.Unix(), m.Organizer)
	}
	text := fmt.Sprintf("`%s` R%d %s vs %s — %d:%d [%s] %s", m.ID, m.Round, m.A, m.B, scoreA, scoreB, m.State, when)
	if m.Winner == "" && m.A != "" && m.B != "" {
		text += fmt.Sprintf("; next game `%s-G%d`", m.ID, len(m.Games)+1)
	}
	return text
}
func tournamentSummary(t *tournament, d *tournamentData) string {
	var out strings.Builder
	fmt.Fprintf(&out, "**%s** `%s` — %s, %d entrants\n", t.Title, t.ID, t.State, len(t.People))
	if t.State == "registration" || t.State == "closed" {
		out.WriteString(tournamentFormats(t) + "\n")
	}
	pending := 0
	for _, n := range d.Notices {
		if n.Tournament == t.ID && !n.Sent && n.Error == "" {
			pending++
		}
	}
	fmt.Fprintf(&out, "Queued notifications: %d. Use /tournament match for an individual match.\n", pending)
	if len(t.Teams) > 0 {
		out.WriteString(tournamentTeamText(t))
		for _, m := range t.Matches {
			out.WriteString(tournamentMatchText(t, m) + "\n")
		}
	}
	fail := 0
	for _, n := range d.Notices {
		if n.Tournament == t.ID && n.Error != "" && !n.Sent {
			fail++
		}
	}
	if fail > 0 {
		fmt.Fprintf(&out, "%d notifications failed; admins can retry-notifications.\n", fail)
	}
	return out.String()
}
func tournamentWithdraw(t *tournament, id, actor, reason string, now time.Time) error {
	p, ok := t.People[id]
	if !ok {
		return fmt.Errorf("entrant not found")
	}
	if t.State == "registration" {
		if p.Partner != "" {
			q := t.People[p.Partner]
			q.Partner = ""
			t.People[p.Partner] = q
		}
		delete(t.People, id)
		for to, from := range t.Invites {
			if to == id || from == id {
				delete(t.Invites, to)
			}
		}
	} else if t.State == "closed" {
		delete(t.People, id)
		if p.Partner != "" {
			q := t.People[p.Partner]
			q.Partner = ""
			t.People[p.Partner] = q
		}
	} else if t.State == "ready" {
		team := ""
		for _, x := range t.Teams {
			for _, uid := range x.Players {
				if uid == id {
					team = x.ID
				}
			}
		}
		found := false
		for j := range t.Matches {
			m := &t.Matches[j]
			if (m.A == team || m.B == team) && m.Winner == "" && m.A != "" && m.B != "" {
				winner := m.A
				if winner == team {
					winner = m.B
				}
				tournamentAdvance(t, j, winner)
				m.State = "forfeit"
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no active match exists for that entrant's team")
		}
	} else {
		return fmt.Errorf("tournament finished")
	}
	tournamentAudit(t, actor, "withdraw "+id+" "+reason, now)
	return nil
}

// Notifications form a durable outbox. Discord delivery is at-least-once: a
// crash after sending but before recording success can duplicate a message.
func (b *Bot) runTournamentNotices(ctx context.Context, s *discordgo.Session) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			b.deliverTournamentNotices(ctx, s, now)
		}
	}
}
func (b *Bot) deliverTournamentNotices(ctx context.Context, s *discordgo.Session, now time.Time) {

	due := false
	b.tournaments.mu.Lock()
	for _, t := range b.tournaments.data.Tournaments {
		if t.State == "registration" && !now.Before(t.Deadline) {
			due = true
			break
		}
	}
	b.tournaments.mu.Unlock()
	if due {
		tickErr := b.tournaments.transaction(func(d *tournamentData) error {
			for _, t := range d.Tournaments {
				if t.State == "registration" && !now.Before(t.Deadline) {
					t.State = "closed"
					t.Invites = map[string]string{}
					tournamentAddNotice(d, t, "", "Registration deadline reached for **"+t.Title+"**. "+tournamentFormats(t)+" Admin: choose a format after eligibility checks.")
				}
			}
			return nil
		})
		if tickErr != nil {
			b.log.Error("tournament deadlines", "error", tickErr)
			return
		}
	}
	var notices []tournamentNotice
	b.tournaments.mu.Lock()
	for _, n := range b.tournaments.data.Notices {
		if !n.Sent && n.Error == "" {
			notices = append(notices, n)
			if len(notices) == 10 {
				break
			}
		}
	}
	b.tournaments.mu.Unlock()
	for _, n := range notices {
		if ctx.Err() != nil {
			return
		}
		// A cancellation or correction may have superseded an item after the batch was read.
		live := false
		b.tournaments.mu.Lock()
		for _, current := range b.tournaments.data.Notices {
			if current.ID == n.ID && !current.Sent && current.Error == "" {
				live = true
				break
			}
		}
		b.tournaments.mu.Unlock()
		if !live {
			continue
		}
		var err error
		channel := n.Channel
		if n.User != "" {
			var dm *discordgo.Channel
			dm, err = s.UserChannelCreate(n.User, discordgo.WithContext(ctx))
			if err == nil {
				channel = dm.ID
			}
		}
		if err == nil {
			for _, part := range tournamentMessageParts(n.Text) {
				_, err = s.ChannelMessageSendComplex(channel, &discordgo.MessageSend{Content: part, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
				if err != nil {
					break
				}
			}
		}

		if err != nil && ctx.Err() != nil {
			return
		}
		updateErr := b.tournaments.transaction(func(d *tournamentData) error {
			for j := range d.Notices {
				if d.Notices[j].ID == n.ID {
					if err == nil {
						d.Notices[j].Sent = true
						d.Notices[j].Error = ""
					} else {
						d.Notices[j].Error = "Discord delivery failed; use /tournament status and ask an admin to retry"
					}
				}
			}
			return nil
		})
		if updateErr != nil {
			b.log.Error("persist tournament notification", "error", updateErr)
		}
		if err != nil {
			b.log.Warn("tournament notification failed", "notice", n.ID, "error", err)
		}
	}
}

func tournamentMessageParts(text string) []string {
	var out []string
	chars := []rune(text)
	for len(chars) > 0 {
		n := min(1800, len(chars))
		if n < len(chars) {
			for j := n - 1; j > n/2; j-- {
				if chars[j] == '\n' {
					n = j + 1
					break
				}
			}
		}
		out = append(out, string(chars[:n]))
		chars = chars[n:]
	}
	return out
}

func (b *Bot) tournamentOrganizerAdmin(s *discordgo.Session, guild string, m *discordgo.Member) bool {
	if b.tournamentAdmin(m) {
		return true
	}
	g, e := s.Guild(guild)
	if e != nil {
		return false
	}
	if g.OwnerID == m.User.ID {
		return true
	}
	roles, e := s.GuildRoles(guild)
	if e != nil {
		return false
	}
	for _, r := range roles {
		if r.Permissions&discordgo.PermissionAdministrator != 0 {
			for _, id := range m.Roles {
				if id == r.ID {
					return true
				}
			}
		}
	}
	return false
}
