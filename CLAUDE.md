# CLAUDE.md

Notes for an agent working on this repository. `README.md` is for the
operator; this is for you.

## What this is

The Discord front end for SwitchNet accounts. It is deliberately thin: every
rule (who may register, friend-request rules, identity) lives in SwitchNet's
bot API, `internal/baas/botapi.go` in `n-popescu/switchnet`. If a behaviour
change is about *accounts*, it almost certainly belongs there, with a test
there, not here.

## Rules

1. **Never commit a secret.** The Discord bot token and the bot API token come
   from the environment (`SWITCHNET_DISCORD_TOKEN`, `SWITCHNET_BOT_API_TOKEN`)
   or an untracked `config.yaml`. `.gitignore` covers both; keep it that way.
2. **Never log or echo a password** except in the ephemeral reply to the
   person it belongs to. Credential replies are ephemeral; `commandsArePublic`
   lists the only two commands that answer in the channel.
3. **The caller is `i.Member.User.ID` from Discord's payload, always.** Never
   let a command option name "who I am". Targets (`/fc-add user:`) are
   fine; the actor is not.
4. **Guild-locked.** `onInteraction` ignores DMs and servers not in
   `discord.guild_ids`. Don't remove that check to "make DMs work".

## Adding a command

1. Add it to `commands` and `handlers` in `commands.go`.
2. If it needs data SwitchNet doesn't expose yet, add the endpoint to
   `botapi.go` in SwitchNet first (with a test in `botapi_test.go`), then a
   method in `switchnet.go` here.
3. Map any new SwitchNet error code to a sentence in `explain`.

Commands are registered per guild on every connect (bulk overwrite), so a
removed command disappears on the next restart.

## Commits

Plain declarative sentences, no `feat:` tags. Commit directly to `main`.
