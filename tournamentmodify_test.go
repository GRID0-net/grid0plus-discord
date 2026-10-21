package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestTournamentRoleRosterUsesAllPagesAndExcludesBots(t *testing.T) {
	b := &Bot{}
	b.cfg.Tournaments.AdminRoleIDs = []string{"staff"}
	pages := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/roles") {
			return 200, `[{"id":"staff","permissions":"0"}]`
		}
		if strings.HasSuffix(r.URL.Path, "/members") {
			pages++
			if r.URL.Query().Get("after") == "" {
				members := make([]map[string]any, 1000)
				for j := range members {
					members[j] = map[string]any{"user": map[string]any{"id": fmt.Sprint(10000 + j)}, "roles": []string{}}
				}
				members[0]["roles"] = []string{"staff"}
				raw, _ := json.Marshal(members)
				return 200, string(raw)
			}
			if r.URL.Query().Get("after") != "10999" {
				t.Fatal("bad member pagination cursor")
			}
			return 200, `[{"user":{"id":"20000"},"roles":["staff"]},{"user":{"id":"30000","bot":true},"roles":["staff"]}]`
		}
		t.Fatalf("unexpected request %s", r.URL.Path)
		return 500, ""
	})
	ids, role, e := b.tournamentResolveOrganizers(context.Background(), s, "guild", "20000", "", "staff")
	requireTournamentOK(t, e)
	if role != "staff" || len(ids) != 2 || ids[0] != "20000" || ids[1] != "10000" || pages != 2 {
		t.Fatalf("wrong resolved roster %v role=%s pages=%d", ids, role, pages)
	}
}
func TestTournamentRoleCannotEscalatePermissionsOrReturnAPartialRoster(t *testing.T) {
	for _, scenario := range []string{"untrusted", "forbidden", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			b := &Bot{}
			if scenario != "untrusted" {
				b.cfg.Tournaments.AdminRoleIDs = []string{"staff"}
			}
			s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
				if strings.HasSuffix(r.URL.Path, "/roles") {
					return 200, `[{"id":"staff","permissions":"0"}]`
				}
				if scenario == "untrusted" {
					t.Fatal("untrusted role triggered member scan")
				}
				if scenario == "forbidden" {
					return 403, `{"code":50001,"message":"Missing Access"}`
				}
				return 200, `[]`
			})
			ids, _, e := b.tournamentResolveOrganizers(context.Background(), s, "guild", "actor", "", "staff")
			if e == nil || len(ids) != 0 {
				t.Fatal("accepted untrusted/incomplete/empty role roster")
			}
		})
	}
}
func TestTournamentReplacingOrganizersProtectsAssignmentsAndPreservesHours(t *testing.T) {
	cup, now := tournamentFixture(2)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	d := &tournamentData{Tournaments: map[string]*tournament{cup.ID: cup}}
	tournamentSchedule(d, cup, now)
	if tournamentSetOrganizers(cup, []string{"new"}, "") == nil {
		t.Fatal("removed a booked organizer")
	}
	cup.Organizers[0].From = 15
	requireTournamentOK(t, tournamentSetOrganizers(cup, []string{"new", "organizer"}, "staff"))
	if cup.Organizers[1].From != 15 || cup.OrganizerRole != "staff" {
		t.Fatal("existing availability/provenance lost")
	}
}
func TestTournamentModifyDispatchAndRegistrationReopen(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	cup, _ := tournamentFixture(0)
	cup.State = "closed"
	cup.Deadline = time.Now().Add(time.Hour)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	b := &Bot{tournaments: store}
	b.cfg.Discord.GuildIDs = []string{"guild"}
	calls := 0
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) { calls++; return 200, `{"id":"response"}` })
	i := tournamentInteraction("tournament-admin", "modify")
	i.Member.Permissions = discordgo.PermissionAdministrator
	data := i.Data.(discordgo.ApplicationCommandInteractionData)
	data.Options[0].Type = discordgo.ApplicationCommandOptionSubCommandGroup
	data.Options[0].Options = []*discordgo.ApplicationCommandInteractionDataOption{{Name: "registration", Type: discordgo.ApplicationCommandOptionSubCommand, Options: []*discordgo.ApplicationCommandInteractionDataOption{tournamentStringOption("tournament", cup.ID), tournamentStringOption("action", "open")}}}
	i.Data = data
	b.onInteraction(s, i)
	if store.data.Tournaments[cup.ID].State != "registration" || calls != 2 {
		t.Fatal("group command did not reopen registration")
	}
}
func TestTournamentSettingsAreTransactionalAndKeepBookedMatches(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		d.Tournaments[cup.ID] = cup
		tournamentSchedule(d, d.Tournaments[cup.ID], now)
		return nil
	}))
	at := store.data.Tournaments[cup.ID].Matches[0].At
	opts := map[string]*discordgo.ApplicationCommandInteractionDataOption{"title": tournamentStringOption("title", "Updated cup"), "date": tournamentStringOption("date", "2026-10-23")}
	err := store.transaction(func(d *tournamentData) error {
		return tournamentEditSettings(d, d.Tournaments[cup.ID], opts, "", "admin", now)
	})
	if err == nil || store.data.Tournaments[cup.ID].Title != "Test cup" || !store.data.Tournaments[cup.ID].Matches[0].At.Equal(at) {
		t.Fatal("failed settings mutation leaked across transaction")
	}
	delete(opts, "date")
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error {
		return tournamentEditSettings(d, d.Tournaments[cup.ID], opts, "", "admin", now)
	}))
	if store.data.Tournaments[cup.ID].Title != "Updated cup" || len(store.data.Tournaments[cup.ID].Audit) == 0 {
		t.Fatal("metadata update not audited")
	}
}
func TestTournamentDelayRefusesConflictsAndDoesNotPauseGames(t *testing.T) {
	cup, now := tournamentFixture(4)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	d := &tournamentData{Tournaments: map[string]*tournament{cup.ID: cup}}
	tournamentSchedule(d, cup, now)
	m := &cup.Matches[0]
	old := m.At
	if tournamentDelay(d, cup, m.ID, 15, "network problem", "admin", now) == nil || !m.At.Equal(old) {
		t.Fatal("delay double booked the organizer")
	}
	// Release the other booking and delay exactly fifteen minutes.
	cup.Matches[1].State = "waiting"
	cup.Matches[1].At = time.Time{}
	requireTournamentOK(t, tournamentDelay(d, cup, m.ID, 15, "network problem", "admin", now))
	if !m.At.Equal(old.Add(15 * time.Minute)) {
		t.Fatal("wrong delayed time")
	}
	if len(cup.Audit) == 0 || len(d.Notices) < 4 {
		t.Fatal("missing delay audit/notifications")
	}
	m.State = "playing"
	if tournamentDelay(d, cup, m.ID, 15, "pause", "admin", now) == nil {
		t.Fatal("delay pretended to pause a running game")
	}
}
func TestTournamentDelayInfersEventFromMatchID(t *testing.T) {
	store, e := openTournamentStore(filepath.Join(t.TempDir(), "state.json"))
	requireTournamentOK(t, e)
	cup, now := tournamentFixture(2)
	cup.State = "closed"
	requireTournamentOK(t, tournamentConfigure(cup, 1, now))
	cup.Matches[0].State = "scheduled"
	cup.Matches[0].At = time.Now().Add(time.Hour)
	cup.Matches[0].Organizer = "organizer"
	for id, p := range cup.People {
		p.From = 0
		p.Until = 24
		cup.People[id] = p
	}
	cup.Organizers[0].From = 0
	cup.Organizers[0].Until = 24
	cup.Windows[0] = tournamentWindow{Start: time.Now(), End: time.Now().Add(10 * time.Hour)}
	cup.Deadline = time.Now().Add(-time.Hour)
	requireTournamentOK(t, store.transaction(func(d *tournamentData) error { d.Tournaments[cup.ID] = cup; return nil }))
	old := cup.Matches[0].At
	b := &Bot{tournaments: store}
	b.cfg.Discord.GuildIDs = []string{"guild"}
	var response string
	s := tournamentFakeDiscord(t, func(r *http.Request) (int, string) {
		if r.Method == "PATCH" {
			raw, _ := io.ReadAll(r.Body)
			response = string(raw)
		}
		return 200, `{"id":"response"}`
	})
	i := tournamentInteraction("tournament-admin", "delay", tournamentStringOption("match", cup.Matches[0].ID), &discordgo.ApplicationCommandInteractionDataOption{Name: "minutes", Type: discordgo.ApplicationCommandOptionInteger, Value: float64(15)}, tournamentStringOption("reason", "connection issue"))
	i.Member.Permissions = discordgo.PermissionAdministrator
	b.onInteraction(s, i)
	if !store.data.Tournaments[cup.ID].Matches[0].At.Equal(old.Add(15 * time.Minute)) {
		t.Fatalf("match-only delay failed: %s", response)
	}
}
