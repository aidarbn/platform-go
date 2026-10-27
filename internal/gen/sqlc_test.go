package gen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

const sqlcProject = "schema: 1\nproject:\n  module: example.com/app\nmodules:\n  postgres:"

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs:\n%s", name, got)
	}
}

// Without the option every project gets the file it had before the option existed,
// byte for byte: testdata/sqlc.default.yaml is that file.
func TestSqlcDefaultsAreTheFileBefore(t *testing.T) {
	for _, section := range []string{" {}\n", "\n    sqlc: {}\n", "\n    sqlc:\n      dir: db\n      out: internal/db/sqlcgen/\n      overrides: []\n      renames: {}\n"} {
		files, err := gen.Wiring(mustParse(t, sqlcProject+section))
		if err != nil {
			t.Fatalf("Wiring: %v", err)
		}
		golden(t, "sqlc.default.yaml", files[gen.SqlcPath])
	}
}

// taplySqlc is the layout and type mapping of taply's db/sqlc/sqlc.yaml.
const taplySqlc = `
    sqlc:
      dir: db/sqlc
      out: internal/adapters/out/db/sqlcgen
      emit_interface: false
      renames:
        url: URL
        sku: SKU
      overrides:
        - {db_type: int4, go_type: int64}
        - {db_type: int4, nullable: true, go_type: {type: int64, pointer: true}}
        - {db_type: numeric, go_type: float64}
        - {db_type: numeric, nullable: true, go_type: {type: float64, pointer: true}}
        - {column: orders.meta, go_type: {import: example.com/app/internal/domain, package: domain, type: Meta}}
        - {db_type: "text", nullable: true, go_type: {type: string, pointer: true, slice: false}}
        - {db_type: "true", go_type: "1"}
        - {db_type: "0x1F", go_type: "1_000"}
`

func TestSqlcTaplyLayout(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, sqlcProject+taplySqlc))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	if _, ok := files[gen.SqlcPath]; ok {
		t.Errorf("%s is still generated after the configuration moved", gen.SqlcPath)
	}
	golden(t, "sqlc.taply.yaml", files["db/sqlc/sqlc.yaml"])
}

func TestSqlcSettings(t *testing.T) {
	s, err := gen.Sqlc(mustParse(t, sqlcProject+taplySqlc))
	if err != nil {
		t.Fatal(err)
	}
	if s.ConfigPath() != "db/sqlc/sqlc.yaml" || s.QueriesDir() != "db/sqlc/queries" || s.Out != "internal/adapters/out/db/sqlcgen" || s.EmitInterface {
		t.Errorf("settings = %+v", s)
	}
	d, err := gen.Sqlc(mustParse(t, sqlcProject+" {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if d.ConfigPath() != gen.SqlcPath || d.QueriesDir() != "db/queries" || d.Out != "internal/db/sqlcgen" || d.Package != "sqlcgen" || !d.EmitInterface {
		t.Errorf("defaults = %+v", d)
	}
}

func TestSqlcRejectsBadSettings(t *testing.T) {
	for _, tc := range []struct{ section, want string }{
		{"dir: /etc", `dir "/etc" must be a folder inside the project`},
		{"dir: ../other", `dir "../other" must be a folder inside the project`},
		{"queries: ../../..", `queries "../../.." must be a folder inside the project`},
		{"out: .", `out "." must be a folder inside the project`},
		{"queries: /abs", `queries "/abs" must be a folder inside the project`},
		{"dir: \"db\\nx\"", `must be a folder inside the project`},
		{"out: db/migrations", `overlaps the migrations folder`},
		{"out: db/queries", `overlaps the queries folder`},
		{"out: db/queries/gen", `overlaps the queries folder`},
		{"out: db", `overlaps the dir folder db`},
		{"out: internal", `overlaps the jet output folder`},
		{"out: internal/db/jetgen/x", `overlaps the jet output folder`},
		{"overrides: [{db_type: int4, go_type: {typo: 1, type: x}}]", `unknown key "typo"`},
		{"package: my-pkg", `package "my-pkg" is not a Go package name`},
		{"overrides: [{go_type: int64}]", "overrides[0]: set db_type or column"},
		{"overrides: [{db_type: int4, column: a.b, go_type: int64}]", "overrides[0]: db_type and column exclude each other"},
		{"overrides: [{db_type: int4, go_type: {import: x}}]", "missing required key type"},
		{"overrides: [{db_type: int4, go_type: \"\"}]", "overrides[0]: go_type needs a type"},
	} {
		f, err := spec.Parse([]byte(sqlcProject + "\n    sqlc:\n      " + tc.section + "\n"))
		if err == nil {
			_, err = gen.Wiring(f)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.section, err, tc.want)
		}
	}
}

// The generated configuration runs through the real sqlc: the layout resolves and the
// overrides of the project win over the platform's.
func TestSqlcOverridesWithRealSqlc(t *testing.T) {
	if testing.Short() {
		t.Skip("builds sqlc")
	}
	dir := t.TempDir()
	files, err := gen.Wiring(mustParse(t, sqlcProject+`
    sqlc:
      dir: db/sqlc
      out: internal/store/gen
      package: store
      emit_interface: false
      overrides:
        - {db_type: int4, go_type: int64}
        - {db_type: int4, nullable: true, go_type: {type: int64, pointer: true}}
        - {db_type: uuid, go_type: string}
      renames: {url: URL}
`))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "db/sqlc/sqlc.yaml", string(files["db/sqlc/sqlc.yaml"]))
	write(t, dir, "db/migrations/0001_init.sql", `-- +goose Up
CREATE TABLE items (
  id uuid PRIMARY KEY,
  qty int4 NOT NULL,
  spare int4,
  price numeric(10,2) NOT NULL,
  url text NOT NULL,
  made_at timestamptz NOT NULL
);
-- +goose Down
DROP TABLE items;
`)
	write(t, dir, "db/sqlc/queries/items.sql", "-- name: GetItem :one\nSELECT * FROM items WHERE id = $1;\n")

	cmd := exec.Command("go", "run", "github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1", "generate", "-f", "db/sqlc/sqlc.yaml")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlc generate: %v\n%s", err, out)
	}
	models, err := os.ReadFile(filepath.Join(dir, "internal/store/gen/models.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(models), "package store") {
		t.Errorf("package option ignored:\n%s", models)
	}
	for _, want := range []string{
		`ID\s+string\n`,              // the project's uuid override wins over the platform's
		`Qty\s+int64\n`,              // int4 of the project
		`Spare\s+\*int64\n`,          // nullable int4 of the project
		`Price\s+decimal\.Decimal\n`, // numeric of the platform
		`URL\s+string\n`,             // rename
		`MadeAt\s+time\.Time\n`,      // timestamptz of the platform
	} {
		if !regexp.MustCompile(want).Match(models) {
			t.Errorf("models.go lacks %s:\n%s", want, models)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "internal/store/gen/querier.go")); err == nil {
		t.Error("emit_interface: false still generated the Querier")
	}
}
