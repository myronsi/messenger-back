package scylla

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gocql/gocql"

	"github.com/myronsi/messenger-back/internal/ids"
)

// BucketSpan is the time range of one partition of a chat's messages.
const BucketSpan = 10 * 24 * time.Hour

// MaxPage is the largest page Page returns.
const MaxPage = 100

// Message types.
const (
	TypeText   = "text"
	TypeFile   = "file"
	TypeVoice  = "voice"
	TypeSystem = "system"
)

// Reasons a message is hidden from one user.
const (
	HiddenDeletedForMe = "deleted_for_me"
	HiddenNotDelivered = "not_delivered"
)

// ErrNotFound is returned for a message that does not exist (in that chat).
var ErrNotFound = errors.New("message not found")

// Forwarded describes where a forwarded message came from. SenderName is a copy, so it survives the
// original sender deleting the account.
type Forwarded struct {
	MessageID  int64
	SenderID   *int64
	SenderName string
}

// Message is one stored message. Content and AttachmentID are nil once the message is deleted.
type Message struct {
	ChatID       int64
	ID           int64
	SenderID     *int64 // nil for system messages
	Type         string
	Content      *string
	AttachmentID *gocql.UUID
	ReplyTo      *int64
	Forwarded    *Forwarded
	EditedAt     *time.Time
	Deleted      bool
	CreatedAt    time.Time
}

// Reaction is one user's reaction to a message.
type Reaction struct {
	Emoji     string
	UserID    int64
	CreatedAt time.Time
}

// PageQuery selects a page of a chat as seen by Viewer (messages hidden for the viewer are left out). Set at
// most one of Before, After and Around; none means the newest messages.
type PageQuery struct {
	ChatID int64
	Viewer int64
	Before int64
	After  int64
	Around int64
	// Limit is clamped to 1..MaxPage; 0 means 50.
	Limit int
}

// Page is a page of messages, oldest first.
type Page struct {
	Messages []Message
	// HasOlder and HasNewer tell whether messages exist beyond the page in either direction.
	HasOlder bool
	HasNewer bool
}

// MessageRepository stores messages, reactions and per-user hidden messages. It is the seam that lets
// ScyllaDB be replaced (Apache Cassandra runs the same CQL) and lets feature code be tested with fakes.
type MessageRepository interface {
	// Insert stores a new message. It is idempotent: inserting the same message again changes nothing.
	Insert(ctx context.Context, m Message) error
	// Get returns a message of the chat, or ErrNotFound.
	Get(ctx context.Context, chatID, messageID int64) (Message, error)
	// Locate returns the chat of a message, or ErrNotFound.
	Locate(ctx context.Context, messageID int64) (chatID int64, err error)
	Page(ctx context.Context, q PageQuery) (Page, error)
	// Edit replaces the content of a message that is not deleted.
	Edit(ctx context.Context, chatID, messageID int64, content string, at time.Time) error
	// Delete deletes the message for everyone: it keeps its id and shows as deleted.
	Delete(ctx context.Context, chatID, messageID int64) error
	// Hide hides the message from one user.
	Hide(ctx context.Context, userID, chatID, messageID int64, reason string) error
	// Unhide shows a message hidden as not delivered once it is delivered.
	Unhide(ctx context.Context, userID, chatID, messageID int64) error
	AddReaction(ctx context.Context, chatID, messageID, userID int64, emoji string, at time.Time) error
	RemoveReaction(ctx context.Context, chatID, messageID, userID int64, emoji string) error
	// Reactions returns the reactions of the messages, keyed by message id, in one query.
	Reactions(ctx context.Context, chatID int64, messageIDs []int64) (map[int64][]Reaction, error)
	// CountAfter counts the messages newer than afterID that viewer can see and did not send, up to limit.
	// It rebuilds unread counters.
	CountAfter(ctx context.Context, chatID, viewer, afterID int64, limit int) (int, error)
	// DeleteChat removes every message, reaction, location and hidden marker of the chat. It may take long
	// for a big chat; when it fails or is cancelled, calling it again continues where it stopped. Call it once
	// more after DeleteGracePeriod to catch writes that were in flight.
	DeleteChat(ctx context.Context, chatID int64) error
}

// BucketOf returns the bucket of a message created at t.
func BucketOf(t time.Time) int {
	return int(t.UnixMilli() / BucketSpan.Milliseconds())
}

// Messages is the ScyllaDB implementation of MessageRepository.
type Messages struct {
	s       *Store
	timeout time.Duration
}

var _ MessageRepository = (*Messages)(nil)

// NewMessages returns the repository. Every call runs under timeout (a page that reads several buckets
// counts as one call).
func NewMessages(s *Store, timeout time.Duration) *Messages {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Messages{s: s, timeout: timeout}
}

func (r *Messages) session(ctx context.Context) (context.Context, context.CancelFunc, *gocql.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	sess, err := r.s.Session(ctx)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return ctx, cancel, sess, nil
}

const messageColumns = `message_id, sender_id, type, content, attachment_id, reply_to, forwarded_from_message_id,
forwarded_from_sender_id, forwarded_from_sender_name, edited_at, deleted, created_at`

func scanMessage(chatID int64, scan func(dest ...any) bool) (Message, bool) {
	var (
		m          = Message{ChatID: chatID}
		sender     *int64
		content    *string
		attachment *gocql.UUID
		fwdID      *int64
		fwdSender  *int64
		fwdName    *string
		edited     *time.Time
		deleted    *bool
	)
	if !scan(&m.ID, &sender, &m.Type, &content, &attachment, &m.ReplyTo, &fwdID, &fwdSender, &fwdName, &edited, &deleted, &m.CreatedAt) {
		return Message{}, false
	}
	m.SenderID, m.Content, m.AttachmentID, m.EditedAt = sender, content, attachment, edited
	m.Deleted = deleted != nil && *deleted
	if fwdID != nil {
		m.Forwarded = &Forwarded{MessageID: *fwdID, SenderID: fwdSender}
		if fwdName != nil {
			m.Forwarded.SenderName = *fwdName
		}
	}
	if m.Deleted {
		m.Content, m.AttachmentID = nil, nil
	}
	return m, true
}

func validType(t string) bool {
	switch t {
	case TypeText, TypeFile, TypeVoice, TypeSystem:
		return true
	}
	return false
}

// bucketFor returns the bucket of a stored message: computed from a Snowflake id, looked up for a v1 id.
func (r *Messages) bucketFor(ctx context.Context, sess *gocql.Session, chatID, messageID int64) (int, error) {
	if ids.IsSnowflake(messageID) {
		return BucketOf(ids.Time(messageID)), nil
	}
	var chat int64
	var bucket int
	err := sess.Query(`SELECT chat_id, bucket FROM message_locations WHERE message_id = ?`, messageID).
		WithContext(ctx).Scan(&chat, &bucket)
	if errors.Is(err, gocql.ErrNotFound) || (err == nil && chat != chatID) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("locate message: %w", err)
	}
	return bucket, nil
}

// insertBucket returns the bucket of a new message. Snowflake ids determine it; v1 messages (imported by
// the migration) use their creation time.
func insertBucket(m Message) int {
	if ids.IsSnowflake(m.ID) {
		return BucketOf(ids.Time(m.ID))
	}
	return BucketOf(m.CreatedAt)
}

func (r *Messages) Insert(ctx context.Context, m Message) error {
	if !validType(m.Type) || m.ID <= 0 || m.ChatID <= 0 || m.CreatedAt.IsZero() {
		return errors.New("insert message: invalid message")
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	bucket := insertBucket(m)

	// The bucket and the location are written before the message (in parallel, every time: a cache of
	// written buckets would go stale when another instance deletes the chat), so a message that can be read
	// is always reachable by paging and by id. A failure leaves at most a bucket or location without a message, which
	// readers skip; retrying the insert completes it.
	//
	// Every creation write carries the creation time as its write timestamp. Later changes (edits, deletes,
	// the deletion of the chat) are written at their own, later time, so a delayed retry of the insert can
	// never overwrite them and bring back a message or its text.
	ts := m.CreatedAt.UnixMicro()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() {
		errs[0] = sess.Query(`INSERT INTO chat_buckets (chat_id, bucket) VALUES (?, ?) USING TIMESTAMP ?`, m.ChatID, bucket, ts).
			WithContext(ctx).Idempotent(true).Exec()
	})
	wg.Go(func() {
		// Provisional until the message is stored: an insert that never completes leaves no location behind
		// for longer than the TTL. A completed insert rewrites it without TTL (and a newer timestamp).
		errs[1] = sess.Query(`INSERT INTO message_locations (message_id, chat_id, bucket) VALUES (?, ?, ?) USING TTL ? AND TIMESTAMP ?`,
			m.ID, m.ChatID, bucket, int(provisionalLocationTTL.Seconds()), ts).WithContext(ctx).Idempotent(true).Exec()
	})
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	var fwdID, fwdSender *int64
	var fwdName *string
	if f := m.Forwarded; f != nil {
		fwdID, fwdSender, fwdName = &f.MessageID, f.SenderID, &f.SenderName
	}
	err = sess.Query(`INSERT INTO messages (chat_id, bucket, `+messageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) USING TIMESTAMP ?`,
		m.ChatID, bucket, m.ID, m.SenderID, m.Type, m.Content, m.AttachmentID, m.ReplyTo,
		fwdID, fwdSender, fwdName, m.EditedAt, m.Deleted, m.CreatedAt, ts,
	).WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	err = sess.Query(`INSERT INTO message_locations (message_id, chat_id, bucket) VALUES (?, ?, ?) USING TIMESTAMP ?`, m.ID, m.ChatID, bucket, ts+1).
		WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

// provisionalLocationTTL is how long the location of an insert that did not complete survives.
const provisionalLocationTTL = 7 * 24 * time.Hour

func (r *Messages) get(ctx context.Context, sess *gocql.Session, chatID, messageID int64) (Message, int, error) {
	bucket, err := r.bucketFor(ctx, sess, chatID, messageID)
	if err != nil {
		return Message{}, 0, err
	}
	iter := sess.Query(`SELECT `+messageColumns+` FROM messages WHERE chat_id = ? AND bucket = ? AND message_id = ?`,
		chatID, bucket, messageID).WithContext(ctx).Iter()
	m, ok := scanMessage(chatID, iter.Scan)
	if err := iter.Close(); err != nil {
		return Message{}, 0, fmt.Errorf("get message: %w", err)
	}
	// A row without a type is what an update of a missing message would leave; it does not exist.
	if !ok || m.Type == "" {
		return Message{}, 0, ErrNotFound
	}
	return m, bucket, nil
}

func (r *Messages) Get(ctx context.Context, chatID, messageID int64) (Message, error) {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return Message{}, err
	}
	defer cancel()
	m, _, err := r.get(ctx, sess, chatID, messageID)
	return m, err
}

func (r *Messages) Locate(ctx context.Context, messageID int64) (int64, error) {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return 0, err
	}
	defer cancel()
	var chat int64
	err = sess.Query(`SELECT chat_id FROM message_locations WHERE message_id = ?`, messageID).WithContext(ctx).Scan(&chat)
	if errors.Is(err, gocql.ErrNotFound) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("locate message: %w", err)
	}
	// An interrupted insert can leave a location without its message; only a stored message counts.
	if _, _, err := r.get(ctx, sess, chat, messageID); err != nil {
		return 0, err
	}
	return chat, nil
}

func (r *Messages) Edit(ctx context.Context, chatID, messageID int64, content string, at time.Time) error {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	m, bucket, err := r.get(ctx, sess, chatID, messageID)
	if err != nil {
		return err
	}
	if m.Deleted {
		return ErrNotFound
	}
	// Edits and deletes are lightweight transactions, so they are serialized per message: an edit racing a
	// delete for everyone can never write the text back. Both are rare, so the extra round trips are fine.
	applied, err := sess.Query(`UPDATE messages SET content = ?, edited_at = ? WHERE chat_id = ? AND bucket = ? AND message_id = ? IF deleted = false`,
		content, at, chatID, bucket, messageID).WithContext(ctx).SerialConsistency(gocql.LocalSerial).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}
	if !applied {
		return ErrNotFound
	}
	return nil
}

func (r *Messages) Delete(ctx context.Context, chatID, messageID int64) error {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	_, bucket, err := r.get(ctx, sess, chatID, messageID)
	if err != nil {
		return err
	}
	// A lightweight transaction like Edit, so the two are serialized (mixing them with plain writes would
	// order them by client timestamps instead). Deleting twice is harmless.
	_, err = sess.Query(`UPDATE messages SET deleted = true, content = null, attachment_id = null WHERE chat_id = ? AND bucket = ? AND message_id = ? IF EXISTS`,
		chatID, bucket, messageID).WithContext(ctx).SerialConsistency(gocql.LocalSerial).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	return nil
}

func (r *Messages) Hide(ctx context.Context, userID, chatID, messageID int64, reason string) error {
	if reason != HiddenDeletedForMe && reason != HiddenNotDelivered {
		return errors.New("hide message: invalid reason")
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	// Recorded first, so a chat deletion always finds the user's hidden_messages partition.
	err = sess.Query(`INSERT INTO hidden_message_users (chat_id, user_id) VALUES (?, ?)`, chatID, userID).
		WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("hide message: %w", err)
	}
	// All writes of a hidden row are lightweight transactions, so they are serialized: "deleted for me" is
	// final, and neither a late "not delivered" nor Unhide can replace or remove it.
	for range hideAttempts {
		applied, err := sess.Query(`INSERT INTO hidden_messages (user_id, chat_id, message_id, reason) VALUES (?, ?, ?, ?) IF NOT EXISTS`,
			userID, chatID, messageID, reason).WithContext(ctx).SerialConsistency(gocql.LocalSerial).MapScanCAS(map[string]any{})
		if err != nil {
			return fmt.Errorf("hide message: %w", err)
		}
		if applied || reason == HiddenNotDelivered {
			// A row exists either way; not_delivered never replaces anything.
			return r.markHidden(ctx, sess, chatID, userID)
		}
		// Hidden as not delivered so far: upgrade to deleted. When Unhide removed the row in between, the
		// update does not apply and the insert is tried again.
		applied, err = sess.Query(`UPDATE hidden_messages SET reason = ? WHERE user_id = ? AND chat_id = ? AND message_id = ? IF EXISTS`,
			HiddenDeletedForMe, userID, chatID, messageID).WithContext(ctx).SerialConsistency(gocql.LocalSerial).MapScanCAS(map[string]any{})
		if err != nil {
			return fmt.Errorf("hide message: %w", err)
		}
		if applied {
			return r.markHidden(ctx, sess, chatID, userID)
		}
	}
	return errors.New("hide message: the row kept changing")
}

// markHidden records the user in hidden_message_users. Hide writes it before and after the row: a crash
// after the row leaves the first marker, and a chat deletion that ran in between (removing the first marker)
// leaves the second one for its grace pass to find.
func (r *Messages) markHidden(ctx context.Context, sess *gocql.Session, chatID, userID int64) error {
	err := sess.Query(`INSERT INTO hidden_message_users (chat_id, user_id) VALUES (?, ?)`, chatID, userID).
		WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("hide message: %w", err)
	}
	return nil
}

// hideAttempts bounds the insert/upgrade rounds of Hide against concurrent Unhide calls.
const hideAttempts = 5

func (r *Messages) Unhide(ctx context.Context, userID, chatID, messageID int64) error {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	// Only an undelivered message becomes visible; one the user deleted for themselves stays hidden.
	_, err = sess.Query(`DELETE FROM hidden_messages WHERE user_id = ? AND chat_id = ? AND message_id = ? IF reason = ?`,
		userID, chatID, messageID, HiddenNotDelivered).WithContext(ctx).SerialConsistency(gocql.LocalSerial).MapScanCAS(map[string]any{})
	if err != nil {
		return fmt.Errorf("unhide message: %w", err)
	}
	return nil
}

func (r *Messages) AddReaction(ctx context.Context, chatID, messageID, userID int64, emoji string, at time.Time) error {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	// Written at the time of the request, so a delayed retry cannot undo a removal that came after it.
	err = sess.Query(`INSERT INTO message_reactions (chat_id, message_id, reaction, user_id, created_at) VALUES (?, ?, ?, ?, ?) USING TIMESTAMP ?`,
		chatID, messageID, emoji, userID, at, at.UnixMicro()).WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("add reaction: %w", err)
	}
	return nil
}

func (r *Messages) RemoveReaction(ctx context.Context, chatID, messageID, userID int64, emoji string) error {
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	err = sess.Query(`DELETE FROM message_reactions WHERE chat_id = ? AND message_id = ? AND reaction = ? AND user_id = ?`,
		chatID, messageID, emoji, userID).WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return fmt.Errorf("remove reaction: %w", err)
	}
	return nil
}

func (r *Messages) Reactions(ctx context.Context, chatID int64, messageIDs []int64) (map[int64][]Reaction, error) {
	out := make(map[int64][]Reaction)
	if len(messageIDs) == 0 {
		return out, nil
	}
	ctx, cancel, sess, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	iter := sess.Query(`SELECT message_id, reaction, user_id, created_at FROM message_reactions WHERE chat_id = ? AND message_id IN ?`,
		chatID, messageIDs).WithContext(ctx).Iter()
	var (
		id   int64
		rx   Reaction
		when *time.Time
	)
	for iter.Scan(&id, &rx.Emoji, &rx.UserID, &when) {
		if when != nil {
			rx.CreatedAt = *when
		}
		out[id] = append(out[id], rx)
		rx = Reaction{}
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("reactions: %w", err)
	}
	for _, list := range out {
		slices.SortStableFunc(list, func(a, b Reaction) int { return a.CreatedAt.Compare(b.CreatedAt) })
	}
	return out, nil
}

// DeleteGracePeriod is longer than any request can take, so a second DeleteChat after it finds every write
// that was in flight during the first.
const DeleteGracePeriod = 2 * time.Minute

// deleteParallelism bounds the concurrent statements of one chat deletion.
const deleteParallelism = 32

// DeleteChat works bucket by bucket and, within a bucket, in chunks of deleteChunk messages, each with the
// repository timeout of its own. Finished chunks and buckets are gone, so a deletion that fails or is
// cancelled continues where it stopped when it is called again. Callers run it in the background.
//
// Requests that were in flight when the chat was deleted can still write afterwards (a hide, a reaction).
// Creation writes lose against the tombstones by their timestamps; for the rest, callers run DeleteChat a
// second time once DeleteGracePeriod has passed since the chat was deleted (the worker does).
func (r *Messages) DeleteChat(ctx context.Context, chatID int64) error {
	sess, err := r.s.Session(ctx)
	if err != nil {
		return err
	}
	if err := r.deleteHidden(ctx, sess, chatID); err != nil {
		return err
	}
	lctx, cancel := context.WithTimeout(ctx, r.timeout)
	buckets, err := r.allBuckets(lctx, sess, chatID)
	cancel()
	if err != nil {
		return err
	}
	for _, b := range buckets {
		if err := r.deleteBucket(ctx, sess, chatID, b); err != nil {
			return err
		}
	}
	return nil
}

// each runs fn for every value with bounded parallelism and returns the first error.
func each[T any](ctx context.Context, values []T, fn func(context.Context, T) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, deleteParallelism)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for _, v := range values {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := fn(ctx, v); err != nil {
				select {
				case errs <- err:
				default:
				}
				cancel()
			}
		})
	}
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return ctx.Err()
	}
}

// deleteChunk is how many messages one step of a chat deletion removes.
const deleteChunk = 500

// deleteBucket removes a bucket in chunks. Each chunk ends with a range delete of exactly its messages, so
// it is never read again: a deletion that fails or runs out of time repeats at most one chunk.
func (r *Messages) deleteBucket(ctx context.Context, sess *gocql.Session, chatID int64, b int) error {
	for {
		done, err := r.deleteChunkOf(ctx, sess, chatID, b)
		if err != nil {
			return fmt.Errorf("delete chat: %w", err)
		}
		if done {
			return nil
		}
	}
}

func (r *Messages) deleteChunkOf(ctx context.Context, sess *gocql.Session, chatID int64, b int) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	iter := sess.Query(`SELECT message_id FROM messages WHERE chat_id = ? AND bucket = ? LIMIT ?`, chatID, b, deleteChunk).WithContext(ctx).Iter()
	var ids []int64
	var id int64
	for iter.Scan(&id) {
		ids = append(ids, id)
	}
	if err := iter.Close(); err != nil {
		return false, err
	}
	if len(ids) == 0 {
		// A partition tombstone first: an insert that wrote the bucket marker and is still on its way writes
		// the message with its older creation time, which the tombstone shadows. Then the checkpoint.
		err := sess.Query(`DELETE FROM messages WHERE chat_id = ? AND bucket = ?`, chatID, b).WithContext(ctx).Idempotent(true).Exec()
		if err == nil {
			err = sess.Query(`DELETE FROM chat_buckets WHERE chat_id = ? AND bucket = ?`, chatID, b).WithContext(ctx).Idempotent(true).Exec()
		}
		return err == nil, err
	}
	err := each(ctx, ids, func(ctx context.Context, id int64) error {
		if err := sess.Query(`DELETE FROM message_reactions WHERE chat_id = ? AND message_id = ?`, chatID, id).WithContext(ctx).Idempotent(true).Exec(); err != nil {
			return err
		}
		return sess.Query(`DELETE FROM message_locations WHERE message_id = ?`, id).WithContext(ctx).Idempotent(true).Exec()
	})
	if err != nil {
		return false, err
	}
	// Rows are read newest first: the chunk is the range from its last to its first id.
	err = sess.Query(`DELETE FROM messages WHERE chat_id = ? AND bucket = ? AND message_id >= ? AND message_id <= ?`,
		chatID, b, ids[len(ids)-1], ids[0]).WithContext(ctx).Idempotent(true).Exec()
	return false, err
}

// deleteHidden removes the hidden_messages partitions of every user that hid something in the chat.
func (r *Messages) deleteHidden(ctx context.Context, sess *gocql.Session, chatID int64) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	iter := sess.Query(`SELECT user_id FROM hidden_message_users WHERE chat_id = ?`, chatID).WithContext(ctx).Iter()
	var users []int64
	var uid int64
	for iter.Scan(&uid) {
		users = append(users, uid)
	}
	if err := iter.Close(); err != nil {
		return fmt.Errorf("delete chat: %w", err)
	}
	err := each(ctx, users, func(ctx context.Context, uid int64) error {
		return sess.Query(`DELETE FROM hidden_messages WHERE user_id = ? AND chat_id = ?`, uid, chatID).WithContext(ctx).Idempotent(true).Exec()
	})
	if err == nil {
		err = sess.Query(`DELETE FROM hidden_message_users WHERE chat_id = ?`, chatID).WithContext(ctx).Idempotent(true).Exec()
	}
	if err != nil {
		return fmt.Errorf("delete chat: %w", err)
	}
	return nil
}

func (r *Messages) allBuckets(ctx context.Context, sess *gocql.Session, chatID int64) ([]int, error) {
	iter := sess.Query(`SELECT bucket FROM chat_buckets WHERE chat_id = ?`, chatID).WithContext(ctx).Iter()
	var out []int
	var b int
	for iter.Scan(&b) {
		out = append(out, b)
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("chat buckets: %w", err)
	}
	return out, nil
}
