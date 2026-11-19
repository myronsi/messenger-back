package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/inactive"
	"github.com/myronsi/messenger-back/internal/jobs"
	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/realtime"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

const inactiveAccountsEvery = 24 * time.Hour

type inactiveAccountsDeps struct {
	cfg      config.Config
	pg       *postgres.Store
	accounts inactive.Accounts
	fanout   *realtime.Fanout
	rdb      redis.Client
	storage  media.Storage
	events   *events.Log
	log      *slog.Logger
}

func inactiveAccounts(d inactiveAccountsDeps) *inactive.Service {
	cleanup := jobs.AccountCleanup{
		Notifier: d.fanout, Unread: redis.NewUnread(d.rdb, ""), Events: d.events,
		Members: redis.NewMembers(d.rdb, "", d.cfg.Redis.MembersCacheTTL), DeleteFile: d.storage.Delete, Log: d.log,
	}
	return inactive.New(inactive.Deps{
		Store: d.pg.InactiveAccounts(), Accounts: d.accounts, AfterDeletion: cleanup.AfterDeletion, Events: d.events,
		Enabled: d.cfg.Inactive.Deletion, Mail: d.cfg.RecoveryMail.Host != "", Log: d.log,
	})
}

func inactiveAccountsReport(ctx context.Context, cfg config.Config, pg *postgres.Store, out io.Writer) error {
	svc := inactive.New(inactive.Deps{Store: pg.InactiveAccounts(), Mail: cfg.RecoveryMail.Host != ""})
	plan, err := svc.Plan(ctx)
	if err != nil {
		return err
	}
	deletion := "off"
	if cfg.Inactive.Deletion {
		deletion = "on"
	}
	mail := "off (SMTP_HOST is empty): accounts are deleted without a warning"
	if plan.Rule.WarnFirst {
		mail = "on: accounts with a verified recovery email are warned and deleted 30 days later"
	}
	_, err = fmt.Fprintf(out, "Inactive accounts: no activity since %s (%s)\n"+
		"would be warned today:  %d\n"+
		"would be deleted today: %d\n"+
		"INACTIVE_ACCOUNT_DELETION: %s\n"+
		"warning emails: %s\n"+
		"Nothing was changed.\n",
		plan.Rule.ActiveBefore.Format(time.DateOnly), inactive.Period, plan.Warn, plan.Delete, deletion, mail)
	return err
}
