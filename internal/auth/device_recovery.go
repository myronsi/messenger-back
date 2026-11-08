package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/myronsi/messenger-back/internal/geoip"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var (
	ErrRecoveryRequestNotFound  = errors.New("recovery request not found or no longer pending")
	ErrUserVerificationRequired = errors.New("approval needs a passkey with user verification")
	ErrApprovalSessionTooNew    = errors.New("session too new to approve a recovery request")
)

const recoveryApprovalPurpose = "recovery_approval"

type RecoveryApproval struct {
	Match       int
	Code        string
	ChallengeID string
	Credential  json.RawMessage
}

type RecoveryApprovalOptions struct {
	Method         string     `json:"method"`
	ChallengeID    string     `json:"challenge_id,omitempty"`
	PublicKey      any        `json:"public_key,omitempty"`
	ApprovableFrom *time.Time `json:"approvable_from,omitempty"`
}

type RecoveryRequestResult struct {
	ID        string    `json:"request_id"`
	Match     int       `json:"match_number"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RecoveryRequestNotice struct {
	ID        string          `json:"request_id"`
	UserAgent string          `json:"user_agent"`
	Location  *geoip.Location `json:"location"`
	Choices   []int           `json:"choices"`
	ExpiresAt time.Time       `json:"expires_at"`
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
	approval, err := randomToken(32)
	if err != nil {
		return RecoveryRequestResult{}, err
	}
	result := RecoveryRequestResult{ID: id, Match: choices[index.Int64()], ExpiresAt: s.now().Add(10 * time.Minute)}
	location := s.geo.Lookup(c.IP)
	request := postgres.DeviceRecovery{Hash: s.recoveryHash(id), ApprovalHash: s.recoveryHash(approval), Match: result.Match, Choices: choices, UserAgent: cleanUserAgent(c.UserAgent), Location: location, ExpiresAt: result.ExpiresAt}
	u, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		return result, err
	}
	if err == nil {
		request.UserID = u.ID
	}
	created, err := s.store.RecoveryRequests().Create(ctx, request)
	if err != nil {
		return result, err
	}
	if created && s.onRecoveryRequest != nil {
		s.onRecoveryRequest(ctx, u.ID, RecoveryRequestNotice{ID: approval, Choices: choices, UserAgent: request.UserAgent, Location: location, ExpiresAt: result.ExpiresAt})
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

func (s *Service) pendingApproval(ctx context.Context, p Principal, id string) (postgres.DeviceRecovery, error) {
	if len(id) < 32 || len(id) > 128 {
		return postgres.DeviceRecovery{}, ErrRecoveryRequestNotFound
	}
	r, err := s.store.RecoveryRequests().GetForApproval(ctx, s.recoveryHash(id))
	if errors.Is(err, postgres.ErrNotFound) || err == nil && (r.UserID != p.UserID || r.Status != "pending" || !r.ExpiresAt.After(s.now())) {
		return postgres.DeviceRecovery{}, ErrRecoveryRequestNotFound
	}
	return r, err
}

func (s *Service) approverHasPasskeys(ctx context.Context, uid int64) (bool, error) {
	if s.passkeys == nil {
		return false, nil
	}
	keys, err := s.store.Passkeys().List(ctx, uid)
	return len(keys) > 0, err
}

func (s *Service) approvableFrom(ctx context.Context, p Principal) (time.Time, error) {
	session, err := s.store.Sessions().Get(ctx, p.SessionID)
	if errors.Is(err, postgres.ErrNotFound) || err == nil && (session.UserID != p.UserID || !session.Active(s.now())) {
		return time.Time{}, ErrApprovalSessionStale
	}
	return session.CreatedAt.Add(s.recoveryApprovalMinSessionAge), err
}

func (s *Service) DeviceRecoveryApprovalOptions(ctx context.Context, c Client, p Principal, id string) (RecoveryApprovalOptions, error) {
	r, err := s.pendingApproval(ctx, p, id)
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	withPasskey, err := s.approverHasPasskeys(ctx, p.UserID)
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	if !withPasskey {
		from, err := s.approvableFrom(ctx, p)
		if err != nil {
			return RecoveryApprovalOptions{}, err
		}
		out := RecoveryApprovalOptions{Method: "session"}
		if from.After(s.now()) {
			from = from.UTC()
			out.ApprovableFrom = &from
		}
		return out, nil
	}
	if err = s.take(ctx, RulePasskeyBegin, c.ipString()); err != nil {
		return RecoveryApprovalOptions{}, err
	}
	user, err := s.loadPasskeyUser(ctx, p.UserID)
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	options, session, err := s.passkeys.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	session.Expires = s.now().Add(5 * time.Minute)
	if r.ExpiresAt.Before(session.Expires) {
		session.Expires = r.ExpiresAt
	}
	challenge, err := randomToken(32)
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	payload, err := json.Marshal(passkeyCeremony{Session: *session, Request: r.Hash})
	if err != nil {
		return RecoveryApprovalOptions{}, err
	}
	err = s.store.Passkeys().Challenge(ctx, postgres.WebAuthnChallenge{Hash: s.recoveryHash(challenge), Purpose: recoveryApprovalPurpose, UserID: &p.UserID, SessionID: &p.SessionID, Payload: payload, ExpiresAt: session.Expires})
	return RecoveryApprovalOptions{Method: "passkey", ChallengeID: challenge, PublicKey: options.Response}, err
}

func (s *Service) verifyApprovalPasskey(ctx context.Context, c Client, p Principal, request string, a RecoveryApproval) (*postgres.PasskeyLoginProof, error) {
	if a.ChallengeID == "" || len(a.Credential) == 0 {
		return nil, ErrUserVerificationRequired
	}
	if len(a.ChallengeID) < 32 {
		return nil, ErrInvalidChallenge
	}
	challenge, err := s.store.Passkeys().TakeChallenge(ctx, s.recoveryHash(a.ChallengeID), recoveryApprovalPurpose, &p.UserID, &p.SessionID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, ErrInvalidChallenge
	}
	if err != nil {
		return nil, err
	}
	var ceremony passkeyCeremony
	if err = json.Unmarshal(challenge.Payload, &ceremony); err != nil {
		return nil, err
	}
	if ceremony.Request != request {
		return nil, ErrInvalidChallenge
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(a.Credential)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	user, err := s.loadPasskeyUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	credential, err := s.passkeys.ValidateLogin(user, ceremony.Session, parsed)
	if err != nil || credential == nil || !credential.Flags.UserVerified {
		return nil, ErrInvalidCredentials
	}
	if credential.Authenticator.CloneWarning {
		s.event(ctx, p.UserID, "passkey_counter_warning", c, nil)
		return nil, ErrInvalidCredentials
	}
	stored, err := s.store.Passkeys().Get(ctx, credential.ID)
	if errors.Is(err, postgres.ErrNotFound) || err == nil && stored.UserID != p.UserID {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealCredential(p.UserID, credential)
	if err != nil {
		return nil, err
	}
	return &postgres.PasskeyLoginProof{ID: stored.ID, ExpectedCredential: stored.Credential, Credential: sealed}, nil
}

func (s *Service) DecideDeviceRecovery(ctx context.Context, c Client, p Principal, id string, a RecoveryApproval, deny bool) (string, error) {
	if len(a.Code) > 128 || len(a.ChallengeID) > 128 || len(a.Credential) > 64*1024 {
		return "", ErrRecoveryRequestNotFound
	}
	r, err := s.pendingApproval(ctx, p, id)
	if err != nil {
		return "", err
	}
	st, err := s.store.SecuritySettings().Get(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	decision := postgres.RecoveryDecision{SessionID: p.SessionID, Match: a.Match, Deny: deny, MaxAge: s.recoveryApprovalMaxAge, MinSessionAge: s.recoveryApprovalMinSessionAge, PasskeysEnabled: s.passkeys != nil}
	decision.Proof = postgres.RecoveryProof{TwoFactorEnabled: st.TwoFactorEnabled, TwoFactorSecret: st.TwoFactorSecret}
	if !deny && r.Match == a.Match {
		if err = s.verifyApprover(ctx, c, p, r, a, &decision); err != nil {
			return "", err
		}
		if decision.Proof.BackupCodeHash, err = s.limitedRecoveryFactor(ctx, p.UserID, st, a.Code); err != nil {
			return "", err
		}
	}
	status, err := s.store.RecoveryRequests().Decide(ctx, r.Hash, p.UserID, decision)
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		return "", ErrRecoveryRequestNotFound
	case errors.Is(err, postgres.ErrStaleApproval):
		return "", ErrApprovalSessionStale
	case errors.Is(err, postgres.ErrVerificationRequired):
		return "", ErrUserVerificationRequired
	case errors.Is(err, postgres.ErrSessionTooNew):
		return "", ErrApprovalSessionTooNew
	case errors.Is(err, postgres.ErrInvalid):
		return "", ErrRecoveryKey
	case errors.Is(err, postgres.ErrConflict):
		return "", ErrConflict
	case err == nil:
		var details map[string]any
		if status == "approved" {
			details = map[string]any{"verification": "session"}
			if decision.Passkey != nil {
				details["verification"] = "passkey"
			}
		}
		s.event(ctx, p.UserID, "recovery_request_"+status, c, details)
	}
	return status, err
}

func (s *Service) verifyApprover(ctx context.Context, c Client, p Principal, r postgres.DeviceRecovery, a RecoveryApproval, decision *postgres.RecoveryDecision) error {
	creds, err := s.store.Users().CredentialsByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	decision.Proof.PasswordHash = creds.PasswordHash
	withPasskey, err := s.approverHasPasskeys(ctx, p.UserID)
	if err != nil {
		return err
	}
	if withPasskey {
		decision.Passkey, err = s.verifyApprovalPasskey(ctx, c, p, r.Hash, a)
		return err
	}
	from, err := s.approvableFrom(ctx, p)
	if err != nil {
		return err
	}
	if from.After(s.now()) {
		return ErrApprovalSessionTooNew
	}
	return nil
}

func (s *Service) CancelDeviceRecovery(ctx context.Context, c Client, id string) error {
	if err := s.take(ctx, RuleRecoveryPoll, c.ipString()); err != nil {
		return err
	}
	if len(id) < 32 || len(id) > 128 {
		return nil
	}
	return s.store.RecoveryRequests().Cancel(ctx, s.recoveryHash(id))
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
