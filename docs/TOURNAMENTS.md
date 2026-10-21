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

Then reload systemd and restart the bot. Explicit organizer user lists need no
additional privileged intents; role lookup needs the Developer Portal setting
described below. It needs View Channel and Send Messages in the announcement channel;
participants should allow DMs from server members. Existing GRID0+ API settings
verify entries through `/bot/v1/me`. API failures refuse entry/format changes;
there is no account fallback or automatic registration.

`/tournament` is public. `/tournament-admin` defaults to Discord Administrators
and is also checked at runtime against Administrators or `admin_role_ids`.
To let a configured organizer role see the admin command, grant that role access
under Server Settings → Integrations → this bot → `/tournament-admin`.
Discord command permissions alone do not bypass the bot's role check. Every
organizer named at creation must be a human guild member with this access.
You can name up to 32 organizers, using explicit users or one trusted role.

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
`rounds` is optional at creation: the date can be announced before the number of
entrants is known. With no windows, registration must close before the end of that
planned date; actual round times, once supplied, must follow the deadline. No
match time is invented. Each best-of-three reserves 45 minutes
by default (configurable 15–180). After closing registration and choosing 1v1 or 2v2, the bot derives the number of
rounds: 4 teams need 2, 8 teams need 3, 16 need 4. You can supply optional `rounds`
on `modify format`, replace the complete calendar via `modify settings rounds`,
or add windows gradually with `modify window`. Known opponents are scheduled as
each applicable round window becomes available; the rest remain visibly waiting.

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
# Optionally also provide rounds: YYYY-MM-DD HH:MM/HH:MM; ...
```

Close recommends available formats. Format selection rechecks every entrant's
GRID0+ account before locking anything. Two or more entrants allow 1v1; an even
number of at least four allow 2v2. In 2v2, accepted partners stay together and
remaining solos are randomly paired. In 1v1, both partners become independent
entries. Teams and bracket positions are randomly seeded once, then published
with stable `T1`, `T2`, etc. IDs. Byes advance automatically when needed. Format
can change before any actual match starts, using explicit `rebuild:true`; old
bookings are withdrawn and fresh match IDs are issued. Started/played brackets
cannot be rebuilt. 3v3/4v4 and groups/double elimination
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
booked matches first; the organizer roster can also be changed with `modify organizers`, as described below.

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

## Edit an existing tournament

`/tournament-admin modify` collects the controls in one group. Original top-level
commands remain shortcuts, so existing instructions keep working. All grouped
controls use exactly the same administrator, assigned-organizer and GRID0+
eligibility checks as their shortcuts.

### Select organizers from users or a role

At creation, choose **one** of:

```text
organizers: @Nico @OtherOrganizer
organizer_role: @TournamentOrganizers
```

The string `organizers: @TournamentOrganizers` also accepts a single role mention.
A role must have Administrator or be explicitly allowlisted in
`tournaments.admin_role_ids`; choosing it does not grant an ordinary role access
to tournament controls. The same Discord Integration command-access configuration
still applies to allowlisted roles.

Role lookup requires **Server Members Intent enabled in the Developer Portal**
(Bot → Privileged Gateway Intents). The bot does not subscribe to member events
or maintain a privileged gateway member cache. This REST requirement is documented
by [Discord](https://github.com/discord/discord-api-docs/blob/main/developers/resources/guild.mdx).
Failure to list all members refuses the operation; it never silently saves a
partial roster. Explicit user lists do not require this intent.

Role members are resolved across all pages, bots are excluded, and the resulting
human roster is saved (maximum 32). For a role, the creator has first scheduling
priority if included; remaining members have deterministic ID order. Explicit
user lists retain the supplied priority order. Membership is a **snapshot**:
later Discord role edits cannot silently remove an assigned organizer.

Replace or refresh the roster with:

```text
/tournament-admin modify organizers tournament: ID organizers: @Nico @OtherOrganizer
/tournament-admin modify organizers tournament: ID organizer_role: @TournamentOrganizers
```

The operation preserves existing organizers' availability. New organizers default
to noon–midnight in the tournament timezone; use `modify organizer` to customize
their hours. A booked/playing organizer cannot be removed until their matches are
rescheduled to a retained organizer or unscheduled. Keep at least one organizer.

### Details, registration and dates

```text
/tournament-admin modify view tournament: ID
/tournament-admin modify settings tournament: ID title: New title prizes: Updated prize
/tournament-admin modify settings tournament: ID date: 2026-11-13 registration_closes: 2026-11-12 20:00
/tournament-admin modify settings tournament: ID rounds: 2026-11-13 18:00/22:00;2026-11-14 18:00/22:00
/tournament-admin modify settings tournament: ID timezone: Europe/Brussels slot_minutes: 60 channel: #new-channel
/tournament-admin modify registration tournament: ID action: close
/tournament-admin modify registration tournament: ID action: open
```

Only supplied settings change. `date` moves all round windows by calendar days,
keeping their local clock times; `rounds` replaces the full list instead. They
cannot be supplied together. Changing the timezone changes how future inputs are
interpreted; it does not move existing instants or overwrite individual player
and organizer timezones.

Edits are transactional: a failed validation rolls back **all** supplied fields.
Bookings must remain inside their round windows, and bracket rounds cannot be
removed. Release bookings before shifting them outside their windows or changing
series duration. Once a series has been played, use `modify window` to extend a
future unplayed round rather than rewriting the entire calendar. A completed or
cancelled event is immutable. Changes are audited, posted to the channel, and
notified to entrants. Changing the channel also posts a move notice in the old
channel and redirects queued notifications to the new one.

Registration can reopen only before format selection, with a future deadline.
Set a new deadline first if the previous one expired. Reopening retains current
entries but cannot restore expired invitations or alter an already locked bracket.
Closing through `modify registration action:close` rechecks GRID0+ eligibility,
just like `modify close`.

### Matches and recovery

All of these are available under `/tournament-admin modify`:

| Control | Purpose |
| --- | --- |
| `close`, `format` | Close entry and choose 1v1/2v2 |
| `window` | Add/adjust a round window without displacing bookings |
| `organizer` | Edit one organizer's timezone/availability |
| `schedule`, `reschedule`, `unschedule` | Manage match bookings |
| `delay` | Move a booked series later using its match ID |
| `start`, `report` | Assigned organizer starts a series and records games |
| `undo` | Correct an unused result with an audit reason |
| `withdraw`, `cancel` | Forfeit an entrant/team or cancel the event |
| `retry-notifications`, `audit` | Recover delivery failures and inspect actions |

For a delay you only need the match ID; its tournament is inferred safely within
the command's server:

```text
/tournament-admin modify delay match: ID-M1 minutes: 15 reason: Connection issue
```

`/tournament-admin delay` is an equivalent shortcut. The bot moves the existing
booking forward by the specified minutes (1–10080); for an overdue booking it
counts from the current time. It preserves the organizer and opponents, suppresses
pending notices with the old time, and sends the new time/reason to the channel,
players and organizer. It refuses conflicts or times outside the round window:
extend that window or release/reschedule the conflicting match first. It does not
cascade changes to other matches. Only scheduled, unstarted series can be delayed;
it does **not** pause a match already running on the consoles.

## Live registration counter

The original creation announcement shows **Participants: N** and the current
registration/event state. A direct entry counts immediately; an invitation counts
its recipient only after acceptance unless that recipient had already registered.
Withdrawals before the bracket locks reduce the count. After locking, the number
records original entrants, including eliminated/forfeited teams. The existing message is edited, not reposted, with
changes coalesced into the bot's five-second delivery cycle. The Discord message
ID, channel and last rendered content persist across restarts; unchanged counts
cause no extra edit or state write. No extra Discord intent is needed.

Metadata and state changes refresh that same announcement too. After moving the
announcement channel, a replacement announcement in the new channel becomes the
tracked message. A deleted tracked announcement is recreated automatically. Other failed edits
are visible through `/tournament status`, back off
for a minute, and retry; `modify retry-notifications` can reset the backoff.
Long prize descriptions are shortened in the announcement so the counter stays
in one editable message; `/tournament status` includes the full prize text.

### Every creation option remains editable

All creation fields have a corresponding control: `modify settings` edits title,
prizes, date, timezone, registration deadline, rounds, match duration and channel;
`modify organizers` replaces user lists or role snapshots; `modify organizer`
adjusts individual organizer availability. Booking/result checks still apply.

To change an already chosen format before **any** actual match starts:

```text
/tournament-admin modify format tournament: ID players_per_team: 1 rebuild: true
```

Optional `rounds` can be supplied in the same command. The operation rechecks
GRID0+ eligibility, keeps accepted partnership records for a future 2v2 format,
withdraws old bookings, republishes teams, and notifies affected players and
organizers. New match IDs are never reused from the old bracket, so stale result
commands cannot accidentally target different opponents. Automatic byes alone
do not prevent rebuilding; a started match, reported game or recorded forfeit
does. Failure rolls back the entire change, including notifications.
