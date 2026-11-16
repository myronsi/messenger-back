package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/myronsi/messenger-back/internal/connections"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/redis"
	"github.com/myronsi/messenger-back/internal/users"
)

var RateConnect = redis.Rate{Name: "connect", Rate: 30, Period: time.Hour, Burst: 10}

type ConnectionOptions struct {
	Service   *connections.Service
	Users     postgres.UserRepository
	Directory *users.Directory
	Limiter   RateAllower
	Log       *slog.Logger
}

type ConnectionServer struct {
	o ConnectionOptions
}

func NewConnectionServer(o ConnectionOptions) *ConnectionServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &ConnectionServer{o: o}
}

func PresentConnection(c postgres.Connection) Connection {
	return Connection{
		Id: FormatID(c.ID), Provider: ConnectionProvider(c.Provider), DisplayName: c.DisplayName, ProfileUrl: c.ProfileURL,
		VerifiedAt: utc(c.VerifiedAt),
	}
}

func presentConnections(list []postgres.Connection) ConnectionList {
	out := ConnectionList{Items: make([]Connection, len(list))}
	for i, c := range list {
		out.Items[i] = PresentConnection(c)
	}
	return out
}

func (c *ConnectionServer) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, connections.ErrUnknown), errors.Is(err, postgres.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
	case errors.Is(err, postgres.ErrConnectionLimit):
		WriteProblem(w, http.StatusConflict, ErrorCodeLimitReached, ProblemError{Message: "at most 15 connected accounts"})
	case errors.Is(err, connections.ErrWrongFlow):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "provider", Message: "this provider uses another flow"})
	case errors.Is(err, connections.ErrInvalidHandle):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "identifier", Message: "not a valid account or domain"})
	case errors.Is(err, connections.ErrNoClaim):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Message: "no open claim; start again"})
	case errors.Is(err, connections.ErrNotVerified):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Message: "the code was not found yet"})
	case errors.Is(err, postgres.ErrInvalid):
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "ids", Message: "name every connected account once"})
	default:
		c.o.Log.ErrorContext(r.Context(), what, "error", err)
		WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
	}
}

func (c *ConnectionServer) limited(w http.ResponseWriter, r *http.Request, userID int64) bool {
	return !allow(w, r, c.o.Limiter, RateConnect, strconv.FormatInt(userID, 10), c.o.Log)
}

func (c *ConnectionServer) ListMyConnections(w http.ResponseWriter, r *http.Request, _ ListMyConnectionsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	list, err := c.o.Service.List(r.Context(), p.UserID)
	if err != nil {
		c.fail(w, r, "list connections", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentConnections(list))
}

func (c *ConnectionServer) ReorderMyConnections(w http.ResponseWriter, r *http.Request, _ ReorderMyConnectionsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body ConnectionOrderRequest
	if !decode(w, r, &body) {
		return
	}
	ids := make([]int64, 0, len(body.Ids))
	for _, raw := range body.Ids {
		id, ok := parseID(w, "ids", raw)
		if !ok {
			return
		}
		ids = append(ids, id)
	}
	list, err := c.o.Service.Reorder(r.Context(), p.UserID, ids)
	if err != nil {
		c.fail(w, r, "reorder connections", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, presentConnections(list))
}

func (c *ConnectionServer) DeleteMyConnection(w http.ResponseWriter, r *http.Request, connectionID Id, _ DeleteMyConnectionParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "connection_id", connectionID)
	if !ok {
		return
	}
	if err := c.o.Service.Delete(r.Context(), p.UserID, id); err != nil {
		c.fail(w, r, "delete connection", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *ConnectionServer) StartConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, _ StartConnectionParams) {
	p, ok := principalOr401(w, r)
	if !ok || c.limited(w, r, p.UserID) {
		return
	}
	redirect, err := c.o.Service.Start(r.Context(), p.UserID, string(provider))
	if err != nil {
		c.fail(w, r, "start connection", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, ConnectionStart{RedirectUrl: redirect})
}

func (c *ConnectionServer) ClaimConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, _ ClaimConnectionParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body ConnectionClaimRequest
	if !decode(w, r, &body) || c.limited(w, r, p.UserID) {
		return
	}
	ch, err := c.o.Service.Claim(r.Context(), p.UserID, string(provider), body.Identifier)
	if err != nil {
		c.fail(w, r, "claim connection", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, ConnectionChallenge{Identifier: ch.Identifier, Code: ch.Code, ExpiresAt: utc(ch.ExpiresAt)})
}

func (c *ConnectionServer) VerifyConnection(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath, _ VerifyConnectionParams) {
	p, ok := principalOr401(w, r)
	if !ok || c.limited(w, r, p.UserID) {
		return
	}
	conn, err := c.o.Service.Verify(r.Context(), p.UserID, string(provider))
	if err != nil {
		c.fail(w, r, "verify connection", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusCreated, PresentConnection(conn))
}

func (c *ConnectionServer) ConnectionCallback(w http.ResponseWriter, r *http.Request, provider ConnectionProviderPath) {
	location := c.o.Service.Callback(r.Context(), string(provider), r.URL.Query())
	noStore(w)
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, location, http.StatusFound) //nolint:gosec // the target is CONNECTIONS_WEB_URL with escaped parameters
}

func (c *ConnectionServer) ListUserConnections(w http.ResponseWriter, r *http.Request, userID Id, _ ListUserConnectionsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseID(w, "user_id", userID)
	if !ok {
		return
	}
	if _, err := c.o.Users.Get(r.Context(), id); err != nil {
		c.fail(w, r, "user for connections", err)
		return
	}
	visible, err := c.o.Directory.ProfileVisible(r.Context(), p.UserID, id)
	if err != nil {
		c.fail(w, r, "connection visibility", err)
		return
	}
	list := []postgres.Connection{}
	if visible {
		if list, err = c.o.Service.List(r.Context(), id); err != nil {
			c.fail(w, r, "list user connections", err)
			return
		}
	}
	writeJSONCached(w, r, presentConnections(list))
}
