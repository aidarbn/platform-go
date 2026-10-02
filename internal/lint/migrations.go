package lint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/internal/codegen"
)

// Squawk lints the SQL of migrations: the locks a statement takes, changes an older
// release of the service cannot live with, constraints that scan a whole table. It is
// a Rust binary, so it is downloaded at a pinned version and checked against the
// digest GitHub publishes for the release asset.
const squawkVersion = "2.66.0"

var squawkSums = map[string]string{
	"darwin-arm64": "d273f3a234b81b8d540b2cdf1b07ed84f719a3ab11b47d6f3efe96baaf21b40b",
	"darwin-x64":   "e361365762a6bf11abbef746d29ff46ac0b4feba43df2f0570595f06c67d7daf",
	"linux-arm64":  "04455267bb895de1568dc24e52eb062b311096ad614c3e15a77cf8a7b3483c2a",
	"linux-x64":    "e7965f8146b53cfa7a4625ceecfea5c6aeb35add39132210aa60b11ae180b9f4",
}

// squawkExcluded are the rules about style rather than safety: they flag every int
// and varchar column and would drown the findings that matter. statement_timeout is
// left out on purpose — a backfill may legitimately run long, while lock_timeout,
// which keeps a migration from queueing every query behind it, stays required.
var squawkExcluded = []string{
	"prefer-text-field",
	"prefer-bigint-over-int",
	"prefer-bigint-over-smallint",
	"prefer-identity",
	"require-statement-timeout",
}

// Exec runs a command and returns its combined output. Tests replace it.
type Exec func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// Migrations lints the goose migrations a branch adds. Migrations already applied
// are history: the databases lived through them, and a rule added later must not
// turn every old file red.
type Migrations struct {
	Dir     string                                    // migrations folder in the project; codegen.MigrationsDir when empty
	Against string                                    // base branch; empty lints the files not committed yet
	Exec    Exec                                      // runs git and squawk
	Squawk  func(ctx context.Context) (string, error) // path to the squawk binary; DownloadSquawk when nil
}

var (
	gooseDown    = regexp.MustCompile(`(?i)^--\s*\+goose\s+down\b`)
	gooseNoTx    = regexp.MustCompile(`(?im)^--\s*\+goose\s+no\s+transaction\b`)
	dbLogic      = regexp.MustCompile(`(?i)^\s*create\s+(or\s+replace\s+)?(function|procedure|rule|(constraint\s+)?trigger)\b`)
	concurrently = regexp.MustCompile(`(?i)\bconcurrently\b`)
)

// Run lints every migration the branch adds and returns all findings at once.
func (m Migrations) Run(ctx context.Context, dir string) error {
	if m.Exec == nil {
		m.Exec = ExecCommand
	}
	files, err := m.newFiles(ctx, dir)
	if err != nil || len(files) == 0 {
		return err
	}
	squawkPath := m.Squawk
	if squawkPath == nil {
		squawkPath = DownloadSquawk
	}
	squawk, err := squawkPath(ctx)
	if err != nil {
		return err
	}

	var findings []string
	for _, rel := range files {
		content, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		up := upSection(content)
		noTx := gooseNoTx.Match(up)
		findings = append(findings, ownRules(rel, up, noTx)...)
		out, err := m.squawk(ctx, dir, squawk, rel, up, noTx)
		if err != nil {
			findings = append(findings, strings.TrimSpace(out))
		}
	}
	if len(findings) > 0 {
		return fmt.Errorf("%s\nsilence a deliberate finding with a comment above the statement: -- squawk-ignore <rule>, and say why", strings.Join(findings, "\n"))
	}
	return nil
}

// newFiles lists the .sql files under the migrations folder that the base branch does
// not have, untracked ones included.
func (m Migrations) newFiles(ctx context.Context, dir string) ([]string, error) {
	folder := m.Dir
	if folder == "" {
		folder = codegen.MigrationsDir
	}
	ref := "HEAD"
	if m.Against != "" {
		ref = m.Against
		// CI checks out a branch without the base as a local branch.
		if _, err := m.Exec(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "origin/"+m.Against); err == nil {
			ref = "origin/" + m.Against
		}
	}
	added, err := m.Exec(ctx, dir, "git", "diff", "--name-only", "--diff-filter=A", ref, "--", folder)
	if err != nil {
		return nil, fmt.Errorf("git diff against %s: %w\n%s", ref, err, added)
	}
	untracked, err := m.Exec(ctx, dir, "git", "ls-files", "--others", "--exclude-standard", "--", folder)
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w\n%s", err, untracked)
	}
	var files []string
	for _, line := range strings.Split(string(added)+"\n"+string(untracked), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".sql") && !slices.Contains(files, line) {
			files = append(files, line)
		}
	}
	slices.Sort(files)
	return files, nil
}

// upSection keeps the file up to the Down marker. Down undoes the migration and is
// supposed to drop what Up created; the lines before the marker are kept as they are,
// so squawk reports the same line numbers as the file.
func upSection(content []byte) []byte {
	var b bytes.Buffer
	for _, line := range strings.SplitAfter(string(content), "\n") {
		if gooseDown.MatchString(strings.TrimSpace(line)) {
			break
		}
		b.WriteString(line)
	}
	return b.Bytes()
}

// ownRules are the rules squawk does not know: logic (functions, procedures,
// triggers, rules) does not live in the database,
// and goose wraps every migration in a transaction, where an index cannot be built
// concurrently.
func ownRules(rel string, up []byte, noTx bool) []string {
	var findings []string
	for i, line := range strings.Split(string(up), "\n") {
		code := line
		if idx := strings.Index(code, "--"); idx >= 0 {
			code = code[:idx]
		}
		if dbLogic.MatchString(code) {
			findings = append(findings, fmt.Sprintf("%s:%d: functions, procedures, triggers and rules are not created in migrations: the logic lives in Go", rel, i+1))
		}
		if !noTx && concurrently.MatchString(code) {
			findings = append(findings, fmt.Sprintf("%s:%d: CONCURRENTLY cannot run inside a transaction, which goose opens for every migration: add -- +goose NO TRANSACTION", rel, i+1))
		}
	}
	return findings
}

// squawk lints one Up section. It runs in a temporary folder that mirrors the path of
// the migration, so the findings name the file in the project, and with the project
// .squawk.toml when there is one.
func (m Migrations) squawk(ctx context.Context, dir, squawk, rel string, up []byte, noTx bool) (string, error) {
	tmp, err := os.MkdirTemp("", "platformgo-migrations-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, up, 0o600); err != nil {
		return "", err
	}

	args := []string{"--reporter=gcc", "--exclude=" + strings.Join(squawkExcluded, ",")}
	if !noTx {
		args = append(args, "--assume-in-transaction")
	}
	if config, err := filepath.Abs(filepath.Join(dir, ".squawk.toml")); err == nil {
		if _, err := os.Stat(config); err == nil {
			args = append(args, "--config="+config)
		}
	}
	out, err := m.Exec(ctx, tmp, squawk, append(args, rel)...)
	return string(out), err
}

// DownloadSquawk returns the pinned squawk binary, downloading it into the user cache
// on first use and refusing a file whose digest does not match.
func DownloadSquawk(ctx context.Context) (string, error) {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	platform := runtime.GOOS + "-" + arch
	sum, ok := squawkSums[platform]
	if !ok {
		return "", fmt.Errorf("squawk %s has no binary for %s", squawkVersion, platform)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(cache, "platformgo", "squawk", squawkVersion, "squawk")
	if content, err := os.ReadFile(path); err == nil && digest(content) == sum {
		return path, nil
	}

	url := fmt.Sprintf("https://github.com/sbdchd/squawk/releases/download/v%s/squawk-%s", squawkVersion, platform)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download squawk: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download squawk: %s: %s", url, resp.Status)
	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("download squawk: %w", err)
	}
	if got := digest(content); got != sum {
		return "", fmt.Errorf("download squawk: %s has sha256 %s, want %s", url, got, sum)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Written next to the target and renamed, so a parallel run never executes a half
	// written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", errors.Join(err, os.Remove(tmp))
	}
	return path, nil
}

func digest(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// ExecCommand is Exec for real commands.
func ExecCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
