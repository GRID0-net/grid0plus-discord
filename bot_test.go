package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The client must name the caller in X-Discord-User-ID and send the token;
// SwitchNet refuses anything else.
func TestTheClientSendsTheTokenAndTheCaller(t *testing.T) {
	var gotAuth, gotCaller string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCaller = r.Header.Get("Authorization"), r.Header.Get("X-Discord-User-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{"userId": "abc", "friendCode": "1111-2222-3333", "consoles": 1})
	}))
	defer srv.Close()

	me, err := NewSwitchNet(srv.URL, "secret").Me(t.Context(), "123456789012345678")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret" || gotCaller != "123456789012345678" {
		t.Errorf("headers: auth %q caller %q", gotAuth, gotCaller)
	}
	if me.UserID != "abc" || me.Consoles != 1 {
		t.Errorf("decoded %+v", me)
	}
}

// A reset names which of the caller's own accounts it is for, and sends the
// disconnect choice with it. The empty login is the account they registered
// first, and has to go on the wire as an empty string rather than being left
// out, or "no account named" would stop being distinguishable downstream.
func TestResetPasswordSendsTheLoginAndTheDisconnectChoice(t *testing.T) {
	var logins []string
	var choices []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot/v1/reset-password" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req struct {
			Username              string `json:"username"`
			DisconnectAllAccounts bool   `json:"disconnectAllAccounts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		logins = append(logins, req.Username)
		choices = append(choices, req.DisconnectAllAccounts)
		_ = json.NewEncoder(w).Encode(map[string]string{"password": "new-password"})
	}))
	defer srv.Close()
	client := NewSwitchNet(srv.URL, "secret")
	for _, tc := range []struct {
		login      string
		disconnect bool
	}{{"", false}, {"alice.psn", true}} {
		creds, err := client.ResetPassword(t.Context(), "123456789012345678", tc.login, tc.disconnect)
		if err != nil || creds.Password != "new-password" {
			t.Fatalf("reset %q: %+v, %v", tc.login, creds, err)
		}
	}
	if !bytes.Equal([]byte{boolByte(choices[0]), boolByte(choices[1])}, []byte{0, 1}) {
		t.Errorf("disconnect choices = %v", choices)
	}
	if logins[0] != "" || logins[1] != "alice.psn" {
		t.Errorf("logins sent = %q, want the named one and an empty one", logins)
	}
}

// Registering a second account is only allowed when the person named the login
// themselves, so the flag has to reach GRID0+ rather than being inferred there
// from a login that happens to differ.
func TestRegisterSaysWhenAnExtraAccountWasAskedFor(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests = append(requests, req)
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice", "password": "pw"})
	}))
	defer srv.Close()
	client := NewSwitchNet(srv.URL, "secret")
	for _, tc := range []struct {
		login      string
		additional bool
	}{{"alice", false}, {"alice.psn", true}} {
		if _, err := client.Register(t.Context(), "123456789012345678", tc.login, "Alice", tc.additional); err != nil {
			t.Fatalf("register %q: %v", tc.login, err)
		}
	}
	if requests[0]["additional"] != false || requests[1]["additional"] != true {
		t.Errorf("additional flags = %v, %v", requests[0]["additional"], requests[1]["additional"])
	}
	if requests[1]["username"] != "alice.psn" {
		t.Errorf("second register username = %v", requests[1]["username"])
	}
}

// The list /reset-password autocompletes is the caller's own accounts, and it
// is asked for as the caller: a list of anybody else's logins would be a leak
// the reset itself refuses to be.
func TestAccountsAsksForTheCallersOwn(t *testing.T) {
	var gotCaller string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCaller = r.Header.Get("X-Discord-User-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []Account{
			{UserID: "u1", Username: "alice", Primary: true},
			{UserID: "u2", Username: "alice.psn"},
		}})
	}))
	defer srv.Close()
	accounts, err := NewSwitchNet(srv.URL, "secret").Accounts(t.Context(), "123456789012345678")
	if err != nil {
		t.Fatal(err)
	}
	if gotCaller != "123456789012345678" {
		t.Errorf("caller = %q", gotCaller)
	}
	if len(accounts) != 2 || accounts[0].Username != "alice" || !accounts[0].Primary || accounts[1].Primary {
		t.Errorf("accounts = %+v", accounts)
	}
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func TestARefusalKeepsItsCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_registered", "message": "x"})
	}))
	defer srv.Close()

	_, err := NewSwitchNet(srv.URL, "secret").Me(t.Context(), "123456789012345678")
	if !IsCode(err, "not_registered") {
		t.Errorf("err = %v, want not_registered", err)
	}
}

func TestFriendCodesAreShownTheWayTheSwitchShowsThem(t *testing.T) {
	for in, want := range map[string]string{
		"1111-2222-3333":    "SW-1111-2222-3333",
		"111122223333":      "SW-1111-2222-3333",
		"SW-1111-2222-3333": "SW-1111-2222-3333",
		"12":                "12",
	} {
		if got := formatFriendCode(in); got != want {
			t.Errorf("formatFriendCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPresenceWords(t *testing.T) {
	cases := map[string]User{
		"playing Splatoon 3": {Presence: "PLAYING", AppID: "0100C2500FC20000"},
		"playing a game":     {Presence: "PLAYING", AppID: "0100000000010000"},
		"online":             {Presence: "ONLINE"},
		"offline":            {Presence: "OFFLINE"},
		"hidden":             {},
	}
	for want, u := range cases {
		if got := presenceWord(u); got != want {
			t.Errorf("presenceWord(%+v) = %q, want %q", u, got, want)
		}
	}
}

func TestConfigRefusesMissingSecretsAndUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("SWITCHNET_DISCORD_TOKEN", "")
	t.Setenv("SWITCHNET_BOT_API_TOKEN", "")

	if _, err := LoadConfig(write("discord:\n  guild_ids: [\"1\"]\nswitchnet:\n  api_url: http://x\n")); err == nil {
		t.Error("config with no tokens accepted")
	}
	if _, err := LoadConfig(write("discord:\n  gild_ids: [\"1\"]\n")); err == nil {
		t.Error("misspelt key accepted")
	}

	t.Setenv("SWITCHNET_DISCORD_TOKEN", "bot-token")
	t.Setenv("SWITCHNET_BOT_API_TOKEN", "0123456789abcdef0123456789abcdef")
	cfg, err := LoadConfig(write("discord:\n  guild_ids: [\"1\"]\nswitchnet:\n  api_url: http://x\n"))
	if err != nil {
		t.Fatalf("tokens from the environment: %v", err)
	}
	if cfg.Discord.Token != "bot-token" {
		t.Errorf("token = %q", cfg.Discord.Token)
	}
}

// A save wipe names the account the same way a reset does, and reads back the
// backup GRID0+ kept.
func TestWipeSplatoon3SaveSendsTheLoginAndReadsTheBackup(t *testing.T) {
	var gotPath, gotLogin, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		var req struct {
			Username string `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotLogin = req.Username
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "alice", "bytes": 12, "backup": "u-20261010T110000Z-wipe.pb"})
	}))
	defer srv.Close()
	res, err := NewSwitchNet(srv.URL, "secret").WipeSplatoon3Save(t.Context(), "123456789012345678", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/bot/v1/splatoon3/save/wipe" || gotLogin != "alice" {
		t.Errorf("request %s %s login %q", gotMethod, gotPath, gotLogin)
	}
	if res.Backup != "u-20261010T110000Z-wipe.pb" || res.Login != "alice" {
		t.Errorf("decoded %+v", res)
	}
}

// An import sends the file as base64 in JSON, which is how GRID0+ reads it.
func TestImportSplatoon3SaveSendsTheFileAndTheLogin(t *testing.T) {
	var got struct {
		Username string `json:"username"`
		Save     []byte `json:"save"`
	}
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "alice", "gameVersion": "11.3.0", "bytes": 9})
	}))
	defer srv.Close()
	res, err := NewSwitchNet(srv.URL, "secret").ImportSplatoon3Save(t.Context(), "123456789012345678", "alice", []byte{0, 1, 0xff})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot/v1/splatoon3/save/import" || got.Username != "alice" || string(got.Save) != "\x00\x01\xff" {
		t.Errorf("request %s %+v", gotPath, got)
	}
	if res.GameVersion != "11.3.0" || res.Backup != "" {
		t.Errorf("decoded %+v", res)
	}
}

// A pending upload is only ever imported for the person who sent it.
func TestAPendingSaveIsOnlyTheUploadersToConfirm(t *testing.T) {
	b := &Bot{pendingSaves: map[string]pendingSave{
		"i1": {owner: "alice", data: []byte("x"), expires: time.Now().Add(time.Minute)},
		"i2": {owner: "alice", data: []byte("y"), expires: time.Now().Add(-time.Minute)},
	}}
	if _, ok := b.takePendingSave("i1", "bob"); ok {
		t.Error("bob confirmed alice's upload")
	}
	if p, ok := b.takePendingSave("i1", "alice"); !ok || string(p.data) != "x" {
		t.Error("alice could not confirm her own upload")
	}
	if _, ok := b.takePendingSave("i1", "alice"); ok {
		t.Error("an upload was imported twice")
	}
	if _, ok := b.takePendingSave("i2", "alice"); ok {
		t.Error("an expired upload was imported")
	}
}

func TestOnlyDiscordHostsAreFetched(t *testing.T) {
	for _, u := range []string{"http://cdn.discordapp.com/a", "https://example.com/save.dat", "https://127.0.0.1/x"} {
		if _, err := fetchAttachment(t.Context(), u); err == nil || !errors.Is(err, errNotDiscord) {
			t.Errorf("%s: err = %v", u, err)
		}
	}
}
