package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// Errors of a search.
var (
	// ErrInvalid: the query breaks a rule (the text says which).
	ErrInvalid = errors.New("invalid search")
	// ErrUnavailable: Elasticsearch did not answer; the client may retry.
	ErrUnavailable = errors.New("search unavailable")
	// ErrBadCursor: a cursor that the search did not make.
	ErrBadCursor = errors.New("unknown cursor")
)

// Query is a message search.
type Query struct {
	Text string
	// ChatID limits the search to one chat (0: every chat of the user).
	ChatID   int64
	SenderID int64
	Type     string
	From, To time.Time
	After    string
	Limit    int
}

// Hit is a message found, as the store holds it now, and an excerpt with the matches between MarkStart and
// MarkEnd.
type Hit struct {
	Message   scylla.Message
	Highlight *string
}

// Access tells the searcher which chats a user is in and which messages they can see (messages.Service and
// the social repository).
type Access interface {
	// ChatIDs are the chats the user is in.
	ChatIDs(ctx context.Context, userID int64) ([]int64, error)
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
	// Visible: the message exists, is not deleted and not hidden for the user.
	Visible(ctx context.Context, userID, chatID, messageID int64) (bool, error)
}

// Searcher answers searches.
type Searcher struct {
	ix     *Index
	access Access
	msgs   interface {
		Get(ctx context.Context, chatID, messageID int64) (scylla.Message, error)
	}
}

// NewSearcher returns the searcher.
func NewSearcher(ix *Index, access Access, msgs Messages) *Searcher {
	return &Searcher{ix: ix, access: access, msgs: msgs}
}

// Query limits.
const (
	MinQuery     = 2
	MaxQuery     = 128
	MaxLimit     = 50
	DefaultLimit = 20
)

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }

func parseCursor(s string) ([]any, error) {
	ms, mid, ok := strings.Cut(s, "_")
	t, err := strconv.ParseInt(ms, 10, 64)
	if !ok || err != nil || mid == "" {
		return nil, ErrBadCursor
	}
	if _, err := strconv.ParseInt(mid, 10, 64); err != nil {
		return nil, ErrBadCursor
	}
	return []any{t, mid}, nil
}

type esHits struct {
	Hits struct {
		Hits []struct {
			ID     string `json:"_id"`
			Source struct {
				ChatID string `json:"chat_id"`
			} `json:"_source"`
			Highlight map[string][]string `json:"highlight"`
			Sort      []any               `json:"sort"`
		} `json:"hits"`
	} `json:"hits"`
}

// Search finds the user's messages that match the query, newest first, and returns the cursor of the next page
// ("" at the end). Hits are limited to the user's chats in Elasticsearch and checked against the message store
// again before they are returned, so a lagging index never shows a deleted or hidden message.
func (s *Searcher) Search(ctx context.Context, userID int64, q Query) ([]Hit, string, error) {
	text := strings.TrimSpace(q.Text)
	if n := utf8.RuneCountInString(text); n < MinQuery || n > MaxQuery {
		return nil, "", invalid(fmt.Sprintf("q must be %d to %d characters", MinQuery, MaxQuery))
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	q.Limit = min(q.Limit, MaxLimit)

	var chats []string
	if q.ChatID != 0 {
		ok, err := s.access.IsMember(ctx, q.ChatID, userID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !ok {
			return nil, "", errNotMember
		}
		chats = []string{id(q.ChatID)}
	} else {
		ids, err := s.access.ChatIDs(ctx, userID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		for _, c := range ids {
			chats = append(chats, id(c))
		}
	}
	if len(chats) == 0 {
		return nil, "", nil
	}

	filter := []any{map[string]any{"terms": map[string]any{"chat_id": chats}}}
	if q.SenderID != 0 {
		filter = append(filter, map[string]any{"term": map[string]any{"sender_id": id(q.SenderID)}})
	}
	if q.Type != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"type": q.Type}})
	}
	if !q.From.IsZero() || !q.To.IsZero() {
		r := map[string]any{}
		if !q.From.IsZero() {
			r["gte"] = q.From.UTC().Format(time.RFC3339Nano)
		}
		if !q.To.IsZero() {
			r["lt"] = q.To.UTC().Format(time.RFC3339Nano)
		}
		filter = append(filter, map[string]any{"range": map[string]any{"created_at": r}})
	}
	body := map[string]any{
		"size":             q.Limit + 1,
		"track_total_hits": false,
		"_source":          []string{"chat_id"},
		"query": map[string]any{"bool": map[string]any{
			"must": []any{map[string]any{"multi_match": map[string]any{
				"query": text, "operator": "and", "type": "most_fields",
				"fields": []string{"content", "content.en", "content.ru", "file_name"},
			}}},
			"filter":   filter,
			"must_not": []any{map[string]any{"term": map[string]any{"hidden_for": id(userID)}}},
		}},
		"sort": []any{map[string]any{"created_at": "desc"}, map[string]any{"message_id": "desc"}},
		"highlight": map[string]any{
			"pre_tags": []string{MarkStart}, "post_tags": []string{MarkEnd},
			"fields": map[string]any{
				"content":    map[string]any{"number_of_fragments": 1, "fragment_size": 160},
				"content.en": map[string]any{"number_of_fragments": 1, "fragment_size": 160},
				"content.ru": map[string]any{"number_of_fragments": 1, "fragment_size": 160},
				"file_name":  map[string]any{"number_of_fragments": 0},
			},
		},
	}
	if q.After != "" {
		after, err := parseCursor(q.After)
		if err != nil {
			return nil, "", err
		}
		body["search_after"] = after
	}
	var res esHits
	if err := s.ix.es.Do(ctx, "POST", "/"+s.ix.alias+"/_search", body, &res); err != nil {
		if errors.Is(err, elastic.ErrNotFound) {
			return nil, "", nil // no index yet: nothing indexed
		}
		return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	raw := res.Hits.Hits
	next := ""
	if len(raw) > q.Limit {
		raw = raw[:q.Limit]
		next = cursor(raw[len(raw)-1].Sort)
	}
	out := make([]Hit, 0, len(raw))
	for _, h := range raw {
		chatID, err1 := strconv.ParseInt(h.Source.ChatID, 10, 64)
		msgID, err2 := strconv.ParseInt(h.ID, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		ok, err := s.access.Visible(ctx, userID, chatID, msgID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if !ok {
			continue
		}
		m, err := s.msgs.Get(ctx, chatID, msgID)
		if err != nil {
			continue // deleted between the check and now
		}
		out = append(out, Hit{Message: m, Highlight: excerpt(h.Highlight)})
	}
	return out, next, nil
}

// errNotMember answers a search in a chat the user is not in like one in a chat that does not exist.
var errNotMember = fmt.Errorf("%w: not a member", ErrNotFound)

// ErrNotFound: the chat does not exist or the user is not in it.
var ErrNotFound = errors.New("not found")

func excerpt(h map[string][]string) *string {
	for _, f := range []string{"content", "content.en", "content.ru", "file_name"} {
		if v := h[f]; len(v) > 0 && v[0] != "" {
			s := v[0]
			return &s
		}
	}
	return nil
}
