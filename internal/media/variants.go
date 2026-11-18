package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"strings"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	VariantNameDisplay   = "display"
	VariantNamePanel     = "panel"
	VariantNameMessage   = "message"
	VariantNameInline    = "inline"
	VariantNameOptimized = "optimized"
	VariantNamePoster    = "poster"
	ThumbnailWebPSide    = 480
	PanelSide            = 128
	MessageStickerSide   = 320
	InlineEmojiSide      = 64
	AvatarSizes          = "64,128,256"
)

var avatarSides = []int{64, 128, 256}

func AvatarVariant(side int) string {
	switch side {
	case 64:
		return "avatar_64"
	case 128:
		return "avatar_128"
	case 256:
		return "avatar_256"
	}
	return ""
}

func AvatarVariantFor(size int) string {
	for _, side := range avatarSides {
		if size <= side {
			return AvatarVariant(side)
		}
	}
	return ""
}

type variantSet struct {
	base     string
	puts     []put
	variants postgres.Variants
}

func newVariantSet(base string) *variantSet {
	return &variantSet{base: base, variants: postgres.Variants{}}
}

func extensionOf(contentType string) string {
	switch contentType {
	case "image/webp":
		return ".webp"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "video/mp4":
		return ".mp4"
	case "audio/mp4":
		return ".m4a"
	}
	return ".bin"
}

func (v *variantSet) add(name string, img EncodedImage) {
	key := v.base + "." + strings.ReplaceAll(name, "_", "-") + extensionOf(img.ContentType)
	v.puts = append(v.puts, put{key, bytes.NewReader(img.Data), int64(len(img.Data)), img.ContentType})
	v.variants[name] = postgres.StoredVariant{Key: key, ContentType: img.ContentType, Size: int64(len(img.Data)), Width: img.Width, Height: img.Height}
}

func (s *Service) addResized(ctx context.Context, v *variantSet, name string, src image.Image, side, quality int) {
	if src == nil {
		return
	}
	img, err := s.o.Prober.Resized(ctx, src, side, quality)
	if err != nil {
		if !errors.Is(err, ErrNoEncoder) {
			s.o.Log.WarnContext(ctx, "image variant", "variant", name, "error", err)
		}
		return
	}
	v.add(name, img)
}

func thumbnailContentType(key string) string {
	if strings.HasSuffix(key, ".webp") {
		return "image/webp"
	}
	return "image/jpeg"
}
