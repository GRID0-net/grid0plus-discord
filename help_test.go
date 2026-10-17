package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func helpText(e *discordgo.MessageEmbed) string {
	var sb strings.Builder
	sb.WriteString(e.Title + e.Description)
	for _, f := range e.Fields {
		sb.WriteString(f.Name + f.Value)
	}
	return sb.String()
}

// A member who is not a mod never learns a mod command from the bot.
func TestHelpShowsModCommandsOnlyToMods(t *testing.T) {
	b := &Bot{}
	b.cfg.Fest.ModRoleIDs = []string{"mod"}
	b.cfg.Fest.ReviewChannelID = "review"
	player := &discordgo.Member{Roles: []string{"other"}}
	mod := &discordgo.Member{Roles: []string{"mod"}}

	for _, key := range []string{"", "fest", "salmon", "fest-mod", "salmon-mod"} {
		if text := helpText(b.helpEmbed(key, player)); strings.Contains(text, "-mod") {
			t.Errorf("help %q for a player mentions a mod command: %s", key, text)
		}
	}
	if text := helpText(b.helpEmbed("", mod)); !strings.Contains(text, "/fest-mod create") {
		t.Errorf("help for a mod lacks /fest-mod: %s", text)
	}
	if text := helpText(b.helpEmbed("salmon-mod", mod)); !strings.Contains(text, "Review queue") {
		t.Errorf("salmon-mod help for a mod: %s", text)
	}
	for _, c := range helpCommand().Options[0].Choices {
		if strings.HasSuffix(c.Value.(string), "-mod") {
			t.Errorf("/help's picker, which everyone sees, offers %v", c.Value)
		}
	}
}
