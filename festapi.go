package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// The Splatfest half of GRID0+'s bot API (internal/baas/botfest.go). Every
// rule -- first pick final, who may submit what, what counts -- lives there;
// this is only the client.

// FestTeam is one side of a fest. Team is GRID0+'s name for it (Alpha, Bravo,
// Charlie), in the order the fest lists them.
type FestTeam struct {
	Team   string `json:"team"`
	Name   string `json:"name"`
	Emoji  string `json:"emoji"`
	RoleID string `json:"roleId"`
}

type FestTimetable struct {
	Open  time.Time `json:"open"`
	Start time.Time `json:"start"`
	Mid   time.Time `json:"mid"`
	End   time.Time `json:"end"`
	Close time.Time `json:"close"`
}

// Fest is a fest with a vote post.
type Fest struct {
	FestID           string               `json:"festId"`
	DiscordOnly      bool                 `json:"discordOnly"`
	Title            string               `json:"title"`
	Description      string               `json:"description"`
	Teams            []FestTeam           `json:"teams"`
	ChannelID        string               `json:"channelId"`
	MessageID        string               `json:"messageId"`
	DefaultWinPaint  int64                `json:"defaultWinPaint"`
	DefaultLossPaint int64                `json:"defaultLossPaint"`
	Posted           map[string]time.Time `json:"posted"`
	CreatedBy        string               `json:"createdBy"`
	Phase            string               `json:"phase"`
	Timetable        FestTimetable        `json:"timetable"`
}

// team returns the side by GRID0+'s name for it.
func (f Fest) team(name string) (FestTeam, bool) {
	for _, t := range f.Teams {
		if t.Team == name {
			return t, true
		}
	}
	return FestTeam{}, false
}

// over reports whether play has ended, by the clock rather than the phase
// GRID0+ computed when the list was read.
func (f Fest) over(now time.Time) bool { return !now.Before(f.Timetable.End) }

// CreateFest is what /fest create sends. FestID empty makes a Discord-only
// fest, whose StartAt/EndAt are its play window.
type CreateFest struct {
	FestID           string     `json:"festId,omitempty"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	StartAt          time.Time  `json:"startAt,omitzero"`
	EndAt            time.Time  `json:"endAt,omitzero"`
	Teams            []FestTeam `json:"teams"`
	ChannelID        string     `json:"channelId"`
	DefaultWinPaint  int64      `json:"defaultWinPaint,omitempty"`
	DefaultLossPaint int64      `json:"defaultLossPaint,omitempty"`
}

type FestEntry struct {
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId"`
	Team          string `json:"team"`
}

type SubmissionPlayer struct {
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId"`
	Team          string `json:"team"`
	Paint         *int64 `json:"paint,omitempty"`
}

type SubmissionFlag struct {
	DiscordUserID string    `json:"discordUserId"`
	Reason        string    `json:"reason"`
	At            time.Time `json:"at"`
}

// Submission is a LAN game waiting for, or past, a mod's review.
type Submission struct {
	ID                  string             `json:"id"`
	FestID              string             `json:"festId"`
	SubmittedBy         string             `json:"submittedBy"`
	WinningTeam         string             `json:"winningTeam"`
	LosingTeam          string             `json:"losingTeam"`
	Players             []SubmissionPlayer `json:"players"`
	ScreenshotURL       string             `json:"screenshotUrl"`
	Status              string             `json:"status"`
	Reason              string             `json:"reason"`
	ReviewedBy          string             `json:"reviewedBy"`
	Flags               []SubmissionFlag   `json:"flags"`
	ReviewChannelID     string             `json:"reviewChannelId"`
	ReviewMessageID     string             `json:"reviewMessageId"`
	CreatedAt           time.Time          `json:"createdAt"`
	PossibleDuplicateOf []string           `json:"possibleDuplicateOf"`
}

type SubmitGame struct {
	Winners       []string         `json:"winners"`
	Losers        []string         `json:"losers"`
	Paint         map[string]int64 `json:"paint,omitempty"`
	ScreenshotURL string           `json:"screenshotUrl,omitempty"`
}

type FestTally struct {
	Games        int   `json:"games"`
	Wins         int   `json:"wins"`
	Contribution int64 `json:"contribution"`
}

// FestRecord is a player's own record in one fest. Never a team total.
type FestRecord struct {
	Team        string       `json:"team"`
	Online      FestTally    `json:"online"`
	LAN         FestTally    `json:"lan"`
	Submissions []Submission `json:"submissions"`
}

type TeamShare struct {
	Team  string  `json:"team"`
	Ratio float64 `json:"ratio"`
}

type TeamPoints struct {
	Team   string `json:"team"`
	Points int64  `json:"points"`
}

// Standings is what the game would show now, and GRID0+ sends nothing more:
// no Midterm before the second half, no Final before the results.
type Standings struct {
	Phase   string      `json:"phase"`
	Midterm []TeamShare `json:"midterm"`
	Final   *struct {
		Categories map[string][]TeamShare `json:"categories"`
		Points     []TeamPoints           `json:"points"`
		Winner     string                 `json:"winner"`
	} `json:"final"`
}

func (c *SwitchNet) Fests(ctx context.Context) ([]Fest, error) {
	var out struct {
		Fests []Fest `json:"fests"`
	}
	err := c.doJSON(ctx, "GET", "/bot/v1/fests", "", nil, &out)
	return out.Fests, err
}

func (c *SwitchNet) Fest(ctx context.Context, id string) (Fest, error) {
	var out Fest
	err := c.doJSON(ctx, "GET", "/bot/v1/fests/"+id, "", nil, &out)
	return out, err
}

// CreateFest is a mod's /fest create; modID is recorded as its creator.
func (c *SwitchNet) CreateFest(ctx context.Context, modID string, in CreateFest) (Fest, error) {
	var out Fest
	err := c.doJSON(ctx, "POST", "/bot/v1/fests", modID, in, &out)
	return out, err
}

// SetFestPost records where the vote post is.
func (c *SwitchNet) SetFestPost(ctx context.Context, id, channelID, messageID string) error {
	return c.doJSON(ctx, "PATCH", "/bot/v1/fests/"+id, "", map[string]string{
		"channelId": channelID, "messageId": messageID,
	}, nil)
}

func (c *SwitchNet) DeleteFest(ctx context.Context, id string) error {
	return c.doJSON(ctx, "DELETE", "/bot/v1/fests/"+id, "", nil, nil)
}

// ClaimFestPost reports whether this bot is the one to make an announcement.
// False means it was already made.
func (c *SwitchNet) ClaimFestPost(ctx context.Context, id, kind string) (bool, error) {
	var out struct {
		Claimed bool `json:"claimed"`
	}
	err := c.doJSON(ctx, "POST", "/bot/v1/fests/"+id+"/posts/"+kind, "", nil, &out)
	return out.Claimed, err
}

func (c *SwitchNet) FestStandings(ctx context.Context, id string) (Standings, error) {
	var out Standings
	err := c.doJSON(ctx, "GET", "/bot/v1/fests/"+id+"/standings", "", nil, &out)
	return out, err
}

// FestEntries lists the votes that belong to a Discord account's main
// account, for roles.
func (c *SwitchNet) FestEntries(ctx context.Context, id string) ([]FestEntry, error) {
	var out struct {
		Entries []FestEntry `json:"entries"`
	}
	err := c.doJSON(ctx, "GET", "/bot/v1/fests/"+id+"/entries", "", nil, &out)
	return out.Entries, err
}

// EnterFest votes for a team. The first pick is final: team is the one that
// stands, and created is false when it was already picked.
func (c *SwitchNet) EnterFest(ctx context.Context, discordID, festID, team string) (string, bool, error) {
	var out struct {
		Team    string `json:"team"`
		Created bool   `json:"created"`
	}
	err := c.doJSON(ctx, "POST", "/bot/v1/fests/"+festID+"/entry", discordID, map[string]string{"team": team}, &out)
	return out.Team, out.Created, err
}

func (c *SwitchNet) FestRecord(ctx context.Context, discordID, festID string) (FestRecord, error) {
	var out FestRecord
	err := c.doJSON(ctx, "GET", "/bot/v1/fests/"+festID+"/me", discordID, nil, &out)
	return out, err
}

func (c *SwitchNet) SubmitGame(ctx context.Context, discordID, festID string, in SubmitGame) (Submission, error) {
	var out Submission
	err := c.doJSON(ctx, "POST", "/bot/v1/fests/"+festID+"/submissions", discordID, in, &out)
	return out, err
}

func (c *SwitchNet) Submission(ctx context.Context, id string) (Submission, error) {
	var out Submission
	err := c.doJSON(ctx, "GET", "/bot/v1/submissions/"+id, "", nil, &out)
	return out, err
}

func (c *SwitchNet) SetSubmissionMessage(ctx context.Context, id, channelID, messageID string) error {
	return c.doJSON(ctx, "PUT", "/bot/v1/submissions/"+id+"/message", "", map[string]string{
		"channelId": channelID, "messageId": messageID,
	}, nil)
}

func (c *SwitchNet) FlagSubmission(ctx context.Context, discordID, id, reason string) (Submission, error) {
	var out Submission
	err := c.doJSON(ctx, "POST", "/bot/v1/submissions/"+id+"/flag", discordID, map[string]string{"reason": reason}, &out)
	return out, err
}

func (c *SwitchNet) AcceptSubmission(ctx context.Context, modID, id string) (Submission, error) {
	var out Submission
	err := c.doJSON(ctx, "POST", "/bot/v1/submissions/"+id+"/accept", modID, nil, &out)
	return out, err
}

func (c *SwitchNet) RefuseSubmission(ctx context.Context, modID, id, reason string) (Submission, error) {
	var out Submission
	err := c.doJSON(ctx, "POST", "/bot/v1/submissions/"+id+"/refuse", modID, map[string]string{"reason": reason}, &out)
	return out, err
}

// FestEvent is one line of GRID0+'s fest event stream.
type FestEvent struct {
	Type          string `json:"type"`
	FestID        string `json:"festId"`
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId"`
	Team          string `json:"team"`
}

// festStreamIdle is how long the stream may go without a line before the bot
// takes it as dead. GRID0+ sends a heartbeat every 30 s.
const festStreamIdle = 75 * time.Second

// FestEvents holds GRID0+'s fest event stream open and hands each event to
// on, until the stream ends, goes quiet, or ctx is cancelled. connected is
// called once the stream is up, before any event.
//
// Its own HTTP client: the API client's timeout would end a stream fifteen
// seconds in.
func (c *SwitchNet) FestEvents(ctx context.Context, connected func(), on func(FestEvent)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/bot/v1/fest-events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("grid0+ unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.NewDecoder(resp.Body).Decode(apiErr)
		return apiErr
	}

	lines := make(chan []byte)
	go func() {
		defer close(lines)
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			select {
			case lines <- append([]byte(nil), scan.Bytes()...):
			case <-ctx.Done():
				return
			}
		}
	}()
	idle := time.NewTimer(festStreamIdle)
	defer idle.Stop()
	first := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle.C:
			return fmt.Errorf("fest event stream quiet for %s", festStreamIdle)
		case line, ok := <-lines:
			if !ok {
				return fmt.Errorf("fest event stream ended")
			}
			idle.Reset(festStreamIdle)
			var e FestEvent
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			if first {
				first = false
				connected()
			}
			if e.Type == "fest_entry" {
				on(e)
			}
		}
	}
}
