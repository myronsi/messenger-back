package notifications

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/myronsi/messenger-back/internal/config"
)

type serviceAccount struct {
	ProjectID  string `json:"project_id"`
	Email      string `json:"client_email"`
	PrivateKey string `json:"private_key"`
}
type nativeAuth struct {
	fcm     serviceAccount
	rsa     *rsa.PrivateKey
	ec      *ecdsa.PrivateKey
	mu      sync.Mutex
	token   string
	expires time.Time
}

func newNativeAuth(cfg config.Notifications) (*nativeAuth, error) {
	n := &nativeAuth{}
	if cfg.AndroidEnabled() {
		if json.Unmarshal([]byte(cfg.FCMServiceAccount.Reveal()), &n.fcm) != nil || n.fcm.ProjectID == "" || n.fcm.Email == "" {
			return nil, errors.New("invalid PUSH_FCM_SERVICE_ACCOUNT")
		}
		var err error
		n.rsa, err = jwt.ParseRSAPrivateKeyFromPEM([]byte(n.fcm.PrivateKey))
		if err != nil {
			return nil, errors.New("invalid FCM private key")
		}
	}
	if cfg.IOSEnabled() {
		var err error
		n.ec, err = jwt.ParseECPrivateKeyFromPEM([]byte(strings.ReplaceAll(cfg.APNsKey.Reveal(), `\n`, "\n")))
		if err != nil || n.ec.Curve.Params().BitSize != 256 {
			return nil, errors.New("invalid PUSH_APNS_KEY")
		}
	}
	return n, nil
}
func (n *nativeAuth) fcmToken(ctx context.Context, client *http.Client) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Now().Before(n.expires) {
		return n.token, nil
	}
	now := time.Now()
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": n.fcm.Email, "scope": "https://www.googleapis.com/auth/firebase.messaging",
		"aud": "https://oauth2.googleapis.com/token", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}).SignedString(n.rsa)
	if err != nil {
		return "", errors.New("sign FCM credentials")
	}
	data := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("FCM authentication transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result) != nil || result.Token == "" {
		return "", errors.New("FCM authentication rejected")
	}
	n.token = result.Token
	n.expires = now.Add(time.Duration(result.Expires)*time.Second - time.Minute)
	return n.token, nil
}
func (n *nativeAuth) apnsToken(cfg config.Notifications) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": cfg.APNsTeamID, "iat": time.Now().Unix()})
	t.Header["kid"] = cfg.APNsKeyID
	token, err := t.SignedString(n.ec)
	if err != nil {
		return "", errors.New("sign APNs credentials")
	}
	return token, nil
}
