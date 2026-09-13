package s3_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/s3"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadRequiresCredentials(t *testing.T) {
	l := confx.New("")
	s3.Load(l)
	err := l.Err()
	for _, want := range []string{"S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %s in it", err, want)
		}
	}
}

func TestInitWithoutEndpoint(t *testing.T) {
	if err := s3.New(s3.Config{}).Init(context.Background(), platform.NewApp(nil)); err == nil {
		t.Fatal("no endpoint was accepted")
	}
}

// storage connects to the MinIO of S3_TEST_ENDPOINT, as taply's docker-compose runs it.
func storage(t *testing.T) (*s3.Storage, s3.Config) {
	t.Helper()
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT is not set")
	}
	cfg := s3.Config{Endpoint: endpoint, AccessKey: "minioadmin", SecretKey: "minioadmin"}
	app := platform.NewApp(nil)
	m := s3.New(cfg)
	if err := m.Init(context.Background(), app); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	return s3.From(app), cfg
}

func bucketName(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// A public bucket is served without signing, the way nginx serves taply's images.
func TestPublicBucket(t *testing.T) {
	st, _ := storage(t)
	ctx := context.Background()
	bucket := bucketName("images")

	path, err := st.Put(ctx, bucket, true, "restaurants/1/images/logo.png", "image/png", []byte("png bytes"))
	if err != nil || path != "restaurants/1/images/logo.png" {
		t.Fatalf("Put = %q, %v", path, err)
	}
	code, body := httpGet(t, st.PublicURL(bucket, path))
	if code != http.StatusOK || body != "png bytes" {
		t.Errorf("anonymous GET = %d %q", code, body)
	}

	if err := st.Delete(ctx, bucket, path); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if code, _ := httpGet(t, st.PublicURL(bucket, path)); code != http.StatusNotFound {
		t.Errorf("after Delete = %d", code)
	}
	// Deleting what is already gone is not an error.
	if err := st.Delete(ctx, bucket, path); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

func TestPrivateBucket(t *testing.T) {
	st, _ := storage(t)
	ctx := context.Background()
	bucket := bucketName("receipts")

	if _, err := st.Put(ctx, bucket, false, "2026/09/receipt.pdf", "application/pdf", []byte("pdf bytes")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if code, _ := httpGet(t, st.PublicURL(bucket, "2026/09/receipt.pdf")); code != http.StatusForbidden {
		t.Errorf("anonymous GET of a private file = %d", code)
	}

	signed, err := st.SignedURL(ctx, bucket, "2026/09/receipt.pdf", time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if code, body := httpGet(t, signed); code != http.StatusOK || body != "pdf bytes" {
		t.Errorf("signed GET = %d %q", code, body)
	}

	r, err := st.Get(ctx, bucket, "2026/09/receipt.pdf")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	raw, _ := io.ReadAll(r)
	r.Close()
	if string(raw) != "pdf bytes" {
		t.Errorf("Get = %q", raw)
	}

	if _, err := st.Get(ctx, bucket, "missing.pdf"); !errors.Is(err, s3.ErrNotFound) {
		t.Errorf("Get of a missing file: %v", err)
	}
}

// A policy tightened by hand survives the next upload: the public policy is set only
// when the bucket is created.
func TestExistingBucketPolicyIsKept(t *testing.T) {
	st, _ := storage(t)
	ctx := context.Background()
	bucket := bucketName("tightened")

	if _, err := st.Put(ctx, bucket, false, "a.txt", "text/plain", []byte("a")); err != nil {
		t.Fatal(err)
	}
	// A second storage instance, as after a restart, asks for a public bucket.
	again, _ := storage(t)
	if _, err := again.Put(ctx, bucket, true, "b.txt", "text/plain", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if code, _ := httpGet(t, st.PublicURL(bucket, "b.txt")); code != http.StatusForbidden {
		t.Errorf("an existing private bucket became public: %d", code)
	}
}

func TestPutStreamOfUnknownSize(t *testing.T) {
	st, _ := storage(t)
	ctx := context.Background()
	bucket := bucketName("videos")

	payload := strings.Repeat("frame", 200_000) // a megabyte, streamed without a length
	if _, err := st.PutStream(ctx, bucket, false, "screensaver.mp4", "video/mp4", strings.NewReader(payload), -1); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	r, err := st.Get(ctx, bucket, "screensaver.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw, _ := io.ReadAll(r)
	if len(raw) != len(payload) {
		t.Errorf("read %d bytes, want %d", len(raw), len(payload))
	}
}

// Wrong keys stop the start instead of the first upload.
func TestInitRejectsWrongKeys(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("S3_TEST_ENDPOINT is not set")
	}
	err := s3.New(s3.Config{Endpoint: endpoint, AccessKey: "minioadmin", SecretKey: "wrong"}).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestPublicURL(t *testing.T) {
	st, cfg := storage(t)
	_ = cfg
	if got := st.PublicURL("images", "/a/b.png"); !strings.HasSuffix(got, "/images/a/b.png") || !strings.HasPrefix(got, "http://") {
		t.Errorf("PublicURL = %s", got)
	}
}
