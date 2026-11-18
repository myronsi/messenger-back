package messages_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/messages"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

func TestVoiceAndVideoFollowTheRecipientsSetting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bob, stranger, friend, other := f.user("bob"), f.user("stranger"), f.user("friend"), f.user("other")
	direct, _, err := f.pg.Chats().CreateDirect(ctx, stranger, bob)
	if err != nil {
		t.Fatal(err)
	}
	friendChat, _, err := f.pg.Chats().CreateDirect(ctx, bob, friend)
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: stranger, Name: "source", MemberIDs: []int64{other}})
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := f.pg.Social().Privacy(ctx, []int64{bob})
	s := settings[bob]
	s.VoiceVideoMessages = postgres.VisibleSharedChats
	if _, err := f.pg.Social().UpdatePrivacy(ctx, s); err != nil {
		t.Fatal(err)
	}
	upload := func(chatID, uploader int64, mime, kind string) uuid.UUID {
		a, err := f.pg.Attachments().Create(ctx, postgres.NewAttachment{UploaderID: &uploader, ChatID: &chatID, Kind: kind, StorageKey: "attachments/" + uuid.NewString(), MimeType: mime, Size: 10})
		if err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	send := func(chatID, sender int64, typ, mime, kind string) (scylla.Message, error) {
		id := upload(chatID, sender, mime, kind)
		sent, err := f.svc.Send(ctx, messages.SendRequest{ChatID: chatID, SenderID: sender, ClientTempID: uuid.NewString(), Type: typ, AttachmentID: &id})
		return sent.Message, err
	}

	_, err = send(direct.ID, stranger, scylla.TypeVoice, "audio/ogg", "voice")
	var field *messages.FieldForbiddenError
	if !errors.Is(err, messages.ErrForbidden) || !errors.As(err, &field) || field.Field != "voice_video_messages" {
		t.Fatalf("voice to a non-contact: %v", err)
	}
	unsent, err := f.svc.ListUnsent(ctx, stranger, direct.ID)
	if err != nil || len(unsent) != 1 || unsent[0].ErrorCode != "forbidden" {
		t.Fatalf("refused voice is kept as unsent: %+v %v", unsent, err)
	}
	if _, err := send(direct.ID, stranger, scylla.TypeFile, "video/mp4", "video"); !errors.Is(err, messages.ErrVoiceVideoNotAllowed) {
		t.Fatalf("video to a non-contact: %v", err)
	}
	if _, err := send(direct.ID, stranger, scylla.TypeFile, "image/png", "image"); err != nil {
		t.Fatalf("a photo is not affected: %v", err)
	}
	f.send(direct.ID, stranger, "text", "hello")
	if _, err := send(friendChat.ID, friend, scylla.TypeVoice, "audio/ogg", "voice"); err != nil {
		t.Fatalf("voice from a contact: %v", err)
	}
	if _, err := send(direct.ID, bob, scylla.TypeVoice, "audio/ogg", "voice"); err != nil {
		t.Fatalf("the recipient's own setting does not limit what they send: %v", err)
	}
	inGroup, err := send(source.ID, stranger, scylla.TypeVoice, "audio/ogg", "voice")
	if err != nil {
		t.Fatal(err)
	}

	before := len(f.rec.created)
	if _, err := f.svc.ForwardMany(ctx, stranger, source.ID, []int64{inGroup.ID}, []int64{direct.ID}); !errors.Is(err, messages.ErrVoiceVideoNotAllowed) {
		t.Fatalf("forwarding voice to a non-contact: %v", err)
	}
	if len(f.rec.created) != before {
		t.Fatal("a refused forward wrote a copy")
	}

	allow := postgres.PrivacyException{OwnerID: bob, SettingKey: postgres.SettingVoiceVideo, TargetID: stranger, Effect: postgres.EffectAllow}
	if err := f.pg.Social().SetException(ctx, allow); err != nil {
		t.Fatal(err)
	}
	voice, err := send(direct.ID, stranger, scylla.TypeVoice, "audio/ogg", "voice")
	if err != nil {
		t.Fatalf("voice with an allow exception: %v", err)
	}
	if _, err := f.svc.ForwardMany(ctx, stranger, source.ID, []int64{inGroup.ID}, []int64{direct.ID}); err != nil {
		t.Fatalf("forward with an allow exception: %v", err)
	}
	if err := f.pg.Social().DeleteException(ctx, bob, postgres.SettingVoiceVideo, stranger); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Resend(ctx, stranger, direct.ID, voice.ID); !errors.Is(err, messages.ErrVoiceVideoNotAllowed) {
		t.Fatalf("resend after the exception is gone: %v", err)
	}

	s.VoiceVideoMessages = postgres.VisibleEveryone
	if _, err := f.pg.Social().UpdatePrivacy(ctx, s); err != nil {
		t.Fatal(err)
	}
	deny := postgres.PrivacyException{OwnerID: bob, SettingKey: postgres.SettingVoiceVideo, TargetID: friend, Effect: postgres.EffectDeny}
	if err := f.pg.Social().SetException(ctx, deny); err != nil {
		t.Fatal(err)
	}
	if _, err := send(direct.ID, stranger, scylla.TypeVoice, "audio/ogg", "voice"); err != nil {
		t.Fatalf("voice when everyone may: %v", err)
	}
	if _, err := send(friendChat.ID, friend, scylla.TypeVoice, "audio/ogg", "voice"); !errors.Is(err, messages.ErrVoiceVideoNotAllowed) {
		t.Fatalf("voice from a denied contact: %v", err)
	}
	group, err := f.pg.Chats().CreateGroup(ctx, postgres.NewGroup{OwnerID: friend, Name: "g", MemberIDs: []int64{bob}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := send(group.ID, friend, scylla.TypeVoice, "audio/ogg", "voice"); err != nil {
		t.Fatalf("groups are not affected: %v", err)
	}
	if _, err := send(group.ID, friend, scylla.TypeFile, "video/mp4", "video"); err != nil {
		t.Fatalf("videos in groups are not affected: %v", err)
	}
}
