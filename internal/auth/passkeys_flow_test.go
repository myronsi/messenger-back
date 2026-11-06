package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const softOrigin = "http://localhost:4173"

type softAuthenticator struct {
	t     *testing.T
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{t: t, key: key, id: id}
}

func softEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *softAuthenticator) clientData(kind string, challenge []byte) []byte {
	raw, err := json.Marshal(map[string]any{"type": kind, "challenge": softEncode(challenge), "origin": softOrigin, "crossOrigin": false})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func (a *softAuthenticator) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte("localhost"))
	out := append([]byte{}, rp[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.count)
	return append(out, attested...)
}

func (a *softAuthenticator) register(options PasskeyOptions) json.RawMessage {
	creation, ok := options.PublicKey.(protocol.PublicKeyCredentialCreationOptions)
	if !ok {
		a.t.Fatalf("unexpected registration options %T", options.PublicKey)
	}
	pub, err := a.key.PublicKey.Bytes()
	if err != nil {
		a.t.Fatal(err)
	}
	cose, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: pub[1:33], -3: pub[33:65]})
	if err != nil {
		a.t.Fatal(err)
	}
	attested := make([]byte, 16)
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.id)))
	attested = append(attested, a.id...)
	attested = append(attested, cose...)
	object, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(0x45, attested)})
	if err != nil {
		a.t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"id": softEncode(a.id), "rawId": softEncode(a.id), "type": "public-key", "response": map[string]string{
		"clientDataJSON":    softEncode(a.clientData("webauthn.create", creation.Challenge)),
		"attestationObject": softEncode(object),
	}})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func (a *softAuthenticator) assert(options PasskeyOptions, handle []byte) json.RawMessage {
	request, ok := options.PublicKey.(protocol.PublicKeyCredentialRequestOptions)
	if !ok {
		a.t.Fatalf("unexpected login options %T", options.PublicKey)
	}
	data := a.authData(0x05, nil)
	client := a.clientData("webauthn.get", request.Challenge)
	clientHash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, data...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"id": softEncode(a.id), "rawId": softEncode(a.id), "type": "public-key", "response": map[string]string{
		"clientDataJSON":    softEncode(client),
		"authenticatorData": softEncode(data),
		"signature":         softEncode(signature),
		"userHandle":        softEncode(handle),
	}})
	if err != nil {
		a.t.Fatal(err)
	}
	return raw
}

func storedSignCount(t *testing.T, e *env, uid int64, id []byte) uint32 {
	t.Helper()
	user, err := e.svc.loadPasskeyUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range user.credentials {
		if string(credential.ID) == string(id) {
			return credential.Authenticator.SignCount
		}
	}
	t.Fatal("registered credential missing")
	return 0
}

func TestPasskeyRegistrationAndLoginEndToEnd(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.client("10.0.0.98")
	owner := e.register(t, "passkey_flow", "correct horse")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{softOrigin})
	if err != nil {
		t.Fatal(err)
	}
	device := newSoftAuthenticator(t)
	options, err := e.svc.BeginPasskeyRegistration(ctx, c, principal(owner), "correct horse", "", "Software key")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.FinishPasskeyRegistration(ctx, c, principal(owner), options.ChallengeID, device.register(options)); err != nil {
		t.Fatal("registration failed", err)
	}
	keys, err := e.svc.ListPasskeys(ctx, principal(owner))
	if err != nil || len(keys) != 1 || keys[0].Name != "Software key" || keys[0].ID != softEncode(device.id) || keys[0].LastUsedAt != nil {
		t.Fatal("registered passkey missing", keys, err)
	}
	var handle []byte
	if err = e.store.Pool().QueryRow(ctx, `SELECT handle FROM user_webauthn_handles WHERE user_id=$1`, owner.User.ID).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	login := func(username string) (*TokenSet, error) {
		options, err := e.svc.BeginPasskeyLogin(ctx, c, username)
		if err != nil {
			t.Fatal(err)
		}
		return e.svc.FinishPasskeyLogin(ctx, c, options.ChallengeID, device.assert(options, handle))
	}
	device.count = 1
	tokens, err := login("passkey_flow")
	if err != nil {
		t.Fatal("passkey login failed", err)
	}
	if tokens.User.ID != owner.User.ID || tokens.AccessToken == "" {
		t.Fatal("passkey login returned the wrong session", tokens.User.ID)
	}
	if _, err = e.svc.Authenticate(ctx, tokens.AccessToken); err != nil {
		t.Fatal("passkey session rejected", err)
	}
	if got := storedSignCount(t, e, owner.User.ID, device.id); got != 1 {
		t.Fatal("sign count not updated", got)
	}
	if keys, err = e.svc.ListPasskeys(ctx, principal(owner)); err != nil || keys[0].LastUsedAt == nil {
		t.Fatal("last use not recorded", err)
	}
	if _, err = login(""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("replayed sign count accepted", err)
	}
	if got := storedSignCount(t, e, owner.User.ID, device.id); got != 1 {
		t.Fatal("rejected login changed the sign count", got)
	}
	var warnings int
	if err = e.store.Pool().QueryRow(ctx, `SELECT count(*) FROM user_security_events WHERE user_id=$1 AND event_type='passkey_counter_warning'`, owner.User.ID).Scan(&warnings); err != nil || warnings != 1 {
		t.Fatal("replay was not rejected as a counter warning", warnings, err)
	}
	if _, err = login("someone_else"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("passkey accepted for another username", err)
	}
	device.count = 7
	if _, err = login(""); err != nil {
		t.Fatal("discoverable passkey login failed", err)
	}
	if got := storedSignCount(t, e, owner.User.ID, device.id); got != 7 {
		t.Fatal("sign count not advanced", got)
	}
}

func TestPasskeyFailuresDoNotConsumePasswordLoginBudget(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.register(t, "passkey_fail_budget", "correct horse")
	c := e.client("10.0.0.99")
	var err error
	e.svc.passkeys, err = NewPasskeys("localhost", []string{softOrigin})
	if err != nil {
		t.Fatal(err)
	}
	for range RulePasskeyLoginIP.Limit {
		if _, err = e.svc.FinishPasskeyLogin(ctx, c, "short", json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	var limited *RateLimitError
	if _, err = e.svc.FinishPasskeyLogin(ctx, c, "short", json.RawMessage(`{}`)); !errors.As(err, &limited) {
		t.Fatal("passkey failures were not limited", err)
	}
	if _, err = e.svc.Login(ctx, c, "passkey_fail_budget", "correct horse"); err != nil {
		t.Fatal("passkey failures blocked password login", err)
	}
}

func TestPasskeyConcurrentDeletesKeepLastPasskey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	owner := e.register(t, "passkey_race", "correct horse")
	proof, err := e.svc.recoveryChangeProof(ctx, principal(owner), "correct horse", "")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"AQID", "BAUG"}
	for i, raw := range [][]byte{{1, 2, 3}, {4, 5, 6}} {
		if err = e.store.Passkeys().Add(ctx, postgres.Passkey{ID: raw, UserID: owner.User.ID, Credential: "sealed-test-placeholder", Name: ids[i]}, owner.SessionID, proof); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		var wg sync.WaitGroup
		errs := make([]error, len(ids))
		for i, id := range ids {
			raw := mustDecode(t, id)
			wg.Go(func() { errs[i] = e.store.Passkeys().Delete(ctx, owner.User.ID, raw, "") })
		}
		wg.Wait()
		keys, err := e.store.Passkeys().List(ctx, owner.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 1 || (errs[0] == nil) == (errs[1] == nil) {
			t.Fatal("concurrent deletes removed the last passkey without the password", len(keys), errs)
		}
		if !errors.Is(errs[0], postgres.ErrConflict) && !errors.Is(errs[1], postgres.ErrConflict) {
			t.Fatal("unexpected delete failure", errs)
		}
		removed := ids[0]
		if errs[0] != nil {
			removed = ids[1]
		}
		if err = e.store.Passkeys().Add(ctx, postgres.Passkey{ID: mustDecode(t, removed), UserID: owner.User.ID, Credential: "sealed-test-placeholder", Name: removed}, owner.SessionID, proof); err != nil {
			t.Fatal(err)
		}
	}
}

func mustDecode(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
