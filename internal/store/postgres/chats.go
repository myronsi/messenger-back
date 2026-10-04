package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

type chatRepo struct{ s *Store }

var _ ChatRepository = chatRepo{}

func chatFrom(c sqlcdb.Chat) Chat {
	return Chat{
		ID:          c.ID,
		Type:        ChatType(c.Type),
		Name:        c.Name,
		Description: c.Description,
		AvatarURL:   c.AvatarUrl,
		CreatedBy:   c.CreatedBy,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

func participantFrom(p sqlcdb.Participant) Participant {
	return Participant{
		ChatID:            p.ChatID,
		UserID:            p.UserID,
		Role:              Role(p.Role),
		LastReadMessageID: p.LastReadMessageID,
		JoinedAt:          p.JoinedAt,
	}
}

func (r chatRepo) CreateDirect(ctx context.Context, creatorID, otherID int64) (Chat, bool, error) {
	if creatorID == otherID {
		return Chat{}, false, fmt.Errorf("%w: a direct chat needs two different users", ErrInvalid)
	}
	low, high := min(creatorID, otherID), max(creatorID, otherID)
	key := fmt.Sprintf("%d:%d", low, high)

	var chat Chat
	created := false
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		row, err := q.InsertDirectChat(ctx, sqlcdb.InsertDirectChatParams{DirectKey: &key, CreatedBy: &creatorID})
		if errors.Is(err, pgx.ErrNoRows) {
			// The pair has a chat already (possibly committed a moment ago by a concurrent call).
			row, err = q.GetDirectChat(ctx, &key)
			chat = chatFrom(row)
			return err
		}
		if err != nil {
			return err
		}
		for _, uid := range []int64{low, high} {
			if _, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: row.ID, UserID: uid, Role: string(RoleMember)}); err != nil {
				return err
			}
		}
		chat, created = chatFrom(row), true
		return nil
	})
	if err != nil {
		return Chat{}, false, err
	}
	return chat, created, nil
}

func (r chatRepo) CreateGroup(ctx context.Context, g NewGroup) (Chat, error) {
	var chat Chat
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		row, err := q.InsertGroupChat(ctx, sqlcdb.InsertGroupChatParams{
			Name: &g.Name, Description: g.Description, AvatarUrl: g.AvatarURL, CreatedBy: &g.OwnerID,
		})
		if err != nil {
			return err
		}
		if _, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: row.ID, UserID: g.OwnerID, Role: string(RoleOwner)}); err != nil {
			return err
		}
		members := slices.Clone(g.MemberIDs)
		slices.Sort(members)
		for i, uid := range members {
			if uid == g.OwnerID || (i > 0 && members[i-1] == uid) {
				continue
			}
			if _, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: row.ID, UserID: uid, Role: string(RoleMember)}); err != nil {
				return err
			}
		}
		chat = chatFrom(row)
		return nil
	})
	if err != nil {
		return Chat{}, err
	}
	return chat, nil
}

func (r chatRepo) Get(ctx context.Context, id int64) (Chat, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	c, err := r.s.q.GetChat(ctx, id)
	if err != nil {
		return Chat{}, mapError(err)
	}
	return chatFrom(c), nil
}

func (r chatRepo) ListForUser(ctx context.Context, userID int64, limit int) ([]Chat, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListUserChats(ctx, sqlcdb.ListUserChatsParams{UserID: userID, MaxRows: clampLimit(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Chat, len(rows))
	for i, c := range rows {
		out[i] = chatFrom(c)
	}
	return out, nil
}

func (r chatRepo) Participants(ctx context.Context, chatID int64) ([]Participant, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListParticipants(ctx, chatID)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]Participant, len(rows))
	for i, p := range rows {
		out[i] = participantFrom(p)
	}
	return out, nil
}

func (r chatRepo) Participant(ctx context.Context, chatID, userID int64) (Participant, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p, err := r.s.q.GetParticipant(ctx, sqlcdb.GetParticipantParams{ChatID: chatID, UserID: userID})
	if err != nil {
		return Participant{}, mapError(err)
	}
	return participantFrom(p), nil
}

// lockGroup locks the chat row, so membership changes of one group run one after another.
func lockGroup(ctx context.Context, q *sqlcdb.Queries, chatID int64) error {
	c, err := q.LockChat(ctx, chatID)
	if err != nil {
		return err
	}
	if ChatType(c.Type) != ChatGroup {
		return ErrNotGroup
	}
	return nil
}

func (r chatRepo) AddMember(ctx context.Context, chatID, userID int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockGroup(ctx, q, chatID); err != nil {
			return err
		}
		_, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: chatID, UserID: userID, Role: string(RoleMember)})
		return err
	})
}

func (r chatRepo) RemoveMember(ctx context.Context, chatID, userID int64) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockGroup(ctx, q, chatID); err != nil {
			return err
		}
		p, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: userID})
		if err != nil {
			return err
		}
		if Role(p.Role) == RoleOwner {
			return ErrOwnerMustTransfer
		}
		_, err = q.RemoveParticipant(ctx, sqlcdb.RemoveParticipantParams{ChatID: chatID, UserID: userID})
		return err
	})
}

func (r chatRepo) SetRole(ctx context.Context, chatID, userID int64, role Role) error {
	switch role {
	case RoleAdmin, RoleModerator, RoleMember:
	default:
		return fmt.Errorf("%w: role must be admin, moderator or member", ErrInvalid)
	}
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockGroup(ctx, q, chatID); err != nil {
			return err
		}
		p, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: userID})
		if err != nil {
			return err
		}
		if Role(p.Role) == RoleOwner {
			return ErrOwnerMustTransfer
		}
		return setRole(ctx, q, chatID, userID, role)
	})
}

func (r chatRepo) TransferOwnership(ctx context.Context, chatID, fromID, toID int64) error {
	if fromID == toID {
		return fmt.Errorf("%w: the new owner must be someone else", ErrInvalid)
	}
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := lockGroup(ctx, q, chatID); err != nil {
			return err
		}
		from, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: fromID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && Role(from.Role) != RoleOwner) {
			return ErrNotOwner
		}
		if err != nil {
			return err
		}
		if _, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: toID}); err != nil {
			return err
		}
		// Demote first: a group has at most one owner at any moment.
		if err := setRole(ctx, q, chatID, fromID, RoleAdmin); err != nil {
			return err
		}
		return setRole(ctx, q, chatID, toID, RoleOwner)
	})
}

func (r chatRepo) MarkRead(ctx context.Context, chatID, userID, messageID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	n, err := r.s.q.MarkRead(ctx, sqlcdb.MarkReadParams{ChatID: chatID, UserID: userID, MessageID: messageID})
	return affected(n, err)
}

func (r chatRepo) Delete(ctx context.Context, chatID int64) ([]string, error) {
	var keys []string
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		var err error
		if keys, err = q.ListAttachmentKeysOfChats(ctx, []int64{chatID}); err != nil {
			return err
		}
		n, err := q.DeleteChat(ctx, chatID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}
