package s3_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/modules/s3"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestModuleBasics(t *testing.T) {
	m := s3.New(s3.Config{})
	if m.Name() != "s3" {
		t.Fatalf("name = %q", m.Name())
	}
	if err := m.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("health before Init: %v", err)
	}
	// minio refuses an endpoint with a path before any request is made.
	err := s3.New(s3.Config{Endpoint: "localhost:9000/storage"}).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "s3: client") {
		t.Fatalf("endpoint with a path: %v", err)
	}
}

// Links of a public bucket point at PUBLIC_URL when it is set: the nginx in front.
func TestPublicURLBehindAProxy(t *testing.T) {
	cfg := testConfig(t)
	cfg.PublicURL = "https://cdn.example.kz/storage/"
	app := platform.NewApp(nil)
	if err := s3.New(cfg).Init(context.Background(), app); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got, want := s3.From(app).PublicURL("images", "/a.png"), "https://cdn.example.kz/storage/images/a.png"; got != want {
		t.Fatalf("PublicURL = %s, want %s", got, want)
	}
}

func TestStorageErrors(t *testing.T) {
	st, _ := storage(t)
	if st.Client() == nil {
		t.Fatal("Client() is nil")
	}
	ctx := context.Background()

	if _, err := st.Put(ctx, "", false, "a.txt", "text/plain", []byte("a")); err == nil || !strings.Contains(err.Error(), "empty bucket") {
		t.Errorf("empty bucket: %v", err)
	}
	if _, err := st.Put(ctx, "Not_A_Bucket", false, "a.txt", "text/plain", []byte("a")); err == nil || !strings.Contains(err.Error(), "s3: bucket Not_A_Bucket") {
		t.Errorf("invalid bucket name: %v", err)
	}
	if _, err := st.Get(ctx, "Not_A_Bucket", "a.txt"); err == nil || !strings.Contains(err.Error(), "s3: get") {
		t.Errorf("Get from an invalid bucket: %v", err)
	}
	if err := st.Delete(ctx, "Not_A_Bucket", "a.txt"); err == nil || !strings.Contains(err.Error(), "s3: delete") {
		t.Errorf("Delete from an invalid bucket: %v", err)
	}
	if _, err := st.SignedURL(ctx, "Not_A_Bucket", "a.txt", time.Minute); err == nil || !strings.Contains(err.Error(), "s3: sign") {
		t.Errorf("SignedURL of an invalid bucket: %v", err)
	}

	// The bucket is known after the first Put, so a later failure comes from the upload.
	bucket := bucketName(t, st, "errors")
	if _, err := st.Put(ctx, bucket, false, "a.txt", "text/plain", []byte("a")); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := st.Put(cancelled, bucket, false, "b.txt", "text/plain", []byte("b")); err == nil || !strings.Contains(err.Error(), "s3: put "+bucket+"/b.txt") {
		t.Errorf("Put with a cancelled context: %v", err)
	}
	if _, err := st.Get(cancelled, bucket, "a.txt"); err == nil || strings.Contains(err.Error(), "not found") {
		t.Errorf("Get with a cancelled context: %v", err)
	}
}
