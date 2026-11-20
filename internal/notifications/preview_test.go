package notifications

import (
	"context"
	"errors"
	"testing"

	"github.com/gocql/gocql"
	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type mimeAttachments map[uuid.UUID]string

func (a mimeAttachments) Get(_ context.Context, id uuid.UUID) (postgres.Attachment, error) {
	mime, ok := a[id]
	if !ok {
		return postgres.Attachment{}, postgres.ErrNotFound
	}
	return postgres.Attachment{ID: id, MimeType: mime}, nil
}

type emojiImages map[int64]postgres.EmojiImage

func (e emojiImages) Emoji(_ context.Context, id int64) (postgres.EmojiImage, error) {
	img, ok := e[id]
	if !ok {
		return postgres.EmojiImage{}, postgres.ErrNotFound
	}
	return img, nil
}

func TestFindImagePicksWhatTheNotificationShows(t *testing.T) {
	photo, video, pdf, sticker, emojiPic := gocql.TimeUUID(), gocql.TimeUUID(), gocql.TimeUUID(), gocql.TimeUUID(), uuid.New()
	attachments := mimeAttachments{uuid.UUID(photo): "image/jpeg", uuid.UUID(video): "video/mp4", uuid.UUID(pdf): "application/pdf"}
	emojis := emojiImages{42: {CustomEmoji: postgres.CustomEmoji{ID: 42, AttachmentID: emojiPic}}, 43: {Deleted: true}}
	text := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		m    scylla.Message
		want *ImageSource
	}{
		{"photo", scylla.Message{Type: scylla.TypeFile, AttachmentID: &photo}, &ImageSource{Attachment: uuid.UUID(photo), Size: ImageDisplay}},
		{"video", scylla.Message{Type: scylla.TypeFile, AttachmentID: &video}, &ImageSource{Attachment: uuid.UUID(video), Size: ImageThumbnail}},
		{"album starts with the first item", scylla.Message{Type: scylla.TypeFile, AlbumIDs: []gocql.UUID{video, photo}}, &ImageSource{Attachment: uuid.UUID(video), Size: ImageThumbnail}},
		{"sticker", scylla.Message{Type: scylla.TypeSticker, AttachmentID: &sticker}, &ImageSource{Attachment: uuid.UUID(sticker)}},
		{"one custom emoji", scylla.Message{Type: scylla.TypeText, Content: text(" <:party_cat:42> ")}, &ImageSource{Attachment: emojiPic}},
		{"custom emoji with text", scylla.Message{Type: scylla.TypeText, Content: text("hi <:party_cat:42>")}, nil},
		{"deleted custom emoji", scylla.Message{Type: scylla.TypeText, Content: text("<:gone:43>")}, nil},
		{"document", scylla.Message{Type: scylla.TypeFile, AttachmentID: &pdf}, nil},
		{"voice", scylla.Message{Type: scylla.TypeVoice, AttachmentID: &photo}, nil},
		{"deleted message", scylla.Message{Type: scylla.TypeFile, AttachmentID: &photo, Deleted: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FindImage(context.Background(), tc.m, attachments, emojis)
			if tc.want == nil {
				if !errors.Is(err, ErrNoImage) {
					t.Fatalf("want no picture, got %+v %v", got, err)
				}
				return
			}
			if err != nil || got != *tc.want {
				t.Fatalf("got %+v %v want %+v", got, err, *tc.want)
			}
		})
	}
}
