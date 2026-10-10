package httpapi

import "net/http"

// Server serves the whole contract by delegating to the feature servers: authentication, sessions and the
// account (AuthServer), uploads, downloads and avatars (MediaServer). Operations of features that are not
// implemented yet answer 501 through the AuthServer's embedded Unimplemented.
type Server struct {
	*AuthServer
	media   *MediaServer
	account *AccountServer
	chats   *ChatServer
	meta    MetaInfo
}

// WithChats adds the chat list, direct chat, pin, read and request endpoints.
func (s *Server) WithChats(c *ChatServer) *Server { s.chats = c; return s }

// WithAccount adds the profile, privacy, blocking and user endpoints.
func (s *Server) WithAccount(a *AccountServer) *Server { s.account = a; return s }

// WithMeta sets what GET /meta reports.
func (s *Server) WithMeta(m MetaInfo) *Server { s.meta = m; return s }

// GetMeta implements GET /meta.
func (s *Server) GetMeta(w http.ResponseWriter, _ *http.Request) { s.meta.serve(w) }

var _ ServerInterface = (*Server)(nil)

// NewServer combines the feature servers. media may be nil (its operations then answer 501).
func NewServer(a *AuthServer, media *MediaServer) *Server {
	return &Server{AuthServer: a, media: media}
}

// UploadAttachment implements POST /attachments.
func (s *Server) UploadAttachment(w http.ResponseWriter, r *http.Request, params UploadAttachmentParams) {
	if s.media == nil {
		s.AuthServer.UploadAttachment(w, r, params)
		return
	}
	s.media.UploadAttachment(w, r, params)
}

// GetAttachmentContent implements GET /attachments/{id}/content.
func (s *Server) GetAttachmentContent(w http.ResponseWriter, r *http.Request, id AttachmentId, params GetAttachmentContentParams) {
	if s.media == nil {
		s.AuthServer.GetAttachmentContent(w, r, id, params)
		return
	}
	s.media.GetAttachmentContent(w, r, id, params)
}

// SetMyAvatar implements PUT /me/avatar.
func (s *Server) SetMyAvatar(w http.ResponseWriter, r *http.Request, params SetMyAvatarParams) {
	if s.media == nil {
		s.AuthServer.SetMyAvatar(w, r, params)
		return
	}
	s.media.SetMyAvatar(w, r, params)
}

// GetUserAvatar implements GET /users/{id}/avatar.
func (s *Server) GetUserAvatar(w http.ResponseWriter, r *http.Request, userID UserId, params GetUserAvatarParams) {
	if s.media == nil {
		s.AuthServer.GetUserAvatar(w, r, userID, params)
		return
	}
	s.media.GetUserAvatar(w, r, userID, params)
}

// ListUserAvatars implements GET /users/{id}/avatars.
func (s *Server) ListUserAvatars(w http.ResponseWriter, r *http.Request, userID UserId, params ListUserAvatarsParams) {
	if s.media == nil {
		s.AuthServer.ListUserAvatars(w, r, userID, params)
		return
	}
	s.media.ListUserAvatars(w, r, userID, params)
}

// UpdateMe delegates to the account endpoints.
func (s *Server) UpdateMe(w http.ResponseWriter, r *http.Request, params UpdateMeParams) {
	if s.account == nil {
		s.AuthServer.UpdateMe(w, r, params)
		return
	}
	s.account.UpdateMe(w, r, params)
}

// DeleteMe delegates to the account endpoints.
func (s *Server) DeleteMe(w http.ResponseWriter, r *http.Request, params DeleteMeParams) {
	if s.account == nil {
		s.AuthServer.DeleteMe(w, r, params)
		return
	}
	s.account.DeleteMe(w, r, params)
}

// GetPrivacySettings delegates to the account endpoints.
func (s *Server) GetPrivacySettings(w http.ResponseWriter, r *http.Request, params GetPrivacySettingsParams) {
	if s.account == nil {
		s.AuthServer.GetPrivacySettings(w, r, params)
		return
	}
	s.account.GetPrivacySettings(w, r, params)
}

// UpdatePrivacySettings delegates to the account endpoints.
func (s *Server) UpdatePrivacySettings(w http.ResponseWriter, r *http.Request, params UpdatePrivacySettingsParams) {
	if s.account == nil {
		s.AuthServer.UpdatePrivacySettings(w, r, params)
		return
	}
	s.account.UpdatePrivacySettings(w, r, params)
}

// ListBlockedUsers delegates to the account endpoints.
func (s *Server) ListBlockedUsers(w http.ResponseWriter, r *http.Request, params ListBlockedUsersParams) {
	if s.account == nil {
		s.AuthServer.ListBlockedUsers(w, r, params)
		return
	}
	s.account.ListBlockedUsers(w, r, params)
}

// SearchUsers delegates to the account endpoints.
func (s *Server) SearchUsers(w http.ResponseWriter, r *http.Request, params SearchUsersParams) {
	if s.account == nil {
		s.AuthServer.SearchUsers(w, r, params)
		return
	}
	s.account.SearchUsers(w, r, params)
}

// BlockUser delegates to the account endpoints.
func (s *Server) BlockUser(w http.ResponseWriter, r *http.Request, userID UserId, params BlockUserParams) {
	if s.account == nil {
		s.AuthServer.BlockUser(w, r, userID, params)
		return
	}
	s.account.BlockUser(w, r, userID, params)
}

// UnblockUser delegates to the account endpoints.
func (s *Server) UnblockUser(w http.ResponseWriter, r *http.Request, userID UserId, params UnblockUserParams) {
	if s.account == nil {
		s.AuthServer.UnblockUser(w, r, userID, params)
		return
	}
	s.account.UnblockUser(w, r, userID, params)
}

// GetUser delegates to the account endpoints.
func (s *Server) GetUser(w http.ResponseWriter, r *http.Request, userID UserId, params GetUserParams) {
	if s.account == nil {
		s.AuthServer.GetUser(w, r, userID, params)
		return
	}
	s.account.GetUser(w, r, userID, params)
}

// SetContactName delegates to the account endpoints.
func (s *Server) SetContactName(w http.ResponseWriter, r *http.Request, userID UserId, params SetContactNameParams) {
	if s.account == nil {
		s.AuthServer.SetContactName(w, r, userID, params)
		return
	}
	s.account.SetContactName(w, r, userID, params)
}

// RemoveContactName delegates to the account endpoints.
func (s *Server) RemoveContactName(w http.ResponseWriter, r *http.Request, userID UserId, params RemoveContactNameParams) {
	if s.account == nil {
		s.AuthServer.RemoveContactName(w, r, userID, params)
		return
	}
	s.account.RemoveContactName(w, r, userID, params)
}

// GetUserByUsername delegates to the account endpoints.
func (s *Server) GetUserByUsername(w http.ResponseWriter, r *http.Request, username Username, params GetUserByUsernameParams) {
	if s.account == nil {
		s.AuthServer.GetUserByUsername(w, r, username, params)
		return
	}
	s.account.GetUserByUsername(w, r, username, params)
}

// ReplacePrivacyExceptions delegates to the account endpoints.
func (s *Server) ReplacePrivacyExceptions(w http.ResponseWriter, r *http.Request, settingKey, effect string, params ReplacePrivacyExceptionsParams) {
	if s.account == nil {
		s.AuthServer.ReplacePrivacyExceptions(w, r, settingKey, effect, params)
		return
	}
	s.account.ReplacePrivacyExceptions(w, r, settingKey, effect, params)
}

// ListChats delegates to the chat endpoints (GET /chats).
func (s *Server) ListChats(w http.ResponseWriter, r *http.Request, params ListChatsParams) {
	if s.chats == nil {
		s.AuthServer.ListChats(w, r, params)
		return
	}
	s.chats.ListChats(w, r, params)
}

// CreateChat delegates to the chat endpoints (POST /chats).
func (s *Server) CreateChat(w http.ResponseWriter, r *http.Request, params CreateChatParams) {
	if s.chats == nil {
		s.AuthServer.CreateChat(w, r, params)
		return
	}
	s.chats.CreateChat(w, r, params)
}

// GetChat delegates to the chat endpoints (GET /chats/{id}).
func (s *Server) GetChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params GetChatParams) {
	if s.chats == nil {
		s.AuthServer.GetChat(w, r, chatID, params)
		return
	}
	s.chats.GetChat(w, r, chatID, params)
}

// DeleteChat delegates to the chat endpoints (DELETE /chats/{id}).
func (s *Server) DeleteChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params DeleteChatParams) {
	if s.chats == nil {
		s.AuthServer.DeleteChat(w, r, chatID, params)
		return
	}
	s.chats.DeleteChat(w, r, chatID, params)
}

// PinChat delegates to the chat endpoints (PUT /chats/{id}/pin).
func (s *Server) PinChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params PinChatParams) {
	if s.chats == nil {
		s.AuthServer.PinChat(w, r, chatID, params)
		return
	}
	s.chats.PinChat(w, r, chatID, params)
}

// UnpinChat delegates to the chat endpoints (DELETE /chats/{id}/pin).
func (s *Server) UnpinChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params UnpinChatParams) {
	if s.chats == nil {
		s.AuthServer.UnpinChat(w, r, chatID, params)
		return
	}
	s.chats.UnpinChat(w, r, chatID, params)
}

// MarkChatRead delegates to the chat endpoints (POST /chats/{id}/read).
func (s *Server) MarkChatRead(w http.ResponseWriter, r *http.Request, chatID ChatId, params MarkChatReadParams) {
	if s.chats == nil {
		s.AuthServer.MarkChatRead(w, r, chatID, params)
		return
	}
	s.chats.MarkChatRead(w, r, chatID, params)
}

// ListApprovalRequests delegates to the chat endpoints (GET /requests).
func (s *Server) ListApprovalRequests(w http.ResponseWriter, r *http.Request, params ListApprovalRequestsParams) {
	if s.chats == nil {
		s.AuthServer.ListApprovalRequests(w, r, params)
		return
	}
	s.chats.ListApprovalRequests(w, r, params)
}

// ApproveRequest delegates to the chat endpoints (POST /requests/{id}/approve).
func (s *Server) ApproveRequest(w http.ResponseWriter, r *http.Request, requestID RequestId, params ApproveRequestParams) {
	if s.chats == nil {
		s.AuthServer.ApproveRequest(w, r, requestID, params)
		return
	}
	s.chats.ApproveRequest(w, r, requestID, params)
}

// RejectRequest delegates to the chat endpoints (POST /requests/{id}/reject).
func (s *Server) RejectRequest(w http.ResponseWriter, r *http.Request, requestID RequestId, params RejectRequestParams) {
	if s.chats == nil {
		s.AuthServer.RejectRequest(w, r, requestID, params)
		return
	}
	s.chats.RejectRequest(w, r, requestID, params)
}
