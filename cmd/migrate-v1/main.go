// Command migrate-v1 copies the data of the Python backend (its PostgreSQL database and static/ folder) into the
// stores of the Go backend (#54, docs/migration-v1.md). It is repeatable: every step skips what is there
// already, and the messages continue after the last one copied.
//
//	migrate-v1 [-dry-run] [-phases users,chats,files,messages,search] [-report report.json]
//
// It reads the v1 database from V1_DATABASE_URL and the files from V1_STATIC_DIR, decrypts TOTP secrets with
// V1_SECRET_KEY (the Python SECRET_KEY), and writes with the Go backend's own environment.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gocql/gocql"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/observability"
	"github.com/myronsi/messenger-back/internal/search"
	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// allPhases in the order they depend on each other.
var allPhases = []string{"users", "chats", "files", "messages", "search"}

func main() {
	dry := flag.Bool("dry-run", false, "read everything, write nothing, report what would happen")
	phases := flag.String("phases", strings.Join(allPhases, ","), "the steps to run, comma-separated")
	report := flag.String("report", "migrate-v1-report.json", "where to write the report")
	restart := flag.Bool("restart-messages", false, "copy the messages from the first one again (it is idempotent)")
	drop2FA := flag.Bool("drop-unreadable-2fa", false, "turn 2FA off for users whose TOTP secret cannot be decrypted, instead of stopping")
	flag.Parse()
	if err := run(*dry, strings.Split(*phases, ","), *report, *restart, *drop2FA); err != nil {
		fmt.Fprintln(os.Stderr, "migrate-v1:", err)
		os.Exit(1)
	}
}

func run(dry bool, phases []string, reportPath string, restart, drop2FA bool) error {
	ctx, stop := contextWithSignals()
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observability.NewLogger(os.Stdout, slog.LevelInfo, "migrate-v1")
	// The Python backend stripped SECRET_KEY; a trailing newline from a secret file must not change the key.
	v1URL, staticDir, secret := os.Getenv("V1_DATABASE_URL"), os.Getenv("V1_STATIC_DIR"), strings.TrimSpace(os.Getenv("V1_SECRET_KEY"))
	if v1URL == "" || staticDir == "" {
		return errors.New("V1_DATABASE_URL and V1_STATIC_DIR are required")
	}
	root, err := os.OpenRoot(staticDir)
	if err != nil {
		return fmt.Errorf("V1_STATIC_DIR: %w", err)
	}
	defer func() { _ = root.Close() }()
	v1, err := pgxpool.New(ctx, v1URL)
	if err != nil {
		return fmt.Errorf("v1 database: %w", err)
	}
	defer v1.Close()
	pg, err := postgres.New(ctx, cfg.DatabaseURL.Reveal(), postgres.Options{MaxConns: 8, QueryTimeout: time.Minute})
	if err != nil {
		return err
	}
	defer pg.Close()
	sc := scylla.New(cfg.ScyllaHosts, cfg.ScyllaKeyspace, scylla.Options{Consistency: gocql.ParseConsistency(cfg.Scylla.Consistency), RequestTimeout: time.Minute})
	defer func() { _ = sc.Close() }()
	storage, _, err := media.StorageFrom(cfg.Media)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey.Reveal())
	if err != nil {
		return fmt.Errorf("decode ENCRYPTION_KEY: %w", err)
	}
	sealer, err := auth.NewSealer(key)
	if err != nil {
		return err
	}
	m := &migrator{
		v1: v1, pg: pg, msgs: scylla.NewMessages(sc, time.Minute), storage: storage, sealer: sealer,
		root: root, dry: dry, drop2FA: drop2FA, log: log, report: newReport(dry),
	}
	if secret != "" {
		if m.fernet, err = fernetKey(secret); err != nil {
			return err
		}
	}
	for _, p := range phases {
		p = strings.TrimSpace(p)
		start := time.Now()
		log.Info("phase", "name", p, "dry_run", dry)
		switch p {
		case "users":
			err = m.users(ctx)
		case "chats":
			err = m.chats(ctx)
		case "files":
			err = m.files(ctx)
		case "messages":
			err = m.messages(ctx, restart)
		case "search":
			if dry {
				continue
			}
			es, eerr := elastic.New(cfg.ElasticsearchURL.Reveal())
			if eerr != nil {
				return eerr
			}
			ix := search.NewIndex(es, cfg.Search.Alias, cfg.Search.Replicas)
			if err = ix.Ensure(ctx); err == nil {
				var name string
				name, err = search.NewIndexer(ix, m.msgs, pg.Attachments(), log).Rebuild(ctx, pg.Chats())
				m.report.Notes = append(m.report.Notes, "search index rebuilt: "+name)
			}
		case "":
			continue
		default:
			return fmt.Errorf("unknown phase %q (phases: %s)", p, strings.Join(allPhases, ", "))
		}
		if err != nil {
			return fmt.Errorf("phase %s: %w", p, err)
		}
		log.Info("phase done", "name", p, "took", time.Since(start).Round(time.Second))
	}
	if err := m.verify(ctx); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	return m.writeReport(reportPath)
}

// migrator holds the stores and what the phases learn from each other.
type migrator struct {
	v1      *pgxpool.Pool
	pg      *postgres.Store
	msgs    *scylla.Messages
	storage media.Storage
	sealer  *auth.Sealer
	fernet  []byte
	// root is V1_STATIC_DIR: files are read through it, so links cannot lead out of it.
	root *os.Root
	dry  bool
	// drop2FA turns 2FA off for unreadable secrets; without it the run stops on the first one.
	drop2FA bool
	log     *slog.Logger
	mu      sync.Mutex // guards report
	report  *report
}

// report is what the run did (or would do), with counts only: no names or message content.
type report struct {
	DryRun  bool             `json:"dry_run"`
	Started time.Time        `json:"started"`
	Counts  map[string]int64 `json:"counts"`
	// Verification compares v1 with v2 per table, and the message counts of sampled chats.
	Verification map[string][2]int64 `json:"verification,omitempty"`
	Chats        []chatCheck         `json:"chat_samples,omitempty"`
	Notes        []string            `json:"notes,omitempty"`
}

type chatCheck struct {
	V1Chat int64 `json:"v1_chat"`
	V2Chat int64 `json:"v2_chat"`
	V1     int64 `json:"v1_messages"`
	V2     int64 `json:"v2_messages"`
}

func newReport(dry bool) *report {
	return &report{DryRun: dry, Started: time.Now().UTC(), Counts: map[string]int64{}, Verification: map[string][2]int64{}}
}

// count adds to a report counter; the message workers count concurrently.
func (m *migrator) count(key string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.report.Counts[key] += n
}

func (m *migrator) writeReport(path string) error {
	b, err := json.MarshalIndent(m.report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	m.log.Info("report written", "path", path)
	for k, v := range m.report.Verification {
		if v[0] != v[1] {
			m.log.Warn("counts differ", "table", k, "v1", v[0], "v2", v[1])
		}
	}
	return nil
}

// state keeps the mapping tables of the migration in the v2 database, so a rerun continues where it stopped.
// A file is mapped per purpose: the same path as a message file and as an avatar are two attachments with
// different access rules. A mapping goes with its attachment.
const stateSchema = `
CREATE TABLE IF NOT EXISTS migrate_v1_chats (v1_id BIGINT PRIMARY KEY, v2_id BIGINT NOT NULL);
CREATE TABLE IF NOT EXISTS migrate_v1_files (
    path TEXT NOT NULL, purpose TEXT NOT NULL, attachment_id UUID NOT NULL REFERENCES attachments (id) ON DELETE CASCADE,
    kind TEXT NOT NULL, PRIMARY KEY (path, purpose));
CREATE TABLE IF NOT EXISTS migrate_v1_state (key TEXT PRIMARY KEY, value BIGINT NOT NULL);
`

func (m *migrator) ensureState(ctx context.Context) error {
	if m.dry {
		return nil
	}
	_, err := m.pg.Pool().Exec(ctx, stateSchema)
	return err
}
