package realtime

import (
	"context"
	"time"

	"github.com/myronsi/messenger-back/internal/httpapi"
	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

func (f *Fanout) pollViews(ctx context.Context, chatID, messageID int64, recipients []int64) map[int64]messages.PollView {
	polls, err := f.store.Polls().Polls(ctx, []int64{messageID})
	if err != nil {
		f.log.WarnContext(ctx, "render poll", "error", err)
		return nil
	}
	p, ok := polls[messageID]
	if !ok {
		return nil
	}
	votes, err := f.store.Polls().AllVotes(ctx, messageID)
	if err != nil {
		f.log.WarnContext(ctx, "render poll votes", "error", err)
		return nil
	}
	roles := map[int64]postgres.Role{}
	group := false
	if chat, err := f.store.Chats().Get(ctx, chatID); err == nil && chat.Type == postgres.ChatGroup {
		group = true
		ps, err := f.store.Chats().Participants(ctx, chatID)
		if err != nil {
			f.log.WarnContext(ctx, "render poll roles", "error", err)
		}
		for _, part := range ps {
			roles[part.UserID] = part.Role
		}
	}
	now := time.Now()
	out := make(map[int64]messages.PollView, len(recipients))
	for _, uid := range recipients {
		out[uid] = messages.PollViewFor(p, uid, votes[uid], roles[uid], group, now)
	}
	return out
}

func (f *Fanout) contactViews(ctx context.Context, m scylla.Message, recipients []int64) (map[int64]users.View, bool) {
	c, ok := messages.ParseContact(m)
	if !ok || m.Deleted {
		return nil, false
	}
	views, err := f.dir.ToViewers(ctx, c.UserID, recipients, false)
	if err != nil {
		f.log.WarnContext(ctx, "render contact", "error", err)
		return nil, true
	}
	return views, true
}

func (f *Fanout) kindParts(ctx context.Context, m scylla.Message, recipients []int64) func(uid int64, p *httpapi.MessageParts) {
	var polls map[int64]messages.PollView
	if m.Type == scylla.TypePoll && !m.Deleted {
		polls = f.pollViews(ctx, m.ChatID, m.ID, recipients)
	}
	contacts, contactLoaded := f.contactViews(ctx, m, recipients)
	return func(uid int64, p *httpapi.MessageParts) {
		if pv, ok := polls[uid]; ok {
			p.Poll = &pv
		}
		p.ContactLoaded = contactLoaded
		if v, ok := contacts[uid]; ok {
			p.Contact = &v
		}
	}
}

func (f *Fanout) PollChanged(ctx context.Context, chatID, messageID int64, recipients []int64) {
	start := time.Now()
	views := f.pollViews(ctx, chatID, messageID, recipients)
	f.metrics.FanoutRendered("poll_updated", time.Since(start))
	frames := make(map[int64]frame, len(views))
	for uid, v := range views {
		raw, err := f.encode("poll_updated", chatID, map[string]any{"message_id": idString(messageID), "poll": httpapi.PresentPoll(v)}, "")
		if err != nil {
			f.log.ErrorContext(ctx, "encode event", "error", err, "type", "poll_updated")
			return
		}
		frames[uid] = frame{Kind: kindEvent, ChatID: chatID, Event: raw}
	}
	f.publish(ctx, frames)
}
