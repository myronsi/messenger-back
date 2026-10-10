package realtime

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

// conn is one WebSocket of a user.
type conn struct {
	gw        *Gateway
	ws        *websocket.Conn
	userID    int64
	sessionID uuid.UUID

	send chan []byte
	// done is closed when the connection is finished.
	done      chan struct{}
	closeOnce sync.Once
	// overflowed is set by the first enqueue that found the buffer full, so only one close is started.
	overflowed atomic.Bool
	// flushThenClose, once set, closes the connection after everything queued was written.
	flushMu        sync.Mutex
	flushThenClose *closeReason
}

type closeReason struct {
	code   websocket.StatusCode
	reason string
}

var _ Conn = (*conn)(nil)

func newConn(g *Gateway, ws *websocket.Conn, userID int64, sessionID uuid.UUID) *conn {
	return &conn{
		gw: g, ws: ws, userID: userID, sessionID: sessionID,
		send: make(chan []byte, g.o.SendBuffer), done: make(chan struct{}),
	}
}

// enqueue queues an event without blocking. A client that cannot keep up is disconnected (it reconnects and
// catches up through REST) instead of slowing down everybody who shares a chat with it.
func (c *conn) enqueue(b []byte) {
	select {
	case <-c.done:
	case c.send <- b:
	default:
		if c.overflowed.CompareAndSwap(false, true) {
			go c.Close(int(websocket.StatusTryAgainLater), "too slow") //nolint:errcheck // best effort
		}
	}
}

// closeAfterFlush closes the connection once the queued events are written.
func (c *conn) closeAfterFlush(code int, reason string) {
	c.flushMu.Lock()
	c.flushThenClose = &closeReason{websocket.StatusCode(code), reason}
	c.flushMu.Unlock()
	c.enqueue(nil) // wakes the writer
}

// Close implements Conn: it sends a close frame and ends the connection.
func (c *conn) Close(code int, reason string) error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		err = c.ws.Close(websocket.StatusCode(code), reason)
	})
	return err
}

// run serves the connection until it closes.
func (c *conn) run() {
	defer func() { _ = c.Close(int(websocket.StatusNormalClosure), "") }()
	var wg sync.WaitGroup
	wg.Go(c.writeLoop)
	wg.Go(c.pingLoop)
	c.readLoop()
	_ = c.Close(int(websocket.StatusNormalClosure), "")
	wg.Wait()
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case b := <-c.send:
			if b == nil {
				c.flushMu.Lock()
				fc := c.flushThenClose
				c.flushMu.Unlock()
				if fc != nil && len(c.send) == 0 {
					_ = c.Close(int(fc.code), fc.reason)
					return
				}
				continue
			}
			ctx, cancel := context.WithTimeout(c.gw.ctx, writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				_ = c.Close(int(websocket.StatusGoingAway), "")
				return
			}
		}
	}
}

func (c *conn) pingLoop() {
	t := time.NewTicker(c.gw.o.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.gw.ctx, writeTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				_ = c.Close(int(websocket.StatusGoingAway), "ping timeout")
				return
			}
		}
	}
}

func (c *conn) readLoop() {
	for {
		typ, frame, err := c.ws.Read(c.gw.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			c.reply(c.errorEvent("", 0, "invalid_request", "events are JSON text frames"))
			continue
		}
		ev, err := decodeClientEvent(frame)
		if err != nil {
			var pe *protocolError
			if errors.As(err, &pe) {
				c.reply(c.errorEvent(pe.ClientTempID, pe.ChatID, pe.Code, pe.Msg))
			}
			continue
		}
		c.handle(ev)
	}
}

func (c *conn) reply(b []byte) {
	if b != nil {
		c.enqueue(b)
	}
}

func (c *conn) errorEvent(clientTempID string, chatID int64, code, msg string) []byte {
	raw, _ := c.gw.o.Fanout.encode("error", chatID, map[string]any{"code": code, "message": msg}, clientTempID)
	return raw
}

func (c *conn) ack(ev clientEvent, data any) []byte {
	raw, _ := c.gw.o.Fanout.encode("ack", ev.ChatID, data, ev.ClientTempID)
	return raw
}

// rate returns the limit an event counts against.
func rate(typ string) redis.Rate {
	switch typ {
	case "message", "resend":
		return RateSend
	case "typing":
		return RateTyping
	}
	return RateOther
}

// handle runs one client event. Events of one connection are handled in order.
func (c *conn) handle(ev clientEvent) {
	ctx, cancel := context.WithTimeout(c.gw.ctx, eventTimeout)
	defer cancel()
	subject := strconv.FormatInt(c.userID, 10)
	// The limiter fails closed: without Redis no event is handled.
	res, err := c.gw.o.Limiter.Allow(ctx, rate(ev.Type), subject)
	if err != nil || !res.Allowed {
		if ev.Type == "typing" { // typing is not acknowledged; excess indicators are simply dropped
			return
		}
		if err != nil {
			c.gw.o.Log.WarnContext(ctx, "rate limiter unavailable", "error", err)
			c.reply(c.errorEvent(ev.ClientTempID, ev.ChatID, "internal_error", "something went wrong, try again"))
		} else {
			c.reply(c.errorEvent(ev.ClientTempID, ev.ChatID, "rate_limited", "too many events, slow down"))
		}
		return
	}
	svc := c.gw.o.Messages
	var data any = struct{}{}
	switch ev.Type {
	case "message":
		var sent messages.Sent
		sent, err = svc.Send(ctx, messages.SendRequest{
			ChatID: ev.ChatID, SenderID: c.userID, ClientTempID: ev.ClientTempID, Type: ev.Message.Type,
			Content: ev.Message.Content, AttachmentID: ev.Message.AttachmentID, ReplyTo: ev.Message.ReplyTo,
		})
		if err == nil {
			data = map[string]any{"message_id": idString(sent.Message.ID), "created_at": sent.Message.CreatedAt.UTC()}
		}
	case "resend":
		err = svc.Resend(ctx, c.userID, ev.ChatID, ev.Target.MessageID)
	case "edit":
		_, err = svc.Edit(ctx, c.userID, ev.ChatID, ev.Edit.MessageID, ev.Edit.Content)
	case "delete":
		err = svc.Delete(ctx, c.userID, ev.ChatID, ev.Delete.MessageID, ev.Delete.Scope)
	case "read":
		err = svc.Read(ctx, c.userID, ev.ChatID, ev.Target.MessageID)
	case "reaction_add", "reaction_remove":
		err = svc.React(ctx, c.userID, ev.ChatID, ev.Reaction.MessageID, ev.Reaction.Emoji, ev.Type == "reaction_add")
	case "typing":
		_ = svc.Typing(ctx, c.userID, ev.ChatID, ev.Typing.IsTyping)
		return
	}
	if err != nil {
		code, msg := errorCode(err)
		if code == "internal_error" {
			c.gw.o.Log.WarnContext(ctx, "client event failed", "type", ev.Type, "error", err)
		}
		c.reply(c.errorEvent(ev.ClientTempID, ev.ChatID, code, msg))
		return
	}
	c.reply(c.ack(ev, data))
}

// errorCode maps service errors to the contract's codes. Messages never contain message content.
func errorCode(err error) (code, msg string) {
	switch {
	case errors.Is(err, messages.ErrNotFound):
		return "not_found", "the chat or message does not exist"
	case errors.Is(err, messages.ErrForbidden):
		return "forbidden", "not allowed"
	case errors.Is(err, messages.ErrBlocked):
		return "blocked_by_user", "one of you blocked the other"
	case errors.Is(err, messages.ErrInvalid):
		return "validation_failed", err.Error()
	}
	return "internal_error", "something went wrong, try again"
}
