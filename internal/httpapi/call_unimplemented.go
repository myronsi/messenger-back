package httpapi

import "net/http"

func (Unimplemented) GetCallConfig(w http.ResponseWriter, r *http.Request, _ GetCallConfigParams) {
	notImplemented(w, r)
}
func (Unimplemented) SetCallIdentity(w http.ResponseWriter, r *http.Request, _ SetCallIdentityParams) {
	notImplemented(w, r)
}
func (Unimplemented) ListMyCalls(w http.ResponseWriter, r *http.Request, _ ListMyCallsParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetChatCall(w http.ResponseWriter, r *http.Request, _ ChatId, _ GetChatCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) StartChatCall(w http.ResponseWriter, r *http.Request, _ ChatId, _ StartChatCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetCall(w http.ResponseWriter, r *http.Request, _ CallId, _ GetCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) JoinCall(w http.ResponseWriter, r *http.Request, _ CallId, _ JoinCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) RenewCallGrant(w http.ResponseWriter, r *http.Request, _ CallId, _ RenewCallGrantParams) {
	notImplemented(w, r)
}
func (Unimplemented) DeclineCall(w http.ResponseWriter, r *http.Request, _ CallId, _ DeclineCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) LeaveCall(w http.ResponseWriter, r *http.Request, _ CallId, _ LeaveCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) EndCall(w http.ResponseWriter, r *http.Request, _ CallId, _ EndCallParams) {
	notImplemented(w, r)
}
func (Unimplemented) GetCallKeys(w http.ResponseWriter, r *http.Request, _ CallId, _ GetCallKeysParams) {
	notImplemented(w, r)
}
func (Unimplemented) SendCallKeys(w http.ResponseWriter, r *http.Request, _ CallId, _ SendCallKeysParams) {
	notImplemented(w, r)
}

func (s *Server) GetCallConfig(w http.ResponseWriter, r *http.Request, params GetCallConfigParams) {
	if s.calls == nil {
		s.AuthServer.GetCallConfig(w, r, params)
		return
	}
	s.calls.GetCallConfig(w, r, params)
}

func (s *Server) SetCallIdentity(w http.ResponseWriter, r *http.Request, params SetCallIdentityParams) {
	if s.calls == nil {
		s.AuthServer.SetCallIdentity(w, r, params)
		return
	}
	s.calls.SetCallIdentity(w, r, params)
}

func (s *Server) ListMyCalls(w http.ResponseWriter, r *http.Request, params ListMyCallsParams) {
	if s.calls == nil {
		s.AuthServer.ListMyCalls(w, r, params)
		return
	}
	s.calls.ListMyCalls(w, r, params)
}

func (s *Server) GetChatCall(w http.ResponseWriter, r *http.Request, chatID ChatId, params GetChatCallParams) {
	if s.calls == nil {
		s.AuthServer.GetChatCall(w, r, chatID, params)
		return
	}
	s.calls.GetChatCall(w, r, chatID, params)
}

func (s *Server) StartChatCall(w http.ResponseWriter, r *http.Request, chatID ChatId, params StartChatCallParams) {
	if s.calls == nil {
		s.AuthServer.StartChatCall(w, r, chatID, params)
		return
	}
	s.calls.StartChatCall(w, r, chatID, params)
}

func (s *Server) GetCall(w http.ResponseWriter, r *http.Request, callID CallId, params GetCallParams) {
	if s.calls == nil {
		s.AuthServer.GetCall(w, r, callID, params)
		return
	}
	s.calls.GetCall(w, r, callID, params)
}

func (s *Server) JoinCall(w http.ResponseWriter, r *http.Request, callID CallId, params JoinCallParams) {
	if s.calls == nil {
		s.AuthServer.JoinCall(w, r, callID, params)
		return
	}
	s.calls.JoinCall(w, r, callID, params)
}

func (s *Server) RenewCallGrant(w http.ResponseWriter, r *http.Request, callID CallId, params RenewCallGrantParams) {
	if s.calls == nil {
		s.AuthServer.RenewCallGrant(w, r, callID, params)
		return
	}
	s.calls.RenewCallGrant(w, r, callID, params)
}

func (s *Server) DeclineCall(w http.ResponseWriter, r *http.Request, callID CallId, params DeclineCallParams) {
	if s.calls == nil {
		s.AuthServer.DeclineCall(w, r, callID, params)
		return
	}
	s.calls.DeclineCall(w, r, callID, params)
}

func (s *Server) LeaveCall(w http.ResponseWriter, r *http.Request, callID CallId, params LeaveCallParams) {
	if s.calls == nil {
		s.AuthServer.LeaveCall(w, r, callID, params)
		return
	}
	s.calls.LeaveCall(w, r, callID, params)
}

func (s *Server) EndCall(w http.ResponseWriter, r *http.Request, callID CallId, params EndCallParams) {
	if s.calls == nil {
		s.AuthServer.EndCall(w, r, callID, params)
		return
	}
	s.calls.EndCall(w, r, callID, params)
}

func (s *Server) GetCallKeys(w http.ResponseWriter, r *http.Request, callID CallId, params GetCallKeysParams) {
	if s.calls == nil {
		s.AuthServer.GetCallKeys(w, r, callID, params)
		return
	}
	s.calls.GetCallKeys(w, r, callID, params)
}

func (s *Server) SendCallKeys(w http.ResponseWriter, r *http.Request, callID CallId, params SendCallKeysParams) {
	if s.calls == nil {
		s.AuthServer.SendCallKeys(w, r, callID, params)
		return
	}
	s.calls.SendCallKeys(w, r, callID, params)
}
