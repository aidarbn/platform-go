package i18nx_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
	"github.com/aidarbn/platform-go/kit/i18nx/internal/testpb"
	"github.com/aidarbn/platform-go/kit/pgdb"
)

func withLanguage(header string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("accept-language", header))
}

func TestResolveLocale(t *testing.T) {
	c := i18nx.New(nil, i18nx.LocaleRU, i18nx.LocaleKK, i18nx.LocaleEN)

	for header, want := range map[string]i18nx.Locale{
		"":                     "ru",
		"kk":                   "kk",
		"kk-KZ":                "kk",
		"fr-FR":                "ru", // unsupported: the default
		"fr;q=0.9, en;q=0.8":   "en",
		"en;q=0.5, kk;q=0.9":   "kk",
		"*":                    "ru",
		"not a language!!!,;;": "ru",
	} {
		if got := c.ResolveLocale(withLanguage(header)); got != want {
			t.Errorf("Accept-Language %q: %s, want %s", header, got, want)
		}
	}
	// A locale set in code wins over the header.
	if got := c.ResolveLocale(i18nx.WithLocale(withLanguage("kk"), i18nx.LocaleEN)); got != "en" {
		t.Errorf("WithLocale: %s", got)
	}
	if got := c.Locales(); len(got) != 3 || got[0] != "ru" {
		t.Errorf("locales = %v", got)
	}
}

func client(t *testing.T) *i18nx.Client {
	t.Helper()
	c := i18nx.New(nil, i18nx.LocaleRU, i18nx.LocaleKK, i18nx.LocaleEN)
	if err := c.LoadMessages([]byte(i18nx.Builtin)); err != nil {
		t.Fatalf("builtin: %v", err)
	}
	if err := c.LoadMessages([]byte(`
error.resource.product: {en: product, ru: продукт, kk: өнім}
error.field.name: {en: name, ru: название, kk: атау}
error.msg.product is out of stock: {en: product is out of stock, ru: товар не в наличии, kk: өнім қоймада жоқ}
error.tmpl.image_too_large: {en: "image is too large: %s; maximum allowed is %s", ru: "изображение слишком большое: %s; максимально допустимо %s", kk: "сурет тым үлкен: %s; рұқсат етілген ең үлкені %s"}
`)); err != nil {
		t.Fatalf("messages: %v", err)
	}
	return c
}

// The four layers of taply's error translation, checked in order.
func TestTranslateMessage(t *testing.T) {
	c := client(t)
	ru := i18nx.LocaleRU
	for msg, want := range map[string]string{
		"product is out of stock": "товар не в наличии",
		"not found":               "не найден",
		"product 42: not found":   "продукт 42: не найден",
		"name is required":        "название обязательно",
		"missing name":            "отсутствует название",
		"missing phone":           "отсутствует phone", // unknown field stays as is
		"image is too large: 26.0 MB; maximum allowed is 10 MB": "изображение слишком большое: 26.0 MB; максимально допустимо 10 MB",
		"something nobody translated":                           "something nobody translated",
		"insufficient role for /shop.v1.Orders/Create":          "недостаточно прав для /shop.v1.Orders/Create",
	} {
		if got := c.TranslateMessage(msg, ru); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
	// A locale without a translation falls back to the default one.
	if got := c.TranslateMessage("not found", i18nx.LocaleTR); got != "не найден" {
		t.Errorf("fallback: %q", got)
	}
}

func TestTranslateError(t *testing.T) {
	c := client(t)
	err := c.TranslateError(status.Error(codes.NotFound, "product 7: not found"), i18nx.LocaleKK)
	if st, _ := status.FromError(err); st.Code() != codes.NotFound || st.Message() != "өнім 7: табылмады" {
		t.Errorf("translated = %v", err)
	}
	// Internal errors are for developers and keep their text.
	internal := status.Error(codes.Internal, "not found")
	if got := c.TranslateError(internal, i18nx.LocaleRU); got != internal {
		t.Errorf("internal = %v", got)
	}
	plain := errors.New("not found")
	if got := c.TranslateError(plain, i18nx.LocaleRU); got != plain {
		t.Errorf("plain error = %v", got)
	}
}

func TestLoadMessagesChecksTemplates(t *testing.T) {
	c := i18nx.New(nil, i18nx.LocaleRU)
	err := c.LoadMessages([]byte(`
error.tmpl.bad: {en: "size %s of %s", ru: "размер %s"}
error.tmpl.no_en: {ru: "размер %s"}
error.msg.empty: {}
`))
	if err == nil {
		t.Fatal("broken messages were accepted")
	}
	for _, want := range []string{"error.tmpl.bad: ru has a different number of %s", "error.tmpl.no_en: a template needs its English skeleton", "error.msg.empty: no translations"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
	if err := c.LoadMessages([]byte("not: [yaml")); err == nil {
		t.Error("broken YAML was accepted")
	}
}

// An editor's request carries every locale: the base value is copied into the default
// locale so the column and the translation never disagree.
func TestRequestInterceptorMirrorsBaseValue(t *testing.T) {
	c := i18nx.New(nil, i18nx.LocaleRU)
	req := &testpb.ProductEditor{
		Id:   1,
		Name: "  Латте  ",
		FieldTranslations: map[string]*i18npb.LocaleMap{
			"name": {Values: map[string]string{"ru": "stale", "kk": "Латте kk"}},
		},
		Variants: []*testpb.ProductEditor{{Id: 2, Name: "Большой"}},
	}
	_, err := c.RequestInterceptor()(context.Background(), req, &grpc.UnaryServerInfo{}, func(_ context.Context, r any) (any, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := req.GetFieldTranslations()["name"].GetValues(); got["ru"] != "Латте" || got["kk"] != "Латте kk" {
		t.Errorf("translations = %v", got)
	}
	if got := req.GetVariants()[0].GetFieldTranslations()["name"].GetValues()["ru"]; got != "Большой" {
		t.Errorf("nested = %q", got)
	}
}

func TestValidateFieldsAndConversions(t *testing.T) {
	m := map[string]*i18npb.LocaleMap{"name": {Values: map[string]string{"kk": "Атау"}}}
	if err := i18nx.ValidateFields(m, []string{"name", "description"}); err != nil {
		t.Errorf("ValidateFields: %v", err)
	}
	if err := i18nx.ValidateFields(map[string]*i18npb.LocaleMap{"price": {}}, []string{"name"}); err == nil {
		t.Error("an unknown field was accepted")
	}
	tr := i18nx.ProtoToTranslations(m)
	if tr["name"]["kk"] != "Атау" {
		t.Errorf("ProtoToTranslations = %v", tr)
	}
	if back := i18nx.TranslationsToProto(tr); back["name"].GetValues()["kk"] != "Атау" {
		t.Errorf("TranslationsToProto = %v", back)
	}
}

// --- against a real database ----------------------------------------------------------

func database(t *testing.T) *i18nx.Client {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("i18n_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	if err := i18nx.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := i18nx.EnsureSchema(ctx, pool); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	c := i18nx.New(pool, i18nx.LocaleRU, i18nx.LocaleKK, i18nx.LocaleEN)
	if err := c.LoadMessages([]byte(i18nx.Builtin)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStorage(t *testing.T) {
	c := database(t)
	ctx := context.Background()

	if err := c.SetBatch(ctx, "product", "1", i18nx.Translations{"name": {"kk": "Латте", "en": "Latte"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "product", "1", "name", "en", "Caffe latte"); err != nil {
		t.Fatal(err)
	}
	// A sync seeds missing translations and keeps what an editor changed.
	if err := c.SetBatchIfMissing(ctx, "product", "1", i18nx.Translations{"name": {"en": "overwritten?", "tr": "Latte tr"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetAll(ctx, "product", "1")
	if err != nil {
		t.Fatal(err)
	}
	if got["name"]["en"] != "Caffe latte" || got["name"]["kk"] != "Латте" || got["name"]["tr"] != "Latte tr" {
		t.Errorf("GetAll = %v", got)
	}
	if err := c.Set(ctx, "", "1", "name", "en", "x"); !errors.Is(err, i18nx.ErrInvalidArgs) {
		t.Errorf("empty entity: %v", err)
	}
	batch, err := c.GetBatchAll(ctx, "product", []string{"1", "2"})
	if err != nil || len(batch) != 1 {
		t.Errorf("GetBatchAll = %v, %v", batch, err)
	}
}

func TestResponseInterceptor(t *testing.T) {
	c := database(t)
	ctx := context.Background()
	for id, tr := range map[string]i18nx.Translations{
		"1": {"name": {"kk": "Латте kk"}, "description": {"kk": "Сүтті кофе"}},
		"2": {"name": {"kk": "Капучино kk"}},
	} {
		if err := c.SetBatch(ctx, "product", id, tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Set(ctx, "menu", "", "title", "kk", "Мәзір"); err != nil {
		t.Fatal(err)
	}

	menu := func() *testpb.Menu {
		return &testpb.Menu{
			Title:    "Меню",
			Products: []*testpb.Product{{Id: 1, Name: "Латте", Description: "Кофе с молоком"}, {Id: 2, Name: "Капучино"}, {Id: 3, Name: "Раф"}},
			Featured: &testpb.Product{Id: 2, Name: "Капучино"},
		}
	}
	call := func(ctx context.Context, resp any, skip func(context.Context) bool) any {
		out, err := c.ResponseInterceptor(skip)(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return resp, nil })
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	got := call(withLanguage("kk-KZ"), menu(), nil).(*testpb.Menu)
	if got.GetTitle() != "Мәзір" || got.GetProducts()[0].GetName() != "Латте kk" || got.GetProducts()[0].GetDescription() != "Сүтті кофе" ||
		got.GetProducts()[1].GetName() != "Капучино kk" || got.GetFeatured().GetName() != "Капучино kk" {
		t.Errorf("kk = %v", got)
	}
	// Untranslated entities and the default locale keep the base values.
	if got.GetProducts()[2].GetName() != "Раф" {
		t.Errorf("an untranslated product changed: %v", got.GetProducts()[2])
	}
	if ru := call(withLanguage("ru"), menu(), nil).(*testpb.Menu); ru.GetProducts()[0].GetName() != "Латте" {
		t.Errorf("ru = %v", ru)
	}
	// The skip hook turns substitution off.
	if skipped := call(withLanguage("kk"), menu(), func(context.Context) bool { return true }).(*testpb.Menu); skipped.GetTitle() != "Меню" {
		t.Errorf("skipped = %v", skipped)
	}
	// An editor's message is left for its handler.
	editor := call(withLanguage("kk"), &testpb.ProductEditor{Id: 1, Name: "Латте"}, nil).(*testpb.ProductEditor)
	if editor.GetName() != "Латте" {
		t.Errorf("editor = %v", editor)
	}

	// Errors are translated on the way out.
	_, err := c.ResponseInterceptor(nil)(withLanguage("kk"), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	})
	if st, _ := status.FromError(err); st.Message() != "сұраныстар шегінен асып кетті" {
		t.Errorf("error = %v", err)
	}
}
