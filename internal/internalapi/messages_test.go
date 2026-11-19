package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type fakeRemover struct {
	calls   []scylla.ModerationRemoval
	ids     [][2]int64
	err     error
	removed map[int64]scylla.ModerationRemoval
}

func (f *fakeRemover) RemoveByModeration(_ context.Context, chatID, messageID int64, reason scylla.ModerationRemoval) (scylla.Message, bool, error) {
	f.calls = append(f.calls, reason)
	f.ids = append(f.ids, [2]int64{chatID, messageID})
	if f.err != nil {
		return scylla.Message{}, false, f.err
	}
	sender := int64(5)
	if first, ok := f.removed[messageID]; ok {
		return scylla.Message{ChatID: chatID, ID: messageID, SenderID: &sender, Deleted: true, Moderation: &first}, false, nil
	}
	f.removed[messageID] = reason
	return scylla.Message{ChatID: chatID, ID: messageID, SenderID: &sender, Deleted: true, Moderation: &reason}, true, nil
}

func post(t *testing.T, h http.Handler, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRemoveMessage(t *testing.T) {
	f := &fakeRemover{removed: map[int64]scylla.ModerationRemoval{}}
	h := Handler(Options{Token: token, Messages: f})
	path := BasePath + "/chats/42/messages/77/remove"
	body := `{"reason_code":"harassment","reason_text":"Insults are not allowed."}`

	if rec := post(t, h, path, "", body); rec.Code != http.StatusUnauthorized || len(f.calls) != 0 {
		t.Fatalf("without the token: %d, %d calls", rec.Code, len(f.calls))
	}
	rec := post(t, h, path, "Bearer "+token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != "42" || got["message_id"] != "77" || got["sender_id"] != "5" || got["reason_code"] != "harassment" || got["already_removed"] != false {
		t.Fatalf("body %v", got)
	}
	if f.ids[0] != [2]int64{42, 77} || f.calls[0].Text != "Insults are not allowed." {
		t.Fatalf("calls %v %v", f.ids, f.calls)
	}
	again := post(t, h, path, "Bearer "+token, `{"reason_code":"spam","reason_text":""}`)
	if err := json.Unmarshal(again.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if again.Code != http.StatusOK || got["already_removed"] != true || got["reason_code"] != "harassment" {
		t.Fatalf("second removal: %d %v", again.Code, got)
	}
}

func TestRemoveMessageRejectsBadInput(t *testing.T) {
	f := &fakeRemover{removed: map[int64]scylla.ModerationRemoval{}}
	h := Handler(Options{Token: token, Messages: f})
	auth := "Bearer " + token
	cases := map[string]struct {
		path, body string
		want       int
	}{
		"unknown code":  {BasePath + "/chats/1/messages/2/remove", `{"reason_code":"rude","reason_text":""}`, http.StatusBadRequest},
		"bad id":        {BasePath + "/chats/x/messages/2/remove", `{"reason_code":"spam","reason_text":""}`, http.StatusBadRequest},
		"unknown field": {BasePath + "/chats/1/messages/2/remove", `{"reason_code":"spam","staff":"x"}`, http.StatusBadRequest},
		"not json":      {BasePath + "/chats/1/messages/2/remove", `nope`, http.StatusBadRequest},
	}
	for name, c := range cases {
		if rec := post(t, h, c.path, auth, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.want)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("invalid requests reached the service: %v", f.calls)
	}
	for err, want := range map[error]int{
		messages.ErrNotFound:          http.StatusNotFound,
		messages.ErrInvalid:           http.StatusBadRequest,
		messages.ErrUnavailable:       http.StatusServiceUnavailable,
		errors.New("scylla exploded"): http.StatusServiceUnavailable,
	} {
		f.err = err
		if rec := post(t, h, BasePath+"/chats/1/messages/2/remove", auth, `{"reason_code":"spam","reason_text":""}`); rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
}
