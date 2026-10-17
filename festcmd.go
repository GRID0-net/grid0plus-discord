package main

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func festTeamOptions(n int, required bool, note string) []*discordgo.ApplicationCommandOption {
	p := fmt.Sprintf("team%d_", n)
	return []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: p + "name", Description: fmt.Sprintf("Team %d's name%s", n, note), Required: required, MaxLength: 60},
		{Type: discordgo.ApplicationCommandOptionString, Name: p + "emoji", Description: fmt.Sprintf("Team %d's emoji, for reacting", n), Required: required},
		{Type: discordgo.ApplicationCommandOptionRole, Name: p + "role", Description: fmt.Sprintf("Team %d's role, given to everyone on it", n), Required: required},
	}
}

func festPicker(desc string) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "fest", Description: desc, Autocomplete: true}
}

// festCommand is /fest. Registered only when fest.mod_role_ids is set.
func festCommand() *discordgo.ApplicationCommand {
	create := []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "The fest's name, e.g. Custom Frostyfest!", Required: true, MaxLength: 200},
	}
	create = append(create, festTeamOptions(1, true, " (Alpha; first in the BCAT pack for an in-game fest)")...)
	create = append(create, festTeamOptions(2, true, " (Bravo)")...)
	create = append(create, festTeamOptions(3, true, " (Charlie)")...)
	create = append(create,
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "start", Description: "When play opens: a Discord timestamp, or YYYY-MM-DD HH:MM in UTC (LAN-only fests)"},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "end", Description: "When play ends, at least 24 h after the start (default: 48 h)"},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: "game_fest", Description: "The GRID0+ fest id (e.g. JUEA-90003) to tie this to; empty for a LAN-only fest"},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionChannel, Name: "channel", Description: "Where to post it (default: the fest channel)",
			ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "win_paint", Description: "Paint for a LAN winner who gave none, until online games give an average", MinValue: ptr(0.0), MaxValue: 9999},
		&discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionInteger, Name: "loss_paint", Description: "Same, for a LAN loser", MinValue: ptr(0.0), MaxValue: 9999},
	)

	user := func(name, desc string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: name, Description: desc, Required: required}
	}
	submit := []*discordgo.ApplicationCommandOption{
		user("winner1", "A player on the winning team", true),
		user("loser1", "A player on the losing team", true),
		user("winner2", "Another winner", false), user("winner3", "Another winner", false), user("winner4", "Another winner", false),
		user("loser2", "Another loser", false), user("loser3", "Another loser", false), user("loser4", "Another loser", false),
		{Type: discordgo.ApplicationCommandOptionString, Name: "paint", Description: "Each player's paint, winners then losers, e.g. 1203 980 - 850 (- if unknown)"},
		{Type: discordgo.ApplicationCommandOptionAttachment, Name: "screenshot", Description: "A picture of the results screen"},
		festPicker("Which fest (default: the one being played)"),
	}
	return &discordgo.ApplicationCommand{
		Name: "fest", Description: "Splatfests: vote posts, LAN games, your record",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "create", Description: "Mods: post a fest vote (asks for the text next)", Options: create},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "submit", Description: "Report a LAN game you played, for a mod to check", Options: submit},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "me", Description: "Your team, games and contribution in a fest",
				Options: []*discordgo.ApplicationCommandOption{festPicker("Which fest (default: the latest)")}},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "standings", Description: "What the game shows about a fest right now",
				Options: []*discordgo.ApplicationCommandOption{festPicker("Which fest (default: the latest)")}},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "cancel", Description: "Mods: remove a fest's vote post (a LAN-only fest loses its votes and games)",
				Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "fest", Description: "Which fest", Required: true, Autocomplete: true}}},
		},
	}
}

func ptr[T any](v T) *T { return &v }

// subcommand returns /fest's subcommand and its options by name.
func subcommand(data discordgo.ApplicationCommandInteractionData) (string, map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	opts := map[string]*discordgo.ApplicationCommandInteractionDataOption{}
	if len(data.Options) == 0 {
		return "", opts
	}
	for _, o := range data.Options[0].Options {
		opts[o.Name] = o
	}
	return data.Options[0].Name, opts
}

func optString(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if o, ok := opts[name]; ok {
		if s, ok := o.Value.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func optInt(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) int64 {
	if o, ok := opts[name]; ok {
		return o.IntValue()
	}
	return 0
}

// onFestCommand answers /fest. create opens a form first, so it is handled
// before the deferral every other subcommand gets.
func (b *Bot) onFestCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	sub, opts := subcommand(i.ApplicationCommandData())
	if sub == "create" {
		b.festCreateForm(s, i, opts)
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		b.log.Warn("defer interaction", "command", "fest "+sub, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := reply{s, i}
	b.log.Info("command", "name", "fest "+sub, "user", i.Member.User.ID)
	switch sub {
	case "submit":
		b.festSubmit(ctx, r, opts)
	case "me":
		b.festMe(ctx, r, opts)
	case "standings":
		b.festStandings(ctx, r, opts)
	case "cancel":
		b.festCancelPrompt(ctx, r, opts)
	}
}

// pickFest resolves the fest option, or the obvious default: the one being
// played (for submit), or the latest.
func (b *Bot) pickFest(ctx context.Context, opts map[string]*discordgo.ApplicationCommandInteractionDataOption, inPlay bool) (Fest, string) {
	if err := b.refreshFests(ctx); err != nil {
		b.log.Warn("refresh fests", "error", err)
	}
	if id := optString(opts, "fest"); id != "" {
		if f, ok := b.cachedFest(strings.ToUpper(id)); ok {
			return f, ""
		}
		return Fest{}, "I don't know a fest called `" + id + "`."
	}
	fests := b.cachedFests()
	now := time.Now()
	var candidates []Fest
	for _, f := range fests {
		if !inPlay || (!now.Before(f.Timetable.Start) && now.Before(f.Timetable.End)) {
			candidates = append(candidates, f)
		}
	}
	switch {
	case len(candidates) == 0 && inPlay:
		return Fest{}, "No fest is being played right now."
	case len(candidates) == 0:
		return Fest{}, "There are no fests yet."
	case inPlay && len(candidates) > 1:
		return Fest{}, "More than one fest is being played: pick one with the `fest` option."
	}
	return candidates[len(candidates)-1], ""
}

// ---- /fest create ---------------------------------------------------------------

func (b *Bot) festCreateForm(s *discordgo.Session, i *discordgo.InteractionCreate, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	refuse := func(msg string) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
		})
	}
	if !b.isMod(i.Member) {
		refuse("Only mods can create a fest.")
		return
	}
	in := CreateFest{
		FestID: strings.ToUpper(optString(opts, "game_fest")), Title: optString(opts, "title"),
		DefaultWinPaint: optInt(opts, "win_paint"), DefaultLossPaint: optInt(opts, "loss_paint"),
	}
	for n := 1; n <= 3; n++ {
		p := fmt.Sprintf("team%d_", n)
		name, emoji, role := optString(opts, p+"name"), optString(opts, p+"emoji"), optString(opts, p+"role")
		if name == "" || emoji == "" || role == "" {
			refuse(fmt.Sprintf("Team %d needs a name, an emoji and a role.", n))
			return
		}
		in.Teams = append(in.Teams, FestTeam{Name: name, Emoji: emoji, RoleID: role})
	}
	if in.FestID == "" {
		start, end := optString(opts, "start"), optString(opts, "end")
		if start == "" {
			refuse("A LAN-only fest needs a `start` (or tie it to an in-game fest with `game_fest`).")
			return
		}
		var err error
		if in.StartAt, err = parseFestTime(start); err != nil {
			refuse(err.Error())
			return
		}
		if end != "" {
			if in.EndAt, err = parseFestTime(end); err != nil {
				refuse(err.Error())
				return
			}
		}
	}
	channel := optString(opts, "channel")
	if channel == "" {
		channel = b.cfg.Fest.ChannelID
	}
	if channel == "" {
		channel = i.ChannelID
	}
	b.fest.mu.Lock()
	now := time.Now()
	for k, p := range b.fest.creating {
		if now.After(p.expires) {
			delete(b.fest.creating, k)
		}
	}
	b.fest.creating[i.ID] = pendingFestCreate{owner: i.Member.User.ID, in: in, channel: channel, expires: now.Add(15 * time.Minute)}
	b.fest.mu.Unlock()

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "festcreate:" + i.ID,
			Title:    "The fest's announcement",
			Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.TextInput{CustomID: "description", Label: "The announcement text",
					Style: discordgo.TextInputParagraph, Required: true, MaxLength: 3500,
					Placeholder: "What would you rather make for the holidays?"},
			}}},
		},
	})
	if err != nil {
		b.log.Warn("show fest form", "error", err)
	}
}

func modalValues(data discordgo.ModalSubmitInteractionData) map[string]string {
	values := map[string]string{}
	for _, row := range data.Components {
		if ar, ok := row.(*discordgo.ActionsRow); ok {
			for _, c := range ar.Components {
				if ti, ok := c.(*discordgo.TextInput); ok {
					values[ti.CustomID] = strings.TrimSpace(ti.Value)
				}
			}
		}
	}
	return values
}

// onFestModal handles the fest forms: the announcement text, a refusal's
// reason and a dispute's.
func (b *Bot) onFestModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	kind, id, _ := strings.Cut(data.CustomID, ":")
	values := modalValues(data)
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := reply{s, i}
	switch kind {
	case "festcreate":
		b.festCreate(ctx, r, id, values["description"])
	case "festrefuse":
		b.festRefuse(ctx, r, id, values["reason"])
	case "festdispute":
		b.festDispute(ctx, r, id, values["reason"])
	}
}

func (b *Bot) festCreate(ctx context.Context, r reply, pendingID, description string) {
	b.fest.mu.Lock()
	p, ok := b.fest.creating[pendingID]
	delete(b.fest.creating, pendingID)
	b.fest.mu.Unlock()
	if !ok || p.owner != r.i.Member.User.ID {
		r.text("That form expired. Run `/fest create` again.")
		return
	}
	p.in.Description, p.in.ChannelID = description, p.channel
	f, err := b.api.CreateFest(ctx, r.i.Member.User.ID, p.in)
	if err != nil {
		b.festFail(r, err)
		return
	}
	msg, err := r.s.ChannelMessageSendComplex(p.channel, &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{festPostEmbed(f)}})
	if err != nil {
		b.log.Error("post fest vote", "fest", f.FestID, "error", err)
		_ = b.api.DeleteFest(ctx, f.FestID)
		r.text("I couldn't post in <#" + p.channel + ">: check that I can send messages and embeds there.")
		return
	}
	if err := b.api.SetFestPost(ctx, f.FestID, p.channel, msg.ID); err != nil {
		b.log.Error("record fest vote post", "fest", f.FestID, "error", err)
	}
	var missing []string
	for _, t := range f.Teams {
		if err := r.s.MessageReactionAdd(p.channel, msg.ID, reactionName(t.Emoji)); err != nil {
			b.log.Warn("add team reaction", "fest", f.FestID, "emoji", t.Emoji, "error", err)
			missing = append(missing, t.Emoji)
		}
	}
	if _, err := b.api.ClaimFestPost(ctx, f.FestID, "announced"); err != nil {
		b.log.Warn("claim fest announcement", "fest", f.FestID, "error", err)
	}
	if err := b.refreshFests(ctx); err != nil {
		b.log.Warn("refresh fests", "error", err)
	}
	b.log.Info("fest created", "fest", f.FestID, "discord_only", f.DiscordOnly, "by", r.i.Member.User.ID)
	text := "Posted **" + f.Title + "** (`" + f.FestID + "`) in <#" + p.channel + ">."
	if f.DiscordOnly && !f.Timetable.Start.Equal(p.in.StartAt) {
		text += fmt.Sprintf(" Play opens <t:%d:F>: fests start on a two-hour boundary, as in game.", f.Timetable.Start.Unix())
	}
	if len(missing) > 0 {
		text += " I couldn't react with " + strings.Join(missing, " ") + ": if it's another server's emoji I can't use it, so pick one from this server and recreate the fest."
	}
	r.text(text)
}

// ---- /fest cancel -------------------------------------------------------------------

func (b *Bot) festCancelPrompt(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	if !b.isMod(r.i.Member) {
		r.text("Only mods can cancel a fest.")
		return
	}
	f, problem := b.pickFest(ctx, opts, false)
	if problem != "" {
		r.text(problem)
		return
	}
	what := "This removes the vote post's link to **" + f.Title + "**. Votes and games stay with the in-game fest."
	if f.DiscordOnly {
		what = "This deletes **" + f.Title + "** with **every vote and LAN game in it**. It can't be undone."
	}
	r.edit(&discordgo.WebhookEdit{
		Content: &what,
		Components: &[]discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Cancel the fest", Style: discordgo.DangerButton, CustomID: "fx:delete:" + f.FestID},
		}}},
	})
}

func (b *Bot) festCancel(s *discordgo.Session, i *discordgo.InteractionCreate, festID string) {
	respond := func(msg string) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{Content: msg, Components: []discordgo.MessageComponent{}},
		})
	}
	if !b.isMod(i.Member) {
		respond("Only mods can cancel a fest.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, known := b.cachedFest(festID)
	if err := b.api.DeleteFest(ctx, festID); err != nil {
		if msg := festExplain(err); msg != "" {
			respond(msg)
			return
		}
		b.log.Error("cancel fest", "fest", festID, "error", err)
		respond("Something went wrong talking to GRID0+.")
		return
	}
	if known && f.ChannelID != "" && f.MessageID != "" {
		_ = s.ChannelMessageDelete(f.ChannelID, f.MessageID)
	}
	_ = b.refreshFests(ctx)
	b.log.Info("fest cancelled", "fest", festID, "by", i.Member.User.ID)
	respond("Cancelled `" + festID + "`.")
}

// ---- /fest me and /fest standings ---------------------------------------------------

func (b *Bot) festMe(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	f, problem := b.pickFest(ctx, opts, false)
	if problem != "" {
		r.text(problem)
		return
	}
	rec, err := b.api.FestRecord(ctx, r.i.Member.User.ID, f.FestID)
	if err != nil {
		b.festFail(r, err)
		return
	}
	e := &discordgo.MessageEmbed{Title: "You in " + f.Title, Color: festColour}
	if rec.Team == "" {
		e.Description = "You haven't joined a team: react on the vote post"
		if !f.DiscordOnly {
			e.Description += " or pick one in Splatoon 3"
		}
		e.Description += "."
	} else {
		e.Description = "Team " + teamLabel(f, rec.Team)
	}
	tally := func(name string, t FestTally) {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: name, Inline: true,
			Value: fmt.Sprintf("%d games, %d wins\n%d contribution", t.Games, t.Wins, t.Contribution)})
	}
	if !f.DiscordOnly {
		tally("Online", rec.Online)
	}
	tally("LAN", rec.LAN)
	var subs []string
	for _, s := range rec.Submissions {
		line := map[string]string{"pending": "⏳", "accepted": "✅", "refused": "❌"}[s.Status] + " " +
			teamLabel(f, s.WinningTeam) + " beat " + teamLabel(f, s.LosingTeam) +
			" <t:" + fmt.Sprint(s.CreatedAt.Unix()) + ":R>"
		if s.Status == "refused" && s.Reason != "" {
			line += ": " + s.Reason
		}
		subs = append(subs, line)
	}
	if len(subs) > 10 {
		subs = subs[len(subs)-10:]
	}
	if len(subs) > 0 {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Your LAN submissions", Value: strings.Join(subs, "\n")})
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{e}})
}

func (b *Bot) festStandings(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	f, problem := b.pickFest(ctx, opts, false)
	if problem != "" {
		r.text(problem)
		return
	}
	st, err := b.api.FestStandings(ctx, f.FestID)
	if err != nil {
		b.festFail(r, err)
		return
	}
	switch {
	case st.Final != nil:
		r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{resultsEmbed(f, st)}})
	case st.Midterm != nil:
		r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{halftimeEmbed(f, st)}})
	case st.Phase == "counting":
		r.text(fmt.Sprintf("Play is over and the votes are being counted: results <t:%d:R>.", f.Timetable.Close.Unix()))
	default:
		r.text(fmt.Sprintf("Nothing to show yet, just like in game: the halftime report comes <t:%d:R>.", f.Timetable.Mid.Unix()))
	}
}

// ---- /fest submit -------------------------------------------------------------------

const maxScreenshot = 10 << 20

func (b *Bot) festSubmit(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	f, problem := b.pickFest(ctx, opts, true)
	if problem != "" {
		r.text(problem)
		return
	}
	var winners, losers []string
	for n := 1; n <= 4; n++ {
		if id := optString(opts, fmt.Sprintf("winner%d", n)); id != "" {
			winners = append(winners, id)
		}
	}
	for n := 1; n <= 4; n++ {
		if id := optString(opts, fmt.Sprintf("loser%d", n)); id != "" {
			losers = append(losers, id)
		}
	}
	in := SubmitGame{Winners: winners, Losers: losers}
	if p := optString(opts, "paint"); p != "" {
		paint, err := parsePaint(p, append(append([]string{}, winners...), losers...))
		if err != nil {
			r.text("I couldn't read the paint: " + err.Error() + ". Give one number per player, winners first, `-` for one you don't know.")
			return
		}
		in.Paint = paint
	}
	// The screenshot is downloaded and attached to the review post itself:
	// a Discord attachment link from a command expires, and the mods need
	// to see it whenever they get to it.
	var shot *discordgo.File
	if o, ok := opts["screenshot"]; ok {
		data := r.i.ApplicationCommandData()
		if data.Resolved != nil {
			if att := data.Resolved.Attachments[fmt.Sprint(o.Value)]; att != nil {
				if !strings.HasPrefix(att.ContentType, "image/") || att.Size > maxScreenshot {
					r.text("The screenshot has to be a picture of 10 MB or less.")
					return
				}
				raw, err := fetchAttachment(ctx, att.URL, maxScreenshot)
				if err != nil {
					b.log.Warn("download screenshot", "error", err)
					r.text("I couldn't download the screenshot from Discord. Try again.")
					return
				}
				shot = &discordgo.File{Name: "results" + path.Ext(att.Filename), ContentType: att.ContentType, Reader: bytes.NewReader(raw)}
			}
		}
	}
	sub, err := b.api.SubmitGame(ctx, r.i.Member.User.ID, f.FestID, in)
	if err != nil {
		b.festFail(r, err)
		return
	}
	review := &discordgo.MessageSend{Components: reviewButtons(sub.ID), AllowedMentions: &discordgo.MessageAllowedMentions{}}
	image := ""
	if shot != nil {
		review.Files = []*discordgo.File{shot}
		image = "attachment://" + shot.Name
	}
	review.Embeds = []*discordgo.MessageEmbed{reviewEmbed(f, sub, image)}
	msg, err := r.s.ChannelMessageSendComplex(b.cfg.Fest.ReviewChannelID, review)
	if err != nil {
		b.log.Error("post LAN game for review", "submission", sub.ID, "error", err)
	} else if err := b.api.SetSubmissionMessage(ctx, sub.ID, msg.ChannelID, msg.ID); err != nil {
		b.log.Warn("record review post", "submission", sub.ID, "error", err)
	}

	// The other players hear about it in the channel, and can dispute it
	// before a mod rules.
	var others []string
	for _, p := range sub.Players {
		if p.DiscordUserID != sub.SubmittedBy {
			others = append(others, p.DiscordUserID)
		}
	}
	w, l := submissionSides(sub)
	notice := "<@" + sub.SubmittedBy + "> reported a LAN game in **" + f.Title + "**: " +
		teamLabel(f, sub.WinningTeam) + " " + mentionList(w) + " beat " + teamLabel(f, sub.LosingTeam) + " " + mentionList(l) +
		".\nIf that's not what happened, press **Dispute** before a mod reviews it."
	if _, err := r.s.ChannelMessageSendComplex(r.i.ChannelID, &discordgo.MessageSend{
		Content:         notice,
		AllowedMentions: &discordgo.MessageAllowedMentions{Users: others},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Dispute", Style: discordgo.SecondaryButton, CustomID: "fs:dispute:" + sub.ID},
		}}},
	}); err != nil {
		b.log.Warn("post LAN game notice", "submission", sub.ID, "error", err)
	}
	b.log.Info("LAN game submitted", "fest", f.FestID, "submission", sub.ID, "by", sub.SubmittedBy)
	r.text("Submitted. A mod will check it; I'll message you when they do.")
}

// ---- review buttons -------------------------------------------------------------

// onFestButton handles the fest buttons: fs:accept/refuse/dispute:<submission>
// and fx:delete:<fest>.
func (b *Bot) onFestButton(s *discordgo.Session, i *discordgo.InteractionCreate, kind, action, id string) {
	if kind == "fx" {
		if action == "delete" {
			b.festCancel(s, i, id)
		}
		return
	}
	ephemeral := func(msg string) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
		})
	}
	reasonForm := func(customID, title, label string, required bool) {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: customID, Title: title,
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{CustomID: "reason", Label: label, Style: discordgo.TextInputParagraph, Required: required, MaxLength: 500},
				}}},
			},
		})
		if err != nil {
			b.log.Warn("show reason form", "error", err)
		}
	}
	switch action {
	case "accept":
		if !b.isMod(i.Member) {
			ephemeral("Only mods can review LAN games.")
			return
		}
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		}); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		b.festAccept(ctx, reply{s, i}, id)
	case "refuse":
		if !b.isMod(i.Member) {
			ephemeral("Only mods can review LAN games.")
			return
		}
		reasonForm("festrefuse:"+id, "Refuse this game", "Why? The player is told this", true)
	case "dispute":
		reasonForm("festdispute:"+id, "Dispute this game", "What's wrong with it? Mods see this", false)
	}
}

// settled answers a review of a submission someone else already settled.
func (b *Bot) settled(ctx context.Context, r reply, id string, err error) bool {
	if !IsCode(err, "already_reviewed") {
		return false
	}
	s, gerr := b.api.Submission(ctx, id)
	if gerr != nil {
		r.text("Someone already reviewed that game.")
		return true
	}
	r.text("That game was already " + s.Status + " by <@" + s.ReviewedBy + ">.")
	b.updateReviewPost(ctx, r.s, s)
	return true
}

func (b *Bot) festAccept(ctx context.Context, r reply, id string) {
	s, err := b.api.AcceptSubmission(ctx, r.i.Member.User.ID, id)
	if err != nil {
		if !b.settled(ctx, r, id, err) {
			b.festFail(r, err)
		}
		return
	}
	b.updateReviewPost(ctx, r.s, s)
	f, _ := b.cachedFest(s.FestID)
	b.dm(r.s, s.SubmittedBy, "✅ Your LAN game in **"+f.Title+"** ("+teamLabel(f, s.WinningTeam)+" beat "+
		teamLabel(f, s.LosingTeam)+") was accepted and now counts.")
	b.log.Info("LAN game accepted", "submission", id, "by", r.i.Member.User.ID)
	r.text("Accepted: it counts now.")
}

func (b *Bot) festRefuse(ctx context.Context, r reply, id, reason string) {
	if !b.isMod(r.i.Member) {
		r.text("Only mods can review LAN games.")
		return
	}
	s, err := b.api.RefuseSubmission(ctx, r.i.Member.User.ID, id, reason)
	if err != nil {
		if !b.settled(ctx, r, id, err) {
			b.festFail(r, err)
		}
		return
	}
	b.updateReviewPost(ctx, r.s, s)
	f, _ := b.cachedFest(s.FestID)
	b.dm(r.s, s.SubmittedBy, "❌ Your LAN game in **"+f.Title+"** ("+teamLabel(f, s.WinningTeam)+" beat "+
		teamLabel(f, s.LosingTeam)+") was refused by a mod: "+reason)
	b.log.Info("LAN game refused", "submission", id, "by", r.i.Member.User.ID)
	r.text("Refused. I've told the player why.")
}

func (b *Bot) festDispute(ctx context.Context, r reply, id, reason string) {
	s, err := b.api.FlagSubmission(ctx, r.i.Member.User.ID, id, reason)
	if err != nil {
		if IsCode(err, "already_reviewed") {
			r.text("A mod has already reviewed that game. If it's wrong, ask a mod.")
			return
		}
		b.festFail(r, err)
		return
	}
	b.updateReviewPost(ctx, r.s, s)
	r.text("Thanks: the mods will see your dispute when they review it.")
}

// updateReviewPost redraws the mods' post for a submission, keeping its
// screenshot, and takes the buttons away once it is settled.
func (b *Bot) updateReviewPost(ctx context.Context, s *discordgo.Session, sub Submission) {
	if sub.ReviewChannelID == "" || sub.ReviewMessageID == "" {
		return
	}
	f, ok := b.cachedFest(sub.FestID)
	if !ok {
		if fresh, err := b.api.Fest(ctx, sub.FestID); err == nil {
			f = fresh
		}
	}
	image := ""
	if old, err := s.ChannelMessage(sub.ReviewChannelID, sub.ReviewMessageID); err == nil &&
		len(old.Embeds) > 0 && old.Embeds[0].Image != nil {
		image = old.Embeds[0].Image.URL
		if len(old.Attachments) > 0 {
			image = "attachment://" + old.Attachments[0].Filename
		}
	}
	components := reviewButtons(sub.ID)
	if sub.Status != "pending" {
		components = []discordgo.MessageComponent{}
	}
	embeds := []*discordgo.MessageEmbed{reviewEmbed(f, sub, image)}
	if _, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID: sub.ReviewMessageID, Channel: sub.ReviewChannelID, Embeds: &embeds, Components: &components,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}); err != nil {
		b.log.Warn("update review post", "submission", sub.ID, "error", err)
	}
}

// dm sends a private message, quietly giving up when the person does not
// accept them.
func (b *Bot) dm(s *discordgo.Session, userID, text string) {
	ch, err := s.UserChannelCreate(userID)
	if err == nil {
		_, err = s.ChannelMessageSend(ch.ID, text)
	}
	if err != nil {
		b.log.Info("could not DM", "user", userID, "error", err)
	}
}

// ---- autocomplete -------------------------------------------------------------------

func (b *Bot) festAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, opts := subcommand(i.ApplicationCommandData())
	typed := strings.ToLower(optString(opts, "fest"))
	if len(b.cachedFests()) == 0 {
		_ = b.refreshFests(ctx)
	}
	now := time.Now()
	var choices []*discordgo.ApplicationCommandOptionChoice
	fests := b.cachedFests()
	for k := len(fests) - 1; k >= 0 && len(choices) < maxChoices; k-- {
		f := fests[k]
		if sub == "submit" && (now.Before(f.Timetable.Start) || f.over(now)) {
			continue
		}
		name := f.Title + " (" + f.FestID + ")"
		if r := []rune(name); len(r) > 100 {
			name = string(r[:100])
		}
		if typed == "" || strings.Contains(strings.ToLower(name), typed) {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: name, Value: f.FestID})
		}
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
}
