// Command switchnet-discord is the Discord bot for a SwitchNet server: the
// only way to create a SwitchNet account, plus friends, avatars, who is
// online and whether it is up. See README.md.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Error("config", "error", err)
		os.Exit(1)
	}

	session, err := discordgo.New("Bot " + cfg.Discord.Token)
	if err != nil {
		log.Error("discord session", "error", err)
		os.Exit(1)
	}
	// Slash commands need no privileged intents: guild events only.
	session.Identify.Intents = discordgo.IntentsGuilds

	bot := &Bot{
		cfg:      cfg,
		api:      NewSwitchNet(cfg.SwitchNet.APIURL, cfg.SwitchNet.APIToken),
		log:      log,
		lastMint: map[string]time.Time{},
	}
	session.AddHandler(bot.onInteraction)
	registerIn := func(s *discordgo.Session, guild string) {
		if !slices.Contains(cfg.Discord.GuildIDs, guild) {
			return
		}
		// Per server, not global: guild commands update instantly, and the
		// bot is only for the servers it is configured for.
		if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guild, commands); err != nil {
			log.Error("register commands (is the bot invited with applications.commands?)", "guild", guild, "error", err)
			return
		}
		log.Info("commands registered", "guild", guild, "count", len(commands))
	}
	ready := make(chan struct{}, 1)
	notifyReady := func() {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	session.AddHandler(func(s *discordgo.Session, _ *discordgo.Resumed) { notifyReady() })
	session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		notifyReady()
		log.Info("connected", "as", r.User.Username)
	})
	// GuildCreate arrives for every server the bot is in once connected, and
	// again the moment it is invited to a new one, so an invite needs no
	// restart.
	session.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		registerIn(s, g.ID)
	})

	if err := session.Open(); err != nil {
		log.Error("connect to discord", "error", err)
		os.Exit(1)
	}
	defer session.Close()
	activityCtx, cancelActivity := context.WithCancel(context.Background())
	defer cancelActivity()
	go runNetworkActivity(activityCtx, bot.api, log, ready, func(text string) error { return session.UpdateWatchStatus(0, text) })

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
}
