package media_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/media"
	"github.com/myronsi/messenger-back/internal/store/postgres"
)

type memStorage struct {
	latency time.Duration
	failPut func(key string) error
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
}

func newMemStorage(latency time.Duration) *memStorage {
	return &memStorage{latency: latency, objects: map[string][]byte{}}
}

func (m *memStorage) Put(ctx context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.failPut != nil {
		if err := m.failPut(key); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *memStorage) Get(_ context.Context, key string) (media.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return media.Object{}, media.ErrNotFound
	}
	return media.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}, nil
}

func (m *memStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	m.deleted = append(m.deleted, key)
	return nil
}

func (m *memStorage) SignedURL(context.Context, string, time.Duration, string, string) (string, error) {
	return "", nil
}

func (m *memStorage) keys() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.objects))
	for k, v := range m.objects {
		out[k] = len(v)
	}
	return out
}

type memAttachments struct {
	postgres.AttachmentRepository
	mu   sync.Mutex
	rows map[uuid.UUID]postgres.Attachment
	fail error
}

func newMemAttachments() *memAttachments {
	return &memAttachments{rows: map[uuid.UUID]postgres.Attachment{}}
}

func (m *memAttachments) Create(_ context.Context, n postgres.NewAttachment) (postgres.Attachment, error) {
	if m.fail != nil {
		return postgres.Attachment{}, m.fail
	}
	a := postgres.Attachment{
		ID: uuid.New(), UploaderID: n.UploaderID, ChatID: n.ChatID, Purpose: n.Purpose, Kind: n.Kind, Filename: n.Filename,
		StorageKey: n.StorageKey, ThumbnailKey: n.ThumbnailKey, MimeType: n.MimeType, Size: n.Size, Width: n.Width,
		Height: n.Height, Duration: n.Duration, Waveform: n.Waveform, CreatedAt: time.Now(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[a.ID] = a
	return a, nil
}

func (m *memAttachments) Get(_ context.Context, id uuid.UUID) (postgres.Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok {
		return postgres.Attachment{}, postgres.ErrNotFound
	}
	return a, nil
}

func photoJPEG(tb testing.TB, w, h int) []byte {
	tb.Helper()
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	seed := uint32(2463534242)
	for y := range h {
		for x := range w {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			img.Y[y*img.YStride+x] = uint8((x*255/w+y*255/h)/2) + uint8(seed&0x1F)
		}
	}
	for i := range img.Cb {
		img.Cb[i] = uint8(96 + i%64)
		img.Cr[i] = uint8(160 - i%48)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 92}); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

func withExif(raw []byte, orientation int) []byte {
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00")
	tiff = binary.LittleEndian.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	payload = append(payload, []byte("GPSLatitude=48.137")...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(payload)+2))
	app1 = append(app1, payload...)
	return append(append(append([]byte{}, raw[:2]...), app1...), raw[2:]...)
}
