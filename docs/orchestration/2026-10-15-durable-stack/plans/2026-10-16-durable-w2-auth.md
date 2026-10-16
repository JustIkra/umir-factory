# Auth durable, волна 2 (хранение): план

> Для исполнителя: superpowers:executing-plans, задачи по порядку, чекбоксы.

**Цель:** Auth хранит состояние в PostgreSQL построчно, публикует события и письма через outbox и RabbitMQ, работает в двух репликах; Gateway не пир жизненного цикла.

**Архитектура:** порт `application.Store` (`InTx`/`View` с репозиториями по сущностям) заменяет `AggregateRepository` и `Accounts.mu`; тестовый адаптер `tests/memstore` и контрактный набор `tests/storecontract` общие для memory и Postgres; адаптеры `internal/adapters/{postgres,rabbitmq,relay}` копируются из `umir-factory reference/durable` (R15) без правок; бюджеты Auth в Postgres в транзакции действия (C51, Redis нет).

**Стек:** Go 1.26.9, pgx/v5, amqp091-go; Postgres 55432, RabbitMQ 55672 (тестовая инфраструктура factory).

**Спека:** `docs/superpowers/specs/2026-10-15-durable-stack/auth.md` (раздел «Волна 2»), главная спека разд. 6, 10, 11.

## Global Constraints
- `GOWORK=off GOTOOLCHAIN=go1.26.9`; `make check`, `make quality-test`, `make integration` (`-tags integration -p 1 -race`).
- Эталон только через `copy.sh`, правки эталона в сервисе запрещены (R15); нужна правка — CR.
- Redis-клиента у Auth нет (C51). Режима memory в боевом бинарнике нет (C10); тест сборки.
- Роли `umir_auth_owner` / `umir_auth`, `AssertRuntimeRole`, RLS (R8, C5, C8). Подкоманды `migrate`, `init`, `serve`, `generation rotate`, `keys check`, `healthcheck` (C9, C63).
- События `umir.auth` по C1/C45 (все ключи accept), письма `umir.tasks` ключ `auth.mail` (retry). Топологию объявляет только `mq-init`.
- Сроки: `retention.md` (G1-8). Файлы секретов uid 65532, 0600 (C52). `/healthz`, `/readyz` без ключа (C32).
- R3 (Gateway из пиров) — отдельная последняя задача (T11), PR после Gateway PR-1 (C7).
- Чистый лист: без импорта (R1). Коммиты Conventional Commits, без футеров, CHANGELOG «Unreleased».

## Review Focus
- Гонка двух реплик на одной команде: один исход (storecontract + integration).
- Потерянный ответ на COMMIT: повтор по commandID возвращает прежний результат.
- Утечка RLS-контекста через пул (тест с ролью `umir_auth`, MaxConns=1).
- Очистка не удаляет нужное повтору/бюджету/proof; израсходованный proof не оживает.
- Холостые тики воркеров и `authorize` не делают Seq Scan на 100 тыс. строк.

## Задачи
- [ ] T1. Основа: копия эталона (postgres, rabbitmq, relay), зависимости, arch-lint, `make integration`, хелпер `tests/itest`.
- [ ] T2. Порт `Store`, `tests/memstore`, переписанный `internal/application`, тесты на memstore; временная персистентность снимком (удаляется в T5).
- [ ] T3. Схема, миграции, роли, RLS; `adapters/storepg` (реализация `Store`), `tests/storecontract` на memory и Postgres.
- [ ] T4. Секреты `owner.json` v2, HKDF-подключи, `keys check`; конфигурация JSON; подкоманды `migrate`, `init`, `serve`, `generation rotate`, `healthcheck`.
- [ ] T5. Bootstrap на Postgres, удаление jsonstore/legacy/import/stateFile; тест сборки.
- [ ] T6. Бюджеты в Postgres (код в T2/T3, интеграционные тесты двух реплик).
- [ ] T7. Outbox, события `umir.auth`, письма `auth.mail`, relay, потребитель, подметание.
- [ ] T8. Реестр по ревизии (ETag/304, limit/after, since, точечное), квота, читатель Feedback, токены C11, сверка под арендой.
- [ ] T9. Очистка по срокам (`auth.cleanup`).
- [ ] T10. `/healthz`, `/readyz` по возможностям, надзор воркеров.
- [ ] T11. R3: Gateway из пиров (отдельный коммит-группа).
- [ ] T12. README, CHANGELOG, CI job integration, контейнерный smoke, фикстуры на memstore, самопроверка.
