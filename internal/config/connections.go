package config

import (
	"net/url"
	"strings"
)

type Connections struct {
	CallbackURL         string `env:"CONNECTIONS_CALLBACK_URL"`
	WebURL              string `env:"CONNECTIONS_WEB_URL"`
	VerifiersEnabled    bool   `env:"CONNECTIONS_VERIFIERS_ENABLED" envDefault:"true"`
	GitHubClientID      string `env:"CONNECT_GITHUB_CLIENT_ID"`
	GitHubClientSecret  Secret `env:"CONNECT_GITHUB_CLIENT_SECRET"`
	SpotifyClientID     string `env:"CONNECT_SPOTIFY_CLIENT_ID"`
	SpotifyClientSecret Secret `env:"CONNECT_SPOTIFY_CLIENT_SECRET"`
	XboxClientID        string `env:"CONNECT_XBOX_CLIENT_ID"`
	XboxClientSecret    Secret `env:"CONNECT_XBOX_CLIENT_SECRET"`
	DiscordClientID     string `env:"CONNECT_DISCORD_CLIENT_ID"`
	DiscordClientSecret Secret `env:"CONNECT_DISCORD_CLIENT_SECRET"`
	TwitchClientID      string `env:"CONNECT_TWITCH_CLIENT_ID"`
	TwitchClientSecret  Secret `env:"CONNECT_TWITCH_CLIENT_SECRET"`
	YouTubeClientID     string `env:"CONNECT_YOUTUBE_CLIENT_ID"`
	YouTubeClientSecret Secret `env:"CONNECT_YOUTUBE_CLIENT_SECRET"`
	RedditClientID      string `env:"CONNECT_REDDIT_CLIENT_ID"`
	RedditClientSecret  Secret `env:"CONNECT_REDDIT_CLIENT_SECRET"`
	RobloxClientID      string `env:"CONNECT_ROBLOX_CLIENT_ID"`
	RobloxClientSecret  Secret `env:"CONNECT_ROBLOX_CLIENT_SECRET"`
	TikTokClientKey     string `env:"CONNECT_TIKTOK_CLIENT_KEY"`
	TikTokClientSecret  Secret `env:"CONNECT_TIKTOK_CLIENT_SECRET"`
	SteamAPIKey         Secret `env:"CONNECT_STEAM_API_KEY"`
}

type OAuthPair struct {
	Name   string
	ID     string
	Secret Secret
}

func (c Connections) Pairs() []OAuthPair {
	return []OAuthPair{
		{"GITHUB", c.GitHubClientID, c.GitHubClientSecret},
		{"SPOTIFY", c.SpotifyClientID, c.SpotifyClientSecret},
		{"XBOX", c.XboxClientID, c.XboxClientSecret},
		{"DISCORD", c.DiscordClientID, c.DiscordClientSecret},
		{"TWITCH", c.TwitchClientID, c.TwitchClientSecret},
		{"YOUTUBE", c.YouTubeClientID, c.YouTubeClientSecret},
		{"REDDIT", c.RedditClientID, c.RedditClientSecret},
		{"ROBLOX", c.RobloxClientID, c.RobloxClientSecret},
		{"TIKTOK", c.TikTokClientKey, c.TikTokClientSecret},
	}
}

func httpsBase(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") && u.RawQuery == "" && u.Fragment == ""
}

func (c Connections) validate() []string {
	var problems []string
	configured := c.SteamAPIKey != ""
	for _, p := range c.Pairs() {
		if (p.ID == "") != (p.Secret == "") {
			idVar := "_CLIENT_ID"
			if p.Name == "TIKTOK" {
				idVar = "_CLIENT_KEY"
			}
			problems = append(problems, "set both CONNECT_"+p.Name+idVar+" and CONNECT_"+p.Name+"_CLIENT_SECRET")
		}
		configured = configured || p.ID != ""
	}
	if c.CallbackURL != "" && !httpsBase(c.CallbackURL) {
		problems = append(problems, "CONNECTIONS_CALLBACK_URL must be an http(s) URL without query")
	}
	if c.WebURL != "" && !httpsBase(c.WebURL) {
		problems = append(problems, "CONNECTIONS_WEB_URL must be an http(s) URL without query")
	}
	if configured && (c.CallbackURL == "" || c.WebURL == "") {
		problems = append(problems, "OAuth providers need CONNECTIONS_CALLBACK_URL and CONNECTIONS_WEB_URL")
	}
	if strings.HasSuffix(c.CallbackURL, "/connections/callback") {
		problems = append(problems, "CONNECTIONS_CALLBACK_URL is the API base, without /connections/callback")
	}
	return problems
}
