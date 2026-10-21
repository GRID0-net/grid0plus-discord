package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

func tournamentFixture(n int) (*tournament, time.Time) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	t := &tournament{ID: "test", Guild: "guild", Channel: "channel", Title: "Test cup", Zone: "Europe/Brussels", State: "registration", Deadline: now.Add(time.Hour), SlotMinutes: 45, People: map[string]tournamentPerson{}, Invites: map[string]string{}, Organizers: []tournamentOrganizer{{ID: "organizer", Zone: "Europe/Brussels", From: 12, Until: 24}}}
	for j := 0; j < 7; j++ {
		start := now.Add(time.Duration(j*24+3) * time.Hour)
		t.Windows = append(t.Windows, tournamentWindow{Start: start, End: start.Add(10 * time.Hour)})
	}
	for j := 0; j < n; j++ {
		id := fmt.Sprint(j)
		t.People[id] = tournamentPerson{ID: id, Account: "account-" + id, Zone: "Europe/Brussels", From: 12, Until: 24}
	}
	return t, now
}
func requireTournamentOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestTournamentInvitationRequiresAcceptanceAndKeepsPairs(t *testing.T) {
	cup, now := tournamentFixture(0)
	a := tournamentPerson{ID: "a", Account: "A", Zone: "Europe/Brussels", From: 12, Until: 24}
	b := a
	b.ID = "b"
	b.Account = "B"
	requireTournamentOK(t, tournamentRegister(cup, a, b.ID, now))
	if len(cup.People) != 1 || cup.People[a.ID].Partner != "" {
		t.Fatal("invitation must not enter/partner its recipient")
	}
	impostor := b
	impostor.ID = "c"
	if tournamentAccept(cup, impostor, true, now) == nil {
		t.Fatal("another person accepted invitation")
	}
	requireTournamentOK(t, tournamentAccept(cup, b, true, now))
	if cup.People[a.ID].Partner != b.ID || cup.People[b.ID].Partner != a.ID {
		t.Fatal("pair not reciprocal")
	}
	for _, id := range []string{"c", "d"} {
		p := a
		p.ID = id
		p.Account = id
		requireTournamentOK(t, tournamentRegister(cup, p, "", now))
	}
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 2, now))
	found := false
	for _, team := range cup.Teams {
		if strings.Contains(strings.Join(team.Players, ","), "a") {
			found = len(team.Players) == 2 && ((team.Players[0] == "a" && team.Players[1] == "b") || (team.Players[0] == "b" && team.Players[1] == "a"))
		}
	}
	if !found {
		t.Fatal("accepted pair split in 2v2")
	}
	cup, now = tournamentFixture(2)
	a = cup.People["0"]
	b = cup.People["1"]
	a.Partner = b.ID
	b.Partner = a.ID
	cup.People[a.ID] = a
	cup.People[b.ID] = b
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	if len(cup.Teams) != 2 || len(cup.Teams[0].Players) != 1 {
		t.Fatal("1v1 must split pairs")
	}
}
func TestTournamentRefusesUnlinkedDuplicateAndExpiredEntries(t *testing.T) {
	cup, now := tournamentFixture(1)
	p := cup.People["0"]
	p.ID = "other"
	if tournamentRegister(cup, p, "", now) == nil {
		t.Fatal("same GRID0 account entered twice")
	}
	p.Account = ""
	if tournamentRegister(cup, p, "", now) == nil {
		t.Fatal("unlinked entrant accepted")
	}
	p.Account = "other"
	if tournamentRegister(cup, p, "", cup.Deadline) == nil {
		t.Fatal("entry after deadline")
	}
	cup.Invites["other"] = "0"
	requireTournamentOK(t, tournamentAccept(cup, tournamentPerson{ID: "other"}, false, now))
	if len(cup.Invites) != 0 {
		t.Fatal("declining must work without an account")
	}
}
func TestTournamentBracketsWithByesFinishOnceAtTwoWins(t *testing.T) {
	for _, n := range []int{2, 3, 5, 7, 8, 17} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			cup, now := tournamentFixture(n)
			cup.State = "closed"
			requireTournamentOK(t, tournamentConfigure(cup, 1, now))
			played := 0
			for j := range cup.Matches {
				m := &cup.Matches[j]
				if m.State == "bye" {
					continue
				}
				if m.A == "" || m.B == "" {
					t.Fatal("bracket stranded a team")
				}
				m.At = now
				m.Organizer = "organizer"
				m.State = "scheduled"
				requireTournamentOK(t, tournamentStart(cup, m.ID, "organizer", now))
				if tournamentReport(cup, m.ID, m.ID+"-G1", m.A, "intruder", "", now) == nil {
					t.Fatal("unauthorized result")
				}
				requireTournamentOK(t, tournamentReport(cup, m.ID, m.ID+"-G1", m.A, "organizer", "", now))
				if tournamentReport(cup, m.ID, m.ID+"-G1", m.A, "organizer", "", now) == nil {
					t.Fatal("duplicate game counted")
				}
				requireTournamentOK(t, tournamentReport(cup, m.ID, m.ID+"-G2", m.B, "organizer", "", now))
				requireTournamentOK(t, tournamentReport(cup, m.ID, m.ID+"-G3", m.A, "organizer", "", now))
				played++
			}
			if cup.State != "complete" || cup.Champion == "" || played != n-1 {
				t.Fatalf("bad bracket: state=%s played=%d want=%d", cup.State, played, n-1)
			}
		})
	}
}
func TestTournamentSchedulingHonorsTimezonesAndOrganizerPlayerConflicts(t *testing.T) {
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	d := &tournamentData{}
	tournamentSchedule(d, cup, now)
	first, second := cup.Matches[0], cup.Matches[1]
	if first.At.IsZero() || second.At.IsZero() || tournamentOverlap(first.At, 45, second.At, 45) {
		t.Fatal("one organizer double booked")
	}
	if len(d.Notices) == 0 {
		t.Fatal("no schedule notifications")
	}
	cup.Organizers = append(cup.Organizers, tournamentOrganizer{ID: cup.Teams[0].Players[0], Zone: "Europe/Brussels", From: 0, Until: 24})
	org := cup.Organizers[1]
	other := &cup.Matches[1]
	if tournamentSlotOK(cup, other, first.At, org) {
		t.Fatal("organizer booked while playing a different match")
	}
	// A participant in Tokyo only available at noon cannot be scheduled in Brussels afternoon.
	for id, p := range cup.People {
		p.Zone = "Asia/Tokyo"
		p.From = 12
		p.Until = 13
		cup.People[id] = p
	}
	cup.Matches[0].At = time.Time{}
	cup.Matches[0].State = "waiting"
	tournamentSchedule(d, cup, now)
	if !cup.Matches[0].At.IsZero() {
		t.Fatal("ignored player timezone availability")
	}
	// No organizer means no manufactured schedule.
	cup.Organizers = nil
	cup.Matches[1].At = time.Time{}
	cup.Matches[1].State = "waiting"
	tournamentSchedule(d, cup, now)
	if !cup.Matches[1].At.IsZero() {
		t.Fatal("scheduled without organizer")
	}
}
func TestTournamentUndoDoesNotRewriteALaterRound(t *testing.T) {
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	for j := 0; j < 2; j++ {
		m := &cup.Matches[j]
		m.State = "playing"
		m.Organizer = "organizer"
		for game := 1; game <= 2; game++ {
			requireTournamentOK(t, tournamentReport(cup, m.ID, fmt.Sprintf("%s-G%d", m.ID, game), m.A, "organizer", "", now))
		}
	}
	next := &cup.Matches[2]
	next.State = "scheduled"
	next.At = now
	next.Organizer = "organizer"
	requireTournamentOK(t, tournamentUndo(cup, cup.Matches[0].ID, "organizer", "wrong winner", now))
	if next.A != "" || next.State != "waiting" || !next.At.IsZero() {
		t.Fatal("dependent schedule not released")
	}
	m := &cup.Matches[0]
	requireTournamentOK(t, tournamentReport(cup, m.ID, m.ID+"-G2", m.A, "organizer", "fixed", now))
	next.State = "playing"
	if tournamentUndo(cup, m.ID, "organizer", "late", now) == nil {
		t.Fatal("rewrote a consumed result")
	}
}
func TestTournamentPersistenceRollbackRestartAndConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, e := openTournamentStore(path)
	requireTournamentOK(t, e)
	cup, _ := tournamentFixture(2)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	info, e := os.Stat(path)
	requireTournamentOK(t, e)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private state mode %o", info.Mode().Perm())
	}
	before, e := os.ReadFile(path)
	requireTournamentOK(t, e)
	err := store.transaction(func(d *tournamentData) error {
		d.Tournaments[cup.ID].Title = "must roll back"
		return errors.New("reject")
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	after, e := os.ReadFile(path)
	requireTournamentOK(t, e)
	if string(before) != string(after) || store.data.Tournaments[cup.ID].Title != "Test cup" {
		t.Fatal("failed transaction leaked")
	}
	var wg sync.WaitGroup
	for j := 0; j < 12; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := store.transaction(func(d *tournamentData) error {
				d.Tournaments[cup.ID].Audit = append(d.Tournaments[cup.ID].Audit, "action")
				return nil
			}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	reloaded, e := openTournamentStore(path)
	requireTournamentOK(t, e)
	if len(reloaded.data.Tournaments[cup.ID].Audit) != 12 {
		t.Fatal("concurrent updates lost")
	}
	info, e = os.Stat(path)
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { return nil }))
	info2, _ := os.Stat(path)
	if !info.ModTime().Equal(info2.ModTime()) {
		t.Fatal("read-only poll rewrites disk")
	}
	requireTournamentOK(t, os.WriteFile(path, []byte("not json"), 0600))
	if _, e := openTournamentStore(path); e == nil {
		t.Fatal("corrupt state silently replaced")
	}
}
func TestTournamentWindowValidationAndUnicodeMessages(t *testing.T) {
	if _, e := tournamentParseTime("2027-03-28 02:30", "Europe/Brussels"); e == nil {
		t.Fatal("accepted nonexistent DST hour")
	}
	if _, e := tournamentParseWindows("2026-11-01 16:00/18:00;2026-11-01 17:00/19:00", "Europe/Brussels"); e == nil {
		t.Fatal("overlapping rounds accepted")
	}
	text := strings.Repeat("🦑é", 2200)
	parts := tournamentMessageParts(text)
	if strings.Join(parts, "") != text {
		t.Fatal("message splitter lost characters")
	}
	for _, p := range parts {
		if !utf8.ValidString(p) || len([]rune(p)) > 1800 {
			t.Fatal("invalid message chunk")
		}
	}
}
func TestTournamentCommandPermissionsAndDiscordLimits(t *testing.T) {
	bot := &Bot{}
	if bot.tournamentAdmin(&discordgo.Member{User: &discordgo.User{ID: "ordinary"}}) {
		t.Fatal("ordinary user controls tournament")
	}
	bot.cfg.Tournaments.AdminRoleIDs = []string{"organizer"}
	if !bot.tournamentAdmin(&discordgo.Member{User: &discordgo.User{ID: "staff"}, Roles: []string{"organizer"}}) {
		t.Fatal("configured organizer rejected")
	}
	var check func([]*discordgo.ApplicationCommandOption)
	check = func(opts []*discordgo.ApplicationCommandOption) {
		optional := false
		if len(opts) > 25 {
			t.Fatal("too many Discord options")
		}
		for _, o := range opts {
			if len(o.Name) > 32 || len(o.Description) > 100 {
				t.Fatalf("Discord option too long: %s", o.Name)
			}
			if o.Type != discordgo.ApplicationCommandOptionSubCommand {
				if !o.Required {
					optional = true
				} else if optional {
					t.Fatalf("required option after optional: %s", o.Name)
				}
			}
			check(o.Options)
		}
	}
	cmds := tournamentCommands()
	if cmds[1].DefaultMemberPermissions == nil || *cmds[1].DefaultMemberPermissions != discordgo.PermissionAdministrator {
		t.Fatal("admin controls exposed by default")
	}
	for _, c := range cmds {
		check(c.Options)
	}
}

type tournamentRoundTripper func(*http.Request) (*http.Response, error)

func (f tournamentRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func tournamentFakeDiscord(t *testing.T, handle func(*http.Request) (int, string)) *discordgo.Session {
	t.Helper()
	s, e := discordgo.New("Bot test-token")
	requireTournamentOK(t, e)
	s.Client = &http.Client{Transport: tournamentRoundTripper(func(r *http.Request) (*http.Response, error) {
		status, body := handle(r)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	return s
}
func tournamentInteraction(command, sub string, options ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "interaction", AppID: "app", Token: "test-token", Type: discordgo.InteractionApplicationCommand, GuildID: "guild", ChannelID: "channel", Member: &discordgo.Member{User: &discordgo.User{ID: "entrant"}}, Data: discordgo.ApplicationCommandInteractionData{Name: command, Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: sub, Type: discordgo.ApplicationCommandOptionSubCommand, Options: options}}}}}
}
func tournamentStringOption(name, value string) *discordgo.ApplicationCommandInteractionDataOption {
	return &discordgo.ApplicationCommandInteractionDataOption{Name: name, Type: discordgo.ApplicationCommandOptionString, Value: value}
}
func TestTournamentInteractionsUseCallerIdentityAndFailClosed(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			var apiCalls int
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiCalls++
				if r.URL.Path != "/bot/v1/me" || r.Header.Get("X-Discord-User-ID") != "entrant" {
					t.Error("eligibility was checked for the wrong identity")
				}
				if !allowed {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"code":"unavailable"}`)
					return
				}
				fmt.Fprint(w, `{"userId":"owned-account","discordUserId":"entrant"}`)
			}))
			defer api.Close()
			store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
			requireTournamentOK(t, e)
			cup, _ := tournamentFixture(0)
			cup.Deadline = time.Now().Add(time.Hour)
			requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
			b := &Bot{api: NewSwitchNet(api.URL, "test"), tournaments: store}
			b.cfg.Discord.GuildIDs = []string{"guild"}
			var content string
			callbacks := 0
			s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
				raw, _ := io.ReadAll(r.Body)
				if strings.HasSuffix(r.URL.Path, "/callback") {
					callbacks++
					var msg map[string]any
					requireTournamentOK(t, json.Unmarshal(raw, &msg))
					data := msg["data"].(map[string]any)
					if int(data["flags"].(float64)) != int(discordgo.MessageFlagsEphemeral) {
						t.Error("account command response not private")
					}
				} else if r.Method == "PATCH" {
					var msg map[string]any
					requireTournamentOK(t, json.Unmarshal(raw, &msg))
					content, _ = msg["content"].(string)
				} else {
					t.Fatalf("unexpected Discord request %s %s", r.Method, r.URL.Path)
				}
				return 200, `{"id":"message"}`
			})
			i := tournamentInteraction("tournament", "participate", tournamentStringOption("tournament", cup.ID))
			b.onInteraction(s, i)
			if apiCalls != 1 || callbacks != 1 {
				t.Fatalf("calls api=%d callbacks=%d", apiCalls, callbacks)
			}
			p, entered := store.data.Tournaments[cup.ID].People["entrant"]
			if entered != allowed {
				t.Fatalf("entered=%v allowed=%v: %s", entered, allowed, content)
			}
			if allowed && p.Account != "owned-account" {
				t.Fatal("account not bound to API identity")
			}
			// The existing guild/DM gate must remain intact for the new command family.
			i.GuildID = "unknown"
			b.onInteraction(s, i)
			i.GuildID = ""
			b.onInteraction(s, i)
			if apiCalls != 1 || callbacks != 1 {
				t.Fatal("DM/foreign guild reached command handler")
			}
			i = tournamentInteraction("tournament-admin", "cancel", tournamentStringOption("tournament", cup.ID))
			b.onInteraction(s, i)
			if store.data.Tournaments[cup.ID].State != "registration" || !strings.Contains(content, "Only tournament admins") {
				t.Fatal("ordinary entrant controlled tournament")
			}
		})
	}
}
func TestTournamentOutboxSurvivesClosedDMAndClosesDeadline(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	cup, now := tournamentFixture(2)
	cup.Deadline = now
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		d.Tournaments[cup.ID] = cup
		tournamentAddNotice(d, cup, "blocked", "invitation")
		return nil
	}))
	b := &Bot{tournaments: store, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	posts := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/users/@me/channels") {
			return 403, `{"code":50007,"message":"Cannot send messages to this user"}`
		}
		if strings.HasSuffix(r.URL.Path, "/channels/channel/messages") {
			posts++
			var msg map[string]any
			raw, _ := io.ReadAll(r.Body)
			requireTournamentOK(t, json.Unmarshal(raw, &msg))
			mentions := msg["allowed_mentions"].(map[string]any)
			if parsed, _ := mentions["parse"].([]any); len(parsed) != 0 {
				t.Error("announcement can ping arbitrary roles")
			}
			return 200, `{"id":"message"}`
		}
		t.Fatalf("unexpected request %s", r.URL.Path)
		return 500, ""
	})
	b.deliverTournamentNotices(context.Background(), s, now)
	if store.data.Tournaments[cup.ID].State != "closed" || posts != 1 {
		t.Fatal("deadline announcement missing")
	}
	if store.data.Notices[0].Error == "" || store.data.Notices[0].Sent {
		t.Fatal("blocked DM silently considered delivered")
	}
	b.deliverTournamentNotices(context.Background(), s, now.Add(time.Minute))
	if posts != 1 {
		t.Fatal("successful notice repeated")
	}
	restarted, e := openTournamentStore(store.path)
	requireTournamentOK(t, e)
	if restarted.data.Notices[0].Error == "" {
		t.Fatal("delivery failure lost across restart")
	}
}
func TestTournamentSchedulerAvoidsOtherEvents(t *testing.T) {
	first, now := tournamentFixture(2)
	first.State = "closed"
	requireTournamentOK(t, tournamentConfigure(first, 1, now))
	second, _ := tournamentFixture(2)
	second.ID = "second"
	second.State = "closed"
	requireTournamentOK(t, tournamentConfigure(second, 1, now))
	d := &tournamentData{Tournaments: map[string]*tournament{first.ID: first, second.ID: second}}
	tournamentSchedule(d, first, now)
	tournamentSchedule(d, second, now)
	if tournamentOverlap(first.Matches[0].At, 45, second.Matches[0].At, 45) {
		t.Fatal("organizer double booked across events")
	}
}

func TestTournamentCreateQueuesAnnouncementWithoutSendingIt(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	b := &Bot{tournaments: store}
	b.cfg.Discord.GuildIDs = []string{"guild"}
	b.cfg.Tournaments.AdminRoleIDs = []string{"staff"}
	requests := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		requests++
		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"), r.Method == "PATCH":
			return 200, `{"id":"response"}`
		case strings.HasSuffix(r.URL.Path, "/guilds/guild/members/12345"):
			return 200, `{"user":{"id":"12345","bot":false},"roles":["staff"]}`
		case strings.HasSuffix(r.URL.Path, "/channels/channel"):
			return 200, `{"id":"channel","guild_id":"guild","type":0}`
		default:
			t.Fatalf("unexpected Discord request %s", r.URL.Path)
			return 500, ""
		}
	})
	date := time.Now().Add(72 * time.Hour).UTC().Format("2006-01-02")
	i := tournamentInteraction("tournament-admin", "create", tournamentStringOption("title", "Grid0 cup"), tournamentStringOption("date", date), tournamentStringOption("registration_closes", time.Now().Add(time.Hour).Format(time.RFC3339)), tournamentStringOption("rounds", date+" 18:00/23:00"), tournamentStringOption("organizers", "<@12345>"), tournamentStringOption("prizes", "Winner role"))
	i.Member.Permissions = discordgo.PermissionAdministrator
	b.onInteraction(s, i)
	if len(store.data.Tournaments) != 1 || len(store.data.Notices) != 1 || store.data.Notices[0].Sent || requests != 4 {
		t.Fatalf("event not durably queued: tournaments=%d notices=%d requests=%d", len(store.data.Tournaments), len(store.data.Notices), requests)
	}
}

func TestTournamentForfeitAdvancesAndOverrunBlocksNextStart(t *testing.T) {
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	m := &cup.Matches[0]
	loser := tournamentPlayers(cup, m.A)[0]
	requireTournamentOK(t, tournamentWithdraw(cup, loser, "organizer", "left", now))
	if m.State != "forfeit" || m.Winner != m.B || cup.Matches[m.Next].A != m.B {
		t.Fatal("forfeit did not advance opponent")
	}
	// An organizer cannot start the next booked match while an earlier one overruns.
	cup, now = tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	cup.Matches[0].State = "playing"
	cup.Matches[0].Organizer = "organizer"
	cup.Matches[0].At = now.Add(-2 * time.Hour)
	cup.Matches[1].State = "scheduled"
	cup.Matches[1].Organizer = "organizer"
	cup.Matches[1].At = now
	if tournamentStart(cup, cup.Matches[1].ID, "organizer", now) == nil {
		t.Fatal("organizer double booked into live games")
	}
}

func TestTournamentAcceptedInvitationCannotBeDeliveredFromAnOldBatch(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	cup, now := tournamentFixture(2)
	cup.Invites["1"] = "0"
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		d.Tournaments[cup.ID] = cup
		tournamentAddNotice(d, cup, "1", "pending invitation")
		d.Notices[0].Inviter = "0"
		return nil
	}))
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		return tournamentAccept(d.Tournaments[cup.ID], cup.People["1"], true, now)
	}))
	if !store.data.Notices[0].Sent {
		t.Fatal("accepted invitation remained deliverable")
	}
}

func TestTournamentOrganizerAdminResolvesRESTMemberRoles(t *testing.T) {
	b := &Bot{}
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/guilds/guild/roles"):
			return 200, `[{"id":"administrator","permissions":"8"}]`
		case strings.HasSuffix(r.URL.Path, "/guilds/guild"):
			return 200, `{"id":"guild","owner_id":"owner"}`
		default:
			t.Fatalf("unexpected REST permission request %s", r.URL.Path)
			return 500, ""
		}
	})
	if !b.tournamentOrganizerAdmin(s, "guild", &discordgo.Member{User: &discordgo.User{ID: "admin"}, Roles: []string{"administrator"}}) {
		t.Fatal("administrator role rejected because REST member lacks Permissions")
	}
	if !b.tournamentOrganizerAdmin(s, "guild", &discordgo.Member{User: &discordgo.User{ID: "owner"}}) {
		t.Fatal("guild owner rejected")
	}
	if b.tournamentOrganizerAdmin(s, "guild", &discordgo.Member{User: &discordgo.User{ID: "ordinary"}}) {
		t.Fatal("ordinary guild member accepted as organizer")
	}
}
