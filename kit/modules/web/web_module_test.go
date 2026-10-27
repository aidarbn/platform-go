package web_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/web"
	"github.com/aidarbn/platform-go/kit/platform"
)

func key(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func TestLoadDecodesEveryBase64Flavour(t *testing.T) {
	raw := key(33) // 33 bytes: padded and unpadded encodings differ
	for name, enc := range map[string]*base64.Encoding{
		"std": base64.StdEncoding, "raw std": base64.RawStdEncoding,
		"url": base64.URLEncoding, "raw url": base64.RawURLEncoding,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("WEB_SECRET_KEY", enc.EncodeToString(raw))
			t.Setenv("WEB_INSECURE_COOKIES", "true")
			l := confx.New("")
			cfg := web.Load(l)
			if err := l.Err(); err != nil {
				t.Fatal(err)
			}
			if string(cfg.SecretKey) != string(raw) || !cfg.InsecureCookies {
				t.Fatalf("cfg = %+v", cfg)
			}
		})
	}
}

func TestLoadRejectsBadKeys(t *testing.T) {
	for name, tc := range map[string]struct{ value, want string }{
		"missing":    {"", "WEB_SECRET_KEY: required"},
		"short":      {base64.StdEncoding.EncodeToString(key(16)), "16 bytes, at least 32"},
		"not base64": {"not base64 at all!", "openssl rand -base64 32"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("WEB_SECRET_KEY", tc.value)
			l := confx.New("")
			cfg := web.Load(l)
			err := l.Err()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if cfg.SecretKey != nil {
				t.Fatalf("key = %x", cfg.SecretKey)
			}
		})
	}
}

func TestLoadReportsThePrefixedName(t *testing.T) {
	t.Setenv("SHOP_WEB_SECRET_KEY", "short")
	l := confx.New("SHOP")
	web.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "SHOP_WEB_SECRET_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitProvidesTheKit(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		app := platform.NewApp(nil)
		m := web.New(web.Config{SecretKey: key(32), InsecureCookies: insecure})
		if m.Name() != "web" {
			t.Fatalf("name = %q", m.Name())
		}
		if err := m.Init(context.Background(), app); err != nil {
			t.Fatal(err)
		}
		kit := web.From(app)
		if kit.CSRF == nil || kit.Cookies == nil || kit.Secure == insecure {
			t.Fatalf("insecure %v: kit = %+v", insecure, kit)
		}
	}
}

func TestInitRejectsAShortKey(t *testing.T) {
	err := web.New(web.Config{SecretKey: key(8)}).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.HasPrefix(err.Error(), "web: ") {
		t.Fatalf("err = %v", err)
	}
}
