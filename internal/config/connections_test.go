package config

import "testing"

func TestConnectionConfiguration(t *testing.T) {
	if problems := (Connections{VerifiersEnabled: true}).validate(); len(problems) > 0 {
		t.Fatalf("connections must be optional: %v", problems)
	}
	ok := Connections{CallbackURL: "https://app.example/api/v2", WebURL: "https://app.example", GitHubClientID: "id", GitHubClientSecret: "s"}
	if problems := ok.validate(); len(problems) > 0 {
		t.Fatal(problems)
	}
	for name, c := range map[string]Connections{
		"half a pair":        {CallbackURL: ok.CallbackURL, WebURL: ok.WebURL, TwitchClientID: "id"},
		"no callback URL":    {WebURL: ok.WebURL, GitHubClientID: "id", GitHubClientSecret: "s"},
		"steam without URLs": {SteamAPIKey: "k"},
		"bad web URL":        {CallbackURL: ok.CallbackURL, WebURL: "app.example"},
		"full callback path": {CallbackURL: "https://app.example/api/v2/connections/callback", WebURL: ok.WebURL},
	} {
		if len(c.validate()) == 0 {
			t.Errorf("%s accepted", name)
		}
	}
	env := validEnv()
	env["CONNECT_DISCORD_CLIENT_ID"] = "only-id"
	if _, err := LoadFrom(env); err == nil {
		t.Fatal("Load accepts half a provider")
	}
}
