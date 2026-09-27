package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SwitchNet is a client for SwitchNet's bot API (internal/baas/botapi.go in
// n-popescu/grid0plus). The bot never touches SwitchNet's database: every
// account change goes through that API, so its rules live in one place.
type SwitchNet struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewSwitchNet(baseURL, token string) *SwitchNet {
	return &SwitchNet{baseURL: baseURL, token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// APIError is a refusal from SwitchNet. Code is stable; see botapi.go.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("switchnet: %d %s: %s", e.Status, e.Code, e.Message)
}

// IsCode reports whether err is a SwitchNet refusal with that code.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// do sends one request as discordID ("" for calls that name nobody) and
// decodes the JSON answer into out, if out is not nil.
func (c *SwitchNet) do(ctx context.Context, method, path, discordID, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if discordID != "" {
		req.Header.Set("X-Discord-User-ID", discordID)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("switchnet unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(raw, apiErr) != nil || apiErr.Code == "" {
			apiErr.Code, apiErr.Message = "http_error", string(raw)
		}
		return apiErr
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *SwitchNet) doJSON(ctx context.Context, method, path, discordID string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	return c.do(ctx, method, path, discordID, "application/json", body, out)
}

type Credentials struct {
	UserID     string `json:"userId"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	FriendCode string `json:"friendCode"`
}

type User struct {
	UserID        string `json:"userId"`
	DiscordUserID string `json:"discordUserId"`
	Username      string `json:"username"`
	Nickname      string `json:"nickname"`
	FriendCode    string `json:"friendCode"`
	Presence      string `json:"presence"`
	AppID         string `json:"appId"`
}

type Me struct {
	User
	Consoles     int       `json:"consoles"`
	Friends      int       `json:"friends"`
	RegisteredAt time.Time `json:"registeredAt"`
}

type FriendRequest struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Other     User      `json:"other"`
	CreatedAt time.Time `json:"createdAt"`
}

type ServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Status struct {
	Services []ServiceStatus `json:"services"`
	Online   int             `json:"online"`
	Players  int             `json:"players"`
}

func (c *SwitchNet) Register(ctx context.Context, discordID, username, displayName string) (Credentials, error) {
	var out Credentials
	err := c.doJSON(ctx, "POST", "/bot/v1/register", "", map[string]string{
		"discordUserId": discordID, "username": username, "displayName": displayName,
	}, &out)
	return out, err
}

// Link claims an account made before the bot, by its login and password.
func (c *SwitchNet) Link(ctx context.Context, discordID, username, password string) (User, error) {
	var out User
	err := c.doJSON(ctx, "POST", "/bot/v1/link", "", map[string]string{
		"discordUserId": discordID, "username": username, "password": password,
	}, &out)
	return out, err
}

func (c *SwitchNet) ResetPassword(ctx context.Context, discordID string) (Credentials, error) {
	var out Credentials
	err := c.doJSON(ctx, "POST", "/bot/v1/reset-password", discordID, nil, &out)
	return out, err
}

func (c *SwitchNet) Me(ctx context.Context, discordID string) (Me, error) {
	var out Me
	err := c.doJSON(ctx, "GET", "/bot/v1/me", discordID, nil, &out)
	return out, err
}

func (c *SwitchNet) SetAvatar(ctx context.Context, discordID string, jpeg []byte) error {
	return c.do(ctx, "PUT", "/bot/v1/avatar", discordID, "image/jpeg", bytes.NewReader(jpeg), nil)
}

func (c *SwitchNet) Friends(ctx context.Context, discordID string) ([]User, error) {
	var out struct {
		Friends []User `json:"friends"`
	}
	err := c.doJSON(ctx, "GET", "/bot/v1/friends", discordID, nil, &out)
	return out.Friends, err
}

func (c *SwitchNet) RemoveFriend(ctx context.Context, discordID, userID string) error {
	return c.doJSON(ctx, "DELETE", "/bot/v1/friends/"+userID, discordID, nil, nil)
}

func (c *SwitchNet) FriendRequests(ctx context.Context, discordID string) (incoming, outgoing []FriendRequest, err error) {
	var out struct {
		Incoming []FriendRequest `json:"incoming"`
		Outgoing []FriendRequest `json:"outgoing"`
	}
	err = c.doJSON(ctx, "GET", "/bot/v1/friend-requests", discordID, nil, &out)
	return out.Incoming, out.Outgoing, err
}

// SendFriendRequest names the target by Discord id or by friend code.
// Result is "sent", or "became_friends" when it crossed their request.
func (c *SwitchNet) SendFriendRequest(ctx context.Context, discordID, targetDiscordID, friendCode string) (result string, other User, err error) {
	var out struct {
		Result string `json:"result"`
		Other  User   `json:"other"`
	}
	err = c.doJSON(ctx, "POST", "/bot/v1/friend-requests", discordID, map[string]string{
		"discordUserId": targetDiscordID, "friendCode": friendCode,
	}, &out)
	return out.Result, out.Other, err
}

// SettleFriendRequest is accept, deny or cancel.
func (c *SwitchNet) SettleFriendRequest(ctx context.Context, discordID, requestID, action string) error {
	return c.doJSON(ctx, "POST", "/bot/v1/friend-requests/"+requestID+"/"+action, discordID, nil, nil)
}

func (c *SwitchNet) LookupDiscord(ctx context.Context, discordID string) (User, error) {
	var out User
	err := c.doJSON(ctx, "GET", "/bot/v1/users/by-discord/"+discordID, "", nil, &out)
	return out, err
}

func (c *SwitchNet) Online(ctx context.Context) ([]User, error) {
	var out struct {
		Users []User `json:"users"`
	}
	err := c.doJSON(ctx, "GET", "/bot/v1/online", "", nil, &out)
	return out.Users, err
}

func (c *SwitchNet) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.doJSON(ctx, "GET", "/bot/v1/status", "", nil, &out)
	return out, err
}
