# Running a GRID0+ tournament

This manager handles registration, teams, a single-elimination bracket, scheduling
and organizer-entered best-of-three results. It does not create a private room,
launch a game, verify attendance in-game, or award an in-game prize. An assigned
organizer attends and records results; the prize text describes what organizers
will award. No participant password is requested or stored.

## Enable it

Configure a durable, private state file and restart the bot:

```yaml
tournaments:
  store_file: "/var/lib/grid0plus-discord/tournaments.json"
  admin_role_ids: ["YOUR_TRUSTED_ORGANIZER_ROLE_ID"]
```

Empty `store_file` disables the feature. The directory must be writable by the
bot service user. For the supplied systemd service, add a service override:

```ini
[Service]
StateDirectory=grid0plus-discord
StateDirectoryMode=0700
```

Then reload systemd and restart the bot. No additional Discord intents are
required. It needs View Channel and Send Messages in the announcement channel;
participants should allow DMs from server members. Existing GRID0+ API settings
verify entries through `/bot/v1/me`. API failures refuse entry/format changes;
there is no account fallback or automatic registration.

`/tournament` is public. `/tournament-admin` defaults to Discord Administrators
and is also checked at runtime against Administrators or `admin_role_ids`.
To let a configured organizer role see the admin command, grant that role access
under Server Settings → Integrations → this bot → `/tournament-admin`.
Discord command permissions alone do not bypass the bot's role check. Every
organizer named at creation must be a human guild member with this access.

## Create and register

Example values (choose your own future dates and real organizer mentions):

```text
/tournament-admin create
  title: Friday Cup
  date: 2026-11-06
  registration_closes: 2026-11-05 20:00
  organizers: @Nico @OtherOrganizer
  rounds: 2026-11-06 18:00/22:00;2026-11-07 18:00/22:00;2026-11-08 18:00/22:00
  prizes: Winners get the announced prize; runners-up get a special role.
  timezone: Europe/Brussels
  slot_minutes: 45
  channel: #tournaments
```

The bot posts an announcement containing a tournament ID. Date/time input is in
the tournament timezone, default Europe/Brussels. RFC3339 values with an explicit
offset also work, especially around daylight-saving changes. Round windows are
ordered, non-overlapping and at most 12 hours long. An end written only as HH:MM
uses the start's date; use a full end date/time for a window crossing midnight.
Registration must close before round one. Each best-of-three reserves 45 minutes
by default (configurable 15–180). Prepare enough round windows: 4 teams need 2,
8 teams need 3, 16 need 4. `/tournament-admin window` can add/replace a window.

Everyone enters with their own linked GRID0+ account:

```text
/tournament participate tournament: ID timezone: America/New_York from_hour: 12 until_hour: 23
/tournament participate tournament: ID team: @Friend timezone: Europe/Brussels
```

The optional `team` argument invites one human member who already has a GRID0+
account. They receive a DM instructing them to run, **in the original server**:

```text
/tournament accept tournament: ID timezone: Europe/Brussels
/tournament accept tournament: ID accept: false
```

The recipient is not entered by the invitation itself. Acceptance registers them
and forms a reciprocal pair. Existing entrants retain their saved availability.
Only the actual recipient can accept/decline. There is one pending invitation
per person and one accepted partner. Use `/tournament leave` before the deadline
to remove an entry/invitation or dissolve an accepted team. The other partner
stays entered as a solo player and is notified. There are at most 128 entrants;
the same GRID0+ account cannot occupy two entries.

Registration uses commands rather than reactions, so account checks and partner
consent happen before teams are assigned. DMs remain notification-only: the
existing guild/DM security boundary is unchanged.

## Close, choose teams and schedule

Registration automatically closes at its deadline (normally within five seconds).
An admin can close earlier:

```text
/tournament-admin close tournament: ID
/tournament-admin format tournament: ID players_per_team: 2
```

Close recommends available formats. Format selection rechecks every entrant's
GRID0+ account before locking anything. Two or more entrants allow 1v1; an even
number of at least four allow 2v2. In 2v2, accepted partners stay together and
remaining solos are randomly paired. In 1v1, both partners become independent
entries. Teams and bracket positions are randomly seeded once, then published
with stable `T1`, `T2`, etc. IDs. Byes advance automatically when needed. Format
cannot change after the bracket is built. 3v3/4v4 and groups/double elimination
are not implemented yet.

Players can set their timezone and daily availability:

```text
/tournament availability tournament: ID timezone: Asia/Tokyo from_hour: 18 until_hour: 24
/tournament-admin organizer tournament: ID user: @Nico timezone: Europe/Brussels from_hour: 18 until_hour: 23
```

Availability defaults to noon–midnight in Europe/Brussels; the bot cannot infer a
Discord user's timezone. Whole-hour windows must satisfy `0 <= from < until <= 24`.
A night window spanning midnight is not supported; choose a window on one side
of midnight in the appropriate timezone. Set organizer hours before choosing
format. An organizer may participate too, but cannot organize a different match
while playing.

Scheduling finds the earliest slot in each round that fits **all** players and
one organizer, scanning at 15-minute intervals. At equal times it prefers the
first organizer listed at creation—put Nico first and specify his actual hours
for Brussels priority. Another organizer may cover an earlier feasible slot;
priority does not mean Nico must attend every match. Discord timestamps display
in each recipient's own timezone. Slots reserve the full best-of-three duration
and prevent player/organizer overlap, including across tournaments. This is a
feasible-slot scheduler, not an optimizer promising the mathematically best
calendar across every possible combination.

Only matches with known opponents can be scheduled. Their players and organizer
get DMs and the channel gets a schedule post. Later rounds are scheduled when
results establish their opponents. An impossible match remains `waiting`, shown
by status, rather than assigning an unavailable person. Fix availability or
extend a round window, then retry:

```text
/tournament-admin schedule tournament: ID
/tournament-admin unschedule tournament: ID match: ID-M1 reason: Player unavailable
/tournament availability tournament: ID timezone: Europe/Brussels from_hour: 18 until_hour: 23
/tournament-admin reschedule tournament: ID match: ID-M1 start: 2026-11-06 19:00 organizer: @Nico
```

A player with a booked/live match must have it unscheduled before changing
availability. A round window cannot be moved across an existing booking;
release that booking first. Round windows can be extended after format selection.
Rescheduling checks the same availability and conflict rules as auto-scheduling.
To change an organizer's availability after format selection, unschedule their
booked matches first; the original organizer roster remains fixed.

## Play, report and correct

Read `/tournament status tournament: ID` for the overview or
`/tournament match tournament: ID match: ID-M1` for a particular match, its
players, assigned organizer, and exact next game ID. The assigned organizer
starts the match once its scheduled time arrives:

```text
/tournament-admin start tournament: ID match: ID-M1
/tournament-admin report tournament: ID match: ID-M1 game: ID-M1-G1 winner: T1 note: 3-1
/tournament-admin report tournament: ID match: ID-M1 game: ID-M1-G2 winner: T2
/tournament-admin report tournament: ID match: ID-M1 game: ID-M1-G3 winner: T1
```

`ID-M1` represents a best-of-three, `ID-M1-G1` one game. Players are already bound
to the match, so the winner is a team ID, not a repeated roster. Only its assigned
organizer may start/report. The bot refuses early starts, duplicate/out-of-order
game IDs, invalid winning teams, and additional reports after a side reaches two
wins. It also prevents overlapping live matches inside the event if an earlier
series overruns. It does not automatically record timeouts or manufacture losses.
Results advance the bracket; the final publishes winners and the prize text.

Recovery commands:

- `undo tournament: ID match: ID-M1 reason: Wrong result` removes the last game.
  Resubmit its original game ID. It releases a dependent booking and notifies
  affected players. Undo is refused once a later match has started or used that
  result; it cannot retroactively rewrite played rounds.
- `withdraw tournament: ID user: @Player reason: Cannot attend` removes an entry
  before format selection. Afterwards it forfeits the entrant's whole team in
  its current match with known opponents, advancing the other side. It cannot
  forfeit an already eliminated team or one still waiting for unknown opponents.
- `cancel tournament: ID reason: Rescheduled` ends the event and notifies entrants.
- `audit tournament: ID` shows recent administrator/result actions and reasons.

All these are `/tournament-admin` subcommands. Result corrections and withdrawals
are retained in the audit trail. No users or historical results are silently
deleted from a locked bracket.

## Delivery, persistence and backups

Notifications are queued durably and delivered in batches of at most ten every
five seconds, using discordgo's rate-limit handling. Idle polling does not rewrite
the file. Blocked DMs/channel failures remain visible in `/tournament status`;
`/tournament-admin retry-notifications tournament: ID` retries that event only.
Announcements do not ping arbitrary users/roles. Long posts are split safely.

Discord delivery is **at least once**: a crash between sending and saving success
can duplicate a message; a partially delivered multipart post can repeat on retry.
An item already being sent can race a cancellation, so the cancellation/correction
post remains authoritative. Pending superseded schedule posts are suppressed.
Do not mistake an admin's ephemeral "queued" acknowledgement for confirmed DM
receipt. Status is available even when DMs are closed.

State is a versioned JSON file written by private temporary-file + fsync + atomic
rename, with a process-local mutex and transactional rollback. It survives normal
bot restarts; corrupt/unsupported state stops startup rather than wiping entries.
Run **one bot process per state file**; there is no cross-process lock. Back it up
with the bot stopped. Keep the directory private and outside Git: it contains
Discord/GRID0+ IDs, availability and organizer notes. The default example file
`data/tournaments.json` is ignored by Git. No secrets/passwords are stored.
There is a 32 MiB file limit and at most 200 simultaneous unfinished events.
For a full store, stop the bot and archive complete/cancelled events and their
notices together after making a backup; don't edit live state. Server/game
redeploys do not invalidate this independent bot state.

Tests use temporary stores, a local fake GRID0+ API and an intercepted Discord
HTTP transport. They send no messages to real members or channels.
