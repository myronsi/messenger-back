package notifications

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/emoji"
	"github.com/myronsi/messenger-back/internal/store/postgres"
	"github.com/myronsi/messenger-back/internal/store/scylla"
)

type ImageSize int

const (
	ImageOriginal ImageSize = iota
	ImageDisplay
	ImageThumbnail
)

type ImageSource struct {
	Attachment uuid.UUID
	Size       ImageSize
}

type AttachmentGetter interface {
	Get(ctx context.Context, id uuid.UUID) (postgres.Attachment, error)
}

type EmojiGetter interface {
	Emoji(ctx context.Context, id int64) (postgres.EmojiImage, error)
}

var ErrNoImage = errors.New("message has no picture to show")

func SingleCustomEmoji(m scylla.Message) (int64, bool) {
	if m.Type != scylla.TypeText || m.Content == nil {
		return 0, false
	}
	text := strings.TrimSpace(*m.Content)
	tokens := emoji.Parse(text)
	if len(tokens) != 1 || tokens[0].Start != 0 || tokens[0].End != len(text) {
		return 0, false
	}
	return tokens[0].ID, true
}

func HasImage(m scylla.Message, contentType string) bool {
	if m.Deleted {
		return false
	}
	switch contentType {
	case ContentPhoto, ContentVideo, ContentAlbum, ContentSticker:
		return true
	}
	_, ok := SingleCustomEmoji(m)
	return ok
}

func FindImage(ctx context.Context, m scylla.Message, attachments AttachmentGetter, emojis EmojiGetter) (ImageSource, error) {
	if m.Deleted {
		return ImageSource{}, ErrNoImage
	}
	if id, ok := SingleCustomEmoji(m); ok {
		e, err := emojis.Emoji(ctx, id)
		if errors.Is(err, postgres.ErrNotFound) || err == nil && e.Deleted {
			return ImageSource{}, ErrNoImage
		}
		if err != nil {
			return ImageSource{}, err
		}
		return ImageSource{Attachment: e.AttachmentID}, nil
	}
	ids := m.AttachmentIDs()
	if len(ids) == 0 || m.Type == scylla.TypeVoice {
		return ImageSource{}, ErrNoImage
	}
	if m.Type == scylla.TypeSticker {
		return ImageSource{Attachment: uuid.UUID(ids[0])}, nil
	}
	a, err := attachments.Get(ctx, uuid.UUID(ids[0]))
	if errors.Is(err, postgres.ErrNotFound) {
		return ImageSource{}, ErrNoImage
	}
	if err != nil {
		return ImageSource{}, err
	}
	switch {
	case strings.HasPrefix(a.MimeType, "image/"):
		return ImageSource{Attachment: a.ID, Size: ImageDisplay}, nil
	case strings.HasPrefix(a.MimeType, "video/"):
		return ImageSource{Attachment: a.ID, Size: ImageThumbnail}, nil
	}
	return ImageSource{}, ErrNoImage
}
