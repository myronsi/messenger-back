package media_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/media"
)

func BenchmarkUploadPhoto6000x4000(b *testing.B) {
	raw := photoJPEG(b, 6000, 4000)
	for _, bc := range []struct {
		orientation int
		latency     time.Duration
	}{{1, 0}, {1, 40 * time.Millisecond}, {6, 0}, {6, 40 * time.Millisecond}} {
		src := withExif(raw, bc.orientation)
		b.Run(fmt.Sprintf("orientation=%d/put_latency=%s", bc.orientation, bc.latency), func(b *testing.B) {
			svc := media.NewService(media.Options{
				Storage: newMemStorage(bc.latency), Attachments: newMemAttachments(),
				Prober: media.NewProber("/nonexistent", "/nonexistent"),
			})
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := svc.Upload(b.Context(), media.Upload{UploaderID: 1, Purpose: media.PurposeMessage, Filename: "photo.jpg", Body: bytes.NewReader(src)}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
