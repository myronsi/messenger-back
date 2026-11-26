package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	EventClientSettingsUpdated = "client_settings_updated"
	EventWallpaperUpdated      = "wallpaper_updated"
	EventPlaylistsUpdated      = "playlists_updated"

	maxRecentEmoji     = 24
	maxRecentReactions = 8
	maxRecentEmojiLen  = 100
)

var accentPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type UserNotifier interface {
	NotifyUser(ctx context.Context, uid int64, kind string, data any)
}

type AttachmentAuthorizer interface {
	Authorize(ctx context.Context, viewerID int64, id uuid.UUID) (postgres.Attachment, error)
}

type PersonalStore interface {
	Personal() postgres.PersonalRepository
	Attachments() postgres.AttachmentRepository
}

type Personal struct {
	Store    PersonalStore
	Media    AttachmentAuthorizer
	Notifier UserNotifier
	BasePath string
	Log      *slog.Logger
}

func (p *Personal) logger() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

func (p *Personal) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	p.logger().ErrorContext(r.Context(), what, "error", err)
	WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
}

func (p *Personal) notify(ctx context.Context, uid int64, kind string, data any) {
	if p.Notifier != nil {
		p.Notifier.NotifyUser(context.WithoutCancel(ctx), uid, kind, data)
	}
}

func (a *AccountServer) personal(w http.ResponseWriter, r *http.Request) (*Personal, int64, bool) {
	if a.o.Personal == nil {
		notImplemented(w, r)
		return nil, 0, false
	}
	pr, ok := principalOr401(w, r)
	if !ok {
		return nil, 0, false
	}
	return a.o.Personal, pr.UserID, true
}

func presentClientSettings(s postgres.ClientSettings) ClientSettings {
	out := ClientSettings{RecentEmoji: s.RecentEmoji, RecentReactions: s.RecentReactions, Version: s.Version}
	if out.RecentEmoji == nil {
		out.RecentEmoji = []RecentEmoji{}
	}
	if out.RecentReactions == nil {
		out.RecentReactions = []RecentEmoji{}
	}
	if s.Version > 0 && s.UpdatedAt != nil {
		at := utc(*s.UpdatedAt)
		out.UpdatedAt = &at
	}
	if len(s.Appearance) > 0 {
		var ap AppearanceSettings
		if json.Unmarshal(s.Appearance, &ap) == nil {
			out.Appearance = &ap
		}
	}
	return out
}

func (a *AccountServer) GetClientSettings(w http.ResponseWriter, r *http.Request, _ GetClientSettingsParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	s, err := p.Store.Personal().ClientSettings(r.Context(), uid)
	if err != nil {
		p.internal(w, r, "client settings", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentClientSettings(s))
}

func validAppearance(ap AppearanceSettings) bool {
	return accentPattern.MatchString(ap.Accent) && ap.FontSize >= 14 && ap.FontSize <= 20 && ap.Density.Valid() && ap.Corners.Valid()
}

func cleanRecent(list []RecentEmoji, limit int) ([]string, bool) {
	if len(list) > limit {
		return nil, false
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, e := range list {
		if e == "" || utf8.RuneCountInString(e) > maxRecentEmojiLen || !utf8.ValidString(e) || strings.ContainsFunc(e, isControl) {
			return nil, false
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, true
}

func clientSettingsPatch(body UpdateClientSettingsRequest) (postgres.ClientSettingsPatch, *ProblemError) {
	var patch postgres.ClientSettingsPatch
	if body.Appearance == nil && body.RecentEmoji == nil && body.RecentReactions == nil {
		return patch, &ProblemError{Field: "appearance", Message: "send at least one part"}
	}
	if body.Appearance != nil {
		if !validAppearance(*body.Appearance) {
			return patch, &ProblemError{Field: "appearance", Message: "accent, font_size, density or corners is out of range"}
		}
		raw, err := json.Marshal(body.Appearance)
		if err != nil {
			return patch, &ProblemError{Field: "appearance", Message: "invalid"}
		}
		patch.Appearance = raw
	}
	if body.RecentEmoji != nil {
		list, ok := cleanRecent(*body.RecentEmoji, maxRecentEmoji)
		if !ok {
			return patch, &ProblemError{Field: "recent_emoji", Message: "at most 24 emoji of 1 to 100 characters"}
		}
		patch.RecentEmoji = &list
	}
	if body.RecentReactions != nil {
		list, ok := cleanRecent(*body.RecentReactions, maxRecentReactions)
		if !ok {
			return patch, &ProblemError{Field: "recent_reactions", Message: "at most 8 reactions of 1 to 100 characters"}
		}
		patch.RecentReactions = &list
	}
	return patch, nil
}

func (a *AccountServer) UpdateClientSettings(w http.ResponseWriter, r *http.Request, _ UpdateClientSettingsParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	var body UpdateClientSettingsRequest
	if !decode(w, r, &body) {
		return
	}
	patch, problem := clientSettingsPatch(body)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	s, err := p.Store.Personal().UpdateClientSettings(r.Context(), uid, patch)
	if err != nil {
		p.internal(w, r, "update client settings", err)
		return
	}
	out := presentClientSettings(s)
	p.notify(r.Context(), uid, EventClientSettingsUpdated, out)
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func wallpaperFromRequest(req SetWallpaperRequest) (postgres.Wallpaper, *ProblemError) {
	wp := postgres.Wallpaper{Kind: string(req.Kind)}
	switch req.Kind {
	case Preset:
		if req.Preset == nil || req.Colors != nil || req.Pattern != nil || req.AttachmentId != nil || req.Blur != nil || req.Dim != nil || !presetPattern.MatchString(*req.Preset) {
			return wp, &ProblemError{Field: "preset", Message: "preset needs a preset name and nothing else"}
		}
		name := *req.Preset
		wp.Preset = &name
	case Color:
		if req.Colors == nil || req.Preset != nil || req.AttachmentId != nil || req.Blur != nil || req.Dim != nil || len(*req.Colors) < 1 || len(*req.Colors) > 2 {
			return wp, &ProblemError{Field: "colors", Message: "color needs one or two colors and may have a pattern"}
		}
		for _, c := range *req.Colors {
			c = strings.ToLower(c)
			if !colorPattern.MatchString(c) {
				return wp, &ProblemError{Field: "colors", Message: "colors are #rrggbb"}
			}
			wp.Colors = append(wp.Colors, c)
		}
		if req.Pattern != nil {
			if !req.Pattern.Valid() {
				return wp, &ProblemError{Field: "pattern", Message: "unknown value"}
			}
			pattern := string(*req.Pattern)
			wp.Pattern = &pattern
		}
	case Media:
		if req.AttachmentId == nil || req.Preset != nil || req.Colors != nil || req.Pattern != nil {
			return wp, &ProblemError{Field: "attachment_id", Message: "media needs attachment_id and may have blur and dim"}
		}
		id := *req.AttachmentId
		wp.AttachmentID = &id
		blur := int16(0)
		if req.Blur != nil {
			if *req.Blur < 0 || *req.Blur > 20 {
				return wp, &ProblemError{Field: "blur", Message: "0 to 20"}
			}
			blur = int16(*req.Blur) //nolint:gosec // checked above
		}
		if req.Dim != nil {
			if *req.Dim < 0 || *req.Dim > 60 {
				return wp, &ProblemError{Field: "dim", Message: "0 to 60"}
			}
			dim := int16(*req.Dim) //nolint:gosec // checked above
			wp.Dim = &dim
		}
		wp.Blur = &blur
	default:
		return wp, &ProblemError{Field: "kind", Message: "unknown value"}
	}
	return wp, nil
}

func (p *Personal) checkWallpaperUpload(ctx context.Context, w http.ResponseWriter, r *http.Request, uid int64, wp postgres.Wallpaper) bool {
	if wp.AttachmentID == nil {
		return true
	}
	att, err := p.Store.Attachments().Get(ctx, *wp.AttachmentID)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		p.internal(w, r, "wallpaper attachment", err)
		return false
	}
	if err != nil || att.UploaderID == nil || *att.UploaderID != uid || att.Purpose != postgres.PurposeChatWallpaper {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "attachment_id", Message: "not one of your chat background uploads"})
		return false
	}
	return true
}

func (p *Personal) presentWallpapers(ctx context.Context, ws []postgres.Wallpaper) ([]WallpaperEntry, error) {
	var ids []uuid.UUID
	for _, wp := range ws {
		if wp.AttachmentID != nil {
			ids = append(ids, *wp.AttachmentID)
		}
	}
	atts := map[uuid.UUID]postgres.Attachment{}
	if len(ids) > 0 {
		found, err := p.Store.Attachments().GetMany(ctx, ids)
		if err != nil {
			return nil, err
		}
		atts = found
	}
	out := make([]WallpaperEntry, 0, len(ws))
	for _, wp := range ws {
		out = append(out, p.presentWallpaper(wp, atts))
	}
	return out, nil
}

func (p *Personal) presentWallpaper(wp postgres.Wallpaper, atts map[uuid.UUID]postgres.Attachment) WallpaperEntry {
	v := Wallpaper{Kind: ProfileBackgroundKind(wp.Kind), Preset: wp.Preset}
	if len(wp.Colors) > 0 {
		colors := append([]BackgroundColor(nil), wp.Colors...)
		v.Colors = &colors
	}
	if wp.Pattern != nil {
		pattern := WallpaperPattern(*wp.Pattern)
		v.Pattern = &pattern
	}
	if wp.AttachmentID != nil {
		if att, ok := atts[*wp.AttachmentID]; ok {
			a := PresentAttachment(att, "", p.BasePath)
			v.Attachment = &a
		}
	}
	if wp.Blur != nil {
		blur := int(*wp.Blur)
		v.Blur = &blur
	}
	if wp.Dim != nil {
		dim := int(*wp.Dim)
		v.Dim = &dim
	}
	return WallpaperEntry{ChatId: formatIDPtr(wp.ChatID), Wallpaper: v, SetBy: FormatID(wp.SetBy), UpdatedAt: utc(wp.UpdatedAt)}
}

func (p *Personal) storeWallpapers(w http.ResponseWriter, r *http.Request, rows []postgres.Wallpaper) ([]WallpaperEntry, bool) {
	stored, err := p.Store.Personal().SetWallpapers(r.Context(), rows)
	if err != nil {
		p.internal(w, r, "set wallpaper", err)
		return nil, false
	}
	entries, err := p.presentWallpapers(r.Context(), stored)
	if err != nil {
		p.internal(w, r, "present wallpaper", err)
		return nil, false
	}
	for i, wp := range stored {
		entry := entries[i]
		p.notify(r.Context(), wp.UserID, EventWallpaperUpdated, map[string]any{"chat_id": entry.ChatId, "entry": entry})
	}
	return entries, true
}

func (p *Personal) removeWallpaper(w http.ResponseWriter, r *http.Request, uid int64, chatID *int64) {
	removed, err := p.Store.Personal().DeleteWallpaper(r.Context(), uid, chatID)
	if err != nil {
		p.internal(w, r, "delete wallpaper", err)
		return
	}
	if removed {
		p.notify(r.Context(), uid, EventWallpaperUpdated, map[string]any{"chat_id": formatIDPtr(chatID), "entry": nil})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *AccountServer) ListWallpapers(w http.ResponseWriter, r *http.Request, _ ListWallpapersParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	ws, err := p.Store.Personal().Wallpapers(r.Context(), uid)
	if err != nil {
		p.internal(w, r, "wallpapers", err)
		return
	}
	items, err := p.presentWallpapers(r.Context(), ws)
	if err != nil {
		p.internal(w, r, "present wallpapers", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, WallpaperList{Items: items})
}

func (a *AccountServer) SetDefaultWallpaper(w http.ResponseWriter, r *http.Request, _ SetDefaultWallpaperParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	var body SetWallpaperRequest
	if !decode(w, r, &body) {
		return
	}
	wp, problem := wallpaperFromRequest(body)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	if !p.checkWallpaperUpload(r.Context(), w, r, uid, wp) {
		return
	}
	wp.UserID, wp.SetBy = uid, uid
	entries, ok := p.storeWallpapers(w, r, []postgres.Wallpaper{wp})
	if !ok {
		return
	}
	writeJSONStatus(w, http.StatusOK, entries[0])
}

func (a *AccountServer) DeleteDefaultWallpaper(w http.ResponseWriter, r *http.Request, _ DeleteDefaultWallpaperParams) {
	p, uid, ok := a.personal(w, r)
	if !ok {
		return
	}
	p.removeWallpaper(w, r, uid, nil)
}

func (c *ChatServer) SetChatWallpaper(w http.ResponseWriter, r *http.Request, chatID ChatId, _ SetChatWallpaperParams) {
	p := c.o.Personal
	if p == nil {
		notImplemented(w, r)
		return
	}
	pr, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body SetChatWallpaperRequest
	if !decode(w, r, &body) {
		return
	}
	wp, problem := wallpaperFromRequest(SetWallpaperRequest{
		Kind: body.Kind, Preset: body.Preset, Colors: body.Colors, Pattern: body.Pattern,
		AttachmentId: body.AttachmentId, Blur: body.Blur, Dim: body.Dim,
	})
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	forBoth := body.ForBoth != nil && *body.ForBoth
	owners, err := c.o.Service.WallpaperOwners(r.Context(), pr.UserID, id, forBoth)
	if err != nil {
		serviceError(w, r, c.o.Log, "chat wallpaper", err)
		return
	}
	if !p.checkWallpaperUpload(r.Context(), w, r, pr.UserID, wp) {
		return
	}
	rows := make([]postgres.Wallpaper, len(owners))
	for i, owner := range owners {
		row := wp
		row.UserID, row.ChatID, row.SetBy = owner, &id, pr.UserID
		rows[i] = row
	}
	entries, ok := p.storeWallpapers(w, r, rows)
	if !ok {
		return
	}
	if forBoth {
		c.o.Service.WallpaperSetForBoth(context.WithoutCancel(r.Context()), id, pr.UserID)
	}
	writeJSONStatus(w, http.StatusOK, entries[0])
}

func (c *ChatServer) DeleteChatWallpaper(w http.ResponseWriter, r *http.Request, chatID ChatId, _ DeleteChatWallpaperParams) {
	p := c.o.Personal
	if p == nil {
		notImplemented(w, r)
		return
	}
	pr, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if _, err := c.o.Service.WallpaperOwners(r.Context(), pr.UserID, id, false); err != nil {
		serviceError(w, r, c.o.Log, "chat wallpaper", err)
		return
	}
	p.removeWallpaper(w, r, pr.UserID, &id)
}
