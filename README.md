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

Файл `compose.dev.yaml` использует соседние каталоги как контексты сборки. Порты и сеть остаются такими же, как у опубликованной сборки.

## CI/CD и обновление образов

Сервисные workflows проверяют push в ветки, PR в main и ручные запуски. Go-сервисы проходят gofmt, go test с race detector и go vet; frontend проходит npm ci и сборку Vite. Все сервисы проверяют Docker-сборку.

После успешной проверки main публикуется образ `ghcr.io/justikra/umir-<service>:sha-<полный SHA коммита>` для `linux/amd64` и `linux/arm64`. Публикация использует штатный `GITHUB_TOKEN`; разрешение `packages:write` задано только заданию публикации. Это правило действует и для коммитов с изменением документации.

Новый образ не меняет выбранную сборку автоматически:

1. Найдите успешный запуск сервиса и полный SHA его коммита.
2. Измените тег соответствующего образа в `compose.yaml` в рабочей ветке фабрики.
3. Откройте PR с описанием изменения и ссылкой на CI сервиса.
4. Дождитесь успешного CI фабрики и merge от `JustIkra`.
5. Выполните `docker compose pull` и `docker compose up -d --wait --wait-timeout 120` на целевом хосте.

CI фабрики проверяет Compose и соответствие SHA тегов сервисам, скачивает выбранные образы, запускает четыре контейнера и проверяет frontend и оба маршрута gateway. Команды проверки:

```sh
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example -f compose.yaml -f compose.dev.yaml config --quiet
python3 scripts/check-images.py
python3 scripts/smoke.py
```

Smoke-проверка требует запущенной сборки со штатными портами 8080 и 8081.

Фабрика не публикует собственный контейнер. Автоматический деплой на сервер пока не настроен.

## Доступ и совместная работа

- Все пять репозиториев публичные; issue включены.
- Только `JustIkra` может обновлять main и объединять PR. Активный ruleset main имеет единственное исключение для его аккаунта.
- `Kxqusos` добавлен во все репозитории с ролью write: работа в остальных ветках и PR.
- Другие пользователи GitHub могут создать fork, рабочую ветку в нём и PR.
- `.github/CODEOWNERS` указывает `JustIkra` как владельца кода.

Перед PR запускайте проверки затронутого сервиса. Согласовывайте изменения межсервисных контрактов с обеими сторонами. Настоящие `.env`, токены и рабочие данные ресторана в Git не сохраняются.

