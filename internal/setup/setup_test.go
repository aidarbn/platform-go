package setup_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/setup"
)

var commands = []setup.Command{
	{Name: "new", Summary: "create a project", Args: "", Flags: []setup.Flag{
		{Name: "dir", Summary: "project directory", Dir: true},
		{Name: "with", Summary: "modules: comma separated", Values: []string{"api", "postgres"}, List: true},
	}},
	{Name: "migrate", Summary: "migrations", Sub: []string{"create"}},
	{Name: "generate", Summary: "regenerate [files]", Flags: []setup.Flag{{Name: "check", Summary: "fail when stale", Bool: true}}},
}

func TestDetect(t *testing.T) {
	if got, err := setup.Detect("/bin/zsh"); err != nil || got != "zsh" {
		t.Errorf("Detect(/bin/zsh) = %q, %v", got, err)
	}
	if _, err := setup.Detect("/bin/tcsh"); err == nil {
		t.Error("tcsh is accepted")
	}
	if _, err := setup.Detect(""); err == nil {
		t.Error("an empty SHELL is accepted")
	}
}

func TestInstallIsIdempotentAndRemovable(t *testing.T) {
	for _, shell := range setup.Shells {
		t.Run(shell, func(t *testing.T) {
			home := t.TempDir()
			rc := map[string]string{"zsh": ".zshrc", "bash": ".bashrc"}[shell]
			const userRC = "export EDITOR=vim\nalias ll='ls -l'"
			if rc != "" {
				if err := os.WriteFile(filepath.Join(home, rc), []byte(userRC), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			o := setup.Options{Home: home, Shell: shell, Alias: true}

			changes, err := setup.Install(o, commands)
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			wantChanges := 2
			if rc == "" {
				wantChanges = 1
			}
			if len(changes) != wantChanges {
				t.Errorf("first install changed %v", changes)
			}
			again, err := setup.Install(o, commands)
			if err != nil || len(again) != 0 {
				t.Errorf("second install changed %v, %v", again, err)
			}

			if rc != "" {
				content, _ := os.ReadFile(filepath.Join(home, rc))
				if !strings.HasPrefix(string(content), userRC+"\n\n# >>> platformgo >>>\n") || strings.Count(string(content), "platformgo >>>") != 1 {
					t.Errorf("%s:\n%s", rc, content)
				}
				if !strings.Contains(string(content), "alias pgo=platformgo") {
					t.Errorf("no alias in %s", rc)
				}
				// Without the alias the block is replaced, not added.
				o.Alias = false
				if _, err := setup.Install(o, commands); err != nil {
					t.Fatal(err)
				}
				content, _ = os.ReadFile(filepath.Join(home, rc))
				if strings.Contains(string(content), "alias pgo") || strings.Count(string(content), "platformgo >>>") != 1 {
					t.Errorf("block not replaced:\n%s", content)
				}
			}

			o.Remove = true
			if _, err := setup.Install(o, commands); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if rc != "" {
				content, _ := os.ReadFile(filepath.Join(home, rc))
				if strings.TrimRight(string(content), "\n") != userRC {
					t.Errorf("remove left:\n%q", content)
				}
			}
			entries, _ := filepath.Glob(filepath.Join(home, ".config", "*", "*platformgo*"))
			more, _ := filepath.Glob(filepath.Join(home, ".config", "*", "*", "platformgo*"))
			if len(entries)+len(more) != 0 {
				t.Errorf("remove left %v %v", entries, more)
			}
		})
	}
}

func TestBrokenBlockIsNotTouched(t *testing.T) {
	home := t.TempDir()
	broken := "# >>> platformgo >>>\nalias pgo=platformgo\n"
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte(broken), 0o644)
	if _, err := setup.Install(setup.Options{Home: home, Shell: "zsh"}, commands); err == nil {
		t.Fatal("a broken block is accepted")
	}
	content, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if string(content) != broken {
		t.Errorf("a broken file was changed:\n%s", content)
	}
}

// The scripts are checked by the shells themselves when they are installed.
func TestScriptsParse(t *testing.T) {
	for shell, check := range map[string][]string{
		"zsh":  {"zsh", "-n"},
		"bash": {"bash", "-n"},
		"fish": {"fish", "--no-execute"},
	} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(check[0]); err != nil {
				t.Skipf("%s is not installed", check[0])
			}
			script, err := setup.Completion(shell, commands, "platformgo", "pgo")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "completion")
			os.WriteFile(path, []byte(script), 0o644)
			if out, err := exec.Command(check[0], append(check[1:], path)...).CombinedOutput(); err != nil {
				t.Errorf("%s: %v\n%s\n%s", shell, err, out, script)
			}
		})
	}
}

func TestZshCompletionWorks(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is not installed")
	}
	script, _ := setup.Completion("zsh", commands, "platformgo", "pgo")
	path := filepath.Join(t.TempDir(), "completion.zsh")
	os.WriteFile(path, []byte(script), 0o644)
	out, err := exec.Command("zsh", "-f", "-c", "autoload -Uz compinit && compinit -u -D && source "+path+" && print -r -- ${_comps[pgo]} ${_comps[platformgo]}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "_platformgo _platformgo" {
		t.Errorf("zsh did not register the completion: %v\n%s", err, out)
	}
}

func TestBashCompletionWorks(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	script, _ := setup.Completion("bash", commands, "platformgo", "pgo")
	path := filepath.Join(t.TempDir(), "completion.bash")
	os.WriteFile(path, []byte(script), 0o644)
	for line, want := range map[string]string{
		"pgo ge":                       "generate",
		"platformgo new --with ":       "api postgres",
		"platformgo new --with api,po": "api,postgres",
		"platformgo migrate c":         "create",
	} {
		words := strings.Split(line, " ")
		cmd := "compopt() { :; }; source " + path + "; COMP_WORDS=(" + quoteWords(words) + "); COMP_CWORD=" + itoa(len(words)-1) + "; _platformgo; echo ${COMPREPLY[@]}"
		out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("%q completes to %q, want %q (%v)", line, strings.TrimSpace(string(out)), want, err)
		}
	}
}

func quoteWords(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = "'" + w + "'"
	}
	return strings.Join(q, " ")
}

func itoa(n int) string { return strconv.Itoa(n) }
