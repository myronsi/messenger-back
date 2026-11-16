package connections

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const openIDNS = "http://specs.openid.net/auth/2.0"

var steamIDPattern = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(\d{17})$`)

type Steam struct {
	APIKey   string
	LoginURL string
	APIURL   string
	Client   *http.Client
}

func NewSteam(apiKey string) *Steam {
	return &Steam{APIKey: apiKey, LoginURL: "https://steamcommunity.com/openid/login", APIURL: "https://api.steampowered.com"}
}

func (s *Steam) Name() string { return "steam" }

func (s *Steam) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: requestTimeout}
}

func returnTo(redirect, state string) string {
	return redirect + "?state=" + url.QueryEscape(state)
}

func realm(redirect string) string {
	u, err := url.Parse(redirect)
	if err != nil {
		return redirect
	}
	return u.Scheme + "://" + u.Host + "/"
}

func (s *Steam) AuthURL(state, _, redirect string) string {
	q := url.Values{}
	q.Set("openid.ns", openIDNS)
	q.Set("openid.mode", "checkid_setup")
	q.Set("openid.return_to", returnTo(redirect, state))
	q.Set("openid.realm", realm(redirect))
	q.Set("openid.identity", openIDNS+"/identifier_select")
	q.Set("openid.claimed_id", openIDNS+"/identifier_select")
	return s.LoginURL + "?" + q.Encode()
}

func (s *Steam) Complete(ctx context.Context, query url.Values, _, redirect string) (Account, error) {
	switch query.Get("openid.mode") {
	case "cancel":
		return Account{}, ErrDenied
	case "id_res":
	default:
		return Account{}, fmt.Errorf("%w: unexpected openid mode", ErrFailed)
	}
	if query.Get("openid.op_endpoint") != s.LoginURL || query.Get("openid.return_to") != returnTo(redirect, query.Get("state")) {
		return Account{}, fmt.Errorf("%w: assertion for another endpoint", ErrFailed)
	}
	m := steamIDPattern.FindStringSubmatch(query.Get("openid.claimed_id"))
	if m == nil {
		return Account{}, fmt.Errorf("%w: not a steam id", ErrFailed)
	}
	if err := s.checkAuthentication(ctx, query); err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrFailed, err)
	}
	a := Account{ExternalID: m[1], DisplayName: m[1], ProfileURL: str("https://steamcommunity.com/profiles/" + m[1])}
	if name, link, err := s.summary(ctx, m[1]); err == nil {
		a.DisplayName = name
		if link != "" {
			a.ProfileURL = str(link)
		}
	}
	return cleanAccount(a)
}

func (s *Steam) checkAuthentication(ctx context.Context, query url.Values) error {
	form := url.Values{}
	for k, vs := range query {
		if strings.HasPrefix(k, "openid.") {
			form[k] = vs
		}
	}
	form.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.LoginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "is_valid:true" {
			return nil
		}
	}
	return errors.New("assertion not valid")
}

func (s *Steam) summary(ctx context.Context, id string) (string, string, error) {
	var out struct {
		Response struct {
			Players []struct {
				PersonaName string `json:"personaname"`
				ProfileURL  string `json:"profileurl"`
			} `json:"players"`
		} `json:"response"`
	}
	u := s.APIURL + "/ISteamUser/GetPlayerSummaries/v0002/?key=" + url.QueryEscape(s.APIKey) + "&steamids=" + id
	if err := getJSON(ctx, s.client(), u, "", nil, &out); err != nil {
		return "", "", err
	}
	if len(out.Response.Players) == 0 {
		return "", "", errors.New("no player")
	}
	p := out.Response.Players[0]
	return p.PersonaName, p.ProfileURL, nil
}
