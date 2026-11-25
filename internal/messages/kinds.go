package messages

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	MaxPollQuestion     = 300
	MaxPollOption       = 100
	MinPollOptions      = 2
	MaxPollOptions      = 10
	MaxPollExplanation  = 200
	MinPollOpen         = 5 * time.Minute
	MaxPollOpen         = 30 * 24 * time.Hour
	MaxPlaceTitle       = 100
	MaxLocationAccuracy = 10000
)

type PollInput struct {
	Question        string     `json:"question"`
	Options         []string   `json:"options"`
	MultipleAnswers bool       `json:"multiple_answers,omitempty"`
	Anonymous       bool       `json:"anonymous"`
	CorrectOption   *int       `json:"correct_option,omitempty"`
	Explanation     *string    `json:"explanation,omitempty"`
	CloseAt         *time.Time `json:"close_at,omitempty"`
}

type ContactInput struct {
	UserID int64 `json:"user_id,string"`
}

type LocationInput struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lon"`
	AccuracyM *int    `json:"accuracy_m,omitempty"`
	Title     *string `json:"title,omitempty"`
}

type Location struct {
	LocationInput
	Signature string `json:"sig,omitempty"`
}

type MapSigner interface {
	Sign(lat, lon float64) string
}

type Directory interface {
	ForViewer(ctx context.Context, viewer int64, ids []int64) (map[int64]users.View, error)
}

func trimmed(s string) string { return strings.TrimSpace(s) }

func validLine(s string, maxLen int) bool {
	return s != "" && validText(s, maxLen) && !strings.ContainsAny(s, "\n\r")
}

func validatePayload(r SendRequest) error {
	if r.Poll != nil && r.Type != scylla.TypePoll {
		return invalid("only a poll message has poll")
	}
	if r.Contact != nil && r.Type != scylla.TypeContact {
		return invalid("only a contact message has contact")
	}
	if r.Location != nil && r.Type != scylla.TypeLocation {
		return invalid("only a location message has location")
	}
	switch r.Type {
	case scylla.TypePoll, scylla.TypeContact, scylla.TypeLocation:
	default:
		return nil
	}
	if r.Content != nil || r.AttachmentID != nil || len(r.AlbumIDs) > 0 || r.StickerID != nil {
		return invalid("a %s message has no content, attachment or sticker", r.Type)
	}
	switch r.Type {
	case scylla.TypePoll:
		if r.Poll == nil {
			return invalid("a poll message needs poll")
		}
		return validatePoll(*r.Poll)
	case scylla.TypeContact:
		if r.Contact == nil || r.Contact.UserID <= 0 {
			return invalid("a contact message needs contact.user_id")
		}
	case scylla.TypeLocation:
		if r.Location == nil {
			return invalid("a location message needs location")
		}
		return validateLocation(*r.Location)
	}
	return nil
}

func validatePoll(p PollInput) error {
	if !validLine(trimmed(p.Question), MaxPollQuestion) {
		return invalid("poll.question must be 1 to %d characters on one line", MaxPollQuestion)
	}
	if len(p.Options) < MinPollOptions || len(p.Options) > MaxPollOptions {
		return invalid("poll.options must contain %d to %d items", MinPollOptions, MaxPollOptions)
	}
	seen := make(map[string]bool, len(p.Options))
	for _, o := range p.Options {
		text := trimmed(o)
		if !validLine(text, MaxPollOption) {
			return invalid("each poll option must be 1 to %d characters on one line", MaxPollOption)
		}
		key := strings.ToLower(text)
		if seen[key] {
			return invalid("poll options must differ")
		}
		seen[key] = true
	}
	if p.CorrectOption != nil {
		if *p.CorrectOption < 0 || *p.CorrectOption >= len(p.Options) {
			return invalid("poll.quiz.correct_option must be the index of an option")
		}
		if p.MultipleAnswers {
			return invalid("a quiz takes one answer; multiple_answers must be false")
		}
	}
	if p.Explanation != nil {
		if p.CorrectOption == nil {
			return invalid("only a quiz has an explanation")
		}
		if text := trimmed(*p.Explanation); text == "" || !validText(text, MaxPollExplanation) {
			return invalid("poll.quiz.explanation must be 1 to %d characters", MaxPollExplanation)
		}
	}
	return nil
}

func validateLocation(l LocationInput) error {
	if math.IsNaN(l.Latitude) || math.IsNaN(l.Longitude) || l.Latitude < -90 || l.Latitude > 90 || l.Longitude < -180 || l.Longitude > 180 {
		return invalid("location.latitude must be -90 to 90 and location.longitude -180 to 180")
	}
	if l.AccuracyM != nil && (*l.AccuracyM < 0 || *l.AccuracyM > MaxLocationAccuracy) {
		return invalid("location.accuracy_m must be 0 to %d", MaxLocationAccuracy)
	}
	if l.Title != nil && !validLine(trimmed(*l.Title), MaxPlaceTitle) {
		return invalid("location.title must be 1 to %d characters on one line", MaxPlaceTitle)
	}
	return nil
}

func (s *Service) checkPollTime(p *PollInput) error {
	if p == nil || p.CloseAt == nil {
		return nil
	}
	now := s.now()
	if p.CloseAt.Before(now.Add(MinPollOpen)) || p.CloseAt.After(now.Add(MaxPollOpen)) {
		return invalid("poll.close_at must be 5 minutes to 30 days from now")
	}
	return nil
}

func roundCoordinate(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func formatCoordinate(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func OpenStreetMapURL(lat, lon float64) string {
	la, lo := formatCoordinate(lat), formatCoordinate(lon)
	return fmt.Sprintf("https://www.openstreetmap.org/?mlat=%s&mlon=%s#map=16/%s/%s", la, lo, la, lo)
}

func (s *Service) cleanPoll(ctx context.Context, userID int64, p PollInput) (PollInput, error) {
	out := p
	var err error
	if out.Question, err = s.cleanEmoji(ctx, userID, trimmed(p.Question)); err != nil {
		return PollInput{}, err
	}
	out.Options = make([]string, len(p.Options))
	for i, o := range p.Options {
		if out.Options[i], err = s.cleanEmoji(ctx, userID, trimmed(o)); err != nil {
			return PollInput{}, err
		}
	}
	if p.Explanation != nil {
		text, err := s.cleanEmoji(ctx, userID, trimmed(*p.Explanation))
		if err != nil {
			return PollInput{}, err
		}
		out.Explanation = &text
	}
	if p.CloseAt != nil {
		at := p.CloseAt.UTC().Truncate(time.Second)
		out.CloseAt = &at
	}
	return out, nil
}

func (s *Service) cleanLocation(ctx context.Context, userID int64, l LocationInput) (Location, error) {
	out := Location{LocationInput: LocationInput{Latitude: roundCoordinate(l.Latitude), Longitude: roundCoordinate(l.Longitude), AccuracyM: l.AccuracyM}}
	if l.Title != nil {
		text, err := s.cleanEmoji(ctx, userID, trimmed(*l.Title))
		if err != nil {
			return Location{}, err
		}
		out.Title = &text
	}
	if s.d.Maps != nil {
		out.Signature = s.d.Maps.Sign(out.Latitude, out.Longitude)
	}
	return out, nil
}

func (s *Service) checkContact(ctx context.Context, senderID, userID int64) (users.View, error) {
	if s.d.Directory == nil {
		return users.View{}, fmt.Errorf("%w: no directory", ErrUnavailable)
	}
	views, err := s.d.Directory.ForViewer(ctx, senderID, []int64{userID})
	if err != nil {
		return users.View{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	v, ok := views[userID]
	if !ok || v.IsDeleted || !v.Findable {
		return users.View{}, fmt.Errorf("%w: contact.user_id is not a user you can see", ErrForbidden)
	}
	return v, nil
}

func pollSummary(question string) string { return "📊 " + question }

func contactSummary(username string) string { return "👤 @" + username }

func locationSummary(l Location) string {
	link := OpenStreetMapURL(l.Latitude, l.Longitude)
	if l.Title != nil {
		return "📍 " + *l.Title + " " + link
	}
	return "📍 " + link
}

func encodePayload(v any) *string {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := string(raw)
	return &s
}

func ParseContact(m scylla.Message) (ContactInput, bool) {
	if m.Type != scylla.TypeContact || m.Payload == nil {
		return ContactInput{}, false
	}
	var c ContactInput
	if err := json.Unmarshal([]byte(*m.Payload), &c); err != nil || c.UserID <= 0 {
		return ContactInput{}, false
	}
	return c, true
}

func ParseLocation(m scylla.Message) (Location, bool) {
	if m.Type != scylla.TypeLocation || m.Payload == nil {
		return Location{}, false
	}
	var l Location
	if err := json.Unmarshal([]byte(*m.Payload), &l); err != nil {
		return Location{}, false
	}
	return l, true
}

func ContactUserIDs(msgs ...scylla.Message) []int64 {
	var out []int64
	for _, m := range msgs {
		if c, ok := ParseContact(m); ok && !m.Deleted {
			out = append(out, c.UserID)
		}
	}
	return out
}

func PollIDs(msgs ...scylla.Message) []int64 {
	var out []int64
	for _, m := range msgs {
		if m.Type == scylla.TypePoll && !m.Deleted {
			out = append(out, m.ID)
		}
	}
	return out
}

func unsentPayload(r SendRequest) []byte {
	var v any
	switch {
	case r.Poll != nil:
		v = r.Poll
	case r.Contact != nil:
		v = r.Contact
	case r.Location != nil:
		v = r.Location
	default:
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func UnsentPayload(u postgres.UnsentMessage) (*PollInput, *ContactInput, *LocationInput) {
	if len(u.Payload) == 0 {
		return nil, nil, nil
	}
	switch u.Type {
	case scylla.TypePoll:
		var p PollInput
		if json.Unmarshal(u.Payload, &p) == nil {
			return &p, nil, nil
		}
	case scylla.TypeContact:
		var c ContactInput
		if json.Unmarshal(u.Payload, &c) == nil {
			return nil, &c, nil
		}
	case scylla.TypeLocation:
		var l LocationInput
		if json.Unmarshal(u.Payload, &l) == nil {
			return nil, nil, &l
		}
	}
	return nil, nil, nil
}

type kindExtra struct {
	content *string
	payload *string
	poll    *PollInput
}

func (s *Service) prepareKind(ctx context.Context, r SendRequest) (kindExtra, error) {
	switch r.Type {
	case scylla.TypePoll:
		p, err := s.cleanPoll(ctx, r.SenderID, *r.Poll)
		if err != nil {
			return kindExtra{}, err
		}
		summary := pollSummary(p.Question)
		return kindExtra{content: &summary, poll: &p}, nil
	case scylla.TypeContact:
		v, err := s.checkContact(ctx, r.SenderID, r.Contact.UserID)
		if err != nil {
			return kindExtra{}, err
		}
		summary := contactSummary(v.Username)
		return kindExtra{content: &summary, payload: encodePayload(ContactInput{UserID: v.ID})}, nil
	case scylla.TypeLocation:
		l, err := s.cleanLocation(ctx, r.SenderID, *r.Location)
		if err != nil {
			return kindExtra{}, err
		}
		summary := locationSummary(l)
		return kindExtra{content: &summary, payload: encodePayload(l)}, nil
	}
	return kindExtra{}, nil
}
