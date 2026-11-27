package messages

import (
	"context"
	"fmt"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const (
	ImportSenderStateMe          = "me"
	ImportSenderStateConfirmed   = "confirmed"
	ImportSenderStateUnconfirmed = "unconfirmed"
)

type ImportLookup interface {
	ByID(ctx context.Context, ids []int64) (map[int64]postgres.ChatImport, error)
	Senders(ctx context.Context, importIDs []int64) (map[int64][]postgres.ImportSender, error)
	OfChat(ctx context.Context, chatID int64) ([]postgres.ChatImport, error)
}

type ImportResolver struct {
	scylla.MessageRepository
	imports ImportLookup
}

func NewImportResolver(repo scylla.MessageRepository, imports ImportLookup) *ImportResolver {
	return &ImportResolver{MessageRepository: repo, imports: imports}
}

func (r *ImportResolver) Get(ctx context.Context, chatID, messageID int64) (scylla.Message, error) {
	m, err := r.MessageRepository.Get(ctx, chatID, messageID)
	if err != nil || m.Import == nil {
		return m, err
	}
	out, err := r.Resolve(ctx, []scylla.Message{m})
	if err != nil {
		return scylla.Message{}, err
	}
	if len(out) == 0 {
		return scylla.Message{}, scylla.ErrNotFound
	}
	return out[0], nil
}

func (r *ImportResolver) GetMany(ctx context.Context, chatID int64, messageIDs []int64) (map[int64]scylla.Message, error) {
	found, err := r.MessageRepository.GetMany(ctx, chatID, messageIDs)
	if err != nil {
		return nil, err
	}
	list := make([]scylla.Message, 0, len(found))
	for _, m := range found {
		if m.Import != nil {
			list = append(list, m)
		}
	}
	if len(list) == 0 {
		return found, nil
	}
	resolved, err := r.Resolve(ctx, list)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		delete(found, m.ID)
	}
	for _, m := range resolved {
		found[m.ID] = m
	}
	return found, nil
}

func (r *ImportResolver) Page(ctx context.Context, q scylla.PageQuery) (scylla.Page, error) {
	var hidden hiddenImports
	if q.Viewer != 0 {
		imports, err := r.imports.OfChat(ctx, q.ChatID)
		if err != nil {
			return scylla.Page{}, fmt.Errorf("resolve imports: %w", err)
		}
		hidden = runningFor(imports, q.Viewer)
	}
	p, err := r.MessageRepository.Page(ctx, q)
	if err != nil {
		return p, err
	}
	if len(hidden) > 0 {
		if p, err = r.skipHidden(ctx, q, p, hidden); err != nil {
			return scylla.Page{}, err
		}
	}
	p.Messages, err = r.Resolve(ctx, p.Messages)
	return p, err
}

func (r *ImportResolver) skipHidden(ctx context.Context, q scylla.PageQuery, p scylla.Page, hidden hiddenImports) (scylla.Page, error) {
	if q.Around != 0 {
		for _, m := range p.Messages {
			if m.ID == q.Around && hidden.has(m) {
				return scylla.Page{}, scylla.ErrNotFound
			}
		}
		p.Messages = hidden.drop(p.Messages)
		return p, nil
	}
	want := len(p.Messages)
	kept := hidden.drop(p.Messages)
	raw := p.Messages
	newer := q.After != 0
	for range len(hidden) {
		if len(kept) >= want || len(raw) == 0 {
			break
		}
		edge := raw[0]
		if newer {
			edge = raw[len(raw)-1]
		}
		span, ok := hidden.of(edge)
		if !ok || (newer && !p.HasNewer) || (!newer && !p.HasOlder) {
			break
		}
		next := q
		next.Limit = want - len(kept)
		if newer {
			next.After = span.last
		} else {
			next.Before = span.first
		}
		more, err := r.MessageRepository.Page(ctx, next)
		if err != nil {
			return scylla.Page{}, err
		}
		raw = more.Messages
		if newer {
			kept = append(kept, hidden.drop(raw)...)
			p.HasNewer = more.HasNewer
		} else {
			kept = append(hidden.drop(raw), kept...)
			p.HasOlder = more.HasOlder
		}
	}
	p.Messages = kept
	return p, nil
}

func (r *ImportResolver) Resolve(ctx context.Context, msgs []scylla.Message) ([]scylla.Message, error) {
	var importIDs []int64
	seen := map[int64]bool{}
	for _, m := range msgs {
		if m.Import != nil && !seen[m.Import.ID] {
			seen[m.Import.ID] = true
			importIDs = append(importIDs, m.Import.ID)
		}
	}
	if len(importIDs) == 0 {
		return msgs, nil
	}
	imports, err := r.imports.ByID(ctx, importIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve imports: %w", err)
	}
	senders, err := r.imports.Senders(ctx, importIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve imports: %w", err)
	}
	byKey := make(map[int64]map[string]postgres.ImportSender, len(senders))
	for id, list := range senders {
		byKey[id] = make(map[string]postgres.ImportSender, len(list))
		for _, snd := range list {
			byKey[id][snd.Key] = snd
		}
	}
	out := make([]scylla.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Import == nil {
			out = append(out, m)
			continue
		}
		imp, ok := imports[m.Import.ID]
		if !ok || imp.State == postgres.ImportRemoving {
			continue
		}
		info := *m.Import
		info.Source, info.ImportedBy = imp.Source, imp.ImportedBy
		if m.Type != scylla.TypeSystem {
			snd := byKey[imp.ID][info.SenderKey]
			info.SenderName = snd.Name
			m.SenderID, info.SenderState = resolvedSender(imp, snd)
		}
		m.Import = &info
		out = append(out, m)
	}
	return out, nil
}

func resolvedSender(imp postgres.ChatImport, snd postgres.ImportSender) (*int64, string) {
	switch {
	case snd.State == postgres.ImportSenderMe && imp.ImportedBy != nil:
		id := *imp.ImportedBy
		return &id, ImportSenderStateMe
	case snd.State == postgres.ImportSenderConfirmed && snd.AssignedUserID != nil:
		id := *snd.AssignedUserID
		return &id, ImportSenderStateConfirmed
	}
	return nil, ImportSenderStateUnconfirmed
}

func deletesOwn(m scylla.Message, userID int64) bool {
	if m.SenderID != nil && *m.SenderID == userID {
		return true
	}
	return m.Import != nil && m.Import.ImportedBy != nil && *m.Import.ImportedBy == userID
}

type hiddenImport struct {
	id, first, last int64
}

type hiddenImports []hiddenImport

func runningFor(imports []postgres.ChatImport, viewer int64) hiddenImports {
	var out hiddenImports
	for _, imp := range imports {
		if imp.State == postgres.ImportRunning && (imp.ImportedBy == nil || *imp.ImportedBy != viewer) {
			out = append(out, hiddenImport{id: imp.ID, first: imp.FirstMessageID, last: imp.LastMessageID})
		}
	}
	return out
}

func (h hiddenImports) of(m scylla.Message) (hiddenImport, bool) {
	if m.Import == nil {
		return hiddenImport{}, false
	}
	for _, x := range h {
		if x.id == m.Import.ID {
			return x, true
		}
	}
	return hiddenImport{}, false
}

func (h hiddenImports) has(m scylla.Message) bool {
	_, ok := h.of(m)
	return ok
}

func (h hiddenImports) drop(msgs []scylla.Message) []scylla.Message {
	out := make([]scylla.Message, 0, len(msgs))
	for _, m := range msgs {
		if !h.has(m) {
			out = append(out, m)
		}
	}
	return out
}
