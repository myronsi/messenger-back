package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type RecoveryRequestResult struct {
	ID        string    `json:"request_id"`
	Match     int       `json:"match_number"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RecoveryRequestNotice struct {
	ID        string    `json:"request_id"`
	UserAgent string    `json:"user_agent"`
	IPHint    string    `json:"ip_hint"`
	Choices   []int     `json:"choices"`
	ExpiresAt time.Time `json:"expires_at"`
}

func recoveryNumbers() ([]int, error) {
	choices := make([]int, 0, 3)
	for len(choices) < 3 {
		n, err := rand.Int(rand.Reader, big.NewInt(90))
		if err != nil {
			return nil, err
		}
		value := int(n.Int64()) + 10
		found := false
		for _, prior := range choices {
			if prior == value {
				found = true
			}
		}
		if !found {
			choices = append(choices, value)
		}
	}
	return choices, nil
}

func (s *Service) StartDeviceRecovery(ctx context.Context, c Client, username string) (RecoveryRequestResult, error) {
	username = normalizeUsername(username)
	if len(username) > 64 {
		return RecoveryRequestResult{}, ErrRecoveryKey
	}
	if _, err := s.allow(ctx, limitCheck{RuleRecoverIP, c.ipString()}, limitCheck{RuleRecoverUsr, c.userKey(username)}, limitCheck{RuleRecoverCap, username}); err != nil {
		return RecoveryRequestResult{}, err
	}
	id, err := randomToken(32)
	if err != nil {
		return RecoveryRequestResult{}, err
	}
	choices, err := recoveryNumbers()
	if err != nil {
		return RecoveryRequestResult{}, err
	}
	index, err := rand.Int(rand.Reader, big.NewInt(3))
	if err != nil {
		return RecoveryRequestResult{}, err
	}
	result := RecoveryRequestResult{ID: id, Match: choices[index.Int64()], ExpiresAt: s.now().Add(10 * time.Minute)}
	u, err := s.store.Users().GetByUsername(ctx, username)
	if errors.Is(err, postgres.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	hint := "Unknown network"
	if c.IP.IsValid() {
		bits := 24
		if c.IP.Is6() {
			bits = 48
		}
		hint = netip.PrefixFrom(c.IP.Unmap(), bits).Masked().String()
	}
	created, err := s.store.RecoveryRequests().Create(ctx, postgres.DeviceRecovery{Hash: s.recoveryHash(id), UserID: u.ID, Match: result.Match, Choices: choices, UserAgent: cleanUserAgent(c.UserAgent), IPHint: hint, ExpiresAt: result.ExpiresAt})
	if err != nil {
		return result, err
	}
	if created && s.onRecoveryRequest != nil {
		s.onRecoveryRequest(ctx, u.ID, RecoveryRequestNotice{ID: id, Choices: choices, UserAgent: cleanUserAgent(c.UserAgent), IPHint: hint, ExpiresAt: result.ExpiresAt})
	}
	return result, nil
}

func (s *Service) DeviceRecoveryStatus(ctx context.Context, c Client, id string) (string, error) {
	if err := s.take(ctx, RuleRecoveryPoll, c.ipString()); err != nil {
		return "", err
	}
	if len(id) < 32 || len(id) > 128 {
		return "expired", nil
	}
	r, err := s.store.RecoveryRequests().Get(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) {
		return "expired", nil
	}
	if err != nil {
		return "", err
	}
	if !r.ExpiresAt.After(s.now()) {
		return "expired", nil
	}
	return r.Status, nil
}

func (s *Service) DecideDeviceRecovery(ctx context.Context, c Client, p Principal, id string, match int, code string, deny bool) (string, error) {
	if len(id) < 32 || len(id) > 128 || len(code) > 128 {
		return "", ErrRecoveryKey
	}
	if err := s.take(ctx, RuleTwoFAUser, fmt.Sprint(p.UserID)); err != nil {
		return "", err
	}
	r, err := s.store.RecoveryRequests().Get(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) || err == nil && r.UserID != p.UserID {
		return "", ErrRecoveryKey
	}
	if err != nil {
		return "", err
	}
	credentials, err := s.store.Users().CredentialsByID(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	proof := postgres.RecoveryProof{PasswordHash: credentials.PasswordHash, TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret}
	if !deny && r.Match == match {
		proof.BackupCodeHash, err = s.recoveryFactor(ctx, p.UserID, st, code)
		if err != nil {
			return "", err
		}
	}
	status, err := s.store.RecoveryRequests().Decide(ctx, r.Hash, p.UserID, p.SessionID, match, proof, s.recoveryApprovalMaxAge, deny)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrConflict) {
		return "", ErrRecoveryKey
	}
	if err == nil {
		s.event(ctx, p.UserID, "recovery_request_"+status, c, nil)
	}
	return status, err
}

func (s *Service) CompleteDeviceRecovery(ctx context.Context, c Client, id, password string) (*TokenSet, error) {
	if err := s.take(ctx, RuleResetIP, c.ipString()); err != nil {
		return nil, err
	}
	if len(id) < 32 || len(id) > 128 {
		return nil, ErrRecoveryKey
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	r, err := s.store.RecoveryRequests().Get(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) || err == nil && (r.Status != "approved" || !r.ExpiresAt.After(s.now())) {
		return nil, ErrRecoveryKey
	}
	if err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	key, keyHash := s.newRecoveryKey()
	reset, err := s.store.RecoveryRequests().Complete(ctx, r, hash, keyHash, s.recoveryApprovalMaxAge)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrConflict) {
		return nil, ErrRecoveryKey
	}
	if err != nil {
		return nil, err
	}
	return s.recoveredSession(ctx, c, reset, hash, key)
}

func (s *Service) recoveredSession(ctx context.Context, c Client, reset postgres.PasswordReset, passwordHash, key string) (*TokenSet, error) {
	notify := s.onRevoked
	if s.onRecovery != nil {
		notify = s.onRecovery
	}
	if err := s.revokeInCacheWithNotifier(ctx, notify, reset.RevokedSessions...); err != nil {
		return nil, err
	}
	u, err := s.store.Users().Get(ctx, reset.UserID)
	if err != nil {
		return nil, err
	}
	tokens, err := s.startSession(ctx, u, c, digest(passwordHash))
	if err != nil {
		return nil, err
	}
	tokens.RecoveryKey = key
	s.event(ctx, reset.UserID, "password_reset_completed", c, nil)
	return tokens, nil
}
