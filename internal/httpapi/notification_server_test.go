package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/config"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type notificationAuth struct{ p auth.Principal }

func (a notificationAuth) Authenticate(context.Context, string) (auth.Principal, error) {
	return a.p, nil
}

type notificationStoreFake struct {
	postgres.NotificationRepository
	uid    int64
	sid    uuid.UUID
	device postgres.NotificationDevice
	p      postgres.NotificationPreferences
}

func (q *notificationStoreFake) Preferences(_ context.Context, uid int64) (postgres.NotificationPreferences, error) {
	q.uid = uid
	return postgres.NotificationPreferences{Enabled: true}, nil
}
func (q *notificationStoreFake) Mutes(context.Context, int64) ([]postgres.NotificationMute, error) {
	return []postgres.NotificationMute{}, nil
}
func (q *notificationStoreFake) SetPreferences(_ context.Context, uid int64, p postgres.NotificationPreferences) error {
	q.uid = uid
	q.p = p
	return nil
}
func (q *notificationStoreFake) Register(_ context.Context, uid int64, sid uuid.UUID, d postgres.NotificationDevice) error {
	q.uid = uid
	q.sid = sid
	q.device = d
	return nil
}
func (q *notificationStoreFake) Unregister(_ context.Context, uid int64, sid, id uuid.UUID) error {
	q.uid = uid
	q.sid = sid
	q.device.ID = id
	return nil
}

func TestNotificationRoutesRequireAuthentication(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, tc := range []struct{ method, path string }{{"GET", "/me/notifications"}, {"PUT", "/me/notifications"}, {"POST", "/me/notification-devices"}, {"DELETE", "/me/notification-devices/" + uuid.NewString()}, {"PUT", "/chats/7/notification-mute"}, {"PUT", "/me/notification-devices/" + uuid.NewString() + "/activity"}} {
		rec := e.do(httptest.NewRequest(tc.method, apiBase+tc.path, nil))
		if rec.Code != 401 {
			t.Fatalf("%s %s: %d", tc.method, tc.path, rec.Code)
		}
	}
}
func TestNotificationAPIValidatesBodiesAndUsesAuthenticatedOwner(t *testing.T) {
	store := &notificationStoreFake{}
	principal := auth.Principal{UserID: 42, SessionID: uuid.New()}
	api := (&Server{}).WithNotifications(NotificationOptions{Store: store, Config: config.Notifications{FCMServiceAccount: "configured"}})
	e := newTestEnv(t, func(o *Options) {
		o.API = api
		o.Authenticator = notificationAuth{principal}
		o.HTTP.MaxBodyBytes = 8192
	})
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, apiBase+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Content-Type", "application/json")
		rec := e.do(r)
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		checkContract(t, method, apiBase+path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Bytes())
		return rec
	}
	request("GET", "/me/notifications", "", 200)
	request("PUT", "/me/notifications", `{"enabled":false}`, 400)
	request("PUT", "/me/notifications", `{"enabled":false,"sound":false,"previews":false}`, 204)
	if store.uid != 42 || store.p.Enabled {
		t.Fatal("preferences used an incorrect owner")
	}
	request("POST", "/me/notification-devices", `{"platform":"web","destination":"http://localhost/private"}`, 400)
	request("POST", "/me/notification-devices", `{"platform":"android","destination":"valid-token","user_id":999}`, 200)
	if store.uid != 42 || store.sid != principal.SessionID {
		t.Fatal("request supplied its owner")
	}
	request("DELETE", "/me/notification-devices/"+store.device.ID.String(), "", 204)
}

var _ http.Handler = (*Router)(nil)
