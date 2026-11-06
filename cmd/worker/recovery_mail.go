package main

import (
	"context"
	"encoding/base64"
	"log/slog"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/recoverymail"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func recoveryMailWorker(cfg config.Config, pg *postgres.Store, rd auth.Redis, log *slog.Logger) (func(context.Context), error) {
	if cfg.RecoveryMail.Host == "" {
		return func(context.Context) {}, nil
	}
	key, err := base64.StdEncoding.DecodeString(cfg.EncryptionKey.Reveal())
	if err != nil {
		return nil, err
	}
	svc, err := auth.NewService(pg, rd, auth.Config{JWTSecret: []byte(cfg.JWTSecret.Reveal()), EncryptionKey: key, RecoveryPepper: []byte(cfg.RecoveryPepper.Reveal()), RecoveryMailEnabled: true, RecoveryWebURL: cfg.RecoveryMail.WebURL}, log)
	if err != nil {
		return nil, err
	}
	sender := recoverymail.Sender{Config: cfg.RecoveryMail}
	return func(ctx context.Context) { svc.RunRecoveryMail(ctx, sender.Send) }, nil
}
