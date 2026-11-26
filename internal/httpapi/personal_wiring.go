package httpapi

import "net/http"

func (s *Server) GetClientSettings(w http.ResponseWriter, r *http.Request, params GetClientSettingsParams) {
	if s.account == nil {
		s.AuthServer.GetClientSettings(w, r, params)
		return
	}
	s.account.GetClientSettings(w, r, params)
}

func (s *Server) UpdateClientSettings(w http.ResponseWriter, r *http.Request, params UpdateClientSettingsParams) {
	if s.account == nil {
		s.AuthServer.UpdateClientSettings(w, r, params)
		return
	}
	s.account.UpdateClientSettings(w, r, params)
}

func (s *Server) ListWallpapers(w http.ResponseWriter, r *http.Request, params ListWallpapersParams) {
	if s.account == nil {
		s.AuthServer.ListWallpapers(w, r, params)
		return
	}
	s.account.ListWallpapers(w, r, params)
}

func (s *Server) SetDefaultWallpaper(w http.ResponseWriter, r *http.Request, params SetDefaultWallpaperParams) {
	if s.account == nil {
		s.AuthServer.SetDefaultWallpaper(w, r, params)
		return
	}
	s.account.SetDefaultWallpaper(w, r, params)
}

func (s *Server) DeleteDefaultWallpaper(w http.ResponseWriter, r *http.Request, params DeleteDefaultWallpaperParams) {
	if s.account == nil {
		s.AuthServer.DeleteDefaultWallpaper(w, r, params)
		return
	}
	s.account.DeleteDefaultWallpaper(w, r, params)
}

func (s *Server) ListPlaylists(w http.ResponseWriter, r *http.Request, params ListPlaylistsParams) {
	if s.account == nil {
		s.AuthServer.ListPlaylists(w, r, params)
		return
	}
	s.account.ListPlaylists(w, r, params)
}

func (s *Server) CreatePlaylist(w http.ResponseWriter, r *http.Request, params CreatePlaylistParams) {
	if s.account == nil {
		s.AuthServer.CreatePlaylist(w, r, params)
		return
	}
	s.account.CreatePlaylist(w, r, params)
}

func (s *Server) GetPlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, params GetPlaylistParams) {
	if s.account == nil {
		s.AuthServer.GetPlaylist(w, r, playlistID, params)
		return
	}
	s.account.GetPlaylist(w, r, playlistID, params)
}

func (s *Server) RenamePlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, params RenamePlaylistParams) {
	if s.account == nil {
		s.AuthServer.RenamePlaylist(w, r, playlistID, params)
		return
	}
	s.account.RenamePlaylist(w, r, playlistID, params)
}

func (s *Server) DeletePlaylist(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, params DeletePlaylistParams) {
	if s.account == nil {
		s.AuthServer.DeletePlaylist(w, r, playlistID, params)
		return
	}
	s.account.DeletePlaylist(w, r, playlistID, params)
}

func (s *Server) ReplacePlaylistTracks(w http.ResponseWriter, r *http.Request, playlistID PlaylistId, params ReplacePlaylistTracksParams) {
	if s.account == nil {
		s.AuthServer.ReplacePlaylistTracks(w, r, playlistID, params)
		return
	}
	s.account.ReplacePlaylistTracks(w, r, playlistID, params)
}

func (s *Server) SetChatWallpaper(w http.ResponseWriter, r *http.Request, chatID ChatId, params SetChatWallpaperParams) {
	if s.chats == nil {
		s.AuthServer.SetChatWallpaper(w, r, chatID, params)
		return
	}
	s.chats.SetChatWallpaper(w, r, chatID, params)
}

func (s *Server) DeleteChatWallpaper(w http.ResponseWriter, r *http.Request, chatID ChatId, params DeleteChatWallpaperParams) {
	if s.chats == nil {
		s.AuthServer.DeleteChatWallpaper(w, r, chatID, params)
		return
	}
	s.chats.DeleteChatWallpaper(w, r, chatID, params)
}
