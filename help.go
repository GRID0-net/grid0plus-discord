package main

import (
	"slices"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Help lists only what the caller may use: a member who is not a mod never
// learns the mod commands from the bot, not even from /help.

type helpEntry struct{ command, what string }

type helpCategory struct {
	key, title string
	// mods and admins say who may see the category at all.
	mods, admins bool
	// fest says it exists only when fests are configured.
	fest            bool
	tournament      bool
	tournamentAdmin bool
	entries         []helpEntry
}

var helpCategories = []helpCategory{
	{key: "tournament", title: "Tournaments", tournament: true, entries: []helpEntry{
		{"/tournament participate", "Enter with your linked GRID0+ account; optionally invite a teammate."},
		{"/tournament accept", "Accept or decline an invitation from the DM, here in the server."},
		{"/tournament availability", "Set your IANA timezone and available local hours."},
		{"/tournament leave", "Withdraw before registration closes."},
		{"/tournament status", "Teams, schedules, match/game IDs and delivery status."},
	}},
	{key: "tournament-admin", title: "Tournament organizers", tournament: true, tournamentAdmin: true, entries: []helpEntry{
		{"/tournament-admin create", "Title, prizes, organizer users or role, registration deadline and optional round windows."},
		{"/tournament-admin modify", "Edit details, registration, organizer roster, schedule and results in one group."},
		{"/tournament-admin modify delay", "Delay a scheduled series using its match ID, minutes and reason; everyone is notified."},
		{"/tournament-admin close", "Close entries; format locks teams, determines round count, and optionally sets their time windows."},
		{"/tournament-admin schedule", "Find slots that fit players and an organizer; reschedule or unschedule explicitly."},
		{"/tournament-admin report", "The assigned organizer starts the match and records each best-of-three game."},
		{"/tournament-admin audit", "Review actions. Undo corrects unused results; withdraw records a forfeit; cancel ends an event."},
	}},
	{key: "account", title: "Your GRID0+ account", entries: []helpEntry{
		{"/register", "Create a GRID0+ account. Your login and password are shown only to you."},
		{"/link", "Connect a GRID0+ login you had before the bot to your Discord."},
		{"/reset-password", "Get a new password, and disconnect your consoles if you want."},
		{"/set-password", "Choose your own password instead of the generated one."},
		{"/whoami", "Your friend code, linked consoles and friends."},
		{"/avatar", "Use your Discord avatar as your Switch profile picture."},
		{"/splatoon-save-upload", "Replace your Splatoon 3 save on GRID0+ with a save.dat."},
		{"/splatoon-save-wipe", "Wipe your Splatoon 3 save on GRID0+ (a backup is kept)."},
	}},
	{key: "friends", title: "Friends", entries: []helpEntry{
		{"/fc", "Show your friend code."},
		{"/fc-add", "Send a friend request, to a member or a friend code."},
		{"/fc-pending", "Requests waiting for you, and the ones you sent."},
		{"/fc-remove", "Remove a friend."},
		{"/friends", "Your friends and what they are doing."},
		{"/online", "Who is on GRID0+ right now."},
	}},
	{key: "server", title: "GRID0+", entries: []helpEntry{
		{"/status", "Is GRID0+ up?"},
	}},
	{key: "dashboard", title: "Admins", admins: true, entries: []helpEntry{
		{"/dashboard", "A dashboard login link, working for 15 minutes."},
	}},
	{key: "fest", title: "Splatfests", fest: true, entries: []helpEntry{
		{"Vote post", "React with your team's emoji to join it. Your first pick is final, here and in game."},
		{"/fest submit", "Report a LAN game you played (Open, Pro, or Tricolor in the second half). A mod checks it before it counts."},
		{"/fest me", "Your team, games and contribution in the current fest."},
		{"/fest standings", "What the game shows about the current fest right now."},
		{"/fest help", "This list."},
	}},
	{key: "fest-mod", title: "Splatfests, for mods", mods: true, fest: true, entries: []helpEntry{
		{"/fest-mod create", "Post a fest vote: a title, the text (in a form), the three teams. Only one fest at a time."},
		{"/fest-mod cancel", "Remove the current fest's vote post. A LAN-only fest loses its votes and games."},
		{"Review queue", "Accept or Refuse each LAN game in the review channel. Accepted games count like online ones."},
		{"/fest-mod help", "This list."},
	}},
	{key: "salmon", title: "Salmon Run over LAN", fest: true, entries: []helpEntry{
		{"/salmon request", "Set up a job with 1 to 3 teammates. You get the stage to pick and the hazard level, from everyone's rank: the average rank x 0.2%, to the nearest 5%. Leave weapons on random."},
		{"/salmon report", "Say how the job went: cleared, or the wave you lost in, and whether a teammate disconnected (a loss then costs half). A mod checks it."},
		{"/salmon rank", "A rank and Eggsecutive VP stage badges. It's the same rank the game shows online."},
		{"Points", "Clear +20 (+10 when played at 200% because the host can't go higher). Lost in wave 1: -20, wave 2: -10, wave 3: 0. Points reset at each new rotation, as in game."},
		{"/salmon help", "This list."},
	}},
	{key: "salmon-mod", title: "Salmon Run, for mods", mods: true, fest: true, entries: []helpEntry{
		{"Review queue", "Accept or Refuse each reported job in the review channel. Accepting moves every player's rank, once."},
		{"Check", "The stage and hazard in the job, everyone listed played, and a disconnect really happened."},
		{"/salmon-mod help", "This list."},
	}},
}

func (b *Bot) mayRead(c helpCategory, m *discordgo.Member) bool {
	if c.tournament && b.cfg.Tournaments.StoreFile == "" {
		return false
	}
	if c.tournamentAdmin && !b.tournamentAdmin(m) {
		return false
	}
	if c.fest && !b.cfg.festEnabled() {
		return false
	}
	if c.mods && !b.isMod(m) {
		return false
	}
	if c.admins && (m == nil || b.cfg.Dashboard.AdminRoleID == "" || !slices.Contains(m.Roles, b.cfg.Dashboard.AdminRoleID)) {
		return false
	}
	return true
}

// helpEmbed is one category's help, or the overview for "". A category the
// member may not read answers as if it did not exist.
func (b *Bot) helpEmbed(key string, m *discordgo.Member) *discordgo.MessageEmbed {
	e := &discordgo.MessageEmbed{Color: 0x3b82f6}
	for _, c := range helpCategories {
		if !b.mayRead(c, m) {
			continue
		}
		if key == "" {
			e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: c.title, Value: commandList(c)})
			continue
		}
		if c.key == key {
			e.Title = c.title
			for _, en := range c.entries {
				e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: en.command, Value: en.what})
			}
			return e
		}
	}
	if key == "" {
		e.Title = "What I can do"
		e.Description = "`/help category:` for the details of one."
	} else {
		e.Title, e.Description = "Help", "There's no help on that."
	}
	return e
}

func commandList(c helpCategory) string {
	var names []string
	for _, en := range c.entries {
		if strings.HasPrefix(en.command, "/") && !strings.HasSuffix(en.command, " help") {
			names = append(names, "`"+en.command+"`")
		}
	}
	if len(names) == 0 {
		return c.entries[0].what
	}
	return strings.Join(names, " ")
}

func helpCommand() *discordgo.ApplicationCommand {
	var choices []*discordgo.ApplicationCommandOptionChoice
	for _, c := range helpCategories {
		// Mod categories are left out of the picker, which every member
		// sees; a mod reaches them with /fest-mod help or /salmon-mod help.
		if !c.mods && !c.admins && !c.tournamentAdmin {
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: c.title, Value: c.key})
		}
	}
	return &discordgo.ApplicationCommand{
		Name: "help", Description: "What the bot's commands do",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "category", Description: "One category's details", Choices: choices},
		},
	}
}

func salmonModCommand() *discordgo.ApplicationCommand {
	return &discordgo.ApplicationCommand{
		Name: "salmon-mod", Description: "Mods: reviewing Salmon Run LAN jobs",
		// See /fest-mod: hidden by default, still checked on every use.
		DefaultMemberPermissions: ptr(int64(discordgo.PermissionManageGuild)),
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "help", Description: "What reviewing a job means"},
		},
	}
}

// onHelpCommand answers /help and /salmon-mod help, privately.
func (b *Bot) onHelpCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ApplicationCommandData()
	key := ""
	if data.Name == "salmon-mod" {
		key = "salmon-mod"
	} else if len(data.Options) > 0 {
		key = data.Options[0].StringValue()
	}
	resp := &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}
	if data.Name == "salmon-mod" && !b.isMod(i.Member) {
		resp.Content = "Only mods can use `/salmon-mod`."
	} else {
		resp.Embeds = []*discordgo.MessageEmbed{b.helpEmbed(key, i.Member)}
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource, Data: resp,
	})
}
