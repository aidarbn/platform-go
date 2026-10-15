package admin

import (
	"fmt"
	"html/template"
	"regexp"
	"strings"
)

// Theme is the look of the panel in the colours of the project: the name and a caption in
// the menu, the palette, the radii and the project's own rules for its pages.
//
// The zero Theme is the panel as it is: every empty field keeps the panel's own value, and
// a project without a theme gets exactly the same pages as before themes existed. The
// project sets it with SetTheme in wireDomain, next to AddPage: a theme is part of the
// code, not of the environment, so Load does not read it.
//
// A theme with any colour pins the panel to the light scheme: a brand palette is drawn
// for one background, and the dark one of the panel would mix with it.
//
// Every value is a CSS value — "#343A4D", "rgba(247, 147, 30, .18)", "14px". The rules go
// into the stylesheet of the page, the same <style> block the panel already has, so a
// theme needs no extra request and no inline style attributes.
type Theme struct {
	Brand   string // the name in the menu and on the sign in card; the service name when empty
	Caption string // a small word next to the name, "admin"

	Background  string // the page behind the content
	Surface     string // cards, tables, inputs
	Text        string // the text of the content
	Muted       string // secondary text
	Line        string // borders and separators of the content
	Accent      string // links, focus and checkboxes
	AccentHover string // a hovered link colour of buttons that have no Button colour

	Menu       string // background of the menu
	MenuText   string // its items
	MenuMuted  string // the account and the caption in the menu
	MenuHover  string // background of a hovered item
	MenuLine   string // the separators in the menu
	Active     string // background of the current item
	ActiveText string // text of the current item
	BrandColor string // the name in the menu

	Button      string // background of the primary buttons
	ButtonHover string // and when hovered
	ButtonText  string // their text

	Radius        string // cards, tables and blocks, "14px"
	ControlRadius string // buttons, inputs and menu items, "10px"

	// PagesFirst puts the project pages at the top of the menu and the panel's own
	// pages — overview, settings, accounts, audit — under a separator after them: in a
	// project panel the project pages are the everyday ones.
	PagesFirst bool

	// CSS holds the project's own rules for its pages, added after the panel's.
	CSS string
}

// safeValue is what a CSS value of a theme may hold: colours, lengths, functions and
// variables, but nothing that ends a declaration, a rule or the style element.
var safeValue = regexp.MustCompile(`^[#a-zA-Z0-9(),.%\s+-]*$`)

// Validate reports a value that could break out of its declaration.
func (t Theme) Validate() error {
	for name, v := range t.values() {
		if !safeValue.MatchString(v) {
			return fmt.Errorf("admin: theme %s = %q is not a plain CSS value", name, v)
		}
	}
	if strings.Contains(t.CSS, "<") {
		return fmt.Errorf("admin: theme CSS must not contain %q", "<")
	}
	return nil
}

func (t Theme) values() map[string]string {
	return map[string]string{
		"Background": t.Background, "Surface": t.Surface, "Text": t.Text, "Muted": t.Muted, "Line": t.Line,
		"Accent": t.Accent, "AccentHover": t.AccentHover,
		"Menu": t.Menu, "MenuText": t.MenuText, "MenuMuted": t.MenuMuted, "MenuHover": t.MenuHover, "MenuLine": t.MenuLine,
		"Active": t.Active, "ActiveText": t.ActiveText, "BrandColor": t.BrandColor,
		"Button": t.Button, "ButtonHover": t.ButtonHover, "ButtonText": t.ButtonText,
		"Radius": t.Radius, "ControlRadius": t.ControlRadius,
	}
}

// coloured reports whether the theme changes any colour.
func (t Theme) coloured() bool {
	for name, v := range t.values() {
		if v != "" && name != "Radius" && name != "ControlRadius" {
			return true
		}
	}
	return false
}

// branded reports whether the menu shows a name and a caption instead of the plain
// service name.
func (t Theme) branded() bool { return t.Brand != "" || t.Caption != "" }

// stylesheet is the theme as rules after the panel's own. Empty for the zero theme.
func (t Theme) stylesheet() template.CSS {
	var b strings.Builder
	rule := func(selector string, decls ...string) {
		var set []string
		for i := 0; i+1 < len(decls); i += 2 {
			if decls[i+1] != "" {
				set = append(set, decls[i]+": "+decls[i+1])
			}
		}
		if len(set) > 0 {
			b.WriteString(selector + " { " + strings.Join(set, "; ") + "; }\n")
		}
	}

	if t.coloured() {
		b.WriteString(":root {\n  color-scheme: light;\n" + lightPalette)
		for _, v := range [][2]string{
			{"--bg", t.Background}, {"--surface", t.Surface}, {"--text", t.Text}, {"--muted", t.Muted}, {"--line", t.Line},
			{"--accent", t.Accent}, {"--accent-hover", t.AccentHover}, {"--side", t.Menu},
		} {
			if v[1] != "" {
				b.WriteString("  " + v[0] + ": " + v[1] + ";\n")
			}
		}
		b.WriteString("}\n")
	}
	if t.Menu != "" {
		// A coloured menu needs no line between it and the page. On a phone the menu is a
		// bar on top, and the panel's own rule for that stays.
		b.WriteString("@media (min-width: 761px) { .shell { background: linear-gradient(to right, var(--side) 15rem, var(--bg) 15rem); } }\n")
		rule(".side", "border-bottom-color", t.MenuLine)
	}
	rule(".side", "color", t.MenuText)
	rule(".side nav a", "color", t.MenuText, "border-radius", t.ControlRadius)
	rule(".side nav a:hover", "background", t.MenuHover)
	rule(".side nav a.on", "background", t.Active, "color", t.ActiveText)
	if t.PagesFirst {
		b.WriteString(".side nav .sep { flex: none; height: 1px; margin: .6rem .3rem; background: var(--line); }\n")
		rule(".side nav .sep", "background", t.MenuLine)
		b.WriteString("@media (max-width: 760px) { .side nav .sep { width: 1px; height: auto; margin: .3rem .2rem; } }\n")
	}
	rule(".me", "border-top-color", t.MenuLine)
	rule(".me .email, .side .brand .caption", "color", t.MenuMuted)
	if t.Menu != "" {
		rule(".me button", "background", "transparent", "color", t.MenuText, "border-color", t.MenuLine)
		rule(".me button:hover", "background", t.MenuHover, "border-color", t.MenuMuted)
	}
	if t.branded() {
		b.WriteString(".brand { align-items: baseline; flex-wrap: wrap; gap: .1rem .5rem; }\n.brand::before { content: none; }\n" +
			".brand-name { font-size: 1.35rem; font-weight: 800; letter-spacing: -.01em; }\n" +
			".brand .caption { font-size: .8rem; font-weight: 400; color: var(--muted); }\n")
		rule(".brand-name", "color", t.BrandColor)
	}
	rule("button", "background", t.Button, "border-color", t.Button, "color", t.ButtonText)
	rule("button:hover", "background", t.ButtonHover, "border-color", t.ButtonHover)
	rule("input, select, textarea, button", "border-radius", t.ControlRadius)
	rule("table, .card, .secret, .login", "border-radius", t.Radius)
	rule("tr:first-child > th:first-child", "border-top-left-radius", t.Radius)
	rule("tr:first-child > th:last-child", "border-top-right-radius", t.Radius)
	if t.CSS != "" {
		b.WriteString(strings.TrimSpace(t.CSS) + "\n")
	}
	// #nosec G203 -- every value passed Validate: no value can end its declaration, and
	// the project's own rules can not end the style element.
	return template.CSS(b.String())
}
