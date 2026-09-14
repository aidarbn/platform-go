package setup

import (
	"fmt"
	"strings"
)

const scriptHeader = "# Completion for platformgo, written by platformgo setup. Do not edit.\n"

func zshEscape(s string) string {
	return strings.NewReplacer(`'`, `'\''`, `[`, `\[`, `]`, `\]`, `:`, `\:`).Replace(s)
}

func zsh(cmds []Command, names []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	b.WriteString("_platformgo() {\n  local -a commands\n  commands=(\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "    '%s:%s'\n", c.Name, zshEscape(c.Summary))
	}
	b.WriteString("  )\n  if (( CURRENT == 2 )); then\n    _describe command commands\n    return\n  fi\n")
	b.WriteString("  local cmd=$words[2]\n  shift words\n  (( CURRENT-- ))\n  case $cmd in\n")
	for _, c := range cmds {
		var specs []string
		for _, f := range c.Flags {
			spec := "'--" + f.Name + "[" + zshEscape(f.Summary) + "]"
			switch {
			case f.Bool:
			case f.Dir:
				spec += ":directory:_files -/"
			case f.List:
				spec += ":" + f.Name + ":_sequence compadd - " + strings.Join(f.Values, " ")
			case len(f.Values) > 0:
				spec += ":" + f.Name + ":(" + strings.Join(f.Values, " ") + ")"
			default:
				spec += ":" + f.Name + ": "
			}
			specs = append(specs, spec+"'")
		}
		switch {
		case len(c.Sub) > 0:
			specs = append(specs, "'1:subcommand:("+strings.Join(c.Sub, " ")+")'")
		case c.Args == "dir":
			specs = append(specs, "'1:directory:_files -/'")
		}
		if len(specs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "    %s)\n      _arguments %s\n      ;;\n", c.Name, strings.Join(specs, " \\\n        "))
	}
	b.WriteString("  esac\n}\n")
	fmt.Fprintf(&b, "compdef _platformgo %s\n", strings.Join(names, " "))
	return b.String()
}

func bash(cmds []Command, names []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	var commandNames []string
	for _, c := range cmds {
		commandNames = append(commandNames, c.Name)
	}
	b.WriteString("_platformgo() {\n")
	b.WriteString("  local cur=\"${COMP_WORDS[COMP_CWORD]}\" prev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n")
	b.WriteString("  if [ \"$COMP_CWORD\" -eq 1 ]; then\n")
	fmt.Fprintf(&b, "    COMPREPLY=($(compgen -W %q -- \"$cur\"))\n    return\n  fi\n", strings.Join(commandNames, " "))
	b.WriteString("  case \"${COMP_WORDS[1]}\" in\n")
	for _, c := range cmds {
		var flags []string
		for _, f := range c.Flags {
			flags = append(flags, "--"+f.Name)
		}
		fmt.Fprintf(&b, "    %s)\n      case \"$prev\" in\n", c.Name)
		for _, f := range c.Flags {
			switch {
			case f.Bool:
			case f.Dir:
				fmt.Fprintf(&b, "        --%s) COMPREPLY=($(compgen -d -- \"$cur\")); return ;;\n", f.Name)
			case f.List:
				fmt.Fprintf(&b, "        --%s)\n          local done=\"\"\n          [[ \"$cur\" == *,* ]] && done=\"${cur%%,*},\"\n          COMPREPLY=($(compgen -P \"$done\" -W %q -- \"${cur##*,}\")); compopt -o nospace; return ;;\n", f.Name, strings.Join(f.Values, " "))
			case len(f.Values) > 0:
				fmt.Fprintf(&b, "        --%s) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n", f.Name, strings.Join(f.Values, " "))
			default:
				fmt.Fprintf(&b, "        --%s) return ;;\n", f.Name)
			}
		}
		b.WriteString("      esac\n")
		words := flags
		if len(c.Sub) > 0 {
			fmt.Fprintf(&b, "      if [ \"$COMP_CWORD\" -eq 2 ]; then COMPREPLY=($(compgen -W %q -- \"$cur\")); return; fi\n", strings.Join(c.Sub, " "))
		}
		if c.Args == "dir" {
			b.WriteString("      if [[ \"$cur\" != -* ]]; then COMPREPLY=($(compgen -d -- \"$cur\")); return; fi\n")
		}
		fmt.Fprintf(&b, "      COMPREPLY=($(compgen -W %q -- \"$cur\"))\n      ;;\n", strings.Join(words, " "))
	}
	b.WriteString("  esac\n}\n")
	fmt.Fprintf(&b, "complete -F _platformgo %s\n", strings.Join(names, " "))
	return b.String()
}

func fishEscape(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `\'`) + "'"
}

func fish(cmds []Command, names []string) string {
	var b strings.Builder
	b.WriteString(scriptHeader)
	for _, name := range names {
		if name != "platformgo" {
			fmt.Fprintf(&b, "complete -c %s -w platformgo\n", name)
		}
	}
	b.WriteString("complete -c platformgo -f\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "complete -c platformgo -n __fish_use_subcommand -a %s -d %s\n", c.Name, fishEscape(c.Summary))
		cond := "'__fish_seen_subcommand_from " + c.Name + "'"
		for _, s := range c.Sub {
			fmt.Fprintf(&b, "complete -c platformgo -n %s -a %s\n", cond, s)
		}
		for _, f := range c.Flags {
			line := fmt.Sprintf("complete -c platformgo -n %s -l %s -d %s", cond, f.Name, fishEscape(f.Summary))
			switch {
			case f.Bool:
			case f.Dir:
				line += " -r -F"
			case len(f.Values) > 0:
				line += " -x -a " + fishEscape(strings.Join(f.Values, " "))
			default:
				line += " -r"
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}
