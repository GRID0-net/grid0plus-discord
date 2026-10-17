package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Salmon Run over LAN. GRID0+ keeps the rank -- the same one the game shows
// online -- and decides the hazard level; the bot only asks, shows and
// relays a mod's decision.

// ---- API ----------------------------------------------------------------------

type SalmonPlayer struct {
	DiscordUserID string `json:"discordUserId"`
	Title         string `json:"title"`
	Points        int64  `json:"points"`
	TitleAfter    string `json:"titleAfter"`
	PointsAfter   *int64 `json:"pointsAfter"`
}

type SalmonResult struct {
	ReportedBy string `json:"reportedBy"`
	Cleared    bool   `json:"cleared"`
	Wave       int64  `json:"wave"`
	Disconnect bool   `json:"disconnect"`
}

type SalmonDecision struct {
	By     string `json:"by"`
	Reason string `json:"reason"`
}

type SalmonJob struct {
	ID              string          `json:"id"`
	RequestedBy     string          `json:"requestedBy"`
	Stage           string          `json:"stage"`
	Hazard          int64           `json:"hazard"`
	Played          int64           `json:"played"`
	Modded          bool            `json:"modded"`
	HalfWin         bool            `json:"halfWin"`
	Players         []SalmonPlayer  `json:"players"`
	Status          string          `json:"status"`
	Result          *SalmonResult   `json:"result"`
	Review          *SalmonDecision `json:"review"`
	Delta           *int64          `json:"delta"`
	ReviewChannelID string          `json:"reviewChannelId"`
	ReviewMessageID string          `json:"reviewMessageId"`
}

type SalmonBadge struct {
	Stage string `json:"stage"`
	Best  int64  `json:"best"`
	Badge string `json:"badge"`
}

type SalmonRank struct {
	Title  string        `json:"title"`
	Points int64         `json:"points"`
	Stage  string        `json:"stage"`
	Badges []SalmonBadge `json:"badges"`
}

func (c *SwitchNet) RequestSalmonJob(ctx context.Context, discordID string, players []string, modded bool) (SalmonJob, error) {
	var out SalmonJob
	err := c.doJSON(ctx, "POST", "/bot/v1/salmon/jobs", discordID,
		map[string]any{"players": players, "modded": modded}, &out)
	return out, err
}

func (c *SwitchNet) SalmonJob(ctx context.Context, id string) (SalmonJob, error) {
	var out SalmonJob
	err := c.doJSON(ctx, "GET", "/bot/v1/salmon/jobs/"+id, "", nil, &out)
	return out, err
}

func (c *SwitchNet) ReportSalmonJob(ctx context.Context, discordID, id string, cleared bool, wave int64, disconnect bool) (SalmonJob, error) {
	var out SalmonJob
	err := c.doJSON(ctx, "POST", "/bot/v1/salmon/jobs/"+id+"/report", discordID,
		map[string]any{"cleared": cleared, "wave": wave, "disconnect": disconnect}, &out)
	return out, err
}

func (c *SwitchNet) SetSalmonJobMessage(ctx context.Context, id, channelID, messageID string) error {
	return c.doJSON(ctx, "PUT", "/bot/v1/salmon/jobs/"+id+"/message", "",
		map[string]string{"channelId": channelID, "messageId": messageID}, nil)
}

func (c *SwitchNet) AcceptSalmonJob(ctx context.Context, modID, id string) (SalmonJob, error) {
	var out SalmonJob
	err := c.doJSON(ctx, "POST", "/bot/v1/salmon/jobs/"+id+"/accept", modID, nil, &out)
	return out, err
}

func (c *SwitchNet) RefuseSalmonJob(ctx context.Context, modID, id, reason string) (SalmonJob, error) {
	var out SalmonJob
	err := c.doJSON(ctx, "POST", "/bot/v1/salmon/jobs/"+id+"/refuse", modID, map[string]string{"reason": reason}, &out)
	return out, err
}

func (c *SwitchNet) SalmonRank(ctx context.Context, discordID string) (SalmonRank, error) {
	var out SalmonRank
	err := c.doJSON(ctx, "GET", "/bot/v1/salmon/rank/"+discordID, "", nil, &out)
	return out, err
}

// ---- commands -------------------------------------------------------------------

func salmonCommand() *discordgo.ApplicationCommand {
	teammate := func(n int, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser,
			Name: fmt.Sprintf("teammate%d", n), Description: "Someone playing with you", Required: required}
	}
	return &discordgo.ApplicationCommand{
		Name: "salmon", Description: "Salmon Run over LAN: jobs that count toward your in-game rank",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "request", Description: "Set up a LAN job: get the stage and hazard level to play",
				Options: []*discordgo.ApplicationCommandOption{
					teammate(1, true), teammate(2, false), teammate(3, false),
					{Type: discordgo.ApplicationCommandOptionBoolean, Name: "modded",
						Description: "The host has the IPS patches that allow going above 200% in the difficulty (default: no)"},
				}},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "report", Description: "Report how a job went, for a mod to check",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionString, Name: "job", Description: "The job id from /salmon request", Required: true},
					{Type: discordgo.ApplicationCommandOptionString, Name: "result", Description: "How it ended", Required: true,
						Choices: []*discordgo.ApplicationCommandOptionChoice{
							{Name: "Cleared", Value: "clear"}, {Name: "Lost in wave 1", Value: "1"},
							{Name: "Lost in wave 2", Value: "2"}, {Name: "Lost in wave 3", Value: "3"},
						}},
					{Type: discordgo.ApplicationCommandOptionBoolean, Name: "disconnect",
						Description: "A teammate disconnected: a lost job costs half (default: no)"},
				}},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "rank", Description: "A Salmon Run rank and stage badges",
				Options: []*discordgo.ApplicationCommandOption{
					{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "Whose (default: yours)"},
				}},
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "help", Description: "What the /salmon commands do"},
		},
	}
}

func optUser(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) string {
	if o, ok := opts[name]; ok {
		if s, ok := o.Value.(string); ok {
			return s
		}
	}
	return ""
}

func optBool(opts map[string]*discordgo.ApplicationCommandInteractionDataOption, name string) bool {
	if o, ok := opts[name]; ok {
		return o.BoolValue()
	}
	return false
}

func (b *Bot) onSalmonCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	sub, opts := subcommand(i.ApplicationCommandData())
	// A request is posted for everyone in the job to see; the rest is private.
	var flags discordgo.MessageFlags
	if sub != "request" {
		flags = discordgo.MessageFlagsEphemeral
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: flags},
	}); err != nil {
		b.log.Warn("defer interaction", "command", "salmon "+sub, "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := reply{s, i}
	b.log.Info("command", "name", "salmon "+sub, "user", i.Member.User.ID)
	switch sub {
	case "request":
		b.salmonRequest(ctx, r, opts)
	case "report":
		b.salmonReport(ctx, r, opts)
	case "rank":
		b.salmonRank(ctx, r, opts)
	case "help":
		r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{b.helpEmbed("salmon", i.Member)}})
	}
}

func (b *Bot) salmonRequest(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	me := r.i.Member.User.ID
	players := []string{me}
	for _, name := range []string{"teammate1", "teammate2", "teammate3"} {
		if id := optUser(opts, name); id != "" {
			players = append(players, id)
		}
	}
	job, err := b.api.RequestSalmonJob(ctx, me, players, optBool(opts, "modded"))
	if err != nil {
		b.salmonFail(r, err)
		return
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{salmonJobEmbed(job)}})
}

func (b *Bot) salmonReport(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	me := r.i.Member.User.ID
	id := strings.ToLower(optString(opts, "job"))
	result := optString(opts, "result")
	cleared, wave := result == "clear", int64(0)
	if !cleared {
		wave = int64(result[0] - '0')
	}
	job, err := b.api.ReportSalmonJob(ctx, me, id, cleared, wave, optBool(opts, "disconnect"))
	if err != nil {
		b.salmonFail(r, err)
		return
	}
	review := &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{salmonJobEmbed(job)},
		Components: salmonReviewButtons(job.ID), AllowedMentions: &discordgo.MessageAllowedMentions{}}
	msg, err := r.s.ChannelMessageSendComplex(b.cfg.Fest.ReviewChannelID, review)
	if err != nil {
		b.log.Error("post salmon review", "job", job.ID, "error", err)
	} else if err := b.api.SetSalmonJobMessage(ctx, job.ID, msg.ChannelID, msg.ID); err != nil {
		b.log.Warn("record salmon review post", "job", job.ID, "error", err)
	}
	r.text("Reported. A mod will check it; everyone's rank moves once it's accepted.")
}

func (b *Bot) salmonRank(ctx context.Context, r reply, opts map[string]*discordgo.ApplicationCommandInteractionDataOption) {
	who := optUser(opts, "user")
	if who == "" {
		who = r.i.Member.User.ID
	}
	rank, err := b.api.SalmonRank(ctx, who)
	if err != nil {
		b.salmonFail(r, err)
		return
	}
	e := &discordgo.MessageEmbed{Title: "Salmon Run rank", Color: 0xff6a00,
		Description: fmt.Sprintf("<@%s>: **%s %d**", who, rank.Title, rank.Points)}
	if rank.Stage != "" {
		e.Description += "\nThis rotation: " + rank.Stage
	}
	var badges []string
	for _, bd := range rank.Badges {
		if bd.Badge != "" {
			badges = append(badges, fmt.Sprintf("%s: %s (best %d)", bd.Stage, bd.Badge, bd.Best))
		}
	}
	if len(badges) > 0 {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Eggsecutive VP badges", Value: strings.Join(badges, "\n")})
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{e}})
}

func salmonJobEmbed(j SalmonJob) *discordgo.MessageEmbed {
	var players []string
	for _, p := range j.Players {
		line := fmt.Sprintf("<@%s> %s %d", p.DiscordUserID, p.Title, p.Points)
		if p.PointsAfter != nil {
			line += fmt.Sprintf(" → %s %d", p.TitleAfter, *p.PointsAfter)
		}
		players = append(players, line)
	}
	e := &discordgo.MessageEmbed{
		Title: "Salmon Run job", Color: 0xff6a00,
		Description: fmt.Sprintf("Stage: **%s**\nHazard level: **%d%%**\n"+
			"Leave every weapon on **random**: don't choose one.", j.Stage, j.Played),
		Fields: []*discordgo.MessageEmbedField{
			// Its own field, in a code block: it is what /salmon report asks
			// for, and a code block is easy to read and copy on a phone.
			{Name: "Job ID", Value: "```" + j.ID + "```"},
			{Name: "Players", Value: strings.Join(players, "\n")},
		},
	}
	if j.HalfWin {
		e.Description += fmt.Sprintf("\n⚠️ Your ranks call for **%d%%**, above what the game allows without the IPS patches, "+
			"so play at **200%%**: a clear is worth +10 instead of +20. A host with the patches can request again with `modded:True`.", j.Hazard)
	}
	if j.Status == "requested" {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "When you're done",
			Value: "`/salmon report job:" + j.ID + " result:...`"})
	}
	if res := j.Result; res != nil {
		how := "Cleared"
		if !res.Cleared {
			how = fmt.Sprintf("Lost in wave %d", res.Wave)
			if res.Disconnect {
				how += ", a teammate disconnected"
			}
		}
		if j.Delta != nil {
			how += fmt.Sprintf(" (%+d)", *j.Delta)
		}
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Result", Value: how + " — reported by <@" + res.ReportedBy + ">"})
	}
	switch {
	case j.Status == "accepted" && j.Review != nil:
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "✅ Accepted", Value: "by <@" + j.Review.By + ">"})
	case j.Status == "refused" && j.Review != nil:
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "❌ Refused", Value: "by <@" + j.Review.By + ">: " + j.Review.Reason})
	}
	return e
}

func salmonReviewButtons(id string) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Accept", Style: discordgo.SuccessButton, CustomID: "sj:accept:" + id},
		discordgo.Button{Label: "Refuse", Style: discordgo.DangerButton, CustomID: "sj:refuse:" + id},
	}}}
}

// onSalmonButton is a mod's Accept or Refuse. The mod role is checked here;
// GRID0+ trusts the bot for it, as with fest reviews.
func (b *Bot) onSalmonButton(s *discordgo.Session, i *discordgo.InteractionCreate, action, id string) {
	if !b.isMod(i.Member) {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "Only mods can review Salmon Run jobs.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	if action == "refuse" {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "salmonrefuse:" + id, Title: "Refuse this job",
				Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.TextInput{CustomID: "reason", Label: "Why? The players are told this", Style: discordgo.TextInputParagraph, Required: true, MaxLength: 500},
				}}},
			},
		})
		if err != nil {
			b.log.Warn("show reason form", "error", err)
		}
		return
	}
	if action != "accept" {
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
	r := reply{s, i}
	job, err := b.api.AcceptSalmonJob(ctx, i.Member.User.ID, id)
	if err != nil {
		b.salmonFail(r, err)
		return
	}
	b.updateSalmonReview(s, job)
	for _, p := range job.Players {
		if p.PointsAfter != nil {
			b.dm(s, p.DiscordUserID, fmt.Sprintf("✅ Salmon Run job %s was accepted: you're now **%s %d**.", job.ID, p.TitleAfter, *p.PointsAfter))
		}
	}
	b.log.Info("salmon job accepted", "job", id, "by", i.Member.User.ID)
	r.text("Accepted: everyone's rank moved.")
}

func (b *Bot) onSalmonModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	id := strings.TrimPrefix(data.CustomID, "salmonrefuse:")
	reason := modalValues(data)["reason"]
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		return
	}
	r := reply{s, i}
	if !b.isMod(i.Member) {
		r.text("Only mods can review Salmon Run jobs.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	job, err := b.api.RefuseSalmonJob(ctx, i.Member.User.ID, id, reason)
	if err != nil {
		b.salmonFail(r, err)
		return
	}
	b.updateSalmonReview(s, job)
	for _, p := range job.Players {
		b.dm(s, p.DiscordUserID, "❌ Salmon Run job "+job.ID+" was refused by a mod, so no rank moved: "+reason)
	}
	r.text("Refused. I've told the players why.")
}

func (b *Bot) updateSalmonReview(s *discordgo.Session, j SalmonJob) {
	if j.ReviewChannelID == "" || j.ReviewMessageID == "" {
		return
	}
	none := []discordgo.MessageComponent{}
	embeds := []*discordgo.MessageEmbed{salmonJobEmbed(j)}
	if _, err := s.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: j.ReviewChannelID, ID: j.ReviewMessageID, Embeds: &embeds, Components: &none,
	}); err != nil {
		b.log.Warn("update salmon review post", "job", j.ID, "error", err)
	}
}

func (b *Bot) salmonFail(r reply, err error) {
	if msg := salmonExplain(err); msg != "" {
		r.text(msg)
		return
	}
	if msg := festExplain(err); msg != "" {
		r.text(msg)
		return
	}
	b.log.Error("salmon command", "error", err)
	r.text("Something went wrong on GRID0+'s side. Try again in a minute.")
}

func salmonExplain(err error) string {
	switch {
	case IsCode(err, "bad_players"):
		return "A job has 2 to 4 players, each listed once."
	case IsCode(err, "no_rotation"):
		return "No regular Salmon Run rotation is open right now (a Big Run, or nothing scheduled), so ranks can't move."
	case IsCode(err, "no_job"):
		return "There's no job with that id. It's the code under **Job ID** in the /salmon request answer."
	case IsCode(err, "not_a_player"):
		return "Only a player in that job can do that."
	case IsCode(err, "bad_wave"):
		return "A lost job was lost in wave 1, 2 or 3."
	case IsCode(err, "rotation_over"):
		return "The rotation that job was played in is over, so it can't count any more."
	case IsCode(err, "already_reported"):
		return "That job is already reported and waiting for a mod."
	case IsCode(err, "already_accepted"):
		return "That job was already accepted."
	case IsCode(err, "already_refused"):
		return "That job was refused."
	}
	return ""
}
