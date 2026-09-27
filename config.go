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
	return cfg, errors.Join(errs...)
}
