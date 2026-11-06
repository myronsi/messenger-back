package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

var ErrPasskeysUnavailable = errors.New("passkeys are not configured")

func NewPasskeys(rp string, origins []string) (*webauthn.WebAuthn, error) {
	if rp == "" && len(origins) == 0 {
		return nil, nil
	}
	if rp == "" || strings.ContainsAny(rp, "/: \r\n") || len(origins) == 0 {
		return nil, errors.New("RP_ID and RP_ORIGINS must be configured together")
	}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("RP_ORIGINS must contain frontend origins")
		}
		host := strings.ToLower(u.Hostname())
		local := host == "localhost"
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			local = true
		}
		if (u.Scheme != "https" && (u.Scheme != "http" || !local)) || (host != strings.ToLower(rp) && !strings.HasSuffix(host, "."+strings.ToLower(rp))) {
			return nil, errors.New("RP_ORIGINS must use HTTPS and belong to RP_ID")
		}
	}
	return webauthn.New(&webauthn.Config{RPID: rp, RPOrigins: origins, RPDisplayName: "Messenger",
		AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired, ResidentKey: protocol.ResidentKeyRequirementRequired}})
}

type passkeyUser struct {
	user        postgres.User
	handle      []byte
	credentials []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u *passkeyUser) WebAuthnName() string                       { return u.user.Username }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.user.DisplayName }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) loadPasskeyUser(ctx context.Context, uid int64) (*passkeyUser, error) {
	u, err := s.store.Users().Get(ctx, uid)
	if err != nil {
		return nil, err
	}
	proposed := make([]byte, 32)
	if _, err = rand.Read(proposed); err != nil {
		return nil, err
	}
	handle, err := s.store.Passkeys().Handle(ctx, uid, proposed)
	if err != nil {
		return nil, err
	}
	keys, err := s.store.Passkeys().List(ctx, uid)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(keys))
	for _, key := range keys {
		plain, err := s.sealer.Open(key.Credential, passkeyAAD(uid, key.ID))
		if err != nil {
			return nil, err
		}
		var credential webauthn.Credential
		if err = json.Unmarshal([]byte(plain), &credential); err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return &passkeyUser{user: u, handle: handle, credentials: credentials}, nil
}

func passkeyAAD(uid int64, id []byte) string {
	return fmt.Sprintf("passkey:%d:%s", uid, base64.RawURLEncoding.EncodeToString(id))
}
func (s *Service) sealCredential(uid int64, key *webauthn.Credential) (string, error) {
	raw, err := json.Marshal(key)
	if err != nil {
		return "", err
	}
	return s.sealer.Seal(string(raw), passkeyAAD(uid, key.ID))
}

type passkeyCeremony struct {
	Session  webauthn.SessionData
	Proof    postgres.PinnedProof
	Backup   *string
	Username string
}
type PasskeyOptions struct {
	ChallengeID string `json:"challenge_id"`
	PublicKey   any    `json:"public_key"`
}
type PasskeyInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func validPasskeyName(name string) (string, error) {
	name = cleanUserAgent(strings.TrimSpace(name))
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
		return "", &ValidationError{"name", "must contain 1 to 64 characters"}
	}
	return name, nil
}

func (s *Service) BeginPasskeyRegistration(ctx context.Context, c Client, p Principal, password, code, name string) (PasskeyOptions, error) {
	if s.passkeys == nil {
		return PasskeyOptions{}, ErrPasskeysUnavailable
	}
	name, err := validPasskeyName(name)
	if err != nil {
		return PasskeyOptions{}, err
	}
	proof, err := s.recoveryChangeProof(ctx, p, password, code)
	if err != nil {
		return PasskeyOptions{}, err
	}
	user, err := s.loadPasskeyUser(ctx, p.UserID)
	if err != nil {
		return PasskeyOptions{}, err
	}
	exclusions := make([]protocol.CredentialDescriptor, 0, len(user.credentials))
	for _, key := range user.credentials {
		exclusions = append(exclusions, key.Descriptor())
	}
	options, session, err := s.passkeys.BeginRegistration(user, webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired), webauthn.WithExclusions(exclusions))
	if err != nil {
		return PasskeyOptions{}, err
	}
	session.Expires = s.now().Add(5 * time.Minute)
	id, err := randomToken(32)
	if err != nil {
		return PasskeyOptions{}, err
	}
	payload, err := json.Marshal(passkeyCeremony{Session: *session, Proof: proof.Pin(), Backup: proof.BackupCodeHash})
	if err != nil {
		return PasskeyOptions{}, err
	}
	err = s.store.Passkeys().Challenge(ctx, postgres.WebAuthnChallenge{Hash: s.recoveryHash(id), Purpose: "registration", UserID: &p.UserID, SessionID: &p.SessionID, Payload: payload, Name: name, ExpiresAt: session.Expires})
	return PasskeyOptions{ChallengeID: id, PublicKey: options.Response}, err
}

func (s *Service) FinishPasskeyRegistration(ctx context.Context, c Client, p Principal, id string, response json.RawMessage) error {
	if s.passkeys == nil {
		return ErrPasskeysUnavailable
	}
	if len(id) < 32 || len(id) > 128 {
		return ErrInvalidChallenge
	}
	challenge, err := s.store.Passkeys().TakeChallenge(ctx, s.recoveryHash(id), "registration", &p.UserID, &p.SessionID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrInvalidChallenge
	}
	if err != nil {
		return err
	}
	var ceremony passkeyCeremony
	if err = json.Unmarshal(challenge.Payload, &ceremony); err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return ErrInvalidCredentials
	}
	user, err := s.loadPasskeyUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	credential, err := s.passkeys.CreateCredential(user, ceremony.Session, parsed)
	if err != nil {
		return ErrInvalidCredentials
	}
	if !credential.Flags.UserVerified {
		return ErrInvalidCredentials
	}
	sealed, err := s.sealCredential(p.UserID, credential)
	if err != nil {
		return err
	}
	err = s.store.Passkeys().Add(ctx, postgres.Passkey{ID: credential.ID, UserID: p.UserID, Credential: sealed, Name: challenge.Name}, p.SessionID, ceremony.Proof, ceremony.Backup)
	if errors.Is(err, postgres.ErrConflict) || errors.Is(err, postgres.ErrNotFound) {
		return ErrInvalidCredentials
	}
	if err == nil {
		s.event(ctx, p.UserID, "passkey_added", c, nil)
	}
	return err
}

func (s *Service) ListPasskeys(ctx context.Context, p Principal) ([]PasskeyInfo, error) {
	keys, err := s.store.Passkeys().List(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]PasskeyInfo, 0, len(keys))
	for _, key := range keys {
		out = append(out, PasskeyInfo{ID: base64.RawURLEncoding.EncodeToString(key.ID), Name: key.Name, CreatedAt: key.CreatedAt, LastUsedAt: key.LastUsedAt})
	}
	return out, nil
}

func (s *Service) RenamePasskey(ctx context.Context, c Client, p Principal, id, name string) error {
	name, err := validPasskeyName(name)
	if err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) > 2048 {
		return ErrInvalidCredentials
	}
	err = s.store.Passkeys().Rename(ctx, p.UserID, raw, name)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrSessionNotFound
	}
	if err == nil {
		s.event(ctx, p.UserID, "passkey_renamed", c, nil)
	}
	return err
}

func (s *Service) DeletePasskey(ctx context.Context, c Client, p Principal, id, password string) error {
	if err := s.recoveryChangesAllowed(ctx, p.UserID); err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(raw) > 2048 {
		return ErrInvalidCredentials
	}
	keys, err := s.store.Passkeys().List(ctx, p.UserID)
	if err != nil {
		return err
	}
	hash := ""
	if len(keys) <= 1 {
		hash, err = s.verifyPassword(ctx, p.UserID, password)
		if err != nil {
			return err
		}
	}
	err = s.store.Passkeys().Delete(ctx, p.UserID, raw, hash)
	if errors.Is(err, postgres.ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrSessionNotFound
	}
	if err == nil {
		s.event(ctx, p.UserID, "passkey_removed", c, nil)
	}
	return err
}
