package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

const (
	StaticMapWidth  = 640
	StaticMapHeight = 320
)

func PresentPoll(v messages.PollView) Poll {
	p := v.Poll
	out := Poll{
		Question: p.Question, MultipleAnswers: p.MultipleAnswers, Anonymous: p.Anonymous, Quiz: p.Quiz(),
		MyVotes: v.MyVotes, CanClose: v.CanClose, Version: p.Version, Options: make([]PollOption, len(p.Options)),
		CloseAt: utcPtr(p.CloseAt), ClosedAt: utcPtr(p.ClosedAt),
	}
	for i, text := range p.Options {
		o := PollOption{Text: text}
		if v.ResultsVisible {
			n := 0
			if i < len(p.Counts) {
				n = p.Counts[i]
			}
			o.VoterCount = &n
			if p.Quiz() {
				correct := i == *p.CorrectOption
				o.IsCorrect = &correct
			}
		}
		out.Options[i] = o
	}
	if v.ResultsVisible {
		n := p.Voters
		out.TotalVoters = &n
		if p.Quiz() {
			out.Explanation = p.Explanation
		}
	}
	return out
}

func StaticMapURL(basePath string, l messages.Location) string {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(l.Latitude, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(l.Longitude, 'f', -1, 64))
	q.Set("sig", l.Signature)
	return basePath + "/maps/static?" + q.Encode()
}

func PresentLocation(l messages.Location, basePath string) MessageLocation {
	return MessageLocation{
		Latitude: l.Latitude, Longitude: l.Longitude, AccuracyM: l.AccuracyM, Title: l.Title,
		Map: MapImage{Url: StaticMapURL(basePath, l), Width: StaticMapWidth, Height: StaticMapHeight},
	}
}

func PresentContact(c messages.ContactInput, view *users.View) ContactCard {
	card := ContactCard{UserId: FormatID(c.UserID)}
	if view != nil && !view.IsDeleted {
		u := PresentUser(*view)
		card.User = &u
	}
	return card
}

func presentKind(out *Message, m scylla.Message, p MessageParts) {
	if m.Deleted {
		return
	}
	switch m.Type {
	case scylla.TypePoll:
		if p.Poll != nil {
			poll := PresentPoll(*p.Poll)
			out.Poll = &poll
		}
	case scylla.TypeContact:
		if c, ok := messages.ParseContact(m); ok && p.ContactLoaded {
			card := PresentContact(c, p.Contact)
			out.Contact = &card
		}
	case scylla.TypeLocation:
		if l, ok := messages.ParseLocation(m); ok {
			loc := PresentLocation(l, p.BasePath)
			out.Location = &loc
		}
	}
}

func PollInputFrom(p PollCreate) messages.PollInput {
	in := messages.PollInput{Question: p.Question, Options: p.Options, Anonymous: true, CloseAt: p.CloseAt}
	if p.MultipleAnswers != nil {
		in.MultipleAnswers = *p.MultipleAnswers
	}
	if p.Anonymous != nil {
		in.Anonymous = *p.Anonymous
	}
	if p.Quiz != nil {
		correct := p.Quiz.CorrectOption
		in.CorrectOption, in.Explanation = &correct, p.Quiz.Explanation
	}
	return in
}

func PollCreateFrom(in messages.PollInput) PollCreate {
	out := PollCreate{Question: in.Question, Options: in.Options, CloseAt: in.CloseAt}
	multiple, anonymous := in.MultipleAnswers, in.Anonymous
	out.MultipleAnswers, out.Anonymous = &multiple, &anonymous
	if in.CorrectOption != nil {
		out.Quiz = &PollQuizCreate{CorrectOption: *in.CorrectOption, Explanation: in.Explanation}
	}
	return out
}

func LocationInputFrom(l LocationCreate) messages.LocationInput {
	return messages.LocationInput{Latitude: l.Latitude, Longitude: l.Longitude, AccuracyM: l.AccuracyM, Title: l.Title}
}

func LocationCreateFrom(l messages.LocationInput) LocationCreate {
	return LocationCreate{Latitude: l.Latitude, Longitude: l.Longitude, AccuracyM: l.AccuracyM, Title: l.Title}
}

func kindRequest(w http.ResponseWriter, body SendMessageRequest, req *messages.SendRequest) bool {
	if body.Poll != nil {
		in := PollInputFrom(*body.Poll)
		req.Poll = &in
	}
	if body.Contact != nil {
		user, ok := parseID(w, "contact.user_id", body.Contact.UserId)
		if !ok {
			return false
		}
		req.Contact = &messages.ContactInput{UserID: user}
	}
	if body.Location != nil {
		in := LocationInputFrom(*body.Location)
		req.Location = &in
	}
	return true
}
