package media

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const PlaybackTTL = 5 * time.Minute

type PlaybackSigner struct {
	key []byte
	now func() time.Time
}

func NewPlaybackSigner(secret string) *PlaybackSigner {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("media-playback-token"))
	return &PlaybackSigner{key: mac.Sum(nil), now: time.Now}
}

func (p *PlaybackSigner) mac(id uuid.UUID, viewerID int64, exp int64) string {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte(id.String() + "|" + strconv.FormatInt(viewerID, 10) + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (p *PlaybackSigner) Sign(id uuid.UUID, viewerID int64) (string, time.Time) {
	expires := p.now().Add(PlaybackTTL).Truncate(time.Second)
	exp := expires.Unix()
	return strconv.FormatInt(exp, 10) + "." + strconv.FormatInt(viewerID, 10) + "." + p.mac(id, viewerID, exp), expires
}

func (p *PlaybackSigner) Verify(token string, id uuid.UUID) (int64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || p.now().Unix() > exp {
		return 0, false
	}
	viewerID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(p.mac(id, viewerID, exp))) {
		return 0, false
	}
	return viewerID, true
}
