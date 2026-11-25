package httpapi

import (
	"net/http"

	"github.com/google/uuid"
)

// Unimplemented answers 501 for every operation of the contract. The server embeds it and
// overrides the methods a feature implements, so the router serves the whole contract from day one
// and a contract change that adds an operation fails to compile until it is handled here.
type Unimplemented struct{}

var _ ServerInterface = Unimplemented{}

func (Unimplemented) GetRecoveryOverview(w http.ResponseWriter, r *http.Request, _ GetRecoveryOverviewParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetRecoveryEmail(w http.ResponseWriter, r *http.Request, _ GetRecoveryEmailParams) {
	notImplemented(w, r)
}
func (Unimplemented) SetRecoveryEmail(w http.ResponseWriter, r *http.Request, _ SetRecoveryEmailParams) {
	notImplemented(w, r)
}
func (Unimplemented) RemoveRecoveryEmail(w http.ResponseWriter, r *http.Request, _ RemoveRecoveryEmailParams) {
	notImplemented(w, r)
}
func (Unimplemented) VerifyRecoveryEmail(w http.ResponseWriter, r *http.Request, _ VerifyRecoveryEmailParams) {
	notImplemented(w, r)
}
func (Unimplemented) RequestEmailReset(w http.ResponseWriter, r *http.Request, _ RequestEmailResetParams) {
	notImplemented(w, r)
}
func (Unimplemented) ConfirmEmailReset(w http.ResponseWriter, r *http.Request, _ ConfirmEmailResetParams) {
	notImplemented(w, r)
}

func (Unimplemented) PasskeyRegistrationOptions(w http.ResponseWriter, r *http.Request, _ PasskeyRegistrationOptionsParams) {
	notImplemented(w, r)
}
func (Unimplemented) VerifyPasskeyRegistration(w http.ResponseWriter, r *http.Request, _ VerifyPasskeyRegistrationParams) {
	notImplemented(w, r)
}
func (Unimplemented) PasskeyLoginOptions(w http.ResponseWriter, r *http.Request, _ PasskeyLoginOptionsParams) {
	notImplemented(w, r)
}
func (Unimplemented) VerifyPasskeyLogin(w http.ResponseWriter, r *http.Request, _ VerifyPasskeyLoginParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListPasskeys(w http.ResponseWriter, r *http.Request, _ ListPasskeysParams) {
	notImplemented(w, r)
}
func (Unimplemented) RenamePasskey(w http.ResponseWriter, r *http.Request, id string, _ RenamePasskeyParams) {
	notImplemented(w, r)
}
func (Unimplemented) RemovePasskey(w http.ResponseWriter, r *http.Request, id string, _ RemovePasskeyParams) {
	notImplemented(w, r)
}

func (Unimplemented) StartDeviceRecovery(w http.ResponseWriter, r *http.Request, params StartDeviceRecoveryParams) {
	notImplemented(w, r)
}
func (Unimplemented) DeviceRecoveryStatus(w http.ResponseWriter, r *http.Request, requestId string, params DeviceRecoveryStatusParams) {
	notImplemented(w, r)
}
func (Unimplemented) ApproveDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, params ApproveDeviceRecoveryParams) {
	notImplemented(w, r)
}
func (Unimplemented) DeviceRecoveryApprovalOptions(w http.ResponseWriter, r *http.Request, requestId string, params DeviceRecoveryApprovalOptionsParams) {
	notImplemented(w, r)
}
func (Unimplemented) DenyDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, params DenyDeviceRecoveryParams) {
	notImplemented(w, r)
}
func (Unimplemented) CompleteDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, params CompleteDeviceRecoveryParams) {
	notImplemented(w, r)
}

func (Unimplemented) CancelDeviceRecovery(w http.ResponseWriter, r *http.Request, requestId string, params CancelDeviceRecoveryParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateQrLogin(w http.ResponseWriter, r *http.Request, _ CreateQrLoginParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetQrLogin(w http.ResponseWriter, r *http.Request, _ string, _ GetQrLoginParams) {
	notImplemented(w, r)
}
func (Unimplemented) PollQrLogin(w http.ResponseWriter, r *http.Request, _ string, _ PollQrLoginParams) {
	notImplemented(w, r)
}
func (Unimplemented) ApproveQrLogin(w http.ResponseWriter, r *http.Request, _ string, _ ApproveQrLoginParams) {
	notImplemented(w, r)
}
func (Unimplemented) DenyQrLogin(w http.ResponseWriter, r *http.Request, _ string, _ DenyQrLoginParams) {
	notImplemented(w, r)
}

func (Unimplemented) UploadAttachment(w http.ResponseWriter, r *http.Request, params UploadAttachmentParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetAttachmentContent(w http.ResponseWriter, r *http.Request, attachmentId AttachmentId, params GetAttachmentContentParams) {
	notImplemented(w, r)
}

func (Unimplemented) Login(w http.ResponseWriter, r *http.Request, params LoginParams) {
	notImplemented(w, r)
}

func (Unimplemented) LoginTwoFactor(w http.ResponseWriter, r *http.Request, params LoginTwoFactorParams) {
	notImplemented(w, r)
}

func (Unimplemented) Logout(w http.ResponseWriter, r *http.Request, params LogoutParams) {
	notImplemented(w, r)
}

func (Unimplemented) RefreshToken(w http.ResponseWriter, r *http.Request, params RefreshTokenParams) {
	notImplemented(w, r)
}

func (Unimplemented) Register(w http.ResponseWriter, r *http.Request, params RegisterParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListChats(w http.ResponseWriter, r *http.Request, params ListChatsParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateChat(w http.ResponseWriter, r *http.Request, params CreateChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params DeleteChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params GetChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListChatMedia(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListChatMediaParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListUnsentMessages(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListUnsentMessagesParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteUnsentMessage(w http.ResponseWriter, r *http.Request, chatId ChatId, clientTempId ClientTempId, params DeleteUnsentMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListMessages(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListMessagesParams) {
	notImplemented(w, r)
}

func (Unimplemented) SendMessage(w http.ResponseWriter, r *http.Request, chatId ChatId, params SendMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) SearchMessages(w http.ResponseWriter, r *http.Request, chatId ChatId, params SearchMessagesParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnpinChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params UnpinChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) PinChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params PinChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) ArchiveChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params ArchiveChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) ClearChatHistory(w http.ResponseWriter, r *http.Request, chatId ChatId, params ClearChatHistoryParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnarchiveChat(w http.ResponseWriter, r *http.Request, chatId ChatId, params UnarchiveChatParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReorderPinnedChats(w http.ResponseWriter, r *http.Request, params ReorderPinnedChatsParams) {
	notImplemented(w, r)
}

func (Unimplemented) MarkChatRead(w http.ResponseWriter, r *http.Request, chatId ChatId, params MarkChatReadParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListGroups(w http.ResponseWriter, r *http.Request, params ListGroupsParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateGroup(w http.ResponseWriter, r *http.Request, params CreateGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteGroup(w http.ResponseWriter, r *http.Request, chatId ChatId, params DeleteGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetGroup(w http.ResponseWriter, r *http.Request, chatId ChatId, params GetGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListGroupMembers(w http.ResponseWriter, r *http.Request, chatId ChatId, params ListGroupMembersParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListCommonGroups(w http.ResponseWriter, r *http.Request, userId Id, params ListCommonGroupsParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateGroup(w http.ResponseWriter, r *http.Request, chatId ChatId, params UpdateGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetGroupBackground(w http.ResponseWriter, r *http.Request, chatId ChatId, params SetGroupBackgroundParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteGroupBackground(w http.ResponseWriter, r *http.Request, chatId ChatId, params DeleteGroupBackgroundParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetMyMusic(w http.ResponseWriter, r *http.Request, params SetMyMusicParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMyMusic(w http.ResponseWriter, r *http.Request, params DeleteMyMusicParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetMyBackground(w http.ResponseWriter, r *http.Request, params SetMyBackgroundParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMyBackground(w http.ResponseWriter, r *http.Request, params DeleteMyBackgroundParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetGroupAvatar(w http.ResponseWriter, r *http.Request, chatId ChatId, params SetGroupAvatarParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetLinkPreview(w http.ResponseWriter, r *http.Request, params GetLinkPreviewParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetLinkPreviewImage(w http.ResponseWriter, r *http.Request, params GetLinkPreviewImageParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetGroupRoleLog(w http.ResponseWriter, r *http.Request, chatId ChatId, params GetGroupRoleLogParams) {
	notImplemented(w, r)
}

func (Unimplemented) LeaveGroup(w http.ResponseWriter, r *http.Request, chatId ChatId, params LeaveGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) AddGroupParticipant(w http.ResponseWriter, r *http.Request, chatId ChatId, params AddGroupParticipantParams) {
	notImplemented(w, r)
}

func (Unimplemented) RemoveGroupParticipant(w http.ResponseWriter, r *http.Request, chatId ChatId, userId UserId, params RemoveGroupParticipantParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateGroupParticipantRole(w http.ResponseWriter, r *http.Request, chatId ChatId, userId UserId, params UpdateGroupParticipantRoleParams) {
	notImplemented(w, r)
}

func (Unimplemented) TransferGroupOwnership(w http.ResponseWriter, r *http.Request, chatId ChatId, params TransferGroupOwnershipParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMe(w http.ResponseWriter, r *http.Request, params DeleteMeParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetMe(w http.ResponseWriter, r *http.Request, params GetMeParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateMe(w http.ResponseWriter, r *http.Request, params UpdateMeParams) {
	notImplemented(w, r)
}

func (Unimplemented) ConfirmTwoFactor(w http.ResponseWriter, r *http.Request, params ConfirmTwoFactorParams) {
	notImplemented(w, r)
}

func (Unimplemented) DisableTwoFactor(w http.ResponseWriter, r *http.Request, params DisableTwoFactorParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetupTwoFactor(w http.ResponseWriter, r *http.Request, params SetupTwoFactorParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetMyAvatar(w http.ResponseWriter, r *http.Request, params SetMyAvatarParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMyAvatar(w http.ResponseWriter, r *http.Request, avatarID Id, params DeleteMyAvatarParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListBlockedUsers(w http.ResponseWriter, r *http.Request, params ListBlockedUsersParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnblockUser(w http.ResponseWriter, r *http.Request, userId UserId, params UnblockUserParams) {
	notImplemented(w, r)
}

func (Unimplemented) BlockUser(w http.ResponseWriter, r *http.Request, userId UserId, params BlockUserParams) {
	notImplemented(w, r)
}

func (Unimplemented) ChangePassword(w http.ResponseWriter, r *http.Request, params ChangePasswordParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetPrivacySettings(w http.ResponseWriter, r *http.Request, params GetPrivacySettingsParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdatePrivacySettings(w http.ResponseWriter, r *http.Request, params UpdatePrivacySettingsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReplacePrivacyExceptions(w http.ResponseWriter, r *http.Request, settingKey string, effect string, params ReplacePrivacyExceptionsParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetSecuritySettings(w http.ResponseWriter, r *http.Request, params GetSecuritySettingsParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateSecuritySettings(w http.ResponseWriter, r *http.Request, params UpdateSecuritySettingsParams) {
	notImplemented(w, r)
}

func (Unimplemented) RevokeOtherSessions(w http.ResponseWriter, r *http.Request, params RevokeOtherSessionsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListSessions(w http.ResponseWriter, r *http.Request, params ListSessionsParams) {
	notImplemented(w, r)
}

func (Unimplemented) RevokeSession(w http.ResponseWriter, r *http.Request, sessionId SessionId, params RevokeSessionParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMessage(w http.ResponseWriter, r *http.Request, messageId MessageId, params DeleteMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) EditMessage(w http.ResponseWriter, r *http.Request, messageId MessageId, params EditMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) ForwardMessage(w http.ResponseWriter, r *http.Request, messageId MessageId, params ForwardMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) ForwardMessages(w http.ResponseWriter, r *http.Request, chatId ChatId, params ForwardMessagesParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMessages(w http.ResponseWriter, r *http.Request, chatId ChatId, params DeleteMessagesParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetMeta(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}

func (Unimplemented) ListApprovalRequests(w http.ResponseWriter, r *http.Request, params ListApprovalRequestsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ApproveRequest(w http.ResponseWriter, r *http.Request, requestId RequestId, params ApproveRequestParams) {
	notImplemented(w, r)
}

func (Unimplemented) RejectRequest(w http.ResponseWriter, r *http.Request, requestId RequestId, params RejectRequestParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetUserByUsername(w http.ResponseWriter, r *http.Request, username Username, params GetUserByUsernameParams) {
	notImplemented(w, r)
}

func (Unimplemented) SearchUsers(w http.ResponseWriter, r *http.Request, params SearchUsersParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetUser(w http.ResponseWriter, r *http.Request, userId UserId, params GetUserParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetUserAvatar(w http.ResponseWriter, r *http.Request, userId UserId, params GetUserAvatarParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListUserAvatars(w http.ResponseWriter, r *http.Request, userId UserId, params ListUserAvatarsParams) {
	notImplemented(w, r)
}

func (Unimplemented) RemoveContactName(w http.ResponseWriter, r *http.Request, userId UserId, params RemoveContactNameParams) {
	notImplemented(w, r)
}

func (Unimplemented) SetContactName(w http.ResponseWriter, r *http.Request, userId UserId, params SetContactNameParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateWebSocketTicket(w http.ResponseWriter, r *http.Request, params CreateWebSocketTicketParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetRecoveryKey(w http.ResponseWriter, r *http.Request, _ GetRecoveryKeyParams) {
	notImplemented(w, r)
}
func (Unimplemented) ReplaceRecoveryKey(w http.ResponseWriter, r *http.Request, _ ReplaceRecoveryKeyParams) {
	notImplemented(w, r)
}
func (Unimplemented) RecoverWithKey(w http.ResponseWriter, r *http.Request, _ RecoverWithKeyParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListSecurityEvents(w http.ResponseWriter, r *http.Request, _ ListSecurityEventsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListStickerPacks(w http.ResponseWriter, r *http.Request, _ ListStickerPacksParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ GetStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) AddStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ AddStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) RemoveStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ RemoveStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListRecentStickers(w http.ResponseWriter, r *http.Request, _ ListRecentStickersParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetStickerPackByLink(w http.ResponseWriter, r *http.Request, _ StickerPackSlug, _ GetStickerPackByLinkParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateStickerPack(w http.ResponseWriter, r *http.Request, _ CreateStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ UpdateStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ DeleteStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) AddSticker(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ AddStickerParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateSticker(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ StickerId, _ UpdateStickerParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteSticker(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ StickerId, _ DeleteStickerParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReorderStickers(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ ReorderStickersParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReportStickerPack(w http.ResponseWriter, r *http.Request, _ StickerPackId, _ ReportStickerPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListEmojiPacks(w http.ResponseWriter, r *http.Request, params ListEmojiPacksParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateEmojiPack(w http.ResponseWriter, r *http.Request, params CreateEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params GetEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params UpdateEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params DeleteEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) AddEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params AddEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) RemoveEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params RemoveEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) AddCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params AddCustomEmojiParams) {
	notImplemented(w, r)
}

func (Unimplemented) UpdateCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, emojiId EmojiId, params UpdateCustomEmojiParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteCustomEmoji(w http.ResponseWriter, r *http.Request, packId EmojiPackId, emojiId EmojiId, params DeleteCustomEmojiParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReportEmojiPack(w http.ResponseWriter, r *http.Request, packId EmojiPackId, params ReportEmojiPackParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetEmojiPackByLink(w http.ResponseWriter, r *http.Request, slug StickerPackSlug, params GetEmojiPackByLinkParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetCustomEmoji(w http.ResponseWriter, r *http.Request, emojiId EmojiId, params GetCustomEmojiParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetCustomEmojiImage(w http.ResponseWriter, r *http.Request, emojiId EmojiId, params GetCustomEmojiImageParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetGroupEmoji(w http.ResponseWriter, r *http.Request, chatId ChatId, params GetGroupEmojiParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateAttachmentPlayback(w http.ResponseWriter, r *http.Request, id AttachmentId, params CreateAttachmentPlaybackParams) {
	notImplemented(w, r)
}

func (Unimplemented) CreateUploadSession(w http.ResponseWriter, r *http.Request, params CreateUploadSessionParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params GetUploadSessionParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params DeleteUploadSessionParams) {
	notImplemented(w, r)
}

func (Unimplemented) PutUploadChunk(w http.ResponseWriter, r *http.Request, id uuid.UUID, params PutUploadChunkParams) {
	notImplemented(w, r)
}

func (Unimplemented) CompleteUploadSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, params CompleteUploadSessionParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReportUser(w http.ResponseWriter, r *http.Request, _ UserId, _ ReportUserParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnreportUser(w http.ResponseWriter, r *http.Request, _ UserId, _ UnreportUserParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReportMessage(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ ReportMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnreportMessage(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ UnreportMessageParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReportGroup(w http.ResponseWriter, r *http.Request, _ ChatId, _ ReportGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) UnreportGroup(w http.ResponseWriter, r *http.Request, _ ChatId, _ UnreportGroupParams) {
	notImplemented(w, r)
}

func (Unimplemented) GetAccountStatus(w http.ResponseWriter, r *http.Request, _ GetAccountStatusParams) {
	notImplemented(w, r)
}

func (Unimplemented) AppealAccountStatus(w http.ResponseWriter, r *http.Request, _ AppealAccountStatusParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListReportOutcomes(w http.ResponseWriter, r *http.Request, _ ListReportOutcomesParams) {
	notImplemented(w, r)
}

func (Unimplemented) MarkReportOutcomeRead(w http.ResponseWriter, r *http.Request, _ ReportOutcomeId, _ MarkReportOutcomeReadParams) {
	notImplemented(w, r)
}

func (Unimplemented) RequestAccountReview(w http.ResponseWriter, r *http.Request, _ RequestAccountReviewParams) {
	notImplemented(w, r)
}

func (Unimplemented) AcceptChatRequest(w http.ResponseWriter, r *http.Request, chatId ChatId, params AcceptChatRequestParams) {
	notImplemented(w, r)
}

func (Unimplemented) CountChatRequests(w http.ResponseWriter, r *http.Request, params CountChatRequestsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ListGroupInviteLinks(w http.ResponseWriter, r *http.Request, _ ChatId, _ ListGroupInviteLinksParams) {
	notImplemented(w, r)
}
func (Unimplemented) CreateGroupInviteLink(w http.ResponseWriter, r *http.Request, _ ChatId, _ CreateGroupInviteLinkParams) {
	notImplemented(w, r)
}
func (Unimplemented) UpdateGroupInviteLink(w http.ResponseWriter, r *http.Request, _ ChatId, _ InviteLinkId, _ UpdateGroupInviteLinkParams) {
	notImplemented(w, r)
}
func (Unimplemented) RevokeGroupInviteLink(w http.ResponseWriter, r *http.Request, _ ChatId, _ InviteLinkId, _ RevokeGroupInviteLinkParams) {
	notImplemented(w, r)
}
func (Unimplemented) SetGroupPublicName(w http.ResponseWriter, r *http.Request, _ ChatId, _ SetGroupPublicNameParams) {
	notImplemented(w, r)
}
func (Unimplemented) ClearGroupPublicName(w http.ResponseWriter, r *http.Request, _ ChatId, _ ClearGroupPublicNameParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListGroupBans(w http.ResponseWriter, r *http.Request, _ ChatId, _ ListGroupBansParams) {
	notImplemented(w, r)
}
func (Unimplemented) UnbanGroupUser(w http.ResponseWriter, r *http.Request, _ ChatId, _ UserId, _ UnbanGroupUserParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetInvite(w http.ResponseWriter, r *http.Request, _ InviteToken, _ GetInviteParams) {
	notImplemented(w, r)
}
func (Unimplemented) JoinByInvite(w http.ResponseWriter, r *http.Request, _ InviteToken, _ JoinByInviteParams) {
	notImplemented(w, r)
}
func (Unimplemented) SearchPublicGroups(w http.ResponseWriter, r *http.Request, _ SearchPublicGroupsParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicGroup(w http.ResponseWriter, r *http.Request, _ PublicNameParam, _ GetPublicGroupParams) {
	notImplemented(w, r)
}
func (Unimplemented) JoinPublicGroup(w http.ResponseWriter, r *http.Request, _ PublicNameParam, _ JoinPublicGroupParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicNameAvailability(w http.ResponseWriter, r *http.Request, _ PublicNameParam, _ GetPublicNameAvailabilityParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListUnreadMentions(w http.ResponseWriter, r *http.Request, _ ChatId, _ ListUnreadMentionsParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicName(w http.ResponseWriter, r *http.Request, _ PublicNameParam) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicInvite(w http.ResponseWriter, r *http.Request, _ InviteToken) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicNameAvatar(w http.ResponseWriter, r *http.Request, _ PublicNameParam, _ GetPublicNameAvatarParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetPublicInviteAvatar(w http.ResponseWriter, r *http.Request, _ InviteToken, _ GetPublicInviteAvatarParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetOpenGraphName(w http.ResponseWriter, r *http.Request, _ PublicNameParam) {
	notImplemented(w, r)
}
func (Unimplemented) GetOpenGraphInvite(w http.ResponseWriter, r *http.Request, _ InviteToken) {
	notImplemented(w, r)
}
func (Unimplemented) PinMessage(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ PinMessageParams) {
	notImplemented(w, r)
}
func (Unimplemented) UnpinMessage(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ UnpinMessageParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListPinnedMessages(w http.ResponseWriter, r *http.Request, _ ChatId, _ ListPinnedMessagesParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListSyncChanges(w http.ResponseWriter, r *http.Request, _ ListSyncChangesParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetStorageKey(w http.ResponseWriter, r *http.Request, _ GetStorageKeyParams) {
	notImplemented(w, r)
}
func (Unimplemented) VotePoll(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ VotePollParams) {
	notImplemented(w, r)
}
func (Unimplemented) ClosePoll(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ ClosePollParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListPollVoters(w http.ResponseWriter, r *http.Request, _ ChatId, _ MessageId, _ ListPollVotersParams) {
	notImplemented(w, r)
}
func (Unimplemented) DeleteGroupAvatar(w http.ResponseWriter, r *http.Request, _ ChatId, _ DeleteGroupAvatarParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetMapTile(w http.ResponseWriter, r *http.Request, _ int, _ int, _ int, _ GetMapTileParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetStaticMap(w http.ResponseWriter, r *http.Request, _ GetStaticMapParams) {
	notImplemented(w, r)
}
