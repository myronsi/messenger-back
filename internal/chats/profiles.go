package chats

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const MaxMemberQuery = 64

func encodeMemberCursor(c postgres.MemberCursor) string {
	raw := strconv.FormatInt(int64(c.Rank), 10) + "." + strconv.FormatInt(c.UserID, 10) + "." + c.Name
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeMemberCursor(s string) (*postgres.MemberCursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, badCursor()
	}
	parts := strings.SplitN(string(b), ".", 3)
	if len(parts) != 3 {
		return nil, badCursor()
	}
	rank, err1 := strconv.ParseInt(parts[0], 10, 32)
	id, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || !utf8.ValidString(parts[2]) {
		return nil, badCursor()
	}
	return &postgres.MemberCursor{Rank: int32(rank), UserID: id, Name: parts[2]}, nil
}

func encodeGroupCursor(c postgres.CommonGroupCursor) string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeGroupCursor(s string) (*postgres.CommonGroupCursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, badCursor()
	}
	parts := strings.Split(string(b), ".")
	if len(parts) != 2 {
		return nil, badCursor()
	}
	us, err1 := strconv.ParseInt(parts[0], 10, 64)
	id, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return nil, badCursor()
	}
	return &postgres.CommonGroupCursor{At: time.UnixMicro(us), ID: id}, nil
}

func pageSize(limit int) int {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

func (s *Service) GroupMembers(ctx context.Context, viewer, chatID int64, query, after string, limit int) ([]Member, string, int64, error) {
	if utf8.RuneCountInString(query) > MaxMemberQuery {
		return nil, "", 0, fmt.Errorf("%w: q is at most %d characters", ErrInvalid, MaxMemberQuery)
	}
	cur, err := decodeMemberCursor(after)
	if err != nil {
		return nil, "", 0, err
	}
	chat, err := s.d.Store.Chats().Get(ctx, chatID)
	if err != nil {
		return nil, "", 0, notFoundOr(err)
	}
	if chat.Type != postgres.ChatGroup {
		return nil, "", 0, ErrNotFound
	}
	if _, err := s.d.Store.Chats().Participant(ctx, chatID, viewer); err != nil {
		return nil, "", 0, notFoundOr(err)
	}
	limit = pageSize(limit)
	entries, total, err := s.d.Store.Chats().GroupMembersPage(ctx, chatID, query, cur, limit+1)
	if err != nil {
		return nil, "", 0, unavailable(err)
	}
	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		next = encodeMemberCursor(entries[limit-1].Cursor)
	}
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.UserID
	}
	people, err := s.d.Directory.ForViewer(ctx, viewer, ids)
	if err != nil {
		return nil, "", 0, unavailable(err)
	}
	out := make([]Member, len(entries))
	for i, e := range entries {
		out[i] = Member{User: people[e.UserID], Role: e.Role, JoinedAt: e.JoinedAt}
	}
	return out, next, total, nil
}

func (s *Service) CommonGroups(ctx context.Context, viewer, userID int64, after string, limit int) ([]postgres.CommonGroup, string, error) {
	cur, err := decodeGroupCursor(after)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.d.Store.Users().Get(ctx, userID); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			return nil, "", ErrNotFound
		}
		return nil, "", unavailable(err)
	}
	if userID == viewer {
		return []postgres.CommonGroup{}, "", nil
	}
	limit = pageSize(limit)
	groups, err := s.d.Store.Chats().CommonGroups(ctx, viewer, userID, cur, limit+1)
	if err != nil {
		return nil, "", unavailable(err)
	}
	next := ""
	if len(groups) > limit {
		groups = groups[:limit]
		last := groups[limit-1]
		next = encodeGroupCursor(postgres.CommonGroupCursor{At: last.LastActivityAt, ID: last.ID})
	}
	return groups, next, nil
}
