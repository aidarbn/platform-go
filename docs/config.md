# platformgo.yaml

`platformgo.yaml` в корне проекта — единственный источник правды о том, что нужно проекту: модули, их настройки, дополнительная генерация, правила линтера, CI. Проект меняется так: правишь файл → `platformgo plan` → `platformgo apply`. Команд `add` и `remove` нет.

## Файлы

| Файл | Кто пишет | Что внутри | В git |
|---|---|---|---|
| `platformgo.yaml` | человек | что нужно проекту | да |
| `platformgo.lock` | `platformgo` | что фактически применено: версии модулей и библиотек, хеши managed-файлов | да |

Как `go.mod` и `go.sum`: намерение отдельно, зафиксированный результат отдельно.

## Пример

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/aidarbn/platform-go/v0.2.0/schema/platformgo.schema.json
schema: 1
platform: v0.2.0

project:
  module: github.com/aidarbn/shop-api
  service: shop-api
  go: "1.27"

modules:
  postgres:
    migrations: db/migrations
    queries: sqlc

  river:
    queues:
      default: 5
      notify: 3
    periodic:
      - job: CleanupExpired
        cron: "0 3 * * *"
    ui: true

  api:
    grpc_addr: 127.0.0.1:9090
    rest_prefix: /api/v1
    proto: proto
    openapi:
      base: api/base.openapi.yaml
      out: api/shop.openapi.yaml
    interceptors: [logging, recovery, validate, ratelimit]

  s3:
    buckets: [media]

generate:
  extra:
    - go run ./tools/mygen

lint:
  depguard:
    - deny: github.com/riverqueue/river
      except: [internal/adapters/dbqueue/**]

ci:
  e2e: false
  security_scan: true
```

## Правила

- **Секретов в файле нет.** Только структура и настройки; значения — из переменных окружения. `.env.example` генерируется по файлу.
- **Схема JSON** публикуется для каждой версии платформы; ссылка в первой строке включает автодополнение и проверку в редакторе.
- **`schema`** — версия формата файла. `platformgo upgrade` переводит файл на новую схему миграциями, см. [upgrades.md](upgrades.md).
- **Модуль добавляется** появлением секции в `modules`, **убирается** её удалением. Owned-файлы модуля при удалении остаются — платформа предупреждает о них.
- **Заготовки кода** появляются из объявлений: например, задание из `river.periodic`, которого ещё нет, `apply` создаст owned-файлом.
- **Зависимости ставит платформа**: соседние модули (river требует postgres — `plan` об этом скажет), Go-модули и Go-инструменты с закреплёнными версиями, сервисы локальной инфраструктуры в `docker-compose.yml`. Системные программы (Docker) проверяет `platformgo doctor`.

## Команды

| Команда | Что делает |
|---|---|
| `platformgo new <имя>` | создаёт проект с `platformgo.yaml` и ядром |
| `platformgo plan` | показывает, чем проект отличается от `platformgo.yaml`; ничего не меняет |
| `platformgo apply` | приводит проект к `platformgo.yaml`: зависимости, managed-файлы, заготовки, генерация, `go build`, `go test`; обновляет `platformgo.lock` |
| `platformgo generate` | только генерация; `--check` для CI падает при расхождении |
| `platformgo verify` | для CI: проект соответствует файлу, managed-файлы не тронуты, генерация актуальна |
| `platformgo upgrade` | переводит проект на новую версию платформы |
| `platformgo doctor` | проверяет инструменты и системные зависимости; `--fix` предлагает установить |
| `platformgo setup` | автодополнение и псевдоним `pgo` |

## Пример плана

```
$ platformgo plan
+ модуль s3
    go get github.com/minio/minio-go/v7@v7.0.69
    docker-compose.yml: сервис minio
    config.gen.go: S3Config; .env.example: S3_*
~ модуль river
    queues.notify: новая очередь, 3 воркера
    periodic: CleanupExpired "0 3 * * *" — задания нет, будет создана заготовка
- модуль keycloak
    убрать из modules.gen.go, docker-compose.yml, правил линтера
    останутся owned-файлы: internal/adapters/out/keycloak/…
```
