# Factory W0: DUR1, ADR 0002, docs/durable, reference/durable, test infra — план

> **Для исполнителей:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Шаги — чекбоксы. Каждой задаче — сначала падающий тест, потом код. Исполнители субагентов не запускают.

**Цель:** выдать всем группам общую основу волны 0: скрипт тестовой инфраструктуры, эталон `reference/durable` с зелёными интеграционными тестами, контракт DUR1 `2.0.0-DUR.1`, каталог событий и документы `docs/durable/*`, ADR 0002.

**Архитектура:** четыре независимых направления (infra-скрипт; Go-модуль `reference/durable`; контракт DUR1 в `docs/api` + `tests/http`; документы `docs/durable` + ADR). Эталон копируется сервисами, не импортируется. Контракт строится по образцу REG1 (`tests/http/tools/compose-registration-contract.mjs`).

**Стек:** Go 1.26.9 (`GOWORK=off GOTOOLCHAIN=go1.26.9`), pgx/v5, amqp091-go, go-redis/v9, Node 24 (`node --test`), bash, jq.

**Спека:** подспека `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/factory.md` (разделы 1–3, «Волна 0» W0.1–W0.6, 6–8) и главная `00-orchestration.md` (разделы 2, 3, 6, 7, 8, 10 — решения C1–C53 главнее подспеки).

## Global Constraints

- Замороженные байты C2/UA1/FIN1/REG1 не меняются; DUR1 только дополняет REG1 composed (`info.version === '2.0.0-REG.1'`).
- Версия контракта `2.0.0-DUR.1`; в составном файле `x-durable-source` с SHA-256 входов.
- Тестовая инфраструктура: `umir-prod-pg` 127.0.0.1:55432 и `umir-prod-mq` 127.0.0.1:55672 / management 55673 уже работают — не перезапускать; новые `umir-durable-redis-limiter` 127.0.0.1:56379 и `umir-durable-redis-cache` 127.0.0.1:56380. Порты 5432/6379 и чужие контейнеры не трогать. `FLUSHALL`/`FLUSHDB` запрещены. Без переменных `UMIR_TEST_*` интеграционный набор падает, пропусков нет.
- Интеграционные тесты под тегом `integration`; `go test -tags integration -p 1 -race ./...`.
- Секреты не печатать и не коммитить; только синтетические.
- Числа сроков и бюджетов — «предложение до G1».
- Коммиты: Conventional Commits, английский, без футеров `Co-Authored-By`/generated-by; `git diff --check`; CHANGELOG/README/AGENTS правит только ведущий в финальной задаче.
- Исполнители не коммитят; ведущий коммитит по путям задачи.
- Не трогать файлы группы factory-w1: `scripts/completion/*`, `compose.completion*.yaml`, CI-задания completion, `tests/infra/completion/*`.
- Тексты документов — на русском (как остальные docs репозитория), идентификаторы и код — по-английски.

## Review Focus

- Утечка RLS-контекста через пул при откате и ошибке (`MaxConns=1`) — тест в T3.
- Публикация «подтверждена» без положительного confirm маршрутизированного сообщения — T4.
- Лимитер Redis «разрешает» при любой ошибке — T5 (`ErrAdmissionUnavailable`).
- Тест-инфраструктура удаляет чужие данные (чужие БД, vhost, ключи) — T1/T2: удаляется только `t_<repo>_<rand>`/`t:<repo>:<rand>:`.
- `durable-test-infra.sh env` печатает пароль или вызывает `run/rm/stop` над чужими контейнерами — T1.
- Составной контракт DUR1 незаметно меняет `paths` предшественника — T8.

## Задачи

### T1. `scripts/durable-test-infra.sh` (W0.6) — первой
Файлы: `scripts/durable-test-infra.sh`, `tests/infra/durable-test-infra.test.sh` (shim `docker`), `.gitignore` (`.cache/` уже игнорируется).
Готово: `check|up|env|down` по W0.6; shim-тест зелёный; затем реально `up` и `env` (Redis 56379/56380 созданы, Postgres/RabbitMQ только проверены).

### T2. Каркас `reference/durable` и `itest` (W0.5, часть)
Файлы: `reference/durable/{go.mod,go.sum,README.md(каркас),deps.go}`, `reference/durable/itest/*`, `.go-quality-modules`, `scripts/go-quality.sh` (`module_root`, `run_named_module`, циклы `check`, `QUALITY_MODULE=durable`), `scripts/check-go-readiness.sh`, `.go-arch-durable.yml`, `Makefile` (`check-durable`, `integration`).
Interfaces (Produces): пакет `itest`: `DatabaseURL(t) string`, `NewDatabase(t) string`, `AMQPURL(t) string`, `NewVHost(t) string`, `RedisLimiterURL(t)`, `RedisCacheURL(t)`, `RedisPrefix(t) string`, `TCPProxy` (`NewTCPProxy(t, target) *TCPProxy`, `Addr()`, `CutAfterBytes(n)`, `CutAfterMatch([]byte)`, `Pause()/Resume()`, `Close()`). Все зависимости модуля заранее в go.mod (файл `deps.go` с пустыми импортами), чтобы T3–T6 не трогали go.mod/go.sum.
Готово: `make check-durable` зелёный; `itest` с тестами; `go test -tags integration` на `itest` зелёный на реальной инфраструктуре.

### T3. Пакет `postgres` (W0.5)
Все публичные функции таблицы W0.5 `postgres`, SQL-фрагменты (`storage_meta`, `worker_leases`, `outbox`, `inbox`), интеграционные тесты списка `postgres`.

### T4. Пакет `rabbitmq` (W0.5)
`Connector`, `LoadURL`, `Declare`/`DeclareExclusive`/`CheckPassive`, `Publisher`, `Consume`; тесты списка `rabbitmq`. Топология — по `docs/durable/events.md` (T9a).

### T5. Пакет `redis` (W0.5)
`Open`, `LoadURL`, `Limiter.Admit` (Lua), `Continuity`, `Lease`, `Slots`; тесты списка `redis`.

### T6. Пакет `relay` (W0.5)
`Relay`, `Run`, `Stats`; политики `accept`/`retry`; сквозные тесты «транзакция → outbox → брокер → inbox» с точками падения.

### T7. `copy.sh`, README модуля, CI (W0.5)
`reference/durable/copy.sh` (`--check`), README модуля (пакеты, функции, тесты), задание CI `durable-reference` (сервисные контейнеры Postgres 17, RabbitMQ 4, 2 Redis; `make check-durable`, `make integration`).

### T8a. DUR1 overlays, скрипт, примеры, тесты (W0.1)
Файлы: `docs/api/durable-v1-{public,private}.json`, `durable-v1-examples.json`, `durable-v1-composed-*.json` (генерируются), `tests/http/tools/compose-durable-contract.mjs`, `tests/http/cmd/acceptance/durable_contract_test.go`, `docs/durable-api-amendment.md`, `Makefile` (`check-contract`). Четыре теста; п. 1–14 W0.1.

### T8b. `umir-checks contract-sync`
`tools/assembly-checks`: новая команда сверки SHA составного контракта с копиями Auth/Gateway/Frontend, тесты на temp-каталогах.

### T9a. `docs/durable/events.md` + `event-envelope.schema.json` + `task-message.schema.json` (W0.3) + таблица прав RabbitMQ
### T9b. `docs/durable/configuration.md` (имена, таблица команд C44, таблица здоровья C32/C42/C49, соответствие C34)
### T9c. `docs/durable/retention.md`, `docs/durable/admission.md` (W0.4)
### T10. `docs/adr/0002-postgres-rabbitmq-redis.md` (W0.2)

### T11. Финал
README/AGENTS/CHANGELOG (карта документов, три модуля → четыре, команды), полные проверки (`make check`, `make check-contract`, `make integration`, `bash tests/infra/*.sh`), финальное ревью (opus), один цикл правок, push, draft PR, отчёт.

## Самопроверка плана
- Покрытие W0.1 — T8a/T8b; W0.2 — T10; W0.3 — T9a, T4; W0.4 — T9c; W0.5 — T2–T7; W0.6 — T1.
- Тест воспроизводимости UA1 (W1.9) относится к группе factory-w1 и здесь не делается (`Ruling` в журнале).
- Плейсхолдеров нет: детальные требования берутся из указанных строк подспеки (она в исполнителе обязательна к прочтению).
