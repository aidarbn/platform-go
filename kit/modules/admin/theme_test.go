package admin_test

import (
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/modules/admin"
)

var update = flag.Bool("update", false, "rewrite the golden pages in testdata")

// csrfValue hides the token of the session: it changes with every sign in.
var csrfValue = regexp.MustCompile(`name="csrf" value="[^"]*"`)

func ordersPage() admin.Page {
	return admin.Page{Title: "Orders", Path: "/orders", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.Render(w, r, "Orders", func(w io.Writer) error {
			_, err := io.WriteString(w, `<table><tr><th>Order</th></tr><tr><td><a href="/orders/1">1</a></td></tr></table>`)
			return err
		})
	})}
}

// panelPages are the sign in page and a project page as the browser gets them.
func panelPages(t *testing.T, cfg admin.Config) (login, page string) {
	t.Helper()

	p := newPanelWith(t, cfg, ordersPage())
	_, login = p.get("/login")
	p.login("admin@example.com", "hunter2")
	code, page := p.get("/orders")
	if code != http.StatusOK {
		t.Fatalf("GET /orders: %d\n%s", code, page)
	}
	return login, csrfValue.ReplaceAllString(page, `name="csrf" value="-"`)
}

func golden(t *testing.T, name, got string) {
	t.Helper()

	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run the test with -update", err)
	}
	if got != string(want) {
		t.Errorf("%s changed:\n%s", name, got)
	}
}

// A project without a theme gets the panel byte for byte as before the themes.
func TestNoThemeLooksAsBefore(t *testing.T) {
	login, page := panelPages(t, admin.Config{Insecure: true})
	golden(t, "login.html", login)
	golden(t, "page.html", page)
}

var damdala = admin.Theme{
	Brand: "dămdala", Caption: "админка",
	Background: "#F4F4F4", Accent: "#B83A12",
	Menu: "#343A4D", MenuText: "#FFFFFF", MenuMuted: "rgba(255, 255, 255, .6)", MenuHover: "rgba(255, 255, 255, .08)",
	MenuLine: "rgba(255, 255, 255, .12)", Active: "rgba(247, 147, 30, .18)", ActiveText: "#FFB566", BrandColor: "#F7931E",
	Button: "#E84E1B", ButtonHover: "#C9400F", ButtonText: "#FFFFFF",
	Radius: "14px", ControlRadius: "10px",
	PagesFirst: true,
	CSS:        ".chip { border-radius: 10px; }",
}

func TestThemedPanel(t *testing.T) {
	login, page := panelPages(t, admin.Config{Insecure: true, Theme: damdala})
	golden(t, "themed-page.html", page)

	for _, want := range []string{
		`<span class="brand-name">dămdala</span> <span class="caption">админка</span>`,
		// A brand palette is drawn for one background: the dark scheme stays out.
		"color-scheme: light;\n  --bg: #f5f6f8;",
		"--bg: #F4F4F4;", "--side: #343A4D;",
		".side nav a.on { background: rgba(247, 147, 30, .18); color: #FFB566; }",
		"button { background: #E84E1B; border-color: #E84E1B; color: #FFFFFF; }",
		"table, .card, .secret, .login { border-radius: 14px; }",
		".chip { border-radius: 10px; }\n</style>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
	if !strings.Contains(login, `<span class="brand-name">dămdala</span>`) || !strings.Contains(login, "--side: #343A4D;") {
		t.Errorf("the sign in page is not themed:\n%s", login)
	}
	// The title keeps the service name: it is what tells the tabs of the panels apart.
	if !strings.Contains(page, "<title>Orders — shop-api</title>") {
		t.Error("the title lost the service name")
	}
}

func TestPagesFirst(t *testing.T) {
	_, page := panelPages(t, admin.Config{Insecure: true, Theme: admin.Theme{PagesFirst: true}})
	orders := strings.Index(page, `<a href="/orders" class="on">Orders</a>`)
	sep := strings.Index(page, `<span class="sep" role="separator"></span><a href="/">`)
	audit := strings.Index(page, `<a href="/audit">`)
	if orders < 0 || sep < orders || audit < sep {
		t.Errorf("the menu is not project pages, a separator, the panel:\n%s", page)
	}
	// Without colours the panel keeps both of its schemes.
	if strings.Contains(page, "color-scheme: light;") {
		t.Error("a theme without colours pinned the light scheme")
	}
}

// A caption without a brand shows next to the service name.
func TestCaptionWithoutBrand(t *testing.T) {
	_, page := panelPages(t, admin.Config{Insecure: true, Theme: admin.Theme{Caption: "admin"}})
	if !strings.Contains(page, `<span class="brand-name">shop-api</span> <span class="caption">admin</span>`) {
		t.Errorf("no caption next to the service name:\n%s", page)
	}
}

func TestThemeRejectsValuesThatBreakOut(t *testing.T) {
	for _, bad := range []admin.Theme{
		{Menu: "red; } body { display: none"},
		{Button: "#fff</style><script>"},
		{Radius: "1px;"},
		{CSS: "</style><script>alert(1)</script>"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v passed", bad)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("SetTheme(%+v) did not panic", bad)
				}
			}()
			(&admin.Registry{}).SetTheme(bad)
		}()
	}
	if err := damdala.Validate(); err != nil {
		t.Errorf("a plain theme failed: %v", err)
	}
}
