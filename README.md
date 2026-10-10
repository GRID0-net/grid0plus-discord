# grid0plus-discord

The Discord bot for a [GRID0+](https://github.com/n-popescu/grid0plus) server.
It is **the only way to create a GRID0+ account**: a person runs `/register`
in the community's Discord and gets a login and password to type into their
Switch's "Link Nintendo Account" screen.

## Why a bot

A GRID0+ account used to come from a public web form, and every console
also left behind a throwaway account when it linked. Discord is where the
group already is. It knows who each person is, so a GRID0+ account belongs to
the Discord account that made it, a password nobody has to invent, and a place
for the rest of the account chores (friends, avatar, "who's on").

## Commands

| Command | What it does | Who sees the answer |
|---|---|---|
| `/register` | Creates your account and shows your login (your Discord username) and a generated 8-character password. Copies your Discord avatar to your Switch profile. | only you |
| `/register username:alice.psn` | An **extra** account, with a login you choose, beside the first one. Repeat it for as many as you want. | only you |
| `/link` | For an account made before the bot: enter its login and password in a private form to connect it to your Discord. Works for a second account too. | only you |
| `/reset-password` | A new password. The old one stops working. `disconnect_all_accounts` defaults to true; set it to false to keep linked consoles signed in. | only you |
| `/reset-password username:…` | A new password for that one of your accounts instead of your first (autocompletes from your own logins). | only you |
| `/splatoon-save-wipe` | Asks to confirm, then deletes your Splatoon 3 cloud save on GRID0+ (archived on the server first). For after you put a different save on your Switch: the game plays from the console's save and uploads only changes, so the old cloud copy would otherwise mix with the new one. `username:` picks another of your accounts. | only you |
| `/whoami` | Login, friend code, linked consoles, friends, status | only you |
| `/fc` | Your friend code | only you |
| `/fc-add user:@someone` or `code:SW-…` | Sends a friend request. If they already asked you, you become friends. | only you |
| `/fc-pending` | Requests waiting for you (Accept / Decline buttons) and the ones you sent (Cancel) | only you |
| `/fc-remove friend:` | Removes a friend (autocompletes from your list) | only you |
| `/friends` | Your friends and what they're doing | only you |
| `/avatar` | Re-copies your Discord avatar to your Switch profile picture | only you |
| `/online` | Everyone on GRID0+ right now, except people whose Switch presence setting is "only me" | the channel |
| `/status` | Whether each GRID0+ service is up, and how many people are on | the channel |

Passwords are never logged or stored by the bot. GRID0+ keeps only a
bcrypt hash, so nobody can read one back later, the admin included.

## More than one account

One Discord account can own several GRID0+ accounts: `/register username:…`
for each extra, `/link` for accounts that predate the bot. The first one you
made is your **first account** — that is what `/whoami`, `/fc`, `/friends` and a
bare `/reset-password` act on — and the rest are reachable by naming their login.

Naming a login on `/reset-password` can only ever reach your own: GRID0+ resolves
it through the accounts your Discord account owns, and answers the same way for
a login that is somebody else's as for one that does not exist, so no login's
existence is readable by asking.

## How it works

The bot is a thin client. It never touches GRID0+'s database; every action
is a call to GRID0+'s **bot API** (`internal/baas/botapi.go` in the
GRID0+ repo), a loopback-only listener inside `baas` that shares its
rules with the console's own endpoints. Each call carries:

- `Authorization: Bearer <baas.bot_api.token>`, which proves it's the bot;
- `X-Discord-User-ID`, the person, taken from Discord's interaction payload
  and never from anything they typed.

An unregistered Discord id gets "not registered", never someone else's
account.

## Setup

1. In the [Discord developer portal](https://discord.com/developers/applications),
   create an application and a bot, and copy the bot token. No privileged
   intents are needed.
2. Invite it with the `bot` and `applications.commands` scopes:
   `https://discord.com/oauth2/authorize?client_id=<application id>&scope=bot+applications.commands&permissions=0`
3. On the GRID0+ host, set `baas.bot_api` in GRID0+'s `config.yaml`
   (`listen: "127.0.0.1:8096"`, `token:` from `openssl rand -hex 32`) and
   restart `switchnet-baas`.
4. Build and install the bot:

   ```sh
   go build -o switchnet-discord .
   sudo mkdir -p /opt/switchnet-discord
   sudo cp switchnet-discord config.example.yaml /opt/switchnet-discord/
   sudo cp /opt/switchnet-discord/config.example.yaml /opt/switchnet-discord/config.yaml
   # edit guild_ids (and api_url if you changed the port)
   printf 'SWITCHNET_DISCORD_TOKEN=%s\nSWITCHNET_BOT_API_TOKEN=%s\n' '<bot token>' '<bot_api token>' \
     | sudo tee /etc/switchnet-discord.env >/dev/null
   sudo chmod 600 /etc/switchnet-discord.env
   sudo cp deploy/switchnet-discord.service /etc/systemd/system/
   sudo systemctl daemon-reload && sudo systemctl enable --now switchnet-discord
   ```

   The commands appear in the configured servers as soon as the bot connects.

## Updating

`deploy/deploy.sh [ssh-host]` builds for the host's architecture, replaces the
binary and restarts the service.

## Development

```sh
go test ./...
go vet ./...
```

`SWITCHNET_DISCORD_TOKEN` and `SWITCHNET_BOT_API_TOKEN` override the file, so
a local `config.yaml` never needs to hold a secret.
