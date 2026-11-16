package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

type Credentials struct {
	ClientID     string
	ClientSecret string
}

func (c Credentials) Set() bool { return c.ClientID != "" && c.ClientSecret != "" }

func str(s string) *string { return &s }

func GitHub(c Credentials) *OAuth {
	return &OAuth{ProviderName: "github", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://github.com/login/oauth/authorize", ExchangeURL: "https://github.com/login/oauth/access_token",
		Scopes: []string{"read:user"}, Profile: githubProfile("https://api.github.com")}
}

func githubProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var u struct {
			ID      int64  `json:"id"`
			Login   string `json:"login"`
			HTMLURL string `json:"html_url"`
		}
		if err := getJSON(ctx, c, api+"/user", token, nil, &u); err != nil {
			return Account{}, err
		}
		return Account{ExternalID: strconv.FormatInt(u.ID, 10), DisplayName: u.Login, ProfileURL: str(u.HTMLURL)}, nil
	}
}

func Spotify(c Credentials) *OAuth {
	return &OAuth{ProviderName: "spotify", ClientID: c.ClientID, ClientSecret: c.ClientSecret, BasicAuth: true,
		AuthorizeURL: "https://accounts.spotify.com/authorize", ExchangeURL: "https://accounts.spotify.com/api/token",
		Profile: spotifyProfile("https://api.spotify.com")}
}

func spotifyProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var u struct {
			ID           string `json:"id"`
			DisplayName  string `json:"display_name"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
		}
		if err := getJSON(ctx, c, api+"/v1/me", token, nil, &u); err != nil {
			return Account{}, err
		}
		return Account{ExternalID: u.ID, DisplayName: u.DisplayName, ProfileURL: str(u.ExternalURLs.Spotify)}, nil
	}
}

func Discord(c Credentials) *OAuth {
	return &OAuth{ProviderName: "discord", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://discord.com/oauth2/authorize", ExchangeURL: "https://discord.com/api/oauth2/token",
		Scopes: []string{"identify"}, Profile: discordProfile("https://discord.com/api")}
}

func discordProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var u struct {
			ID         string `json:"id"`
			Username   string `json:"username"`
			GlobalName string `json:"global_name"`
		}
		if err := getJSON(ctx, c, api+"/users/@me", token, nil, &u); err != nil {
			return Account{}, err
		}
		name := u.Username
		if u.GlobalName != "" {
			name = u.GlobalName + " (@" + u.Username + ")"
		}
		return Account{ExternalID: u.ID, DisplayName: name, ProfileURL: str("https://discord.com/users/" + url.PathEscape(u.ID))}, nil
	}
}

func Twitch(c Credentials) *OAuth {
	return &OAuth{ProviderName: "twitch", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://id.twitch.tv/oauth2/authorize", ExchangeURL: "https://id.twitch.tv/oauth2/token",
		Profile: twitchProfile("https://api.twitch.tv", c.ClientID)}
}

func twitchProfile(api, clientID string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var out struct {
			Data []struct {
				ID          string `json:"id"`
				Login       string `json:"login"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := getJSON(ctx, c, api+"/helix/users", token, http.Header{"Client-Id": {clientID}}, &out); err != nil {
			return Account{}, err
		}
		if len(out.Data) == 0 {
			return Account{}, errors.New("no user")
		}
		u := out.Data[0]
		return Account{ExternalID: u.ID, DisplayName: u.DisplayName, ProfileURL: str("https://www.twitch.tv/" + url.PathEscape(u.Login))}, nil
	}
}

func YouTube(c Credentials) *OAuth {
	return &OAuth{ProviderName: "youtube", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth", ExchangeURL: "https://oauth2.googleapis.com/token",
		Scopes: []string{"https://www.googleapis.com/auth/youtube.readonly"}, Profile: youtubeProfile("https://www.googleapis.com")}
}

func youtubeProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var out struct {
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title     string `json:"title"`
					CustomURL string `json:"customUrl"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if err := getJSON(ctx, c, api+"/youtube/v3/channels?part=snippet&mine=true", token, nil, &out); err != nil {
			return Account{}, err
		}
		if len(out.Items) == 0 {
			return Account{}, errors.New("no channel")
		}
		ch := out.Items[0]
		link := "https://www.youtube.com/channel/" + url.PathEscape(ch.ID)
		if ch.Snippet.CustomURL != "" {
			link = "https://www.youtube.com/" + url.PathEscape(ch.Snippet.CustomURL)
		}
		return Account{ExternalID: ch.ID, DisplayName: ch.Snippet.Title, ProfileURL: str(link)}, nil
	}
}

func Reddit(c Credentials) *OAuth {
	return &OAuth{ProviderName: "reddit", ClientID: c.ClientID, ClientSecret: c.ClientSecret, BasicAuth: true,
		AuthorizeURL: "https://www.reddit.com/api/v1/authorize", ExchangeURL: "https://www.reddit.com/api/v1/access_token",
		Scopes: []string{"identity"}, ExtraAuth: url.Values{"duration": {"temporary"}}, Profile: redditProfile("https://oauth.reddit.com")}
}

func redditProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var u struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := getJSON(ctx, c, api+"/api/v1/me", token, nil, &u); err != nil {
			return Account{}, err
		}
		return Account{ExternalID: u.ID, DisplayName: "u/" + u.Name, ProfileURL: str("https://www.reddit.com/user/" + url.PathEscape(u.Name))}, nil
	}
}

func Roblox(c Credentials) *OAuth {
	return &OAuth{ProviderName: "roblox", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://apis.roblox.com/oauth/v1/authorize", ExchangeURL: "https://apis.roblox.com/oauth/v1/token",
		Scopes: []string{"openid", "profile"}, Profile: robloxProfile("https://apis.roblox.com")}
}

func robloxProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var u struct {
			Sub               string `json:"sub"`
			PreferredUsername string `json:"preferred_username"`
			Profile           string `json:"profile"`
		}
		if err := getJSON(ctx, c, api+"/oauth/v1/userinfo", token, nil, &u); err != nil {
			return Account{}, err
		}
		link := u.Profile
		if link == "" {
			link = "https://www.roblox.com/users/" + url.PathEscape(u.Sub) + "/profile"
		}
		return Account{ExternalID: u.Sub, DisplayName: u.PreferredUsername, ProfileURL: str(link)}, nil
	}
}

func TikTok(c Credentials) *OAuth {
	return &OAuth{ProviderName: "tiktok", ClientID: c.ClientID, ClientSecret: c.ClientSecret, ClientParam: "client_key", ScopeSep: ",",
		AuthorizeURL: "https://www.tiktok.com/v2/auth/authorize/", ExchangeURL: "https://open.tiktokapis.com/v2/oauth/token/",
		Scopes: []string{"user.info.basic", "user.info.profile"}, Profile: tiktokProfile("https://open.tiktokapis.com")}
}

func tiktokProfile(api string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var out struct {
			Data struct {
				User struct {
					OpenID      string `json:"open_id"`
					DisplayName string `json:"display_name"`
					Username    string `json:"username"`
				} `json:"user"`
			} `json:"data"`
		}
		if err := getJSON(ctx, c, api+"/v2/user/info/?fields=open_id,display_name,username", token, nil, &out); err != nil {
			return Account{}, err
		}
		u := out.Data.User
		if u.OpenID == "" {
			return Account{}, errors.New("no user")
		}
		a := Account{ExternalID: u.OpenID, DisplayName: u.DisplayName}
		if u.Username != "" {
			a.DisplayName = "@" + u.Username
			a.ProfileURL = str("https://www.tiktok.com/@" + url.PathEscape(u.Username))
		}
		return a, nil
	}
}

func Xbox(c Credentials) *OAuth {
	return &OAuth{ProviderName: "xbox", ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		AuthorizeURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
		ExchangeURL:  "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
		Scopes:       []string{"XboxLive.signin"},
		Profile:      xboxProfile("https://user.auth.xboxlive.com", "https://xsts.auth.xboxlive.com")}
}

func postJSON(ctx context.Context, c *http.Client, u string, body any, header http.Header, dst any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return doJSON(c, req, dst)
}

func xboxProfile(userAuth, xsts string) ProfileFunc {
	return func(ctx context.Context, c *http.Client, token string) (Account, error) {
		var user struct {
			Token string `json:"Token"`
		}
		if err := postJSON(ctx, c, userAuth+"/user/authenticate", map[string]any{
			"Properties":   map[string]any{"AuthMethod": "RPS", "SiteName": "user.auth.xboxlive.com", "RpsTicket": "d=" + token},
			"RelyingParty": "http://auth.xboxlive.com", "TokenType": "JWT",
		}, nil, &user); err != nil {
			return Account{}, err
		}
		var auth struct {
			DisplayClaims struct {
				Xui []struct {
					Gamertag string `json:"gtg"`
					XUID     string `json:"xid"`
				} `json:"xui"`
			} `json:"DisplayClaims"`
		}
		if err := postJSON(ctx, c, xsts+"/xsts/authorize", map[string]any{
			"Properties":   map[string]any{"SandboxId": "RETAIL", "UserTokens": []string{user.Token}},
			"RelyingParty": "http://xboxlive.com", "TokenType": "JWT",
		}, http.Header{"x-xbl-contract-version": {"1"}}, &auth); err != nil {
			return Account{}, err
		}
		if len(auth.DisplayClaims.Xui) == 0 {
			return Account{}, errors.New("no gamertag")
		}
		x := auth.DisplayClaims.Xui[0]
		return Account{ExternalID: x.XUID, DisplayName: x.Gamertag, ProfileURL: str("https://www.xbox.com/play/user/" + url.PathEscape(x.Gamertag))}, nil
	}
}
