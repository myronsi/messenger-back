package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	maxPronouns    = 40
	maxMusicText   = 100
	maxTimeZoneLen = 64
)

var (
	presetPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	colorPattern  = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

func presentBirthday(b *postgres.Birthday) *Birthday {
	if b == nil {
		return nil
	}
	return &Birthday{Day: b.Day, Month: b.Month, Year: b.Year}
}

func presentMusic(m *users.Music, basePath string) *ProfileMusic {
	if m == nil {
		return nil
	}
	return &ProfileMusic{Attachment: PresentAttachment(m.Attachment, "", basePath), Title: m.Title, Artist: m.Artist}
}

func presentBackground(b *users.Background, basePath string) *ProfileBackground {
	if b == nil {
		return nil
	}
	out := &ProfileBackground{Kind: ProfileBackgroundKind(b.Kind), Preset: b.Preset}
	if b.Attachment != nil {
		a := PresentAttachment(*b.Attachment, "", basePath)
		out.Attachment = &a
	}
	if len(b.Colors) > 0 {
		colors := append([]BackgroundColor(nil), b.Colors...)
		out.Colors = &colors
	}
	return out
}

func validBirthday(b postgres.Birthday) bool {
	if b.Month < 1 || b.Month > 12 || b.Day < 1 {
		return false
	}
	year := 2000
	if b.Year != nil {
		if *b.Year < 1900 || *b.Year > 2100 {
			return false
		}
		year = *b.Year
	}
	t := time.Date(year, time.Month(b.Month), b.Day, 0, 0, 0, 0, time.UTC)
	return t.Day() == b.Day && int(t.Month()) == b.Month
}

func parseBirthday(raw json.RawMessage) (*postgres.Birthday, bool) {
	var in *struct {
		Day   *int `json:"day"`
		Month *int `json:"month"`
		Year  *int `json:"year"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, false
	}
	if in == nil {
		return nil, true
	}
	if in.Day == nil || in.Month == nil {
		return nil, false
	}
	b := postgres.Birthday{Day: *in.Day, Month: *in.Month, Year: in.Year}
	if !validBirthday(b) {
		return nil, false
	}
	return &b, true
}

func parsePronouns(raw json.RawMessage) (*string, bool) {
	var p *string
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	if p == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*p)
	if utf8.RuneCountInString(trimmed) > maxPronouns || strings.ContainsFunc(trimmed, isControl) {
		return nil, false
	}
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func (a *AccountServer) parseTimeZone(ctx context.Context, raw json.RawMessage) (*string, bool, error) {
	var tz string
	if json.Unmarshal(raw, &tz) != nil || tz == "" || len(tz) > maxTimeZoneLen || tz == "Local" {
		return nil, false, nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, false, nil
	}
	known, err := a.o.Store.Users().KnownTimeZone(ctx, tz)
	if err != nil || !known {
		return nil, false, err
	}
	return &tz, true, nil
}

func cleanMusicText(s *string) (*string, bool) {
	if s == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*s)
	if utf8.RuneCountInString(trimmed) > maxMusicText || strings.ContainsFunc(trimmed, isControl) {
		return nil, false
	}
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

func backgroundFromRequest(req SetBackgroundRequest) (*postgres.Background, *ProblemError) {
	bg := &postgres.Background{Kind: string(req.Kind)}
	switch req.Kind {
	case Media:
		if req.AttachmentId == nil || req.Preset != nil || req.Colors != nil {
			return nil, &ProblemError{Field: "attachment_id", Message: "media needs attachment_id and nothing else"}
		}
		id := *req.AttachmentId
		bg.AttachmentID = &id
	case Preset:
		if req.Preset == nil || req.AttachmentId != nil || req.Colors != nil || !presetPattern.MatchString(*req.Preset) {
			return nil, &ProblemError{Field: "preset", Message: "preset needs a preset name and nothing else"}
		}
		p := *req.Preset
		bg.Preset = &p
	case Color:
		if req.Colors == nil || req.AttachmentId != nil || req.Preset != nil || len(*req.Colors) < 1 || len(*req.Colors) > 2 {
			return nil, &ProblemError{Field: "colors", Message: "color needs one or two colors and nothing else"}
		}
		for _, c := range *req.Colors {
			c = strings.ToLower(c)
			if !colorPattern.MatchString(c) {
				return nil, &ProblemError{Field: "colors", Message: "colors are #rrggbb"}
			}
			bg.Colors = append(bg.Colors, c)
		}
	default:
		return nil, &ProblemError{Field: "kind", Message: "unknown value"}
	}
	return bg, nil
}

func (a *AccountServer) ownUpload(ctx context.Context, userID int64, id uuid.UUID, purpose string) (bool, error) {
	att, err := a.o.Store.Attachments().Get(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return att.UploaderID != nil && *att.UploaderID == userID && att.Purpose == purpose, nil
}

func (a *AccountServer) presentMe(ctx context.Context, u postgres.User) (Me, error) {
	media, err := a.o.Directory.MediaOf(ctx, u)
	if err != nil {
		return Me{}, err
	}
	return PresentMe(u, a.o.BasePath, media), nil
}

func (a *AccountServer) profileChanged(w http.ResponseWriter, r *http.Request, userID, before int64, u postgres.User) {
	a.emit(r.Context(), events.Event{Type: events.UserUpdated, UserID: userID, ActorID: userID})
	if a.o.ProfileChanged != nil && u.ProfileVersion != before {
		a.o.ProfileChanged(context.WithoutCancel(r.Context()), userID, u.ProfileVersion)
	}
	me, err := a.presentMe(r.Context(), u)
	if err != nil {
		a.internal(w, r, "render profile", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, me)
}

func (a *AccountServer) SetMyMusic(w http.ResponseWriter, r *http.Request, _ SetMyMusicParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body SetMusicRequest
	if !decode(w, r, &body) {
		return
	}
	title, okTitle := cleanMusicText(body.Title)
	artist, okArtist := cleanMusicText(body.Artist)
	if !okTitle || !okArtist {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "title", Message: "title and artist are at most 100 characters"})
		return
	}
	mine, err := a.ownUpload(r.Context(), p.UserID, body.AttachmentId, postgres.PurposeProfileMusic)
	if err != nil {
		a.internal(w, r, "music attachment", err)
		return
	}
	if !mine {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "attachment_id", Message: "not one of your profile music uploads"})
		return
	}
	before := profileVersionOf(r.Context(), a.o.Store.Users(), p.UserID)
	id := body.AttachmentId
	u, err := a.o.Store.Users().SetMusic(r.Context(), p.UserID, &id, title, artist)
	if err != nil {
		a.internal(w, r, "set music", err)
		return
	}
	a.profileChanged(w, r, p.UserID, before, u)
}

func (a *AccountServer) DeleteMyMusic(w http.ResponseWriter, r *http.Request, _ DeleteMyMusicParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	before := profileVersionOf(r.Context(), a.o.Store.Users(), p.UserID)
	u, err := a.o.Store.Users().SetMusic(r.Context(), p.UserID, nil, nil, nil)
	if err != nil {
		a.internal(w, r, "delete music", err)
		return
	}
	a.profileChanged(w, r, p.UserID, before, u)
}

func (a *AccountServer) SetMyBackground(w http.ResponseWriter, r *http.Request, _ SetMyBackgroundParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body SetBackgroundRequest
	if !decode(w, r, &body) {
		return
	}
	bg, problem := backgroundFromRequest(body)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	if bg.AttachmentID != nil {
		mine, err := a.ownUpload(r.Context(), p.UserID, *bg.AttachmentID, postgres.PurposeProfileBackground)
		if err != nil {
			a.internal(w, r, "background attachment", err)
			return
		}
		if !mine {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "attachment_id", Message: "not one of your background uploads"})
			return
		}
	}
	before := profileVersionOf(r.Context(), a.o.Store.Users(), p.UserID)
	u, err := a.o.Store.Users().SetBackground(r.Context(), p.UserID, bg)
	if err != nil {
		a.internal(w, r, "set background", err)
		return
	}
	a.profileChanged(w, r, p.UserID, before, u)
}

func (a *AccountServer) DeleteMyBackground(w http.ResponseWriter, r *http.Request, _ DeleteMyBackgroundParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	before := profileVersionOf(r.Context(), a.o.Store.Users(), p.UserID)
	u, err := a.o.Store.Users().SetBackground(r.Context(), p.UserID, nil)
	if err != nil {
		a.internal(w, r, "delete background", err)
		return
	}
	a.profileChanged(w, r, p.UserID, before, u)
}

func (c *ChatServer) SetGroupBackground(w http.ResponseWriter, r *http.Request, chatID ChatId, _ SetGroupBackgroundParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body SetBackgroundRequest
	if !decode(w, r, &body) || !c.changeAllowed(w, r, p.UserID) {
		return
	}
	bg, problem := backgroundFromRequest(body)
	if problem != nil {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, *problem)
		return
	}
	g, err := c.o.Service.SetGroupBackground(r.Context(), p.UserID, id, bg)
	c.writeGroup(w, r, http.StatusOK, g, err, "set group background")
}

func (c *ChatServer) DeleteGroupBackground(w http.ResponseWriter, r *http.Request, chatID ChatId, _ DeleteGroupBackgroundParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok || !c.changeAllowed(w, r, p.UserID) {
		return
	}
	g, err := c.o.Service.SetGroupBackground(r.Context(), p.UserID, id, nil)
	c.writeGroup(w, r, http.StatusOK, g, err, "delete group background")
}

const (
	maxRawDisplayName = 400
	maxRawBio         = 4000
)

type EmojiCleaner interface {
	Clean(ctx context.Context, userID int64, text string) (string, error)
}

func (a *AccountServer) cleanText(ctx context.Context, userID int64, text string) (string, error) {
	if a.o.Emoji == nil {
		return emoji.Plain(text), nil
	}
	return a.o.Emoji.Clean(ctx, userID, text)
}

func (a *AccountServer) cleanProfileEmoji(w http.ResponseWriter, r *http.Request, userID int64, prof *postgres.ProfilePatch) bool {
	if prof.DisplayName != nil {
		name, err := a.cleanText(r.Context(), userID, *prof.DisplayName)
		if err != nil {
			a.internal(w, r, "check emoji", err)
			return false
		}
		prof.DisplayName = &name
	}
	if prof.Bio != nil {
		bio, err := a.cleanText(r.Context(), userID, *prof.Bio)
		if err != nil {
			a.internal(w, r, "check emoji", err)
			return false
		}
		prof.Bio = &bio
	}
	return true
}
