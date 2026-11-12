package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres/sqlcdb"
)

const MaxMessageURLs = 20

type MemberCursor struct {
	Rank   int32
	Name   string
	UserID int64
}

type MemberEntry struct {
	UserID   int64
	Role     Role
	JoinedAt time.Time
	Cursor   MemberCursor
}

type CommonGroup struct {
	ID                 int64
	Name               string
	AvatarAttachmentID *uuid.UUID
	LastActivityAt     time.Time
	MemberCount        int
}

type CommonGroupCursor struct {
	At time.Time
	ID int64
}

func (r attachmentRepo) SetMessageURLs(ctx context.Context, chatID, messageID int64, urls []string, at time.Time) error {
	if len(urls) > MaxMessageURLs {
		urls = urls[:MaxMessageURLs]
	}
	return r.s.inTx(ctx, func(ctx context.Context, q *sqlcdb.Queries) error {
		if err := q.DeleteMessageURLs(ctx, sqlcdb.DeleteMessageURLsParams{ChatID: chatID, MessageID: messageID}); err != nil {
			return err
		}
		for _, u := range urls {
			if err := q.InsertMessageURL(ctx, sqlcdb.InsertMessageURLParams{ChatID: chatID, MessageID: messageID, Url: u, CreatedAt: at}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r attachmentRepo) LinkMessageIDs(ctx context.Context, chatID, beforeMessageID int64, limit int) ([]int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	var before *int64
	if beforeMessageID > 0 {
		before = &beforeMessageID
	}
	ids, err := r.s.q.ListLinkMessageIDs(ctx, sqlcdb.ListLinkMessageIDsParams{ChatID: chatID, Before: before, MaxRows: clampLimit(limit)})
	return ids, mapError(err)
}

func memberQuery(query string) *string {
	q := likeEscape(strings.ToLower(strings.TrimSpace(query)))
	if q == "" {
		return nil
	}
	return &q
}

func (r chatRepo) GroupMembersPage(ctx context.Context, chatID int64, query string, after *MemberCursor, limit int) ([]MemberEntry, int64, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	q := memberQuery(query)
	p := sqlcdb.ListGroupMembersPageParams{ChatID: chatID, Q: q, MaxRows: clampLimit(limit)}
	if after != nil {
		p.AfterRank, p.AfterName, p.AfterID = &after.Rank, &after.Name, &after.UserID
	}
	rows, err := r.s.q.ListGroupMembersPage(ctx, p)
	if err != nil {
		return nil, 0, mapError(err)
	}
	total, err := r.s.q.CountGroupMembers(ctx, sqlcdb.CountGroupMembersParams{ChatID: chatID, Q: q})
	if err != nil {
		return nil, 0, mapError(err)
	}
	out := make([]MemberEntry, len(rows))
	for i, row := range rows {
		out[i] = MemberEntry{
			UserID: row.UserID, Role: Role(row.Role), JoinedAt: row.JoinedAt,
			Cursor: MemberCursor{Rank: row.RoleRank, Name: row.SortName, UserID: row.UserID},
		}
	}
	return out, total, nil
}

func (r chatRepo) CommonGroups(ctx context.Context, viewerID, userID int64, after *CommonGroupCursor, limit int) ([]CommonGroup, error) {
	ctx, cancel := r.s.call(ctx)
	defer cancel()
	p := sqlcdb.ListCommonGroupsParams{ViewerID: viewerID, UserID: userID, MaxRows: clampLimit(limit)}
	if after != nil {
		p.AfterAt, p.AfterID = &after.At, &after.ID
	}
	rows, err := r.s.q.ListCommonGroups(ctx, p)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]CommonGroup, len(rows))
	for i, row := range rows {
		g := CommonGroup{ID: row.ID, AvatarAttachmentID: row.AvatarAttachmentID, LastActivityAt: row.LastActivityAt, MemberCount: int(row.MemberCount)}
		if row.Name != nil {
			g.Name = *row.Name
		}
		out[i] = g
	}
	return out, nil
}
