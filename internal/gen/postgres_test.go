package gen_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

// postgresProject has a second module with a local service, so the golden files also
// pin where the postgres service and its volume sit among the others.
const postgresProject = "schema: 1\nproject:\n  module: example.com/app\nmodules:\n  s3: {}\n  postgres:"

// Without the compose option every project gets the docker-compose.yml and the
// .env.example it had before the option existed, byte for byte: the golden files are
// those files.
func TestPostgresComposeDefaultsAreTheFilesBefore(t *testing.T) {
	for _, section := range []string{
		" {}\n",
		"\n    compose: {}\n",
		"\n    compose:\n      enabled: true\n      image: postgres:18-alpine\n      db: app\n      user: app\n      password: app\n      port: 5432\n      command: []\n      init: \"\"\n",
	} {
		files, err := gen.Wiring(mustParse(t, postgresProject+section))
		if err != nil {
			t.Fatalf("Wiring: %v", err)
		}
		golden(t, "compose.default.yml", files[gen.ComposePath])
		golden(t, "env.default.example", files[gen.EnvPath])
	}
}

// Every generated file, not only the two the option changes, stays the same when the
// option spells out its defaults.
func TestPostgresComposeDefaultsChangeNoFile(t *testing.T) {
	before, err := gen.Wiring(mustParse(t, postgresProject+" {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := gen.Wiring(mustParse(t, postgresProject+"\n    compose:\n      enabled: true\n      image: postgres:18-alpine\n      port: 5432\n      data_dir: /var/lib/postgresql\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("files: %d before, %d after", len(before), len(after))
	}
	for path, content := range before {
		if string(after[path]) != string(content) {
			t.Errorf("%s changed", path)
		}
	}
}

// taplyCompose is taply's local database: an older image, its own database name and
// port, more connections and the keycloak database created on start.
const taplyCompose = `
    compose:
      image: postgres:16-alpine
      db: be-maas-api
      port: 5544
      data_dir: /var/lib/postgresql/data
      command: [-c, max_connections=300]
      init: deploy/postgres/init-keycloak.sql
`

func TestPostgresComposeTaply(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, postgresProject+taplyCompose))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	golden(t, "compose.taply.yml", files[gen.ComposePath])
	if !strings.Contains(string(files[gen.EnvPath]), "\nDATABASE_URL=postgres://app:app@localhost:5544/be-maas-api?sslmode=disable\n") {
		t.Errorf(".env.example:\n%s", files[gen.EnvPath])
	}
	pg := parseCompose(t, files[gen.ComposePath]).Services["postgres"]
	if fmt.Sprint(pg["command"]) != "[-c max_connections=300]" {
		t.Errorf("command = %v", pg["command"])
	}
	if fmt.Sprint(pg["volumes"]) != "[postgres-data:/var/lib/postgresql/data ./deploy/postgres/init-keycloak.sql:/docker-entrypoint-initdb.d/init-keycloak.sql:ro]" {
		t.Errorf("volumes = %v", pg["volumes"])
	}
}

func TestPostgresComposeInitFolder(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, postgresProject+"\n    compose:\n      init: ./db/init/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[gen.ComposePath]), "\n      - \"./db/init:/docker-entrypoint-initdb.d:ro\"\n") {
		t.Errorf("docker-compose.yml:\n%s", files[gen.ComposePath])
	}
}

// A project that runs its own database service keeps DATABASE_URL and loses the
// service with its volume; the other services stay.
func TestPostgresComposeDisabled(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, postgresProject+"\n    compose:\n      enabled: false\n      port: 6543\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := parseCompose(t, files[gen.ComposePath])
	if _, ok := c.Services["postgres"]; ok || len(c.Services) != 1 {
		t.Errorf("services = %v", c.Services)
	}
	if _, ok := c.Volumes["postgres-data"]; ok || len(c.Volumes) != 1 {
		t.Errorf("volumes = %v", c.Volumes)
	}
	if !strings.Contains(string(files[gen.EnvPath]), "DATABASE_URL=postgres://app:app@localhost:6543/app?sslmode=disable\n") {
		t.Errorf(".env.example:\n%s", files[gen.EnvPath])
	}

	only, err := gen.Wiring(mustParse(t, "schema: 1\nproject:\n  module: example.com/app\nmodules:\n  postgres:\n    compose: {enabled: false}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c := parseCompose(t, only[gen.ComposePath]); len(c.Services) != 0 || c.Volumes != nil {
		t.Errorf("compose = %+v", c)
	}
}

// Values YAML, the shell or compose would read differently come back as written.
func TestPostgresComposeQuotesValues(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, postgresProject+`
    compose:
      user: "o'neil"
      password: "p@ss $word: #1"
      db: "true"
      command: ["-c", "search_path=$user"]
`))
	if err != nil {
		t.Fatal(err)
	}
	pg := parseCompose(t, files[gen.ComposePath]).Services["postgres"]
	env := pg["environment"].(map[string]any)
	if env["POSTGRES_USER"] != "o'neil" || env["POSTGRES_PASSWORD"] != "p@ss $$word: #1" || env["POSTGRES_DB"] != "true" {
		t.Errorf("environment = %#v", env)
	}
	if fmt.Sprint(pg["command"]) != "[-c search_path=$$user]" {
		t.Errorf("command = %v", pg["command"])
	}
	test := pg["healthcheck"].(map[string]any)["test"].([]any)
	if test[1] != `pg_isready -U 'o'\''neil' -d true` {
		t.Errorf("healthcheck = %q", test[1])
	}
	want := "DATABASE_URL=postgres://o%27neil:p%40ss%20$word%3A%20%231@localhost:5432/true?sslmode=disable\n"
	if !strings.Contains(string(files[gen.EnvPath]), want) {
		t.Errorf(".env.example, want %s:\n%s", want, files[gen.EnvPath])
	}
	u, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(want, "DATABASE_URL=")))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := u.User.Password(); u.User.Username() != "o'neil" || p != "p@ss $word: #1" {
		t.Errorf("url = %v", u)
	}
}

func TestPostgresComposeRejectsBadSettings(t *testing.T) {
	for _, tc := range []struct{ section, want string }{
		{"port: 0", "port 0 is not between 1 and 65535"},
		{"port: 70000", "port 70000 is not between 1 and 65535"},
		{"port: \"5432\"", "option compose.port must be an integer"},
		{"image: \" \"", "image must not be empty"},
		{"image: \"postgres 16\"", `image "postgres 16" has spaces`},
		{"db: \"a\\nb\"", "has control characters"},
		{"user: \"\\t\"", "user must not be empty"},
		{"command: [\"\"]", `command[0] "" must be a non-empty argument`},
		{"command: -c", "option compose.command must be a list"},
		{"init: /etc/init.sql", `init "/etc/init.sql" must be a folder or a file inside the project`},
		{"init: ../init.sql", `init "../init.sql" must be a folder or a file inside the project`},
		{"init: .", `init "." must be a folder or a file inside the project`},
		{"init: \"db/a:b.sql\"", "without a colon"},
		{"data_dir: data", `data_dir "data" must be an absolute folder`},
		{"enabled: \"no\"", "option compose.enabled must be true or false"},
		{"typo: 1", `unknown key "typo"`},
	} {
		f, err := spec.Parse([]byte(postgresProject + "\n    compose:\n      " + tc.section + "\n"))
		if err == nil {
			_, err = gen.Wiring(f)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.section, err, tc.want)
		}
	}
}

func TestJetSettings(t *testing.T) {
	d, err := gen.Jet(mustParse(t, postgresProject+" {}\n"))
	if err != nil || d.Out != "internal/db/jetgen" {
		t.Errorf("defaults = %+v, %v", d, err)
	}
	j, err := gen.Jet(mustParse(t, postgresProject+"\n    jet:\n      out: ./internal/adapters/out/db/jet/\n"))
	if err != nil || j.Out != "internal/adapters/out/db/jet" {
		t.Errorf("settings = %+v, %v", j, err)
	}
}

// The folder of jet is not a generated file: moving it changes nothing platformgo writes.
func TestJetOutChangesNoFile(t *testing.T) {
	before, err := gen.Wiring(mustParse(t, postgresProject+" {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := gen.Wiring(mustParse(t, postgresProject+"\n    jet:\n      out: internal/adapters/out/db/jet\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("files: %d before, %d after", len(before), len(after))
	}
	for path, content := range before {
		if string(after[path]) != string(content) {
			t.Errorf("%s changed", path)
		}
	}
}

func TestJetRejectsBadSettings(t *testing.T) {
	for _, tc := range []struct{ section, want string }{
		{"jet: {out: /etc}", `option jet: out "/etc" must be a folder inside the project`},
		{"jet: {out: ../jet}", `option jet: out "../jet" must be a folder inside the project`},
		{"jet: {out: .}", `option jet: out "." must be a folder inside the project`},
		{"jet: {out: \"a\\nb\"}", "must be a folder inside the project"},
		{"jet: {out: db}", "out \"db\" overlaps the migrations folder db/migrations"},
		{"jet: {out: db/migrations/jet}", "overlaps the migrations folder"},
		{"jet: {out: db/queries}", "overlaps the queries folder db/queries"},
		{"{jet: {out: db/sqlc/queries/x}, sqlc: {dir: db/sqlc}}", "overlaps the queries folder db/sqlc/queries"},
		{"jet: {out: 5}", "option jet.out must be a string"},
		{"jet: {typo: x}", `unknown key "typo"`},
		// The sqlc option checks its output against the folder jet actually uses.
		{"jet: {out: internal/store}", ""},
		{"{jet: {out: internal/store}, sqlc: {out: internal/store/sqlc}}", `option sqlc: out "internal/store/sqlc" overlaps the jet output folder internal/store`},
		{"{jet: {out: internal/store/jet}, sqlc: {out: internal/store}}", "overlaps the jet output folder internal/store/jet"},
	} {
		section := "\n    " + tc.section + "\n"
		if strings.HasPrefix(tc.section, "{") {
			section = " " + tc.section + "\n"
		}
		f, err := spec.Parse([]byte(postgresProject + section))
		if err == nil {
			_, err = gen.Wiring(f)
		}
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.section, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.section, err, tc.want)
		}
	}
	// The old default moves with the option: sqlc may now use it.
	if _, err := gen.Wiring(mustParse(t, postgresProject+" {jet: {out: internal/store/jet}, sqlc: {out: internal/db/jetgen}}\n")); err != nil {
		t.Errorf("sqlc in the old jet folder: %v", err)
	}
}

// The example environment leaves RIVER_QUEUES empty: make run reads .env.example when
// there is no .env, and a value there would win over riverx.QueuesFrom.
func TestEnvExampleLeavesRiverQueuesEmpty(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, "schema: 1\nproject:\n  module: example.com/app\nmodules:\n  postgres: {}\n  river: {}\n"))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	env := string(files[gen.EnvPath])
	if !strings.Contains(env, "\nRIVER_QUEUES=\n") || strings.Contains(env, "RIVER_QUEUES=default") {
		t.Errorf("RIVER_QUEUES in .env.example:\n%s", env)
	}
	if !strings.Contains(env, "wins over riverx.QueuesFrom") {
		t.Errorf("no word on the precedence:\n%s", env)
	}
}
