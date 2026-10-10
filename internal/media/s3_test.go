package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
)

// testS3 connects to TEST_S3_ENDPOINT (with TEST_S3_ACCESS_KEY and TEST_S3_SECRET_KEY), or skips, and
// creates a bucket of its own.
func testS3(t *testing.T) *S3 {
	t.Helper()
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT is not set")
	}
	bucket := "test-" + strings.ReplaceAll(uuid.NewString()[:13], "-", "")
	s, err := NewS3(S3Options{Endpoint: endpoint, AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), Bucket: bucket, Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	t.Cleanup(func() {
		for obj := range s.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			_ = s.client.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
		}
		_ = s.client.RemoveBucket(ctx, bucket)
	})
	return s
}

func TestS3Storage(t *testing.T) {
	s := testS3(t)
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "attachments/one", strings.NewReader("hello s3"), 8, "text/plain"); err != nil {
		t.Fatal(err)
	}
	obj, err := s.Get(ctx, "attachments/one")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj.Body)
	_ = obj.Body.Close()
	if string(b) != "hello s3" || obj.Size != 8 {
		t.Fatalf("read %q (%d)", b, obj.Size)
	}
	if _, err := s.Get(ctx, "attachments/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	// A signed URL works without credentials and forces the download headers.
	u, err := s.SignedURL(ctx, "attachments/one", time.Minute, "application/octet-stream", `attachment; filename="one.txt"`)
	if err != nil || u == "" {
		t.Fatalf("signed url: %q %v", u, err)
	}
	resp, err := http.Get(u) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello s3" || resp.Header.Get("Content-Disposition") != `attachment; filename="one.txt"` ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("signed download: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if err := s.Delete(ctx, "attachments/one"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "attachments/one"); err != nil {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.Put(ctx, "../escape", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("accepted a bad key")
	}
}
