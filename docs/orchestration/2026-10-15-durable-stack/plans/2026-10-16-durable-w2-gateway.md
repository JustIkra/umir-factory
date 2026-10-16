# Gateway, волна 2: состояние, брокер, Redis — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox syntax.

**Goal:** перевести Gateway на Redis-допуск, live через RabbitMQ, реестр Auth по ревизии и планировщик под арендой Redis; удалить локальные лимитеры и локальный `Append`.

**Architecture:** хранилища нет; эталоны R15 (`redis`, `rabbitmq`) копируются через `copy.sh` в `internal/adapters/{redislimiter,rabbitmq}` без правок. Порты лежат у потребителей (`admission`, `live`, `journal`, `expiration`, `registry`), склейка с эталоном — в `internal/adapters/{redisadmission,amqplive}`. Тестовые memory-реализации живут в `tests/testsupport`.

**Tech Stack:** Go 1.26.9, go-redis v9.23.0, amqp091-go v1.15.0, реальные RabbitMQ и Redis limiter для `make integration`.

**Spec:** `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/gateway.md` (раздел 5), главная спека `00-orchestration.md` (разделы 6, 10 C1–C63, 11 G1).

## Global Constraints

- Каждая команда с `GOWORK=off GOTOOLCHAIN=go1.26.9`; готово при зелёных `make check`, `make quality-test`, `make integration`.
- Эталон не правится (R15); расхождения — CR.
- Нет запасного пути «Redis недоступен → память» и «брокер недоступен → локальный Append» (R20).
- Ключи Redis только `umir:gw:*`, плюс `umir:limiter:generation` и `umir:limiter:strict_since` (C61).
- Старый режим (без `AUTH_URL`) не трогается (C28).
- Публичные пути, тела, статусы, CSRF/Origin не меняются; 429 `RateLimited` с `Retry-After` и `details.retryAfterSeconds`, 503 `ServiceUnavailable` до отправки владельцу (C16).
- Conventional Commits, без футеров; CHANGELOG «Unreleased»; `git diff --check`.

## Review Focus

- Redis отвечает мусором или таймаутом после списания: повтор с тем же отпечатком не списывает дважды.
- Маркер limiter пропал или не совпал: строгий режим, вход остаётся открытым.
- Поток теряет аренду: закрывается не позже конца аренды, чужой держатель не освобождает слот.
- Брокер перезапущен: `ResetAll`, `refresh-required`, новые потоки получают 503 пока потребитель не готов.
- Событие дублируется, опаздывает, с разрывом ревизии, чужая организация: кольцо не растёт, чужих данных нет.

## Задачи

### T1 Фундамент
- [x] Копии эталона `redis:redislimiter`, `rabbitmq` (`copy.sh`), зависимости, `.go-arch-lint.yml`.
- [x] `tests/testsupport/itest` (vhost, префикс Redis, TCP-прокси), `make integration`, job CI.

### T2 Конфигурация (5.4)
`GATEWAY_AMQP_URL_FILE`, `GATEWAY_REDIS_LIMITER_URL_FILE` обязательны в режиме Auth; `GATEWAY_ADMISSION_KEY_FILE`, `GATEWAY_LIVE_CURSOR_KEY_FILE`, `GATEWAY_LIMITER_GENERATION`, `GATEWAY_REDIS_TIMEOUT`, `GATEWAY_BUDGET_*`, `GATEWAY_LIVE_MAX_SCOPES`, `GATEWAY_LIVE_RING_IDLE`, `GATEWAY_LIVE_TOPICS`, `GATEWAY_REGISTRY_SYNC_INTERVAL`, `GATEWAY_EXPIRATION_BATCH`; `AUTH_RUNTIME_SERVICE_KEY` и `PRIVATE_*` необязательны. Тесты `internal/config`, тест сборки «без URL не стартует».

### T3 Ядро допуска (5.3, G1-3, R7, C47, C61)
`internal/admission`: порт `Limiter`, правила и бюджеты по таблице admission.md, HMAC субъектов, строгий режим; `internal/adapters/redisadmission` (склейка); `tests/testsupport/memredis`; контрактные наборы `internal/admission/contract`; интеграционные тесты на Redis.

### T4 Допуск на маршрутах (одним коммитом удаляется C31)
csrf, вход, регистрация, сброс (operatorhttp), анонимная обратная связь, prepare/confirm (httpapi). Удаляются `registration_limit.go`, `admission/temporary_memory.go`. Тест «в `internal/` нет процессных лимитеров допуска».

### T5 Аренды SSE (G1-4, 5.3)
`live.Leases` на `redislimiter.Slots`, продление `TTL/3`, закрытие при потере, локальный потолок.

### T6 Журнал (5.1, 5.2)
`Apply(Event)`, `ResetAll`, `GATEWAY_LIVE_MAX_SCOPES`, вытеснение простаивающих, удаление кеша курсоров, ключ курсора отдельный.

### T7 Потребитель RabbitMQ (5.2)
`internal/adapters/amqplive`: очереди `gateway.live.<i>`, `gateway.auth.<i>`, пассивная проверка обменников, `ResetAll` при переподключении, уведомления `umir.auth`; удаление `publishChange` и `Append` планировщика.

### T8 Реестр по ревизии (5.5, C13)
`internal/registry` (проекция, надгробия, синхронизация `limit/after`, `since`, ETag, точечное чтение); замена `registry_cache.go` и refresher; приватный listener необязателен.

### T9 Планировщик (5.5, R6)
Аренда `expiry` с fencing, `expiry-due`, проекция, убрать `GatewayEpoch` и `unknown`.

### T10 Здоровье и документация (5.7)
`/readyz`: `http, auth, events, limiter, scheduler`; Dockerfile, compose, README, CHANGELOG.

### T11 Сценарии отказов (7)
Интеграционные тесты: Redis (таймаут, NOSCRIPT, обрыв, пустое состояние), две реплики, брокер.

### T12 Самопроверка, отчёт.

### T13 PR-2 (ветка `feat/durable-w2-gateway-pr2`, после Auth PR)
Удаление `runtime|registry`, JWKS-проверки, `PRIVATE_*`, `AUTH_RUNTIME_SERVICE_KEY` и тестов G5; без `sync-contract`.
