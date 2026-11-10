package httpapi

import "net/http"

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

func (Unimplemented) UpdateGroup(w http.ResponseWriter, r *http.Request, chatId ChatId, params UpdateGroupParams) {
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
