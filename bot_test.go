package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func TestResetPasswordSendsTheDisconnectChoice(t *testing.T) {
	var choices []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot/v1/reset-password" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req struct {
			DisconnectAllAccounts bool `json:"disconnectAllAccounts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		choices = append(choices, req.DisconnectAllAccounts)
		_ = json.NewEncoder(w).Encode(map[string]string{"password": "new-password"})
	}))
	defer srv.Close()
	client := NewSwitchNet(srv.URL, "secret")
	for _, disconnect := range []bool{false, true} {
		creds, err := client.ResetPassword(t.Context(), "123456789012345678", disconnect)
		if err != nil || creds.Password != "new-password" {
			t.Fatalf("reset disconnect=%v: %+v, %v", disconnect, creds, err)
		}
	}
	if !bytes.Equal([]byte{boolByte(choices[0]), boolByte(choices[1])}, []byte{0, 1}) {
		t.Errorf("disconnect choices = %v", choices)
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
