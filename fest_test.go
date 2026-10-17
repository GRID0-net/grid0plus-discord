package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// A mod types an emoji; a reaction carries Discord's form of it. They must
// match whether it is a server emoji or a standard one with or without the
// variation selector Discord adds and drops.
func TestATypedEmojiMatchesTheReactionDiscordSends(t *testing.T) {
	cases := []struct {
		typed string
		got   discordgo.Emoji
	}{
		{"⛄️", discordgo.Emoji{Name: "⛄"}},
		{"⛄", discordgo.Emoji{Name: "⛄️"}},
		{"🍪", discordgo.Emoji{Name: "🍪"}},
		{"<:cookie:123456789012345678>", discordgo.Emoji{Name: "cookie", ID: "123456789012345678"}},
		{"<a:spin:123456789012345678>", discordgo.Emoji{Name: "spin", ID: "123456789012345678"}},
	}
	for _, c := range cases {
		if emojiKey(c.typed) != reactionKey(c.got) {
			t.Errorf("%q does not match reaction %+v", c.typed, c.got)
		}
	}
	if reactionName("<:cookie:123456789012345678>") != "cookie:123456789012345678" {
		t.Error("a server emoji is not named the way the reaction API wants it")
	}
	if emojiKey("🍪") == reactionKey(discordgo.Emoji{Name: "⛄"}) {
		t.Error("two different emoji matched")
	}
}

func TestFestTimesAreReadInEveryFormAModMightType(t *testing.T) {
	want := time.Date(2026, 12, 15, 18, 0, 0, 0, time.UTC)
	for _, in := range []string{"<t:1797357600:F>", "1797357600", "2026-12-15T18:00:00Z", "2026-12-15 18:00", "2026-12-15T18:00"} {
		got, err := parseFestTime(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseFestTime("friday"); err == nil {
		t.Error("an unreadable time was accepted")
	}
}

func TestPaintIsGivenInTheOrderThePlayersWereListed(t *testing.T) {
	players := []string{"w1", "w2", "l1", "l2"}
	got, err := parsePaint("1203, - 850p", players)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["w1"] != 1203 || got["l1"] != 850 {
		t.Errorf("paint = %v, want w1 1203 and l1 850 only", got)
	}
	for _, bad := range []string{"1 2 3 4 5", "12000", "lots"} {
		if _, err := parsePaint(bad, players); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func testFest() Fest {
	start := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	return Fest{FestID: "LANF-00001", Title: "Frostyfest", Posted: map[string]time.Time{},
		Teams: []FestTeam{{Team: "Alpha", Name: "Cookies", Emoji: "🍪", RoleID: "1"},
			{Team: "Bravo", Name: "Snowmen", Emoji: "⛄", RoleID: "2"},
			{Team: "Charlie", Name: "Ornaments", Emoji: "🎄", RoleID: "3"}},
		Timetable: FestTimetable{Start: start, Mid: start.Add(24 * time.Hour), End: start.Add(48 * time.Hour), Close: start.Add(50 * time.Hour)}}
}

// Each announcement belongs to its window. Coming back after one has
// passed, the bot skips it rather than posting it late; a made one is never
// made again.
func TestEachAnnouncementIsMadeOnceAndOnlyInItsWindow(t *testing.T) {
	f := testFest()
	at := func(h int) time.Time { return f.Timetable.Start.Add(time.Duration(h) * time.Hour) }
	cases := map[int]string{-1: "", 1: "started", 25: "halftime", 49: "ended", 51: "results"}
	for h, want := range cases {
		got := strings.Join(dueFestPosts(f, at(h)), ",")
		if got != want {
			t.Errorf("at %+dh: due %q, want %q", h, got, want)
		}
	}
	f.Posted["halftime"] = at(24)
	if due := dueFestPosts(f, at(25)); len(due) != 0 {
		t.Errorf("a made announcement is due again: %v", due)
	}
	if next := nextFestBoundary([]Fest{f}, at(1)); !next.Equal(f.Timetable.Mid) {
		t.Errorf("next boundary = %v, want the midpoint", next)
	}
}

// What a two-team result would show must never list a team the fest does
// not have, even though GRID0+ always ranks three. Pro shows even in a
// Discord-only fest: LAN games can be Pro.
func TestResultsListOnlyTheFestsTeams(t *testing.T) {
	f := testFest()
	f.Teams = f.Teams[:2]
	f.DiscordOnly = true
	var st Standings
	_ = json.Unmarshal([]byte(`{"phase":"closed","final":{"winner":"Bravo",
		"categories":{"PlayerCount":[{"team":"Bravo","ratio":0.6},{"team":"Alpha","ratio":0.4},{"team":"Charlie","ratio":0}],
		"Challenge":[{"team":"Bravo","ratio":0.6}],
		"Yobisai":[{"team":"Bravo","ratio":0.6}],
		"Tricolor":[{"team":"Bravo","ratio":0},{"team":"Alpha","ratio":0}]},
		"points":[{"team":"Bravo","points":300},{"team":"Alpha","points":150},{"team":"Charlie","points":0}]}}`), &st)
	e := resultsEmbed(f, st)
	text := e.Description
	for _, fl := range e.Fields {
		text += fl.Name + fl.Value
	}
	if strings.Contains(text, "Charlie") || !strings.Contains(text, "Pro") || strings.Contains(text, "Conch") || strings.Contains(text, "Tricolor") || !strings.Contains(text, "Snowmen") || !strings.Contains(text, "60.00%") {
		t.Errorf("results = %s", text)
	}
}

// A service is reported down only after several failed reads (a deploy
// restarts everything for a moment), and back only if it was reported.
func TestDowntimeIsReportedOnlyAfterSeveralFailedReads(t *testing.T) {
	tr := newHealthTracker(3)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	read := func(status string) []alertChange {
		now = now.Add(time.Minute)
		return tr.observe(now, map[string]ServiceStatus{"npln": {Name: "npln", Status: status}})
	}
	// A restart: two failed reads, then up. Nothing to say.
	if c := append(read("down"), append(read("down"), read("ok")...)...); len(c) != 0 {
		t.Errorf("a short restart was reported: %v", c)
	}
	read("down")
	read("down")
	c := read("down")
	if len(c) != 1 || !c[0].Down || c[0].Service != "npln" {
		t.Fatalf("third failed read: %v", c)
	}
	if c := read("down"); len(c) != 0 {
		t.Errorf("an outage was reported twice: %v", c)
	}
	c = read("ok")
	if len(c) != 1 || c[0].Down || c[0].For != 4*time.Minute {
		t.Errorf("recovery: %v", c)
	}
	if c := read("unknown"); len(c) != 0 {
		t.Errorf("an unprobeable service was reported: %v", c)
	}
}

// The event stream keeps going through heartbeats, hands on fest entries, and
// says when it connected.
func TestTheFestStreamDeliversInGameVotes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot/v1/fest-events" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"type":"hello"}` + "\n" + `{"type":"ping"}` + "\n" +
			`{"type":"fest_entry","festId":"LANF-00001","discordUserId":"5","team":"Bravo"}` + "\n"))
	}))
	defer srv.Close()
	connected := 0
	var got []FestEvent
	err := NewSwitchNet(srv.URL, "secret").FestEvents(t.Context(), func() { connected++ }, func(e FestEvent) { got = append(got, e) })
	if err == nil || connected != 1 || len(got) != 1 || got[0].Team != "Bravo" || got[0].DiscordUserID != "5" {
		t.Errorf("err %v connected %d events %+v", err, connected, got)
	}
}

// A refused submission names the players it failed on, so the bot can say
// who rather than "someone".
func TestASubmissionRefusalNamesThePlayers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"players_not_registered","message":"x","discordUserIds":["111","222"]}`))
	}))
	defer srv.Close()
	_, err := NewSwitchNet(srv.URL, "secret").SubmitGame(t.Context(), "111", "LANF-00001", SubmitGame{})
	if msg := festExplain(err); !strings.Contains(msg, "<@111> <@222>") {
		t.Errorf("explanation = %q", msg)
	}
}

func TestAnnouncementsReplyToTheVotePostUnlessTheyHaveTheirOwnChannel(t *testing.T) {
	b := &Bot{}
	f := Fest{ChannelID: "100", MessageID: "200"}
	if ch, ref := b.festAnnounceTarget(nil, f); ch != "100" || ref != "the vote post above" {
		t.Fatalf("no announce channel: got %q %q", ch, ref)
	}
	b.cfg.Fest.AnnounceChannelID = "100"
	if ch, _ := b.festAnnounceTarget(nil, f); ch != "100" {
		t.Fatalf("announce channel = vote channel: got %q", ch)
	}
}
