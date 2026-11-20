package httpapi

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/publicnames"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var (
	RatePublicIP     = redis.Rate{Name: "public_ip", Rate: 60, Period: time.Minute, Burst: 30}
	RatePublicSubnet = redis.Rate{Name: "public_subnet", Rate: 600, Period: time.Minute, Burst: 200}
)

type PublicStore interface {
	Users() postgres.UserRepository
	Social() postgres.SocialRepository
	Sharing() postgres.SharingRepository
}

type PublicGroups interface {
	InviteCard(ctx context.Context, token string) (chats.GroupCard, error)
	PublicGroupCard(ctx context.Context, name string) (chats.GroupCard, error)
}

type HiddenUsers interface {
	Hidden(ctx context.Context, userIDs []int64) (map[int64]bool, error)
}

type PublicOptions struct {
	Store          PublicStore
	Groups         PublicGroups
	Hidden         HiddenUsers
	Media          *MediaServer
	Limiter        RateAllower
	TrustedProxies []netip.Prefix
	BasePath       string
	WebOrigin      string
	Log            *slog.Logger
}

type PublicServer struct{ o PublicOptions }

func NewPublicServer(o PublicOptions) *PublicServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &PublicServer{o: o}
}

type publicCard struct {
	kind   PublicNameKind
	user   *postgres.User
	avatar bool
	bio    bool
	group  *chats.GroupCard
}

func (p *PublicServer) allowed(w http.ResponseWriter, r *http.Request) bool {
	ip := clientIP(r, p.o.TrustedProxies)
	if !allow(w, r, p.o.Limiter, RatePublicIP, ip.String(), p.o.Log) {
		return false
	}
	bits := 24
	if ip.Is6() && !ip.Is4In6() {
		bits = 48
	}
	prefix, err := ip.Prefix(bits)
	if err != nil {
		return true
	}
	return allow(w, r, p.o.Limiter, RatePublicSubnet, prefix.String(), p.o.Log)
}

func publicHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "public, max-age=60")
}

func (p *PublicServer) lookup(ctx context.Context, name string) (publicCard, error) {
	unavailable := publicCard{kind: PublicNameKindUnavailable}
	if !publicnames.Valid(name) {
		return unavailable, nil
	}
	owner, err := p.o.Store.Sharing().PublicName(ctx, name)
	if errors.Is(err, postgres.ErrNotFound) {
		return unavailable, nil
	}
	if err != nil {
		return publicCard{}, err
	}
	if owner.ChatID != nil {
		card, err := p.o.Groups.PublicGroupCard(ctx, name)
		if errors.Is(err, chats.ErrNotFound) {
			return unavailable, nil
		}
		if err != nil {
			return publicCard{}, err
		}
		return publicCard{kind: PublicNameKindGroup, group: &card}, nil
	}
	if owner.UserID == nil {
		return unavailable, nil
	}
	u, err := p.o.Store.Users().Get(ctx, *owner.UserID)
	if errors.Is(err, postgres.ErrNotFound) {
		return unavailable, nil
	}
	if err != nil {
		return publicCard{}, err
	}
	settings, err := p.o.Store.Social().Privacy(ctx, []int64{u.ID})
	if err != nil {
		return publicCard{}, err
	}
	s := settings[u.ID]
	if s.WebProfileVisibility != postgres.VisibleEveryone || s.SearchVisibility != postgres.VisibleEveryone {
		return unavailable, nil
	}
	if p.o.Hidden != nil {
		hidden, err := p.o.Hidden.Hidden(ctx, []int64{u.ID})
		if err != nil {
			return publicCard{}, err
		}
		if hidden[u.ID] {
			return unavailable, nil
		}
	}
	return publicCard{
		kind: PublicNameKindUser, user: &u,
		avatar: s.AvatarVisibility == postgres.VisibleEveryone && u.AvatarAttachmentID != nil,
		bio:    s.ProfileVisibility == postgres.VisibleEveryone && u.Bio != nil && strings.TrimSpace(*u.Bio) != "",
	}, nil
}

func (p *PublicServer) presentUser(c publicCard) PublicUser {
	u := c.user
	out := PublicUser{Username: u.Username, DisplayName: u.DisplayName}
	if out.DisplayName == "" {
		out.DisplayName = u.Username
	}
	if c.avatar {
		a := p.o.BasePath + "/public/names/" + url.PathEscape(u.Username) + "/avatar?rev=" + u.AvatarAttachmentID.String()
		out.AvatarUrl = &a
	}
	if c.bio {
		b := *u.Bio
		out.Bio = &b
	}
	return out
}

func (p *PublicServer) GetPublicName(w http.ResponseWriter, r *http.Request, name PublicNameParam) {
	if !p.allowed(w, r) {
		return
	}
	c, err := p.lookup(r.Context(), name)
	if err != nil {
		p.unavailable(w, r, "public name", err)
		return
	}
	out := PublicNameLookup{Kind: c.kind}
	switch c.kind {
	case PublicNameKindUser:
		u := p.presentUser(c)
		out.User = &u
	case PublicNameKindGroup:
		g := PresentPublicGroup(*c.group, p.o.BasePath, "")
		out.Group = &g
	}
	publicHeaders(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (p *PublicServer) GetPublicInvite(w http.ResponseWriter, r *http.Request, token InviteToken) {
	if !p.allowed(w, r) {
		return
	}
	card, err := p.o.Groups.InviteCard(r.Context(), token)
	if errors.Is(err, chats.ErrNotFound) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		p.unavailable(w, r, "public invite", err)
		return
	}
	publicHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusOK, PresentPublicGroup(card, p.o.BasePath, token))
}

func (p *PublicServer) unavailable(w http.ResponseWriter, r *http.Request, what string, err error) {
	p.o.Log.WarnContext(r.Context(), what, "error", err)
	w.Header().Set("Retry-After", "2")
	WriteProblem(w, http.StatusServiceUnavailable, ErrorCodeInternalError)
}

func (p *PublicServer) serveAvatar(w http.ResponseWriter, r *http.Request, attachment *uuid.UUID, rev *string, size *int) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if attachment == nil || p.o.Media == nil {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	cache := "public, max-age=300"
	if rev != nil && *rev == attachment.String() {
		cache = "public, max-age=86400"
	}
	p.o.Media.serveAvatar(w, r, attachment, size, cache)
}

func (p *PublicServer) GetPublicNameAvatar(w http.ResponseWriter, r *http.Request, name PublicNameParam, params GetPublicNameAvatarParams) {
	if !p.allowed(w, r) {
		return
	}
	c, err := p.lookup(r.Context(), name)
	if err != nil {
		p.unavailable(w, r, "public avatar", err)
		return
	}
	var attachment *uuid.UUID
	switch c.kind {
	case PublicNameKindUser:
		if c.avatar {
			attachment = c.user.AvatarAttachmentID
		}
	case PublicNameKindGroup:
		attachment = c.group.Chat.AvatarAttachmentID
	}
	p.serveAvatar(w, r, attachment, params.Rev, params.Size)
}

func (p *PublicServer) GetPublicInviteAvatar(w http.ResponseWriter, r *http.Request, token InviteToken, params GetPublicInviteAvatarParams) {
	if !p.allowed(w, r) {
		return
	}
	card, err := p.o.Groups.InviteCard(r.Context(), token)
	if errors.Is(err, chats.ErrNotFound) {
		p.serveAvatar(w, r, nil, nil, nil)
		return
	}
	if err != nil {
		p.unavailable(w, r, "public invite avatar", err)
		return
	}
	p.serveAvatar(w, r, card.Chat.AvatarAttachmentID, params.Rev, params.Size)
}

var ogPage = template.Must(template.New("og").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="robots" content="noindex, nofollow">
<title>{{.Title}}</title>
<meta name="description" content="{{.Description}}">
<meta property="og:type" content="profile">
<meta property="og:site_name" content="Messenger">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Description}}">
{{if .URL}}<meta property="og:url" content="{{.URL}}">
{{end}}{{if .Image}}<meta property="og:image" content="{{.Image}}">
{{end}}<meta name="twitter:card" content="summary">
<meta name="twitter:title" content="{{.Title}}">
<meta name="twitter:description" content="{{.Description}}">
{{if .Image}}<meta name="twitter:image" content="{{.Image}}">
{{end}}</head>
<body><h1>{{.Title}}</h1><p>{{.Description}}</p>{{if .URL}}<p><a href="{{.URL}}">Open in Messenger</a></p>{{end}}</body>
</html>
`))

type ogData struct {
	Title       string
	Description string
	URL         string
	Image       string
}

func (p *PublicServer) absolute(path string) string {
	if p.o.WebOrigin == "" {
		return ""
	}
	return strings.TrimRight(p.o.WebOrigin, "/") + path
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func groupDescription(g PublicGroup) string {
	members := "1 member"
	if g.MemberCount != 1 {
		members = strconv.Itoa(g.MemberCount) + " members"
	}
	if g.Description != nil && *g.Description != "" {
		return members + " · " + truncate(*g.Description, 200)
	}
	return members + " · Join the group on Messenger"
}

func (p *PublicServer) writeOG(w http.ResponseWriter, d ogData) {
	if d.Title == "" {
		d.Title = "Messenger"
	}
	if d.Description == "" {
		d.Description = "Chats that look the way you like."
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(http.StatusOK)
	_ = ogPage.Execute(w, d)
}

func (p *PublicServer) GetOpenGraphName(w http.ResponseWriter, r *http.Request, name PublicNameParam) {
	if !p.allowed(w, r) {
		return
	}
	c, err := p.lookup(r.Context(), name)
	if err != nil {
		p.o.Log.WarnContext(r.Context(), "open graph name", "error", err)
		p.writeOG(w, ogData{})
		return
	}
	switch c.kind {
	case PublicNameKindUser:
		u := p.presentUser(c)
		d := ogData{Title: u.DisplayName + " (@" + u.Username + ")", URL: p.absolute("/@" + url.PathEscape(u.Username))}
		if u.Bio != nil {
			d.Description = truncate(*u.Bio, 200)
		} else {
			d.Description = "Message @" + u.Username + " on Messenger"
		}
		if u.AvatarUrl != nil {
			d.Image = p.absolute(*u.AvatarUrl + "&size=256")
		}
		p.writeOG(w, d)
	case PublicNameKindGroup:
		g := PresentPublicGroup(*c.group, p.o.BasePath, "")
		d := ogData{Title: g.Name, Description: groupDescription(g), URL: p.absolute("/@" + url.PathEscape(*c.group.Chat.PublicName))}
		if g.AvatarUrl != nil {
			d.Image = p.absolute(*g.AvatarUrl + "&size=256")
		}
		p.writeOG(w, d)
	default:
		p.writeOG(w, ogData{})
	}
}

func (p *PublicServer) GetOpenGraphInvite(w http.ResponseWriter, r *http.Request, token InviteToken) {
	if !p.allowed(w, r) {
		return
	}
	card, err := p.o.Groups.InviteCard(r.Context(), token)
	if err != nil {
		if !errors.Is(err, chats.ErrNotFound) {
			p.o.Log.WarnContext(r.Context(), "open graph invite", "error", err)
		}
		p.writeOG(w, ogData{})
		return
	}
	g := PresentPublicGroup(card, p.o.BasePath, token)
	d := ogData{Title: g.Name, Description: groupDescription(g), URL: p.absolute("/join/" + url.PathEscape(token))}
	if g.AvatarUrl != nil {
		d.Image = p.absolute(*g.AvatarUrl + "&size=256")
	}
	p.writeOG(w, d)
}
