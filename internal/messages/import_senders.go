package messages

import (
	"context"
	"errors"
	"fmt"

	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	ImportDecisionConfirm  = "confirm"
	ImportDecisionDecline  = "decline"
	ImportDecisionWithdraw = "withdraw"
)

func (s *Service) visibleImport(ctx context.Context, repo postgres.ImportRepository, userID, importID int64) (postgres.ChatImport, error) {
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
	if imp.State == postgres.ImportRemoving {
		return postgres.ChatImport{}, ErrNotFound
	}
	return imp, nil
}

func (s *Service) AssignImportSender(ctx context.Context, userID, importID int64, key string, assignTo *int64) (postgres.ImportSender, error) {
	repo, _, err := s.imports()
	if err != nil {
		return postgres.ImportSender{}, err
	}
	imp, err := s.visibleImport(ctx, repo, userID, importID)
	if err != nil {
		return postgres.ImportSender{}, err
	}
	if imp.ImportedBy == nil || *imp.ImportedBy != userID {
		return postgres.ImportSender{}, ErrForbidden
	}
	if assignTo != nil {
		if *assignTo == userID {
			return postgres.ImportSender{}, invalid("assign your own messages when you start the import")
		}
		ok, err := s.IsMember(ctx, imp.ChatID, *assignTo)
		if err != nil {
			return postgres.ImportSender{}, err
		}
		if !ok {
			return postgres.ImportSender{}, invalid("a sender can only be assigned to a member of the chat")
		}
	}
	before := s.importSender(ctx, repo, importID, key)
	snd, err := repo.Assign(ctx, importID, key, assignTo)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.ImportSender{}, fmt.Errorf("%w: this sender cannot be assigned to that member", ErrConflict)
	}
	if err != nil {
		return postgres.ImportSender{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	recipients := []int64{userID}
	if before != nil && before.AssignedUserID != nil {
		recipients = append(recipients, *before.AssignedUserID)
	}
	if assignTo != nil && imp.State == postgres.ImportDone {
		recipients = append(recipients, *assignTo)
	}
	s.d.Notifier.ImportSenderChanged(ctx, ImportSenderChange{ChatID: imp.ChatID, ImportID: importID, Sender: snd}, uniqueIDs(recipients))
	return snd, nil
}

func (s *Service) importSender(ctx context.Context, repo postgres.ImportRepository, importID int64, key string) *postgres.ImportSender {
	all, err := repo.Senders(ctx, []int64{importID})
	if err != nil {
		return nil
	}
	for _, snd := range all[importID] {
		if snd.Key == key {
			return &snd
		}
	}
	return nil
}

func (s *Service) DecideImportSender(ctx context.Context, userID, importID int64, key, decision string) (postgres.ImportSender, error) {
	repo, _, err := s.imports()
	if err != nil {
		return postgres.ImportSender{}, err
	}
	imp, err := s.visibleImport(ctx, repo, userID, importID)
	if err != nil {
		return postgres.ImportSender{}, err
	}
	if imp.State != postgres.ImportDone {
		return postgres.ImportSender{}, invalid("the import is not finished")
	}
	var snd postgres.ImportSender
	switch decision {
	case ImportDecisionConfirm:
		snd, err = repo.Confirm(ctx, importID, key, userID)
	case ImportDecisionDecline:
		snd, err = repo.Decline(ctx, importID, key, userID)
	case ImportDecisionWithdraw:
		snd, err = repo.Withdraw(ctx, importID, key, userID)
	default:
		return postgres.ImportSender{}, invalid("decision must be confirm, decline or withdraw")
	}
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.ImportSender{}, ErrNotFound
	}
	if err != nil {
		return postgres.ImportSender{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	recipients := []int64{userID}
	if imp.ImportedBy != nil {
		recipients = append(recipients, *imp.ImportedBy)
	}
	if decision != ImportDecisionDecline {
		members, err := s.Members(ctx, imp.ChatID)
		if err == nil {
			recipients = members
		}
		s.recordChange(ctx, postgres.SyncChange{ChatID: imp.ChatID, Kind: postgres.SyncHistoryImported})
		s.emit(ctx, events.Event{Type: events.HistoryImported, ChatID: imp.ChatID, ImportID: imp.ID, MessageID: imp.FirstMessageID, UpTo: imp.LastMessageID, ActorID: userID})
	}
	s.d.Notifier.ImportSenderChanged(ctx, ImportSenderChange{ChatID: imp.ChatID, ImportID: importID, Sender: snd}, uniqueIDs(recipients))
	return snd, nil
}

func uniqueIDs(list []int64) []int64 {
	seen := make(map[int64]bool, len(list))
	out := make([]int64, 0, len(list))
	for _, id := range list {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
