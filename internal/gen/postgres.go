package gen

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
)

// postgresSection is the section of the postgres module in platformgo.yaml.
type postgresSection struct {
	Sqlc    SqlcSettings    `yaml:"sqlc"`
	Compose PostgresCompose `yaml:"compose"`
}

// postgresOptions reads the section of the postgres module, with the defaults for what
// the file leaves out.
func postgresOptions(f *spec.File) (postgresSection, error) {
	var opts postgresSection
	err := f.DecodeOptions("postgres", &opts)
	return opts, err
}

// PostgresCompose is the compose option of the postgres module: the local database in
// docker-compose.yml and the DATABASE_URL example in .env.example. With the defaults
// both are what every project had before the option existed, byte for byte.
type PostgresCompose struct {
	Enabled  bool     `yaml:"enabled"`
	Image    string   `yaml:"image"`
	DB       string   `yaml:"db"`
	User     string   `yaml:"user"`
	Password string   `yaml:"password"`
	Port     int      `yaml:"port"`
	DataDir  string   `yaml:"data_dir"`
	Command  []string `yaml:"command"`
	Init     string   `yaml:"init"` // relative to the project root; empty for none
}

// initDir is where the postgres image looks for scripts to run on an empty database.
const initDir = "/docker-entrypoint-initdb.d"

// PostgresComposeSettings reads the compose option of the postgres module, with the
// defaults for what the file leaves out.
func PostgresComposeSettings(f *spec.File) (PostgresCompose, error) {
	opts, err := postgresOptions(f)
	if err != nil {
		return PostgresCompose{}, err
	}
	c := opts.Compose
	if c.Init != "" {
		c.Init = path.Clean(strings.TrimSpace(c.Init))
	}
	if err := c.check(); err != nil {
		return PostgresCompose{}, fmt.Errorf("module postgres: option compose: %w", err)
	}
	return c, nil
}

func (c PostgresCompose) check() error {
	var errs []error
	for _, v := range []struct{ name, value string }{
		{"image", c.Image}, {"db", c.DB}, {"user", c.User}, {"password", c.Password},
	} {
		switch {
		case strings.TrimSpace(v.value) == "":
			errs = append(errs, fmt.Errorf("%s must not be empty", v.name))
		case strings.ContainsFunc(v.value, unicode.IsControl):
			errs = append(errs, fmt.Errorf("%s %q has control characters", v.name, v.value))
		}
	}
	if strings.ContainsFunc(c.Image, unicode.IsSpace) {
		errs = append(errs, fmt.Errorf("image %q has spaces", c.Image))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d is not between 1 and 65535", c.Port))
	}
	if !path.IsAbs(c.DataDir) || strings.ContainsFunc(c.DataDir, unicode.IsControl) || strings.Contains(c.DataDir, ":") {
		errs = append(errs, fmt.Errorf("data_dir %q must be an absolute folder inside the container", c.DataDir))
	}
	for i, arg := range c.Command {
		if arg == "" || strings.ContainsFunc(arg, unicode.IsControl) {
			errs = append(errs, fmt.Errorf("command[%d] %q must be a non-empty argument without control characters", i, arg))
		}
	}
	if c.Init != "" && (!lock.SafePath(c.Init) || path.IsAbs(c.Init) || strings.ContainsFunc(c.Init, unicode.IsControl) || strings.Contains(c.Init, ":")) {
		errs = append(errs, fmt.Errorf("init %q must be a folder or a file inside the project, without a colon", c.Init))
	}
	return errors.Join(errs...)
}

// DatabaseURL is the address of the local database, the DATABASE_URL example.
func (c PostgresCompose) DatabaseURL() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     "localhost:" + strconv.Itoa(c.Port),
		Path:     "/" + c.DB,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// initMount is the volume of the init option: a script goes into the folder of the
// image under its own name, a folder replaces it.
func (c PostgresCompose) initMount() string {
	target := initDir
	for _, ext := range []string{".sql", ".sql.gz", ".sql.xz", ".sql.zst", ".sh"} {
		if strings.HasSuffix(c.Init, ext) {
			target = initDir + "/" + path.Base(c.Init)
			break
		}
	}
	return "./" + c.Init + ":" + target + ":ro"
}

// service is the postgres entry under services of docker-compose.yml.
func (c PostgresCompose) service() string {
	var b strings.Builder
	b.WriteString("  postgres:\n")
	b.WriteString("    image: " + composeString(c.Image) + "\n")
	if len(c.Command) > 0 {
		args := make([]string, len(c.Command))
		for i, a := range c.Command {
			args[i] = composeQuote(a)
		}
		b.WriteString("    command: [" + strings.Join(args, ", ") + "]\n")
	}
	b.WriteString("    environment:\n")
	b.WriteString("      POSTGRES_USER: " + composeString(c.User) + "\n")
	b.WriteString("      POSTGRES_PASSWORD: " + composeString(c.Password) + "\n")
	b.WriteString("      POSTGRES_DB: " + composeString(c.DB) + "\n")
	b.WriteString("    ports:\n")
	b.WriteString("      - \"127.0.0.1:" + strconv.Itoa(c.Port) + ":5432\"\n")
	b.WriteString("    volumes:\n")
	b.WriteString("      - " + composeString("postgres-data:"+c.DataDir) + "\n")
	if c.Init != "" {
		b.WriteString("      - " + composeQuote(c.initMount()) + "\n")
	}
	b.WriteString("    healthcheck:\n")
	b.WriteString("      test: [\"CMD-SHELL\", " + composeQuote("pg_isready -U "+shellWord(c.User)+" -d "+shellWord(c.DB)) + "]\n")
	b.WriteString("      interval: 2s\n")
	b.WriteString("      timeout: 5s\n")
	b.WriteString("      retries: 30")
	return b.String()
}

// apply puts the local database of the option into the postgres module.
func (c PostgresCompose) apply(m registry.Module) registry.Module {
	if c.Enabled {
		m.Compose = c.service()
	} else {
		m.Compose, m.Volumes = "", nil
	}
	env := make([]registry.EnvVar, len(m.Env))
	copy(env, m.Env)
	for i := range env {
		if env[i].Key == "DATABASE_URL" {
			env[i].Example = c.DatabaseURL()
		}
	}
	m.Env = env
	return m
}

var (
	plainCompose = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./:@+-]*$`)
	plainShell   = regexp.MustCompile(`^[A-Za-z0-9_.@%+=:,/-]+$`)
)

// composeString writes a value of docker-compose.yml plain when YAML reads it back as
// the same string, and quoted otherwise. A $ is doubled: compose interpolates it.
func composeString(s string) string {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "y", "n", "null":
		return composeQuote(s)
	}
	if plainCompose.MatchString(s) && !strings.HasSuffix(s, ":") {
		return s
	}
	return composeQuote(s)
}

// composeQuote writes a quoted value of docker-compose.yml.
func composeQuote(s string) string {
	return strconv.Quote(strings.ReplaceAll(s, "$", "$$"))
}

// shellWord quotes a word of a CMD-SHELL command when the shell would split or expand it.
func shellWord(s string) string {
	if plainShell.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
