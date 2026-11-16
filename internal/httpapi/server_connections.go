package httpapi

import "net/http"

func (s *Server) WithConnections(c *ConnectionServer) *Server {
	s.connections = c
	s.meta.ConnectionProviders = c.o.Service.Enabled()
	return s
}

func (s *Server) ListMyConnections(w http.ResponseWriter, r *http.Request, params ListMyConnectionsParams) {
	if s.connections == nil {
		s.AuthServer.ListMyConnections(w, r, params)
		return
	}
	s.connections.ListMyConnections(w, r, params)
}

func (s *Server) ReorderMyConnections(w http.ResponseWriter, r *http.Request, params ReorderMyConnectionsParams) {
	if s.connections == nil {
		s.AuthServer.ReorderMyConnections(w, r, params)
		return
	}
	s.connections.ReorderMyConnections(w, r, params)
}

func (s *Server) DeleteMyConnection(w http.ResponseWriter, r *http.Request, connectionID Id, params DeleteMyConnectionParams) {
	if s.connections == nil {
		s.AuthServer.DeleteMyConnection(w, r, connectionID, params)
		return
	}
	s.connections.DeleteMyConnection(w, r, connectionID, params)
}

func (s *Server) StartConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, params StartConnectionParams) {
	if s.connections == nil {
		s.AuthServer.StartConnection(w, r, provider, params)
		return
	}
	s.connections.StartConnection(w, r, provider, params)
}

func (s *Server) ClaimConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, params ClaimConnectionParams) {
	if s.connections == nil {
		s.AuthServer.ClaimConnection(w, r, provider, params)
		return
	}
	s.connections.ClaimConnection(w, r, provider, params)
}

func (s *Server) VerifyConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, params VerifyConnectionParams) {
	if s.connections == nil {
		s.AuthServer.VerifyConnection(w, r, provider, params)
		return
	}
	s.connections.VerifyConnection(w, r, provider, params)
}

func (s *Server) ConnectionCallback(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath) {
	if s.connections == nil {
		s.AuthServer.ConnectionCallback(w, r, provider)
		return
	}
	s.connections.ConnectionCallback(w, r, provider)
}

func (s *Server) ListUserConnections(w http.ResponseWriter, r *http.Request, userID Id, params ListUserConnectionsParams) {
	if s.connections == nil {
		s.AuthServer.ListUserConnections(w, r, userID, params)
		return
	}
	s.connections.ListUserConnections(w, r, userID, params)
}

func (Unimplemented) ListMyConnections(w http.ResponseWriter, r *http.Request, _ ListMyConnectionsParams) {
	notImplemented(w, r)
}

func (Unimplemented) ReorderMyConnections(w http.ResponseWriter, r *http.Request, _ ReorderMyConnectionsParams) {
	notImplemented(w, r)
}

func (Unimplemented) DeleteMyConnection(w http.ResponseWriter, r *http.Request, _ Id, _ DeleteMyConnectionParams) {
	notImplemented(w, r)
}

func (Unimplemented) StartConnection(w http.ResponseWriter, r *http.Request, _ ConnectionProviderPath, _ StartConnectionParams) {
	notImplemented(w, r)
}

func (Unimplemented) ClaimConnection(w http.ResponseWriter, r *http.Request, _ ConnectionProviderPath, _ ClaimConnectionParams) {
	notImplemented(w, r)
}

func (Unimplemented) VerifyConnection(w http.ResponseWriter, r *http.Request, _ ConnectionProviderPath, _ VerifyConnectionParams) {
	notImplemented(w, r)
}

func (Unimplemented) ConnectionCallback(w http.ResponseWriter, r *http.Request, _ ConnectionProviderPath) {
	notImplemented(w, r)
}

func (Unimplemented) ListUserConnections(w http.ResponseWriter, r *http.Request, _ Id, _ ListUserConnectionsParams) {
	notImplemented(w, r)
}
