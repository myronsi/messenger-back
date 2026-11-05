package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func (s *Service) BeginPasskeyLogin(ctx context.Context, c Client, username string) (PasskeyOptions, error) {
	if s.passkeys == nil {
		return PasskeyOptions{}, ErrPasskeysUnavailable
	}
	if len(username) > 64 {
		return PasskeyOptions{}, ErrInvalidCredentials
	}
	if err := s.take(ctx, RulePasskeyBegin, c.ipString()); err != nil {
		return PasskeyOptions{}, err
	}
	options, session, err := s.passkeys.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return PasskeyOptions{}, err
	}
	session.Expires = s.now().Add(5 * time.Minute)
	id, err := randomToken(32)
	if err != nil {
		return PasskeyOptions{}, err
	}
	payload, err := json.Marshal(passkeyCeremony{Session: *session, Username: normalizeUsername(username)})
	if err != nil {
		return PasskeyOptions{}, err
	}
	err = s.store.Passkeys().Challenge(ctx, postgres.WebAuthnChallenge{Hash: s.recoveryHash(id), Purpose: "authentication", Payload: payload, ExpiresAt: session.Expires})
	return PasskeyOptions{ChallengeID: id, PublicKey: options.Response}, err
}

func (s *Service) FinishPasskeyLogin(ctx context.Context, c Client, id string, response json.RawMessage) (*TokenSet, error) {
	if s.passkeys == nil {
		return nil, ErrPasskeysUnavailable
	}
	if err := s.take(ctx, RuleLoginIP, c.ipString()); err != nil {
		return nil, err
	}
	if len(id) < 32 || len(id) > 128 {
		return nil, ErrInvalidCredentials
	}
	challenge, err := s.store.Passkeys().TakeChallenge(ctx, s.recoveryHash(id), "authentication", nil, nil)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	var ceremony passkeyCeremony
	if err = json.Unmarshal(challenge.Payload, &ceremony); err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	var stored postgres.Passkey
	var selected *passkeyUser
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		uid, err := s.store.Passkeys().UserByHandle(ctx, handle)
		if err != nil {
			return nil, ErrInvalidCredentials
		}
		stored, err = s.store.Passkeys().Get(ctx, rawID)
		if err != nil || stored.UserID != uid {
			return nil, ErrInvalidCredentials
		}
		selected, err = s.loadPasskeyUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(handle, selected.handle) || (ceremony.Username != "" && normalizeUsername(selected.user.Username) != ceremony.Username) {
			return nil, ErrInvalidCredentials
		}
		return selected, nil
	}
	_, credential, err := s.passkeys.ValidatePasskeyLogin(handler, ceremony.Session, parsed)
	if err != nil || credential == nil || selected == nil || !credential.Flags.UserVerified {
		return nil, ErrInvalidCredentials
	}
	if credential.Authenticator.CloneWarning {
		s.event(ctx, stored.UserID, "passkey_counter_warning", c, nil)
		return nil, ErrInvalidCredentials
	}
	sealed, err := s.sealCredential(stored.UserID, credential)
	if err != nil {
		return nil, err
	}
	credentials, err := s.store.Users().CredentialsByID(ctx, stored.UserID)
	if err != nil {
		return nil, err
	}
	proof := &postgres.PasskeyLoginProof{ID: stored.ID, ExpectedCredential: stored.Credential, Credential: sealed}
	tokens, err := s.startSessionWithRecoveryCode(ctx, selected.user, c, digest(credentials.PasswordHash), "", proof)
	if err == nil {
		s.event(ctx, stored.UserID, "passkey_login", c, nil)
	}
	return tokens, err
}
