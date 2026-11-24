package app

import (
	"time"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func (p *Process) OpenPostgres() (*postgres.Store, error) {
	pg, err := postgres.New(p.Ctx, p.Cfg.DatabaseURL.Reveal(), postgres.Options{
		MaxConns:     p.Cfg.Postgres.MaxConns,
		QueryTimeout: p.Cfg.Postgres.QueryTimeout,
		Observe: func(op string, d time.Duration, err error) {
			p.Metrics.StoreOperation("postgres", op, d, err)
		},
	})
	if err != nil {
		return nil, err
	}
	p.Metrics.RegisterPostgresPool(pg.Pool().Stat)
	return pg, nil
}

func (p *Process) OpenRedis() (*redis.Store, error) {
	return redis.New(p.Cfg.RedisURL.Reveal(), redis.Options{
		Timeout: p.Cfg.Redis.Timeout,
		OnError: func() { p.Metrics.StoreError("redis") },
		Observe: func(op string, d time.Duration) { p.Metrics.StoreOperation("redis", op, d, nil) },
	})
}

func (p *Process) OpenScylla() *scylla.Store {
	return scylla.New(p.Cfg.ScyllaHosts, p.Cfg.ScyllaKeyspace, scylla.Options{
		Consistency:    gocql.ParseConsistency(p.Cfg.Scylla.Consistency),
		RequestTimeout: p.Cfg.Scylla.Timeout,
		Observe: func(op string, d time.Duration, err error) {
			p.Metrics.StoreOperation("scylla", op, d, err)
		},
	})
}
