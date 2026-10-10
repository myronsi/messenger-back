// Package redis is the Redis store: everything that is shared between API instances or too hot for the
// databases. Presence, live delivery between instances (pub/sub), unread counters, the chat membership
// cache and rate limits live here; the auth package keeps its own keys (sessions, tickets, challenges).
//
// Redis is not a source of truth. Everything except rate limits and one-time credentials can be rebuilt
// from PostgreSQL and ScyllaDB, so losing it costs a slower start, not data. It works with Redis 7.2+ and
// Valkey 8. Some scripts touch two keys that are not in the same hash slot, so Redis Cluster is not
// supported; run a primary with replicas and Sentinel (docs/deployment.md).
package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Client is what the components of this package need from a Redis client.
type Client interface {
	goredis.Cmdable
	goredis.Scripter
}

// DefaultTimeout bounds every Redis command that has no shorter deadline of its own.
const DefaultTimeout = 2 * time.Second

// Options configures the client.
type Options struct {
	// Timeout bounds every command (a pipeline counts as one). Blocking commands such as XREADGROUP with
	// BLOCK are exempt and rely on the caller's context. 0 means DefaultTimeout.
	Timeout time.Duration
	// OnError is called for every failed command except a missing key (redis.Nil), for the error metric.
	OnError func()
}

// Store wraps a Redis client. The client connects lazily.
type Store struct {
	client *goredis.Client
}

// New parses the connection URL and creates the client without connecting.
func New(url string, o Options) (*Store, error) {
	opts, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: invalid format")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	// Socket reads and writes follow the context deadline, which the hook below always sets.
	opts.ContextTimeoutEnabled = true
	c := goredis.NewClient(opts)
	c.AddHook(hook{timeout: o.Timeout, onError: o.OnError})
	return &Store{client: c}, nil
}

// Client returns the underlying client for the features that use Redis.
func (s *Store) Client() *goredis.Client { return s.client }

// Ping checks that Redis is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Close closes the client.
func (s *Store) Close() error { return s.client.Close() }

// hook gives every command a deadline and counts failures.
type hook struct {
	timeout time.Duration
	onError func()
}

// alwaysBlocking lists the commands that wait on the server on purpose; their wait is bounded by the caller.
var alwaysBlocking = map[string]bool{
	"blpop": true, "brpop": true, "brpoplpush": true, "blmove": true, "blmpop": true,
	"bzpopmin": true, "bzpopmax": true, "bzmpop": true, "wait": true, "waitaof": true,
}

// isBlocking reports whether the command waits on the server: the commands above, and stream reads
// with the BLOCK option.
func isBlocking(cmd goredis.Cmder) bool {
	name := strings.ToLower(cmd.Name())
	if alwaysBlocking[name] {
		return true
	}
	if name != "xread" && name != "xreadgroup" {
		return false
	}
	for _, a := range cmd.Args() {
		if s, ok := a.(string); ok && strings.EqualFold(s, "block") {
			return true
		}
	}
	return false
}

func (h hook) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= h.timeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, h.timeout)
}

func (h hook) failed(err error) {
	if err != nil && h.onError != nil && !errors.Is(err, goredis.Nil) {
		h.onError()
	}
}

func (h hook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, cancel := h.withTimeout(ctx)
		defer cancel()
		conn, err := next(ctx, network, addr)
		h.failed(err)
		return conn, err
	}
}

func (h hook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error {
		if !isBlocking(cmd) {
			var cancel context.CancelFunc
			ctx, cancel = h.withTimeout(ctx)
			defer cancel()
		}
		err := next(ctx, cmd)
		h.failed(err)
		return err
	}
}

func (h hook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []goredis.Cmder) error {
		ctx, cancel := h.withTimeout(ctx)
		defer cancel()
		err := next(ctx, cmds)
		h.failed(err)
		return err
	}
}

// pipelineScript calls the script n times in one pipeline; call(pipe, i) adds the i-th call with
// s.EvalSha. When Redis has lost its script cache (a restart, SCRIPT FLUSH) the script is loaded and the
// pipeline sent once more.
func pipelineScript(ctx context.Context, rdb Client, s *goredis.Script, n int, call func(pipe goredis.Pipeliner, i int) *goredis.Cmd) ([]*goredis.Cmd, error) {
	for attempt := 0; ; attempt++ {
		pipe := rdb.Pipeline()
		cmds := make([]*goredis.Cmd, n)
		for i := range n {
			cmds[i] = call(pipe, i)
		}
		_, err := pipe.Exec(ctx)
		if err != nil && attempt == 0 && isNoScript(err) {
			if err := s.Load(ctx, rdb).Err(); err != nil {
				return nil, err
			}
			continue
		}
		return cmds, err
	}
}

func isNoScript(err error) bool { return strings.HasPrefix(err.Error(), "NOSCRIPT") }

// digest is a short, fixed-length stand-in for a value that must not appear in key names as it is.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
