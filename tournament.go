package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type tournamentWindow struct{ Start, End time.Time }
type tournamentPerson struct {
	ID, Account, Zone, Partner string
	From, Until                int
}
type tournamentOrganizer struct {
	ID, Zone    string
	From, Until int
}
type tournamentTeam struct {
	ID      string
	Players []string
}
type tournamentMatch struct {
	ID             string
	Round          int
	A, B, Winner   string
	Next, NextSide int
	Organizer      string
	At             time.Time
	State          string
	Games          []tournamentGame
}
type tournamentGame struct {
	ID, Winner, Reporter, Note string
	At                         time.Time
}
type tournamentNotice struct {
	ID, Tournament, Guild, Channel, User, Text string
	Inviter                                    string
	Sent                                       bool
	Error                                      string
}
type tournament struct {
	ID, Guild, Channel, Title, Prize, Zone, State, Champion string
	CreatedBy                                               string
	Deadline                                                time.Time
	Windows                                                 []tournamentWindow
	SlotMinutes                                             int
	Size                                                    int
	Organizers                                              []tournamentOrganizer
	People                                                  map[string]tournamentPerson
	Invites                                                 map[string]string // invited Discord id -> inviting Discord id
	Teams                                                   []tournamentTeam
	Matches                                                 []tournamentMatch
	Audit                                                   []string
}
type tournamentData struct {
	Version     int
	Tournaments map[string]*tournament
	Notices     []tournamentNotice
}
type tournamentStore struct {
	mu   sync.Mutex
	path string
	data tournamentData
}

func tournamentID() string {
	var b [6]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func openTournamentStore(path string) (*tournamentStore, error) {
	s := &tournamentStore{path: path, data: tournamentData{Version: 1, Tournaments: map[string]*tournament{}}}
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 32<<20 {
		return nil, errors.New("tournament store exceeds 32 MiB")
	}
	if e = json.Unmarshal(b, &s.data); e != nil {
		return nil, e
	}
	if s.data.Version != 1 || s.data.Tournaments == nil {
		return nil, errors.New("unsupported tournament store")
	}
	for id, t := range s.data.Tournaments {
		if t == nil || t.ID != id || t.Guild == "" || t.Channel == "" || t.People == nil || t.Invites == nil || len(t.Organizers) == 0 || t.SlotMinutes < 15 || t.SlotMinutes > 180 {
			return nil, errors.New("invalid tournament state; restore a backup instead of resetting registrations")
		}
		switch t.State {
		case "registration", "closed", "ready", "complete", "cancelled":
		default:
			return nil, errors.New("invalid stored tournament status")
		}
		if _, e := time.LoadLocation(t.Zone); e != nil {
			return nil, e
		}
		for _, org := range t.Organizers {
			if e := tournamentPersonValid(tournamentPerson{ID: org.ID, Account: "organizer", Zone: org.Zone, From: org.From, Until: org.Until}); e != nil {
				return nil, e
			}
		}
		for id, p := range t.People {
			if p.ID != id {
				return nil, errors.New("invalid stored entrant identity")
			}
			if e := tournamentPersonValid(p); e != nil {
				return nil, e
			}
		}
		if e := tournamentValidateWindows(t); e != nil {
			return nil, e
		}
		for _, m := range t.Matches {
			if m.Round < 1 || m.Round > len(t.Windows) || m.Next < -1 || m.Next >= len(t.Matches) {
				return nil, errors.New("invalid stored tournament bracket")
			}
		}
	}
	return s, nil
}
func (s *tournamentStore) transaction(fn func(*tournamentData) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, e := json.Marshal(s.data)
	if e != nil {
		return e
	}
	var next tournamentData
	if e = json.Unmarshal(raw, &next); e != nil {
		return e
	}
	if e = fn(&next); e != nil {
		return e
	}
	// An accepted/declined/expired invitation must never be delivered later
	// from an older outbox batch.
	for j := range next.Notices {
		n := &next.Notices[j]
		if n.Inviter == "" || n.Sent {
			continue
		}
		t := next.Tournaments[n.Tournament]
		if t == nil || t.State != "registration" || t.Invites[n.User] != n.Inviter {
			n.Sent = true
			n.Error = "invitation no longer pending"
		}
	}
	updated, e := json.Marshal(next)
	if e != nil {
		return e
	}
	if bytes.Equal(raw, updated) {
		return nil
	}
	raw, e = json.MarshalIndent(next, "", "  ")
	if e != nil {
		return e
	}
	if len(raw) > 32<<20 {
		return errors.New("tournament store full; archive completed events before adding more")
	}
	if e = os.MkdirAll(filepath.Dir(s.path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".tournament-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), s.path); e != nil {
		return e
	}
	s.data = next
	return nil
}
func tournamentFind(d *tournamentData, guild, id string) (*tournament, error) {
	t := d.Tournaments[id]
	if t == nil || t.Guild != guild {
		return nil, errors.New("tournament not found in this server")
	}
	return t, nil
}
func tournamentAddNotice(d *tournamentData, t *tournament, user, text string) {
	d.Notices = append(d.Notices, tournamentNotice{ID: tournamentID(), Tournament: t.ID, Guild: t.Guild, Channel: t.Channel, User: user, Text: text})
}
func tournamentAudit(t *tournament, actor, action string, now time.Time) {
	t.Audit = append(t.Audit, now.UTC().Format(time.RFC3339)+" "+actor+" "+action)
}
func tournamentPersonValid(p tournamentPerson) error {
	if p.ID == "" || p.Account == "" {
		return errors.New("register or link your GRID0+ account first with /register or /link")
	}
	if _, e := time.LoadLocation(p.Zone); e != nil {
		return errors.New("timezone must be an IANA name, e.g. Europe/Brussels")
	}
	if p.From < 0 || p.Until > 24 || p.From >= p.Until {
		return errors.New("availability must be whole hours, 0 <= from < until <= 24")
	}
	return nil
}
func tournamentRegister(t *tournament, p tournamentPerson, partner string, now time.Time) error {
	if t.State != "registration" || !now.Before(t.Deadline) {
		return errors.New("registration is closed")
	}
	if e := tournamentPersonValid(p); e != nil {
		return e
	}
	if len(t.People) >= 128 {
		if _, ok := t.People[p.ID]; !ok {
			return errors.New("this tournament has reached its 128-player limit")
		}
	}
	for id, other := range t.People {
		if id != p.ID && other.Account == p.Account {
			return errors.New("this GRID0+ account is already entered")
		}
	}
	if old, ok := t.People[p.ID]; ok {
		p.Partner = old.Partner
	}
	if partner != "" {
		if partner == p.ID {
			return errors.New("you cannot invite yourself")
		}
		if p.Partner != "" {
			return errors.New("leave your accepted team before inviting a different partner")
		}
		if x, ok := t.People[partner]; ok && x.Partner != "" {
			return errors.New("that player already has an accepted partner")
		}
		for target, from := range t.Invites {
			if target == p.ID || from == p.ID || target == partner || from == partner {
				return errors.New("one of you already has a pending invitation; accept, decline or leave first")
			}
		}
		t.Invites[partner] = p.ID
	}
	t.People[p.ID] = p
	return nil
}
func tournamentAccept(t *tournament, p tournamentPerson, accept bool, now time.Time) error {
	if t.State != "registration" || !now.Before(t.Deadline) {
		return errors.New("registration is closed")
	}
	from := t.Invites[p.ID]
	if from == "" {
		return errors.New("you have no pending team invitation")
	}
	if !accept {
		delete(t.Invites, p.ID)
		return nil
	}
	if e := tournamentPersonValid(p); e != nil {
		return e
	}
	for id, other := range t.People {
		if id != p.ID && other.Account == p.Account {
			return errors.New("this GRID0+ account is already entered")
		}
	}
	a, ok := t.People[from]
	if !ok || a.Partner != "" {
		return errors.New("that invitation is no longer valid")
	}
	if old, ok := t.People[p.ID]; ok {
		if old.Partner != "" {
			return errors.New("you already have an accepted partner")
		}
		p = old
	}
	if len(t.People) >= 128 {
		if _, ok := t.People[p.ID]; !ok {
			return errors.New("tournament full")
		}
	}
	a.Partner = p.ID
	p.Partner = from
	t.People[from] = a
	t.People[p.ID] = p
	delete(t.Invites, p.ID)
	return nil
}
func tournamentLeave(t *tournament, id string, now time.Time) error {
	if t.State != "registration" || !now.Before(t.Deadline) {
		return errors.New("registration is closed; ask an admin to withdraw you")
	}
	p, ok := t.People[id]
	if !ok {
		return errors.New("you are not registered")
	}
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
	return nil
}
func tournamentFormats(t *tournament) string {
	n := len(t.People)
	if n < 2 {
		return fmt.Sprintf("%d entrants: at least two are needed for a tournament.", n)
	}
	if n >= 4 && n%2 == 0 {
		return fmt.Sprintf("%d entrants: 2v2 keeps accepted partners together (%d teams); 1v1 is also available.", n, n/2)
	}
	return fmt.Sprintf("%d entrants: choose 1v1; 2v2 requires an even number of at least four entrants.", n)
}
func tournamentConfigure(t *tournament, size int, now time.Time) error {
	if t.State != "closed" {
		return errors.New("close registration before choosing the format")
	}
	if size != 1 && size != 2 {
		return errors.New("supported formats are 1v1 and 2v2")
	}
	if len(t.People) < size*2 || len(t.People)%size != 0 {
		return errors.New("not enough entrants to make complete opposing teams")
	}
	ids := make([]string, 0, len(t.People))
	for id := range t.People {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Shuffle once, after registration is locked. Accepted pairs remain indivisible.
	for i := len(ids) - 1; i > 0; i-- {
		var b [1]byte
		limit := 256 - (256 % (i + 1))
		for {
			if _, e := rand.Read(b[:]); e != nil {
				return e
			}
			if int(b[0]) < limit {
				break
			}
		}
		j := int(b[0]) % (i + 1)
		ids[i], ids[j] = ids[j], ids[i]
	}
	teams := []tournamentTeam{}
	used := map[string]bool{}
	solo := []string{}
	for _, id := range ids {
		if used[id] {
			continue
		}
		p := t.People[id]
		if size == 2 && p.Partner != "" {
			other, ok := t.People[p.Partner]
			if !ok || other.Partner != id {
				return errors.New("accepted team is inconsistent")
			}
			teams = append(teams, tournamentTeam{Players: []string{id, p.Partner}})
			used[id] = true
			used[p.Partner] = true
		} else {
			solo = append(solo, id)
			used[id] = true
		}
	}
	for len(solo) > 0 {
		teams = append(teams, tournamentTeam{Players: append([]string(nil), solo[:size]...)})
		solo = solo[size:]
	}
	// Pair preservation must not give premade teams preferential bracket positions.
	for i := len(teams) - 1; i > 0; i-- {
		var b [1]byte
		limit := 256 - (256 % (i + 1))
		for {
			if _, e := rand.Read(b[:]); e != nil {
				return e
			}
			if int(b[0]) < limit {
				break
			}
		}
		j := int(b[0]) % (i + 1)
		teams[i], teams[j] = teams[j], teams[i]
	}
	n := 1
	rounds := 0
	for n < len(teams) {
		n *= 2
		rounds++
	}
	if len(t.Windows) < rounds {
		return fmt.Errorf("need %d round windows for %d teams; add windows before choosing the format", rounds, len(teams))
	}
	for i := range teams {
		teams[i].ID = fmt.Sprintf("T%d", i+1)
	}
	t.Size = size
	t.Teams = teams
	t.Matches = nil
	prev := []int{}
	for round, width := 1, n/2; width > 0; round, width = round+1, width/2 {
		cur := []int{}
		for j := 0; j < width; j++ {
			index := len(t.Matches)
			t.Matches = append(t.Matches, tournamentMatch{ID: fmt.Sprintf("%s-M%d", t.ID, index+1), Round: round, Next: -1, State: "waiting"})
			cur = append(cur, index)
			if round == 1 {
				if j < len(teams) {
					t.Matches[index].A = teams[j].ID
				}
				if j+n/2 < len(teams) {
					t.Matches[index].B = teams[j+n/2].ID
				}
			} else {
				t.Matches[prev[j*2]].Next = index
				t.Matches[prev[j*2]].NextSide = 0
				t.Matches[prev[j*2+1]].Next = index
				t.Matches[prev[j*2+1]].NextSide = 1
			}
		}
		prev = cur
	}
	t.State = "ready"
	for i := range t.Matches {
		m := &t.Matches[i]
		if m.Round == 1 && (m.A == "" || m.B == "") {
			winner := m.A
			if winner == "" {
				winner = m.B
			}
			if winner != "" {
				tournamentAdvance(t, i, winner)
				m.State = "bye"
			}
		}
	}
	return nil
}
func tournamentAdvance(t *tournament, index int, winner string) {
	m := &t.Matches[index]
	m.Winner = winner
	m.State = "complete"
	if m.Next < 0 {
		t.Champion = winner
		t.State = "complete"
		return
	}
	next := &t.Matches[m.Next]
	if m.NextSide == 0 {
		next.A = winner
	} else {
		next.B = winner
	}
}
func tournamentPlayers(t *tournament, team string) []string {
	for _, x := range t.Teams {
		if x.ID == team {
			return x.Players
		}
	}
	return nil
}
func tournamentHourOK(at time.Time, minutes int, zone string, from, until int) bool {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return false
	}
	for p := at; !p.After(at.Add(time.Duration(minutes)*time.Minute - time.Nanosecond)); p = p.Add(time.Minute) {
		local := p.In(loc)
		if local.Hour() < from || local.Hour() >= until {
			return false
		}
	}
	return true
}
func tournamentOverlap(a time.Time, am int, b time.Time, bm int) bool {
	return a.Before(b.Add(time.Duration(bm)*time.Minute)) && b.Before(a.Add(time.Duration(am)*time.Minute))
}
func tournamentSlotOK(t *tournament, m *tournamentMatch, at time.Time, org tournamentOrganizer) bool {
	if !tournamentHourOK(at, t.SlotMinutes, org.Zone, org.From, org.Until) {
		return false
	}
	players := append(append([]string{}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...)
	for _, id := range players {
		p := t.People[id]
		if !tournamentHourOK(at, t.SlotMinutes, p.Zone, p.From, p.Until) {
			return false
		}
	}
	for _, x := range t.Matches {
		if x.ID == m.ID || x.At.IsZero() || x.State == "bye" {
			continue
		}
		if tournamentOverlap(at, t.SlotMinutes, x.At, t.SlotMinutes) {
			resources := map[string]bool{org.ID: true}
			for _, id := range players {
				resources[id] = true
			}
			other := append(append([]string{x.Organizer}, tournamentPlayers(t, x.A)...), tournamentPlayers(t, x.B)...)
			for _, id := range other {
				if resources[id] {
					return false
				}
			}
		}
		if x.Winner != "" && x.Next >= 0 && t.Matches[x.Next].ID == m.ID && at.Before(x.At.Add(time.Duration(t.SlotMinutes)*time.Minute)) {
			return false
		}
	}
	return true
}
func tournamentSchedule(d *tournamentData, t *tournament, now time.Time) {
	if t.State != "ready" {
		return
	}
	for index := range t.Matches {
		m := &t.Matches[index]
		if m.A == "" || m.B == "" || m.State != "waiting" {
			continue
		}
		w := t.Windows[m.Round-1]
		start := w.Start
		if now.Add(5 * time.Minute).After(start) {
			start = now.Add(5 * time.Minute).Truncate(time.Minute).Add(time.Minute)
		}
		found := false
		for at := start; !at.Add(time.Duration(t.SlotMinutes)*time.Minute).After(w.End) && !found; at = at.Add(15 * time.Minute) {
			for _, org := range t.Organizers {
				if tournamentSlotOK(t, m, at, org) && tournamentAcrossEventsOK(d, t, m, at, org) {
					m.At = at
					m.Organizer = org.ID
					m.State = "scheduled"
					text := fmt.Sprintf("**%s** — match `%s` (round %d), %s vs %s, best of 3. Starts <t:%d:F> (<t:%d:R>). Organizer: <@%s>. Dates render in your Discord timezone.", t.Title, m.ID, m.Round, m.A, m.B, at.Unix(), at.Unix(), org.ID)
					for _, id := range append(append([]string{}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
						tournamentAddNotice(d, t, id, text)
					}
					tournamentAddNotice(d, t, org.ID, text)
					tournamentAddNotice(d, t, "", text)
					found = true
					break
				}
			}
		}
	}
}
func tournamentFindMatch(t *tournament, id string) (int, error) {
	for i := range t.Matches {
		if t.Matches[i].ID == id {
			return i, nil
		}
	}
	return -1, errors.New("match not found")
}
func tournamentStart(t *tournament, id, actor string, now time.Time) error {
	if t.State != "ready" {
		return errors.New("tournament is not active")
	}
	index, e := tournamentFindMatch(t, id)
	if e != nil {
		return e
	}
	m := &t.Matches[index]
	if m.State != "scheduled" {
		return errors.New("match must have a confirmed schedule first")
	}
	if m.Organizer != actor {
		return errors.New("only the assigned organizer may start this match")
	}
	// Reserved slots are estimates. An earlier match overrunning must not put
	// its organizer or players in two live matches at the same time.
	for _, other := range t.Matches {
		if other.ID == m.ID || other.State != "playing" {
			continue
		}
		busy := map[string]bool{other.Organizer: true}
		for _, id := range append(append([]string{}, tournamentPlayers(t, other.A)...), tournamentPlayers(t, other.B)...) {
			busy[id] = true
		}
		for _, id := range append(append([]string{actor}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
			if busy[id] {
				return errors.New("a participant or organizer still has another match playing")
			}
		}
	}
	if now.Before(m.At) {
		return errors.New("the match's scheduled start has not arrived")
	}
	m.State = "playing"
	tournamentAudit(t, actor, "started "+id, now)
	return nil
}
func tournamentReport(t *tournament, id, game, winner, actor, note string, now time.Time) error {
	if t.State != "ready" {
		return errors.New("tournament is not active")
	}
	index, e := tournamentFindMatch(t, id)
	if e != nil {
		return e
	}
	m := &t.Matches[index]
	if m.State != "playing" {
		return errors.New("match is not playing (or is already complete)")
	}
	if m.Organizer != actor {
		return errors.New("only the assigned organizer may report this match")
	}
	if winner != m.A && winner != m.B {
		return errors.New("winner must be one of this match's team IDs")
	}
	expected := fmt.Sprintf("%s-G%d", m.ID, len(m.Games)+1)
	if game != expected {
		return fmt.Errorf("next game ID is %s; duplicate/out-of-order reports are refused", expected)
	}
	m.Games = append(m.Games, tournamentGame{ID: game, Winner: winner, Reporter: actor, Note: note, At: now})
	score := 0
	for _, g := range m.Games {
		if g.Winner == winner {
			score++
		}
	}
	if score == 2 {
		tournamentAdvance(t, index, winner)
	}
	tournamentAudit(t, actor, game+" winner "+winner+" "+note, now)
	return nil
}

// Correct only the last game, and never rewrite a result another match has used.
func tournamentUndo(t *tournament, id, actor, reason string, now time.Time) error {
	index, e := tournamentFindMatch(t, id)
	if e != nil {
		return e
	}
	m := &t.Matches[index]
	if t.State == "cancelled" || len(m.Games) == 0 || m.State == "forfeit" {
		return errors.New("no reported game can be undone")
	}
	if m.Next >= 0 && m.Winner != "" {
		next := &t.Matches[m.Next]
		if next.State == "playing" || next.Winner != "" || len(next.Games) > 0 {
			return errors.New("the next round has already used this result")
		}
		if m.NextSide == 0 {
			next.A = ""
		} else {
			next.B = ""
		}
		next.State = "waiting"
		next.At = time.Time{}
		next.Organizer = ""
	}
	removed := m.Games[len(m.Games)-1]
	m.Games = m.Games[:len(m.Games)-1]
	m.Winner = ""
	m.State = "playing"
	t.State = "ready"
	t.Champion = ""
	tournamentAudit(t, actor, "undo "+removed.ID+" "+reason, now)
	return nil
}

// Discord identities are global: an organizer or player must not be scheduled
// into two different tournaments at once, either.
func tournamentAcrossEventsOK(d *tournamentData, t *tournament, m *tournamentMatch, at time.Time, org tournamentOrganizer) bool {
	resources := map[string]bool{org.ID: true}
	for _, id := range append(append([]string{}, tournamentPlayers(t, m.A)...), tournamentPlayers(t, m.B)...) {
		resources[id] = true
	}
	for _, other := range d.Tournaments {
		if other.ID == t.ID || other.State != "ready" {
			continue
		}
		for _, x := range other.Matches {
			if x.At.IsZero() || x.State == "bye" || !tournamentOverlap(at, t.SlotMinutes, x.At, other.SlotMinutes) {
				continue
			}
			for _, id := range append(append([]string{x.Organizer}, tournamentPlayers(other, x.A)...), tournamentPlayers(other, x.B)...) {
				if resources[id] {
					return false
				}
			}
		}
	}
	return true
}
