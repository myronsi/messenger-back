package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidActionToken = errors.New("invalid notification action token")

const (
	AvatarTokenTTL = time.Hour
	ReadTokenTTL   = 7 * 24 * time.Hour
)

type ActionTokens struct {
	key []byte
	now func() time.Time
}

func NewActionTokens(secret string) *ActionTokens {
	if secret == "" {
		return nil
	}
	sum := sha256.Sum256([]byte("messenger notification action v1\x00" + secret))
	return &ActionTokens{key: sum[:], now: time.Now}
}

func (t *ActionTokens) Avatar(recipient, sender int64) string {
	return t.sign("a", t.now().Add(AvatarTokenTTL), recipient, sender)
}

func (t *ActionTokens) Read(recipient, chat, message int64) string {
	return t.sign("r", t.now().Add(ReadTokenTTL), recipient, chat, message)
}

func (t *ActionTokens) ParseAvatar(token string) (recipient, sender int64, err error) {
	ids, err := t.parse("a", 2, token)
	if err != nil {
		return 0, 0, err
	}
	return ids[0], ids[1], nil
}

func (t *ActionTokens) ParseRead(token string) (recipient, chat, message int64, err error) {
	ids, err := t.parse("r", 3, token)
	if err != nil {
		return 0, 0, 0, err
	}
	return ids[0], ids[1], ids[2], nil
}

func (t *ActionTokens) sign(purpose string, exp time.Time, ids ...int64) string {
	parts := []string{purpose}
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	parts = append(parts, strconv.FormatInt(exp.Unix(), 10))
	body := strings.Join(parts, ".")
	return body + "." + t.mac(body)
}

func (t *ActionTokens) parse(purpose string, n int, token string) ([]int64, error) {
	parts := strings.Split(token, ".")
	if t == nil || len(parts) != n+3 || parts[0] != purpose {
		return nil, ErrInvalidActionToken
	}
	body := strings.Join(parts[:len(parts)-1], ".")
	if !hmac.Equal([]byte(parts[len(parts)-1]), []byte(t.mac(body))) {
		return nil, ErrInvalidActionToken
	}
	exp, err := strconv.ParseInt(parts[n+1], 10, 64)
	if err != nil || t.now().Unix() > exp {
		return nil, ErrInvalidActionToken
	}
	ids := make([]int64, n)
	for i := range ids {
		if ids[i], err = strconv.ParseInt(parts[i+1], 10, 64); err != nil || ids[i] <= 0 {
			return nil, ErrInvalidActionToken
		}
	}
	return ids, nil
}

func (t *ActionTokens) mac(body string) string {
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
