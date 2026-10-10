# UMIR Factory

Сборка системы учёта продуктов для одного ресторана. Фабрика закрепляет версии четырёх сервисных образов и запускает их через Docker Compose.

## Репозитории и границы

| Репозиторий | Ответственность |
| --- | --- |
| [umir-factory](https://github.com/JustIkra/umir-factory) | Compose, версии образов, запуск и проверка сборки. |
| [umir-gateway](https://github.com/JustIkra/umir-gateway) | Единый API-вход; маршрутизация к serving и warehouse. |
| [umir-serving](https://github.com/JustIkra/umir-serving) | OrdersService, MenuService (включая стоп-лист) и RecommendationsService. |
| [umir-warehouse](https://github.com/JustIkra/umir-warehouse) | Единый владелец склада: каталог, поставки, оценки, оформление, просрочка и запросы. |
| [umir-frontend](https://github.com/JustIkra/umir-frontend) | React и раздача интерфейса; запросы API через gateway. |

Бизнес-ядро в памяти реализует поставки и партии, приготовление по FEFO, подтверждение сокращённого состава заказа, списание просрочки, стоп-лист и рекомендации по продаже. Warehouse владеет остатками и атомарно фиксирует окончательную продажу вместе с расходом ингредиентов. Serving хранит предложения заказа и координирует подтверждение через склад.

Продукты, блюда и рецептуры хранятся в warehouse вместе с событиями. MenuService в serving работает с ними через порт склада. Мониторинг показывает остатки и сроки партий; отчёт для закупок находится вне текущей версии.

Перед новым оформлением и запросами актуальных остатков warehouse восстанавливает очередь и применяет записанные события наступивших списаний. При невозможности подготовки возвращается техническая ошибка InventoryUnavailable. Оформление подтверждается только записанным OrderPlaced; неизвестный исход записи разрешается повтором исходной попытки.

Остатки и индекс истечения публикуются вместе после полной команды; полная сверка нужна при восстановлении или известном сбое. Чистые расчёты используют захваченное неизменяемое состояние. GetMenu показывает только доступные блюда без количества порций. Снапшоты сохраняются в фоне после полных команд; точка восстановления меняется только после успешного сохранения.

Решение рассчитано на один ресторан. Время — `Europe/Moscow`. Регистрация пользователей, роли приложения и несколько ресторанов находятся вне базовой версии.

Основной документ бизнес-контрактов — [docs/business-core.md](docs/business-core.md). При расхождении описаний контрактов в README и docs приоритет имеет docs. Согласованные правила сохранены в документе; отдельно обозначенные предложения остаются на рассмотрении.

[Проект observability](docs/observability.md) описывает JSON-логи операций, связь запросов между сервисами, метрики зависаний и трассировку. Он подготовлен для рассмотрения; инструментация и стек мониторинга ещё не подключены.

## Текущее состояние

Warehouse и serving предоставляют независимые Go-модули с публичными contract/bootstrap и сценариями в памяти. Совместные тесты вызывают оба реальных Runtime через bridge фабрики, который сохраняет метаданные `GetDish` и копирует вложенный рецепт. Warehouse cmd/server собирает пустой Runtime, таймер истечения и snapshot worker; сбой Save повторяется с заданным интервалом, пока HTTP и обработка просрочки продолжаются. Serving cmd/server сохраняет healthz до подключения транспортного адаптера. Бизнес-HTTP, постоянные хранилища и экраны относятся к следующему этапу. Выбранные в compose.yaml опубликованные образы не меняются вместе с исходным кодом рабочих веток.

## Бизнес-ядро и разработка

Начните с [business-core](docs/business-core.md), затем прочитайте [архитектуру Go](docs/go-architecture.md) и README изменяемого сервиса. Эти документы задают поведение и границы исходного ядра; сетевую интеграцию контейнеров нужно проверить отдельно.

Отдельный разработчик может параллельно выполнять [задачу gateway/frontend](docs/gateway-frontend-handoff.md) на сценарных HTTP-моках. Эта спецификация определяет интерфейсы gateway, wire contract и границу подключения реальных сервисов. [Задание на Figma](docs/frontend-design-brief.md) описывает дизайн и evidence; [проектный review-skill](skills/umir-pr-review/SKILL.md) хранит особенности будущих PR. Порядок начала работы и список задач находятся в связанных GitHub issues. Моки не подменяют приёмку реального бизнес-ядра или сетевого восстановления.

| Репозиторий | Работа первого этапа | Свидетельство результата |
| --- | --- | --- |
| warehouse | Значения, события, FEFO, единый допуск, in-memory EventStore/SnapshotStore, полные снапшоты | Собственные контрактные и приёмочные Go-тесты. |
| serving | Потребительские порты, предложения, подтверждение, меню, стоп-лист, рекомендации, in-memory ProposalStore | Собственные Go-тесты сценариев и переходов предложения. |
| factory | Документация границ и совместные проверки двух Go-модулей в памяти | Реальные сценарии через test bridge; отдельно сохраняются проверки Compose. |
| gateway | Существующий proxy и healthz; подготовка к будущей схеме API | Текущие тесты маршрутизации без переноса бизнес-логики в gateway. |
| frontend | Существующая точка входа; ожидание схемы API для экранов | Текущая сборка Vite; браузерные сценарии появятся вместе с экранами. |

Совместная проверочная сборка находится в `tests/business`; её bridge связывает публичные contract/bootstrap двух сервисов и явно переводит значения. Сервисные модули независимы; приложение serving не импортирует warehouse. Автономный модуль `tools/assembly-checks` предоставляет команды `business`, `check-images` и `smoke` на Go:

```sh
GOWORK=off go -C tools/assembly-checks build -o /tmp/umir-checks
/tmp/umir-checks business --factory-dir "$PWD" --local
/tmp/umir-checks business --factory-dir "$PWD" --warehouse-ref <full-commit-sha> --serving-ref <full-commit-sha>
```

Каждая команда требует существующий абсолютный `--factory-dir`. Локальный режим использует соседние checkout `umir-warehouse` и `umir-serving`; режим фиксированных источников клонирует полные SHA из публичных репозиториев и проверяет `HEAD`. Runner создаёт временный `go.work` и удаляет только свои временные файлы. Он не меняет сервисные `go.mod` и не выполняет `go work sync`. Совместная бизнес-проверка работает в памяти и не требует контейнеров; она не проверяет сетевое восстановление pending. CI собирает бинарник и вызывает его для сохранения точного кода дочернего процесса. Ручной запуск бизнес-suite выключен по умолчанию: включайте его, когда выбранные warehouse и serving SHA содержат публичные `contract` и `bootstrap`.

Перед PR сверяйте изменения с business-core, запускайте проверки владельца и обновляйте его README. Фикстуры хранятся в тестах; настройки и worker собираются в точке запуска. Смена закреплённых образов остаётся отдельным изменением сборки.

Полный Go gate фабрики проверяет оба вложенных модуля из `.go-quality-modules`:

```sh
make tools
make check-tools
make check-business
make check
```

`check-tools` проверяет автономный runner с GOWORK=off. `check-business` создаёт временный workspace с соседними сервисами; другие пути задаются через WAREHOUSE_DIR/SERVING_DIR. Gates включают форматирование, сборку, закреплённые linters, архитектуру и полные race-тесты. Для test-only business модуля build компилирует тестовый бинарник в временный каталог. Версии и правила находятся в `.go-quality-versions`, `.golangci.yml` и `.go-arch-*.yml`. Необъявленный или отсутствующий вложенный модуль вызывает ошибку. Для форматирования отдельных файлов выберите QUALITY_MODULE=tools или business и передайте FILES как список путей с разделением новой строкой.

## Gateway OpenAPI

[OpenAPI v1](docs/api/openapi.yml) defines the target gateway contract for HTTP mocks and clients. It includes 15 business operations, three health routes, typed results, errors and JSON examples. All int64 business values use bounded decimal strings; timestamps include the Moscow `+03:00` offset. Wire DTOs use `ingredients` and `acceptedPositions` across services. Business HTTP adapters are not implemented yet. The resolve operation requires authoritative resolution, prevention of late writes and pending recovery after restart before production integration. The document revision is `1.0.0-draft.1`; it does not change service releases or selected images.

Contract sources are the local [gateway handoff](docs/gateway-frontend-handoff.md) and [business rules](docs/business-core.md), updated with the reviewed delivery form fields and timestamp semantics.

## Запуск опубликованной сборки

Требуются Git, Docker Engine и Docker Compose v2. Образы публичные; для скачивания из GHCR вход не требуется.

```sh
git clone https://github.com/JustIkra/umir-factory.git
cd umir-factory
cp .env.example .env
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose ps
```

| Адрес по умолчанию | Назначение |
| --- | --- |
| `http://127.0.0.1:8080/` | Точка входа frontend. |
| `http://127.0.0.1:8080/healthz` | Проверка frontend-контейнера. |
| `http://127.0.0.1:8081/healthz` | Проверка gateway. |
| `/api/serving/healthz` через порт 8080 или 8081 | Запрос через gateway к serving. |
| `/api/warehouse/healthz` через порт 8080 или 8081 | Запрос через gateway к warehouse. |

Healthz проверяет работоспособность процессов. Serving и warehouse не публикуют порты хоста в этой сборке.

Compose наследует `HEALTHCHECK` из Dockerfile каждого сервиса. Go-образы выполняют `/server healthcheck`, frontend выполняет `wget` к своему `/healthz`. Проверки запускаются каждые 10 секунд с timeout 3 секунды и start period 5 секунд; три последовательные ошибки дают статус unhealthy. Эти проверки нужны для `depends_on: service_healthy` и `up --wait`; новый образ должен сохранить рабочий HEALTHCHECK. CI проверяет его наличие в скачанном образе до запуска контейнеров.

Настройки запуска задаются в `.env`:

| Переменная | Значение по умолчанию |
| --- | --- |
| `UMIR_BIND` | `127.0.0.1` |
| `UMIR_WEB_PORT` | `8080` |
| `UMIR_API_PORT` | `8081` |

## Локальная сборка исходников

Из каталога, который содержит `umir-factory`, клонируйте остальные репозитории:

```sh
git clone https://github.com/JustIkra/umir-gateway.git
git clone https://github.com/JustIkra/umir-serving.git
git clone https://github.com/JustIkra/umir-warehouse.git
git clone https://github.com/JustIkra/umir-frontend.git
cd umir-factory
docker compose -f compose.yaml -f compose.dev.yaml up -d --build --wait --wait-timeout 120
```

Итоговая структура:

```text
workspace/
├── umir-factory/
├── umir-gateway/
├── umir-serving/
├── umir-warehouse/
└── umir-frontend/
```

Файл `compose.dev.yaml` использует соседние каталоги как контексты сборки, отдельные локальные теги `umir-<service>:dev` и pull_policy=build. Он не перезаписывает выбранные SHA-теги. Порты и сеть остаются такими же, как у опубликованной сборки.

## CI/CD и обновление образов

Сервисные workflows проверяют push в ветки, PR в main и ручные запуски. Warehouse и serving проходят закреплённый make check с форматированием, build, lint, архитектурой и race-тестами; frontend проходит npm ci и сборку Vite. Сервисы проверяют Docker-сборку.

После успешной проверки main публикуется образ `ghcr.io/justikra/umir-<service>:sha-<полный SHA коммита>` для `linux/amd64` и `linux/arm64`. Публикация использует штатный `GITHUB_TOKEN`; разрешение `packages:write` задано только заданию публикации. Это правило действует и для коммитов с изменением документации.

Новый образ не меняет выбранную сборку автоматически:

1. Найдите успешный запуск сервиса и полный SHA его коммита.
2. Измените тег соответствующего образа в `compose.yaml` в рабочей ветке фабрики.
3. Откройте PR с описанием изменения и ссылкой на CI сервиса.
4. Дождитесь успешного CI фабрики и merge от `JustIkra`.
5. Выполните `docker compose pull` и `docker compose up -d --wait --wait-timeout 120` на целевом хосте.

CI фабрики устанавливает Go 1.26.5, проверяет runner, собирает umir-checks, проверяет оба Compose и SHA-теги. До pull он проверяет существование выбранных commit и успешные Check/Publish image на main. После pull он проверяет OCI source/revision и HEALTHCHECK. Затем запускает четыре контейнера и проверяет frontend и маршруты gateway через опубликованные порты. При сбое показывает логи. Совместный business job включается вручную после публикации выбранных реализаций; два SHA извлекаются из Compose через Go. Проверки сборки выполняются командами:

```sh
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example -f compose.yaml -f compose.dev.yaml config --quiet
make check-tools
bash tests/infra/image-checks.sh
GOWORK=off go -C tools/assembly-checks run . check-images --factory-dir "$PWD"
GOWORK=off go -C tools/assembly-checks run . smoke --factory-dir "$PWD" --env-file .env.example
```

`check-images` читает JSON от `docker compose config` с .env.example. `smoke` требует запущенную сборку; он определяет адреса через Compose port frontend/gateway 8080. --env-file задаёт файл относительно factory-dir; по умолчанию Compose читает .env. Для других адресов используйте --web-url и --api-url. Оба явных URL позволяют обойти Docker discovery. Для CI используйте собранный бинарник, чтобы сохранить точный код возврата проверки.

Фабрика не публикует собственный контейнер. Автоматический деплой на сервер пока не настроен.

## Совместимость и версии

Публичная поверхность фабрики — Compose-настройки, точные identities выбранных образов и CLI umir-checks (аргументы, проверки и код выхода). Business suite использует только публичные пакеты сервисов. Данные Runtime существуют в памяти процесса; такая сборка не проверяет HTTP, позднюю запись по сети или восстановление pending после перезапуска.

Используется SemVer. До 1.0 исправления получают patch, добавления или несовместимые изменения — minor с описанием миграции. После 1.0 несовместимые изменения требуют major. До выпуска изменения остаются в CHANGELOG под «Не выпущено». Корневые теги имеют вид vX.Y.Z; независимо публикуемые вложенные модули используют префиксы tools/assembly-checks/vX.Y.Z и tests/business/vX.Y.Z. При v2+ import path содержит /v2; завершающий каталог major не дублируется в префиксе тега. Выпущенные теги неизменны. SemVer не заменяет точные SHA образов фабрики.

## Доступ и совместная работа

- Все пять репозиториев публичные; issue включены.
- Только `JustIkra` может обновлять main и объединять PR. Активный ruleset main имеет единственное исключение для его аккаунта.
- `Kxqusos` добавлен во все репозитории с ролью write: работа в остальных ветках и PR.
- Другие пользователи GitHub могут создать fork, рабочую ветку в нём и PR.
- `.github/CODEOWNERS` указывает `JustIkra` как владельца кода.

Перед PR запускайте проверки затронутого сервиса. Согласовывайте изменения межсервисных контрактов с обеими сторонами. Настоящие `.env`, токены и рабочие данные ресторана в Git не сохраняются.

## Development standard and releases

Repository instructions are in [AGENTS.md](AGENTS.md). Public compatibility covers documented requests, results, error states and operational interfaces. Warehouse and serving publish their Go contract surfaces; internal refactoring does not automatically change those contracts.

The full [development standard](docs/development-standard.md) describes Go, Git and versioning. The [module inventory](.go-quality-modules) declares both real nested Go modules. `make go-readiness` checks that inventory against the repository; it does not replace their build, lint, architecture or race-test gates. `make check` runs those gates for both modules. The effective lint and architecture configurations are `.golangci.yml` and `.go-arch-*.yml`; `.quality` retains the reviewed module-specific policy proposals for future consolidation.

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Stable incompatible changes increment major, compatible additions minor and fixes patch. Our pre-1.0 convention uses patch for compatible fixes and minor for additions or breaking changes. Mark breaking behavior and migration explicitly.

Release repositories independently. Use annotated `vX.Y.Z` tags for actual releases; published nested Go modules prefix tags with the logical module subdirectory (excluding a trailing major-version directory), and Go v2+ needs the matching major import path. Published tags/versions stay immutable. Record changes under Unreleased before releasing. Preserve Umir `sha-<full-SHA>` image identities and exact factory component pins; SemVer does not replace them with `latest`. API versions and service releases are separate.

Preserve unrelated local edits, stage reviewed paths and check `git diff --cached` and `git diff --check`. Tagging, publishing and shared-history rewrites require the corresponding user instruction. This standard does not create a release or change the current version.
