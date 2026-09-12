// Package gen генерирует файлы проекта по platformgo.yaml.
//
// Генерируются подключение модулей, их настройки и пример окружения. Именно это
// избавляет от ручной правки проекта при добавлении и удалении модулей: в коде
// проекта остаётся только предметная часть.
package gen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
)

// Пути генерируемых файлов относительно корня проекта.
const (
	ConfigPath  = "cmd/app/config.gen.go"
	ModulesPath = "cmd/app/modules.gen.go"
	EnvPath     = ".env.example"
)

const header = "// Код сгенерирован platformgo по " + spec.FileName + ". Не правьте вручную.\n"

var configTmpl = template.Must(template.New("config").Parse(header + `
package main

import (
{{- range .Imports}}
	"{{.}}"
{{- end}}
)

// Config — настройки платформенных модулей сервиса {{.Service}}.
type Config struct {
{{- range .Modules}}
	{{.Field}} {{.ConfigType}}
{{- end}}
}

// LoadConfig читает настройки модулей из переменных окружения и возвращает все
// ошибки сразу, чтобы не искать их по одной за запуск.
func LoadConfig() (*Config, error) {
	l := confx.New("")
	cfg := &Config{
{{- range .Modules}}
		{{.Field}}: {{.LoadCall}},
{{- end}}
	}
	if err := l.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}
`))

var modulesTmpl = template.Must(template.New("modules").Parse(header + `
package main

import (
{{- range .Imports}}
	"{{.}}"
{{- end}}
)

// platformModules возвращает модули проекта в порядке зависимостей.
func platformModules(cfg *Config) []platform.Module {
	return []platform.Module{
{{- range .Modules}}
		{{.NewCall}},
{{- end}}
	}
}
`))

type templateData struct {
	Service string
	Imports []string
	Modules []registry.Module
}

// Wiring возвращает содержимое генерируемых файлов: путь относительно корня проекта → содержимое.
func Wiring(f *spec.File) (map[string][]byte, error) {
	modules := f.EnabledModules()

	config, err := render(configTmpl, templateData{
		Service: f.Service(),
		Imports: imports("github.com/aidarbn/platform-go/kit/confx", modules),
		Modules: modules,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ConfigPath, err)
	}

	mods, err := render(modulesTmpl, templateData{
		Service: f.Service(),
		Imports: imports("github.com/aidarbn/platform-go/kit/platform", modules),
		Modules: modules,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ModulesPath, err)
	}

	return map[string][]byte{
		ConfigPath:  config,
		ModulesPath: mods,
		EnvPath:     env(f, modules),
	}, nil
}

func imports(base string, modules []registry.Module) []string {
	out := []string{base}
	for _, m := range modules {
		if !slices.Contains(out, m.Import) {
			out = append(out, m.Import)
		}
	}
	slices.Sort(out)
	return out
}

func render(t *template.Template, data templateData) ([]byte, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	// Форматируем сами: сгенерированный код должен проходить gofmt в CI проекта.
	out, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("форматирование: %w", err)
	}
	return out, nil
}

func env(f *spec.File, modules []registry.Module) []byte {
	var b strings.Builder
	b.WriteString("# Сгенерировано platformgo по " + spec.FileName + ". Не правьте вручную.\n")
	b.WriteString("# Сервис: " + f.Service() + "\n")

	for _, m := range modules {
		if len(m.Env) == 0 {
			continue
		}
		b.WriteString("\n# модуль " + m.Name + "\n")
		for _, v := range m.Env {
			if v.Comment != "" {
				b.WriteString("# " + v.Comment)
				if v.Required {
					b.WriteString(" (обязательно)")
				}
				b.WriteString("\n")
			}
			b.WriteString(v.Key + "=" + v.Example + "\n")
		}
	}
	return []byte(b.String())
}

// Changed возвращает пути файлов, содержимое которых в проекте отличается от
// сгенерированного. Пустой список означает, что генерация актуальна.
func Changed(dir string, files map[string][]byte) ([]string, error) {
	var changed []string
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			if os.IsNotExist(err) {
				changed = append(changed, path)
				continue
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !bytes.Equal(got, want) {
			changed = append(changed, path)
		}
	}
	slices.Sort(changed)
	return changed, nil
}

// Apply записывает сгенерированные файлы в проект.
func Apply(dir string, files map[string][]byte) ([]string, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)

	for _, path := range paths {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := os.WriteFile(full, files[path], 0o644); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return paths, nil
}
