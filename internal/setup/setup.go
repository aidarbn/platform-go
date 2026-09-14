// Package setup prepares the shell of a developer: completion for platformgo and the
// short pgo alias.
//
// The shell configuration gets one block between markers, so running setup again
// replaces the block and --remove takes it out without touching anything else.
package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Command is a command of the CLI as completion sees it.
type Command struct {
	Name    string
	Summary string
	Sub     []string // subcommands, such as create of migrate
	Args    string   // what a positional argument completes to: "dir" or ""
	Flags   []Flag
}

// Flag is a flag of a command.
type Flag struct {
	Name    string
	Summary string
	Bool    bool
	Dir     bool     // completes to directories
	Values  []string // completes to these values
	List    bool     // Values separated by commas
}

// Shells setup knows.
var Shells = []string{"bash", "fish", "zsh"}

// Detect returns the shell named by $SHELL.
func Detect(shellEnv string) (string, error) {
	name := filepath.Base(shellEnv)
	if slices.Contains(Shells, name) {
		return name, nil
	}
	if shellEnv == "" {
		return "", errors.New("SHELL is not set: pass --shell " + strings.Join(Shells, "|"))
	}
	return "", fmt.Errorf("shell %q is not supported: pass --shell %s", name, strings.Join(Shells, "|"))
}

// Completion returns the completion script of a shell for the binary and its aliases.
func Completion(shell string, cmds []Command, names ...string) (string, error) {
	if len(names) == 0 {
		names = []string{"platformgo"}
	}
	switch shell {
	case "zsh":
		return zsh(cmds, names), nil
	case "bash":
		return bash(cmds, names), nil
	case "fish":
		return fish(cmds, names), nil
	}
	return "", fmt.Errorf("shell %q is not supported: %s", shell, strings.Join(Shells, ", "))
}

// Options of Install.
type Options struct {
	Home   string // home directory
	Config string // XDG_CONFIG_HOME, <home>/.config when empty
	Shell  string
	Alias  bool // add pgo
	Remove bool // take everything out
}

// Change is a file Install wrote or removed.
type Change struct {
	Path    string
	Removed bool
}

const (
	beginMark = "# >>> platformgo >>>"
	endMark   = "# <<< platformgo <<<"
)

// ScriptPath is where the completion script of the shell goes.
func ScriptPath(o Options) (string, error) {
	if o.Home == "" {
		return "", errors.New("home directory is unknown")
	}
	config := o.Config
	if config == "" {
		config = filepath.Join(o.Home, ".config")
	}
	switch o.Shell {
	case "zsh":
		return filepath.Join(config, "platformgo", "completion.zsh"), nil
	case "bash":
		return filepath.Join(config, "platformgo", "completion.bash"), nil
	case "fish":
		// fish reads conf.d on its own: the file is the whole hook.
		return filepath.Join(config, "fish", "conf.d", "platformgo.fish"), nil
	}
	return "", fmt.Errorf("shell %q is not supported: %s", o.Shell, strings.Join(Shells, ", "))
}

// Install writes the completion script and hooks it into the shell configuration.
func Install(o Options, cmds []Command) ([]Change, error) {
	names := []string{"platformgo"}
	if o.Alias {
		names = append(names, "pgo")
	}

	script, err := ScriptPath(o)
	if err != nil {
		return nil, err
	}
	var rc, block string
	switch o.Shell {
	case "zsh":
		rc = filepath.Join(o.Home, ".zshrc")
		block = "(( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }\n" +
			"source " + shellQuote(script) + "\n"
	case "bash":
		rc = filepath.Join(o.Home, ".bashrc")
		block = "source " + shellQuote(script) + "\n"
	}
	if o.Alias && o.Shell != "fish" {
		block = "alias pgo=platformgo\n" + block
	}

	var changes []Change
	if o.Remove {
		if err := os.Remove(script); err == nil {
			changes = append(changes, Change{Path: script, Removed: true})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if rc != "" {
			changed, err := editBlock(rc, "")
			if err != nil {
				return nil, err
			}
			if changed {
				changes = append(changes, Change{Path: rc})
			}
		}
		return changes, nil
	}

	content, err := Completion(o.Shell, cmds, names...)
	if err != nil {
		return nil, err
	}
	if o.Shell == "fish" && o.Alias {
		content += "alias pgo platformgo\n"
	}
	changed, err := writeIfChanged(script, []byte(content))
	if err != nil {
		return nil, err
	}
	if changed {
		changes = append(changes, Change{Path: script})
	}
	if rc != "" {
		changed, err := editBlock(rc, block)
		if err != nil {
			return nil, err
		}
		if changed {
			changes = append(changes, Change{Path: rc})
		}
	}
	return changes, nil
}

func writeIfChanged(path string, content []byte) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, content, 0o644)
}

// editBlock puts block between the markers of the file, in place of the previous one;
// an empty block removes the markers too.
func editBlock(path, block string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	text := string(raw)
	wrapped := ""
	if block != "" {
		wrapped = beginMark + "\n" + block + endMark + "\n"
	}

	var updated string
	begin, end := strings.Index(text, beginMark), strings.Index(text, endMark)
	switch {
	case begin >= 0 && end > begin:
		before, after := text[:begin], strings.TrimPrefix(text[end+len(endMark):], "\n")
		if wrapped == "" && strings.HasSuffix(before, "\n\n") {
			before = before[:len(before)-1] // the blank line added with the block
		}
		updated = before + wrapped + after
	case begin >= 0 || end >= 0:
		return false, fmt.Errorf("%s: the platformgo block is broken, fix the %q and %q lines by hand", path, beginMark, endMark)
	case wrapped == "":
		return false, nil
	default:
		updated = text
		if updated != "" {
			updated = strings.TrimRight(updated, "\n") + "\n\n"
		}
		updated += wrapped
	}
	if updated == text {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(updated), 0o644)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
