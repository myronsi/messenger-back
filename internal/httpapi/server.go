package httpapi

import "net/http"

// Server serves the whole contract by delegating to the feature servers: authentication, sessions and the
// account (AuthServer), uploads, downloads and avatars (MediaServer). Operations of features that are not
// implemented yet answer 501 through the AuthServer's embedded Unimplemented.
type Server struct {
	*AuthServer
	media *MediaServer
}

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
