package messages_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

const sketch = `{"version":1,"items":[{"type":"stroke","color":"#FF0000","width":0.01,"points":[0.123456,0.2,0.3,0.4]}]}`

func (f *fixture) upload(chatID, uploader int64, kind, mime string) uuid.UUID {
	f.t.Helper()
	a, err := f.pg.Attachments().Create(context.Background(), postgres.NewAttachment{
		UploaderID: &uploader, ChatID: &chatID, Kind: kind, StorageKey: "attachments/" + uuid.NewString(), MimeType: mime, Size: 10,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return a.ID
}

func (f *fixture) drawingOf(chatID, messageID int64, attachment uuid.UUID) string {
	f.t.Helper()
	found, err := f.pg.Attachments().Drawings(context.Background(), chatID, []int64{messageID})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, d := range found {
		if d.AttachmentID == attachment {
			return string(d.Drawing)
		}
	}
	return ""
}

func TestSendStoresANormalizedDrawingWithTheImage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	photo, plain := f.upload(chat.ID, alice, "image", "image/png"), f.upload(chat.ID, alice, "image", "image/png")
	sent, err := f.svc.Send(ctx, messages.SendRequest{
		ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile,
		AlbumIDs: []uuid.UUID{photo, plain}, Drawings: map[uuid.UUID][]byte{photo: []byte(sketch)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := f.drawingOf(chat.ID, sent.Message.ID, photo)
	if !strings.Contains(got, `"#ff0000"`) || !strings.Contains(got, "0.1235") {
		t.Fatalf("drawing not stored normalized: %s", got)
	}
	if f.drawingOf(chat.ID, sent.Message.ID, plain) != "" {
		t.Fatal("the image without a drawing got one")
	}
}

func TestDrawingsNeedImagesOfTheMessage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	photo := f.upload(chat.ID, alice, "image", "image/png")
	video := f.upload(chat.ID, alice, "video", "video/mp4")
	voice := f.upload(chat.ID, alice, "voice", "audio/ogg")
	drawn := func(id uuid.UUID) map[uuid.UUID][]byte { return map[uuid.UUID][]byte{id: []byte(sketch)} }
	cases := map[string]messages.SendRequest{
		"video":           {Type: scylla.TypeFile, AttachmentID: &video, Drawings: drawn(video)},
		"voice":           {Type: scylla.TypeVoice, AttachmentID: &voice, Drawings: drawn(voice)},
		"text":            {Type: scylla.TypeText, Content: str("hi"), Drawings: drawn(photo)},
		"not attached":    {Type: scylla.TypeFile, AttachmentID: &video, Drawings: drawn(photo)},
		"invalid drawing": {Type: scylla.TypeFile, AttachmentID: &photo, Drawings: map[uuid.UUID][]byte{photo: []byte(`{"version":1,"items":[]}`)}},
	}
	for name, r := range cases {
		r.ChatID, r.SenderID, r.ClientTempID = chat.ID, alice, uuid.NewString()
		if _, err := f.svc.Send(ctx, r); !errors.Is(err, messages.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMembersDrawOnASentImageByReplying(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	other, _, _ := f.pg.Chats().CreateDirect(ctx, bob, carol)
	photo := f.upload(chat.ID, alice, "image", "image/png")
	original, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile, AttachmentID: &photo})
	if err != nil {
		t.Fatal(err)
	}
	text := f.send(chat.ID, alice, uuid.NewString(), "no image here")
	reply := func(chatID, replyTo int64, drawings map[uuid.UUID][]byte) (messages.Sent, error) {
		return f.svc.Send(ctx, messages.SendRequest{
			ChatID: chatID, SenderID: bob, ClientTempID: uuid.NewString(), Type: scylla.TypeFile,
			AttachmentID: &photo, ReplyTo: &replyTo, Drawings: drawings,
		})
	}
	drawn := map[uuid.UUID][]byte{photo: []byte(sketch)}
	if _, err := reply(chat.ID, original.Message.ID, nil); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("reusing an image without drawing on it: %v", err)
	}
	if _, err := reply(chat.ID, text.ID, drawn); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("reusing an image of another message: %v", err)
	}
	if _, err := reply(other.ID, original.Message.ID, drawn); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("reusing an image in another chat: %v", err)
	}
	sent, err := reply(chat.ID, original.Message.ID, drawn)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Message.AttachmentID == nil || uuid.UUID(*sent.Message.AttachmentID) != photo {
		t.Fatalf("reply does not reuse the image: %+v", sent.Message)
	}
	if f.drawingOf(chat.ID, sent.Message.ID, photo) == "" || f.drawingOf(chat.ID, original.Message.ID, photo) != "" {
		t.Fatal("the drawing must belong to the reply only")
	}
	if err := f.svc.Delete(ctx, alice, chat.ID, original.Message.ID, messages.ScopeEveryone); err != nil {
		t.Fatal(err)
	}
	if _, err := reply(chat.ID, original.Message.ID, drawn); !errors.Is(err, messages.ErrInvalid) {
		t.Errorf("reusing an image of a deleted message: %v", err)
	}
}

func TestForwardKeepsTheDrawing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice, bob := f.user("alice"), f.user("bob")
	chat, _, _ := f.pg.Chats().CreateDirect(ctx, alice, bob)
	other, _, _ := f.pg.Chats().CreateDirect(ctx, alice, f.user("carol"))
	photo := f.upload(chat.ID, alice, "image", "image/png")
	sent, err := f.svc.Send(ctx, messages.SendRequest{
		ChatID: chat.ID, SenderID: alice, ClientTempID: uuid.NewString(), Type: scylla.TypeFile,
		AttachmentID: &photo, Drawings: map[uuid.UUID][]byte{photo: []byte(sketch)},
	})
	if err != nil {
		t.Fatal(err)
	}
	copies, err := f.svc.Forward(ctx, alice, sent.Message.ID, []int64{other.ID})
	if err != nil || len(copies) != 1 {
		t.Fatalf("forward: %v %v", copies, err)
	}
	if got := f.drawingOf(other.ID, copies[0].ID, photo); got != f.drawingOf(chat.ID, sent.Message.ID, photo) || got == "" {
		t.Fatalf("forwarded drawing: %q", got)
	}
}
