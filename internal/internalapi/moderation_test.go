package internalapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/auth"
	"github.com/myronsi/messenger-back/internal/events"
	"github.com/myronsi/messenger-back/internal/moderation"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type fakeModeration struct {
	decision   moderation.Decision
	dismissed  []int64
	dismissAll bool
	packs      []string
	err        error
}

func (f *fakeModeration) Decide(_ context.Context, d moderation.Decision) (moderation.DecisionResult, error) {
	f.decision = d
	code, text := d.ReasonCode, d.ReasonText
	return moderation.DecisionResult{
		Before: moderation.View{Status: moderation.Limited, DecidedBy: "automatic"},
		After:  moderation.View{Status: d.Status, DecidedBy: "staff", ReasonCode: &code, ReasonText: &text},
	}, f.err
}

func (f *fakeModeration) DismissReports(_ context.Context, _ int64, reporters []int64) (moderation.DismissResult, error) {
	f.dismissed, f.dismissAll = reporters, reporters == nil
	return moderation.DismissResult{Dismissed: 2, Before: moderation.View{Status: moderation.Limited}, After: moderation.View{Status: moderation.Warned}}, f.err
}

func (f *fakeModeration) HidePack(_ context.Context, kind moderation.PackKind, id int64) (bool, error) {
	f.packs = append(f.packs, "hide "+string(kind))
	return id == 7, f.err
}

func (f *fakeModeration) UnhidePack(_ context.Context, kind moderation.PackKind, _ int64) (bool, error) {
	f.packs = append(f.packs, "unhide "+string(kind))
	return true, f.err
}

type fakeAccounts struct {
	deleted []int64
	err     error
}

func (f *fakeAccounts) DeleteAccountByStaff(_ context.Context, userID int64) (postgres.DeletedAccount, error) {
	f.deleted = append(f.deleted, userID)
	return postgres.DeletedAccount{}, f.err
}

type fakeEvents struct{ events []events.Event }

func (f *fakeEvents) Emit(_ context.Context, e events.Event) error {
	f.events = append(f.events, e)
	return nil
}

func send(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	return out
}

func TestActionsNeedTheServiceToken(t *testing.T) {
	h := Handler(Options{Token: token, Moderation: &fakeModeration{}, Accounts: &fakeAccounts{}})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, BasePath + "/users/5/status"},
		{http.MethodPost, BasePath + "/users/5/reports/dismiss"},
		{http.MethodPost, BasePath + "/packs/sticker_pack/5/hide"},
		{http.MethodDelete, BasePath + "/users/5"},
	} {
		if rec := call(t, h, route.method, route.path, "Bearer nope"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d, want 401", route.method, route.path, rec.Code)
		}
	}
}

func TestSetStatus(t *testing.T) {
	mod := &fakeModeration{}
	h := Handler(Options{Token: token, Moderation: mod})
	rec := send(t, h, http.MethodPost, BasePath+"/users/5/status", `{"status":"restricted","reason_code":"spam","reason_text":"Links","staff_ref":"case-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if mod.decision != (moderation.Decision{UserID: 5, Status: moderation.Restricted, ReasonCode: "spam", ReasonText: "Links", StaffRef: "case-1"}) {
		t.Fatalf("decision %+v", mod.decision)
	}
	body := decodeMap(t, rec)
	before, after := body["before"].(map[string]any), body["after"].(map[string]any)
	if before["status"] != "limited" || before["decided_by"] != "automatic" || after["status"] != "restricted" || after["decided_by"] != "staff" || after["reason_text"] != "Links" {
		t.Fatalf("body %v", body)
	}

	for name, c := range map[string]struct {
		path, body string
		code       int
	}{
		"bad id":        {"/users/x/status", `{}`, http.StatusBadRequest},
		"bad json":      {"/users/5/status", `{`, http.StatusBadRequest},
		"unknown field": {"/users/5/status", `{"status":"good","who":"me"}`, http.StatusBadRequest},
	} {
		if rec := send(t, h, http.MethodPost, BasePath+c.path, c.body); rec.Code != c.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	mod.err = &moderation.FieldError{Field: "reason_code", Message: "unknown reason"}
	rec = send(t, h, http.MethodPost, BasePath+"/users/5/status", `{"status":"good","reason_code":"x","reason_text":"y"}`)
	if body := decodeMap(t, rec); rec.Code != http.StatusBadRequest || body["field"] != "reason_code" {
		t.Fatalf("field error %d %v", rec.Code, body)
	}
	mod.err = moderation.ErrNotFound
	if rec := send(t, h, http.MethodPost, BasePath+"/users/5/status", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user %d", rec.Code)
	}
	mod.err = moderation.ErrUnavailable
	if rec := send(t, h, http.MethodPost, BasePath+"/users/5/status", `{}`); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("unavailable %d", rec.Code)
	}
}

func TestDismissReports(t *testing.T) {
	mod := &fakeModeration{}
	h := Handler(Options{Token: token, Moderation: mod})
	rec := send(t, h, http.MethodPost, BasePath+"/users/5/reports/dismiss", "")
	if rec.Code != http.StatusOK || !mod.dismissAll {
		t.Fatalf("dismiss all %d %s", rec.Code, rec.Body)
	}
	if body := decodeMap(t, rec); body["dismissed"] != float64(2) || body["after"].(map[string]any)["status"] != "warned" {
		t.Fatalf("body %v", body)
	}
	rec = send(t, h, http.MethodPost, BasePath+"/users/5/reports/dismiss", `{"reporter_ids":["8","9"]}`)
	if rec.Code != http.StatusOK || mod.dismissAll || len(mod.dismissed) != 2 || mod.dismissed[1] != 9 {
		t.Fatalf("dismiss some %d %v", rec.Code, mod.dismissed)
	}
	if rec := send(t, h, http.MethodPost, BasePath+"/users/5/reports/dismiss", `{"reporter_ids":["x"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad reporter id %d", rec.Code)
	}
}

func TestPacks(t *testing.T) {
	mod := &fakeModeration{}
	h := Handler(Options{Token: token, Moderation: mod})
	rec := send(t, h, http.MethodPost, BasePath+"/packs/sticker_pack/7/hide", "")
	if rec.Code != http.StatusOK || decodeMap(t, rec)["changed"] != true {
		t.Fatalf("hide %d %s", rec.Code, rec.Body)
	}
	if rec := send(t, h, http.MethodPost, BasePath+"/packs/emoji_pack/7/unhide", ""); rec.Code != http.StatusOK {
		t.Fatalf("unhide %d %s", rec.Code, rec.Body)
	}
	if strings.Join(mod.packs, ",") != "hide sticker_pack,unhide emoji_pack" {
		t.Fatalf("calls %v", mod.packs)
	}
	if rec := send(t, h, http.MethodPost, BasePath+"/packs/chat/7/hide", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind %d", rec.Code)
	}
	mod.err = moderation.ErrForbidden
	if rec := send(t, h, http.MethodPost, BasePath+"/packs/sticker_pack/1/hide", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("built-in pack %d", rec.Code)
	}
}

func TestDeleteAccount(t *testing.T) {
	accounts, ev := &fakeAccounts{}, &fakeEvents{}
	var cleaned []int64
	h := Handler(Options{Token: token, Accounts: accounts, Events: ev, AfterDeletion: func(_ context.Context, id int64, _ postgres.DeletedAccount) {
		cleaned = append(cleaned, id)
	}})
	if rec := send(t, h, http.MethodDelete, BasePath+"/users/5", ""); rec.Code != http.StatusOK || decodeMap(t, rec)["deleted"] != true {
		t.Fatalf("delete %d %s", rec.Code, rec.Body)
	}
	if len(cleaned) != 1 || cleaned[0] != 5 || len(ev.events) != 1 || ev.events[0].Type != events.UserDeleted || ev.events[0].UserID != 5 {
		t.Fatalf("cleanup %v, events %+v", cleaned, ev.events)
	}
	accounts.err = auth.ErrRevocationIncomplete
	if rec := send(t, h, http.MethodDelete, BasePath+"/users/6", ""); rec.Code != http.StatusOK {
		t.Fatalf("a session cache error after the deletion still deletes: %d", rec.Code)
	}
	accounts.err = postgres.ErrNotFound
	if rec := send(t, h, http.MethodDelete, BasePath+"/users/7", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user %d", rec.Code)
	}
	if len(cleaned) != 2 {
		t.Fatalf("no cleanup for an unknown user: %v", cleaned)
	}
}

func TestActionsAbsentWithoutServices(t *testing.T) {
	h := Handler(Options{Token: token})
	if rec := send(t, h, http.MethodPost, BasePath+"/users/5/status", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("status route without a service: %d", rec.Code)
	}
}
