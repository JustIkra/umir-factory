# UMIR Factory

Сборка системы учёта продуктов для одного ресторана. Фабрика закрепляет версии четырёх сервисных образов и запускает их через Docker Compose.

## Репозитории и границы

| Репозиторий | Ответственность |
| --- | --- |
| [umir-factory](https://github.com/JustIkra/umir-factory) | Compose, версии образов, запуск и проверка сборки. |
| [umir-gateway](https://github.com/JustIkra/umir-gateway) | Единый API-вход; маршрутизация к serving и warehouse. |
| [umir-serving](https://github.com/JustIkra/umir-serving) | MenuService, OrdersService, StopListService и RecommendationsService. |
| [umir-warehouse](https://github.com/JustIkra/umir-warehouse) | DeliveryService, InventoryService, ExpirationService и MonitoringService. |
| [umir-frontend](https://github.com/JustIkra/umir-frontend) | React и раздача интерфейса; запросы API через gateway. |

Планируемая бизнес-логика: поставки и партии, приготовление по FEFO, подтверждение сокращённого состава заказа, списание просрочки, стоп-лист и рекомендации по продаже. Warehouse владеет остатками и атомарно фиксирует окончательную продажу вместе с расходом ингредиентов. Serving хранит предложения заказа и координирует подтверждение через склад.

Решение рассчитано на один ресторан. Время — `Europe/Moscow`. Регистрация пользователей, роли приложения и несколько ресторанов находятся вне базовой версии.

## Текущее состояние

Работают каркасы сервисов, healthz, gateway, сборка и публикация контейнеров, запуск всей сборки в CI. React имеет точку входа и пока отображает пустой корневой элемент. Бизнес-команды и экраны ещё предстоит реализовать.

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

Файл `compose.dev.yaml` использует соседние каталоги как контексты сборки. Локальные образы получают отдельные теги `umir-<service>:dev`; `pull_policy: build` выбирает сборку из исходников. Порты и сеть остаются такими же, как у опубликованной сборки.

Чтобы переключиться на опубликованную сборку, выполните `docker compose up -d --wait --wait-timeout 120` без dev-файла. Он выберет закреплённые GHCR-образы. Если вы собирали исходники прежней версией dev-файла, сначала выполните `docker compose pull`: старый dev-файл мог перезаписать локальные SHA-теги.

## CI/CD и обновление образов

Сервисные workflows проверяют push в ветки, PR в main и ручные запуски. Go-сервисы проходят gofmt, go test с race detector и go vet; frontend проходит npm ci и сборку Vite. Все сервисы проверяют Docker-сборку.

После успешной проверки main публикуется образ `ghcr.io/justikra/umir-<service>:sha-<полный SHA коммита>` для `linux/amd64` и `linux/arm64`. Публикация использует штатный `GITHUB_TOKEN`; разрешение `packages:write` задано только заданию публикации. Это правило действует и для коммитов с изменением документации.

Новый образ не меняет выбранную сборку автоматически:

1. Найдите успешный запуск сервиса и полный SHA его коммита.
2. Измените тег соответствующего образа в `compose.yaml` в рабочей ветке фабрики.
3. Откройте PR с описанием изменения и ссылкой на CI сервиса.
4. Дождитесь успешного CI фабрики и merge от `JustIkra`.
5. Выполните `docker compose pull` и `docker compose up -d --wait --wait-timeout 120` на целевом хосте.

CI фабрики выполняет следующие проверки:

1. Проверяет обе конфигурации Compose. `scripts/check-images.py` проверяет только набор сервисов, имя образа каждого сервиса и формат полного SHA в теге.
2. Запускает тесты проверок образов. `scripts/check-image-sources.sh` через GitHub API проверяет существование каждого коммита и успешный запуск `.github/workflows/ci.yml` в main для этого SHA. В том же запуске задания `Check` и `Publish image` должны завершиться успешно. Одного успешного PR или пропущенной публикации недостаточно.
3. Скачивает закреплённые образы. `scripts/check-pulled-images.sh` проверяет совпадение OCI source/revision с репозиторием и SHA тега, а также наличие включённого HEALTHCHECK.
4. Запускает все четыре контейнера через `up --wait`. `scripts/smoke.py` проверяет frontend и оба маршрута gateway через frontend и напрямую через API-порт.

Pull подтверждает доступность образа, smoke — его работу в сборке. Наличие коммита и результаты CI проверяются отдельным обращением к GitHub API. Для проверки источников локально нужны Bash, jq и GitHub CLI (`gh` с действующей авторизацией). Проверка скачанных образов требует Bash, jq и Docker. Эти инструменты уже доступны на runner фабрики; workflow передаёт штатный GitHub token только шагу проверки источников.

Замена `scripts/check-images.py` и `scripts/smoke.py` на Go запланирована вместе с runner проверок фабрики. До переноса эти Python-скрипты не запускать при выполнении плана бизнес-ядра. Доступные проверки без Python из каталога фабрики:

```sh
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example -f compose.yaml -f compose.dev.yaml config --quiet
bash tests/infra/image-checks.sh
bash scripts/check-image-sources.sh --env-file .env.example
docker compose --env-file .env.example pull
bash scripts/check-pulled-images.sh --env-file .env.example
```

После реализации tools/assembly-checks проверки образов и smoke будут запускаться из каталога фабрики так:

```sh
GOWORK=off go -C tools/assembly-checks run . check-images --factory-dir "$PWD"
GOWORK=off go -C tools/assembly-checks run . smoke --factory-dir "$PWD"
```

Smoke требует запущенной сборки. Текущая проверка получает адреса frontend и gateway командой `docker compose port <service> 8080`, поэтому использует фактические опубликованные порты и адрес привязки. По умолчанию Compose читает `.env`; `--env-file <path>` выбирает другой файл. Для wildcard-привязки `0.0.0.0` или `::` проверка обращается к соответствующему loopback-адресу. `--web-url` и `--api-url` позволяют явно задать адреса, например для удалённой сборки. Запускайте smoke из каталога фабрики с тем же `COMPOSE_PROJECT_NAME`, если запуск использует отдельное имя проекта. CI передаёт `--env-file .env.example` при запуске и проверке.

Go-версия должна сохранить это поведение и параметры. При переключении CI обе проверки будут вызывать собранный Go-бинарник; прежние Python-скрипты будут удалены. Проверки источников и метаданных образов сохраняются отдельными шагами CI.

Фабрика не публикует собственный контейнер. Автоматический деплой на сервер пока не настроен.

## Доступ и совместная работа

- Все пять репозиториев публичные; issue включены.
- Только `JustIkra` может обновлять main и объединять PR. Активный ruleset main имеет единственное исключение для его аккаунта.
- `Kxqusos` добавлен во все репозитории с ролью write: работа в остальных ветках и PR.
- Другие пользователи GitHub могут создать fork, рабочую ветку в нём и PR.
- `.github/CODEOWNERS` указывает `JustIkra` как владельца кода.

Перед PR запускайте проверки затронутого сервиса. Согласовывайте изменения межсервисных контрактов с обеими сторонами. Настоящие `.env`, токены и рабочие данные ресторана в Git не сохраняются.

