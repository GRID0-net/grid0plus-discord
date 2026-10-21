package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/bwmarrin/discordgo"
)

func TestTournamentCanBeCreatedAndFormattedBeforeRoundsArePlanned(t *testing.T) {
	b := &Bot{}
	b.cfg.Tournaments.AdminRoleIDs = []string{"staff"}
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		if strings.Contains(r.URL.Path, "/members/") {
			return 200, `{"user":{"id":"12345"},"roles":["staff"]}`
		}
		return 200, `{"id":"channel","guild_id":"guild","type":0}`
	})
	_, now := tournamentFixture(0)
	opts := map[string]*discordgo.ApplicationCommandInteractionDataOption{}
	for key, value := range map[string]string{"title": "Flexible cup", "date": "2026-10-23", "registration_closes": "2026-10-22T00:00:00+02:00", "organizers": "12345", "prizes": "Winner role"} {
		opts[key] = tournamentStringOption(key, value)
	}
	cup, e := b.tournamentCreate(context.Background(), s, tournamentInteraction("tournament-admin", "create"), opts, now)
	requireTournamentOK(t, e)
	if len(cup.Windows) != 0 || cup.Date != "2026-10-23" {
		t.Fatal("creation invented round windows")
	}
	fixture, _ := tournamentFixture(8)
	cup.People = fixture.People
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 2, now))
	if tournamentRoundCount(cup) != 2 {
		t.Fatal("format did not derive bracket rounds from team count")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	store, e := openTournamentStore(path)
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	store, e = openTournamentStore(path)
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { tournamentSchedule(d, d.Tournaments[cup.ID], now); return nil }))
	for _, m := range store.data.Tournaments[cup.ID].Matches {
		if !m.At.IsZero() {
			t.Fatal("scheduled without round times")
		}
	}
	cup = store.data.Tournaments[cup.ID]
	start := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	cup.Windows = []tournamentWindow{{Start: start, End: start.Add(5 * time.Hour)}}
	d := &tournamentData{Tournaments: map[string]*tournament{cup.ID: cup}}
	tournamentSchedule(d, cup, now)
	if cup.Matches[0].At.IsZero() {
		t.Fatal("known first-round opponents were not scheduled when its window became available")
	}
	cup.Windows = append(cup.Windows, tournamentWindow{Start: start.Add(24 * time.Hour), End: start.Add(29 * time.Hour)})
	tournamentSchedule(d, cup, now)
	if cup.Matches[0].At.IsZero() {
		t.Fatal("complete calendar did not schedule known opponents")
	}
}
func TestTournamentAnnouncementCounterTracksAcceptanceAndWithdrawalWithoutReposting(t *testing.T) {
	cup, now := tournamentFixture(0)
	path := filepath.Join(t.TempDir(), "state.json")
	store, e := openTournamentStore(path)
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		d.Tournaments[cup.ID] = cup
		tournamentAddNotice(d, cup, "", tournamentAnnouncement(cup))
		d.Notices[0].Announcement = true
		return nil
	}))
	b := &Bot{tournaments: store}
	var posts, edits []string
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		requireTournamentOK(t, json.Unmarshal(raw, &body))
		content, _ := body["content"].(string)
		if r.Method == "POST" {
			posts = append(posts, content)
		} else if r.Method == "PATCH" {
			if !strings.HasSuffix(r.URL.Path, "/messages/announcement") {
				t.Fatal("edited wrong message")
			}
			edits = append(edits, content)
		} else {
			t.Fatalf("unexpected request %s", r.Method)
		}
		return 200, `{"id":"announcement"}`
	})
	b.deliverTournamentNotices(context.Background(), s, now)
	if len(posts) != 1 || !strings.Contains(posts[0], "Participants: 0") {
		t.Fatal("missing initial count")
	}
	a := tournamentPerson{ID: "a", Account: "A", Zone: "Europe/Brussels", From: 12, Until: 24}
	partner := a
	partner.ID = "b"
	partner.Account = "B"
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { return tournamentRegister(d.Tournaments[cup.ID], a, "b", now) }))
	b.deliverTournamentNotices(context.Background(), s, now.Add(5*time.Second))
	if len(edits) != 1 || !strings.Contains(edits[0], "Participants: 1") {
		t.Fatal("invitation incorrectly counted as an entrant")
	}
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { return tournamentAccept(d.Tournaments[cup.ID], partner, true, now) }))
	b.deliverTournamentNotices(context.Background(), s, now.Add(10*time.Second))
	if len(edits) != 2 || !strings.Contains(edits[1], "Participants: 2") {
		t.Fatal("accepted teammate not counted")
	}
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { return tournamentLeave(d.Tournaments[cup.ID], a.ID, now) }))
	b.deliverTournamentNotices(context.Background(), s, now.Add(15*time.Second))
	if len(edits) != 3 || !strings.Contains(edits[2], "Participants: 1") || len(posts) != 1 {
		t.Fatal("withdrawal did not edit original announcement")
	}
	reloaded, e := openTournamentStore(path)
	requireTournamentOK(t, e)
	b.tournaments = reloaded
	b.deliverTournamentNotices(context.Background(), s, now.Add(20*time.Second))
	if len(edits) != 3 || len(posts) != 1 {
		t.Fatal("restart repeated an unchanged announcement")
	}
}
func TestTournamentAnnouncementErrorsBackOffInsteadOfSpammingDiscord(t *testing.T) {
	cup, now := tournamentFixture(1)
	cup.AnnouncementID = "old"
	cup.AnnouncementChannel = cup.Channel
	cup.AnnouncementText = "old count"
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	b := &Bot{tournaments: store}
	calls := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		calls++
		return 403, `{"code":50013,"message":"Missing Permissions"}`
	})
	b.syncTournamentAnnouncements(context.Background(), s, now)
	b.syncTournamentAnnouncements(context.Background(), s, now.Add(5*time.Second))
	if calls != 1 || store.data.Tournaments[cup.ID].AnnouncementError == "" {
		t.Fatal("failed update has no backoff/error state")
	}
	b.syncTournamentAnnouncements(context.Background(), s, now.Add(time.Minute))
	if calls != 2 {
		t.Fatal("failed update did not retry")
	}
}
func TestTournamentOptionalRoundSchemaAndSingleMessageAnnouncement(t *testing.T) {
	cmds := tournamentCommands()
	create := cmds[1].Options[0]
	found := false
	for _, o := range create.Options {
		if o.Name == "rounds" {
			found = true
			if o.Required {
				t.Fatal("rounds still required")
			}
		}
	}
	if !found {
		t.Fatal("optional rounds missing")
	}
	cup, _ := tournamentFixture(0)
	cup.Title = strings.Repeat("🦑", 100)
	cup.Prize = strings.Repeat("🦑", 1000)
	text := tournamentAnnouncement(cup)
	if len(utf16.Encode([]rune(text))) > 2000 {
		t.Fatal("announcement no longer fits a single Discord message")
	}
}

func TestTournamentDeletedAnnouncementIsReplacedAndTracked(t *testing.T) {
	cup, now := tournamentFixture(1)
	cup.AnnouncementID = "deleted"
	cup.AnnouncementChannel = cup.Channel
	cup.AnnouncementText = "old count"
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	b := &Bot{tournaments: store}
	posts := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		if r.Method == "PATCH" {
			return 404, `{"code":10008,"message":"Unknown Message"}`
		}
		posts++
		return 200, `{"id":"replacement"}`
	})
	b.syncTournamentAnnouncements(context.Background(), s, now)
	if posts != 1 || store.data.Tournaments[cup.ID].AnnouncementID != "replacement" || store.data.Tournaments[cup.ID].AnnouncementError != "" {
		t.Fatal("deleted announcement was not replaced safely")
	}
}

func TestTournamentUnstartedFormatCanRebuildWithoutReusingMatchIDs(t *testing.T) {
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	old := map[string]bool{}
	for _, m := range cup.Matches {
		old[m.ID] = true
	}
	d := &tournamentData{Tournaments: map[string]*tournament{cup.ID: cup}}
	tournamentSchedule(d, cup, now)
	requireTournamentOK(t, tournamentPrepareRebuild(d, cup, "admin", now))
	requireTournamentOK(t, tournamentConfigure(cup, 2, now))
	if len(cup.Teams) != 2 || cup.Size != 2 {
		t.Fatal("format did not change")
	}
	for _, m := range cup.Matches {
		if old[m.ID] {
			t.Fatal("rebuilt bracket reused old match ID")
		}
	}
	cup.Matches[0].State = "playing"
	if tournamentPrepareRebuild(d, cup, "admin", now) == nil {
		t.Fatal("allowed rebuilding a started match")
	}
}
func TestTournamentEveryCreationOptionHasAModifyControl(t *testing.T) {
	admin := tournamentCommands()[1]
	create := admin.Options[0]
	var group *discordgo.ApplicationCommandOption
	for _, o := range admin.Options {
		if o.Name == "modify" {
			group = o
		}
	}
	editable := map[string]bool{}
	for _, sub := range group.Options {
		if sub.Name == "settings" || sub.Name == "organizers" {
			for _, o := range sub.Options {
				editable[o.Name] = true
			}
		}
	}
	for _, o := range create.Options {
		if !editable[o.Name] {
			t.Errorf("creation option %s has no edit control", o.Name)
		}
	}
}
