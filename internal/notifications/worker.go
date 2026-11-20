package notifications

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	jobsAtOnce    = 4
	jobsPerTick   = 20
	sendsAtOnce   = 8
	jobTimeout    = 75 * time.Second
	jobLifetime   = 15 * time.Minute
	maxAttempts   = 8
	activityGrace = 2 * time.Second
	pruneEvery    = time.Minute
	titleRunes    = 64
	bodyRunes     = 160
)

const (
	ContentText    = "text"
	ContentPhoto   = "photo"
	ContentVideo   = "video"
	ContentAudio   = "audio"
	ContentVoice   = "voice"
	ContentFile    = "file"
	ContentAlbum   = "album"
	ContentSticker = "sticker"
)

type Delivery interface {
	Send(context.Context, postgres.NotificationDevice, Payload) error
}
type Worker struct {
	Queue       postgres.NotificationRepository
	Chats       postgres.ChatRepository
	Social      postgres.SocialRepository
	Users       postgres.UserRepository
	Attachments postgres.AttachmentRepository
	Messages    scylla.MessageRepository
	Sender      Delivery
	Tokens      *ActionTokens
	Log         *slog.Logger
	OnResult    func(string)
}

func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range jobsAtOnce {
		wg.Go(func() { w.loop(ctx) })
	}
	wg.Go(func() { w.prune(ctx) })
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for range jobsPerTick {
				if !w.next(ctx) {
					break
				}
			}
		}
	}
}

func (w *Worker) next(ctx context.Context) bool {
	j, err := w.Queue.Claim(ctx)
	if errors.Is(err, postgres.ErrNotFound) {
		return false
	}
	if err != nil {
		w.result("retry")
		if ctx.Err() == nil {
			w.Log.ErrorContext(ctx, "claim notification job", "error", err)
		}
		return false
	}
	batch, cancel := context.WithTimeout(ctx, jobTimeout)
	err = w.Handle(batch, j)
	cancel()
	if err != nil {
		w.Log.WarnContext(ctx, "notification delivery deferred", "message_id", j.MessageID, "attempt", j.Attempts, "error", err)
		w.result("retry")
	}
	return true
}

func (w *Worker) prune(ctx context.Context) {
	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Queue.PruneActivity(ctx); err != nil && ctx.Err() == nil {
				w.Log.WarnContext(ctx, "prune notification activity", "error", err)
			}
		}
	}
}

type delivery struct {
	device  postgres.NotificationDevice
	payload Payload
}

func (w *Worker) Handle(ctx context.Context, j postgres.NotificationJob) error {
	if time.Since(j.CreatedAt) > jobLifetime || j.Attempts > maxAttempts {
		w.result("expired")
		return w.Queue.Finish(ctx, j.MessageID)
	}
	m, err := w.Messages.Get(ctx, j.ChatID, j.MessageID)
	// The intent may precede a slow Scylla write; leave it for the next attempt.
	if err != nil {
		return errors.New("notification message not available")
	}
	if m.Deleted || m.SenderID == nil || m.Type == scylla.TypeSystem {
		return w.Queue.Finish(ctx, j.MessageID)
	}
	recipients, err := w.Queue.Recipients(ctx, j.ChatID, *m.SenderID, j.Recipients)
	if err != nil {
		return err
	}
	eligible := map[int64]postgres.NotificationRecipient{}
	ids := make([]int64, 0, len(recipients))
	for _, r := range recipients {
		read := r.LastReadMessageID != nil && *r.LastReadMessageID >= m.ID
		if r.JoinedAt.After(m.CreatedAt) || read || r.Blocked || !r.Preferences.Enabled {
			continue
		}
		eligible[r.UserID] = r
		ids = append(ids, r.UserID)
	}
	if len(ids) == 0 {
		return w.Queue.Finish(ctx, j.MessageID)
	}
	devices, err := w.Queue.Devices(ctx, j.ChatID, j.MessageID, ids)
	if err != nil {
		return err
	}
	content, err := w.content(ctx, j, m, devices, eligible)
	if err != nil {
		return err
	}
	var activeUntil time.Time
	sends := make([]delivery, 0, len(devices))
	for _, d := range devices {
		p, ok := content.payloads[d.UserID]
		if !ok {
			continue
		}
		if d.ActiveUntil != nil {
			if !j.Deferred && d.ActiveUntil.After(activeUntil) {
				activeUntil = *d.ActiveUntil
			}
			continue
		}
		sends = append(sends, delivery{device: d, payload: w.sign(p, d, content)})
	}
	if err := w.deliver(ctx, j.MessageID, sends); err != nil {
		return err
	}
	if !activeUntil.IsZero() {
		return w.Queue.Defer(ctx, j.MessageID, activeUntil.Add(activityGrace))
	}
	return w.Queue.Finish(ctx, j.MessageID)
}

type jobContent struct {
	payloads map[int64]Payload
	senderID int64
	chatID   int64
	message  int64
	groupPic bool
}

func (w *Worker) content(ctx context.Context, j postgres.NotificationJob, m scylla.Message, devices []postgres.NotificationDevice,
	eligible map[int64]postgres.NotificationRecipient) (jobContent, error) {
	out := jobContent{payloads: map[int64]Payload{}, senderID: *m.SenderID, chatID: j.ChatID, message: j.MessageID}
	var previewers []int64
	for _, d := range devices {
		r, ok := eligible[d.UserID]
		if !ok {
			continue
		}
		if _, done := out.payloads[d.UserID]; done {
			continue
		}
		hidden, err := w.Messages.Hidden(ctx, d.UserID, j.ChatID, j.MessageID)
		if err != nil {
			return out, err
		}
		if hidden {
			continue
		}
		out.payloads[d.UserID] = Payload{}
		if r.Preferences.Previews {
			previewers = append(previewers, d.UserID)
		}
	}
	if len(out.payloads) == 0 {
		return out, nil
	}
	chat, err := w.Chats.Get(ctx, j.ChatID)
	if err != nil {
		return out, err
	}
	kind := w.contentType(ctx, m)
	base := Payload{
		Title: "Messenger", ChatID: fmt.Sprint(j.ChatID), MessageID: fmt.Sprint(j.MessageID), ContentType: kind,
		ChatType: string(chat.Type), ExpiresAt: m.CreatedAt.Add(jobLifetime).Unix(),
	}
	var senderName string
	names := map[int64]string{}
	if len(previewers) > 0 {
		sender, err := w.Users.Get(ctx, *m.SenderID)
		if err != nil {
			return out, err
		}
		senderName = sender.DisplayName
		if w.Social != nil {
			if names, err = w.Social.ContactNamesFor(ctx, previewers, *m.SenderID); err != nil {
				return out, err
			}
		}
	}
	out.groupPic = chat.Type == postgres.ChatGroup && chat.AvatarAttachmentID != nil
	for uid := range out.payloads {
		r := eligible[uid]
		p := base
		p.UserID, p.Sound = fmt.Sprint(uid), r.Preferences.Sound
		if r.Preferences.Previews {
			p.Preview, p.SenderID = true, fmt.Sprint(*m.SenderID)
			p.Title = clip(emoji.Plain(cmp.Or(names[uid], senderName)), titleRunes)
			if m.Content != nil {
				p.Body = clip(emoji.Plain(*m.Content), bodyRunes)
			}
			if chat.Type == postgres.ChatGroup && chat.Name != nil {
				p.ChatTitle = clip(emoji.Plain(*chat.Name), titleRunes)
			}
		}
		out.payloads[uid] = p
	}
	return out, nil
}

func (w *Worker) contentType(ctx context.Context, m scylla.Message) string {
	switch m.Type {
	case scylla.TypeVoice:
		return ContentVoice
	case scylla.TypeSticker:
		return ContentSticker
	}
	ids := m.AttachmentIDs()
	if len(ids) == 0 {
		return ContentText
	}
	if len(ids) > 1 {
		return ContentAlbum
	}
	if w.Attachments == nil {
		return ContentFile
	}
	a, err := w.Attachments.Get(ctx, uuid.UUID(ids[0]))
	if err != nil {
		return ContentFile
	}
	switch {
	case strings.HasPrefix(a.MimeType, "image/"):
		return ContentPhoto
	case strings.HasPrefix(a.MimeType, "video/"):
		return ContentVideo
	case strings.HasPrefix(a.MimeType, "audio/"):
		return ContentAudio
	}
	return ContentFile
}

func (w *Worker) sign(p Payload, d postgres.NotificationDevice, c jobContent) Payload {
	if w.Tokens == nil {
		return p
	}
	uid := d.UserID
	p.ReadToken = w.Tokens.Read(uid, d.ID, d.SessionID, c.chatID, c.message)
	if p.Preview {
		if c.groupPic {
			p.AvatarToken = w.Tokens.ChatAvatar(uid, d.ID, d.SessionID, c.chatID)
		} else {
			p.AvatarToken = w.Tokens.Avatar(uid, d.ID, d.SessionID, c.senderID)
		}
	}
	return p
}

func (w *Worker) deliver(ctx context.Context, messageID int64, sends []delivery) error {
	var (
		mu      sync.Mutex
		pending error
		wg      sync.WaitGroup
	)
	slots := make(chan struct{}, sendsAtOnce)
	for _, s := range sends {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-slots }()
			if err := w.deliverOne(ctx, messageID, s); err != nil {
				mu.Lock()
				pending = err
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return pending
}

func (w *Worker) deliverOne(ctx context.Context, messageID int64, s delivery) error {
	err := w.Sender.Send(ctx, s.device, s.payload)
	switch {
	case err == nil:
		w.result("delivered")
		return w.Queue.Delivered(ctx, messageID, s.device.ID)
	case errors.Is(err, ErrUnregistered):
		w.result("unregistered")
		return w.Queue.RemoveDevice(ctx, s.device.ID)
	case errors.Is(err, ErrRejected), errors.Is(err, ErrTooLarge):
		removed, rerr := w.Queue.Rejected(ctx, messageID, s.device.ID, errors.Is(err, ErrRejected))
		if rerr != nil {
			return rerr
		}
		w.result("rejected")
		if removed {
			w.result("unregistered")
		}
		w.Log.WarnContext(ctx, "notification rejected", "message_id", messageID, "platform", s.device.Platform, "error", err)
		return nil
	}
	return err
}

func (w *Worker) result(outcome string) {
	if w.OnResult != nil {
		w.OnResult(outcome)
	}
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
