package admin

import "html/template"

// The panel is server rendered HTML with no external assets: an admin panel must open
// on a locked down network, and a page that needs a CDN does not.
//
// The sources are kept as text rather than as one parsed set: a template set cannot be
// cloned once it has executed, and each server parses its own pages.
const pageSrc = `
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — {{.Service}}</title>
<style>
:root { color-scheme: light dark; --line: #d4d4d8; --muted: #71717a; --accent: #2563eb; --bad: #b91c1c; --good: #15803d; }
@media (prefers-color-scheme: dark) { :root { --line: #3f3f46; --muted: #a1a1aa; --accent: #60a5fa; --bad: #f87171; --good: #4ade80; } }
* { box-sizing: border-box; }
body { margin: 0; font: 15px/1.5 system-ui, sans-serif; }
header { display: flex; gap: 1rem; align-items: baseline; padding: .75rem 1.25rem; border-bottom: 1px solid var(--line); flex-wrap: wrap; }
header strong { font-size: 1rem; }
nav { display: flex; gap: 1rem; flex-wrap: wrap; }
nav a { color: inherit; text-decoration: none; border-bottom: 2px solid transparent; }
nav a.on, nav a:hover { border-color: var(--accent); }
header form { margin-left: auto; }
main { padding: 1.25rem; max-width: 62rem; }
h1 { font-size: 1.35rem; margin: 0 0 1rem; }
h2 { font-size: 1.05rem; margin: 1.75rem 0 .5rem; }
table { border-collapse: collapse; width: 100%; }
th, td { text-align: left; padding: .45rem .6rem; border-bottom: 1px solid var(--line); vertical-align: top; }
th { font-weight: 600; color: var(--muted); font-size: .85rem; }
code, input, select, button { font-family: ui-monospace, monospace; font-size: .9rem; }
input, select { padding: .3rem .4rem; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: inherit; }
button { padding: .3rem .7rem; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: inherit; cursor: pointer; }
button:hover { border-color: var(--accent); }
.banner { padding: .6rem .8rem; border-left: 3px solid var(--good); margin-bottom: 1rem; }
.banner.error { border-color: var(--bad); }
.muted { color: var(--muted); }
.overridden { font-weight: 600; }
.login { max-width: 20rem; margin: 4rem auto; padding: 0 1.25rem; }
.login label { display: block; margin: .75rem 0 .2rem; }
.login input { width: 100%; }
.login button { margin-top: 1rem; width: 100%; }
.row { display: flex; gap: .4rem; align-items: center; flex-wrap: wrap; }
.secret { padding: .8rem; border: 1px solid var(--line); border-radius: 4px; word-break: break-all; }
</style>
</head>
<body>
<header>
  <strong>{{.Service}}</strong>
  <nav>
    {{- range .Nav}}<a href="{{.Path}}"{{if .Active}} class="on"{{end}}>{{.Title}}</a>{{end}}
  </nav>
  <form method="post" action="/logout">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <button>Sign out {{.User.Email}}</button>
  </form>
</header>
<main>
<h1>{{.Title}}</h1>
{{- with .Message}}<p class="banner">{{.}}</p>{{end}}
{{- with .Error}}<p class="banner error">{{.}}</p>{{end}}
{{template "content" .}}
</main>
</body>
</html>{{end}}

{{define "login"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in — {{.Service}}</title>
<style>
:root { color-scheme: light dark; --line: #d4d4d8; --bad: #b91c1c; --accent: #2563eb; }
@media (prefers-color-scheme: dark) { :root { --line: #3f3f46; --bad: #f87171; --accent: #60a5fa; } }
* { box-sizing: border-box; }
body { margin: 0; font: 15px/1.5 system-ui, sans-serif; }
.login { max-width: 20rem; margin: 4rem auto; padding: 0 1.25rem; }
label { display: block; margin: .75rem 0 .2rem; }
input { width: 100%; padding: .35rem .45rem; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: inherit; }
button { margin-top: 1rem; width: 100%; padding: .4rem; border: 1px solid var(--line); border-radius: 4px; background: transparent; color: inherit; cursor: pointer; }
button:hover { border-color: var(--accent); }
.banner.error { padding: .6rem .8rem; border-left: 3px solid var(--bad); }
</style>
</head>
<body>
<div class="login">
<h1>{{.Service}}</h1>
{{- with .Error}}<p class="banner error">{{.}}</p>{{end}}
<form method="post">
  <input type="hidden" name="next" value="{{.Next}}">
  <label for="email">Email</label>
  <input id="email" name="email" type="email" autocomplete="username" autofocus required>
  <label for="password">Password</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required>
  <label for="code">One time code <span class="muted">if enabled</span></label>
  <input id="code" name="code" inputmode="numeric" autocomplete="one-time-code">
  <button>Sign in</button>
</form>
</div>
</body>
</html>{{end}}

`

// loginTemplate is the only page rendered without a session.
var loginTemplate = template.Must(template.New("admin").Parse(pageSrc))

// Every page is one template that fills the content block of the layout.
var contentTemplates = map[string]string{
	"index": `
<p class="muted">Signed in as {{.User.Email}}{{if .User.Roles}}, roles: {{range $i, $r := .User.Roles}}{{if $i}}, {{end}}<code>{{$r}}</code>{{end}}{{end}}.</p>
<table>
  <tr><th>Business settings</th><td>{{if .Data.HasSettings}}{{.Data.Settings}} total, {{.Data.Overrides}} changed{{else}}the settings module is off{{end}}</td></tr>
  <tr><th>Accounts</th><td>{{.Data.Users}}</td></tr>
</table>
<h2>Recent actions</h2>
{{template "audit-table" .Data.Audit}}
`,
	"settings": `
{{if not .Data.Groups}}<p class="muted">The project has no business settings: describe them in settings.yaml.</p>{{end}}
{{range .Data.Groups}}
<h2>{{.Name}}</h2>
<table>
  <tr><th>Setting</th><th>Value</th><th>Default</th><th></th></tr>
  {{range .Values}}{{$v := .}}
  <tr>
    <td><code>{{.Name}}</code>{{with .Title}}<div class="muted">{{.}}</div>{{end}}</td>
    <td>
      <form method="post" action="/settings/set" class="row">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="key" value="{{.Key}}">
        {{if eq (printf "%s" .Kind) "bool"}}
          <select name="value">
            <option value="true"{{if eq .Value "true"}} selected{{end}}>true</option>
            <option value="false"{{if eq .Value "false"}} selected{{end}}>false</option>
          </select>
        {{else if .Options}}
          <select name="value">{{range .Options}}<option value="{{.}}"{{if eq . $v.Value}} selected{{end}}>{{.}}</option>{{end}}</select>
        {{else}}
          <input name="value" value="{{.Value}}" size="18">
        {{end}}
        <button>Save</button>
      </form>
    </td>
    <td class="muted"><code>{{.Default}}</code>{{if or .Min .Max}}<div>{{with .Min}}min {{.}}{{end}} {{with .Max}}max {{.}}{{end}}</div>{{end}}</td>
    <td>
      {{if .Overridden}}
      <form method="post" action="/settings/reset">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="key" value="{{.Key}}">
        <button>Reset</button>
      </form>
      {{end}}
    </td>
  </tr>
  {{end}}
</table>
{{end}}
`,
	"users": `
<table>
  <tr><th>Email</th><th>Roles</th><th>Two factor</th><th>State</th><th>Last sign in</th><th></th></tr>
  {{range .Data.Users}}
  <tr>
    <td>{{.Email}}</td>
    <td>{{range $i, $r := .Roles}}{{if $i}}, {{end}}<code>{{$r}}</code>{{end}}</td>
    <td>{{if .TwoFactor}}on{{else}}<span class="muted">off</span>{{end}}</td>
    <td>{{if .Disabled}}<span class="muted">disabled</span>{{else}}active{{end}}</td>
    <td class="muted">{{if .LastLoginAt.IsZero}}never{{else}}{{.LastLoginAt.Format "2006-01-02 15:04"}}{{end}}</td>
    <td class="row">
      <form method="post" action="/users/toggle">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="id" value="{{.ID}}">
        <button>{{if .Disabled}}Enable{{else}}Disable{{end}}</button>
      </form>
      <form method="post" action="/users/delete">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="id" value="{{.ID}}">
        <button>Delete</button>
      </form>
    </td>
  </tr>
  {{end}}
</table>

<h2>Add an account</h2>
<form method="post" action="/users/create" class="row">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <input name="email" type="email" placeholder="email" required>
  <input name="password" type="password" placeholder="password" required>
  <input name="roles" placeholder="roles, comma separated">
  <label class="row"><input type="checkbox" name="twofactor" value="1" checked> two factor</label>
  <button>Add</button>
</form>
`,
	"secret": `
<p>The account <strong>{{.Data.Email}}</strong> is created. The secret is shown once: add it to an authenticator app now.</p>
<p class="secret"><code>{{.Data.Secret}}</code></p>
<p class="muted">Link for a QR code: <code>{{.Data.URI}}</code></p>
<p><a href="/users">Back to the accounts</a></p>
`,
	"audit": `
{{template "audit-table" .Data.Entries}}
{{if .Data.Next}}<p><a href="/audit?before={{.Data.Next}}">Older</a></p>{{end}}
`,
	"forbidden": `
<p>The page needs a role this account does not have.</p>
`,
}

const auditTableTmpl = `
{{define "audit-table"}}
{{if not .}}<p class="muted">No records.</p>{{else}}
<table>
  <tr><th>When</th><th>Who</th><th>What</th><th>Target</th><th>Details</th><th>Address</th></tr>
  {{range .}}
  <tr>
    <td class="muted">{{.At.Format "2006-01-02 15:04:05"}}</td>
    <td>{{.Actor}}</td>
    <td><code>{{.Action}}</code></td>
    <td><code>{{.Target}}</code></td>
    <td>{{.Details}}</td>
    <td class="muted">{{.IP}}</td>
  </tr>
  {{end}}
</table>
{{end}}
{{end}}
`
