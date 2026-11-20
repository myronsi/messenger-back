package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

var (
	RateJoinByLink  = redis.Rate{Name: "join_by_link", Rate: 20, Period: time.Hour, Burst: 10}
	RateGroupLookup = redis.Rate{Name: "group_lookup", Rate: 120, Period: time.Minute, Burst: 60}
)

func PresentInviteLink(l postgres.InviteLink, now time.Time) InviteLink {
	out := InviteLink{
		Id: FormatID(l.ID), Token: l.Token, Name: l.Name, CreatedAt: utc(l.CreatedAt), UseCount: int(l.UseCount),
		IsActive: l.Active(now), ExpiresAt: utcPtr(l.ExpiresAt), RevokedAt: utcPtr(l.RevokedAt),
	}
	if l.CreatedBy != nil {
		id := FormatID(*l.CreatedBy)
		out.CreatedBy = &id
	}
	if l.MaxUses != nil {
		n := int(*l.MaxUses)
		out.MaxUses = &n
	}
	return out
}

func avatarRev(c postgres.Chat) string {
	if c.AvatarAttachmentID == nil {
		return ""
	}
	return c.AvatarAttachmentID.String()
}

func PresentPublicGroup(card chats.GroupCard, basePath, inviteToken string) PublicGroup {
	c := card.Chat
	out := PublicGroup{MemberCount: card.MemberCount, PublicName: c.PublicName}
	if c.Name != nil {
		out.Name = *c.Name
	}
	if c.Description != "" {
		d := c.Description
		out.Description = &d
	}
	if rev := avatarRev(c); rev != "" {
		var u string
		switch {
		case c.PublicName != nil:
			u = basePath + "/public/names/" + url.PathEscape(*c.PublicName) + "/avatar?rev=" + rev
		case inviteToken != "":
			u = basePath + "/public/invites/" + url.PathEscape(inviteToken) + "/avatar?rev=" + rev
		}
		if u != "" {
			out.AvatarUrl = &u
		}
	}
	return out
}

func presentPreview(p chats.GroupPreview, basePath, token string) GroupPreview {
	out := GroupPreview{Group: PresentPublicGroup(p.Card, basePath, token)}
	if p.MemberOf {
		id := FormatID(p.Card.Chat.ID)
		out.ChatId = &id
	}
	return out
}

func (c *ChatServer) lookupAllowed(w http.ResponseWriter, r *http.Request, userID int64) bool {
	return allow(w, r, c.o.Limiter, RateGroupLookup, strconv.FormatInt(userID, 10), c.o.Log)
}

func (c *ChatServer) ListGroupInviteLinks(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ListGroupInviteLinksParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	links, err := c.o.Service.InviteLinks(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, c.o.Log, "list invite links", err)
		return
	}
	now := time.Now()
	out := InviteLinkList{Items: make([]InviteLink, len(links))}
	for i, l := range links {
		out.Items[i] = PresentInviteLink(l, now)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (c *ChatServer) CreateGroupInviteLink(w http.ResponseWriter, r *http.Request, chatID ChatId, _ CreateGroupInviteLinkParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body CreateInviteLinkRequest
	if !decode(w, r, &body) {
		return
	}
	if !c.changeAllowed(w, r, p.UserID) {
		return
	}
	in := chats.InviteLinkInput{Name: body.Name, ExpiresAt: body.ExpiresAt}
	if body.MaxUses != nil {
		n := int32(min(max(*body.MaxUses, 0), chats.MaxInviteLinkUses+1))
		in.MaxUses = &n
	}
	l, err := c.o.Service.CreateInviteLink(r.Context(), p.UserID, id, in)
	if err != nil {
		serviceError(w, r, c.o.Log, "create invite link", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusCreated, PresentInviteLink(l, time.Now()))
}

func decodeInviteLinkPatch(w http.ResponseWriter, r *http.Request) (postgres.InviteLinkPatch, bool) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		WriteProblem(w, http.StatusUnsupportedMediaType, ErrorCodeUnsupportedMediaType)
		return postgres.InviteLinkPatch{}, false
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
		return postgres.InviteLinkPatch{}, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "body", Message: "not a JSON object"})
		return postgres.InviteLinkPatch{}, false
	}
	var body UpdateInviteLinkRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "body", Message: "unknown or malformed field"})
		return postgres.InviteLinkPatch{}, false
	}
	var p postgres.InviteLinkPatch
	if _, ok := fields["name"]; ok {
		p.SetName, p.Name = true, body.Name
	}
	if _, ok := fields["expires_at"]; ok {
		p.SetExpiresAt, p.ExpiresAt = true, body.ExpiresAt
	}
	if _, ok := fields["max_uses"]; ok {
		p.SetMaxUses = true
		if body.MaxUses != nil {
			n := int32(min(max(*body.MaxUses, 0), chats.MaxInviteLinkUses+1))
			p.MaxUses = &n
		}
	}
	if !p.SetName && !p.SetExpiresAt && !p.SetMaxUses {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "body", Message: "send at least one field"})
		return postgres.InviteLinkPatch{}, false
	}
	return p, true
}

func (c *ChatServer) UpdateGroupInviteLink(w http.ResponseWriter, r *http.Request, chatID ChatId, linkID InviteLinkId, _ UpdateGroupInviteLinkParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	lid, ok := parseID(w, "link_id", linkID)
	if !ok {
		return
	}
	patch, ok := decodeInviteLinkPatch(w, r)
	if !ok {
		return
	}
	if !c.changeAllowed(w, r, p.UserID) {
		return
	}
	l, err := c.o.Service.UpdateInviteLink(r.Context(), p.UserID, id, lid, patch)
	if err != nil {
		serviceError(w, r, c.o.Log, "update invite link", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentInviteLink(l, time.Now()))
}

func (c *ChatServer) RevokeGroupInviteLink(w http.ResponseWriter, r *http.Request, chatID ChatId, linkID InviteLinkId, _ RevokeGroupInviteLinkParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	lid, ok := parseID(w, "link_id", linkID)
	if !ok {
		return
	}
	if err := c.o.Service.RevokeInviteLink(r.Context(), p.UserID, id, lid); err != nil {
		serviceError(w, r, c.o.Log, "revoke invite link", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ChatServer) SetGroupPublicName(w http.ResponseWriter, r *http.Request, chatID ChatId, _ SetGroupPublicNameParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body SetPublicNameRequest
	if !decode(w, r, &body) {
		return
	}
	if !c.changeAllowed(w, r, p.UserID) {
		return
	}
	name := body.PublicName
	g, err := c.o.Service.SetPublicName(r.Context(), p.UserID, id, &name)
	c.writeGroup(w, r, http.StatusOK, g, err, "set public name")
}

func (c *ChatServer) ClearGroupPublicName(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ClearGroupPublicNameParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	g, err := c.o.Service.SetPublicName(r.Context(), p.UserID, id, nil)
	c.writeGroup(w, r, http.StatusOK, g, err, "clear public name")
}

func (c *ChatServer) GetPublicNameAvailability(w http.ResponseWriter, r *http.Request, name PublicNameParam, _ GetPublicNameAvailabilityParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if !c.lookupAllowed(w, r, p.UserID) {
		return
	}
	free, err := c.o.Service.PublicNameAvailable(r.Context(), name)
	if err != nil {
		serviceError(w, r, c.o.Log, "public name availability", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PublicNameAvailability{PublicName: name, Available: free})
}

func (c *ChatServer) ListGroupBans(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListGroupBansParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	bans, next, err := c.o.Service.Bans(r.Context(), p.UserID, id, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "list bans", err)
		return
	}
	out := GroupBanPage{Items: make([]GroupBan, len(bans))}
	for i, b := range bans {
		out.Items[i] = GroupBan{User: PresentUser(b.User), CreatedAt: utc(b.Ban.CreatedAt)}
		if b.Ban.BannedBy != nil {
			by := FormatID(*b.Ban.BannedBy)
			out.Items[i].BannedBy = &by
		}
	}
	if next != "" {
		out.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (c *ChatServer) UnbanGroupUser(w http.ResponseWriter, r *http.Request, chatID ChatId, userID UserId, _ UnbanGroupUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	target, ok := parseID(w, "user_id", userID)
	if !ok {
		return
	}
	if err := c.o.Service.Unban(r.Context(), p.UserID, id, target); err != nil {
		serviceError(w, r, c.o.Log, "unban", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ChatServer) GetInvite(w http.ResponseWriter, r *http.Request, token InviteToken, _ GetInviteParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if !c.lookupAllowed(w, r, p.UserID) {
		return
	}
	pv, err := c.o.Service.InvitePreview(r.Context(), p.UserID, token)
	if err != nil {
		serviceError(w, r, c.o.Log, "get invite", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentPreview(pv, c.o.BasePath, token))
}

func (c *ChatServer) writeJoined(w http.ResponseWriter, r *http.Request, v chats.View, err error) {
	if err != nil {
		serviceError(w, r, c.o.Log, "join group", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentChat(v, c.o.BasePath))
}

func (c *ChatServer) JoinByInvite(w http.ResponseWriter, r *http.Request, token InviteToken, _ JoinByInviteParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if !allow(w, r, c.o.Limiter, RateJoinByLink, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	v, err := c.o.Service.JoinByLink(r.Context(), p.UserID, token)
	c.writeJoined(w, r, v, err)
}

func (c *ChatServer) SearchPublicGroups(w http.ResponseWriter, r *http.Request, params SearchPublicGroupsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if n := len([]rune(params.Q)); n < 2 || n > 64 {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "q", Message: "2 to 64 characters"})
		return
	}
	if !c.lookupAllowed(w, r, p.UserID) {
		return
	}
	items, next, err := c.o.Service.SearchPublicGroups(r.Context(), p.UserID, params.Q, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "search public groups", err)
		return
	}
	out := GroupPreviewPage{Items: make([]GroupPreview, len(items))}
	for i, it := range items {
		out.Items[i] = presentPreview(it, c.o.BasePath, "")
	}
	if next != "" {
		out.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (c *ChatServer) GetPublicGroup(w http.ResponseWriter, r *http.Request, name PublicNameParam, _ GetPublicGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if !c.lookupAllowed(w, r, p.UserID) {
		return
	}
	pv, err := c.o.Service.PublicGroupPreview(r.Context(), p.UserID, name)
	if err != nil {
		serviceError(w, r, c.o.Log, "get public group", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentPreview(pv, c.o.BasePath, ""))
}

func (c *ChatServer) JoinPublicGroup(w http.ResponseWriter, r *http.Request, name PublicNameParam, _ JoinPublicGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	if !allow(w, r, c.o.Limiter, RateJoinByLink, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	v, err := c.o.Service.JoinPublic(r.Context(), p.UserID, name)
	c.writeJoined(w, r, v, err)
}

func (c *ChatServer) ListUnreadMentions(w http.ResponseWriter, r *http.Request, chatID ChatId, _ ListUnreadMentionsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	ids, err := c.o.Service.UnreadMentions(r.Context(), p.UserID, id)
	if err != nil {
		serviceError(w, r, c.o.Log, "unread mentions", err)
		return
	}
	out := MentionList{MessageIds: make([]Id, len(ids))}
	for i, m := range ids {
		out.MessageIds[i] = FormatID(m)
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
