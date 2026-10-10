package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Bot answers the slash commands.
type Bot struct {
	cfg Config
	api *SwitchNet
	log *slog.Logger

	// cooldown spaces out the commands that mint a password.
	mu       sync.Mutex
	lastMint map[string]time.Time
}

const mintCooldown = 30 * time.Second

var commands = []*discordgo.ApplicationCommand{
	{
		Name: "register", Description: "Create a GRID0+ account (your login and password are shown only to you)",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "username", Description: "A login name of your choosing, to add an account besides your first one"},
		},
	},
	{Name: "link", Description: "Already have a GRID0+ login from before the bot? Connect it to your Discord"},
	{
		Name: "reset-password", Description: "Get a new GRID0+ password (only you see it)",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "username", Description: "Which of your accounts (default: the one you registered first)", Autocomplete: true},
			{Type: discordgo.ApplicationCommandOptionBoolean, Name: "disconnect_all_accounts", Description: "Disconnect linked consoles too (default: yes)"},
		},
	},
	{
		Name: "splatoon-save-wipe", Description: "Wipe your Splatoon 3 cloud save on GRID0+ (asks to confirm; kept as a backup)",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "username", Description: "Which of your accounts (default: the one you registered first)", Autocomplete: true},
		},
	},
	{Name: "whoami", Description: "Your GRID0+ account: friend code, consoles, friends"},
	{Name: "fc", Description: "Show your GRID0+ friend code"},
	{
		Name: "fc-add", Description: "Send a friend request",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionUser, Name: "user", Description: "A server member who has registered"},
			{Type: discordgo.ApplicationCommandOptionString, Name: "code", Description: "Or a friend code, SW-1234-5678-9012"},
		},
	},
	{Name: "fc-pending", Description: "Friend requests waiting for you, and the ones you sent"},
	{
		Name: "fc-remove", Description: "Remove a friend",
		Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "friend", Description: "Who to remove", Required: true, Autocomplete: true},
		},
	},
	{Name: "friends", Description: "Your GRID0+ friends and what they are doing"},
	{Name: "online", Description: "Who is on GRID0+ right now"},
	{Name: "status", Description: "Is GRID0+ up?"},
	{Name: "avatar", Description: "Use your Discord avatar as your Switch profile picture"},
}

// commandsArePublic are answered in the channel; everything else only to
// the person who asked, since it is about their own account.
var commandsArePublic = map[string]bool{"online": true, "status": true}

func (b *Bot) onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !slices.Contains(b.cfg.Discord.GuildIDs, i.GuildID) || i.Member == nil || i.Member.User == nil {
		// DMs and unknown servers: the bot is for one community.
		return
	}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		b.onCommand(s, i)
	case discordgo.InteractionMessageComponent:
		b.onButton(s, i)
	case discordgo.InteractionApplicationCommandAutocomplete:
		b.onAutocomplete(s, i)
	case discordgo.InteractionModalSubmit:
		b.onLinkSubmit(s, i)
	}
}

// reply is where a command writes its answer after the deferral.
type reply struct {
	s *discordgo.Session
	i *discordgo.InteractionCreate
}

func (r reply) text(msg string) {
	r.edit(&discordgo.WebhookEdit{Content: &msg})
}

func (r reply) edit(e *discordgo.WebhookEdit) {
	// Names in answers are <@id> mentions for readability; nobody should be
	// pinged because someone else ran /online.
	e.AllowedMentions = &discordgo.MessageAllowedMentions{}
	if _, err := r.s.InteractionResponseEdit(r.i.Interaction, e); err != nil {
		slog.Warn("edit interaction response", "error", err)
	}
}

func (b *Bot) onCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	name := i.ApplicationCommandData().Name
	if name == "link" {
		// A form, not command options: a password typed as an option would
		// sit in the person's command history.
		b.showLinkForm(s, i)
		return
	}
	var flags discordgo.MessageFlags
	if !commandsArePublic[name] {
		flags = discordgo.MessageFlagsEphemeral
	}
	// Defer first: Discord allows three seconds for the first answer, and an
	// avatar download or a slow SwitchNet can take longer.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: flags},
	}); err != nil {
		b.log.Warn("defer interaction", "command", name, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := reply{s, i}
	user := i.Member.User
	b.log.Info("command", "name", name, "user", user.ID, "username", user.Username)

	handlers := map[string]func(context.Context, reply, *discordgo.User){
		"register":           b.register,
		"reset-password":     b.resetPassword,
		"splatoon-save-wipe": b.saveWipePrompt,
		"whoami":             b.whoami,
		"fc":                 b.fc,
		"fc-add":             b.fcAdd,
		"fc-pending":         b.fcPending,
		"fc-remove":          b.fcRemove,
		"friends":            b.friends,
		"online":             b.online,
		"status":             b.status,
		"avatar":             b.avatar,
	}
	if h, ok := handlers[name]; ok {
		h(ctx, r, user)
	}
}

// explain turns a SwitchNet refusal into a sentence for the person.
func (b *Bot) explain(r reply, err error) {
	messages := map[string]string{
		"not_registered":      "You don't have a GRID0+ account yet. Run `/register` first.",
		"registration_closed": "Registration is closed right now. Ask the server admin.",
		"already_registered":  "You already have a GRID0+ account. Lost your password? Run `/reset-password`. Want another account? Run `/register username:...`.",
		"username_taken":      "That login is already a GRID0+ username. If it's yours from before the bot, run `/link` instead.",
		"not_your_account":    "That's not one of your accounts, so I won't touch it.",
		"bad_credentials":     "That login and password don't match. Check them and try again.",
		"claimed":             "That account is already connected to another Discord account. Ask the server admin.",
		"rate_limited":        "Too many tries. Wait a few minutes.",
		"no_such_user":        "Nobody on GRID0+ matches that. They may need to `/register` first.",
		"self":                "You can't add yourself.",
		"refused":             "That person isn't accepting friend requests.",
		"already_friends":     "You're already friends.",
		"not_friends":         "You're not friends with them.",
		"no_such_request":     "That friend request doesn't exist any more.",
		"not_pending":         "That request was already answered.",
		"not_jpeg":            "Discord didn't give me a usable picture. Try again later.",
		"bad_friend_code":     "A friend code is 12 digits, like SW-1234-5678-9012.",
		"no_cloud_save":       "That account has no Splatoon 3 cloud save on GRID0+, so there's nothing to wipe.",
		"save_wipe_disabled":  "Wiping cloud saves isn't turned on on this GRID0+ server.",
	}
	for code, msg := range messages {
		if IsCode(err, code) {
			r.text(msg)
			return
		}
	}
	b.log.Error("switchnet call failed", "error", err)
	r.text("Something went wrong talking to GRID0+. Try again in a minute; if it keeps happening, tell the server admin.")
}

// allowMint enforces the password cooldown per person and per account, so
// somebody running several accounts is not made to wait between each of them
// while a single person still cannot mint one password per second.
func (b *Bot) allowMint(userID, account string) bool {
	key := userID + "/" + account
	b.mu.Lock()
	defer b.mu.Unlock()
	if last, ok := b.lastMint[key]; ok && time.Since(last) < mintCooldown {
		return false
	}
	b.lastMint[key] = time.Now()
	return true
}

// option reads a command option by name, or "" when it was not given.
func option(data discordgo.ApplicationCommandInteractionData, name string) string {
	for _, o := range data.Options {
		if o.Name == name {
			return o.StringValue()
		}
	}
	return ""
}

func credentialsEmbed(title string, c Credentials) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title: title,
		Color: 0xe60012,
		Description: "On your Switch: **System Settings → Users → your user → Link Nintendo Account**, " +
			"then sign in with these. Only you can see this message. Nobody, including the admin, can see your password again.",
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Login", Value: "`" + c.Username + "`", Inline: true},
			{Name: "Password", Value: "`" + c.Password + "`", Inline: true},
		},
	}
}

func (b *Bot) register(ctx context.Context, r reply, user *discordgo.User) {
	if role := b.cfg.Discord.RegisterRoleID; role != "" && !slices.Contains(r.i.Member.Roles, role) {
		r.text("You need the right role in this server to register. Ask the server admin.")
		return
	}
	// With no login named, the account is made from the person's own Discord
	// username, which only makes sense for someone who has none yet. Naming
	// one is how a second account is asked for.
	login := strings.TrimSpace(option(r.i.ApplicationCommandData(), "username"))
	if login == "" {
		login = user.Username
	}
	if !b.allowMint(user.ID, login) {
		r.text("Slow down a little; try again in half a minute.")
		return
	}
	creds, err := b.api.Register(ctx, user.ID, login, displayName(r.i.Member), login != user.Username)
	if err != nil {
		b.explain(r, err)
		return
	}
	embed := credentialsEmbed("Your GRID0+ account", creds)
	if creds.FriendCode != "" {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Friend code", Value: formatFriendCode(creds.FriendCode)})
	}
	if err := b.syncAvatar(ctx, user); err != nil {
		b.log.Warn("avatar at register", "user", user.ID, "error", err)
	} else {
		embed.Footer = &discordgo.MessageEmbedFooter{Text: "Your Discord avatar is now your Switch profile picture. /avatar re-syncs it."}
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
}

func (b *Bot) resetPassword(ctx context.Context, r reply, user *discordgo.User) {
	// Which of their accounts: the one named, or their first. GRID0+ resolves
	// it against the accounts that Discord account owns, so a name that is
	// not one of theirs never reaches anybody's password.
	login := strings.TrimSpace(option(r.i.ApplicationCommandData(), "username"))
	if !b.allowMint(user.ID, login) {
		r.text("Slow down a little; try again in half a minute.")
		return
	}
	disconnect := true
	for _, o := range r.i.ApplicationCommandData().Options {
		if o.Name == "disconnect_all_accounts" {
			disconnect = o.BoolValue()
		}
	}
	creds, err := b.api.ResetPassword(ctx, user.ID, login, disconnect)
	if err != nil {
		b.explain(r, err)
		return
	}
	embed := credentialsEmbed("Your new GRID0+ password", creds)
	if disconnect {
		embed.Description = "Your old password no longer works, and linked consoles will be asked to sign in again. Only you can see this message."
	} else {
		embed.Description = "Your old password no longer works. Linked consoles stay signed in. Only you can see this message."
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
}

func (b *Bot) whoami(ctx context.Context, r reply, user *discordgo.User) {
	me, err := b.api.Me(ctx, user.ID)
	if err != nil {
		b.explain(r, err)
		return
	}
	consoles := fmt.Sprintf("%d", me.Consoles)
	if me.Consoles == 0 {
		consoles = "none yet: link your Switch with `/register`'s login"
	}
	embed := &discordgo.MessageEmbed{
		Title: displayUser(me.User),
		Color: 0xe60012,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Login", Value: "`" + me.Username + "`", Inline: true},
			{Name: "Friend code", Value: formatFriendCode(me.FriendCode), Inline: true},
			{Name: "Status", Value: presenceWord(me.User), Inline: true},
			{Name: "Linked consoles", Value: consoles, Inline: true},
			{Name: "Friends", Value: fmt.Sprintf("%d", me.Friends), Inline: true},
			{Name: "Registered", Value: fmt.Sprintf("<t:%d:D>", me.RegisteredAt.Unix()), Inline: true},
		},
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
}

func (b *Bot) fc(ctx context.Context, r reply, user *discordgo.User) {
	me, err := b.api.Me(ctx, user.ID)
	if err != nil {
		b.explain(r, err)
		return
	}
	r.text("Your friend code: **" + formatFriendCode(me.FriendCode) + "**")
}

var friendCodeDigits = regexp.MustCompile(`[0-9]`)

func (b *Bot) fcAdd(ctx context.Context, r reply, user *discordgo.User) {
	var target *discordgo.User
	var code string
	for _, opt := range r.i.ApplicationCommandData().Options {
		switch opt.Name {
		case "user":
			target = opt.UserValue(r.s)
		case "code":
			code = strings.Join(friendCodeDigits.FindAllString(opt.StringValue(), -1), "")
		}
	}
	var result string
	var other User
	var err error
	switch {
	case target != nil:
		if target.Bot {
			r.text("Bots don't have Switches.")
			return
		}
		result, other, err = b.api.SendFriendRequest(ctx, user.ID, target.ID, "")
	case len(code) == 12:
		result, other, err = b.api.SendFriendRequest(ctx, user.ID, "", code)
	default:
		r.text("Give me a server member (`user`) or a 12-digit friend code (`code`).")
		return
	}
	if err != nil {
		b.explain(r, err)
		return
	}
	if result == "became_friends" {
		r.text("They had already sent you a request, so you're now friends with **" + displayUser(other) + "**.")
		return
	}
	r.text("Friend request sent to **" + displayUser(other) + "**. They can accept it with `/fc-pending` or on their Switch.")
}

func (b *Bot) fcPending(ctx context.Context, r reply, user *discordgo.User) {
	incoming, outgoing, err := b.api.FriendRequests(ctx, user.ID)
	if err != nil {
		b.explain(r, err)
		return
	}
	if len(incoming) == 0 && len(outgoing) == 0 {
		r.text("No friend requests waiting either way.")
		return
	}
	var lines []string
	var rows []discordgo.MessageComponent
	for _, fr := range incoming {
		lines = append(lines, "📥 from **"+displayUser(fr.Other)+"**")
		if len(rows) < 5 { // Discord's limit on rows of buttons
			rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Accept " + short(displayUser(fr.Other)), Style: discordgo.SuccessButton, CustomID: "fr:accept:" + fr.ID},
				discordgo.Button{Label: "Decline", Style: discordgo.SecondaryButton, CustomID: "fr:deny:" + fr.ID},
			}})
		}
	}
	for _, fr := range outgoing {
		lines = append(lines, "📤 to **"+displayUser(fr.Other)+"**")
		if len(rows) < 5 {
			rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Cancel request to " + short(displayUser(fr.Other)), Style: discordgo.DangerButton, CustomID: "fr:cancel:" + fr.ID},
			}})
		}
	}
	content := strings.Join(lines, "\n")
	r.edit(&discordgo.WebhookEdit{Content: &content, Components: &rows})
}

func (b *Bot) showLinkForm(s *discordgo.Session, i *discordgo.InteractionCreate) {
	input := func(id, label string) discordgo.MessageComponent {
		return discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.TextInput{CustomID: id, Label: label, Style: discordgo.TextInputShort, Required: true, MaxLength: 256},
		}}
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID:   "link",
			Title:      "Connect your GRID0+ login",
			Components: []discordgo.MessageComponent{input("username", "GRID0+ login"), input("password", "Password")},
		},
	})
	if err != nil {
		b.log.Warn("show link form", "error", err)
	}
}

func (b *Bot) onLinkSubmit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()
	if data.CustomID != "link" {
		return
	}
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
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := reply{s, i}
	user, err := b.api.Link(ctx, i.Member.User.ID, values["username"], values["password"])
	if err != nil {
		b.explain(r, err)
		return
	}
	b.log.Info("linked an existing account", "discord", i.Member.User.ID, "login", user.Username)
	r.text("Connected: your Discord is now **" + user.Username + "** on GRID0+. Try `/whoami`.")
}

// onButton handles the accept/decline/cancel buttons from /fc-pending. The
// person pressing is the one SwitchNet checks, so a button in someone
// else's message could do nothing on their behalf.
func (b *Bot) onButton(s *discordgo.Session, i *discordgo.InteractionCreate) {
	parts := strings.SplitN(i.MessageComponentData().CustomID, ":", 3)
	if len(parts) == 3 && parts[0] == "sw" {
		b.onSaveWipeButton(s, i, parts[1], parts[2])
		return
	}
	if len(parts) != 3 || parts[0] != "fr" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := b.api.SettleFriendRequest(ctx, i.Member.User.ID, parts[2], parts[1])
	msg := map[string]string{"accept": "Accepted: you're friends now.", "deny": "Declined.", "cancel": "Request cancelled."}[parts[1]]
	if err != nil {
		msg = "That didn't work: it may have been answered already."
		if !IsCode(err, "no_such_request") && !IsCode(err, "not_pending") {
			b.log.Error("settle friend request", "error", err)
		}
	}
	// A fresh private reply rather than editing the list: the other buttons
	// in it still work.
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	})
}

func (b *Bot) fcRemove(ctx context.Context, r reply, user *discordgo.User) {
	userID := r.i.ApplicationCommandData().Options[0].StringValue()
	friends, err := b.api.Friends(ctx, user.ID)
	if err != nil {
		b.explain(r, err)
		return
	}
	idx := slices.IndexFunc(friends, func(f User) bool { return f.UserID == userID })
	if idx < 0 {
		r.text("Pick someone from the list: they're not one of your friends.")
		return
	}
	if err := b.api.RemoveFriend(ctx, user.ID, userID); err != nil {
		b.explain(r, err)
		return
	}
	r.text("Removed **" + displayUser(friends[idx]) + "** from your friends.")
}

// onAutocomplete offers the caller's own accounts for /reset-password, and
// their friends for /fc-remove. Nothing here is ever anybody else's: a reset
// only reaches the caller's accounts, so offering a stranger's login would be
// offering something that cannot be used.
func (b *Bot) onAutocomplete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data := i.ApplicationCommandData()
	// The option being typed is the last one Discord sends.
	typed := ""
	if n := len(data.Options); n > 0 {
		typed = strings.ToLower(data.Options[n-1].StringValue())
	}
	var choices []*discordgo.ApplicationCommandOptionChoice
	add := func(name, value string) {
		if typed != "" && !strings.Contains(strings.ToLower(name), typed) {
			return
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{Name: name, Value: value})
	}
	if data.Name == "reset-password" || data.Name == "splatoon-save-wipe" {
		accounts, _ := b.api.Accounts(ctx, i.Member.User.ID)
		for _, a := range accounts {
			name := a.Username
			if a.Primary {
				name += " (your first account)"
			}
			add(name, a.Username)
			if len(choices) == maxChoices {
				break
			}
		}
	} else {
		friends, _ := b.api.Friends(ctx, i.Member.User.ID)
		for _, f := range friends {
			add(displayUser(f), f.UserID)
			if len(choices) == maxChoices {
				break
			}
		}
	}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: choices},
	})
}

// maxChoices is Discord's limit on autocomplete suggestions.
const maxChoices = 25

func (b *Bot) friends(ctx context.Context, r reply, user *discordgo.User) {
	friends, err := b.api.Friends(ctx, user.ID)
	if err != nil {
		b.explain(r, err)
		return
	}
	if len(friends) == 0 {
		r.text("No friends yet. Add someone with `/fc-add`.")
		return
	}
	sort.SliceStable(friends, func(a, c int) bool { return presenceRank(friends[a]) < presenceRank(friends[c]) })
	lines := make([]string, 0, len(friends))
	for _, f := range friends {
		lines = append(lines, presenceDot(f)+" **"+displayUser(f)+"**: "+presenceWord(f))
	}
	r.text(strings.Join(lines, "\n"))
}

func (b *Bot) online(ctx context.Context, r reply, _ *discordgo.User) {
	users, err := b.api.Online(ctx)
	if err != nil {
		b.explain(r, err)
		return
	}
	if len(users) == 0 {
		r.text("Nobody's on GRID0+ right now.")
		return
	}
	sort.SliceStable(users, func(a, c int) bool { return presenceRank(users[a]) < presenceRank(users[c]) })
	lines := make([]string, 0, len(users))
	for _, u := range users {
		lines = append(lines, presenceDot(u)+" **"+displayUser(u)+"**: "+presenceWord(u))
	}
	r.text(fmt.Sprintf("**%d on GRID0+**\n", len(users)) + strings.Join(lines, "\n"))
}

func (b *Bot) status(ctx context.Context, r reply, _ *discordgo.User) {
	st, err := b.api.Status(ctx)
	if err != nil {
		r.text("🔴 GRID0+ isn't answering at all.")
		b.log.Error("status", "error", err)
		return
	}
	up := 0
	var lines []string
	for _, svc := range st.Services {
		icon := map[string]string{"ok": "🟢", "degraded": "🟡", "down": "🔴"}[svc.Status]
		if icon == "" {
			icon = "⚪"
		}
		// "unknown" is a service that cannot be probed from here (natcheck
		// speaks only Pia's protocol), not one that is down.
		if svc.Status == "ok" || svc.Status == "unknown" {
			up++
		}
		line := icon + " " + svc.Name
		if svc.Status != "ok" && svc.Detail != "" {
			line += ": " + svc.Detail
		}
		lines = append(lines, line)
	}
	headline := "🟢 GRID0+ is up"
	if up < len(st.Services) {
		headline = fmt.Sprintf("🟡 GRID0+: %d of %d services up", up, len(st.Services))
	}
	embed := &discordgo.MessageEmbed{
		Title:       headline,
		Description: strings.Join(lines, "\n"),
		Color:       0xe60012,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("%d online · %d players", st.Online, st.Players)},
	}
	r.edit(&discordgo.WebhookEdit{Embeds: &[]*discordgo.MessageEmbed{embed}})
}

func (b *Bot) avatar(ctx context.Context, r reply, user *discordgo.User) {
	if user.Avatar == "" {
		r.text("You don't have a Discord avatar set, so there's nothing to copy.")
		return
	}
	if err := b.syncAvatar(ctx, user); err != nil {
		b.explain(r, err)
		return
	}
	r.text("Done: your Discord avatar is now your Switch profile picture. Your Switch shows it next time it signs in.")
}

// syncAvatar copies the Discord avatar to SwitchNet. Discord's CDN serves it
// as a 256x256 JPEG directly, the size and format of a Switch profile icon.
func (b *Bot) syncAvatar(ctx context.Context, user *discordgo.User) error {
	if user.Avatar == "" {
		return fmt.Errorf("no avatar set")
	}
	url := fmt.Sprintf("https://cdn.discordapp.com/avatars/%s/%s.jpg?size=256", user.ID, user.Avatar)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discord cdn: %s", resp.Status)
	}
	jpeg, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return b.api.SetAvatar(ctx, user.ID, jpeg)
}

// ---- presentation ------------------------------------------------------------

func displayName(m *discordgo.Member) string {
	if m.Nick != "" {
		return m.Nick
	}
	if m.User.GlobalName != "" {
		return m.User.GlobalName
	}
	return m.User.Username
}

func displayUser(u User) string {
	name := u.Nickname
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = "a console"
	}
	if u.DiscordUserID != "" {
		return name + " (<@" + u.DiscordUserID + ">)"
	}
	return name
}

func short(s string) string {
	if i := strings.Index(s, " (<@"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 30 {
		return string(r[:30])
	}
	return s
}

func formatFriendCode(code string) string {
	d := strings.Join(friendCodeDigits.FindAllString(code, -1), "")
	if len(d) != 12 {
		return code
	}
	return "SW-" + d[:4] + "-" + d[4:8] + "-" + d[8:]
}

// titles names the games SwitchNet serves; anything else shows its id.
var titles = map[string]string{
	"0100c2500fc20000": "Splatoon 3",
	"01003bc0000a0000": "Splatoon 2",
}

func presenceWord(u User) string {
	switch u.Presence {
	case "PLAYING":
		if name, ok := titles[strings.ToLower(u.AppID)]; ok {
			return "playing " + name
		}
		return "playing a game"
	case "ONLINE":
		return "online"
	case "":
		return "hidden"
	}
	return "offline"
}

func presenceDot(u User) string {
	switch u.Presence {
	case "PLAYING":
		return "🟢"
	case "ONLINE":
		return "🔵"
	}
	return "⚫"
}

func presenceRank(u User) int {
	switch u.Presence {
	case "PLAYING":
		return 0
	case "ONLINE":
		return 1
	}
	return 2
}

// saveWipePrompt explains what wiping does and asks for a button press: the
// wipe itself only happens in onSaveWipeButton.
func (b *Bot) saveWipePrompt(ctx context.Context, r reply, user *discordgo.User) {
	login := strings.TrimSpace(option(r.i.ApplicationCommandData(), "username"))
	which := "your first account"
	if login != "" {
		which = "**" + login + "**"
	}
	msg := "This deletes the Splatoon 3 cloud save GRID0+ keeps for " + which + ".\n\n" +
		"Use it after putting a different save on your Switch, so GRID0+ doesn't mix it with the old one. " +
		"Your game keeps playing from the save on your console, and GRID0+ keeps a backup of the old cloud save " +
		"in case you need it back (ask the server admin).\n\n**Close Splatoon 3 first**, then press Wipe."
	r.edit(&discordgo.WebhookEdit{
		Content: &msg,
		Components: &[]discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Wipe my cloud save", Style: discordgo.DangerButton, CustomID: "sw:confirm:" + login},
			discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: "sw:cancel:"},
		}}},
	})
}

// onSaveWipeButton runs the wipe once confirmed. The account is always the
// presser's own: GRID0+ resolves the login against the presser's Discord id,
// so a button forwarded to someone else can only ever reach their accounts.
func (b *Bot) onSaveWipeButton(s *discordgo.Session, i *discordgo.InteractionCreate, action, login string) {
	msg := "Cancelled: nothing was deleted."
	if action == "confirm" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		res, err := b.api.WipeSplatoon3Save(ctx, i.Member.User.ID, login)
		switch {
		case err == nil:
			msg = fmt.Sprintf("Done: the Splatoon 3 cloud save of **%s** is wiped (backup `%s`). "+
				"Start the game; it will keep your console's save and upload changes from now on.", res.Login, res.Backup)
			b.log.Info("splatoon 3 save wiped", "user", i.Member.User.ID, "login", res.Login, "backup", res.Backup)
		case IsCode(err, "no_cloud_save"):
			msg = "That account has no Splatoon 3 cloud save on GRID0+, so there's nothing to wipe."
		case IsCode(err, "not_your_account"):
			msg = "That's not one of your accounts, so I won't touch it."
		case IsCode(err, "not_registered"):
			msg = "You don't have a GRID0+ account yet. Run `/register` first."
		default:
			msg = "That didn't work. Nothing was deleted; try again later."
			b.log.Error("wipe splatoon 3 save", "error", err)
		}
	}
	// Replace the prompt, so its buttons can't be pressed twice.
	empty := []discordgo.MessageComponent{}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: msg, Components: empty},
	})
}
