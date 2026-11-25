package contentreports

import (
	"time"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
	"github.com/myronsi/messenger-back/internal/users"
)

func copyKind(c *CopyMessage, m scylla.Message, people map[int64]postgres.User, polls map[int64]postgres.Poll, now time.Time) {
	if m.Deleted {
		return
	}
	if p, ok := polls[m.ID]; ok {
		cp := &CopyPoll{
			Question: p.Question, Options: make([]CopyPollOption, len(p.Options)), MultipleAnswers: p.MultipleAnswers,
			Anonymous: p.Anonymous, Quiz: p.Quiz(), Closed: p.IsClosed(now),
		}
		for i, text := range p.Options {
			cp.Options[i] = CopyPollOption{Text: text, Votes: p.Counts[i]}
		}
		c.Poll = cp
	}
	if l, ok := messages.ParseLocation(m); ok {
		c.Location = &CopyLocation{Latitude: l.Latitude, Longitude: l.Longitude, Title: l.Title}
	}
	if ct, ok := messages.ParseContact(m); ok {
		cc := &CopyContact{UserID: id(ct.UserID), Name: users.DeletedName}
		if u, found := people[ct.UserID]; found {
			cc.Name, cc.Username = u.DisplayName, u.Username
		}
		c.Contact = cc
	}
}
