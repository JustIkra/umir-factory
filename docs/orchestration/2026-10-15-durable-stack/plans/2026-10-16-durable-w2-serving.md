# Serving волна 2 (хранение в PostgreSQL) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Шаги — чекбоксы.

**Goal:** предложения, попытки и регистрации Serving живут в PostgreSQL, две реплики работают за одним адресом, реестр Auth не читается на запрос.

**Architecture:** Store за существующими портами (`pgstore`), lease-строка вместо процессного мьютекса, `RegistrationStore` в bootstrap, проекция реестра (точечное чтение Auth + события `umir.auth`), доводчик FIN-04, очистка по срокам. Память остаётся для тестов и фикстур.

**Tech Stack:** Go 1.26.9, pgx/v5, amqp091-go (копия эталона `umir-factory reference/durable`, R15).

**Spec:** /Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/serving.md (раздел 5), 00-orchestration.md (разделы 6, 10, 11).

## Global Constraints
- `GOWORK=off GOTOOLCHAIN=go1.26.9`; `make check`, `make quality-test`, `make integration`.
- Эталон правится только в factory (R15); в копии допустимы только импорт-пути.
- Нет режима memory и `SERVING_STORAGE` в боевом бинарнике (R20, C10); Redis не используется (U3); Serving ничего не публикует (C38).
- Лимиты G1-5: 200 позиций (400 InvalidArgument), 2000 активных предложений (409 LimitExceeded), id до 256 кодовых точек (400).
- Сроки R9: предложение без попытки 24 ч; терминальная попытка 30 дней; без commit 24 ч; закрытый ресторан 7 дней; pending без срока, автодоводка 30 дней.
- Транзакция Postgres не держится на время вызова склада.
- Conventional Commits, без Co-Authored-By, CHANGELOG «Не выпущено», `git diff --check`.

## Review Focus
- Две реплики подтверждают одну команду одновременно: один commit у склада.
- Исход неизвестен после COMMIT (CommitUnknownError) — `unknown`, повтор сходится.
- Событие чужой организации/запоздавшее не воскрешает удалённый ресторан.
- Устаревшая проекция одного ресторана не закрывает остальные.
- Старая попытка (>30 дней) не повторяет commit.

## Tasks (каждая оставляет ветку зелёной)
1. Основа: копия эталона, go.mod, arch-lint, миграция 0001 (+RLS, grants), тестовая инфраструктура `tests/itest`, `make integration`.
2. Домен/порты/память: вынос доменных функций, Lease, поля попыток, лимиты, `tests/storecontract` (memory).
3. `pgstore.Store` + контрактные тесты на Postgres + RLS-пул-тесты.
4. Приложение: lease вместо `lockProposal`, 503 при отказе хранилища, лимиты в Prepare/HTTP, длина id.
5. Регистрации: `RegistrationStore` (memory/pg), Registry без сверки, generation=epoch, ленивые Runtime, фикстуры.
6. Процесс: подкоманды migrate/serve/generation rotate/healthcheck, конфиг, /healthz /readyz, снимок готовности, AssertRuntimeRole, удалить newEpoch/REGISTRY_POLL_INTERVAL.
7. Проекция R5/R17: точечное чтение Auth с ETag, события `umir.auth`, degraded.
8. FIN-04: Interrupted, автомат, доводчик, токен `order.resolve`, логи, unresolved.
9. Очистка по срокам (TTL sweeper).
10. Отказные/интеграционные тесты F1–F9, нагрузка R13, README, CHANGELOG, самопроверка.
