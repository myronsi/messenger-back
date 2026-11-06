package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const QRLoginTTL = 2 * time.Minute

var (
	ErrQRLoginNotFound      = errors.New("qr login request not found")
	ErrQRLoginDecided       = errors.New("qr login request already decided")
	ErrApprovalSessionStale = errors.New("session too old to approve a sign-in")
)

type QRLoginStart struct {
	ID        string    `json:"request_id"`
	PollToken string    `json:"poll_token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type QRLoginInfo struct {
	ID        string          `json:"request_id"`
	Status    string          `json:"status"`
	Device    *string         `json:"device"`
	Location  *geoip.Location `json:"location"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

type QRLoginPoll struct {
	Status string
	Tokens *TokenSet
}

func validQRToken(v string) bool { return len(v) >= 32 && len(v) <= 128 }

func (s *Service) StartQRLogin(ctx context.Context, c Client) (QRLoginStart, error) {
	if err := s.take(ctx, RuleQRLoginIP, c.ipString()); err != nil {
		return QRLoginStart{}, err
	}
	id, err := randomToken(32)
	if err != nil {
		return QRLoginStart{}, err
	}
	poll, err := randomToken(32)
	if err != nil {
		return QRLoginStart{}, err
	}
	now := s.now()
	out := QRLoginStart{ID: id, PollToken: poll, URL: strings.TrimRight(s.recoveryWebURL, "/") + "/qr/" + id, ExpiresAt: now.Add(QRLoginTTL).UTC()}
	err = s.store.QRLogins().Create(ctx, postgres.QRLogin{
		IDHash: s.recoveryHash(id), PollHash: s.recoveryHash(poll), UserAgent: c.agentPtr(), Location: s.geo.Lookup(c.IP), CreatedAt: now, ExpiresAt: out.ExpiresAt,
	})
	if err != nil {
		return QRLoginStart{}, err
	}
	return out, nil
}

func (s *Service) PollQRLogin(ctx context.Context, c Client, id, pollToken string) (QRLoginPoll, error) {
	if err := s.take(ctx, RuleQRLoginPollIP, c.ipString()); err != nil {
		return QRLoginPoll{}, err
	}
	expired := QRLoginPoll{Status: "expired"}
	if !validQRToken(id) || !validQRToken(pollToken) {
		return expired, nil
	}
	pollHash := s.recoveryHash(pollToken)
	r, err := s.store.QRLogins().Get(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) {
		return expired, nil
	}
	if err != nil {
		return QRLoginPoll{}, err
	}
	if !hmac.Equal([]byte(r.PollHash), []byte(pollHash)) || r.UsedAt != nil || !r.ExpiresAt.After(s.now()) {
		return expired, nil
	}
	if r.Status != "approved" {
		return QRLoginPoll{Status: r.Status}, nil
	}
	refresh, refreshHash, err := newRefreshToken()
	if err != nil {
		return QRLoginPoll{}, err
	}
	sess, err := s.store.QRLogins().Complete(ctx, r.IDHash, pollHash, postgres.NewSession{
		RefreshTokenHash: refreshHash, UserAgent: c.agentPtr(), IP: c.ipPtr(), Location: s.geo.Lookup(c.IP),
	}, s.recoveryApprovalMaxAge, s.now())
	if errors.Is(err, postgres.ErrNotFound) {
		return expired, nil
	}
	if err != nil {
		return QRLoginPoll{}, err
	}
	u, err := s.store.Users().Get(ctx, sess.UserID)
	if err != nil {
		return QRLoginPoll{}, err
	}
	access, accessExp, err := s.accessToken(sess.ID, u.ID)
	if err != nil {
		return QRLoginPoll{}, err
	}
	s.event(ctx, u.ID, "qr_login", c, map[string]any{"login_method": "qr"})
	return QRLoginPoll{Status: "approved", Tokens: &TokenSet{
		AccessToken: access, AccessExpiresAt: accessExp,
		RefreshToken: refresh, RefreshExpiresAt: sess.ExpiresAt,
		SessionID: sess.ID, User: u,
	}}, nil
}

func qrLoginInfo(id string, r postgres.QRLogin) QRLoginInfo {
	return QRLoginInfo{ID: id, Status: r.Status, Device: r.UserAgent, Location: r.Location, CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC()}
}

func (s *Service) ScanQRLogin(ctx context.Context, p Principal, id string) (QRLoginInfo, error) {
	if !validQRToken(id) {
		return QRLoginInfo{}, ErrQRLoginNotFound
	}
	r, err := s.store.QRLogins().Scan(ctx, s.recoveryHash(id), p.UserID)
	if errors.Is(err, postgres.ErrNotFound) {
		return QRLoginInfo{}, ErrQRLoginNotFound
	}
	if err != nil {
		return QRLoginInfo{}, err
	}
	return qrLoginInfo(id, r), nil
}

func (s *Service) decidableQRLogin(ctx context.Context, p Principal, id string) (postgres.QRLogin, error) {
	if !validQRToken(id) {
		return postgres.QRLogin{}, ErrQRLoginNotFound
	}
	r, err := s.store.QRLogins().Get(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) {
		return r, ErrQRLoginNotFound
	}
	if err != nil {
		return r, err
	}
	if r.UsedAt != nil || !r.ExpiresAt.After(s.now()) || r.ScannedBy != nil && *r.ScannedBy != p.UserID {
		return r, ErrQRLoginNotFound
	}
	if r.Status != "pending" && r.Status != "scanned" {
		return r, ErrQRLoginDecided
	}
	return r, nil
}

func qrLoginError(err error) error {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return ErrQRLoginNotFound
	case errors.Is(err, postgres.ErrQRLoginDecided):
		return ErrQRLoginDecided
	case errors.Is(err, postgres.ErrStaleApproval):
		return ErrApprovalSessionStale
	case errors.Is(err, postgres.ErrInvalid):
		return ErrRecoveryKey
	case errors.Is(err, postgres.ErrConflict):
		return ErrConflict
	}
	return err
}

func (s *Service) ApproveQRLogin(ctx context.Context, c Client, p Principal, id, code string) error {
	if len(code) > 128 {
		return ErrRecoveryKey
	}
	r, err := s.decidableQRLogin(ctx, p, id)
	if err != nil {
		return err
	}
	if err = s.store.QRLogins().ApproverActive(ctx, p.UserID, p.SessionID, s.recoveryApprovalMaxAge); err != nil {
		return qrLoginError(err)
	}
	credentials, err := s.store.Users().CredentialsByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return err
	}
	if st.TwoFactorEnabled && strings.TrimSpace(code) == "" {
		return ErrTwoFactorRequired
	}
	proof := postgres.RecoveryProof{PasswordHash: credentials.PasswordHash, TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret}
	if proof.BackupCodeHash, err = s.limitedRecoveryFactor(ctx, p.UserID, st, code); err != nil {
		return err
	}
	if _, err = s.store.QRLogins().Approve(ctx, r.IDHash, p.UserID, p.SessionID, proof, s.recoveryApprovalMaxAge); err != nil {
		return qrLoginError(err)
	}
	s.qrLoginEvent(ctx, p.UserID, "qr_login_approved", r)
	return nil
}

func (s *Service) DenyQRLogin(ctx context.Context, p Principal, id string) error {
	r, err := s.decidableQRLogin(ctx, p, id)
	if err != nil {
		return err
	}
	if _, err = s.store.QRLogins().Deny(ctx, r.IDHash, p.UserID); err != nil {
		return qrLoginError(err)
	}
	s.qrLoginEvent(ctx, p.UserID, "qr_login_denied", r)
	return nil
}

func (s *Service) qrLoginEvent(ctx context.Context, uid int64, typ string, r postgres.QRLogin) {
	notice := Client{}
	if r.UserAgent != nil {
		notice.UserAgent = *r.UserAgent
	}
	s.recordEvent(ctx, uid, typ, notice, nil, r.UserAgent, r.Location, nil)
}
