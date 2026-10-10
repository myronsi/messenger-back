package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/users"
)

// AccountStore is the PostgreSQL side of the account and user endpoints.
type AccountStore interface {
	Users() postgres.UserRepository
	Social() postgres.SocialRepository
}

// AccountDeleter deletes accounts after checking the password (auth.Service).
type AccountDeleter interface {
	DeleteAccount(ctx context.Context, p auth.Principal, password string) (postgres.DeletedAccount, error)
}

// AfterAccountDeletion cleans up after an account is gone: caches, events, sockets, files.
type AfterAccountDeletion func(ctx context.Context, userID int64, deleted postgres.DeletedAccount)

// EventEmitter appends domain events.
type EventEmitter interface {
	Emit(ctx context.Context, e events.Event) error
}

// AccountOptions configures the account and user endpoints.
type AccountOptions struct {
	Store     AccountStore
	Directory *users.Directory
	Deleter   AccountDeleter
	// AfterDeletion runs after an account was deleted. Optional.
	AfterDeletion AfterAccountDeletion
	Events        EventEmitter
	Limiter       RateAllower
	BasePath      string
	Log           *slog.Logger
}

// AccountServer implements the profile, privacy, blocking, contact-name and user lookup operations.
type AccountServer struct {
	o AccountOptions
}

// NewAccountServer returns the endpoints.
func NewAccountServer(o AccountOptions) *AccountServer {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &AccountServer{o: o}
}

func (a *AccountServer) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	a.o.Log.ErrorContext(r.Context(), what, "error", err)
	WriteProblem(w, http.StatusInternalServerError, ErrorCodeInternalError)
}

func (a *AccountServer) emit(ctx context.Context, e events.Event) {
	if a.o.Events == nil {
		return
	}
	if err := a.o.Events.Emit(context.WithoutCancel(ctx), e); err != nil {
		a.o.Log.WarnContext(ctx, "emit event", "type", e.Type, "error", err)
	}
}

func principalOr401(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		writeUnauthorized(w, ErrorCodeUnauthenticated)
	}
	return p, ok
}

func parseUserID(w http.ResponseWriter, s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest, ProblemError{Field: "id", Message: "not an id"})
		return 0, false
	}
	return id, true
}

// ---------------------------------------------------------------- the profile

// UpdateMe implements PATCH /me: only the sent fields change; "bio": null clears the bio.
func (a *AccountServer) UpdateMe(w http.ResponseWriter, r *http.Request, _ UpdateMeParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	if len(raw) == 0 {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "body", Message: "send at least one field"})
		return
	}
	var prof postgres.ProfilePatch
	for k, v := range raw {
		switch k {
		case "display_name":
			var name string
			if json.Unmarshal(v, &name) != nil || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "display_name", Message: "1 to 64 characters"})
				return
			}
			name = strings.TrimSpace(name)
			prof.DisplayName = &name
		case "bio":
			var bio *string
			if json.Unmarshal(v, &bio) != nil || (bio != nil && utf8.RuneCountInString(*bio) > 500) {
				WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "bio", Message: "at most 500 characters or null"})
				return
			}
			if bio != nil {
				trimmed := strings.TrimSpace(*bio)
				bio = &trimmed
				if trimmed == "" {
					bio = nil
				}
			}
			prof.Bio, prof.SetBio = bio, true
		default:
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: k, Message: "unknown field"})
			return
		}
	}
	u, err := a.o.Store.Users().PatchProfile(r.Context(), p.UserID, prof)
	if err != nil {
		a.internal(w, r, "update profile", err)
		return
	}
	a.emit(r.Context(), events.Event{Type: events.UserUpdated, UserID: p.UserID, ActorID: p.UserID})
	noStore(w)
	writeJSONStatus(w, http.StatusOK, PresentMe(u, a.o.BasePath))
}

// DeleteMe implements DELETE /me (with the password).
func (a *AccountServer) DeleteMe(w http.ResponseWriter, r *http.Request, _ DeleteMeParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	deleted, err := a.o.Deleter.DeleteAccount(r.Context(), p, body.Password)
	var limited *auth.RateLimitError
	switch {
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(int(limited.RetryAfter/time.Second)+1))
		WriteProblem(w, http.StatusTooManyRequests, ErrorCodeRateLimited)
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		WriteProblem(w, http.StatusForbidden, ErrorCodeInvalidCredentials)
		return
	case errors.Is(err, auth.ErrRevocationIncomplete):
		// The account is gone; the session cache expires within its TTL.
	case err != nil:
		a.internal(w, r, "delete account", err)
		return
	}
	if a.o.AfterDeletion != nil {
		a.o.AfterDeletion(context.WithoutCancel(r.Context()), p.UserID, deleted)
	}
	a.emit(r.Context(), events.Event{Type: events.UserDeleted, UserID: p.UserID})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- privacy

// The contract calls "contacts" what the schema calls shared_chats (people you have a chat with).
func visibilityToDB(v string) string {
	if v == "contacts" {
		return postgres.VisibleSharedChats
	}
	return v
}

func visibilityFromDB(v string) string {
	if v == postgres.VisibleSharedChats {
		return "contacts"
	}
	return v
}

var exceptionSettings = []string{postgres.SettingAvatar, postgres.SettingProfile, postgres.SettingPresence, postgres.SettingGroupInvites}

func (a *AccountServer) privacy(ctx context.Context, userID int64) (PrivacySettings, error) {
	all, err := a.o.Store.Social().Privacy(ctx, []int64{userID})
	if err != nil {
		return PrivacySettings{}, err
	}
	s := all[userID]
	exc, err := a.o.Store.Social().Exceptions(ctx, userID)
	if err != nil {
		return PrivacySettings{}, err
	}
	out := PrivacySettings{
		AvatarVisibility:    Visibility(visibilityFromDB(s.AvatarVisibility)),
		ProfileVisibility:   Visibility(visibilityFromDB(s.ProfileVisibility)),
		PresenceVisibility:  Visibility(visibilityFromDB(s.PresenceVisibility)),
		ReadReceiptsEnabled: s.ReadReceiptsEnabled,
		DirectMessages:      DirectMessagePolicy(s.DirectMessages),
		GroupInvites:        GroupInvitePolicy(visibilityFromDB(s.GroupInvites)),
		SearchVisibility:    SearchVisibility(s.SearchVisibility),
		Exceptions:          PrivacyExceptions{},
	}
	for _, k := range exceptionSettings {
		out.Exceptions[k] = struct {
			Allow []Id `json:"allow"`
			Deny  []Id `json:"deny"`
		}{Allow: []Id{}, Deny: []Id{}}
	}
	for _, e := range exc {
		lists := out.Exceptions[e.SettingKey]
		if e.Effect == postgres.EffectAllow {
			lists.Allow = append(lists.Allow, FormatID(e.TargetID))
		} else {
			lists.Deny = append(lists.Deny, FormatID(e.TargetID))
		}
		out.Exceptions[e.SettingKey] = lists
	}
	return out, nil
}

// GetPrivacySettings implements GET /me/privacy.
func (a *AccountServer) GetPrivacySettings(w http.ResponseWriter, r *http.Request, _ GetPrivacySettingsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	out, err := a.privacy(r.Context(), p.UserID)
	if err != nil {
		a.internal(w, r, "privacy settings", err)
		return
	}
	noStore(w)
	writeJSONStatus(w, http.StatusOK, out)
}

// UpdatePrivacySettings implements PATCH /me/privacy.
func (a *AccountServer) UpdatePrivacySettings(w http.ResponseWriter, r *http.Request, _ UpdatePrivacySettingsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	var body UpdatePrivacySettingsRequest
	if !decode(w, r, &body) {
		return
	}
	var s postgres.PrivacyPatch
	changed := false
	set := func(dst **string, v string) { *dst, changed = &v, true }
	vis := func(dst **string, v *Visibility, field string) bool {
		if v == nil {
			return true
		}
		if !v.Valid() {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: field, Message: "unknown value"})
			return false
		}
		set(dst, visibilityToDB(string(*v)))
		return true
	}
	if !vis(&s.AvatarVisibility, body.AvatarVisibility, "avatar_visibility") ||
		!vis(&s.ProfileVisibility, body.ProfileVisibility, "profile_visibility") ||
		!vis(&s.PresenceVisibility, body.PresenceVisibility, "presence_visibility") {
		return
	}
	if body.ReadReceiptsEnabled != nil {
		s.ReadReceiptsEnabled, changed = body.ReadReceiptsEnabled, true
	}
	if body.DirectMessages != nil {
		if !body.DirectMessages.Valid() {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "direct_messages", Message: "unknown value"})
			return
		}
		set(&s.DirectMessages, string(*body.DirectMessages))
	}
	if body.GroupInvites != nil {
		if !body.GroupInvites.Valid() {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "group_invites", Message: "unknown value"})
			return
		}
		set(&s.GroupInvites, visibilityToDB(string(*body.GroupInvites)))
	}
	if body.SearchVisibility != nil {
		if !body.SearchVisibility.Valid() {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "search_visibility", Message: "unknown value"})
			return
		}
		set(&s.SearchVisibility, string(*body.SearchVisibility))
	}
	if !changed {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "body", Message: "send at least one setting"})
		return
	}
	if _, err := a.o.Store.Social().PatchPrivacy(r.Context(), p.UserID, s); err != nil {
		if errors.Is(err, postgres.ErrInvalid) {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
			return
		}
		a.internal(w, r, "update privacy settings", err)
		return
	}
	a.emit(r.Context(), events.Event{Type: events.UserUpdated, UserID: p.UserID, ActorID: p.UserID})
	a.GetPrivacySettings(w, r, GetPrivacySettingsParams{})
}

// ReplacePrivacyExceptions implements PUT /me/privacy/exceptions/{setting_key}/{effect}.
func (a *AccountServer) ReplacePrivacyExceptions(w http.ResponseWriter, r *http.Request, settingKey, effect string, _ ReplacePrivacyExceptionsParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	known := false
	for _, k := range exceptionSettings {
		known = known || k == settingKey
	}
	if !known || (effect != postgres.EffectAllow && effect != postgres.EffectDeny) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	var body PrivacyExceptionsRequest
	if !decode(w, r, &body) {
		return
	}
	if len(body.UserIds) > 500 {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "user_ids", Message: "at most 500"})
		return
	}
	targets := make([]int64, 0, len(body.UserIds))
	seen := map[int64]bool{}
	for _, s := range body.UserIds {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 || id == p.UserID {
			WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "user_ids", Message: "must be other users' ids"})
			return
		}
		if !seen[id] {
			seen[id] = true
			targets = append(targets, id)
		}
	}
	err := a.o.Store.Social().ReplaceExceptions(r.Context(), p.UserID, settingKey, effect, targets)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrInvalid) {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "user_ids", Message: "unknown user"})
		return
	}
	if err != nil {
		a.internal(w, r, "replace privacy exceptions", err)
		return
	}
	a.emit(r.Context(), events.Event{Type: events.UserUpdated, UserID: p.UserID, ActorID: p.UserID})
	a.GetPrivacySettings(w, r, GetPrivacySettingsParams{})
}

// ---------------------------------------------------------------- blocking

func pageLimit(l *Limit) int {
	if l == nil {
		return 50
	}
	return min(max(*l, 1), 100)
}

// ListBlockedUsers implements GET /me/blocked-users. The cursor is "<unix nanos>_<user id>" of the last block.
func (a *AccountServer) ListBlockedUsers(w http.ResponseWriter, r *http.Request, params ListBlockedUsersParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	limit := pageLimit(params.Limit)
	var after *postgres.Block
	if params.After != nil && *params.After != "" {
		ts, id, found := strings.Cut(*params.After, "_")
		nanos, err1 := strconv.ParseInt(ts, 10, 64)
		uid, err2 := strconv.ParseInt(id, 10, 64)
		if !found || err1 != nil || err2 != nil {
			WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
			return
		}
		after = &postgres.Block{BlockedID: uid, CreatedAt: time.Unix(0, nanos)}
	}
	blocks, err := a.o.Store.Social().BlockedPage(r.Context(), p.UserID, after, limit+1)
	if err != nil {
		a.internal(w, r, "blocked users", err)
		return
	}
	var next *string
	if len(blocks) > limit {
		last := blocks[limit-1]
		c := strconv.FormatInt(last.CreatedAt.UnixNano(), 10) + "_" + strconv.FormatInt(last.BlockedID, 10)
		next, blocks = &c, blocks[:limit]
	}
	ids := make([]int64, len(blocks))
	for i, b := range blocks {
		ids[i] = b.BlockedID
	}
	views, err := a.o.Directory.ForViewer(r.Context(), p.UserID, ids)
	if err != nil {
		a.internal(w, r, "render users", err)
		return
	}
	out := UserPage{Items: make([]User, 0, len(ids)), NextCursor: next}
	for _, id := range ids {
		out.Items = append(out.Items, PresentUser(views[id]))
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// BlockUser implements PUT /me/blocked-users/{id}.
func (a *AccountServer) BlockUser(w http.ResponseWriter, r *http.Request, userID UserId, _ BlockUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	if target == p.UserID {
		WriteProblem(w, http.StatusBadRequest, ErrorCodeInvalidRequest)
		return
	}
	err := a.o.Store.Social().Block(r.Context(), p.UserID, target)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		a.internal(w, r, "block user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnblockUser implements DELETE /me/blocked-users/{id}.
func (a *AccountServer) UnblockUser(w http.ResponseWriter, r *http.Request, userID UserId, _ UnblockUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	// Unblocking someone who is not blocked is fine (a retried request); only an unknown user is 404.
	err := a.o.Store.Social().Unblock(r.Context(), p.UserID, target)
	if errors.Is(err, postgres.ErrNotFound) {
		if _, gerr := a.o.Store.Users().Get(r.Context(), target); errors.Is(gerr, postgres.ErrNotFound) {
			WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
			return
		}
		err = nil
	}
	if err != nil {
		a.internal(w, r, "unblock user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- users

// writeUser renders a user for the viewer, or 404 for a user that does not exist.
func (a *AccountServer) writeUser(w http.ResponseWriter, r *http.Request, viewer, id int64) {
	views, err := a.o.Directory.ForViewer(r.Context(), viewer, []int64{id})
	if err != nil {
		a.internal(w, r, "render user", err)
		return
	}
	v := views[id]
	if v.IsDeleted {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	writeJSONStatus(w, http.StatusOK, PresentUser(v))
}

// GetUser implements GET /users/{id}.
func (a *AccountServer) GetUser(w http.ResponseWriter, r *http.Request, userID UserId, _ GetUserParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	id, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	a.writeUser(w, r, p.UserID, id)
}

// GetUserByUsername implements GET /usernames/{username}.
func (a *AccountServer) GetUserByUsername(w http.ResponseWriter, r *http.Request, username Username, _ GetUserByUsernameParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	u, err := a.o.Store.Users().GetByUsername(r.Context(), username)
	if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrInvalid) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		a.internal(w, r, "user by username", err)
		return
	}
	a.writeUser(w, r, p.UserID, u.ID)
}

// SetContactName implements PUT /users/{id}/contact-name.
func (a *AccountServer) SetContactName(w http.ResponseWriter, r *http.Request, userID UserId, _ SetContactNameParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	var body ContactNameRequest
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.ContactName)
	if name == "" || utf8.RuneCountInString(name) > 64 || target == p.UserID {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "contact_name", Message: "1 to 64 characters, for another user"})
		return
	}
	err := a.o.Store.Social().SetContactName(r.Context(), p.UserID, target, name)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		a.internal(w, r, "set contact name", err)
		return
	}
	a.writeUser(w, r, p.UserID, target)
}

// RemoveContactName implements DELETE /users/{id}/contact-name.
func (a *AccountServer) RemoveContactName(w http.ResponseWriter, r *http.Request, userID UserId, _ RemoveContactNameParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	target, ok := parseUserID(w, userID)
	if !ok {
		return
	}
	err := a.o.Store.Social().DeleteContactName(r.Context(), p.UserID, target)
	if errors.Is(err, postgres.ErrNotFound) {
		WriteProblem(w, http.StatusNotFound, ErrorCodeNotFound)
		return
	}
	if err != nil {
		a.internal(w, r, "remove contact name", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SearchUsers implements GET /users?q=: a username prefix or a part of the display name. The cursor is the
// last username.
func (a *AccountServer) SearchUsers(w http.ResponseWriter, r *http.Request, params SearchUsersParams) {
	p, ok := principalOr401(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(params.Q)
	if n := utf8.RuneCountInString(q); n < 2 || n > 64 {
		WriteProblem(w, http.StatusUnprocessableEntity, ErrorCodeValidationFailed, ProblemError{Field: "q", Message: "2 to 64 characters"})
		return
	}
	if !allow(w, r, a.o.Limiter, RateSearch, strconv.FormatInt(p.UserID, 10), a.o.Log) {
		return
	}
	limit := pageLimit(params.Limit)
	after := ""
	if params.After != nil {
		after = *params.After
	}
	found, err := a.o.Store.Users().Search(r.Context(), p.UserID, q, after, limit+1)
	if err != nil {
		a.internal(w, r, "search users", err)
		return
	}
	var next *string
	if len(found) > limit {
		c := found[limit-1].Username
		next, found = &c, found[:limit]
	}
	ids := make([]int64, len(found))
	for i, u := range found {
		ids[i] = u.ID
	}
	views, err := a.o.Directory.ForViewer(r.Context(), p.UserID, ids)
	if err != nil {
		a.internal(w, r, "render users", err)
		return
	}
	out := UserPage{Items: make([]User, 0, len(ids)), NextCursor: next}
	for _, id := range ids {
		out.Items = append(out.Items, PresentUser(views[id]))
	}
	writeJSONStatus(w, http.StatusOK, out)
}
