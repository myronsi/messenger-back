package main

import (
	"github.com/myronsi/messenger-back/internal/app"
	"github.com/myronsi/messenger-back/internal/calls"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/notifications"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

func callLimits(c config.Calls) calls.Config {
	return calls.Config{
		PublicURL: c.PublicURL, MaxParticipants: c.MaxParticipants, MaxActiveParticipants: c.MaxActiveParticipants,
		RingTimeout: c.RingTimeout, EmptyGrace: c.EmptyGrace, TokenTTL: c.TokenTTL,
	}
}

func startCalls(p *app.Process, pg *postgres.Store, rd *redis.Store, rt *realtimeStack) (*calls.Service, error) {
	cfg := p.Cfg
	if !cfg.Calls.Enabled {
		closer := calls.New(callLimits(cfg.Calls), calls.Deps{
			Store: pg, Members: rt.messages.Members, Notifier: rt.fanout, System: rt.messages, Metrics: p.Metrics, Log: p.Log,
		})
		rt.done.Go(func() { closer.RunDisabled(rt.ctx) })
		return nil, nil
	}
	grants := calls.NewGrants(cfg.Calls.APIKey, cfg.Calls.APISecret.Reveal(), cfg.Calls.TokenTTL)
	deps := calls.Deps{
		Store: pg, Members: rt.messages.Members, Notifier: rt.fanout, System: rt.messages,
		Media: calls.NewRoomClient(cfg.Calls.APIURL, grants), Grants: grants, Metrics: p.Metrics,
		Webhooks: redis.NewOnce(rd.Client(), "", "calls_webhook"), Log: p.Log,
	}
	if cfg.Notifications.WebEnabled() {
		sender, err := notifications.NewSender(cfg.Notifications)
		if err != nil {
			return nil, err
		}
		deps.Push, deps.Tokens = sender, notifications.NewActionTokens(cfg.Notifications.VAPIDPrivate.Reveal())
	}
	svc := calls.New(callLimits(cfg.Calls), deps)
	rt.done.Go(func() { svc.Run(rt.ctx) })
	p.Log.Info("audio calls enabled", "max_participants", cfg.Calls.MaxParticipants)
	return svc, nil
}
