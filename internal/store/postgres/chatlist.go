package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

func entryFrom(r sqlcdb.ListChatEntriesRow) ChatEntry {
	return ChatEntry{
		Chat: chatFrom(sqlcdb.Chat{
			ID: r.ID, Type: r.Type, Name: r.Name, Description: r.Description, AvatarUrl: r.AvatarUrl, DirectKey: r.DirectKey,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, AvatarAttachmentID: r.AvatarAttachmentID,
			LastMessageID: r.LastMessageID, LastActivityAt: r.LastActivityAt,
		}),
		Role: Role(r.Role), LastReadMessageID: r.LastReadMessageID, PinnedAt: r.PinnedAt, SortAt: r.SortAt,
	}
}

func (r chatRepo) DirectBetween(ctx context.Context, a, b int64) (Chat, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	key := fmt.Sprintf("%d:%d", min(a, b), max(a, b))
	c, err := r.s.q.GetDirectChat(ctx, &key)
	if err != nil {
		return Chat{}, mapError(err)
	}
	return chatFrom(c), nil
}

func (r chatRepo) TouchActivity(ctx context.Context, chatID, messageID int64, at time.Time) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	return mapError(r.s.q.TouchChatActivity(ctx, sqlcdb.TouchChatActivityParams{ChatID: chatID, MessageID: messageID, At: at}))
}

func (r chatRepo) Entries(ctx context.Context, userID int64, after *ChatCursor, limit int) ([]ChatEntry, error) {
	return r.EntriesOfType(ctx, userID, "", after, limit)
}

func (r chatRepo) Entry(ctx context.Context, userID, chatID int64) (ChatEntry, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetChatEntry(ctx, sqlcdb.GetChatEntryParams{UserID: userID, ChatID: chatID})
	if err != nil {
		return ChatEntry{}, mapError(err)
	}
	return entryFrom(sqlcdb.ListChatEntriesRow(row)), nil
}

func (r chatRepo) OtherMembers(ctx context.Context, userID int64, chatIDs []int64) ([]ReadMarker, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	rows, err := r.s.q.ListOtherParticipants(ctx, sqlcdb.ListOtherParticipantsParams{ChatIds: chatIDs, UserID: userID})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ReadMarker, len(rows))
	for i, row := range rows {
		out[i] = ReadMarker{ChatID: row.ChatID, UserID: row.UserID, MessageID: row.LastReadMessageID, At: row.LastReadAt}
	}
	return out, nil
}

func (r chatRepo) Pin(ctx context.Context, userID, chatID int64, maxPins int) error {
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		// The user's row serializes concurrent pins, so the limit holds.
		if _, err := q.LockUser(ctx, userID); err != nil {
			return err
		}
		if _, err := q.GetParticipant(ctx, sqlcdb.GetParticipantParams{ChatID: chatID, UserID: userID}); err != nil {
			return err
		}
		n, err := q.CountPins(ctx, userID)
		if err != nil {
			return err
		}
		added, err := q.PinChat(ctx, sqlcdb.PinChatParams{UserID: userID, ChatID: chatID})
		if err != nil {
			return err
		}
		if added == 1 && n >= int64(maxPins) {
			return ErrConflict
		}
		return nil
	})
}

func (r chatRepo) Unpin(ctx context.Context, userID, chatID int64) error {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	_, err := r.s.q.UnpinChat(ctx, sqlcdb.UnpinChatParams{UserID: userID, ChatID: chatID})
	return mapError(err)
}

type approvalRepo struct{ s *Store }

var _ ApprovalRepository = approvalRepo{}

func requestFrom(r sqlcdb.ApprovalRequest) ApprovalRequest {
	return ApprovalRequest{
		ID: r.ID, Type: r.Type, RequesterID: r.RequesterID, RecipientID: r.RecipientID, Status: r.Status,
		MessageText: r.MessageText, ChatID: r.ChatID, CreatedAt: r.CreatedAt, RespondedAt: r.RespondedAt,
	}
}

func (r approvalRepo) RequestDirect(ctx context.Context, requesterID, recipientID int64, message *string) (ApprovalRequest, bool, error) {
	var (
		req     ApprovalRequest
		created bool
	)
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		key := directKey(requesterID, recipientID)
		if err := q.LockDirectPair(ctx, key); err != nil {
			return err
		}
		// Under the pair's lock: a chat opened a moment ago (an approval) makes the request pointless.
		if _, err := q.GetDirectChat(ctx, &key); err == nil {
			return ErrConflict
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		row, err := q.CreateDirectRequest(ctx, sqlcdb.CreateDirectRequestParams{RequesterID: requesterID, RecipientID: recipientID, MessageText: message})
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
		return ApprovalRequest{}, false, err
	}
	return req, created, nil
}

func (r approvalRepo) Get(ctx context.Context, id int64) (ApprovalRequest, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	row, err := r.s.q.GetApprovalRequest(ctx, id)
	if err != nil {
		return ApprovalRequest{}, mapError(err)
	}
	return requestFrom(row), nil
}

func (r approvalRepo) Pending(ctx context.Context, recipientID, beforeID int64, limit int) ([]ApprovalRequest, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := sqlcdb.ListPendingRequestsParams{RecipientID: recipientID, MaxRows: clampLimit(limit)}
	if beforeID > 0 {
		p.BeforeID = &beforeID
	}
	rows, err := r.s.q.ListPendingRequests(ctx, p)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ApprovalRequest, len(rows))
	for i, row := range rows {
		out[i] = requestFrom(row)
	}
	return out, nil
}

// respond locks a pending request of the recipient and records the answer; directOnly refuses group
// invitations. For a direct-message request the pair's lock is taken first, in the order every path that opens
// a chat or asks for one takes its locks.
func respond(ctx context.Context, q *sqlcdb.Queries, id, recipientID int64, status string, directOnly bool) (ApprovalRequest, error) {
	peek, err := q.GetApprovalRequest(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (peek.RecipientID != recipientID || (directOnly && peek.Type != RequestDirectMessage))) {
		return ApprovalRequest{}, ErrNotFound
	}
	if err != nil {
		return ApprovalRequest{}, err
	}
	if peek.Type == RequestDirectMessage {
		if err := q.LockDirectPair(ctx, directKey(peek.RequesterID, peek.RecipientID)); err != nil {
			return ApprovalRequest{}, err
		}
	}
	row, err := q.LockApprovalRequest(ctx, id)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if row.Status != RequestPending {
		return ApprovalRequest{}, ErrConflict
	}
	if _, err := q.RespondToRequest(ctx, sqlcdb.RespondToRequestParams{ID: id, Status: status}); err != nil {
		return ApprovalRequest{}, err
	}
	req := requestFrom(row)
	now := time.Now()
	req.Status, req.RespondedAt = status, &now
	return req, nil
}

func (r approvalRepo) ApproveDirect(ctx context.Context, id, recipientID int64) (ApprovalRequest, Chat, bool, []ApprovalRequest, error) {
	var (
		req     ApprovalRequest
		chat    Chat
		created bool
		closed  []ApprovalRequest
	)
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		var err error
		if req, err = respond(ctx, q, id, recipientID, RequestApproved, true); err != nil {
			return err
		}
		chat, created, closed, err = createDirect(ctx, q, req.RequesterID, req.RecipientID)
		return err
	})
	if err != nil {
		return ApprovalRequest{}, Chat{}, false, nil, err
	}
	return req, chat, created, closed, nil
}

func (r approvalRepo) Reject(ctx context.Context, id, recipientID int64) (ApprovalRequest, error) {
	var req ApprovalRequest
	err := r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		var err error
		req, err = respond(ctx, q, id, recipientID, RequestRejected, false)
		return err
	})
	if err != nil {
		return ApprovalRequest{}, err
	}
	return req, nil
}

func (r chatRepo) IDsAfter(ctx context.Context, afterID int64, since *time.Time, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var (
		ids []int64
		err error
	)
	if since != nil {
		ids, err = r.s.q.ListChatIDsActiveSince(ctx, sqlcdb.ListChatIDsActiveSinceParams{Since: *since, AfterID: afterID, MaxRows: clampLimit(limit)})
	} else {
		ids, err = r.s.q.ListChatIDsAfter(ctx, sqlcdb.ListChatIDsAfterParams{AfterID: afterID, MaxRows: clampLimit(limit)})
	}
	return ids, mapError(err)
}
