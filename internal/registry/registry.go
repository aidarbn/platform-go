// Package registry — каталог модулей, которые умеет подключать platformgo.
//
// Реестр знает о каждом модуле ровно то, что нужно генератору: пакет, тип настроек,
// зависимости и переменные окружения. Поэтому добавление модуля в проект не требует
// правок кода: генератор собирает подключение из этого описания.
package registry

import (
	"fmt"
	"slices"
	"strings"
)

// EnvVar — переменная окружения модуля для .env.example.
type EnvVar struct {
	Key      string
	Example  string
	Comment  string
	Required bool
}

// Module — описание модуля платформы.
type Module struct {
	Name     string   // имя секции в platformgo.yaml
	Requires []string // модули, без которых не работает
	Import   string   // путь пакета модуля
	Package  string   // имя пакета в коде
	Field    string   // поле в структуре Config проекта
	Env      []EnvVar
}

// ConfigType — тип настроек модуля, например postgres.Config.
func (m Module) ConfigType() string { return m.Package + ".Config" }

// LoadCall — вызов чтения настроек из окружения.
func (m Module) LoadCall() string { return m.Package + ".Load(l)" }

// NewCall — создание модуля с настройками из структуры Config.
func (m Module) NewCall() string { return fmt.Sprintf("%s.New(cfg.%s)", m.Package, m.Field) }

var all = []Module{
	{
		Name:    "postgres",
		Import:  "github.com/aidarbn/platform-go/kit/modules/postgres",
		Package: "postgres",
		Field:   "Postgres",
		Env: []EnvVar{
			{Key: "DATABASE_URL", Example: "postgres://app:app@localhost:5432/app?sslmode=disable", Comment: "адрес базы", Required: true},
			{Key: "DATABASE_MAX_CONNS", Example: "10", Comment: "предел соединений в пуле"},
			{Key: "DATABASE_MIN_CONNS", Example: "0", Comment: "сколько соединений держать открытыми"},
		},
	},
}

// All возвращает все известные модули в алфавитном порядке.
func All() []Module {
	out := slices.Clone(all)
	slices.SortFunc(out, func(a, b Module) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Get возвращает модуль по имени.
func Get(name string) (Module, bool) {
	for _, m := range all {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// Names возвращает имена известных модулей.
func Names() []string {
	out := make([]string, 0, len(all))
	for _, m := range All() {
		out = append(out, m.Name)
	}
	return out
}
