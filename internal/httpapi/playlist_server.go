package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	maxPlaylistName  = 60
	maxTrackText     = 200
	maxTrackDuration = 86400
	trackCheckers    = 8
)

func cleanPlaylistName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	n := utf8.RuneCountInString(trimmed)
	if n == 0 || n > maxPlaylistName || strings.ContainsFunc(trimmed, isControl) {
		return "", false
	}
	return trimmed, true
}

func cleanTrackText(s string, required bool) (string, bool) {
	trimmed := strings.TrimSpace(s)
	n := utf8.RuneCountInString(trimmed)
	if (required && n == 0) || n > maxTrackText || strings.ContainsFunc(trimmed, isControl) {
		return "", false
	}
	return trimmed, true
}

func trackRef(s *Id) (*int64, bool) {
	if s == nil {
		return nil, true
	}
	id, err := strconv.ParseInt(*s, 10, 64)
	if err != nil || id <= 0 {
		return nil, false
	}
	return &id, true
}

func playlistTracks(in []PlaylistTrackInput) ([]postgres.PlaylistTrack, *ProblemError) {
	if len(in) > postgres.MaxPlaylistTracks {
		return nil, &ProblemError{Field: "tracks", Message: "at most 1000 tracks"}
	}
	seen := make(map[uuid.UUID]bool, len(in))
	out := make([]postgres.PlaylistTrack, 0, len(in))
	for _, t := range in {
		if seen[t.AttachmentId] {
			return nil, &ProblemError{Field: "tracks", Message: "each attachment only once"}
		}
		seen[t.AttachmentId] = true
		title, okTitle := cleanTrackText(t.Title, true)
		chatID, okChat := trackRef(t.ChatId)
		messageID, okMessage := trackRef(t.MessageId)
		if !okTitle || !okChat || !okMessage {
			return nil, &ProblemError{Field: "tracks", Message: "a track needs a title of 1 to 200 characters and valid ids"}
		}
		track := postgres.PlaylistTrack{AttachmentID: t.AttachmentId, ChatID: chatID, MessageID: messageID, Title: title}
		if t.Artist != nil {
			artist, ok := cleanTrackText(*t.Artist, false)
			if !ok {
				return nil, &ProblemError{Field: "tracks", Message: "an artist is at most 200 characters"}
			}
			if artist != "" {
				track.Artist = &artist
			}
		}
		if t.DurationSeconds != nil {
			d := float64(*t.DurationSeconds)
			if d < 0 || d > maxTrackDuration {
				return nil, &ProblemError{Field: "tracks", Message: "duration_seconds is 0 to 86400"}
			}
			track.DurationSeconds = &d
		}
		out = append(out, track)
	}
	return out, nil
}

func presentPlaylistSummary(p postgres.PlaylistSummary) PlaylistSummary {
	return PlaylistSummary{
		Id: FormatID(p.ID), Name: p.Name, TrackCount: p.TrackCount, Version: p.Version,
		CreatedAt: utc(p.CreatedAt), UpdatedAt: utc(p.UpdatedAt),
	}
}

func (p *Personal) availableTracks(ctx context.Context, uid int64, tracks []postgres.PlaylistTrack) (map[uuid.UUID]postgres.Attachment, error) {
	found := make(map[uuid.UUID]postgres.Attachment, len(tracks))
	if p.Media == nil || len(tracks) == 0 {
		return found, nil
	}
	var mu sync.Mutex
	var firstErr error
	jobs := make(chan uuid.UUID)
	var wg sync.WaitGroup
	for range min(trackCheckers, len(tracks)) {
		wg.Go(func() {
			for id := range jobs {
				att, err := p.Media.Authorize(ctx, uid, id)
				mu.Lock()
				switch {
				case err == nil:
					found[id] = att
				case ctx.Err() != nil && firstErr == nil:
					firstErr = ctx.Err()
				case !isNoAccess(err) && firstErr == nil:
					firstErr = err
				}
				mu.Unlock()
			}
		})
	}
	for _, t := range tracks {
		jobs <- t.AttachmentID
	}
	close(jobs)
	wg.Wait()
	return found, firstErr
}

func isNoAccess(err error) bool {
	return errors.Is(err, media.ErrNoAccess) || errors.Is(err, postgres.ErrNotFound)
}

func (p *Personal) presentPlaylist(ctx context.Context, uid int64, pl postgres.Playlist) (Playlist, error) {
	found, err := p.availableTracks(ctx, uid, pl.Tracks)
	if err != nil {
		return Playlist{}, err
	}
	s := presentPlaylistSummary(pl.PlaylistSummary)
	out := Playlist{Id: s.Id, Name: s.Name, TrackCount: s.TrackCount, Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	out.Tracks = make([]PlaylistTrack, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		track := PlaylistTrack{AttachmentId: t.AttachmentID, ChatId: formatIDPtr(t.ChatID), MessageId: formatIDPtr(t.MessageID), Title: t.Title, Artist: t.Artist}
		if t.DurationSeconds != nil {
			d := float32(*t.DurationSeconds)
			track.DurationSeconds = &d
		}
		if att, ok := found[t.AttachmentID]; ok {
			a := PresentAttachment(att, "", p.BasePath)
			track.Attachment, track.Available = &a, true
		}
		out.Tracks = append(out.Tracks, track)
	}
	return out, nil
}

func (p *Personal) playlistChanged(ctx context.Context, uid int64, action string, id int64, summary *PlaylistSummary) {
	p.notify(ctx, uid, EventPlaylistsUpdated, map[string]any{"action": action, "playlist_id": FormatID(id), "playlist": summary})
}

func (p *Personal) writePlaylist(w http.ResponseWriter, r *http.Request, uid int64, status int, pl postgres.Playlist, action string) {
	out, err := p.presentPlaylist(r.Context(), uid, pl)
	if err != nil {
		p.internal(w, r, "present playlist", err)
		return
	}
	summary := presentPlaylistSummary(pl.PlaylistSummary)
	p.playlistChanged(r.Context(), uid, action, pl.ID, &summary)
	noStore(w)
	writeJSONStatus(w, status, out)
}

func (p *Personal) playlistError(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, postgres.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, postgres.ErrLimit):
		WriteProblem(w, http.StatusConflict, ErrorCodeLimitReached)
	case errors.Is(err, postgres.ErrConflict):
		WriteProblem(w, http.StatusConflict, ErrorCodeConflict)
	default:
		p.internal(w, r, what, err)
	}
}

func (a *AccountServer) ListPlaylists(w http.ResponseWriter, r *http.Request, _ ListPlaylistsParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	list, err := p.Store.Personal().Playlists(r.Context(), uid)
	if err != nil {
		p.internal(w, r, "playlists", err)
		return
	}
	items := make([]PlaylistSummary, len(list))
	for i, pl := range list {
		items[i] = presentPlaylistSummary(pl)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PlaylistList{Items: items})
}

func (a *AccountServer) CreatePlaylist(w http.ResponseWriter, r *http.Request, _ CreatePlaylistParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	var body CreatePlaylistRequest
	if !decode(w, r, &body) {
		return
	}
	name, okName := cleanPlaylistName(body.Name)
	if !okName {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "name", Message: "1 to 60 characters"})
		return
	}
	var in []PlaylistTrackInput
	if body.Tracks != nil {
		in = *body.Tracks
	}
	tracks, problem := playlistTracks(in)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	pl, err := p.Store.Personal().CreatePlaylist(r.Context(), uid, name, tracks)
	if err != nil {
		p.playlistError(w, r, "create playlist", err)
		return
	}
	p.writePlaylist(w, r, uid, http.StatusCreated, pl, "created")
}

func (a *AccountServer) GetPlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, _ GetPlaylistParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "playlist_id", playlistID)
	if !ok {
		return
	}
	pl, err := p.Store.Personal().Playlist(r.Context(), uid, id)
	if err != nil {
		p.playlistError(w, r, "playlist", err)
		return
	}
	out, err := p.presentPlaylist(r.Context(), uid, pl)
	if err != nil {
		p.internal(w, r, "present playlist", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (a *AccountServer) RenamePlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, _ RenamePlaylistParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "playlist_id", playlistID)
	if !ok {
		return
	}
	var body RenamePlaylistRequest
	if !decode(w, r, &body) {
		return
	}
	name, okName := cleanPlaylistName(body.Name)
	if !okName {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "name", Message: "1 to 60 characters"})
		return
	}
	pl, err := p.Store.Personal().RenamePlaylist(r.Context(), uid, id, name)
	if err != nil {
		p.playlistError(w, r, "rename playlist", err)
		return
	}
	out := presentPlaylistSummary(pl)
	p.playlistChanged(r.Context(), uid, "updated", pl.ID, &out)
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (a *AccountServer) DeletePlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, _ DeletePlaylistParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "playlist_id", playlistID)
	if !ok {
		return
	}
	if err := p.Store.Personal().DeletePlaylist(r.Context(), uid, id); err != nil {
		p.playlistError(w, r, "delete playlist", err)
		return
	}
	p.playlistChanged(r.Context(), uid, "deleted", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AccountServer) ReplacePlaylistTracks(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, _ ReplacePlaylistTracksParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "playlist_id", playlistID)
	if !ok {
		return
	}
	var body ReplacePlaylistTracksRequest
	if !decode(w, r, &body) {
		return
	}
	tracks, problem := playlistTracks(body.Tracks)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	pl, err := p.Store.Personal().ReplacePlaylistTracks(r.Context(), uid, id, body.Version, tracks)
	if err != nil {
		p.playlistError(w, r, "replace playlist tracks", err)
		return
	}
	p.writePlaylist(w, r, uid, http.StatusOK, pl, "updated")
}
