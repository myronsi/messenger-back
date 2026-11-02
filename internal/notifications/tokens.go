package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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

type ActionToken struct {
	Recipient int64
	Device    uuid.UUID
	IDs       []int64
}

func NewActionTokens(secret string) *ActionTokens {
	if secret == "" {
		return nil
	}
	sum := sha256.Sum256([]byte("messenger notification action v2\x00" + secret))
	return &ActionTokens{key: sum[:], now: time.Now}
}

func (t *ActionTokens) Avatar(recipient int64, device uuid.UUID, sender int64) string {
	return t.sign("a", t.now().Add(AvatarTokenTTL), recipient, device, sender)
}

func (t *ActionTokens) Read(recipient int64, device uuid.UUID, chat, message int64) string {
	return t.sign("r", t.now().Add(ReadTokenTTL), recipient, device, chat, message)
}

func (t *ActionTokens) ParseAvatar(token string) (ActionToken, error) {
	return t.parse("a", 1, token)
}

func (t *ActionTokens) ParseRead(token string) (ActionToken, error) {
	return t.parse("r", 2, token)
}

func (t *ActionTokens) sign(purpose string, exp time.Time, recipient int64, device uuid.UUID, ids ...int64) string {
	parts := []string{purpose, strconv.FormatInt(recipient, 10), device.String()}
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	parts = append(parts, strconv.FormatInt(exp.Unix(), 10))
	body := strings.Join(parts, ".")
	return body + "." + t.mac(body)
}

func (t *ActionTokens) parse(purpose string, n int, token string) (ActionToken, error) {
	parts := strings.Split(token, ".")
	if t == nil || len(parts) != n+5 || parts[0] != purpose {
		return ActionToken{}, ErrInvalidActionToken
	}
	body := strings.Join(parts[:len(parts)-1], ".")
	if !hmac.Equal([]byte(parts[len(parts)-1]), []byte(t.mac(body))) {
		return ActionToken{}, ErrInvalidActionToken
	}
	exp, err := strconv.ParseInt(parts[n+3], 10, 64)
	if err != nil || t.now().Unix() > exp {
		return ActionToken{}, ErrInvalidActionToken
	}
	recipient, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || recipient <= 0 {
		return ActionToken{}, ErrInvalidActionToken
	}
	device, err := uuid.Parse(parts[2])
	if err != nil {
		return ActionToken{}, ErrInvalidActionToken
	}
	ids := make([]int64, n)
	for i := range ids {
		if ids[i], err = strconv.ParseInt(parts[i+3], 10, 64); err != nil || ids[i] <= 0 {
			return ActionToken{}, ErrInvalidActionToken
		}
	}
	return ActionToken{Recipient: recipient, Device: device, IDs: ids}, nil
}

func (t *ActionTokens) mac(body string) string {
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
