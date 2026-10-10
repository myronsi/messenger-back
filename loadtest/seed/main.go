// Command seed prepares users for the load tests: it registers (or logs in) load_00000…, pairs them into direct
// chats and writes their access tokens and chats as JSON for the k6 scripts. It runs with the API's
// environment (DATABASE_URL, REDIS_URL, JWT_SECRET, …) against a test environment, never production.
//
//	go run ./loadtest/seed -users 1000 -out loadtest/users.json
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

// User is one line of the output.
type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	AccessToken string `json:"access_token"`
	// ChatID is the direct chat with the neighbour (users 2k and 2k+1 share one).
	ChatID string `json:"chat_id"`
	PeerID string `json:"peer_id"`
}

// password of every load test user.
const password = "load-test-password-not-secret"

func main() {
	n := flag.Int("users", 100, "how many users")
	out := flag.String("out", "loadtest/users.json", "where to write the users")
	flag.Parse()
	if err := run(*n, *out); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(n int, out string) error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Env == "production" {
		return errors.New("refusing to seed load test users into a production environment")
	}
	log := slog.New(slog.DiscardHandler)
	pg, err := postgres.New(ctx, cfg.DatabaseURL.Reveal(), postgres.Options{MaxConns: 4})
	if err != nil {
		return err
	}
	defer pg.Close()
	rd, err := redis.New(cfg.RedisURL.Reveal(), redis.Options{Timeout: cfg.Redis.Timeout})
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey.Reveal())
	if err != nil {
		return fmt.Errorf("decode ENCRYPTION_KEY: %w", err)
	}
	svc, err := auth.NewService(pg, rd.Client(), auth.Config{
		JWTSecret: []byte(cfg.JWTSecret.Reveal()), EncryptionKey: key, RecoveryPepper: []byte(cfg.RecoveryPepper.Reveal()),
	}, log)
	if err != nil {
		return err
	}

	users := make([]User, n)
	for i := range n {
		// Every user from an address of its own, so the per-address limits of registration and login hold.
		client := auth.Client{IP: netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), UserAgent: "loadtest-seed"}
		name := fmt.Sprintf("load_%05d", i)
		tokens, err := svc.Register(ctx, client, name, "Load "+strconv.Itoa(i), password, nil)
		if errors.Is(err, auth.ErrUsernameTaken) {
			res, lerr := svc.Login(ctx, client, name, password)
			if lerr != nil {
				return fmt.Errorf("login %s: %w", name, lerr)
			}
			tokens = res.Tokens
		} else if err != nil {
			return fmt.Errorf("register %s: %w", name, err)
		}
		users[i] = User{ID: strconv.FormatInt(tokens.User.ID, 10), Username: name, AccessToken: tokens.AccessToken}
	}
	for i := 0; i+1 < n; i += 2 {
		a, _ := strconv.ParseInt(users[i].ID, 10, 64)
		b, _ := strconv.ParseInt(users[i+1].ID, 10, 64)
		chat, _, err := pg.Chats().CreateDirect(ctx, a, b)
		if err != nil {
			return fmt.Errorf("chat %d: %w", i, err)
		}
		id := strconv.FormatInt(chat.ID, 10)
		users[i].ChatID, users[i+1].ChatID = id, id
		users[i].PeerID, users[i+1].PeerID = users[i+1].ID, users[i].ID
	}
	f, err := os.Create(out) //nolint:gosec // a path the operator chose
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(users); err != nil { //nolint:gosec // writing the test users' tokens is the point
		return err
	}
	fmt.Printf("seeded %d users into %s\n", n, out)
	return nil
}
