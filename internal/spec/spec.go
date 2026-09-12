// Package spec читает и проверяет platformgo.yaml — описание проекта.
package spec

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aidarbn/platform-go/internal/registry"
)

// FileName — имя файла описания в корне проекта.
const FileName = "platformgo.yaml"

// SchemaVersion — версия формата, которую понимает эта версия platformgo.
const SchemaVersion = 1

// File — содержимое platformgo.yaml.
type File struct {
	Schema   int                       `yaml:"schema"`
	Platform string                    `yaml:"platform"`
	Project  Project                   `yaml:"project"`
	Modules  map[string]map[string]any `yaml:"modules"`
}

// Project — сведения о проекте.
type Project struct {
	Module  string `yaml:"module"`  // путь Go-модуля
	Service string `yaml:"service"` // имя сервиса в логах
	Go      string `yaml:"go"`      // версия Go
}

// Load читает файл описания и проверяет его.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return Parse(raw)
}

// Parse разбирает содержимое файла описания.
func Parse(raw []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // опечатка в имени поля — ошибка, а не молчаливое игнорирование
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: разбор: %w", FileName, err)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

func (f *File) validate() error {
	var errs []error

	switch {
	case f.Schema == 0:
		errs = append(errs, errors.New("не задано поле schema"))
	case f.Schema > SchemaVersion:
		errs = append(errs, fmt.Errorf("schema %d новее, чем понимает эта версия platformgo (%d) — обновите platformgo", f.Schema, SchemaVersion))
	}
	if f.Project.Module == "" {
		errs = append(errs, errors.New("не задано project.module"))
	}

	for _, name := range f.moduleNames() {
		m, ok := registry.Get(name)
		if !ok {
			errs = append(errs, fmt.Errorf("неизвестный модуль %q, известные: %s", name, strings.Join(registry.Names(), ", ")))
			continue
		}
		for _, dep := range m.Requires {
			if _, enabled := f.Modules[dep]; !enabled {
				errs = append(errs, fmt.Errorf("модуль %s требует модуль %s — добавьте его в modules", name, dep))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s: %w", FileName, errors.Join(errs...))
	}
	return nil
}

func (f *File) moduleNames() []string {
	out := make([]string, 0, len(f.Modules))
	for name := range f.Modules {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Service возвращает имя сервиса: из файла или последний элемент пути Go-модуля.
func (f *File) Service() string {
	if f.Project.Service != "" {
		return f.Project.Service
	}
	parts := strings.Split(f.Project.Module, "/")
	return parts[len(parts)-1]
}

// EnabledModules возвращает модули в порядке зависимостей: сначала те, от кого зависят.
// Порядок устойчивый, поэтому сгенерированные файлы не меняются между запусками.
func (f *File) EnabledModules() []registry.Module {
	var out []registry.Module
	added := make(map[string]bool, len(f.Modules))

	var add func(name string)
	add = func(name string) {
		if added[name] {
			return
		}
		m, ok := registry.Get(name)
		if !ok {
			return
		}
		added[name] = true
		for _, dep := range m.Requires {
			if _, enabled := f.Modules[dep]; enabled {
				add(dep)
			}
		}
		out = append(out, m)
	}

	for _, name := range f.moduleNames() {
		add(name)
	}
	return out
}
