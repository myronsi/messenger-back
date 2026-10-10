package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/elastic"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

// Group is the consumer group of the indexer on the message and chat streams.
const Group = "search-indexer"

// Doc is a message as the index holds it.
type Doc struct {
	MessageID string    `json:"message_id"`
	ChatID    string    `json:"chat_id"`
	SenderID  string    `json:"sender_id,omitempty"`
	Type      string    `json:"type"`
	Content   string    `json:"content,omitempty"`
	FileName  string    `json:"file_name,omitempty"`
	HiddenFor []string  `json:"hidden_for,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Messages is what the indexer reads from the message store.
type Messages interface {
	Get(ctx context.Context, chatID, messageID int64) (scylla.Message, error)
	Page(ctx context.Context, q scylla.PageQuery) (scylla.Page, error)
	DeletedForMe(ctx context.Context, chatID int64) (map[int64][]int64, error)
}

// Attachments names the files of messages.
type Attachments interface {
	Get(ctx context.Context, id uuid.UUID) (postgres.Attachment, error)
}

// Indexer keeps the index in step with the messages.
type Indexer struct {
	ix    *Index
	msgs  Messages
	files Attachments
	log   *slog.Logger
}

// NewIndexer returns the indexer.
func NewIndexer(ix *Index, msgs Messages, files Attachments, log *slog.Logger) *Indexer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Indexer{ix: ix, msgs: msgs, files: files, log: log}
}

func id(n int64) string { return strconv.FormatInt(n, 10) }

// indexable reports whether a message belongs in the index: stored, not deleted, with something to find.
func indexable(m scylla.Message) bool {
	return !m.Deleted && m.Type != scylla.TypeSystem && (m.Content != nil || m.AttachmentID != nil)
}

// doc builds the document of a message (hidden_for is set by the caller when it knows it).
func (in *Indexer) doc(ctx context.Context, m scylla.Message) Doc {
	d := Doc{MessageID: id(m.ID), ChatID: id(m.ChatID), Type: m.Type, CreatedAt: m.CreatedAt.UTC()}
	if m.SenderID != nil {
		d.SenderID = id(*m.SenderID)
	}
	if m.Content != nil {
		d.Content = *m.Content
	}
	if m.AttachmentID != nil {
		if a, err := in.files.Get(ctx, uuid.UUID(*m.AttachmentID)); err == nil {
			d.FileName = a.Filename
		}
	}
	return d
}

// Handle is the consumer's handler for events:messages and events:chats.
func (in *Indexer) Handle(ctx context.Context, entry redis.Entry) error {
	e, err := events.Parse(entry.Fields)
	if errors.Is(err, events.ErrMalformed) {
		in.log.WarnContext(ctx, "skipping malformed event", "entry", entry.ID)
		return nil
	}
	switch e.Type {
	case events.MessageCreated, events.MessageEdited:
		return in.refresh(ctx, e.ChatID, e.MessageID)
	case events.MessageDeleted:
		return in.delete(ctx, e.MessageID)
	case events.MessageHidden:
		return in.hide(ctx, e.MessageID, e.UserID)
	case events.ChatDeleted:
		return in.dropChat(ctx, e.ChatID)
	}
	return nil
}

// refresh writes the message's current state. The text and file are replaced; hidden_for is kept, so an
// edit does not bring a message back for users who deleted it for themselves.
func (in *Indexer) refresh(ctx context.Context, chatID, messageID int64) error {
	m, err := in.msgs.Get(ctx, chatID, messageID)
	if errors.Is(err, scylla.ErrNotFound) {
		return in.delete(ctx, messageID) // gone (the chat was deleted meanwhile)
	}
	if err != nil {
		return err
	}
	if !indexable(m) {
		return in.delete(ctx, messageID)
	}
	d := in.doc(ctx, m)
	return in.ix.es.Do(ctx, "POST", "/"+in.ix.alias+"/_update/"+d.MessageID+"?retry_on_conflict=3",
		map[string]any{"doc": d, "doc_as_upsert": true}, nil)
}

func (in *Indexer) delete(ctx context.Context, messageID int64) error {
	err := in.ix.es.Do(ctx, "DELETE", "/"+in.ix.alias+"/_doc/"+id(messageID), nil, nil)
	if errors.Is(err, elastic.ErrNotFound) {
		return nil
	}
	return err
}

// hide adds the user to the message's hidden_for. When the document does not exist yet (its created event is
// still to come), a stub with only hidden_for is written, which the created event then fills.
func (in *Indexer) hide(ctx context.Context, messageID, userID int64) error {
	if userID == 0 {
		return nil
	}
	u := id(userID)
	return in.ix.es.Do(ctx, "POST", "/"+in.ix.alias+"/_update/"+id(messageID)+"?retry_on_conflict=3", map[string]any{
		"script": map[string]any{
			"lang":   "painless",
			"source": "if (ctx._source.hidden_for == null) { ctx._source.hidden_for = [params.u] } else if (!ctx._source.hidden_for.contains(params.u)) { ctx._source.hidden_for.add(params.u) }",
			"params": map[string]any{"u": u},
		},
		"upsert": map[string]any{"message_id": id(messageID), "hidden_for": []string{u}},
	}, nil)
}

func (in *Indexer) dropChat(ctx context.Context, chatID int64) error {
	return in.ix.es.Do(ctx, "POST", "/"+in.ix.alias+"/_delete_by_query?conflicts=proceed&refresh=false",
		map[string]any{"query": map[string]any{"term": map[string]any{"chat_id": id(chatID)}}}, nil)
}

// bulk writes documents (index) and removes ids (delete) into the named index in one request.
func (in *Indexer) bulk(ctx context.Context, index string, docs []Doc, deletes []string) error {
	if len(docs) == 0 && len(deletes) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, d := range docs {
		_ = enc.Encode(map[string]any{"index": map[string]any{"_index": index, "_id": d.MessageID}})
		_ = enc.Encode(d)
	}
	for _, del := range deletes {
		_ = enc.Encode(map[string]any{"delete": map[string]any{"_index": index, "_id": del}})
	}
	var res struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int `json:"status"`
			Error  *struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := in.ix.es.Do(ctx, "POST", "/_bulk", buf.Bytes(), &res); err != nil {
		return err
	}
	if !res.Errors {
		return nil
	}
	for _, item := range res.Items {
		for op, r := range item {
			if r.Error != nil && (op != "delete" || r.Status != 404) {
				return fmt.Errorf("search bulk %s: %s: %s", op, r.Error.Type, r.Error.Reason)
			}
		}
	}
	return nil
}
