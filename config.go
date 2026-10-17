package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the bot's configuration. Secrets can come from the environment
// instead of the file, so the file never has to hold them.
type Config struct {
	Discord struct {
		// Token is the bot token (SWITCHNET_DISCORD_TOKEN).
		Token string `yaml:"token"`
		// GuildIDs are the servers the bot answers in. Commands are
		// registered per server, and an interaction from anywhere else is
		// refused.
		GuildIDs []string `yaml:"guild_ids"`
		// RegisterRoleID, if set, is the role a member needs to /register.
		RegisterRoleID string `yaml:"register_role_id"`
	} `yaml:"discord"`
	SwitchNet struct {
		// APIURL is SwitchNet's bot API, baas.bot_api.listen on the same host.
		APIURL string `yaml:"api_url"`
		// APIToken is baas.bot_api.token (SWITCHNET_BOT_API_TOKEN).
		APIToken string `yaml:"api_token"`
	} `yaml:"switchnet"`
	Dashboard struct {
		// URL is the dashboard's public address, without a trailing slash.
		// Empty disables /dashboard.
		URL string `yaml:"url"`
		// Token is GRID0+'s dashboard.token (SWITCHNET_DASHBOARD_TOKEN). The
		// bot signs short-lived login links with it and never shows it.
		Token string `yaml:"token"`
		// AdminRoleID is the role a member needs for /dashboard. Required
		// when URL is set: a login link opens the Splatfest controls.
		AdminRoleID string `yaml:"admin_role_id"`
	} `yaml:"dashboard"`
	Fest struct {
		// ModRoleIDs are the roles that may create a fest's vote post and
		// review LAN games. Empty turns the fest commands off.
		ModRoleIDs []string `yaml:"mod_role_ids"`
		// ChannelID is where a vote post and its announcements go when /fest
		// create names no channel. Empty: the channel the command is run in.
		ChannelID string `yaml:"channel_id"`
		// ReviewChannelID is the mods' queue of submitted LAN games.
		ReviewChannelID string `yaml:"review_channel_id"`
	} `yaml:"fest"`
	Alerts struct {
		// ChannelID is where the bot says a GRID0+ service went down or came
		// back. Empty turns the alerts off.
		ChannelID string `yaml:"channel_id"`
		// PingRoleID is pinged when something goes down. Optional.
		PingRoleID string `yaml:"ping_role_id"`
		// IntervalSeconds is how often GRID0+'s health is read (default 60).
		IntervalSeconds int `yaml:"interval_seconds"`
		// DownAfter is how many reads in a row a service must fail before it
		// is reported (default 3): a deploy restarts every service, and that
		// is not an outage worth a ping.
		DownAfter int `yaml:"down_after"`
	} `yaml:"alerts"`
}

// festEnabled reports whether the fest commands are configured.
func (c Config) festEnabled() bool {
	return len(c.Fest.ModRoleIDs) > 0 && c.Fest.ReviewChannelID != ""
}

func LoadConfig(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	// A misspelt key is a setting the operator thinks they changed.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if v := os.Getenv("SWITCHNET_DISCORD_TOKEN"); v != "" {
		cfg.Discord.Token = v
	}
	if v := os.Getenv("SWITCHNET_BOT_API_TOKEN"); v != "" {
		cfg.SwitchNet.APIToken = v
	}
	if v := os.Getenv("SWITCHNET_DASHBOARD_TOKEN"); v != "" {
		cfg.Dashboard.Token = v
	}

	var errs []error
	if cfg.Discord.Token == "" {
		errs = append(errs, errors.New("discord.token is required (or SWITCHNET_DISCORD_TOKEN)"))
	}
	if len(cfg.Discord.GuildIDs) == 0 {
		errs = append(errs, errors.New("discord.guild_ids needs at least one server id"))
	}
	if cfg.SwitchNet.APIURL == "" {
		errs = append(errs, errors.New("switchnet.api_url is required"))
	}
	if len(cfg.SwitchNet.APIToken) < 32 {
		errs = append(errs, errors.New("switchnet.api_token must be baas.bot_api.token (or SWITCHNET_BOT_API_TOKEN)"))
	}
	if cfg.Dashboard.URL != "" && (cfg.Dashboard.Token == "" || cfg.Dashboard.AdminRoleID == "") {
		errs = append(errs, errors.New("dashboard.url needs dashboard.token (or SWITCHNET_DASHBOARD_TOKEN) and dashboard.admin_role_id"))
	}
	if len(cfg.Fest.ModRoleIDs) > 0 && cfg.Fest.ReviewChannelID == "" {
		errs = append(errs, errors.New("fest.mod_role_ids needs fest.review_channel_id, where mods review LAN games"))
	}
	if cfg.Alerts.IntervalSeconds <= 0 {
		cfg.Alerts.IntervalSeconds = 60
	}
	if cfg.Alerts.DownAfter <= 0 {
		cfg.Alerts.DownAfter = 3
	}
	return cfg, errors.Join(errs...)
}
