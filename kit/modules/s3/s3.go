// Package s3 is a platform module: S3 compatible object storage — MinIO locally, any S3
// in production — for the files of the project.
//
// The storage API is taply's: Put into a bucket that is created on first use, with an
// anonymous read policy when the files are served publicly, and Delete. On top of it
// come streaming uploads, reads and signed links.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/internal/startup"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	Endpoint  string // host:port, without the scheme
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string

	// PublicURL is where public buckets are served from, for example the /storage/ path
	// of the nginx in front of the service. Empty means links point at the endpoint.
	PublicURL string

	// StartupTimeout is how long the start keeps retrying a storage that does not
	// answer: a storage that comes up a moment after the instance, as in a rolling
	// deploy, does not stop the start. Each attempt waits up to 5 seconds, pauses grow
	// from 250ms to 2s, and no attempt starts after StartupTimeout. A refusal of the
	// storage itself, such as wrong keys, stops the start at once. Zero is a single
	// attempt, the behaviour before v0.11.2; Load sets 15 seconds.
	StartupTimeout time.Duration

	// Optional starts the service even when the storage does not answer: the start
	// logs a warning instead of failing, /health reports s3 as degraded with status
	// 200 while it is unreachable, and file operations return its errors. For a
	// service whose files are a side feature.
	Optional bool
}

// DefaultStartupTimeout is the StartupTimeout Load uses when S3_STARTUP_TIMEOUT is unset.
const DefaultStartupTimeout = 15 * time.Second

// probeTimeout bounds one check of the storage, at startup and in /health.
const probeTimeout = 5 * time.Second

// Load reads the module settings from environment variables. The names are taply's.
func Load(l *confx.Loader) Config {
	cfg := Config{
		Endpoint:  l.Required("S3_ENDPOINT"),
		AccessKey: l.Required("S3_ACCESS_KEY"),
		SecretKey: l.Required("S3_SECRET_KEY"),
		UseSSL:    l.Bool("S3_USE_SSL", false),
		Region:    l.String("S3_REGION", ""),
		PublicURL: l.String("S3_PUBLIC_URL", ""),

		StartupTimeout: l.Duration("S3_STARTUP_TIMEOUT", DefaultStartupTimeout),
		Optional:       l.Bool("S3_OPTIONAL", false),
	}
	if cfg.StartupTimeout < 0 {
		l.Fail(fmt.Errorf("%s: %s is negative", l.Key("S3_STARTUP_TIMEOUT"), cfg.StartupTimeout))
	}
	return cfg
}

// anonymousReadPolicy grants s3:GetObject to anyone for every object in a bucket — what
// `mc anonymous set download <bucket>` does. It lets nginx serve images without signing.
const anonymousReadPolicy = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"AWS": ["*"]},
      "Action": ["s3:GetObject"],
      "Resource": ["arn:aws:s3:::%s/*"]
    }
  ]
}`

// Storage stores files in buckets.
type Storage struct {
	cli     *minio.Client
	cfg     Config
	mu      sync.Mutex
	ensured map[string]bool
}

// Put stores an object and returns its path. The bucket is created on first use; with
// anonymousRead a new bucket gets a public read policy. An existing bucket keeps the
// policy it has, so an operator who tightened it by hand is not overruled.
func (s *Storage) Put(ctx context.Context, bucket string, anonymousRead bool, objectPath, contentType string, b []byte) (string, error) {
	return s.PutStream(ctx, bucket, anonymousRead, objectPath, contentType, bytes.NewReader(b), int64(len(b)))
}

// PutStream stores an object read from r, for files too large to hold in memory. A
// negative size streams an object of unknown length in parts.
func (s *Storage) PutStream(ctx context.Context, bucket string, anonymousRead bool, objectPath, contentType string, r io.Reader, size int64) (string, error) {
	if err := s.ensureBucket(ctx, bucket, anonymousRead); err != nil {
		return "", err
	}
	if _, err := s.cli.PutObject(ctx, bucket, objectPath, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return "", fmt.Errorf("s3: put %s/%s: %w", bucket, objectPath, err)
	}
	return objectPath, nil
}

// Get opens an object for reading. ErrNotFound is returned when it does not exist.
func (s *Storage) Get(ctx context.Context, bucket, objectPath string) (io.ReadCloser, error) {
	obj, err := s.cli.GetObject(ctx, bucket, objectPath, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3: get %s/%s: %w", bucket, objectPath, err)
	}
	// GetObject is lazy: the first Stat is what reaches the server.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).StatusCode == 404 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("s3: get %s/%s: %w", bucket, objectPath, err)
	}
	return obj, nil
}

// ErrNotFound is returned for an object that does not exist.
var ErrNotFound = errors.New("s3: object not found")

// Delete removes an object. Removing an object that does not exist is not an error.
func (s *Storage) Delete(ctx context.Context, bucket, objectName string) error {
	if err := s.cli.RemoveObject(ctx, bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3: delete %s/%s: %w", bucket, objectName, err)
	}
	return nil
}

// SignedURL returns a link to a private object that works for ttl.
func (s *Storage) SignedURL(ctx context.Context, bucket, objectPath string, ttl time.Duration) (string, error) {
	u, err := s.cli.PresignedGetObject(ctx, bucket, objectPath, ttl, url.Values{})
	if err != nil {
		return "", fmt.Errorf("s3: sign %s/%s: %w", bucket, objectPath, err)
	}
	return u.String(), nil
}

// PublicURL returns the link to an object of a public bucket.
func (s *Storage) PublicURL(bucket, objectPath string) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		scheme := "http"
		if s.cfg.UseSSL {
			scheme = "https"
		}
		base = scheme + "://" + s.cfg.Endpoint
	}
	return base + "/" + bucket + "/" + strings.TrimLeft(objectPath, "/")
}

// Client returns the MinIO client for what Storage does not cover.
func (s *Storage) Client() *minio.Client { return s.cli }

func (s *Storage) ensureBucket(ctx context.Context, bucket string, anonymousRead bool) error {
	if bucket == "" {
		return errors.New("s3: an empty bucket name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ensured[bucket] {
		return nil
	}

	exists, err := s.cli.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("s3: bucket %s: %w", bucket, err)
	}
	if !exists {
		if err := s.cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: s.cfg.Region}); err != nil {
			// Another instance may have created it a moment ago.
			if code := minio.ToErrorResponse(err).Code; code != "BucketAlreadyOwnedByYou" && code != "BucketAlreadyExists" {
				return fmt.Errorf("s3: create bucket %s: %w", bucket, err)
			}
		} else if anonymousRead {
			if err := s.cli.SetBucketPolicy(ctx, bucket, fmt.Sprintf(anonymousReadPolicy, bucket)); err != nil {
				return fmt.Errorf("s3: public read policy on %s: %w", bucket, err)
			}
		}
	}
	s.ensured[bucket] = true
	return nil
}

// Module implements platform.Module.
type Module struct {
	cfg     Config
	storage *Storage
}

// New creates the module from ready settings.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

func (m *Module) Name() string { return "s3" }

// Init connects to the storage and checks that it answers, so a wrong address or key
// stops the start instead of the first upload. A storage that does not answer yet is
// retried within StartupTimeout; with Optional the start goes on without it.
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	if m.cfg.Endpoint == "" {
		return errors.New("s3: endpoint is not set")
	}
	cli, err := minio.New(m.cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(m.cfg.AccessKey, m.cfg.SecretKey, ""),
		Secure: m.cfg.UseSSL,
		Region: m.cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("s3: client: %w", err)
	}
	m.storage = &Storage{cli: cli, cfg: m.cfg, ensured: map[string]bool{}}

	log := app.Logger().With("module", "s3")
	if m.cfg.Optional {
		if err := m.probe(ctx); err != nil {
			log.Warn("s3: storage is unreachable, starting without it", "err", err)
		}
	} else if err := startup.Retry(ctx, log, "s3: storage", m.cfg.StartupTimeout, m.probe, refused); err != nil {
		return err
	}
	platform.Provide(app, m.storage)
	return nil
}

// refused reports an answer of the storage itself, such as wrong keys or a denied
// request: retrying it would not help. A storage that does not answer, or answers
// 5xx while it starts, is retried.
func refused(err error) bool {
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	code := resp.StatusCode
	return code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusTooManyRequests
}

// Health checks that the storage answers and accepts the keys. With Optional a failure
// is reported as degraded rather than failed.
func (m *Module) Health(ctx context.Context) error {
	if m.storage == nil {
		return errors.New("s3: not connected")
	}
	err := m.probe(ctx)
	if m.cfg.Optional {
		return platform.Degraded(err)
	}
	return err
}

func (m *Module) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if _, err := m.storage.cli.BucketExists(ctx, "platform-health-probe"); err != nil {
		return fmt.Errorf("s3: storage is unreachable: %w", err)
	}
	return nil
}

// From returns the storage from the container.
func From(app *platform.App) *Storage { return platform.Get[*Storage](app) }
