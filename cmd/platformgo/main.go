// Command platformgo создаёт, генерирует и поддерживает Go-проекты на платформе.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}

	switch args[0] {
	case "generate":
		return cmdGenerate(args[1:], out)
	case "plan":
		return cmdPlan(args[1:], out)
	case "doctor":
		return cmdDoctor(args[1:], out)
	case "version":
		fmt.Fprintln(out, version())
		return nil
	case "help", "-h", "--help":
		usage(out)
		return nil
	default:
		usage(out)
		return fmt.Errorf("неизвестная команда %q", args[0])
	}
}

func usage(out *os.File) {
	fmt.Fprint(out, `platformgo — платформа для Go-проектов

  platformgo generate [--check]   сгенерировать подключение модулей и пример окружения
  platformgo plan                 показать, что изменит generate
  platformgo doctor               проверить окружение разработчика
  platformgo version              версия

Проект описывается в `+spec.FileName+`.
`)
}

func cmdGenerate(args []string, out *os.File) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	dir := fs.String("dir", ".", "каталог проекта")
	check := fs.Bool("check", false, "не писать файлы, а упасть при расхождении: для CI")
	if err := fs.Parse(args); err != nil {
		return err
	}

	files, err := wiring(*dir)
	if err != nil {
		return err
	}

	if *check {
		changed, err := gen.Changed(*dir, files)
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			return fmt.Errorf("генерация устарела, выполните platformgo generate: %s", strings.Join(changed, ", "))
		}
		fmt.Fprintln(out, "генерация актуальна")
		return nil
	}

	written, err := gen.Apply(*dir, files)
	if err != nil {
		return err
	}
	for _, path := range written {
		fmt.Fprintln(out, "записан", path)
	}
	return nil
}

func cmdPlan(args []string, out *os.File) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	dir := fs.String("dir", ".", "каталог проекта")
	if err := fs.Parse(args); err != nil {
		return err
	}

	f, err := spec.Load(filepath.Join(*dir, spec.FileName))
	if err != nil {
		return err
	}
	files, err := gen.Wiring(f)
	if err != nil {
		return err
	}
	changed, err := gen.Changed(*dir, files)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "сервис   %s\n", f.Service())
	names := make([]string, 0, len(f.EnabledModules()))
	for _, m := range f.EnabledModules() {
		names = append(names, m.Name)
	}
	if len(names) == 0 {
		names = append(names, "нет")
	}
	fmt.Fprintf(out, "модули   %s\n", strings.Join(names, ", "))

	if len(changed) == 0 {
		fmt.Fprintln(out, "изменений нет")
		return nil
	}
	fmt.Fprintln(out, "будут перезаписаны:")
	for _, path := range changed {
		fmt.Fprintln(out, "  ~", path)
	}
	return nil
}

func cmdDoctor(args []string, out *os.File) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Fprintf(out, "%-10s %s\n", "go", runtime.Version())
	fmt.Fprintf(out, "%-10s %s\n", "platformgo", version())

	var missing []string
	for _, bin := range []string{"git", "docker"} {
		if path, err := exec.LookPath(bin); err == nil {
			fmt.Fprintf(out, "%-10s %s\n", bin, path)
			continue
		}
		fmt.Fprintf(out, "%-10s не найден\n", bin)
		missing = append(missing, bin)
	}
	fmt.Fprintf(out, "%-10s %s\n", "модули", strings.Join(registry.Names(), ", "))

	if len(missing) > 0 {
		return fmt.Errorf("не найдены программы: %s", strings.Join(missing, ", "))
	}
	return nil
}

func wiring(dir string) (map[string][]byte, error) {
	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		return nil, err
	}
	return gen.Wiring(f)
}

func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	var revision string
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			revision = s.Value[:7]
		}
	}
	if revision == "" {
		return "dev"
	}
	return "dev+" + revision
}
