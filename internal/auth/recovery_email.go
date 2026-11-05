package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var ErrRecoveryEmailUnavailable = errors.New("recovery email is not configured")

type RecoveryEmailStatus struct {
	Address string `json:"address"`
	State   string `json:"state"`
	Enabled bool   `json:"enabled"`
}
type recoveryMailPayload struct {
	UserID              int64
	Address, Identifier string
}

func normalizeRecoveryEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", &ValidationError{"email", "must be a valid email address"}
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", &ValidationError{"email", "must be a valid email address"}
	}
	return value, nil
}
func (s *Service) RecoveryEmailStatus(ctx context.Context, p Principal) (RecoveryEmailStatus, error) {
	out := RecoveryEmailStatus{State: "not_added", Enabled: s.recoveryMailEnabled}
	email, err := s.store.RecoveryEmails().Get(ctx, p.UserID)
	if errors.Is(err, postgres.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Address = email.Address
	out.State = "pending"
	if email.VerifiedAt != nil {
		out.State = "verified"
	}
	return out, nil
}
func (s *Service) recoveryChangeProof(ctx context.Context, p Principal, password, code string) (postgres.RecoveryProof, error) {
	if err := s.recoveryChangesAllowed(ctx, p.UserID); err != nil {
		return postgres.RecoveryProof{}, err
	}
	hash, err := s.verifyPassword(ctx, p.UserID, password)
	if err != nil {
		return postgres.RecoveryProof{}, err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return postgres.RecoveryProof{}, err
	}
	backup, err := s.recoveryFactor(ctx, p.UserID, st, code)
	return postgres.RecoveryProof{PasswordHash: hash, TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret, BackupCodeHash: backup}, err
}
func (s *Service) SetRecoveryEmail(ctx context.Context, c Client, p Principal, address, password, code string) error {
	if !s.recoveryMailEnabled {
		return ErrRecoveryEmailUnavailable
	}
	address, err := normalizeRecoveryEmail(address)
	if err != nil {
		return err
	}
	if _, err = s.allow(ctx, limitCheck{RuleEmailAccount, fmt.Sprint(p.UserID)}, limitCheck{RuleEmailAddress, address}); err != nil {
		return err
	}
	proof, err := s.recoveryChangeProof(ctx, p, password, code)
	if err != nil {
		return err
	}
	payload, err := s.sealedMailPayload("verify_email", recoveryMailPayload{UserID: p.UserID, Address: address})
	if err != nil {
		return err
	}
	err = s.store.RecoveryEmails().Set(ctx, p.UserID, proof, address, payload)
	if errors.Is(err, postgres.ErrConflict) {
		return ErrConflict
	}
	if err == nil {
		s.event(ctx, p.UserID, "recovery_email_changed", c, nil)
	}
	return err
}
func (s *Service) RemoveRecoveryEmail(ctx context.Context, c Client, p Principal, password, code string) error {
	proof, err := s.recoveryChangeProof(ctx, p, password, code)
	if err != nil {
		return err
	}
	err = s.store.RecoveryEmails().Remove(ctx, p.UserID, proof)
	if err == nil {
		s.event(ctx, p.UserID, "recovery_email_removed", c, nil)
	}
	return err
}
func (s *Service) sealedMailPayload(kind string, payload recoveryMailPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.sealer.Seal(string(raw), "recovery-mail:"+kind)
}

// Queue every submitted identifier without looking up an account. The background
// worker decides whether a verified address exists; the public response cannot
// distinguish accounts by provider latency or by work performed on this path.
func (s *Service) RequestEmailReset(ctx context.Context, c Client, identifier string) error {
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if len(identifier) > 254 {
		identifier = ""
	}
	if _, err := s.allow(ctx, limitCheck{RuleRecoverIP, c.ipString()}, limitCheck{RuleEmailAddress, identifier}); err != nil {
		return err
	}
	if !s.recoveryMailEnabled {
		return nil
	}
	payload, err := s.sealedMailPayload("password_reset", recoveryMailPayload{Identifier: identifier})
	if err != nil {
		return err
	}
	return s.store.RecoveryEmails().Queue(ctx, "password_reset", payload)
}
func (s *Service) VerifyRecoveryEmail(ctx context.Context, c Client, token string) error {
	if err := s.take(ctx, RuleEmailVerify, c.ipString()); err != nil {
		return err
	}
	if len(token) < 32 || len(token) > 128 {
		return ErrRecoveryKey
	}
	err := s.store.RecoveryEmails().Verify(ctx, s.recoveryHash(token))
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrRecoveryKey
	}
	return err
}
func (s *Service) ResetWithEmail(ctx context.Context, c Client, token, password, code string) (*TokenSet, error) {
	if err := s.take(ctx, RuleResetIP, c.ipString()); err != nil {
		return nil, err
	}
	if len(token) < 32 || len(token) > 128 || len(code) > 128 {
		return nil, ErrRecoveryKey
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	email, err := s.store.RecoveryEmails().Token(ctx, s.recoveryHash(token), "password_reset")
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, ErrRecoveryKey
	}
	if err != nil {
		return nil, err
	}
	if err = s.take(ctx, RuleRecoverCap, fmt.Sprint(email.UserID)); err != nil {
		return nil, err
	}
	st, err := s.store.SecuritySettings().Get(ctx, email.UserID)
	if err != nil {
		return nil, err
	}
	backup, err := s.recoveryFactor(ctx, email.UserID, st, code)
	if err != nil {
		return nil, err
	}
	credentials, err := s.store.Users().CredentialsByID(ctx, email.UserID)
	if err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return nil, err
	}
	key, keyHash := s.newRecoveryKey()
	proof := postgres.RecoveryProof{PasswordHash: credentials.PasswordHash, TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret, BackupCodeHash: backup}
	reset, err := s.store.RecoveryEmails().Reset(ctx, email.UserID, s.recoveryHash(token), proof, hash, keyHash)
	if errors.Is(err, postgres.ErrConflict) || errors.Is(err, postgres.ErrNotFound) {
		return nil, ErrRecoveryKey
	}
	if err != nil {
		return nil, err
	}
	return s.recoveredSession(ctx, c, reset, hash, key)
}

type RecoveryOverview struct {
	Key             RecoveryKeyStatus   `json:"recovery_key"`
	OtherDevices    int                 `json:"other_devices"`
	Passkeys        int                 `json:"passkeys"`
	PasskeysEnabled bool                `json:"passkeys_enabled"`
	Email           RecoveryEmailStatus `json:"email"`
}

func (s *Service) RecoveryOverview(ctx context.Context, p Principal) (RecoveryOverview, error) {
	out := RecoveryOverview{PasskeysEnabled: s.passkeys != nil}
	var err error
	out.Key, err = s.RecoveryKeyStatus(ctx, p)
	if err != nil {
		return out, err
	}
	sessions, err := s.store.Sessions().ListActive(ctx, p.UserID)
	if err != nil {
		return out, err
	}
	for _, session := range sessions {
		if session.ID != p.SessionID && session.LastActiveAt.After(s.now().Add(-s.recoveryApprovalMaxAge)) {
			out.OtherDevices++
		}
	}
	keys, err := s.store.Passkeys().List(ctx, p.UserID)
	if err != nil {
		return out, err
	}
	out.Passkeys = len(keys)
	out.Email, err = s.RecoveryEmailStatus(ctx, p)
	return out, err
}

func (s *Service) processRecoveryMail(ctx context.Context, job postgres.RecoveryMailJob, send func(context.Context, string, string, string) error) error {
	plain, err := s.sealer.Open(job.Payload, "recovery-mail:"+job.Kind)
	if err != nil {
		return err
	}
	var payload recoveryMailPayload
	if err = json.Unmarshal([]byte(plain), &payload); err != nil {
		return err
	}
	var email postgres.RecoveryEmail
	if job.Kind == "password_reset" {
		email, err = s.store.RecoveryEmails().FindVerified(ctx, payload.Identifier)
	} else {
		email, err = s.store.RecoveryEmails().Get(ctx, payload.UserID)
		if err == nil && (email.Address != payload.Address || email.VerifiedAt != nil) {
			return nil
		}
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := randomToken(32)
	if err != nil {
		return err
	}
	if err = s.store.RecoveryEmails().IssueToken(ctx, email, s.recoveryHash(token), job.Kind, s.now().Add(30*time.Minute)); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return nil
		}
		return err
	}
	page, subject := "verify-email", "Verify your Messenger recovery email"
	if job.Kind == "password_reset" {
		page, subject = "reset-password", "Reset your Messenger password"
	}
	link := strings.TrimRight(s.recoveryWebURL, "/") + "/" + page + "?token=" + token
	body := "Open this link to continue:\n\n" + link + "\n\nThis link expires in 30 minutes and works once. If you did not request this, ignore this email. Never share this link.\n"
	return send(ctx, email.Address, subject, body)
}

func (s *Service) RunRecoveryMail(ctx context.Context, send func(context.Context, string, string, string) error) {
	if !s.recoveryMailEnabled {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		job, err := s.store.RecoveryEmails().Claim(ctx)
		if err == nil {
			err = s.processRecoveryMail(ctx, job, send)
			done := err == nil || job.Attempts >= 5
			if err != nil {
				s.log.WarnContext(ctx, "recovery mail job failed", "job_id", job.ID, "attempt", job.Attempts)
			}
			if ack := s.store.RecoveryEmails().FinishJob(ctx, job.ID, done, time.Duration(1<<min(job.Attempts, 6))*30*time.Second); ack != nil {
				s.log.WarnContext(ctx, "recovery mail job acknowledgement failed", "job_id", job.ID)
			}
			continue
		}
		if !errors.Is(err, postgres.ErrNotFound) && ctx.Err() == nil {
			s.log.WarnContext(ctx, "recovery mail queue unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
