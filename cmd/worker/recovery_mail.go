package main

import (
	"context"
	"encoding/base64"
	"log/slog"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/recoverymail"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func workerAuth(cfg config.Config, pg *postgres.Store, rd auth.Redis, log *slog.Logger, onRevoked func(context.Context, []uuid.UUID)) (*auth.Service, error) {
	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey.Reveal())
	if err != nil {
		return nil, err
	}
	return auth.NewService(pg, rd, auth.Config{
		JWTSecret: []byte(cfg.JWTSecret.Reveal()), EncryptionKey: key, RecoveryPepper: []byte(cfg.RecoveryPepper.Reveal()),
		RecoveryMailEnabled: cfg.RecoveryMail.Host != "", RecoveryWebURL: cfg.RecoveryMail.WebURL, OnRevoked: onRevoked,
	}, log)
}

func recoveryMailWorker(cfg config.Config, svc *auth.Service) func(context.Context) {
	if cfg.RecoveryMail.Host == "" {
		return func(context.Context) {}
	}
	sender := recoverymail.Sender{Config: cfg.RecoveryMail}
	return func(ctx context.Context) { svc.RunRecoveryMail(ctx, sender.Send) }
}
