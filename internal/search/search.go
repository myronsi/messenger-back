package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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

// Access tells the searcher which chats a user is in (messages.Service and the social repository).
type Access interface {
	// ChatIDs are the chats the user is in.
	ChatIDs(ctx context.Context, userID int64) ([]int64, error)
	IsMember(ctx context.Context, chatID, userID int64) (bool, error)
}

// Searcher answers searches.
type Searcher struct {
	ix     *Index
	access Access
	msgs   Messages
}

// NewSearcher returns the searcher.
func NewSearcher(ix *Index, access Access, msgs Messages) *Searcher {
	return &Searcher{ix: ix, access: access, msgs: msgs}
}

// Query limits.
const (
	MinQuery     = 2
	MaxQuery     = 256
	MaxLimit     = 100
	DefaultLimit = 50
	// maxChats bounds the chats one search covers (Elasticsearch's terms limit is 65 536).
	maxChats = 60_000
	// checkWorkers read the hits from the message store in parallel.
	checkWorkers = 16
	maxRounds    = 4
	minBatch     = 10
	maxTerms     = 32
)

var (
	exactFields       = []string{"content", "content.en", "content.ru", "content.de", "file_name"}
	prefixFields      = []string{"content.prefix", "file_name.prefix"}
	contentHighlights = []string{"content", "content.de", "content.en", "content.ru", "content.prefix"}
	fileHighlights    = []string{"file_name", "file_name.prefix"}
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

type esHit struct {
	ID     string `json:"_id"`
	Source struct {
		ChatID string `json:"chat_id"`
	} `json:"_source"`
	Highlight map[string][]string `json:"highlight"`
	Sort      []any               `json:"sort"`
}

type esHits struct {
	Hits struct {
		Hits []esHit `json:"hits"`
	} `json:"hits"`
}

func textQuery(text string) map[string]any {
	terms := strings.Fields(text)
	if len(terms) > maxTerms {
		terms = append(terms[:maxTerms-1], strings.Join(terms[maxTerms-1:], " "))
	}
	must := make([]any, 0, len(terms))
	for _, t := range terms {
		exact := map[string]any{"multi_match": map[string]any{"query": t, "operator": "and", "type": "most_fields", "fields": exactFields}}
		if utf8.RuneCountInString(t) < PrefixMin {
			must = append(must, exact)
			continue
		}
		prefix := map[string]any{"multi_match": map[string]any{"query": t, "operator": "and", "type": "most_fields", "fields": prefixFields}}
		must = append(must, map[string]any{"bool": map[string]any{"should": []any{exact, prefix}, "minimum_should_match": 1}})
	}
	return map[string]any{"bool": map[string]any{"must": must}}
}

func request(userID int64, text string, chats []string, q Query) map[string]any {
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
	highlights := map[string]any{}
	for _, f := range contentHighlights {
		highlights[f] = map[string]any{"number_of_fragments": 1, "fragment_size": 160}
	}
	for _, f := range fileHighlights {
		highlights[f] = map[string]any{"number_of_fragments": 0}
	}
	return map[string]any{
		"track_total_hits": false,
		"_source":          []string{"chat_id"},
		"query": map[string]any{"bool": map[string]any{
			"must":     []any{textQuery(text)},
			"filter":   filter,
			"must_not": []any{map[string]any{"term": map[string]any{"hidden_for": id(userID)}}},
		}},
		"sort":      []any{map[string]any{"created_at": "desc"}, map[string]any{"message_id": "desc"}},
		"highlight": map[string]any{"pre_tags": []string{MarkStart}, "post_tags": []string{MarkEnd}, "fields": highlights},
	}
}

// Search finds the user's messages that match the query, newest first, and returns the cursor of the next page
// ("" at the end). Hits are limited to the user's chats in Elasticsearch and checked against the message store
// again before they are returned, so a lagging index never shows a deleted or hidden message. Hits dropped by
// that check are made up for from further results (up to maxRounds queries), so a page is short only at the end.
func (s *Searcher) Search(ctx context.Context, userID int64, q Query) ([]Hit, string, error) {
	text := strings.TrimSpace(q.Text)
	if n := utf8.RuneCountInString(text); n < MinQuery || n > MaxQuery {
		return nil, "", invalid(fmt.Sprintf("q must be %d to %d characters", MinQuery, MaxQuery))
	}
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	q.Limit = min(q.Limit, MaxLimit)
	var after []any
	if q.After != "" {
		var err error
		if after, err = parseCursor(q.After); err != nil {
			return nil, "", err
		}
	}

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
		if len(ids) > maxChats {
			ids = ids[len(ids)-maxChats:] // the most recently joined chats
		}
		for _, c := range ids {
			chats = append(chats, id(c))
		}
	}
	if len(chats) == 0 {
		return nil, "", nil
	}

	body := request(userID, text, chats, q)
	out := make([]Hit, 0, q.Limit)
	batch := q.Limit
	for round := 1; ; round++ {
		body["size"] = batch + 1
		if after != nil {
			body["search_after"] = after
		}
		raw, err := s.query(ctx, body)
		if err != nil {
			return nil, "", err
		}
		more := len(raw) > batch
		raw = raw[:min(len(raw), batch)]
		found, err := s.check(ctx, userID, raw)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		for i, h := range found {
			if h == nil {
				continue
			}
			out = append(out, *h)
			if len(out) == q.Limit {
				if i < len(raw)-1 || more {
					return out, cursor(raw[i].Sort), nil
				}
				return out, "", nil
			}
		}
		if !more {
			return out, "", nil
		}
		after = raw[len(raw)-1].Sort
		if round == maxRounds {
			return out, cursor(after), nil
		}
		batch = min(max(2*(q.Limit-len(out)), minBatch), MaxLimit)
	}
}

func (s *Searcher) query(ctx context.Context, body map[string]any) ([]esHit, error) {
	var res esHits
	if err := s.ix.es.Do(ctx, "POST", "/"+s.ix.alias+"/_search", body, &res); err != nil {
		if errors.Is(err, elastic.ErrNotFound) {
			return nil, nil // no index yet: nothing indexed
		}
		var se *elastic.StatusError
		if errors.As(err, &se) && se.Status/100 == 4 {
			return nil, err // our request was wrong: retrying cannot help (answered as 500)
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return res.Hits.Hits, nil
}

func (s *Searcher) check(ctx context.Context, userID int64, raw []esHit) ([]*Hit, error) {
	found := make([]*Hit, len(raw))
	msgIDs := make([]int64, len(raw))
	byChat := map[int64][]int{}
	for i, h := range raw {
		chatID, err1 := strconv.ParseInt(h.Source.ChatID, 10, 64)
		msgID, err2 := strconv.ParseInt(h.ID, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		msgIDs[i] = msgID
		byChat[chatID] = append(byChat[chatID], i)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	slots := make(chan struct{}, checkWorkers)
	for chatID, hits := range byChat {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			wanted := make([]int64, len(hits))
			for j, i := range hits {
				wanted[j] = msgIDs[i]
			}
			var (
				both           sync.WaitGroup
				msgs           map[int64]scylla.Message
				hidden         map[int64]bool
				errMsg, errHid error
			)
			both.Go(func() { msgs, errMsg = s.msgs.GetMany(ctx, chatID, wanted) })
			both.Go(func() { hidden, errHid = s.msgs.HiddenOf(ctx, userID, chatID, wanted) })
			both.Wait()
			if err := errors.Join(errMsg, errHid); err != nil {
				mu.Lock()
				firstErr = err
				mu.Unlock()
				return
			}
			for _, i := range hits {
				m, ok := msgs[msgIDs[i]]
				if !ok || m.Deleted || hidden[m.ID] {
					continue
				}
				found[i] = &Hit{Message: m, Highlight: current(raw[i].Highlight, m)}
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return found, nil
}

// current keeps an excerpt only if it still matches the message: an index that lags behind an edit must not
// show the text the edit removed.
func current(h map[string][]string, m scylla.Message) *string {
	strip := strings.NewReplacer(MarkStart, "", MarkEnd, "")
	if e := best(h, contentHighlights, func(excerpt string) bool {
		return m.Content != nil && strings.Contains(*m.Content, strip.Replace(excerpt))
	}); e != nil {
		return e
	}
	if m.AttachmentID == nil {
		return nil
	}
	return best(h, fileHighlights, func(string) bool { return true })
}

func best(h map[string][]string, fields []string, valid func(string) bool) *string {
	var (
		out   *string
		marks int
	)
	for _, f := range fields {
		v := h[f]
		if len(v) == 0 || v[0] == "" || !valid(v[0]) {
			continue
		}
		if n := strings.Count(v[0], MarkStart); out == nil || n > marks {
			excerpt := v[0]
			out, marks = &excerpt, n
		}
	}
	return out
}

// errNotMember answers a search in a chat the user is not in like one in a chat that does not exist.
var errNotMember = fmt.Errorf("%w: not a member", ErrNotFound)

// ErrNotFound: the chat does not exist or the user is not in it.
var ErrNotFound = errors.New("not found")
