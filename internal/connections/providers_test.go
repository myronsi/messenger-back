package connections

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func jsonServer(t *testing.T, routes map[string]func(r *http.Request) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := h(r)
		if body == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bearer(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }

func TestProviderProfiles(t *testing.T) {
	srv := jsonServer(t, map[string]func(*http.Request) any{
		"/v1/me": func(r *http.Request) any {
			return map[string]any{"id": "sp1", "display_name": "Spot", "external_urls": map[string]string{"spotify": "https://open.spotify.com/user/sp1"}}
		},
		"/users/@me": func(r *http.Request) any { return map[string]any{"id": "d1", "username": "dee", "global_name": "Dee"} },
		"/helix/users": func(r *http.Request) any {
			if r.Header.Get("Client-Id") != "cid" || !bearer(r) {
				return nil
			}
			return map[string]any{"data": []map[string]string{{"id": "t1", "login": "streamer", "display_name": "Streamer"}}}
		},
		"/youtube/v3/channels": func(r *http.Request) any {
			return map[string]any{"items": []map[string]any{{"id": "UC1", "snippet": map[string]string{"title": "My Channel", "customUrl": "@mine"}}}}
		},
		"/api/v1/me":         func(r *http.Request) any { return map[string]any{"id": "r1", "name": "redditor"} },
		"/oauth/v1/userinfo": func(r *http.Request) any { return map[string]any{"sub": "rb1", "preferred_username": "builder"} },
		"/user/authenticate": func(r *http.Request) any {
			var body struct {
				Properties struct{ RpsTicket string }
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Properties.RpsTicket != "d=tok" {
				return nil
			}
			return map[string]string{"Token": "xbl"}
		},
		"/xsts/authorize": func(r *http.Request) any {
			if r.Header.Get("x-xbl-contract-version") != "1" {
				return nil
			}
			return map[string]any{"DisplayClaims": map[string]any{"xui": []map[string]string{{"gtg": "Gamer Tag", "xid": "x1"}}}}
		},
	})
	cases := []struct {
		name    string
		profile ProfileFunc
		want    Account
	}{
		{"spotify", spotifyProfile(srv.URL), Account{ExternalID: "sp1", DisplayName: "Spot", ProfileURL: str("https://open.spotify.com/user/sp1")}},
		{"discord", discordProfile(srv.URL), Account{ExternalID: "d1", DisplayName: "Dee (@dee)", ProfileURL: str("https://discord.com/users/d1")}},
		{"twitch", twitchProfile(srv.URL, "cid"), Account{ExternalID: "t1", DisplayName: "Streamer", ProfileURL: str("https://www.twitch.tv/streamer")}},
		{"youtube", youtubeProfile(srv.URL), Account{ExternalID: "UC1", DisplayName: "My Channel", ProfileURL: str("https://www.youtube.com/@mine")}},
		{"reddit", redditProfile(srv.URL), Account{ExternalID: "r1", DisplayName: "u/redditor", ProfileURL: str("https://www.reddit.com/user/redditor")}},
		{"roblox", robloxProfile(srv.URL), Account{ExternalID: "rb1", DisplayName: "builder", ProfileURL: str("https://www.roblox.com/users/rb1/profile")}},
		{"xbox", xboxProfile(srv.URL, srv.URL), Account{ExternalID: "x1", DisplayName: "Gamer Tag", ProfileURL: str("https://www.xbox.com/play/user/Gamer%20Tag")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.profile(context.Background(), srv.Client(), "tok")
			if err != nil {
				t.Fatal(err)
			}
			if got.ExternalID != c.want.ExternalID || got.DisplayName != c.want.DisplayName || *got.ProfileURL != *c.want.ProfileURL {
				t.Fatalf("got %+v %s", got, *got.ProfileURL)
			}
		})
	}
}

func TestProviderDefinitions(t *testing.T) {
	c := Credentials{ClientID: "id", ClientSecret: "secret"}
	for _, p := range []*OAuth{GitHub(c), Spotify(c), Xbox(c), Discord(c), Twitch(c), YouTube(c), Reddit(c), Roblox(c)} {
		u, err := url.Parse(p.AuthURL("st", "ch", "https://app.example/cb"))
		if err != nil || u.Scheme != "https" || u.Query().Get("state") != "st" || u.Query().Get("code_challenge") != "ch" {
			t.Fatalf("%s: %v %v", p.Name(), u, err)
		}
	}
	if Reddit(c).AuthURL("s", "c", "r") == "" || !Reddit(c).BasicAuth || !Spotify(c).BasicAuth {
		t.Fatal("reddit and spotify send the client as basic auth")
	}
}

func TestSteamOpenID(t *testing.T) {
	valid := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_ = r.ParseForm()
			if r.PostForm.Get("openid.mode") != "check_authentication" || r.PostForm.Get("openid.sig") != "sig" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if valid {
				_, _ = io.WriteString(w, "ns:http://specs.openid.net/auth/2.0\nis_valid:true\n")
			} else {
				_, _ = io.WriteString(w, "is_valid:false\n")
			}
		case "/ISteamUser/GetPlayerSummaries/v0002/":
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"players": []map[string]string{{"personaname": "Gabe", "profileurl": "https://steamcommunity.com/id/gabe/"}}}})
		}
	}))
	t.Cleanup(srv.Close)
	s := &Steam{APIKey: "k", LoginURL: srv.URL + "/login", APIURL: srv.URL, Client: srv.Client()}
	redirect := "https://app.example/api/v2/connections/callback/steam"
	auth, _ := url.Parse(s.AuthURL("st", "", redirect))
	if auth.Query().Get("openid.return_to") != redirect+"?state=st" || auth.Query().Get("openid.realm") != "https://app.example/" {
		t.Fatalf("auth url %s", auth)
	}
	q := url.Values{
		"state": {"st"}, "openid.mode": {"id_res"}, "openid.op_endpoint": {s.LoginURL}, "openid.return_to": {redirect + "?state=st"},
		"openid.claimed_id": {"https://steamcommunity.com/openid/id/76561197960287930"}, "openid.sig": {"sig"},
	}
	a, err := s.Complete(context.Background(), q, "", redirect)
	if err != nil || a.ExternalID != "76561197960287930" || a.DisplayName != "Gabe" || *a.ProfileURL != "https://steamcommunity.com/id/gabe/" {
		t.Fatalf("steam %+v %v", a, err)
	}
	valid = false
	if _, err := s.Complete(context.Background(), q, "", redirect); err == nil {
		t.Fatal("an assertion steam rejects")
	}
	valid = true
	forged := url.Values{}
	for k, v := range q {
		forged[k] = v
	}
	forged.Set("openid.op_endpoint", "https://evil.example/login")
	if _, err := s.Complete(context.Background(), forged, "", redirect); err == nil {
		t.Fatal("an assertion of another endpoint")
	}
	if _, err := s.Complete(context.Background(), url.Values{"openid.mode": {"cancel"}}, "", redirect); !errors.Is(err, ErrDenied) {
		t.Fatalf("cancel: %v", err)
	}
}
