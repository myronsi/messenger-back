package main

import (
	goredis "github.com/redis/go-redis/v9"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/connections"
	"github.com/myronsi/messenger-back/internal/linkpreview"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func connectionService(cfg config.Connections, pg *postgres.Store, rdb goredis.Cmdable) *connections.Service {
	creds := func(id string, secret config.Secret) connections.Credentials {
		return connections.Credentials{ClientID: id, ClientSecret: secret.Reveal()}
	}
	var providers []connections.Provider
	for _, p := range []struct {
		c   connections.Credentials
		new func(connections.Credentials) *connections.OAuth
	}{
		{creds(cfg.GitHubClientID, cfg.GitHubClientSecret), connections.GitHub},
		{creds(cfg.SpotifyClientID, cfg.SpotifyClientSecret), connections.Spotify},
		{creds(cfg.XboxClientID, cfg.XboxClientSecret), connections.Xbox},
		{creds(cfg.DiscordClientID, cfg.DiscordClientSecret), connections.Discord},
		{creds(cfg.TwitchClientID, cfg.TwitchClientSecret), connections.Twitch},
		{creds(cfg.YouTubeClientID, cfg.YouTubeClientSecret), connections.YouTube},
		{creds(cfg.RedditClientID, cfg.RedditClientSecret), connections.Reddit},
		{creds(cfg.RobloxClientID, cfg.RobloxClientSecret), connections.Roblox},
	} {
		if p.c.Set() {
			providers = append(providers, p.new(p.c))
		}
	}
	if cfg.SteamAPIKey != "" {
		providers = append(providers, connections.NewSteam(cfg.SteamAPIKey.Reveal()))
	}
	var verifiers []connections.Verifier
	if cfg.VerifiersEnabled {
		safe := linkpreview.SafeClient()
		verifiers = []connections.Verifier{connections.NewBluesky(safe), connections.NewMastodon(safe), connections.NewWebsite(safe)}
	}
	return connections.NewService(connections.Options{
		Providers: providers, Verifiers: verifiers, Store: pg.Connections(), State: connections.NewRedisState(rdb, ""),
		CallbackURL: cfg.CallbackURL, WebURL: cfg.WebURL,
	})
}
