package main

import (
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
