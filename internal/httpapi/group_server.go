package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/chats"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
)

// RateCreateGroup limits new groups per user.
var RateCreateGroup = redis.Rate{Name: "create_group", Rate: 20, Period: time.Hour, Burst: 10}

// PresentGroup renders a group as one member sees it.
func PresentGroup(g chats.GroupView, basePath string) Group {
	out := Group{
		Id: FormatID(g.Chat.ID), OwnerId: FormatID(g.OwnerID), MyRole: PresentRole(g.MyRole),
		CreatedAt: utc(g.Chat.CreatedAt), Members: make([]GroupMember, len(g.Members)),
	}
	if g.Chat.Name != nil {
		out.Name = *g.Chat.Name
	}
	if g.Chat.Description != "" {
		d := g.Chat.Description
		out.Description = &d
	}
	if g.Chat.AvatarAttachmentID != nil {
		url := basePath + "/attachments/" + g.Chat.AvatarAttachmentID.String() + "/content"
		out.AvatarUrl = &url
	}
	for i, m := range g.Members {
		out.Members[i] = GroupMember{User: PresentUser(m.User), Role: PresentRole(m.Role), JoinedAt: utc(m.JoinedAt)}
	}
	return out
}

func (c *ChatServer) writeGroup(w http.ResponseWriter, r *http.Request, status int, g chats.GroupView, err error, what string) {
	if err != nil {
		serviceError(w, r, c.o.Log, what, err)
		return
	}
	noStore(w)
	writeJSONStatus(w, status, PresentGroup(g, c.o.BasePath))
}

// ListGroups implements GET /groups.
func (c *ChatServer) ListGroups(w http.ResponseWriter, r *http.Request, params ListGroupsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	views, next, err := c.o.Service.ListGroups(r.Context(), p.UserID, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "list groups", err)
		return
	}
	page := ChatPage{Items: make([]Chat, len(views))}
	for i, v := range views {
		page.Items[i] = PresentChat(v, c.o.BasePath)
	}
	if next != "" {
		page.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, page)
}

// CreateGroup implements POST /groups.
func (c *ChatServer) CreateGroup(w http.ResponseWriter, r *http.Request, _ CreateGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body CreateGroupRequest
	if !decode(w, r, &body) {
		return
	}
	members := make([]int64, len(body.MemberIds))
	for i, s := range body.MemberIds {
		if members[i], ok = parseID(w, "member_ids", s); !ok {
			return
		}
	}
	if !allow(w, r, c.o.Limiter, RateCreateGroup, strconv.FormatInt(p.UserID, 10), c.o.Log) {
		return
	}
	g, err := c.o.Service.CreateGroup(r.Context(), p.UserID, body.Name, body.Description, members)
	c.writeGroup(w, r, http.StatusCreated, g, err, "create group")
}

// GetGroup implements GET /groups/{id}.
func (c *ChatServer) GetGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ GetGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	g, err := c.o.Service.Group(r.Context(), p.UserID, id)
	c.writeGroup(w, r, http.StatusOK, g, err, "get group")
}

// UpdateGroup implements PATCH /groups/{id}: only the sent fields change; "description": null clears it.
func (c *ChatServer) UpdateGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ UpdateGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	var (
		name    *string
		setDesc bool
		desc    *string
	)
	for k, v := range raw {
		switch k {
		case "name":
			var n string
			if json.Unmarshal(v, &n) != nil {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "name", Message: "a string"})
				return
			}
			name = &n
		case "description":
			if json.Unmarshal(v, &desc) != nil {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "description", Message: "a string or null"})
				return
			}
			setDesc = true
		default:
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: k, Message: "unknown field"})
			return
		}
	}
	g, err := c.o.Service.UpdateGroup(r.Context(), p.UserID, id, name, setDesc, desc)
	c.writeGroup(w, r, http.StatusOK, g, err, "update group")
}

// DeleteGroup implements DELETE /groups/{id}.
func (c *ChatServer) DeleteGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ DeleteGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if err := c.o.Service.DeleteGroup(r.Context(), p.UserID, id); err != nil {
		serviceError(w, r, c.o.Log, "delete group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetGroupAvatar implements PUT /groups/{id}/avatar.
func (c *ChatServer) SetGroupAvatar(w http.ResponseWriter, r *http.Request, chatID ChatId, _ SetGroupAvatarParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body SetAvatarRequest
	if !decode(w, r, &body) {
		return
	}
	g, err := c.o.Service.SetGroupAvatar(r.Context(), p.UserID, id, body.AttachmentId)
	c.writeGroup(w, r, http.StatusOK, g, err, "set group avatar")
}

// AddGroupParticipant implements POST /groups/{id}/participants.
func (c *ChatServer) AddGroupParticipant(w http.ResponseWriter, r *http.Request, chatID ChatId, _ AddGroupParticipantParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body AddParticipantRequest
	if !decode(w, r, &body) {
		return
	}
	target, ok := parseID(w, "user_id", body.UserId)
	if !ok {
		return
	}
	g, err := c.o.Service.AddMember(r.Context(), p.UserID, id, target)
	c.writeGroup(w, r, http.StatusOK, g, err, "add participant")
}

// RemoveGroupParticipant implements DELETE /groups/{id}/participants/{user_id}.
func (c *ChatServer) RemoveGroupParticipant(w http.ResponseWriter, r *http.Request, chatID ChatId, userID UserId, _ RemoveGroupParticipantParams) {
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
	if err := c.o.Service.RemoveMember(r.Context(), p.UserID, id, target); err != nil {
		serviceError(w, r, c.o.Log, "remove participant", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateGroupParticipantRole implements PATCH /groups/{id}/participants/{user_id}.
func (c *ChatServer) UpdateGroupParticipantRole(w http.ResponseWriter, r *http.Request, chatID ChatId, userID UserId, _ UpdateGroupParticipantRoleParams) {
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
	var body UpdateRoleRequest
	if !decode(w, r, &body) {
		return
	}
	if !body.Role.Valid() {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "role", Message: "admin or member"})
		return
	}
	g, err := c.o.Service.SetRole(r.Context(), p.UserID, id, target, postgres.Role(body.Role))
	c.writeGroup(w, r, http.StatusOK, g, err, "set role")
}

// TransferGroupOwnership implements POST /groups/{id}/transfer-owner.
func (c *ChatServer) TransferGroupOwnership(w http.ResponseWriter, r *http.Request, chatID ChatId, _ TransferGroupOwnershipParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	var body TransferOwnerRequest
	if !decode(w, r, &body) {
		return
	}
	target, ok := parseID(w, "user_id", body.UserId)
	if !ok {
		return
	}
	g, err := c.o.Service.TransferOwnership(r.Context(), p.UserID, id, target)
	c.writeGroup(w, r, http.StatusOK, g, err, "transfer ownership")
}

// LeaveGroup implements POST /groups/{id}/leave.
func (c *ChatServer) LeaveGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, _ LeaveGroupParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	if err := c.o.Service.Leave(r.Context(), p.UserID, id); err != nil {
		serviceError(w, r, c.o.Log, "leave group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
