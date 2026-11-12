package httpapi

import (
	"net/http"
)

func (c *ChatServer) ListGroupMembers(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListGroupMembersParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "chat_id", chatID)
	if !ok {
		return
	}
	query := ""
	if params.Q != nil {
		query = *params.Q
	}
	members, next, total, err := c.o.Service.GroupMembers(r.Context(), p.UserID, id, query, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "group members", err)
		return
	}
	out := GroupMemberPage{Items: make([]GroupMember, len(members)), Total: int(total)}
	for i, m := range members {
		out.Items[i] = GroupMember{User: PresentUser(m.User), Role: PresentRole(m.Role), JoinedAt: utc(m.JoinedAt)}
	}
	if next != "" {
		out.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

func (c *ChatServer) ListCommonGroups(w http.ResponseWriter, r *http.Request, userID Id, params ListCommonGroupsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "user_id", userID)
	if !ok {
		return
	}
	groups, next, err := c.o.Service.CommonGroups(r.Context(), p.UserID, id, cursorParam(params.After), pageLimit(params.Limit))
	if err != nil {
		serviceError(w, r, c.o.Log, "common groups", err)
		return
	}
	out := CommonGroupPage{Items: make([]CommonGroup, len(groups))}
	for i, g := range groups {
		item := CommonGroup{Id: FormatID(g.ID), Name: g.Name, MemberCount: g.MemberCount}
		if g.AvatarAttachmentID != nil {
			url := c.o.BasePath + "/attachments/" + g.AvatarAttachmentID.String() + "/content"
			item.AvatarUrl = &url
		}
		out.Items[i] = item
	}
	if next != "" {
		out.NextCursor = &next
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}
