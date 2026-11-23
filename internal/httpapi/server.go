package httpapi

import (
	"net/http"

	"github.com/myronsi/messenger-back/internal/moderation"

	"github.com/google/uuid"
)

// Server serves the whole contract by delegating to the feature servers: authentication, sessions and the
// account (AuthServer), uploads, downloads and avatars (MediaServer). Operations of features that are not
// implemented yet answer 501 through the AuthServer's embedded Unimplemented.
type Server struct {
	*AuthServer
	media         *MediaServer
	account       *AccountServer
	chats         *ChatServer
	messages      *MessageServer
	linkPreviews  *LinkPreviewServer
	meta          MetaInfo
	notifications *NotificationOptions
	calls         *CallServer
	stickers      *StickerServer
	emoji         *EmojiServer
	connections   *ConnectionServer
	moderation    *ModerationServer
	public        *PublicServer
}

// WithMessages adds history, sending, editing, deleting and forwarding over HTTP, and the media lists.
func (s *Server) WithMessages(m *MessageServer) *Server { s.messages = m; return s }

func (s *Server) WithLinkPreviews(l *LinkPreviewServer) *Server { s.linkPreviews = l; return s }

func (s *Server) WithStickers(st *StickerServer) *Server { s.stickers = st; return s }

func (s *Server) WithEmoji(e *EmojiServer) *Server { s.emoji = e; return s }

func (s *Server) ListStickerPacks(w http.ResponseWriter, r *http.Request, params ListStickerPacksParams) {
	if s.stickers == nil {
		s.AuthServer.ListStickerPacks(w, r, params)
		return
	}
	s.stickers.ListStickerPacks(w, r, params)
}

func (s *Server) GetStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params GetStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.GetStickerPack(w, r, packID, params)
		return
	}
	s.stickers.GetStickerPack(w, r, packID, params)
}

func (s *Server) AddStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params AddStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.AddStickerPack(w, r, packID, params)
		return
	}
	s.stickers.AddStickerPack(w, r, packID, params)
}

func (s *Server) RemoveStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params RemoveStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.RemoveStickerPack(w, r, packID, params)
		return
	}
	s.stickers.RemoveStickerPack(w, r, packID, params)
}

func (s *Server) GetStickerPackByLink(w http.ResponseWriter, r *http.Request, slug StickerPackSlug, params GetStickerPackByLinkParams) {
	if s.stickers == nil {
		s.AuthServer.GetStickerPackByLink(w, r, slug, params)
		return
	}
	s.stickers.GetStickerPackByLink(w, r, slug, params)
}

func (s *Server) CreateStickerPack(w http.ResponseWriter, r *http.Request, params CreateStickerPackParams) {
	if !s.allowed(w, r, moderation.PublishPack) {
		return
	}
	if s.stickers == nil {
		s.AuthServer.CreateStickerPack(w, r, params)
		return
	}
	s.stickers.CreateStickerPack(w, r, params)
}

func (s *Server) UpdateStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params UpdateStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.UpdateStickerPack(w, r, packID, params)
		return
	}
	s.stickers.UpdateStickerPack(w, r, packID, params)
}

func (s *Server) DeleteStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params DeleteStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.DeleteStickerPack(w, r, packID, params)
		return
	}
	s.stickers.DeleteStickerPack(w, r, packID, params)
}

func (s *Server) AddSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, params AddStickerParams) {
	if !s.allowed(w, r, moderation.PublishPack) {
		return
	}
	if s.stickers == nil {
		s.AuthServer.AddSticker(w, r, packID, params)
		return
	}
	s.stickers.AddSticker(w, r, packID, params)
}

func (s *Server) UpdateSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, stickerID StickerId, params UpdateStickerParams) {
	if s.stickers == nil {
		s.AuthServer.UpdateSticker(w, r, packID, stickerID, params)
		return
	}
	s.stickers.UpdateSticker(w, r, packID, stickerID, params)
}

func (s *Server) DeleteSticker(w http.ResponseWriter, r *http.Request, packID StickerPackId, stickerID StickerId, params DeleteStickerParams) {
	if s.stickers == nil {
		s.AuthServer.DeleteSticker(w, r, packID, stickerID, params)
		return
	}
	s.stickers.DeleteSticker(w, r, packID, stickerID, params)
}

func (s *Server) ReorderStickers(w http.ResponseWriter, r *http.Request, packID StickerPackId, params ReorderStickersParams) {
	if s.stickers == nil {
		s.AuthServer.ReorderStickers(w, r, packID, params)
		return
	}
	s.stickers.ReorderStickers(w, r, packID, params)
}

func (s *Server) ReportStickerPack(w http.ResponseWriter, r *http.Request, packID StickerPackId, params ReportStickerPackParams) {
	if s.stickers == nil {
		s.AuthServer.ReportStickerPack(w, r, packID, params)
		return
	}
	s.stickers.ReportStickerPack(w, r, packID, params)
}

func (s *Server) ListRecentStickers(w http.ResponseWriter, r *http.Request, params ListRecentStickersParams) {
	if s.stickers == nil {
		s.AuthServer.ListRecentStickers(w, r, params)
		return
	}
	s.stickers.ListRecentStickers(w, r, params)
}

func (s *Server) GetLinkPreview(w http.ResponseWriter, r *http.Request, params GetLinkPreviewParams) {
	if s.linkPreviews == nil {
		s.AuthServer.GetLinkPreview(w, r, params)
		return
	}
	s.linkPreviews.GetLinkPreview(w, r, params)
}

func (s *Server) GetLinkPreviewImage(w http.ResponseWriter, r *http.Request, params GetLinkPreviewImageParams) {
	if s.linkPreviews == nil {
		s.AuthServer.GetLinkPreviewImage(w, r, params)
		return
	}
	s.linkPreviews.GetLinkPreviewImage(w, r, params)
}

// WithChats adds the chat list, direct chat, pin, read and request endpoints.
func (s *Server) WithChats(c *ChatServer) *Server { s.chats = c; return s }

// WithAccount adds the profile, privacy, blocking and user endpoints.
func (s *Server) WithAccount(a *AccountServer) *Server { s.account = a; return s }

// WithMeta sets what GET /meta reports.
func (s *Server) WithMeta(m MetaInfo) *Server {
	if m.ConnectionProviders == nil {
		m.ConnectionProviders = s.meta.ConnectionProviders
	}
	s.meta = m
	return s
}

// GetMeta implements GET /meta.
func (s *Server) GetMeta(w http.ResponseWriter, _ *http.Request) { s.meta.serve(w) }

var _ ServerInterface = (*Server)(nil)

// NewServer combines the feature servers. media may be nil (its operations then answer 501).
func NewServer(a *AuthServer, media *MediaServer) *Server {
	return &Server{AuthServer: a, media: media}
}

// UploadAttachment implements POST /attachments.
func (s *Server) UploadAttachment(w http.ResponseWriter, r *http.Request, params UploadAttachmentParams) {
	if !s.allowed(w, r, moderation.Upload) {
		return
	}
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
	if !s.allowed(w, r, moderation.EditProfile) {
		return
	}
	if s.media == nil {
		s.AuthServer.SetMyAvatar(w, r, params)
		return
	}
	s.media.SetMyAvatar(w, r, params)
}

func (s *Server) DeleteMyAvatar(w http.ResponseWriter, r *http.Request, avatarID Id, params DeleteMyAvatarParams) {
	if s.media == nil {
		s.AuthServer.DeleteMyAvatar(w, r, avatarID, params)
		return
	}
	s.media.DeleteMyAvatar(w, r, avatarID, params)
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
func (s *Server) SetMyMusic(w http.ResponseWriter, r *http.Request, params SetMyMusicParams) {
	if !s.allowed(w, r, moderation.EditProfile) {
		return
	}
	if s.account == nil {
		s.AuthServer.SetMyMusic(w, r, params)
		return
	}
	s.account.SetMyMusic(w, r, params)
}

func (s *Server) DeleteMyMusic(w http.ResponseWriter, r *http.Request, params DeleteMyMusicParams) {
	if s.account == nil {
		s.AuthServer.DeleteMyMusic(w, r, params)
		return
	}
	s.account.DeleteMyMusic(w, r, params)
}

func (s *Server) SetMyBackground(w http.ResponseWriter, r *http.Request, params SetMyBackgroundParams) {
	if !s.allowed(w, r, moderation.EditProfile) {
		return
	}
	if s.account == nil {
		s.AuthServer.SetMyBackground(w, r, params)
		return
	}
	s.account.SetMyBackground(w, r, params)
}

func (s *Server) DeleteMyBackground(w http.ResponseWriter, r *http.Request, params DeleteMyBackgroundParams) {
	if s.account == nil {
		s.AuthServer.DeleteMyBackground(w, r, params)
		return
	}
	s.account.DeleteMyBackground(w, r, params)
}

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

func (s *Server) ClearChatHistory(w http.ResponseWriter, r *http.Request, chatID ChatId, params ClearChatHistoryParams) {
	if s.chats == nil {
		s.AuthServer.ClearChatHistory(w, r, chatID, params)
		return
	}
	s.chats.ClearChatHistory(w, r, chatID, params)
}

// ArchiveChat delegates to the chat endpoints (PUT /chats/{id}/archive).
func (s *Server) ArchiveChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params ArchiveChatParams) {
	if s.chats == nil {
		s.AuthServer.ArchiveChat(w, r, chatID, params)
		return
	}
	s.chats.ArchiveChat(w, r, chatID, params)
}

// UnarchiveChat delegates to the chat endpoints (DELETE /chats/{id}/archive).
func (s *Server) UnarchiveChat(w http.ResponseWriter, r *http.Request, chatID ChatId, params UnarchiveChatParams) {
	if s.chats == nil {
		s.AuthServer.UnarchiveChat(w, r, chatID, params)
		return
	}
	s.chats.UnarchiveChat(w, r, chatID, params)
}

// ReorderPinnedChats delegates to the chat endpoints (PUT /chats/pins/order).
func (s *Server) ReorderPinnedChats(w http.ResponseWriter, r *http.Request, params ReorderPinnedChatsParams) {
	if s.chats == nil {
		s.AuthServer.ReorderPinnedChats(w, r, params)
		return
	}
	s.chats.ReorderPinnedChats(w, r, params)
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

// ListMessages delegates to the message endpoints (GET /chats/{id}/messages).
func (s *Server) ListMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListMessagesParams) {
	if s.messages == nil {
		s.AuthServer.ListMessages(w, r, chatID, params)
		return
	}
	s.messages.ListMessages(w, r, chatID, params)
}

// SendMessage delegates to the message endpoints (POST /chats/{id}/messages).
func (s *Server) SendMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, params SendMessageParams) {
	if s.messages == nil {
		s.AuthServer.SendMessage(w, r, chatID, params)
		return
	}
	s.messages.SendMessage(w, r, chatID, params)
}

func (s *Server) PinMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, params PinMessageParams) {
	if s.messages == nil {
		s.AuthServer.PinMessage(w, r, chatID, messageID, params)
		return
	}
	s.messages.PinMessage(w, r, chatID, messageID, params)
}

func (s *Server) UnpinMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, messageID MessageId, params UnpinMessageParams) {
	if s.messages == nil {
		s.AuthServer.UnpinMessage(w, r, chatID, messageID, params)
		return
	}
	s.messages.UnpinMessage(w, r, chatID, messageID, params)
}

func (s *Server) ListPinnedMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListPinnedMessagesParams) {
	if s.messages == nil {
		s.AuthServer.ListPinnedMessages(w, r, chatID, params)
		return
	}
	s.messages.ListPinnedMessages(w, r, chatID, params)
}

// ListChatMedia delegates to the message endpoints (GET /chats/{id}/media).
func (s *Server) ListChatMedia(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListChatMediaParams) {
	if s.messages == nil {
		s.AuthServer.ListChatMedia(w, r, chatID, params)
		return
	}
	s.messages.ListChatMedia(w, r, chatID, params)
}

// ListUnsentMessages delegates to the message endpoints (GET /chats/{id}/unsent).
func (s *Server) ListUnsentMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListUnsentMessagesParams) {
	if s.messages == nil {
		s.AuthServer.ListUnsentMessages(w, r, chatID, params)
		return
	}
	s.messages.ListUnsentMessages(w, r, chatID, params)
}

// DeleteUnsentMessage delegates to the message endpoints (DELETE /chats/{id}/unsent/{client_temp_id}).
func (s *Server) DeleteUnsentMessage(w http.ResponseWriter, r *http.Request, chatID ChatId, clientTempID ClientTempId, params DeleteUnsentMessageParams) {
	if s.messages == nil {
		s.AuthServer.DeleteUnsentMessage(w, r, chatID, clientTempID, params)
		return
	}
	s.messages.DeleteUnsentMessage(w, r, chatID, clientTempID, params)
}

// EditMessage delegates to the message endpoints (PATCH /messages/{id}).
func (s *Server) EditMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, params EditMessageParams) {
	if s.messages == nil {
		s.AuthServer.EditMessage(w, r, messageID, params)
		return
	}
	s.messages.EditMessage(w, r, messageID, params)
}

// DeleteMessage delegates to the message endpoints (DELETE /messages/{id}).
func (s *Server) DeleteMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, params DeleteMessageParams) {
	if s.messages == nil {
		s.AuthServer.DeleteMessage(w, r, messageID, params)
		return
	}
	s.messages.DeleteMessage(w, r, messageID, params)
}

// ForwardMessage delegates to the message endpoints (POST /messages/{id}/forward).
func (s *Server) ForwardMessage(w http.ResponseWriter, r *http.Request, messageID MessageId, params ForwardMessageParams) {
	if s.messages == nil {
		s.AuthServer.ForwardMessage(w, r, messageID, params)
		return
	}
	s.messages.ForwardMessage(w, r, messageID, params)
}

func (s *Server) ForwardMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params ForwardMessagesParams) {
	if s.messages == nil {
		s.AuthServer.ForwardMessages(w, r, chatID, params)
		return
	}
	s.messages.ForwardMessages(w, r, chatID, params)
}

func (s *Server) DeleteMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params DeleteMessagesParams) {
	if s.messages == nil {
		s.AuthServer.DeleteMessages(w, r, chatID, params)
		return
	}
	s.messages.DeleteMessages(w, r, chatID, params)
}

// ListGroups delegates to the group endpoints.
func (s *Server) ListGroups(w http.ResponseWriter, r *http.Request, params ListGroupsParams) {
	if s.chats == nil {
		s.AuthServer.ListGroups(w, r, params)
		return
	}
	s.chats.ListGroups(w, r, params)
}

// CreateGroup delegates to the group endpoints.
func (s *Server) CreateGroup(w http.ResponseWriter, r *http.Request, params CreateGroupParams) {
	if s.chats == nil {
		s.AuthServer.CreateGroup(w, r, params)
		return
	}
	s.chats.CreateGroup(w, r, params)
}

// GetGroup delegates to the group endpoints.
func (s *Server) GetGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, params GetGroupParams) {
	if s.chats == nil {
		s.AuthServer.GetGroup(w, r, chatID, params)
		return
	}
	s.chats.GetGroup(w, r, chatID, params)
}

func (s *Server) ListGroupMembers(w http.ResponseWriter, r *http.Request, chatID ChatId, params ListGroupMembersParams) {
	if s.chats == nil {
		s.AuthServer.ListGroupMembers(w, r, chatID, params)
		return
	}
	s.chats.ListGroupMembers(w, r, chatID, params)
}

func (s *Server) ListCommonGroups(w http.ResponseWriter, r *http.Request, userID Id, params ListCommonGroupsParams) {
	if s.chats == nil {
		s.AuthServer.ListCommonGroups(w, r, userID, params)
		return
	}
	s.chats.ListCommonGroups(w, r, userID, params)
}

// UpdateGroup delegates to the group endpoints.
func (s *Server) UpdateGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, params UpdateGroupParams) {
	if s.chats == nil {
		s.AuthServer.UpdateGroup(w, r, chatID, params)
		return
	}
	s.chats.UpdateGroup(w, r, chatID, params)
}

// DeleteGroup delegates to the group endpoints.
func (s *Server) DeleteGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, params DeleteGroupParams) {
	if s.chats == nil {
		s.AuthServer.DeleteGroup(w, r, chatID, params)
		return
	}
	s.chats.DeleteGroup(w, r, chatID, params)
}

// SetGroupAvatar delegates to the group endpoints.
func (s *Server) SetGroupBackground(w http.ResponseWriter, r *http.Request, chatID ChatId, params SetGroupBackgroundParams) {
	if s.chats == nil {
		s.AuthServer.SetGroupBackground(w, r, chatID, params)
		return
	}
	s.chats.SetGroupBackground(w, r, chatID, params)
}

func (s *Server) DeleteGroupBackground(w http.ResponseWriter, r *http.Request, chatID ChatId, params DeleteGroupBackgroundParams) {
	if s.chats == nil {
		s.AuthServer.DeleteGroupBackground(w, r, chatID, params)
		return
	}
	s.chats.DeleteGroupBackground(w, r, chatID, params)
}

func (s *Server) SetGroupAvatar(w http.ResponseWriter, r *http.Request, chatID ChatId, params SetGroupAvatarParams) {
	if s.chats == nil {
		s.AuthServer.SetGroupAvatar(w, r, chatID, params)
		return
	}
	s.chats.SetGroupAvatar(w, r, chatID, params)
}

func (s *Server) GetGroupRoleLog(w http.ResponseWriter, r *http.Request, chatID ChatId, params GetGroupRoleLogParams) {
	if s.chats == nil {
		s.AuthServer.GetGroupRoleLog(w, r, chatID, params)
		return
	}
	s.chats.GetGroupRoleLog(w, r, chatID, params)
}

// LeaveGroup delegates to the group endpoints.
func (s *Server) LeaveGroup(w http.ResponseWriter, r *http.Request, chatID ChatId, params LeaveGroupParams) {
	if s.chats == nil {
		s.AuthServer.LeaveGroup(w, r, chatID, params)
		return
	}
	s.chats.LeaveGroup(w, r, chatID, params)
}

// AddGroupParticipant delegates to the group endpoints.
func (s *Server) AddGroupParticipant(w http.ResponseWriter, r *http.Request, chatID ChatId, params AddGroupParticipantParams) {
	if s.chats == nil {
		s.AuthServer.AddGroupParticipant(w, r, chatID, params)
		return
	}
	s.chats.AddGroupParticipant(w, r, chatID, params)
}

// RemoveGroupParticipant delegates to the group endpoints.
func (s *Server) RemoveGroupParticipant(w http.ResponseWriter, r *http.Request, chatID ChatId, userID UserId, params RemoveGroupParticipantParams) {
	if s.chats == nil {
		s.AuthServer.RemoveGroupParticipant(w, r, chatID, userID, params)
		return
	}
	s.chats.RemoveGroupParticipant(w, r, chatID, userID, params)
}

// UpdateGroupParticipantRole delegates to the group endpoints.
func (s *Server) UpdateGroupParticipantRole(w http.ResponseWriter, r *http.Request, chatID ChatId, userID UserId, params UpdateGroupParticipantRoleParams) {
	if s.chats == nil {
		s.AuthServer.UpdateGroupParticipantRole(w, r, chatID, userID, params)
		return
	}
	s.chats.UpdateGroupParticipantRole(w, r, chatID, userID, params)
}

// TransferGroupOwnership delegates to the group endpoints.
func (s *Server) TransferGroupOwnership(w http.ResponseWriter, r *http.Request, chatID ChatId, params TransferGroupOwnershipParams) {
	if s.chats == nil {
		s.AuthServer.TransferGroupOwnership(w, r, chatID, params)
		return
	}
	s.chats.TransferGroupOwnership(w, r, chatID, params)
}

// SearchMessages delegates to the message endpoints.
func (s *Server) SearchMessages(w http.ResponseWriter, r *http.Request, chatID ChatId, params SearchMessagesParams) {
	if s.messages == nil {
		s.AuthServer.SearchMessages(w, r, chatID, params)
		return
	}
	s.messages.SearchMessages(w, r, chatID, params)
}

// SearchAllMessages delegates to the message endpoints.
func (s *Server) SearchAllMessages(w http.ResponseWriter, r *http.Request, params SearchAllMessagesParams) {
	if s.messages == nil {
		s.AuthServer.SearchAllMessages(w, r, params)
		return
	}
	s.messages.SearchAllMessages(w, r, params)
}

func (s *Server) ListEmojiPacks(w http.ResponseWriter, r *http.Request, params ListEmojiPacksParams) {
	if s.emoji == nil {
		s.AuthServer.ListEmojiPacks(w, r, params)
		return
	}
	s.emoji.ListEmojiPacks(w, r, params)
}

func (s *Server) CreateEmojiPack(w http.ResponseWriter, r *http.Request, params CreateEmojiPackParams) {
	if !s.allowed(w, r, moderation.PublishPack) {
		return
	}
	if s.emoji == nil {
		s.AuthServer.CreateEmojiPack(w, r, params)
		return
	}
	s.emoji.CreateEmojiPack(w, r, params)
}

func (s *Server) GetEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params GetEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.GetEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.GetEmojiPack(w, r, packId, params)
}

func (s *Server) UpdateEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params UpdateEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.UpdateEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.UpdateEmojiPack(w, r, packId, params)
}

func (s *Server) DeleteEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params DeleteEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.DeleteEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.DeleteEmojiPack(w, r, packId, params)
}

func (s *Server) AddEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params AddEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.AddEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.AddEmojiPack(w, r, packId, params)
}

func (s *Server) RemoveEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params RemoveEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.RemoveEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.RemoveEmojiPack(w, r, packId, params)
}

func (s *Server) AddCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params AddCustomEmojiParams) {
	if !s.allowed(w, r, moderation.PublishPack) {
		return
	}
	if s.emoji == nil {
		s.AuthServer.AddCustomEmoji(w, r, packId, params)
		return
	}
	s.emoji.AddCustomEmoji(w, r, packId, params)
}

func (s *Server) UpdateCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, emojiId EmojiId, params UpdateCustomEmojiParams) {
	if s.emoji == nil {
		s.AuthServer.UpdateCustomEmoji(w, r, packId, emojiId, params)
		return
	}
	s.emoji.UpdateCustomEmoji(w, r, packId, emojiId, params)
}

func (s *Server) DeleteCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, emojiId EmojiId, params DeleteCustomEmojiParams) {
	if s.emoji == nil {
		s.AuthServer.DeleteCustomEmoji(w, r, packId, emojiId, params)
		return
	}
	s.emoji.DeleteCustomEmoji(w, r, packId, emojiId, params)
}

func (s *Server) ReportEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params ReportEmojiPackParams) {
	if s.emoji == nil {
		s.AuthServer.ReportEmojiPack(w, r, packId, params)
		return
	}
	s.emoji.ReportEmojiPack(w, r, packId, params)
}

func (s *Server) GetEmojiPackByLink(w http.ResponseWriter, r *http.Request, slug StickerPackSlug, params GetEmojiPackByLinkParams) {
	if s.emoji == nil {
		s.AuthServer.GetEmojiPackByLink(w, r, slug, params)
		return
	}
	s.emoji.GetEmojiPackByLink(w, r, slug, params)
}

func (s *Server) GetCustomEmoji(w http.ResponseWriter, r *http.Request, emojiId EmojiId, params GetCustomEmojiParams) {
	if s.emoji == nil {
		s.AuthServer.GetCustomEmoji(w, r, emojiId, params)
		return
	}
	s.emoji.GetCustomEmoji(w, r, emojiId, params)
}

func (s *Server) GetCustomEmojiImage(w http.ResponseWriter, r *http.Request, emojiId EmojiId, params GetCustomEmojiImageParams) {
	if s.emoji == nil {
		s.AuthServer.GetCustomEmojiImage(w, r, emojiId, params)
		return
	}
	s.emoji.GetCustomEmojiImage(w, r, emojiId, params)
}

func (s *Server) GetGroupEmoji(w http.ResponseWriter, r *http.Request, chatId ChatId, params GetGroupEmojiParams) {
	if s.emoji == nil {
		s.AuthServer.GetGroupEmoji(w, r, chatId, params)
		return
	}
	s.emoji.GetGroupEmoji(w, r, chatId, params)
}

func (s *Server) CreateAttachmentPlayback(w http.ResponseWriter, r *http.Request, id AttachmentId, params CreateAttachmentPlaybackParams) {
	if s.media == nil {
		s.AuthServer.CreateAttachmentPlayback(w, r, id, params)
		return
	}
	s.media.CreateAttachmentPlayback(w, r, id, params)
}

func (s *Server) CreateUploadSession(w http.ResponseWriter, r *http.Request, params CreateUploadSessionParams) {
	if !s.allowed(w, r, moderation.Upload) {
		return
	}
	if s.media == nil {
		s.AuthServer.CreateUploadSession(w, r, params)
		return
	}
	s.media.CreateUploadSession(w, r, params)
}

func (s *Server) GetUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params GetUploadSessionParams) {
	if s.media == nil {
		s.AuthServer.GetUploadSession(w, r, id, params)
		return
	}
	s.media.GetUploadSession(w, r, id, params)
}

func (s *Server) DeleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params DeleteUploadSessionParams) {
	if s.media == nil {
		s.AuthServer.DeleteUploadSession(w, r, id, params)
		return
	}
	s.media.DeleteUploadSession(w, r, id, params)
}

func (s *Server) PutUploadChunk(w http.ResponseWriter, r *http.Request, id uuid.UUID, params PutUploadChunkParams) {
	if s.media == nil {
		s.AuthServer.PutUploadChunk(w, r, id, params)
		return
	}
	s.media.PutUploadChunk(w, r, id, params)
}

func (s *Server) CompleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params CompleteUploadSessionParams) {
	if s.media == nil {
		s.AuthServer.CompleteUploadSession(w, r, id, params)
		return
	}
	s.media.CompleteUploadSession(w, r, id, params)
}

func (s *Server) AcceptChatRequest(w http.ResponseWriter, r *http.Request, chatID ChatId, params AcceptChatRequestParams) {
	if s.chats == nil {
		s.AuthServer.AcceptChatRequest(w, r, chatID, params)
		return
	}
	s.chats.AcceptChatRequest(w, r, chatID, params)
}

func (s *Server) CountChatRequests(w http.ResponseWriter, r *http.Request, params CountChatRequestsParams) {
	if s.chats == nil {
		s.AuthServer.CountChatRequests(w, r, params)
		return
	}
	s.chats.CountChatRequests(w, r, params)
}

func (s *Server) ListSyncChanges(w http.ResponseWriter, r *http.Request, params ListSyncChangesParams) {
	if s.messages == nil {
		s.AuthServer.ListSyncChanges(w, r, params)
		return
	}
	s.messages.ListSyncChanges(w, r, params)
}

func (s *Server) GetStorageKey(w http.ResponseWriter, r *http.Request, params GetStorageKeyParams) {
	if s.messages == nil {
		s.AuthServer.GetStorageKey(w, r, params)
		return
	}
	s.messages.GetStorageKey(w, r, params)
}
