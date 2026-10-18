package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// maxSaveFile is the largest save.dat accepted. The game reads its save
// into a 1 MiB buffer; real saves are about 80-95 KB.
const maxSaveFile = 1 << 20

var errNotDiscord = errors.New("attachment is not on Discord's CDN")

// pendingSaveTTL is how long an uploaded save waits for its confirm button.
const pendingSaveTTL = 10 * time.Minute

type pendingSave struct {
	owner   string // Discord id of the person who uploaded it
	login   string
	data    []byte
	expires time.Time
}

// saveUploadPrompt downloads the attached save.dat and asks for a button
// press; the import only happens in onSaveUploadButton. The file is held
// here in memory rather than in the button, which can carry 100 characters.
func (b *Bot) saveUploadPrompt(ctx context.Context, r reply, user *discordgo.User) {
	data := r.i.ApplicationCommandData()
	login := strings.TrimSpace(option(data, "username"))
	var att *discordgo.MessageAttachment
	if data.Resolved != nil {
		for _, a := range data.Resolved.Attachments {
			att = a
		}
	}
	if att == nil {
		r.text("Attach your Splatoon 3 `save.dat` to the command.")
		return
	}
	if att.Size > maxSaveFile {
		r.text("That file is too big to be a Splatoon 3 save.dat.")
		return
	}
	save, err := fetchAttachment(ctx, att.URL, maxSaveFile)
	if err != nil {
		b.log.Error("download save upload", "error", err)
		r.text("I couldn't download that file from Discord. Try again.")
		return
	}

	b.mu.Lock()
	if b.pendingSaves == nil {
		b.pendingSaves = map[string]pendingSave{}
	}
	now := time.Now()
	for k, p := range b.pendingSaves {
		if now.After(p.expires) {
			delete(b.pendingSaves, k)
		}
	}
	b.pendingSaves[r.i.ID] = pendingSave{owner: user.ID, login: login, data: save, expires: now.Add(pendingSaveTTL)}
	b.mu.Unlock()

	which := "your first account"
	if login != "" {
		which = "**" + login + "**"
	}
	msg := "This replaces the Splatoon 3 cloud save GRID0+ keeps for " + which + " with the save.dat you sent " +
		"(" + fmt.Sprint(len(save)) + " bytes). Your rank, money, weapons and gear come from it the next time the game goes online. " +
		"The current cloud save is kept as a backup (ask the server admin).\n\n" +
		"**Close Splatoon 3 first**: a running game would write its own progress back over the upload. Then press Replace."
	r.edit(&discordgo.WebhookEdit{
		Content: &msg,
		Components: &[]discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Replace my cloud save", Style: discordgo.DangerButton, CustomID: "su:confirm:" + r.i.ID},
			discordgo.Button{Label: "Cancel", Style: discordgo.SecondaryButton, CustomID: "su:cancel:" + r.i.ID},
		}}},
	})
}

// fetchAttachment downloads a Discord attachment. Only Discord's own CDN
// hosts are fetched: the URL comes from Discord's payload, and this keeps
// the bot from being pointed anywhere else.
func fetchAttachment(ctx context.Context, raw string, limit int) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || (u.Host != "cdn.discordapp.com" && u.Host != "media.discordapp.net") {
		return nil, fmt.Errorf("%w: %s://%s", errNotDiscord, u.Scheme, u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discord cdn: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("attachment over %d bytes", limit)
	}
	return data, nil
}

// takePendingSave removes and returns the upload a button refers to, but
// only to the person who uploaded it: a prompt is ephemeral, and even if
// its button reached someone else it must not import a stranger's file.
func (b *Bot) takePendingSave(id, presser string) (pendingSave, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pendingSaves[id]
	if !ok || p.owner != presser {
		return pendingSave{}, false
	}
	delete(b.pendingSaves, id)
	if time.Now().After(p.expires) {
		return pendingSave{}, false
	}
	return p, true
}

// onSaveUploadButton runs the import once confirmed. As with the wipe,
// GRID0+ resolves the login against the presser's Discord id.
func (b *Bot) onSaveUploadButton(s *discordgo.Session, i *discordgo.InteractionCreate, action, id string) {
	p, ok := b.takePendingSave(id, i.Member.User.ID)
	msg := "Cancelled: nothing was changed."
	switch {
	case action != "confirm":
	case !ok:
		msg = "That upload has expired. Run `/splatoon-save-upload` again."
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := b.api.ImportSplatoon3Save(ctx, i.Member.User.ID, p.login, p.data)
		switch {
		case err == nil:
			msg = fmt.Sprintf("Done: the Splatoon 3 cloud save of **%s** is now your save.dat (game version %s).", res.Login, res.GameVersion)
			if res.Backup != "" {
				msg += fmt.Sprintf(" The old one is kept as `%s`.", res.Backup)
			}
			if res.SetAside > 0 {
				msg += fmt.Sprintf("\n%d bit(s) of your save are newer than GRID0+ knows, so they were left out for now. "+
					"Your save.dat is kept, and the server admin can add them later.", res.SetAside)
			}
			msg += " Start the game and go online to pick it up."
			b.log.Info("splatoon 3 save imported", "user", i.Member.User.ID, "login", res.Login, "backup", res.Backup, "set_aside", res.SetAside)
		case IsCode(err, "not_a_save"):
			msg = "That file isn't a Splatoon 3 save.dat this server can open. It has to be the `save.dat` from Splatoon 3's own save data, unedited. Nothing was changed."
		case IsCode(err, "bad_save"):
			msg = "That save.dat opened but has something GRID0+ doesn't know how to store, so nothing was changed. Tell the server admin."
		case IsCode(err, "not_your_account"):
			msg = "That's not one of your accounts, so I won't touch it."
		case IsCode(err, "not_registered"):
			msg = "You don't have a GRID0+ account yet. Run `/register` first."
		case IsCode(err, "save_import_disabled"):
			msg = "Uploading saves isn't turned on on this GRID0+ server."
		default:
			msg = "That didn't work. Nothing was changed; try again later."
			b.log.Error("import splatoon 3 save", "error", err)
		}
	}
	empty := []discordgo.MessageComponent{}
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Content: msg, Components: empty},
	})
}
