package messages

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/ids"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	MaxImportMessages = 100000
	MaxImportFiles    = 5000
	MaxImportBatch    = 500
	MaxImportSenders  = 200
	maxImportName     = 128
	importWindowTries = 5
	importUpperMargin = time.Minute
)

const (
	ImportSourceTelegram = "telegram"
	ImportSourceWhatsApp = "whatsapp"

	ImportTypeNote = "note"

	SystemHistoryImported    = "history_imported"
	SystemHistoryImportEnded = "history_import_ended"
	SystemImportedNote       = "imported_note"
)

var (
	ErrConflict      = errors.New("conflict")
	ErrImportOverlap = fmt.Errorf("%w: the chat already has imported history from that period", ErrConflict)
	ErrImportNoRoom  = fmt.Errorf("%w: there is no room for the imported history in this chat", ErrConflict)
	ErrImportRunning = fmt.Errorf("%w: an import of this chat is running", ErrConflict)
	ErrImportIndex   = fmt.Errorf("%w: the batch does not continue the import", ErrConflict)
)

var importKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type ImportStore interface {
	InsertImported(ctx context.Context, msgs []scylla.Message, at time.Time) error
}

type ImportSenderInput struct {
	Key          string
	Name         string
	MessageCount int
	Me           bool
	AssignTo     *int64
}

type StartImport struct {
	ChatID       int64
	UserID       int64
	Source       string
	MessageCount int
	FileCount    int
	FirstAt      time.Time
	LastAt       time.Time
	Senders      []ImportSenderInput
}

type ImportedMessage struct {
	Index         int
	SenderKey     string
	OriginalAt    time.Time
	Type          string
	Content       *string
	AttachmentIDs []uuid.UUID
	ReplyToIndex  *int
}

type ChatImport struct {
	postgres.ChatImport
	Senders []postgres.ImportSender
}

type ImportChange struct {
	ChatID   int64
	ImportID int64
	State    string
}

type ImportSenderChange struct {
	ChatID   int64
	ImportID int64
	Sender   postgres.ImportSender
}

func (s *Service) imports() (postgres.ImportRepository, ImportStore, error) {
	if s.d.Imports == nil {
		return nil, nil, fmt.Errorf("%w: imports are not configured", ErrUnavailable)
	}
	return s.d.Store.Imports(), s.d.Imports, nil
}

func (r StartImport) validate() error {
	if r.Source != ImportSourceTelegram && r.Source != ImportSourceWhatsApp {
		return invalid("source must be telegram or whatsapp")
	}
	if r.MessageCount < 1 || r.MessageCount > MaxImportMessages {
		return invalid("message_count must be 1 to %d", MaxImportMessages)
	}
	if r.FileCount < 0 || r.FileCount > MaxImportFiles {
		return invalid("file_count must be 0 to %d", MaxImportFiles)
	}
	if r.FirstAt.IsZero() || r.LastAt.Before(r.FirstAt) {
		return invalid("first_at must not be after last_at")
	}
	if len(r.Senders) == 0 || len(r.Senders) > MaxImportSenders {
		return invalid("senders must contain 1 to %d items", MaxImportSenders)
	}
	keys := make(map[string]bool, len(r.Senders))
	me := 0
	for _, snd := range r.Senders {
		if !importKeyPattern.MatchString(snd.Key) {
			return invalid("a sender key must be 1 to 64 letters, digits, - or _")
		}
		if keys[snd.Key] {
			return invalid("sender keys must be unique")
		}
		keys[snd.Key] = true
		if name := strings.TrimSpace(snd.Name); name == "" || utf8.RuneCountInString(name) > maxImportName || !utf8.ValidString(name) {
			return invalid("a sender name must be 1 to %d characters", maxImportName)
		}
		if snd.MessageCount < 0 || snd.MessageCount > r.MessageCount {
			return invalid("a sender's message_count must be 0 to message_count")
		}
		if snd.Me {
			me++
			if snd.AssignTo != nil {
				return invalid("a sender is either me or assigned to a member")
			}
		}
	}
	if me > 1 {
		return invalid("at most one sender can be me")
	}
	return nil
}

func (s *Service) requireMayImport(ctx context.Context, userID int64, pc pinContext) error {
	if _, ok := pc.roles[userID]; !ok {
		return ErrNotFound
	}
	if pc.chat.Type != postgres.ChatDirect {
		if roleRank(pc.roles[userID]) < roleRank(postgres.RoleAdmin) {
			return ErrForbidden
		}
		return nil
	}
	if len(pc.members) < 2 || pc.request {
		return ErrForbidden
	}
	if err := s.checkNotBlocked(ctx, userID, pc.members); err != nil {
		if errors.Is(err, ErrBlocked) {
			return ErrForbidden
		}
		return err
	}
	return nil
}

func (s *Service) StartImport(ctx context.Context, r StartImport) (ChatImport, error) {
	repo, _, err := s.imports()
	if err != nil {
		return ChatImport{}, err
	}
	if err := r.validate(); err != nil {
		return ChatImport{}, err
	}
	if err := s.requireMember(ctx, r.ChatID, r.UserID); err != nil {
		return ChatImport{}, err
	}
	pc, err := s.pinContext(ctx, r.ChatID)
	if err != nil {
		return ChatImport{}, err
	}
	if err := s.requireMayImport(ctx, r.UserID, pc); err != nil {
		return ChatImport{}, err
	}
	senders, err := importSenders(r, pc)
	if err != nil {
		return ChatImport{}, err
	}
	lower, upper, err := s.importGap(ctx, repo, r)
	if err != nil {
		return ChatImport{}, err
	}
	positions := r.MessageCount + 2
	var created postgres.ChatImport
	var layout importLayout
	for try := 0; ; try++ {
		layout, err = planImport(lower, upper, positions, randomInt)
		if errors.Is(err, errNoRoom) {
			return ChatImport{}, ErrImportNoRoom
		}
		created, err = repo.Create(ctx, postgres.NewChatImport{
			ChatID: r.ChatID, ImportedBy: r.UserID, Source: r.Source, MessageCount: r.MessageCount, FileCount: r.FileCount,
			FirstAt: r.FirstAt.UTC(), LastAt: r.LastAt.UTC(), LowerBound: lower,
			FirstMessageID: layout.id(0), LastMessageID: layout.id(positions - 1),
			Senders: senders, Windows: layout.windows,
		})
		if errors.Is(err, postgres.ErrWindowTaken) && try < importWindowTries {
			continue
		}
		break
	}
	switch {
	case errors.Is(err, postgres.ErrImportRunning):
		return ChatImport{}, ErrImportRunning
	case err != nil:
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.insertImportLine(ctx, created, layout.windows, 0, SystemHistoryImported); err != nil {
		return ChatImport{}, err
	}
	out := ChatImport{ChatImport: created, Senders: senders}
	for i := range out.Senders {
		out.Senders[i].ImportID = created.ID
	}
	s.importChanged(ctx, created)
	return out, nil
}

func importSenders(r StartImport, pc pinContext) ([]postgres.ImportSender, error) {
	out := make([]postgres.ImportSender, len(r.Senders))
	for i, snd := range r.Senders {
		p := postgres.ImportSender{Key: snd.Key, Name: strings.TrimSpace(snd.Name), MessageCount: snd.MessageCount, State: postgres.ImportSenderUnassigned}
		switch {
		case snd.Me:
			uid := r.UserID
			p.AssignedUserID, p.State = &uid, postgres.ImportSenderMe
		case snd.AssignTo != nil:
			if *snd.AssignTo == r.UserID {
				return nil, invalid("assign your own messages with me")
			}
			if _, ok := pc.roles[*snd.AssignTo]; !ok {
				return nil, invalid("a sender can only be assigned to a member of the chat")
			}
			uid := *snd.AssignTo
			p.AssignedUserID, p.State = &uid, postgres.ImportSenderPending
		}
		out[i] = p
	}
	return out, nil
}

func overlaps(r StartImport, imp postgres.ChatImport) bool {
	return !r.LastAt.Before(imp.FirstAt) && !r.FirstAt.After(imp.LastAt)
}

func (s *Service) importGap(ctx context.Context, repo postgres.ImportRepository, r StartImport) (int64, int64, error) {
	lower, err := repo.LowerBound(ctx, r.ChatID)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	lower = max(lower, ids.MinSnowflake)
	existing, err := repo.OfChat(ctx, r.ChatID)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	floor := lower
	for _, imp := range existing {
		if imp.State == postgres.ImportRemoving || imp.LastMessageID <= floor {
			continue
		}
		if overlaps(r, imp) {
			return 0, 0, ErrImportOverlap
		}
		if imp.LastAt.Before(r.FirstAt) {
			lower = max(lower, imp.LastMessageID)
		}
	}
	upper := ids.MinAt(s.now().Add(-importUpperMargin))
	p, err := s.d.Messages.Page(ctx, scylla.PageQuery{ChatID: r.ChatID, After: lower, Limit: 1})
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if len(p.Messages) > 0 {
		upper = min(upper, p.Messages[0].ID)
	}
	for _, imp := range existing {
		if imp.LastMessageID <= lower || imp.FirstMessageID >= upper {
			continue
		}
		if imp.FirstMessageID <= lower {
			lower = imp.LastMessageID
		} else {
			upper = imp.FirstMessageID
		}
	}
	if upper <= lower {
		return 0, 0, ErrImportNoRoom
	}
	return lower, upper, nil
}

func (s *Service) insertImportLine(ctx context.Context, imp postgres.ChatImport, windows []postgres.ImportWindow, position int, kind string) error {
	_, store, err := s.imports()
	if err != nil {
		return err
	}
	e := SystemEvent{Kind: kind, ActorID: imp.ImportedBy, Import: &ImportSummary{
		ImportID: imp.ID, Source: imp.Source, MessageCount: imp.MessageCount, FirstAt: imp.FirstAt.UTC(), LastAt: imp.LastAt.UTC(),
	}}
	if imp.ImportedBy != nil {
		if name, err := s.senderName(ctx, imp.ImportedBy); err == nil {
			e.ActorName = name
		}
	}
	m, err := systemMessage(imp.ChatID, positionID(windows, position), e)
	if err != nil {
		return err
	}
	at := imp.FirstAt
	if kind == SystemHistoryImportEnded {
		at = imp.LastAt
	}
	m.Import = &scylla.Import{ID: imp.ID, OriginalAt: at}
	if err := store.InsertImported(ctx, []scylla.Message{m}, imp.CreatedAt); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

func (s *Service) ownImport(ctx context.Context, repo postgres.ImportRepository, userID, importID int64) (postgres.ChatImport, error) {
	imp, err := repo.Get(ctx, importID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.ChatImport{}, ErrNotFound
	}
	if err != nil {
		return postgres.ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.requireMember(ctx, imp.ChatID, userID); err != nil {
		return postgres.ChatImport{}, err
	}
	if imp.ImportedBy == nil || *imp.ImportedBy != userID {
		return postgres.ChatImport{}, ErrForbidden
	}
	return imp, nil
}

func (s *Service) AddImportBatch(ctx context.Context, userID, importID int64, from int, batch []ImportedMessage) (int, error) {
	repo, store, err := s.imports()
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 || len(batch) > MaxImportBatch {
		return 0, invalid("messages must contain 1 to %d items", MaxImportBatch)
	}
	imp, err := s.ownImport(ctx, repo, userID, importID)
	if err != nil {
		return 0, err
	}
	if imp.State != postgres.ImportRunning {
		return 0, invalid("the import is not running")
	}
	if from < imp.NextIndex && from+len(batch) <= imp.NextIndex {
		return imp.NextIndex, nil
	}
	if from != imp.NextIndex {
		return 0, fmt.Errorf("%w: from_index must be %d", ErrImportIndex, imp.NextIndex)
	}
	if from+len(batch) > imp.MessageCount {
		return 0, invalid("the import has only %d messages", imp.MessageCount)
	}
	senders, err := repo.Senders(ctx, []int64{importID})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	keys := make(map[string]bool, len(senders[importID]))
	for _, snd := range senders[importID] {
		keys[snd.Key] = true
	}
	windows, err := repo.Windows(ctx, importID)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	msgs := make([]scylla.Message, len(batch))
	for i, in := range batch {
		m, err := s.importedMessage(ctx, imp, windows, keys, from+i, in)
		if err != nil {
			return 0, err
		}
		msgs[i] = m
	}
	for _, m := range msgs {
		if m.Type == scylla.TypeSystem {
			continue
		}
		if err := s.linkAttachments(ctx, m, nil); err != nil {
			return 0, err
		}
	}
	if err := store.InsertImported(ctx, msgs, imp.CreatedAt); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	to := from + len(batch)
	if _, err := repo.Advance(ctx, importID, from, to); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return to, nil
}

func (s *Service) importedMessage(ctx context.Context, imp postgres.ChatImport, windows []postgres.ImportWindow, keys map[string]bool, index int, in ImportedMessage) (scylla.Message, error) {
	if in.Index != index {
		return scylla.Message{}, invalid("message %d: index must be %d", index, index)
	}
	if in.OriginalAt.Before(imp.FirstAt) || in.OriginalAt.After(imp.LastAt) {
		return scylla.Message{}, invalid("message %d: original_at must be between first_at and last_at", index)
	}
	if in.Content != nil && !validText(*in.Content, MaxContent) {
		return scylla.Message{}, invalid("message %d: content must be at most %d characters", index, MaxContent)
	}
	if in.ReplyToIndex != nil && (*in.ReplyToIndex < 0 || *in.ReplyToIndex >= index) {
		return scylla.Message{}, invalid("message %d: reply_to_index must point to an earlier message", index)
	}
	id := positionID(windows, index+1)
	m := scylla.Message{ChatID: imp.ChatID, ID: id, CreatedAt: ids.Time(id), Import: &scylla.Import{ID: imp.ID, SenderKey: in.SenderKey, OriginalAt: in.OriginalAt.UTC()}}
	if in.Type == ImportTypeNote {
		if in.Content == nil || strings.TrimSpace(*in.Content) == "" || len(in.AttachmentIDs) > 0 {
			return scylla.Message{}, invalid("message %d: a note needs content and has no attachment", index)
		}
		note, err := systemMessage(imp.ChatID, id, SystemEvent{Kind: SystemImportedNote, Text: *in.Content})
		if err != nil {
			return scylla.Message{}, err
		}
		note.Import = &scylla.Import{ID: imp.ID, OriginalAt: in.OriginalAt.UTC()}
		return note, nil
	}
	if !keys[in.SenderKey] {
		return scylla.Message{}, invalid("message %d: sender_key is not a sender of the import", index)
	}
	if in.ReplyToIndex != nil {
		reply := positionID(windows, *in.ReplyToIndex+1)
		m.ReplyTo = &reply
	}
	req := SendRequest{ChatID: imp.ChatID, SenderID: *imp.ImportedBy, ClientTempID: "import", Type: in.Type, Content: in.Content}
	switch len(in.AttachmentIDs) {
	case 0:
	case 1:
		req.AttachmentID = &in.AttachmentIDs[0]
	default:
		req.AlbumIDs = in.AttachmentIDs
	}
	if in.Type == scylla.TypeSticker {
		return scylla.Message{}, invalid("message %d: import stickers as file messages", index)
	}
	if err := s.validateSend(req); err != nil {
		return scylla.Message{}, fmt.Errorf("message %d: %w", index, err)
	}
	attachments, _, err := s.checkAttachments(ctx, req)
	if err != nil {
		return scylla.Message{}, fmt.Errorf("message %d: %w", index, err)
	}
	sender := *imp.ImportedBy
	m.SenderID, m.Type, m.Content = &sender, in.Type, in.Content
	if len(attachments) > 0 {
		m.AttachmentID = &attachments[0]
	}
	if len(attachments) > 1 {
		m.AlbumIDs = attachments
	}
	return m, nil
}

func (s *Service) FinishImport(ctx context.Context, userID, importID int64) (ChatImport, error) {
	repo, _, err := s.imports()
	if err != nil {
		return ChatImport{}, err
	}
	imp, err := s.ownImport(ctx, repo, userID, importID)
	if err != nil {
		return ChatImport{}, err
	}
	if imp.State == postgres.ImportDone {
		senders, err := repo.Senders(ctx, []int64{importID})
		if err != nil {
			return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return ChatImport{ChatImport: imp, Senders: senders[importID]}, nil
	}
	if imp.State != postgres.ImportRunning || imp.NextIndex != imp.MessageCount {
		return ChatImport{}, invalid("the import is not complete")
	}
	windows, err := repo.Windows(ctx, importID)
	if err != nil {
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.insertImportLine(ctx, imp, windows, 0, SystemHistoryImported); err != nil {
		return ChatImport{}, err
	}
	if err := s.insertImportLine(ctx, imp, windows, imp.MessageCount+1, SystemHistoryImportEnded); err != nil {
		return ChatImport{}, err
	}
	done, err := repo.Finish(ctx, importID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ChatImport{}, invalid("the import is not complete")
	}
	if err != nil {
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := repo.AdvanceReadPointers(ctx, done.ChatID, done.LowerBound, done.LastMessageID); err != nil {
		s.d.Log.WarnContext(ctx, "advance read pointers after import", "error", err, "chat_id", done.ChatID)
	}
	s.recordChange(ctx, postgres.SyncChange{ChatID: done.ChatID, Kind: postgres.SyncHistoryImported})
	s.emit(ctx, events.Event{Type: events.HistoryImported, ChatID: done.ChatID, ImportID: done.ID, MessageID: done.FirstMessageID, UpTo: done.LastMessageID, ActorID: userID})
	s.importChanged(ctx, done)
	senders, err := repo.Senders(ctx, []int64{importID})
	if err != nil {
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, snd := range senders[importID] {
		if snd.State == postgres.ImportSenderPending && snd.AssignedUserID != nil {
			s.d.Notifier.ImportSenderChanged(ctx, ImportSenderChange{ChatID: done.ChatID, ImportID: done.ID, Sender: snd}, []int64{*snd.AssignedUserID})
		}
	}
	return ChatImport{ChatImport: done, Senders: senders[importID]}, nil
}

func (s *Service) RemoveImport(ctx context.Context, userID, importID int64) error {
	repo, _, err := s.imports()
	if err != nil {
		return err
	}
	imp, err := repo.Get(ctx, importID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.requireMember(ctx, imp.ChatID, userID); err != nil {
		return err
	}
	if imp.ImportedBy == nil || *imp.ImportedBy != userID {
		pc, err := s.pinContext(ctx, imp.ChatID)
		if err != nil {
			return err
		}
		if pc.chat.Type == postgres.ChatDirect || pc.roles[userID] != postgres.RoleOwner {
			return ErrForbidden
		}
	}
	if imp.State == postgres.ImportRemoving {
		return nil
	}
	removing, err := repo.MarkRemoving(ctx, importID)
	if errors.Is(err, postgres.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	s.recordChange(ctx, postgres.SyncChange{ChatID: removing.ChatID, Kind: postgres.SyncHistoryImported})
	s.emit(ctx, events.Event{Type: events.ImportRemoved, ChatID: removing.ChatID, ImportID: removing.ID, MessageID: removing.FirstMessageID, UpTo: removing.LastMessageID, ActorID: userID})
	s.importChanged(ctx, removing)
	return nil
}

func (s *Service) Imports(ctx context.Context, userID, chatID int64) ([]ChatImport, error) {
	repo, _, err := s.imports()
	if err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, chatID, userID); err != nil {
		return nil, err
	}
	list, err := repo.OfChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	idList := make([]int64, 0, len(list))
	for _, imp := range list {
		if imp.State != postgres.ImportRemoving {
			idList = append(idList, imp.ID)
		}
	}
	senders, err := repo.Senders(ctx, idList)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	out := make([]ChatImport, 0, len(idList))
	for _, imp := range list {
		if imp.State != postgres.ImportRemoving {
			out = append(out, ChatImport{ChatImport: imp, Senders: visibleSenders(imp, senders[imp.ID], userID)})
		}
	}
	return out, nil
}

func (s *Service) Import(ctx context.Context, userID, importID int64) (ChatImport, error) {
	repo, _, err := s.imports()
	if err != nil {
		return ChatImport{}, err
	}
	imp, err := repo.Get(ctx, importID)
	if errors.Is(err, postgres.ErrNotFound) {
		return ChatImport{}, ErrNotFound
	}
	if err != nil {
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.requireMember(ctx, imp.ChatID, userID); err != nil {
		return ChatImport{}, err
	}
	if imp.State == postgres.ImportRemoving {
		return ChatImport{}, ErrNotFound
	}
	senders, err := repo.Senders(ctx, []int64{importID})
	if err != nil {
		return ChatImport{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return ChatImport{ChatImport: imp, Senders: visibleSenders(imp, senders[importID], userID)}, nil
}

func visibleSenders(imp postgres.ChatImport, list []postgres.ImportSender, viewer int64) []postgres.ImportSender {
	importer := imp.ImportedBy != nil && *imp.ImportedBy == viewer
	out := make([]postgres.ImportSender, len(list))
	for i, snd := range list {
		assignee := snd.AssignedUserID != nil && *snd.AssignedUserID == viewer
		if (snd.State == postgres.ImportSenderPending || snd.State == postgres.ImportSenderDeclined) && !importer && !assignee {
			snd.State, snd.AssignedUserID = postgres.ImportSenderUnassigned, nil
		}
		if !importer {
			snd.DeclinedUserIDs = nil
		}
		out[i] = snd
	}
	return out
}

func (s *Service) importChanged(ctx context.Context, imp postgres.ChatImport) {
	members, err := s.Members(ctx, imp.ChatID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "import members", "error", err, "chat_id", imp.ChatID)
		return
	}
	s.d.Notifier.ImportChanged(ctx, ImportChange{ChatID: imp.ChatID, ImportID: imp.ID, State: imp.State}, members)
}

func systemMessage(chatID, id int64, e SystemEvent) (scylla.Message, error) {
	body, err := jsonSystemEvent(e)
	if err != nil {
		return scylla.Message{}, err
	}
	return scylla.Message{ChatID: chatID, ID: id, Type: scylla.TypeSystem, Content: &body, CreatedAt: ids.Time(id)}, nil
}
