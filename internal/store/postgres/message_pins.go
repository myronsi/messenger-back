package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const (
	PinScopeMe       = "me"
	PinScopeEveryone = "everyone"
)

type MessagePin struct {
	ChatID    int64
	MessageID int64
	Scope     string
	PinnedBy  int64
	PinnedAt  time.Time
}

type PinCursor struct {
	PinnedAt  time.Time
	MessageID int64
}

type MessagePinRepository interface {
	PinForEveryone(ctx context.Context, chatID, messageID, by int64, maxPins int) (MessagePin, bool, bool, error)
	PinForMe(ctx context.Context, userID, chatID, messageID int64, maxPins int) (MessagePin, bool, error)
	SharedPin(ctx context.Context, chatID, messageID int64) (MessagePin, error)
	UnpinForEveryone(ctx context.Context, chatID, messageID int64) (MessagePin, bool, error)
	UnpinForMe(ctx context.Context, userID, chatID, messageID int64) (bool, error)
	VisiblePins(ctx context.Context, userID, chatID int64, after *PinCursor, limit int) ([]MessagePin, error)
	VisiblePinCounts(ctx context.Context, userID int64, chatIDs []int64) (map[int64]int, error)
	RemoveMessagePins(ctx context.Context, chatID int64, messageIDs []int64) ([]int64, error)
	HideMessagePins(ctx context.Context, userID, chatID int64, messageIDs []int64) error
	ClearPins(ctx context.Context, chatID, upTo int64) error
	ClearUserPins(ctx context.Context, userID, chatID, upTo int64) error
	RemoveUserPins(ctx context.Context, userID, chatID int64) error
}

func (s *Store) MessagePins() MessagePinRepository { return pinRepo{s} }

type pinRepo struct{ s *Store }

var _ MessagePinRepository = pinRepo{}

func sharedPin(p sqlcdb.MessagePin) MessagePin {
	return MessagePin{ChatID: p.ChatID, MessageID: p.MessageID, Scope: PinScopeEveryone, PinnedBy: p.PinnedBy, PinnedAt: p.PinnedAt}
}

func personalPin(p sqlcdb.MessagePinsPersonal) MessagePin {
	return MessagePin{ChatID: p.ChatID, MessageID: p.MessageID, Scope: PinScopeMe, PinnedBy: p.UserID, PinnedAt: p.PinnedAt}
}

func (r pinRepo) PinForEveryone(ctx context.Context, chatID, messageID, by int64, maxPins int) (MessagePin, bool, bool, error) {
	var pin MessagePin
	var created, unhid bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		pin, created, unhid = MessagePin{}, false, false
		if _, err := q.LockChat(ctx, chatID); err != nil {
			return err
		}
		existing, err := q.GetSharedPin(ctx, sqlcdb.GetSharedPinParams{ChatID: chatID, MessageID: messageID})
		if err == nil {
			pin = sharedPin(existing)
			n, err := q.DeletePinHide(ctx, sqlcdb.DeletePinHideParams{UserID: by, ChatID: chatID, MessageID: messageID})
			unhid = n > 0
			return err
		}
		if !errors.Is(mapError(err), ErrNotFound) {
			return err
		}
		n, err := q.CountSharedPins(ctx, chatID)
		if err != nil {
			return err
		}
		if int(n) >= maxPins {
			return ErrLimit
		}
		row, err := q.InsertSharedPin(ctx, sqlcdb.InsertSharedPinParams{ChatID: chatID, MessageID: messageID, PinnedBy: by})
		if err != nil {
			return err
		}
		pin, created = sharedPin(row), true
		return nil
	})
	return pin, created, unhid, err
}

func (r pinRepo) PinForMe(ctx context.Context, userID, chatID, messageID int64, maxPins int) (MessagePin, bool, error) {
	var pin MessagePin
	var changed bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		pin, changed = MessagePin{}, false
		if _, err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		shared, err := q.GetSharedPin(ctx, sqlcdb.GetSharedPinParams{ChatID: chatID, MessageID: messageID})
		if err == nil {
			pin = sharedPin(shared)
			n, err := q.DeletePinHide(ctx, sqlcdb.DeletePinHideParams{UserID: userID, ChatID: chatID, MessageID: messageID})
			changed = n > 0
			return err
		}
		if !errors.Is(mapError(err), ErrNotFound) {
			return err
		}
		own, err := q.GetPersonalPin(ctx, sqlcdb.GetPersonalPinParams{UserID: userID, ChatID: chatID, MessageID: messageID})
		if err == nil {
			pin = personalPin(own)
			return nil
		}
		if !errors.Is(mapError(err), ErrNotFound) {
			return err
		}
		n, err := q.CountPersonalPins(ctx, sqlcdb.CountPersonalPinsParams{UserID: userID, ChatID: chatID})
		if err != nil {
			return err
		}
		if int(n) >= maxPins {
			return ErrLimit
		}
		row, err := q.InsertPersonalPin(ctx, sqlcdb.InsertPersonalPinParams{UserID: userID, ChatID: chatID, MessageID: messageID})
		if err != nil {
			return err
		}
		pin, changed = personalPin(row), true
		return nil
	})
	return pin, changed, err
}

func (r pinRepo) SharedPin(ctx context.Context, chatID, messageID int64) (MessagePin, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetSharedPin(ctx, sqlcdb.GetSharedPinParams{ChatID: chatID, MessageID: messageID})
	if err != nil {
		return MessagePin{}, mapError(err)
	}
	return sharedPin(row), nil
}

func (r pinRepo) UnpinForEveryone(ctx context.Context, chatID, messageID int64) (MessagePin, bool, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.DeleteSharedPin(ctx, sqlcdb.DeleteSharedPinParams{ChatID: chatID, MessageID: messageID})
	if err != nil {
		if err = mapError(err); errors.Is(err, ErrNotFound) {
			return MessagePin{}, false, nil
		}
		return MessagePin{}, false, err
	}
	return sharedPin(row), true, nil
}

func (r pinRepo) UnpinForMe(ctx context.Context, userID, chatID, messageID int64) (bool, error) {
	var changed bool
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		changed = false
		removed, err := q.DeletePersonalPin(ctx, sqlcdb.DeletePersonalPinParams{UserID: userID, ChatID: chatID, MessageID: messageID})
		if err != nil {
			return err
		}
		hidden, err := q.HideSharedPinsOf(ctx, sqlcdb.HideSharedPinsOfParams{UserID: userID, ChatID: chatID, MessageIds: []int64{messageID}})
		if err != nil {
			return err
		}
		changed = removed+hidden > 0
		return nil
	})
	return changed, err
}

func (r pinRepo) VisiblePins(ctx context.Context, userID, chatID int64, after *PinCursor, limit int) ([]MessagePin, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	arg := sqlcdb.ListVisiblePinsParams{UserID: userID, ChatID: chatID, RowLimit: int32(limit)} //nolint:gosec // limit is bounded by the API
	if after != nil {
		arg.HasCursor, arg.AfterAt, arg.AfterID = true, after.PinnedAt, after.MessageID
	}
	rows, err := r.s.q.ListVisiblePins(ctx, arg)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]MessagePin, len(rows))
	for i, row := range rows {
		out[i] = MessagePin{ChatID: chatID, MessageID: row.MessageID, Scope: row.Scope, PinnedBy: row.PinnedBy, PinnedAt: row.PinnedAt}
	}
	return out, nil
}

func (r pinRepo) VisiblePinCounts(ctx context.Context, userID int64, chatIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(chatIDs) == 0 {
		return out, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.CountVisiblePins(ctx, sqlcdb.CountVisiblePinsParams{UserID: userID, ChatIds: chatIDs})
	if err != nil {
		return nil, mapError(err)
	}
	for _, row := range rows {
		out[row.ChatID] = int(row.Pins)
	}
	return out, nil
}

func (r pinRepo) RemoveMessagePins(ctx context.Context, chatID int64, messageIDs []int64) ([]int64, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	var removed []int64
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		var err error
		if removed, err = q.DeleteSharedPinsOf(ctx, sqlcdb.DeleteSharedPinsOfParams{ChatID: chatID, MessageIds: messageIDs}); err != nil {
			return err
		}
		_, err = q.DeletePersonalPinsOf(ctx, sqlcdb.DeletePersonalPinsOfParams{ChatID: chatID, MessageIds: messageIDs})
		return err
	})
	return removed, err
}

func (r pinRepo) HideMessagePins(ctx context.Context, userID, chatID int64, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.DeleteUserPersonalPinsOf(ctx, sqlcdb.DeleteUserPersonalPinsOfParams{UserID: userID, ChatID: chatID, MessageIds: messageIDs}); err != nil {
			return err
		}
		_, err := q.HideSharedPinsOf(ctx, sqlcdb.HideSharedPinsOfParams{UserID: userID, ChatID: chatID, MessageIds: messageIDs})
		return err
	})
}

func (r pinRepo) ClearPins(ctx context.Context, chatID, upTo int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.DeleteSharedPinsUpTo(ctx, sqlcdb.DeleteSharedPinsUpToParams{ChatID: chatID, UpTo: upTo}); err != nil {
			return err
		}
		_, err := q.DeletePersonalPinsUpTo(ctx, sqlcdb.DeletePersonalPinsUpToParams{ChatID: chatID, UpTo: upTo})
		return err
	})
}

func (r pinRepo) ClearUserPins(ctx context.Context, userID, chatID, upTo int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.q.DeleteUserPersonalPinsUpTo(ctx, sqlcdb.DeleteUserPersonalPinsUpToParams{UserID: userID, ChatID: chatID, UpTo: upTo})
	return mapError(err)
}

func (r pinRepo) RemoveUserPins(ctx context.Context, userID, chatID int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if _, err := q.DeleteUserChatPersonalPins(ctx, sqlcdb.DeleteUserChatPersonalPinsParams{UserID: userID, ChatID: chatID}); err != nil {
			return err
		}
		_, err := q.DeleteUserChatPinHides(ctx, sqlcdb.DeleteUserChatPinHidesParams{UserID: userID, ChatID: chatID})
		return err
	})
}
