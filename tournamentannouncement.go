package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
)

func tournamentAnnouncement(t *tournament) string {
	// Keep this one message so its registration counter can be edited in place.
	prize := []rune(t.Prize)
	if len(prize) > 500 {
		prize = append(prize[:500], '…')
	}
	format := "Undecided"
	if t.Size > 0 {
		format = fmt.Sprintf("%dv%d", t.Size, t.Size)
	}
	return fmt.Sprintf("**%s** — `%s`\n**Participants: %d** · Status: %s · Format: %s\nRegistration closes <t:%d:F>.\n%s\nEnter with `/tournament participate tournament:%s` (optional `team` invitation). GRID0+ account required.\nAccepted pairs stay together in 2v2; 1v1 splits them into individual entrants. Format selected after registration closes.\nPrizes: %s\nTimezone: %s.", t.Title, t.ID, len(t.People), t.State, format, t.Deadline.Unix(), tournamentPlanningText(t), t.ID, string(prize), t.Zone)
}

// Coalesce changes into at most one edit per event per five-second cycle.
// Persist the last sent text so restarts and concurrent registrations converge.
func (b *Bot) syncTournamentAnnouncements(ctx context.Context, s *discordgo.Session, now time.Time) {
	type update struct{ ID, Channel, Message, Text string }
	var work []update
	b.tournaments.mu.Lock()
	for _, t := range b.tournaments.data.Tournaments {
		if t.AnnouncementID == "" || now.Before(t.AnnouncementRetryAt) {
			continue
		}
		text := tournamentAnnouncement(t)
		if text != t.AnnouncementText || t.Channel != t.AnnouncementChannel {
			work = append(work, update{t.ID, t.Channel, t.AnnouncementID, text})
		}
	}
	b.tournaments.mu.Unlock()
	for _, u := range work {
		if ctx.Err() != nil {
			return
		}
		b.tournaments.mu.Lock()
		t := b.tournaments.data.Tournaments[u.ID]
		moved := t != nil && t.AnnouncementChannel != u.Channel
		b.tournaments.mu.Unlock()
		var message *discordgo.Message
		var err error
		if moved {
			message, err = s.ChannelMessageSendComplex(u.Channel, &discordgo.MessageSend{Content: u.Text, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
		} else {
			message, err = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: u.Message, Channel: u.Channel, Content: &u.Text, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
		}
		var rest *discordgo.RESTError
		if err != nil && errors.As(err, &rest) && rest.Message != nil && rest.Message.Code == 10008 {
			// A deleted announcement can be replaced; missing permissions still back off.
			moved = true
			message, err = s.ChannelMessageSendComplex(u.Channel, &discordgo.MessageSend{Content: u.Text, AllowedMentions: &discordgo.MessageAllowedMentions{}}, discordgo.WithContext(ctx))
		}
		if err != nil && ctx.Err() != nil {
			return
		}
		saveErr := b.tournaments.transaction(func(d *tournamentData) error {
			t := d.Tournaments[u.ID]
			if t == nil {
				return nil
			}
			if err != nil {
				t.AnnouncementError = "Discord announcement update failed"
				t.AnnouncementRetryAt = now.Add(time.Minute)
			} else {
				if moved && message != nil {
					t.AnnouncementID = message.ID
				}
				t.AnnouncementChannel = u.Channel
				t.AnnouncementText = u.Text
				t.AnnouncementError = ""
				t.AnnouncementRetryAt = time.Time{}
			}
			return nil
		})
		if saveErr != nil {
			b.log.Error("save tournament announcement", "error", saveErr)
		}
	}
}
