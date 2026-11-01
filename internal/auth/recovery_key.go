package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var ErrRecoveryLocked = postgres.ErrRecoveryLocked
var ErrRecoveryKey = errors.New("invalid username, recovery key or second factor")

// NewRecoveryKey generates 160 random bits, grouped for printing/password managers.
// Plaintext keys are returned once to the owner and never persisted or logged.
func NewRecoveryKey() string {
	raw := make([]byte, 20)
	rand.Read(raw)
	text := base32.StdEncoding.EncodeToString(raw)
	groups := make([]string, 0, 8)
	for i := 0; i < len(text); i += 4 {
		groups = append(groups, text[i:i+4])
	}
	return strings.Join(groups, "-")
}

func normalizeRecoveryKey(key string) (string, bool) {
	if len(key) > 128 {
		return "", false
	}
	key = strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "", "\r", "").Replace(key))
	if len(key) != 32 {
		return "", false
	}
	for _, c := range key {
		if !(c >= 'A' && c <= 'Z' || c >= '2' && c <= '7') {
			return "", false
		}
	}
	return key, true
}

func (s *Service) recoveryKeyHash(key string) string {
	h := hmac.New(sha256.New, s.pepper)
	h.Write([]byte("account-recovery-key:v1:" + key))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) newRecoveryKey() (string, string) {
	key := NewRecoveryKey()
	normalized, _ := normalizeRecoveryKey(key)
	return key, s.recoveryKeyHash(normalized)
}

type RecoveryKeyStatus struct {
	Exists      bool       `json:"exists"`
	CreatedAt   *time.Time `json:"created_at"`
	LockedUntil *time.Time `json:"locked_until"`
}

func (s *Service) RecoveryKeyStatus(ctx context.Context, p Principal) (RecoveryKeyStatus, error) {
	key, err := s.store.RecoveryKeys().Get(ctx, p.UserID)
	return RecoveryKeyStatus{key.Hash != nil, key.CreatedAt, key.LockedUntil}, err
}

// recoveryFactor leaves backup-code consumption to the password/key transaction.
// The sealed secret is compared again under lock so concurrent 2FA changes cannot
// downgrade the proof. TOTP replay claims are deliberately fail-closed.
func (s *Service) recoveryFactor(ctx context.Context, uid int64, st postgres.SecuritySettings, code string) (*string, error) {
	if !st.TwoFactorEnabled {
		return nil, nil
	}
	if IsRecoveryCode(code) {
		hash := HashRecoveryCode(code)
		return &hash, nil
	}
	ok, _, err := s.checkSecondFactor(ctx, uid, st, code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRecoveryKey
	}
	return nil, nil
}

func (s *Service) ReplaceRecoveryKey(ctx context.Context, c Client, p Principal, password, code string) (string, error) {
	if err := s.recoveryChangesAllowed(ctx, p.UserID); err != nil {
		return "", err
	}
	if err := s.take(ctx, RuleTwoFAUser, fmt.Sprint(p.UserID)); err != nil {
		return "", err
	}
	hash, err := s.verifyPassword(ctx, p.UserID, password)
	if err != nil {
		return "", err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	backup, err := s.recoveryFactor(ctx, p.UserID, st, code)
	if err != nil {
		return "", err
	}
	key, keyHash := s.newRecoveryKey()
	proof := postgres.RecoveryProof{PasswordHash: hash, TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret, BackupCodeHash: backup}
	if err = s.store.RecoveryKeys().Replace(ctx, p.UserID, proof, keyHash); err != nil {
		if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrConflict) {
			return "", ErrRecoveryKey
		}
		return "", err
	}
	s.event(ctx, p.UserID, "recovery_key_changed", c, nil)
	return key, nil
}

// RecoverWithKey has one public failure for unknown accounts, malformed/used keys
// and absent/wrong second factors. A replacement key is issued atomically with
// the reset so a lost response never leaves a consumed key usable by an attacker.
func (s *Service) RecoverWithKey(ctx context.Context, c Client, username, key, newPassword, code string) (*TokenSet, error) {
	username = normalizeUsername(username)
	if len(username) > 64 || len(code) > 128 {
		return nil, ErrRecoveryKey
	}
	if _, err := s.allow(ctx, limitCheck{RuleRecoverIP, c.ipString()}, limitCheck{RuleRecoverUsr, username}); err != nil {
		return nil, err
	}
	if err := validatePassword(newPassword); err != nil {
		return nil, err
	}
	normalized, valid := normalizeRecoveryKey(key)
	u, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		return nil, err
	}
	known := err == nil
	stored := postgres.RecoveryKey{}
	if known {
		stored, err = s.store.RecoveryKeys().Get(ctx, u.ID)
		if err != nil {
			return nil, err
		}
	}
	want := strings.Repeat("0", 64)
	if stored.Hash != nil {
		want = *stored.Hash
	}
	given := s.recoveryKeyHash(normalized)
	match := hmac.Equal([]byte(given), []byte(want))
	if !known || !valid || !match {
		return nil, ErrRecoveryKey
	}
	st, err := s.store.SecuritySettings().Get(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	backup, err := s.recoveryFactor(ctx, u.ID, st, code)
	if err != nil {
		return nil, err
	}
	passwordHash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return nil, err
	}
	replacement, replacementHash := s.newRecoveryKey()
	proof := postgres.RecoveryProof{TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret, BackupCodeHash: backup}
	reset, err := s.store.RecoveryKeys().Reset(ctx, u.ID, want, proof, passwordHash, replacementHash)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrConflict) {
		return nil, ErrRecoveryKey
	}
	if err != nil {
		return nil, err
	}
	if s.onRecovery != nil {
		s.onRecovery(ctx, u.ID)
	}
	if err = s.revokeInCache(ctx, reset.RevokedSessions...); err != nil {
		return nil, err
	}
	tokens, err := s.startSession(ctx, u, c, digest(passwordHash))
	if err != nil {
		return nil, err
	}
	tokens.RecoveryKey = replacement
	return tokens, nil
}

func (s *Service) SecurityEvents(ctx context.Context, p Principal) ([]postgres.SecurityEvent, error) {
	return s.store.SecurityEvents().List(ctx, p.UserID, nil, 50)
}

// Check before consuming one-time factor proofs; the transaction also checks under lock.
func (s *Service) recoveryChangesAllowed(ctx context.Context, uid int64) error {
	key, err := s.store.RecoveryKeys().Get(ctx, uid)
	if err != nil {
		return err
	}
	if key.LockedUntil != nil && key.LockedUntil.After(time.Now()) {
		return ErrRecoveryLocked
	}
	return nil
}
