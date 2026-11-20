package httpapi

import "net/http"

func (s *Server) WithPublic(p *PublicServer) *Server { s.public = p; return s }

func (s *Server) ListGroupInviteLinks(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListGroupInviteLinksParams) {
	if s.chats == nil {
		s.AuthServer.ListGroupInviteLinks(w, r, chatId, params)
		return
	}
	s.chats.ListGroupInviteLinks(w, r, chatId, params)
}

func (s *Server) CreateGroupInviteLink(w http.ResponseWriter, r *http.Request, chatId ChatId, params CreateGroupInviteLinkParams) {
	if s.chats == nil {
		s.AuthServer.CreateGroupInviteLink(w, r, chatId, params)
		return
	}
	s.chats.CreateGroupInviteLink(w, r, chatId, params)
}

func (s *Server) UpdateGroupInviteLink(w http.ResponseWriter, r *http.Request, chatId ChatId, linkId InviteLinkId, params UpdateGroupInviteLinkParams) {
	if s.chats == nil {
		s.AuthServer.UpdateGroupInviteLink(w, r, chatId, linkId, params)
		return
	}
	s.chats.UpdateGroupInviteLink(w, r, chatId, linkId, params)
}

func (s *Server) RevokeGroupInviteLink(w http.ResponseWriter, r *http.Request, chatId ChatId, linkId InviteLinkId, params RevokeGroupInviteLinkParams) {
	if s.chats == nil {
		s.AuthServer.RevokeGroupInviteLink(w, r, chatId, linkId, params)
		return
	}
	s.chats.RevokeGroupInviteLink(w, r, chatId, linkId, params)
}

func (s *Server) SetGroupPublicName(w http.ResponseWriter, r *http.Request, chatId ChatId, params SetGroupPublicNameParams) {
	if s.chats == nil {
		s.AuthServer.SetGroupPublicName(w, r, chatId, params)
		return
	}
	s.chats.SetGroupPublicName(w, r, chatId, params)
}

func (s *Server) ClearGroupPublicName(w http.ResponseWriter, r *http.Request, chatId ChatId, params ClearGroupPublicNameParams) {
	if s.chats == nil {
		s.AuthServer.ClearGroupPublicName(w, r, chatId, params)
		return
	}
	s.chats.ClearGroupPublicName(w, r, chatId, params)
}

func (s *Server) ListGroupBans(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListGroupBansParams) {
	if s.chats == nil {
		s.AuthServer.ListGroupBans(w, r, chatId, params)
		return
	}
	s.chats.ListGroupBans(w, r, chatId, params)
}

func (s *Server) UnbanGroupUser(w http.ResponseWriter, r *http.Request, chatId ChatId, userId UserId, params UnbanGroupUserParams) {
	if s.chats == nil {
		s.AuthServer.UnbanGroupUser(w, r, chatId, userId, params)
		return
	}
	s.chats.UnbanGroupUser(w, r, chatId, userId, params)
}

func (s *Server) GetInvite(w http.ResponseWriter, r *http.Request, token InviteToken, params GetInviteParams) {
	if s.chats == nil {
		s.AuthServer.GetInvite(w, r, token, params)
		return
	}
	s.chats.GetInvite(w, r, token, params)
}

func (s *Server) JoinByInvite(w http.ResponseWriter, r *http.Request, token InviteToken, params JoinByInviteParams) {
	if s.chats == nil {
		s.AuthServer.JoinByInvite(w, r, token, params)
		return
	}
	s.chats.JoinByInvite(w, r, token, params)
}

func (s *Server) SearchPublicGroups(w http.ResponseWriter, r *http.Request, params SearchPublicGroupsParams) {
	if s.chats == nil {
		s.AuthServer.SearchPublicGroups(w, r, params)
		return
	}
	s.chats.SearchPublicGroups(w, r, params)
}

func (s *Server) GetPublicGroup(w http.ResponseWriter, r *http.Request, publicName PublicNameParam, params GetPublicGroupParams) {
	if s.chats == nil {
		s.AuthServer.GetPublicGroup(w, r, publicName, params)
		return
	}
	s.chats.GetPublicGroup(w, r, publicName, params)
}

func (s *Server) JoinPublicGroup(w http.ResponseWriter, r *http.Request, publicName PublicNameParam, params JoinPublicGroupParams) {
	if s.chats == nil {
		s.AuthServer.JoinPublicGroup(w, r, publicName, params)
		return
	}
	s.chats.JoinPublicGroup(w, r, publicName, params)
}

func (s *Server) GetPublicNameAvailability(w http.ResponseWriter, r *http.Request, publicName PublicNameParam, params GetPublicNameAvailabilityParams) {
	if s.chats == nil {
		s.AuthServer.GetPublicNameAvailability(w, r, publicName, params)
		return
	}
	s.chats.GetPublicNameAvailability(w, r, publicName, params)
}

func (s *Server) ListUnreadMentions(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListUnreadMentionsParams) {
	if s.chats == nil {
		s.AuthServer.ListUnreadMentions(w, r, chatId, params)
		return
	}
	s.chats.ListUnreadMentions(w, r, chatId, params)
}

func (s *Server) GetPublicName(w http.ResponseWriter, r *http.Request, publicName PublicNameParam) {
	if s.public == nil {
		s.AuthServer.GetPublicName(w, r, publicName)
		return
	}
	s.public.GetPublicName(w, r, publicName)
}

func (s *Server) GetPublicInvite(w http.ResponseWriter, r *http.Request, token InviteToken) {
	if s.public == nil {
		s.AuthServer.GetPublicInvite(w, r, token)
		return
	}
	s.public.GetPublicInvite(w, r, token)
}

func (s *Server) GetPublicNameAvatar(w http.ResponseWriter, r *http.Request, publicName PublicNameParam, params GetPublicNameAvatarParams) {
	if s.public == nil {
		s.AuthServer.GetPublicNameAvatar(w, r, publicName, params)
		return
	}
	s.public.GetPublicNameAvatar(w, r, publicName, params)
}

func (s *Server) GetPublicInviteAvatar(w http.ResponseWriter, r *http.Request, token InviteToken, params GetPublicInviteAvatarParams) {
	if s.public == nil {
		s.AuthServer.GetPublicInviteAvatar(w, r, token, params)
		return
	}
	s.public.GetPublicInviteAvatar(w, r, token, params)
}

func (s *Server) GetOpenGraphName(w http.ResponseWriter, r *http.Request, publicName PublicNameParam) {
	if s.public == nil {
		s.AuthServer.GetOpenGraphName(w, r, publicName)
		return
	}
	s.public.GetOpenGraphName(w, r, publicName)
}

func (s *Server) GetOpenGraphInvite(w http.ResponseWriter, r *http.Request, token InviteToken) {
	if s.public == nil {
		s.AuthServer.GetOpenGraphInvite(w, r, token)
		return
	}
	s.public.GetOpenGraphInvite(w, r, token)
}
