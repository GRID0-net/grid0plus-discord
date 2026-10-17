package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Splatfests on Discord: a mod's vote post, voting by reaction, the team
// role, LAN games players submit and a mod reviews. GRID0+ holds every fest,
// vote and game (internal/baas/botfest.go); this keeps only a cache of which
// message is which fest's vote post.

// festState is the bot's cache of fests and what it has done this run.
type festState struct {
	mu        sync.Mutex
	fests     map[string]Fest   // by fest id
	byMessage map[string]string // vote post message id -> fest id
	// roles records the team role each person was given this run, so a
	// catch-up after a reconnect does not touch every member again.
	roles map[string]string // festID/discordID -> team
	// creating holds /fest create's options while its description form is
	// open, by the command's interaction id.
	creating map[string]pendingFestCreate
	wake     chan struct{}
}

type pendingFestCreate struct {
	owner   string
	in      CreateFest
	channel string
	expires time.Time
}

func newFestState() *festState {
	return &festState{
		fests: map[string]Fest{}, byMessage: map[string]string{}, roles: map[string]string{},
		creating: map[string]pendingFestCreate{}, wake: make(chan struct{}, 1),
	}
}

// refreshFests reloads the fest list from GRID0+ and wakes the announcer.
func (b *Bot) refreshFests(ctx context.Context) error {
	list, err := b.api.Fests(ctx)
	if err != nil {
		return err
	}
	st := b.fest
	st.mu.Lock()
	st.fests, st.byMessage = map[string]Fest{}, map[string]string{}
	for _, f := range list {
		st.fests[f.FestID] = f
		if f.MessageID != "" {
			st.byMessage[f.MessageID] = f.FestID
		}
	}
	st.mu.Unlock()
	select {
	case st.wake <- struct{}{}:
	default:
	}
	return nil
}

func (b *Bot) cachedFest(id string) (Fest, bool) {
	b.fest.mu.Lock()
	defer b.fest.mu.Unlock()
	f, ok := b.fest.fests[id]
	return f, ok
}

func (b *Bot) cachedFests() []Fest {
	b.fest.mu.Lock()
	defer b.fest.mu.Unlock()
	out := make([]Fest, 0, len(b.fest.fests))
	for _, f := range b.fest.fests {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, c Fest) int { return a.Timetable.Start.Compare(c.Timetable.Start) })
	return out
}

func (b *Bot) isMod(m *discordgo.Member) bool {
	if m == nil {
		return false
	}
	return slices.ContainsFunc(m.Roles, func(r string) bool { return slices.Contains(b.cfg.Fest.ModRoleIDs, r) })
}

// ---- emoji ----------------------------------------------------------------------

var customEmojiPattern = regexp.MustCompile(`^<a?:([A-Za-z0-9_~]+):([0-9]+)>$`)

// reactionName is how the Discord API names an emoji when adding a reaction:
// "name:id" for a server emoji, the character itself otherwise.
func reactionName(typed string) string {
	typed = strings.TrimSpace(typed)
	if m := customEmojiPattern.FindStringSubmatch(typed); m != nil {
		return m[1] + ":" + m[2]
	}
	return typed
}

// emojiKey compares an emoji a mod typed with one a reaction carries: a
// server emoji by id, a standard one by its characters without the
// variation selector, which Discord adds or drops at will.
func emojiKey(typed string) string {
	typed = strings.TrimSpace(typed)
	if m := customEmojiPattern.FindStringSubmatch(typed); m != nil {
		return m[2]
	}
	return strings.ReplaceAll(typed, "️", "")
}

func reactionKey(e discordgo.Emoji) string {
	if e.ID != "" {
		return e.ID
	}
	return strings.ReplaceAll(e.Name, "️", "")
}

// ---- parsing --------------------------------------------------------------------

var discordTimestampPattern = regexp.MustCompile(`^<t:(-?[0-9]+)(:[a-zA-Z])?>$`)

// parseFestTime reads a time a mod typed: a Discord timestamp (<t:...>), a
// Unix time, RFC 3339, or "YYYY-MM-DD HH:MM" in UTC.
func parseFestTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if m := discordTimestampPattern.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04 UTC"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time: use a Discord timestamp, or YYYY-MM-DD HH:MM in UTC", s)
}

// parsePaint reads the paint a player typed for a LAN game: one number per
// player in the order they were listed (winners, then losers), "-" for one
// nobody knows. Fewer numbers than players leaves the rest unknown.
func parsePaint(s string, players []string) (map[string]int64, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '/' || r == ';' })
	if len(fields) > len(players) {
		return nil, fmt.Errorf("%d paint numbers for %d players", len(fields), len(players))
	}
	out := map[string]int64{}
	for i, f := range fields {
		if f == "-" || f == "?" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.ToLower(f), "p"), 10, 64)
		if err != nil || n < 0 || n > 9999 {
			return nil, fmt.Errorf("%q is not a paint number (0 to 9999)", f)
		}
		out[players[i]] = n
	}
	return out, nil
}

// ---- presentation ---------------------------------------------------------------

const festColour = 0xf5d90a

func teamLabel(f Fest, team string) string {
	t, ok := f.team(team)
	if !ok {
		return team
	}
	emoji := t.Emoji
	return emoji + " **" + t.Name + "**"
}

func festPostEmbed(f Fest) *discordgo.MessageEmbed {
	var teams []string
	for _, t := range f.Teams {
		teams = append(teams, t.Emoji+"  **Team "+t.Name+"** <@&"+t.RoleID+">")
	}
	desc := strings.TrimSpace(f.Description)
	if desc != "" {
		desc += "\n\n"
	}
	desc += strings.Join(teams, "\n")
	desc += fmt.Sprintf("\n\nPlay opens <t:%d:F> (<t:%d:R>) and ends <t:%d:F>.",
		f.Timetable.Start.Unix(), f.Timetable.Start.Unix(), f.Timetable.End.Unix())
	desc += "\nReact with your team's emoji to join it. **Your first pick is final**, just like in game"
	if !f.DiscordOnly {
		desc += ", and picking in Splatoon 3 counts too"
	}
	desc += ".\nPlaying over LAN? Report each game with `/fest submit`; a mod checks it before it counts."
	footer := "You need a GRID0+ account to join: /register"
	if f.DiscordOnly {
		footer = "LAN fest · " + footer
	}
	return &discordgo.MessageEmbed{
		Title: f.Title, Description: desc, Color: festColour,
		Footer: &discordgo.MessageEmbedFooter{Text: footer + " · " + f.FestID},
	}
}

func percent(r float64) string { return fmt.Sprintf("%.2f%%", r*100) }

// categoryNames are the result's rankings, as the game's result screen names
// them, in its order.
var categoryNames = []struct{ key, name string }{
	{"Yobisai", "Conch Shells"}, {"PlayerCount", "Votes"}, {"Regular", "Open"},
	{"Challenge", "Pro"}, {"Tricolor", "Tricolor"},
}

// festShares lists one ranking, keeping only this fest's teams: GRID0+
// always ranks three, and a two-team fest has no Charlie.
func festShares(f Fest, shares []TeamShare) []string {
	var lines []string
	place := 0
	for _, s := range shares {
		if _, ok := f.team(s.Team); !ok {
			continue
		}
		place++
		lines = append(lines, fmt.Sprintf("%d. %s: %s", place, teamLabel(f, s.Team), percent(s.Ratio)))
	}
	return lines
}

func halftimeEmbed(f Fest, st Standings) *discordgo.MessageEmbed {
	desc := "Play is open until <t:" + strconv.FormatInt(f.Timetable.End.Unix(), 10) + ":F>.\n\n"
	if lines := festShares(f, st.Midterm); len(lines) > 0 {
		desc += strings.Join(lines, "\n")
	} else {
		desc += "Nobody has joined a team yet."
	}
	return &discordgo.MessageEmbed{Title: f.Title + ": halftime report", Description: desc, Color: festColour}
}

func resultsEmbed(f Fest, st Standings) *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{Title: f.Title + ": results", Color: festColour}
	if st.Final == nil {
		e.Description = "Nobody joined, so there is no winner."
		return e
	}
	switch {
	case st.Final.Winner != "":
		e.Description = "🎉 " + teamLabel(f, st.Final.Winner) + " wins!"
	default:
		e.Description = "It's a tie!"
	}
	for _, c := range categoryNames {
		// A Discord-only fest has no Pro or Tricolor battles; those rankings
		// would only repeat the vote.
		if f.DiscordOnly && (c.key == "Challenge" || c.key == "Tricolor") {
			continue
		}
		if lines := festShares(f, st.Final.Categories[c.key]); len(lines) > 0 {
			e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: c.name, Value: strings.Join(lines, "\n"), Inline: true})
		}
	}
	var points []string
	for _, p := range st.Final.Points {
		if _, ok := f.team(p.Team); ok {
			points = append(points, fmt.Sprintf("%s: %d", teamLabel(f, p.Team), p.Points))
		}
	}
	if len(points) > 0 {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Points", Value: strings.Join(points, "\n")})
	}
	return e
}

func mentionList(ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "<@"+id+">")
	}
	return strings.Join(out, " ")
}

func submissionSides(s Submission) (winners, losers []string) {
	for _, p := range s.Players {
		if p.Team == s.WinningTeam {
			winners = append(winners, p.DiscordUserID)
		} else {
			losers = append(losers, p.DiscordUserID)
		}
	}
	return winners, losers
}

func paintLine(s Submission) string {
	var parts []string
	for _, p := range s.Players {
		v := "average"
		if p.Paint != nil {
			v = strconv.FormatInt(*p.Paint, 10) + "p"
		}
		parts = append(parts, "<@"+p.DiscordUserID+"> "+v)
	}
	return strings.Join(parts, " · ")
}

func reviewEmbed(f Fest, s Submission, screenshot string) *discordgo.MessageEmbed {
	winners, losers := submissionSides(s)
	e := &discordgo.MessageEmbed{
		Title: "LAN game in " + f.Title,
		Color: festColour,
		Description: teamLabel(f, s.WinningTeam) + " " + mentionList(winners) + "\nbeat\n" +
			teamLabel(f, s.LosingTeam) + " " + mentionList(losers),
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Submitted by", Value: "<@" + s.SubmittedBy + "> <t:" + strconv.FormatInt(s.CreatedAt.Unix(), 10) + ":R>", Inline: true},
			{Name: "Paint", Value: paintLine(s)},
		},
		Footer: &discordgo.MessageEmbedFooter{Text: "Submission " + s.ID},
	}
	if len(s.PossibleDuplicateOf) > 0 {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{
			Name:  "⚠️ Same players submitted shortly before",
			Value: "Submission " + strings.Join(s.PossibleDuplicateOf, ", ") + ": check this isn't the same game twice.",
		})
	}
	for _, fl := range s.Flags {
		reason := fl.Reason
		if reason == "" {
			reason = "(no reason given)"
		}
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "⚑ Disputed", Value: "<@" + fl.DiscordUserID + ">: " + reason})
	}
	switch s.Status {
	case "accepted":
		e.Color = 0x2ecc71
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "✅ Accepted", Value: "by <@" + s.ReviewedBy + ">"})
	case "refused":
		e.Color = 0xe74c3c
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "❌ Refused", Value: "by <@" + s.ReviewedBy + ">: " + s.Reason})
	}
	if screenshot != "" {
		e.Image = &discordgo.MessageEmbedImage{URL: screenshot}
	}
	return e
}

func reviewButtons(id string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Accept", Style: discordgo.SuccessButton, CustomID: "fs:accept:" + id},
		discordgo.Button{Label: "Refuse", Style: discordgo.DangerButton, CustomID: "fs:refuse:" + id},
	}}}
}

// ---- errors -----------------------------------------------------------------------

// festExplain turns a fest refusal into a sentence, or "" when it is not one.
func festExplain(err error) string {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return ""
	}
	who := mentionList(apiErr.DiscordUserIDs)
	switch apiErr.Code {
	case "players_not_registered":
		return "These players need a GRID0+ account first (`/register`): " + who
	case "players_not_entered":
		return "These players haven't joined a team in this fest yet (react on the vote post): " + who
	case "team_mismatch":
		return "Each side has to be one team, and the two sides different teams. Check where these players are listed: " + who
	case "not_a_player":
		return "Only someone who played in the game can do that."
	case "not_in_play":
		return "Games can only be submitted while the fest is being played."
	case "bad_players":
		return "Each side needs one to four players, and nobody can be listed twice."
	case "bad_paint":
		return "Paint is a number from 0 to 9999 for each player."
	case "no_such_fest":
		return "That fest doesn't exist (any more)."
	case "no_such_submission":
		return "That submission doesn't exist any more."
	case "fest_over":
		return "Play has ended in that fest."
	case "results_out":
		return "The results are already out, so this game can't count any more."
	case "bad_team", "bad_teams":
		return "A fest has three teams, Alpha, Bravo and Charlie, each with a name, its own emoji and its own role."
	case "bad_times":
		return "Check the times: the end has to be more than 24 hours after the start (halftime is at 24 hours), and not in the past. " + apiErr.Message
	case "bad_title":
		return "A fest needs a title (200 characters at most) and its announcement text (3500 at most)."
	case "not_in_game", "already_posted":
		return apiErr.Message
	case "bad_reason":
		return "A refusal needs a reason."
	}
	return ""
}

func (b *Bot) festFail(r reply, err error) {
	if msg := festExplain(err); msg != "" {
		r.text(msg)
		return
	}
	b.explain(r, err)
}
