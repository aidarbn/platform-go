package admin

// The panel is server rendered HTML with no external assets: an admin panel must open
// on a locked down network, and a page that needs a CDN does not.
//
// The sources are kept as text rather than as one parsed set: a template set cannot be
// cloned once it has executed, and each server parses its own pages.
const pageSrc = `
{{define "style"}}<style>
:root {
  color-scheme: light dark;
  --bg: #f5f6f8; --surface: #fff; --side: #fff; --text: #18181b; --muted: #6b7280; --line: #e5e7eb; --hover: #f3f4f6;
  --accent: #2563eb; --accent-hover: #1d4ed8; --on-accent: #fff; --accent-soft: #eef3fe;
  --bad: #b91c1c; --bad-soft: #fef2f2; --good: #15803d; --good-soft: #f0fdf4; --chip: #f1f2f4;
  --shadow: 0 1px 2px rgba(16, 24, 40, .05);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0e1014; --surface: #161920; --side: #12151a; --text: #e6e8ec; --muted: #9aa1ad; --line: #272c35; --hover: #1d2129;
    --accent: #6ea8ff; --accent-hover: #8dbbff; --on-accent: #0b1220; --accent-soft: #1a2436;
    --bad: #f87171; --bad-soft: #2a1618; --good: #4ade80; --good-soft: #132419; --chip: #222731;
    --shadow: none;
  }
}
* { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.55 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
h1 { font-size: 1.5rem; font-weight: 650; letter-spacing: -.01em; margin: 0 0 1.25rem; }
h2 { font-size: 1.05rem; font-weight: 600; margin: 2rem 0 .75rem; }
p { margin: 0 0 .75rem; }
code { font: .85em/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; background: var(--chip); padding: .1em .35em; border-radius: 4px; }
pre { font: .85em/1.5 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0 0 1rem; }
pre code, .secret code { background: none; padding: 0; }

.shell { display: grid; grid-template-columns: 15rem minmax(0, 1fr); min-height: 100vh;
  background: linear-gradient(to right, var(--side) calc(15rem - 1px), var(--line) calc(15rem - 1px), var(--line) 15rem, var(--bg) 15rem); }
.side { position: sticky; top: 0; height: 100vh; display: flex; flex-direction: column; gap: 1.25rem;
  padding: 1.25rem .85rem; }
.brand { display: flex; align-items: center; gap: .6rem; padding: 0 .5rem; font-weight: 650; font-size: 1rem; word-break: break-word; }
.brand::before { content: ""; flex: none; width: .8rem; height: .8rem; border-radius: 3px; background: var(--accent); }
.side nav { display: flex; flex-direction: column; gap: 2px; flex: 1; overflow-y: auto; }
.side nav a { display: block; padding: .45rem .65rem; border-radius: 6px; color: var(--text); text-decoration: none; }
.side nav a:hover { background: var(--hover); }
.side nav a.on { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
.me { border-top: 1px solid var(--line); padding: .85rem .5rem 0; display: flex; flex-direction: column; gap: .4rem; }
.me .email { color: var(--muted); font-size: .85rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.me button { align-self: flex-start; }
main { padding: 2rem 2.5rem 3rem; max-width: 72rem; min-width: 0; overflow-x: auto; }

input, select, textarea { font: inherit; padding: .45rem .65rem; border: 1px solid var(--line); border-radius: 6px;
  background: var(--surface); color: inherit; min-width: 0; }
input:focus, select:focus, textarea:focus, button:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; border-color: var(--accent); }
input[type=checkbox] { width: auto; accent-color: var(--accent); }
button { font: inherit; font-weight: 550; padding: .45rem .95rem; border: 1px solid var(--accent); border-radius: 6px;
  background: var(--accent); color: var(--on-accent); cursor: pointer; white-space: nowrap; }
button:hover { background: var(--accent-hover); border-color: var(--accent-hover); }
/* Buttons inside tables and the sign out are secondary: a row of filled buttons shouts. */
td button, .me button { padding: .3rem .7rem; font-weight: 500; background: var(--surface); color: var(--text); border-color: var(--line); }
td button:hover, .me button:hover { background: var(--hover); border-color: var(--muted); }

table { width: 100%; border-collapse: separate; border-spacing: 0; background: var(--surface); border: 1px solid var(--line);
  border-radius: 10px; box-shadow: var(--shadow); margin-bottom: 1rem; }
th, td { text-align: left; padding: .6rem .85rem; border-bottom: 1px solid var(--line); vertical-align: middle; }
tr:last-child > td, tr:last-child > th { border-bottom: 0; }
th { font-weight: 600; color: var(--muted); font-size: .78rem; text-transform: uppercase; letter-spacing: .04em; background: var(--bg); }
tr:first-child > th:first-child { border-top-left-radius: 10px; }
tr:first-child > th:last-child { border-top-right-radius: 10px; }
td > form + form { margin-top: .35rem; }
main > form { margin: 0 0 1rem; }
main > form > .muted { margin-left: .5rem; }

.banner { padding: .7rem 1rem; border: 1px solid transparent; border-left: 3px solid var(--good); border-radius: 6px;
  background: var(--good-soft); margin-bottom: 1.25rem; }
.banner.error { border-left-color: var(--bad); background: var(--bad-soft); }
.muted { color: var(--muted); }
.overridden { font-weight: 600; }
.row { display: flex; gap: .5rem; align-items: center; flex-wrap: wrap; }
.row > form { margin: 0; }
td.row { display: table-cell; }
td.row > form { display: inline-block; margin: 0 .35rem .35rem 0; }
.secret { padding: .85rem 1rem; border: 1px solid var(--line); border-radius: 8px; background: var(--surface);
  word-break: break-all; white-space: pre-wrap; overflow: auto; max-height: 32rem; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr)); gap: 1rem; margin-bottom: 1rem; }
.card { background: var(--surface); border: 1px solid var(--line); border-radius: 10px; padding: 1rem 1.15rem; box-shadow: var(--shadow); }
.card .label { color: var(--muted); font-size: .8rem; text-transform: uppercase; letter-spacing: .04em; }
.card .value { font-size: 1.6rem; font-weight: 650; margin-top: .2rem; }
.card .note { color: var(--muted); font-size: .85rem; }

.login-page { min-height: 100vh; display: grid; place-items: center; padding: 1.5rem 1rem; }
.login { width: 100%; max-width: 22rem; background: var(--surface); border: 1px solid var(--line); border-radius: 12px;
  padding: 2rem 1.75rem; box-shadow: 0 8px 24px rgba(16, 24, 40, .08); }
.login .brand { padding: 0; margin-bottom: 1.5rem; }
.login h1 { font-size: 1.25rem; margin-bottom: 1rem; }
.login label { display: block; margin: 1rem 0 .35rem; font-weight: 550; }
.login input { width: 100%; }
.login button { margin-top: 1.5rem; width: 100%; padding: .6rem; }

/* A phone: the sidebar becomes a bar on top, the menu scrolls sideways. */
@media (max-width: 760px) {
  .shell { grid-template-columns: minmax(0, 1fr); background: var(--bg); }
  .side { position: static; height: auto; flex-direction: row; flex-wrap: wrap; align-items: center; gap: .5rem .75rem;
    padding: .75rem 1rem; background: var(--side); border-bottom: 1px solid var(--line); }
  .side nav { order: 3; flex: 1 0 100%; flex-direction: row; overflow-x: auto; overflow-y: visible; gap: .25rem; margin: 0 -.25rem; }
  .side nav a { white-space: nowrap; }
  .me { margin-left: auto; border-top: 0; padding: 0; flex-direction: row; align-items: center; min-width: 0; }
  .me .email { display: none; }
  main { padding: 1.25rem 1rem 2rem; }
  /* A wide table scrolls on its own instead of squeezing every cell into a column. */
  table { display: block; overflow-x: auto; }
  th, td { white-space: nowrap; }
  h1 { font-size: 1.3rem; }
}
</style>{{end}}

{{define "layout"}}<!doctype html>
<html lang="{{lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — {{.Service}}</title>
{{template "style"}}
</head>
<body class="shell">
<aside class="side">
  <div class="brand">{{.Service}}</div>
  <nav>
    {{- range .Nav}}<a href="{{.Path}}"{{if .Active}} class="on"{{end}}>{{.Title}}</a>{{end}}
  </nav>
  <div class="me">
    <span class="email" title="{{.User.Email}}">{{.User.Email}}</span>
    <form method="post" action="/logout">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button>{{t "Sign out"}}</button>
    </form>
  </div>
</aside>
<main>
<h1>{{.Title}}</h1>
{{- with .Message}}<p class="banner">{{.}}</p>{{end}}
{{- with .Error}}<p class="banner error">{{.}}</p>{{end}}
{{template "content" .}}
</main>
</body>
</html>{{end}}

{{define "login"}}<!doctype html>
<html lang="{{lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{t "Sign in"}} — {{.Service}}</title>
{{template "style"}}
</head>
<body class="login-page">
<div class="login">
<div class="brand">{{.Service}}</div>
<h1>{{t "Sign in"}}</h1>
{{- with .Error}}<p class="banner error">{{.}}</p>{{end}}
<form method="post">
  <input type="hidden" name="next" value="{{.Next}}">
  <label for="email">{{t "Email"}}</label>
  <input id="email" name="email" type="email" autocomplete="username" autofocus required>
  <label for="password">{{t "Password"}}</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required>
  <button>{{t "Sign in"}}</button>
</form>
</div>
</body>
</html>{{end}}

`

// Every page is one template that fills the content block of the layout.
var contentTemplates = map[string]string{
	"index": `
<p class="muted">{{t "Signed in as"}} {{.User.Email}}{{if .User.Roles}}, {{t "roles"}}: {{range $i, $r := .User.Roles}}{{if $i}}, {{end}}<code>{{$r}}</code>{{end}}{{end}}.</p>
<div class="cards">
  <div class="card">
    <div class="label">{{t "Business settings"}}</div>
    {{if .Data.HasSettings}}<div class="value">{{.Data.Settings}}</div><div class="note">{{t "changed"}}: {{.Data.Overrides}}</div>
    {{else}}<div class="note">{{t "the settings module is off"}}</div>{{end}}
  </div>
  <div class="card">
    <div class="label">{{t "Accounts"}}</div>
    <div class="value">{{.Data.Users}}</div>
  </div>
</div>
<h2>{{t "Recent actions"}}</h2>
{{template "audit-table" .Data.Audit}}
`,
	"settings": `
{{if not .Data.Groups}}<p class="muted">{{t "The project has no business settings: describe them in settings.yaml."}}</p>{{end}}
{{range .Data.Groups}}
<h2>{{.Name}}</h2>
{{with .Description}}<p class="muted">{{.}}</p>{{end}}
<table>
  <tr><th>{{t "Setting"}}</th><th>{{t "Value"}}</th><th>{{t "Default"}}</th><th></th></tr>
  {{range .Values}}{{$v := .}}
  <tr>
    <td><code>{{.Name}}</code>{{with .Title}}<div>{{.}}</div>{{end}}{{with .Description}}<div class="muted">{{.}}</div>{{end}}{{if .RequiresRestart}}<div class="muted">{{t "applies after a restart"}}</div>{{end}}</td>
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
        <button>{{t "Save"}}</button>
      </form>
    </td>
    <td class="muted"><code>{{.Default}}</code>{{if or .Min .Max}}<div>{{with .Min}}{{t "min"}} {{.}}{{end}} {{with .Max}}{{t "max"}} {{.}}{{end}}</div>{{end}}</td>
    <td>
      {{if .Overridden}}
      <form method="post" action="/settings/reset">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="key" value="{{.Key}}">
        <button>{{t "Reset"}}</button>
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
  <tr><th>{{t "Email"}}</th><th>{{t "Roles"}}</th><th>{{t "State"}}</th><th>{{t "Last sign in"}}</th><th></th></tr>
  {{range .Data.Users}}
  <tr>
    <td>{{.Email}}</td>
    <td>{{range $i, $r := .Roles}}{{if $i}}, {{end}}<code>{{$r}}</code>{{end}}</td>
    <td>{{if .Disabled}}<span class="muted">{{t "disabled"}}</span>{{else}}{{t "active"}}{{end}}</td>
    <td class="muted">{{if .LastLoginAt.IsZero}}{{t "never"}}{{else}}{{.LastLoginAt.Format "2006-01-02 15:04"}}{{end}}</td>
    <td class="row">
      <form method="post" action="/users/toggle">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="id" value="{{.ID}}">
        <button>{{if .Disabled}}{{t "Enable"}}{{else}}{{t "Disable"}}{{end}}</button>
      </form>
      <form method="post" action="/users/delete">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="id" value="{{.ID}}">
        <button>{{t "Delete"}}</button>
      </form>
    </td>
  </tr>
  {{end}}
</table>

<h2>{{t "Add an account"}}</h2>
<form method="post" action="/users/create" class="row">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <input name="email" type="email" placeholder="{{t "email"}}" required>
  <input name="password" type="password" placeholder="{{t "password"}}" required>
  <input name="roles" placeholder="{{t "roles, comma separated"}}">
  <button>{{t "Add"}}</button>
</form>
`,
	"audit": `
{{template "audit-table" .Data.Entries}}
{{if .Data.Next}}<p><a href="/audit?before={{.Data.Next}}">{{t "Older"}}</a></p>{{end}}
`,
	"project": `{{.Data.Body}}`,
	"forbidden": `
<p>{{t "The page needs a role this account does not have."}}</p>
`,
}

const auditTableTmpl = `
{{define "audit-table"}}
{{if not .}}<p class="muted">{{t "No records."}}</p>{{else}}
<table>
  <tr><th>{{t "When"}}</th><th>{{t "Who"}}</th><th>{{t "What"}}</th><th>{{t "Target"}}</th><th>{{t "Details"}}</th><th>{{t "Address"}}</th></tr>
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
