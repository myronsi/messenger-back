package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

// Group roles that may manage a group: change it, its members and their roles.
var managers = []Role{RoleOwner, RoleAdmin}

// actingIn locks the group and the actor's membership and checks the actor's role, inside the caller's
// transaction, so the role cannot change between the check and the change. ErrNotFound: no such group or the
// actor is not in it; ErrForbidden: the role does not allow it.
func actingIn(ctx context.Context, q *sqlcdb.Queries, chatID, actorID int64, allowed []Role) error {
	if err := lockGroup(ctx, q, chatID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotGroup) {
			return ErrNotFound
		}
		return err
	}
	p, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: actorID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !slices.Contains(allowed, Role(p.Role)) {
		return ErrForbidden
	}
	return nil
}

func (r chatRepo) EntriesOfType(ctx context.Context, userID int64, t ChatType, after *ChatCursor, limit int) ([]ChatEntry, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := sqlcdb.ListChatEntriesParams{UserID: userID, MaxRows: clampLimit(limit)}
	if t != "" {
		s := string(t)
		p.ChatType = &s
	}
	if after != nil {
		unpinned := int32(0)
		if after.Unpinned {
			unpinned = 1
		}
		p.AfterChatID, p.AfterUnpinned, p.AfterSortAt = &after.ChatID, &unpinned, &after.SortAt
	}
	rows, err := r.s.q.ListChatEntries(ctx, p)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ChatEntry, len(rows))
	for i, row := range rows {
		out[i] = entryFrom(row)
	}
	return out, nil
}

func (r chatRepo) UpdateGroup(ctx context.Context, chatID, actorID int64, name *string, setDescription bool, description *string) (Chat, error) {
	var chat Chat
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		row, err := q.UpdateGroupInfo(ctx, sqlcdb.UpdateGroupInfoParams{ID: chatID, Name: name, SetDescription: setDescription, Description: description})
		if err != nil {
			return err
		}
		chat = chatFrom(row)
		return nil
	})
	if err != nil {
		return Chat{}, mapError(err)
	}
	return chat, nil
}

func (r chatRepo) AddMemberAs(ctx context.Context, chatID, actorID, userID int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		// User before chat, like AddMember and DeleteAccount.
		if _, err := q.LockUserShared(ctx, userID); err != nil {
			return err
		}
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		_, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: chatID, UserID: userID, Role: string(RoleMember)})
		return err
	}))
}

func (r chatRepo) RemoveMemberAs(ctx context.Context, chatID, actorID, userID int64) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if actorID != userID {
			if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
				return err
			}
		} else if err := lockGroup(ctx, q, chatID); err != nil {
			if errors.Is(err, ErrNotGroup) {
				return ErrNotFound
			}
			return err
		}
		p, err := q.LockParticipant(ctx, sqlcdb.LockParticipantParams{ChatID: chatID, UserID: userID})
		if err != nil {
			return err
		}
		if Role(p.Role) == RoleOwner {
			return ErrOwnerMustTransfer
		}
		if _, err := q.RemoveParticipant(ctx, sqlcdb.RemoveParticipantParams{ChatID: chatID, UserID: userID}); err != nil {
			return err
		}
		// A pin of a chat one is no longer in only takes up a slot.
		_, err = q.UnpinChat(ctx, sqlcdb.UnpinChatParams{UserID: userID, ChatID: chatID})
		return err
	}))
}

func (r chatRepo) SetRoleAs(ctx context.Context, chatID, actorID, userID int64, role Role) error {
	switch role {
	case RoleAdmin, RoleModerator, RoleMember:
	default:
		return ErrInvalid
	}
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
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
	}))
}

func (r chatRepo) DeleteGroupAs(ctx context.Context, chatID, actorID int64) ([]string, []int64, error) {
	var (
		keys    []string
		members []int64
	)
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, []Role{RoleOwner}); err != nil {
			return err
		}
		var err error
		if members, err = q.ListParticipantIDs(ctx, chatID); err != nil {
			return err
		}
		if keys, err = q.ListAttachmentKeysOfChats(ctx, []int64{chatID}); err != nil {
			return err
		}
		_, err = q.DeleteChat(ctx, chatID)
		return err
	})
	if err != nil {
		return nil, nil, mapError(err)
	}
	return keys, members, nil
}

func (r chatRepo) SetGroupAvatarAs(ctx context.Context, chatID, actorID int64, attachmentID *uuid.UUID) error {
	return mapError(r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		_, err := q.SetChatAvatar(ctx, sqlcdb.SetChatAvatarParams{ChatID: chatID, AttachmentID: attachmentID})
		return err
	}))
}

func (r chatRepo) InviteAs(ctx context.Context, chatID, actorID, userID int64) (ApprovalRequest, bool, error) {
	var (
		req     ApprovalRequest
		created bool
	)
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := actingIn(ctx, q, chatID, actorID, managers); err != nil {
			return err
		}
		if _, err := q.GetParticipant(ctx, sqlcdb.GetParticipantParams{ChatID: chatID, UserID: userID}); err == nil {
			return ErrAlreadyParticipant
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err := q.CreateGroupInvite(ctx, sqlcdb.CreateGroupInviteParams{RequesterID: actorID, RecipientID: userID, ChatID: &chatID})
		if err != nil {
			return err
		}
		req = requestFrom(sqlcdb.ApprovalRequest{
			ID: row.ID, Type: row.Type, RequesterID: row.RequesterID, RecipientID: row.RecipientID, Status: row.Status,
			MessageText: row.MessageText, ChatID: row.ChatID, CreatedAt: row.CreatedAt, RespondedAt: row.RespondedAt,
		})
		created = row.Created
		return nil
	})
	if err != nil {
		return ApprovalRequest{}, false, mapError(err)
	}
	return req, created, nil
}

func (r chatRepo) PendingInvitees(ctx context.Context, chatID int64) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	ids, err := r.s.q.PendingInviteRecipients(ctx, &chatID)
	return ids, mapError(err)
}

func (r approvalRepo) ApproveInvite(ctx context.Context, id, recipientID int64) (ApprovalRequest, error) {
	var req ApprovalRequest
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		peek, err := q.GetApprovalRequest(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (peek.RecipientID != recipientID || peek.Type != RequestGroupInvite || peek.ChatID == nil)) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// The lock order of AddMember: the user, then the group.
		if _, err := q.LockUserShared(ctx, recipientID); err != nil {
			return err
		}
		if err := lockGroup(ctx, q, *peek.ChatID); err != nil {
			return err
		}
		row, err := q.LockApprovalRequest(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != RequestPending {
			return ErrConflict
		}
		if _, err := q.RespondToRequest(ctx, sqlcdb.RespondToRequestParams{ID: id, Status: RequestApproved}); err != nil {
			return err
		}
		// A failed insert would abort the transaction, so membership is checked first (the group is locked).
		if _, err := q.GetParticipant(ctx, sqlcdb.GetParticipantParams{ChatID: *row.ChatID, UserID: recipientID}); errors.Is(err, pgx.ErrNoRows) {
			if _, err := q.AddParticipant(ctx, sqlcdb.AddParticipantParams{ChatID: *row.ChatID, UserID: recipientID, Role: string(RoleMember)}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		req = requestFrom(row)
		req.Status = RequestApproved
		return nil
	})
	if err != nil {
		return ApprovalRequest{}, mapError(err)
	}
	return req, nil
}
