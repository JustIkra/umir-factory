# Подспека G-serving: umir-serving на PostgreSQL

Приведено к решениям C1–C40 главной спеки (раздел 10), 15.10.2026.

Дата: 15.10.2026. Статус: черновик для ревью пользователя. Группа G-serving, репозиторий `umir-serving`. Подспека уточняет [главную](00-orchestration.md) (U1–U8, R1–R20); при расхождении действует главная. Ссылки: план — [serving #3](https://github.com/JustIkra/umir-serving/issues/3) (копия `scratchpad/plans/umir-serving-3.md`), ревью `main` — `scratchpad/main-review/serving.md`, опись prod-readiness — `scratchpad/main-review/stalled-persistence.md`. Номера строк ниже даны на `origin/main` `196ccc6`; группа сверяет их с актуальным SHA и записывает его в журнал.

## 1. Цель и границы

Serving владеет: предложениями заказа (`OrderProposal`), попытками подтверждения (`ConfirmationAttempt`) и их восстановлением, регистрациями ресторанов у себя (ответ на `PUT/DELETE /internal/serving/v2/r/{id}/registry` от Auth), меню/стоп-листом/рекомендациями как проекциями складских оценок. Warehouse остаётся единственным источником истины о продаже: Serving его не заменяет и не публикует `OrderCommitted`.

Цель: пережить рестарт процесса и работать в двух репликах за одним URL. Для этого предложения, попытки и регистрации уходят в PostgreSQL за существующие порты, сериализация на предложение переходит с процессного мьютекса на строку с lease, генерация Serving (`runtimeEpoch`) становится стабильной (R2), а запрос больше не скачивает реестр Auth (R5).

Не владеет и не делает: запасами, заказами и сроками (Warehouse); регистрацией, ревизией и событиями жизненного цикла (Auth); бюджетами допуска prepare/confirm (Gateway, R10); live-сигналами (Warehouse → R4); схемой DUR1 и эталоном адаптеров (G-factory); фронтовым хранением повторов (G-frontend).

## 2. Исходное состояние

`origin/main` `196ccc6`. Всё состояние в памяти процесса; файлов нет. Go-модуль без внешних зависимостей (`go.mod`), слои проверяет `.go-arch-lint.yml` (`adapters` не импортируют друг друга сверх разрешённого, `bootstrap` собирает).

| Что | Где |
| --- | --- |
| Порты хранения | `internal/application/ports.go:10-24` (`ProposalStore`, `ConfirmationAttemptStore`; сервис требует, чтобы один объект реализовывал оба: `orders.go:34`) |
| Память | `internal/adapters/memory/proposals.go` (две карты, `:24-35`; мёртвые `MarkPending/ResolvePending/Replace` `:273-360`) |
| Сериализация | `internal/application/orders.go:101-120` (`lockProposal`, процессный мьютекс на весь вызов склада), вызовы `orders_attempts.go:22,341` |
| Подтверждение, FIN-04 | `orders_attempts.go:18-199` (первый проход), `:360-413` (`resolveAttempt`, Unseen `:386-395`, Pending `:397-398`), `:415-422` (`retryScopeMatches`) |
| Реестр ресторанов, готовность | `bootstrap/registry.go` (`Register` `:53-110`, `Close` `:173-222`, глобальный `ready` `:224-301`), поток опроса `bootstrap/process_http.go:70-111` |
| Реестр Auth на запрос | `bootstrap/scoped_http.go:42` → `process_http.go:52-58` → `internal/adapters/authhttp/client.go:244-335` (`Registry`, `WarehouseEpoch`) |
| Эпоха процесса | `cmd/server/main.go:70` (`newEpoch()` `:114-122`), `bootstrap/registry.go:23-28,57` |
| Ошибки | `internal/adapters/httpapi/handler.go:437,447,464-466,509`; сохранённые коды `orders_attempts.go:438-476` |
| Проверка настроек складского ответа | `internal/adapters/warehousehttp/scoped.go:95-130` (`decodeScoped`, `:111-123`) |
| Сборка фикстур | `tests/fixtures/server/process.go:24` (вызывает `bootstrap.NewProcessHTTP` с `Epoch` и `RegistryPollInterval`) |
| Тесты хранилища | `tests/proposalstore_contract_test.go` (только memory), `internal/adapters/memory/attempts_test.go` |

Из заготовок prod-readiness (`_wt/prod/umir-serving`, только чтение) берётся один коммит: `f4db1b4` (`CommandConflict`). Проверено: `git apply --exclude=CHANGELOG.md` на `196ccc6` проходит чисто, CHANGELOG вносится вручную. Коммит `7e9789c` (FIN-04: до 3 повторов commit с паузой 2 с и счётчиком в попытке) **не переносится**: `main` уже содержит `adf3fd3` в той же области, а целевой протокол другой (раздел 5.5). Postgres-кода в Serving нет ни в `main`, ни в prod-readiness. Адаптеры pgx, миграции, AMQP-коннектор копируются из эталона G-factory `umir-factory/reference/durable` (R15); Warehouse `internal/adapters/postgres/{db,migrate}.go` и Gateway `internal/adapters/rabbitmq` из prod-readiness — только образец, зависеть от них нельзя.

## 3. Решения

**U1–U8.**
- U1 принят: база `serving`; роли `umir_serving_owner` (владелец таблиц, только задание `migrate-serving`) и `umir_serving` (среда выполнения, не владелец) (C8). Сам себя Serving не мигрирует.
- U2: Serving только **потребитель** exchange `umir.auth` (R17). Он ничего не публикует (C38) и не потребляет складские события: пассивные чтения склада по HTTP остаются единственным источником исхода заказа. Outbox и inbox в Serving не нужны (раздел 5.9).
- U3: **Redis не используется** (ключей `umir:srv:*` нет, C17). Проекция реестра хранится в памяти реплики; кеш чтений добавляется только по замерам R13, решение выносится на G2 (C27). Лимитера в Serving нет.
- U4 и R1: данные не переносятся. Из issue снимаются «Migration, cutover and rollback» п. 2–3 (экспорт памяти, импорт, shadow-режим) и приёмка «lossless». Остаются: остановка старого стека, runbook чистого листа (Factory), бэкап и восстановление новой базы. У старого Serving томов нет, откат — запуск старого образа. Новый стек стартует с пустой базой и новой storage generation.
- U5–U6: тот же релиз; подход A (хранение за портами); плоскость управления остаётся HTTP: `PUT/DELETE registry` от Auth и `GET /internal/serving/v2/runtime` не меняют проводной формат.
- U7: две реплики за одним адресом. Любая реплика обрабатывает любой запрос, включая регистрацию от Auth.
- U8: Postgres и RabbitMQ — контейнеры compose; конфигурация через `*_FILE` (раздел 5.10).

**R-правила.** R2 применяется (5.1). R5 и R17 применяются (5.6). R8 (RLS, роль не владелец; контекст эталона `Scope` и `InSystemTx`, C5) и R15 (копия эталона) применяются. R9 применяется по таблице раздела 6 п. 4 главной спеки, включая строки Serving (C23–C25). R12: допуск на `iat` остаётся нулевым; группа лишь фиксирует это тестом (issue просит «малую политику»; главная спека её снимает). R13: Serving обязан показать, что работа на запрос не растёт с числом ресторанов, и отдаёт фикстуру ресторанов для нагрузки Factory. R20 и C10: в боевом бинарнике режима `memory` и переменной `SERVING_STORAGE` нет; память живёт в `internal/adapters/memory` для тестов и фикстур, проверка «в проде нет memory» — тест сборки (бинарник без DSN не стартует). R3, R4, R6, R7, R10, R11, R14, R16, R18, R19 Serving не касаются: Gateway перестаёт быть пиром Auth, но Serving и так не вызывает Gateway; пиры Auth для Serving — те же.

**Из issue принимается:** PostgreSQL за портами; уникальность `(restaurant, proposal)` и `(restaurant, command)`; транзакции begin/bind/record/replace/complete; сохранение сохранённой generation склада и точного `evaluatedAt`/`stateVersion`; хранение по состоянию, а не по одному `expires_at`; ограниченный автомат восстановления (resolve по `unseen`, `completed`, `rejected`, `pending` с backoff); готовность по ресторанам; структурные логи; точечная проекция реестра с отказом по тому ресторану, чья запись устарела; запрет подмены pending-попытки чужой командой.
**Меняется:** broker-события Auth «lifecycle/readiness» заменены на сигналы инвалидации R17 (имена по C1) плюс точечное чтение (R5, C13); «consumer inbox/outbox» убраны (U2); Redis registry cache убран (U3); `CommandIdentityConflict → CommandConflict` — волна 1; «Define limits» — числа и коды по C15 (200 позиций — 400 `InvalidArgument`, 2000 активных предложений — 409 `LimitExceeded`).
**Вне объёма:** состояние `Interrupted` и бюджет повторов на стороне склада (G-warehouse, DUR1); ревизия и точечное чтение реестра, события `umir.auth` (G-auth); регрессия границы Gateway для `CommandConflict` (G-gateway); хранение повторов в браузере (G-frontend); нагрузочный стенд и бэкапы (G-factory); демо-данные.

## 4. Волна 1 — независимые правки

Ветка `fix/durable-w1-serving` от свежего `origin/main`. Каждая правка — отдельный коммит с записью в CHANGELOG («Не выпущено»). Контракт DUR1 не трогаем. Проверки: `make check`, `make quality-test`, `go test -race ./...` с `GOWORK=off GOTOOLCHAIN=go1.26.9`. Цели `make integration` в репозитории нет, поэтому в волне 1 она не требуется (C37); волна 1 не зависит от тестовой инфраструктуры волны 0.

**W1-1. `CommandConflict` и `TechnicalError` вместо кодов вне enum.**
- Что не так: `httpapi/handler.go:464-466` отдаёт `CommandIdentityConflict`, `:437,447` — `InternalError`; enum `components.schemas.Error.code` (`umir-factory/docs/api/completion-v1-composed-private.json`) знает `CommandConflict` и `TechnicalError`, но не эти два. Gateway по enum превращает такой ответ в 502/`unknown`. Сохранённые попытки пишут код `orders_attempts.go:447-448`, читают `:466`.
- Тест сначала: `TestServingCommandIdentityConflictIsEmittedAsCommandConflict` из `f4db1b4` (scoped и не scoped); таблица `TestEveryEmittedErrorCodeIsInContractEnum` — для каждой ошибки `contract.Err*` и `OperationError` без кода код ∈ списка из enum (список копируется в `tests/testsupport` с SHA источника); тест на чтение сохранённой попытки со старым кодом `CommandIdentityConflict`.
- Готово: `f4db1b4` применён (`git apply --exclude=CHANGELOG.md`), `InternalError → TechnicalError` (HTTP 500), старый код только читается; статус 409 и `outcome=not_committed` не изменились. Ответ склада о неоднозначной записи не переименовывается.

**W1-2. Сохранённая попытка: смотреть `/commands`, прежде чем отвечать Pending.**
- Что не так: `resolveAttempt` (`orders_attempts.go:385-398`) на складском `Pending` и на `Unseen` без повтора не вызывает `FindRecordedCommand`. Если владелец записал заказ, но вернул неизвестный исход, склад отвечает `Pending`, а Serving бесконечно отвечает `ConfirmationPending` (ревью, FIN-04, вторая половина). `/commands` разрешён токену `resolveOrder` (`warehousehttp/scoped.go:65`).
- Тест сначала (`bootstrap`, мок склада рядом с `order_epoch_http_test.go:45`): `/order-attempts` всегда `Pending`, `/commands` `found` → ждём `OrderConfirmed`, `commits==0`, попытка `Completed`; `/commands` с другим отпечатком → определённый `CommandConflict/not_committed`; `/commands` `found:false` → `ConfirmationPending`, `commits==0`; ошибка `/commands` → `unknown`, попытка не меняется. Существующий `TestResolvePendingDoesNotCommit` остаётся зелёным.
- Готово: порядок `FindOrderAttempt` → при `Pending/Unseen` `FindRecordedCommand` → `recordedOrder`/`finishReadOrder` (как `:77-101`); Resolve по-прежнему не отправляет commit. Живая зависшая попытка склада (`Pending` без записанной команды) остаётся `ConfirmationPending` — это закрывает только DUR1 (волна 2).

**W1-3. Смена настроек не должна ломать пассивное чтение известного исхода.**
- Что не так: `warehousehttp/scoped.go:111-123` для сохранённой привязки (`Retained`) требует от склада прежние `timeZone/settingsVersion`, а склад в конверте отдаёт настройки из текущего токена. После смены настроек Resolve и повторный Confirm получают `ProtocolError` → 502/`unknown`, хотя у склада попытка `Completed`.
- Тест сначала: `bootstrap/order_epoch_http_test.go` — мок склада, который эхом возвращает настройки из токена (как реальный `writeScopedResult`, `umir-warehouse/internal/adapters/httpapi/scoped.go:213-217`), параметр «версия настроек»: commit → unknown, смена v1→v2, Resolve/Confirm → `OrderConfirmed`; склад `Unseen` после смены → `ConfirmationPending`, `commits==1` (повтор commit запрещён `retryScopeMatches`). Нынешний мок возвращает старые настройки и расхождения не ловит.
- Готово: пассивные пути `/order-attempts`, `/commands`, `/orders` сверяют только org, tenant и generation (плюс собственную проверку результата); `/order-commits` под сохранённой привязкой остаётся строгим; README (`:79`) описывает правило.

**W1-4. Не снимать готовность процесса из-за одного ресторана.**
- Что не так: `bootstrap/registry.go:235` сбрасывает `ready` до проверки, `:282-296` возвращает `RuntimeNotReady`, если запись Auth `registering` ещё не установлена локально. Auth требует `st.Ready` для любого ресторана, поэтому создание одного ресторана закрывает доступ всей ячейке до следующего опроса (1–30 с).
- Тест сначала (`bootstrap/registry_test.go`): после первой полной сверки `Status().Ready==true`; вторая сверка с лишней записью `registering`, которой нет локально → `Ready` остаётся `true`, `Resolve` этого ресторана → `RuntimeNotReady`, соседний ресторан работает. До первой полной сверки готовность `false` (контракт backend-v2: «readiness remains false until complete»). Невалидный снимок, откат ревизии, недоступность Auth → `false` как раньше; запись `ready` без локальной установки тоже `false`.
- Готово: `registering` без локальной установки не влияет на `ready`; причина логируется (W1-9). Полная замена модели — волна 2.

**W1-5. JWKS: не держать общий шлюз на сетевом вызове.** `authhttp/client.go:90-143`: все проверки токена идут через один `keyGate`, а обновление (до 5 с) выполняется под ним. Тест сначала: фальшивый Auth держит ответ JWKS 2 с; 50 параллельных проверок токена по закешированному свежему ключу укладываются в 100 мс, пока идёт упреждающее обновление (старт при возрасте ≥ 20 с по управляемым часам). Готово: чтение без блокировки (`RWMutex`), обновление через singleflight с упреждением; fail-closed после 30 с и троттлинг неизвестного `kid` не меняются (существующие тесты `authhttp/client_test.go`, `retry_test.go`).

**W1-6. Пул соединений.** `warehousehttp/client.go:36-37`: `MaxIdleConnsPerHost` по умолчанию 2 при `MaxIdleConns: 64`. Тест сначала: два пакета по 32 параллельных запроса к `httptest`-серверу открывают не больше 40 соединений в сумме (счёт через `ConnState`). Готово: `MaxIdleConnsPerHost: 64`.

**W1-7. Ожидание блокировки предложения учитывает контекст.** `application/orders.go:101-120`: `lock.mu.Lock()` не отменяется, очередь из N Confirm держит горутины до N×12 с и мешает `Registry.Close` (`bootstrap/runtime.go:226-237`). Тест сначала: один Confirm застрял на складе, второй с дедлайном 100 мс возвращает ошибку контекста не позже чем через 150 мс, число горутин возвращается к исходному. Готово: канал-семафор с `select` по `ctx.Done()`. Код заменяется lease в волне 2; правка нужна, чтобы дефект не жил до G1.

**W1-8. Удалить мёртвые методы порта.** `ports.go:13-15` (`MarkPending`, `ResolvePending`, `Replace`) и `memory/proposals.go:273-360` не вызываются продовым кодом; Postgres-адаптеру они не нужны. Тесты, которые ими пользуются (`tests/proposalstore_contract_test.go:57-183`, `application/orders_test.go:581,606,673,921,1139-1142`), переписываются через `BeginAttempt`/`ReserveAttempt`/`CompleteAttempt` (вспомогательная `reservePending`). Падающего теста нет: это удаление; критерий — поведение тестов то же, `grep` по трём именам пуст, покрытие атомарной замены сохранено (`TestReplaceAtomic` идёт через `CompleteAttempt` с заменой).

**W1-9. Структурные логи неизвестных исходов.** Сейчас продовый код пишет только `log.Printf` об ошибках записи ответа (`handler.go`); сверка (`process_http.go:84-88`), неизвестный исход, протокольные ошибки склада невидимы. Внедряется `*slog.Logger` (JSON по `umir-factory/docs/observability.md` §3: `event`, `log_schema_version=1`, `service=umir-serving`, `instance_id`, `command_id`, `proposal_id`, эпохи). События: `confirmation.unknown`, `confirmation.pending.started`/`.resolved`, `warehouse.protocol_error`, `reconcile.failed` (причина), `registry.tenant_mismatch`. Тест сначала: slog-обработчик в памяти видит события в сценариях W1-2/W1-3/W1-4; в записях нет подстрок токена, `Authorization`, тела запроса (проверка с «канарейками»). Готово: `log.Printf` в продовом коде заменён.

**W1-10. README.** `README.md:34` («10 секунд») — на деле `WAREHOUSE_TIMEOUT` 5 с (`cmd/server/main.go:34`, `warehousehttp/scoped.go:28`); `:79` («unseen … cannot authorize another commit») противоречит `:154` и коду `orders_attempts.go:386-393`. Строки о «только в памяти» (`:36,146,150,161`) правятся в волне 2.

**W1-11. Ключи сервисов из файлов (C14).** `cmd/server/main.go:65-68` читает `SERVING_AUTH_KEY`, `AUTH_RUNTIME_KEY`, `GATEWAY_SERVING_KEY`, `SERVING_WAREHOUSE_KEY` только из окружения; durable-профиль factory требует секреты файлами. Тест сначала: для каждого ключа `<NAME>_FILE` читается (значение без завершающего перевода строки), заданные вместе `<NAME>` и `<NAME>_FILE` — ошибка старта без значения в тексте, пустой или нечитаемый файл — ошибка; в логах и ошибках значения нет (проверка «канарейкой»). Готово: функция `secretEnv(name)` в `cmd/server`, прежние переменные работают для legacy-профиля; README описывает оба способа.

**Не в волне 1, и почему:** TTL и лимиты (числа C15 вносит DUR1, сроки R9 — волна 2); FIN-04 для `Interrupted` (DUR1); реестр на запрос (нужны ревизия и точечное чтение Auth, R5); стабильная generation (R2/DUR1); «проигравшая» команда на предложении в ожидании (`orders_attempts.go:134-139,151-153`): какой определённый ответ даёт склад на новый `commandID` для уже оформленного предложения, уже решено Warehouse W1-2 (раздел 9, п. 7).

## 5. Волна 2 — хранение

Начало: после G1 и W1. Заглушки по DUR1, пока чужая сторона не готова (раздел 6).

### 5.1 Generation и экземпляр (R2)

- Generation создаёт первая миграция: строка `storage_meta(generation uuid)` с `gen_random_uuid()` (v4, подходит под `contract.ValidEntityID`) (C9). `serve` без generation не стартует и ключей не создаёт. Процесс читает её при старте; значение — это `runtimeEpoch` (заголовок `X-Umir-Runtime-Epoch`, query `runtimeEpoch` у resolve, ответ `/internal/serving/v2/runtime`, `RegistryOptions.Epoch`). Формат и поля на проводе не меняются.
- Идентификатор экземпляра — `SERVING_INSTANCE_ID`, уникальный для каждой реплики (`serving-a`, `serving-b`), его задаёт factory (C19); если не задан, берётся случайный UUID процесса. Виден в теле `GET /readyz` (`instance`) и логах (`instance_id`), входит в имя очереди `serving.auth.<instance>`. Никогда не попадает в бизнес-ответы и в сравнение эпох.
- Generation меняют только сброс и восстановление: подкоманда `umir-serving generation rotate --reason <текст>` (только при остановленном сервисе, под ролью `umir_serving_owner`, `SERVING_MIGRATE_DATABASE_URL_FILE`; идемпотентная) — шаг `stack.sh restore` у Factory (C9). Живая реплика сверяет generation при каждой проверке готовности; несовпадение — процесс завершается с ошибкой (рестарт подхватывает новое значение).
- `newEpoch()` (`cmd/server/main.go:114-122`) удаляется. В memory-хранилище generation — UUID на экземпляр хранилища (поведение фикстур прежнее: рестарт даёт `RuntimeReset`).

### 5.2 Схема (миграция `0001_init.sql`, `internal/adapters/postgres/migrations/`)

Идентификаторы ресторана и организации — `uuid`; `proposal_id`/`command_id` — `text` с `CHECK (char_length BETWEEN 1 AND 256)`: DUR1 вводит `maxLength: 256` кодовых точек для `ClientCommandID` и `ID` (C43), Serving проверяет длину до записи в хранилище (граница HTTP-входа, рядом с `handler.go:308`) и отвечает 400 `InvalidArgument`, `not_committed`; ошибка Postgres `54000` (запись слишком длинна для индекса) отображается так же как защита в глубину. Позиции и изменения хранятся как `jsonb` (читаются целиком, внутрь не запрашиваются). Точные `evaluatedAt`/`stateVersion` лежат в `context jsonb` в виде строк RFC3339Nano и decimal: `timestamptz` PostgreSQL усекает до микросекунд; отдельная колонка `evaluated_at timestamptz` нужна только для TTL.

```sql
storage_meta(singleton bool PK DEFAULT true CHECK(singleton), generation uuid NOT NULL DEFAULT gen_random_uuid(), created_at timestamptz NOT NULL, rotated_reason text)

serving_restaurants(                      -- регистрация от Auth; заменяет Registry.restaurants/closed
  restaurant_id uuid PK, org_id uuid NOT NULL, lifecycle_version bigint NOT NULL CHECK(>0),
  time_zone text NOT NULL, settings_version bigint NOT NULL CHECK(>0), operation_id uuid NOT NULL,
  state text NOT NULL CHECK(state IN ('registered','closed')),
  close_operation_id uuid, close_lifecycle_version bigint,
  registered_at timestamptz NOT NULL, closed_at timestamptz, updated_at timestamptz NOT NULL,
  UNIQUE(restaurant_id, org_id), CHECK((state='closed') = (closed_at IS NOT NULL)))

serving_proposals(
  restaurant_id uuid, org_id uuid, proposal_id text,                       -- PK(restaurant_id, proposal_id)
  context jsonb NOT NULL,                  -- ProposalContext: org, tenant, generation склада, tz, settingsVersion, evaluatedAt, stateVersion
  evaluated_at timestamptz NOT NULL,       -- копия для TTL
  requested_positions jsonb NOT NULL, accepted_positions jsonb NOT NULL, changes jsonb NOT NULL,
  position_count int NOT NULL CHECK(>0),
  superseded_by text, settled_at timestamptz,                              -- settled: заменено или оформлено
  lease_holder text, lease_until timestamptz, lease_seq bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL,
  FK(restaurant_id, org_id) -> serving_restaurants(restaurant_id, org_id))
  INDEX (restaurant_id) WHERE settled_at IS NULL;  INDEX (evaluated_at)

serving_confirmation_attempts(                                            -- PK(restaurant_id, command_id)
  restaurant_id uuid, org_id uuid, command_id text, proposal_id text, fingerprint text NOT NULL,
  state text NOT NULL CHECK(state IN ('Pending','Completed','Rejected')),  -- 'Unseen' не хранится
  pending bool NOT NULL DEFAULT false CHECK(NOT pending OR state='Pending'),  -- команда владеет предложением (резерв до отправки commit)
  context jsonb NOT NULL,                  -- сохранённый WarehouseContext: generation склада, tz, settings, evaluatedAt, stateVersion
  accepted_positions jsonb NOT NULL,
  warehouse_result jsonb, replacement jsonb, result jsonb, error jsonb,
  check_count int NOT NULL DEFAULT 0, next_check_at timestamptz, stuck_at timestamptz, unresolved_at timestamptz,
  created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
  CHECK((state='Completed') = (result IS NOT NULL)), CHECK((state='Rejected') = (error IS NOT NULL)),
  FK(restaurant_id, org_id) -> serving_restaurants(restaurant_id, org_id))
  UNIQUE INDEX (restaurant_id, proposal_id) WHERE pending;                 -- не больше одной команды на предложение
  INDEX (restaurant_id, proposal_id);  INDEX (next_check_at) WHERE pending AND unresolved_at IS NULL;  INDEX (updated_at) WHERE NOT pending
```

Внешнего ключа от попытки к предложению нет сознательно: предложение живёт по TTL короче попытки. Ключа `superseded_by` тоже нет по той же причине.

**RLS (R8).** На трёх таблицах с данными ресторана (`serving_restaurants`, `serving_proposals`, `serving_confirmation_attempts`) включены `ENABLE` и `FORCE ROW LEVEL SECURITY`; `storage_meta` данных ресторанов не содержит. Контекст и политики — только эталона (C5), своих GUC нет: транзакция по ресторану идёт с `Scope{Actor: tenant, OrgID, RestaurantID}` (ставит `umir.actor`, `umir.org_id`, `umir.restaurant_id`), политики `USING`/`WITH CHECK` строятся SQL-хелперами эталона (без контекста строк нет). Для фоновых проходов по ресторанам (очистка по срокам, выбор работы доводчика) используется `InSystemTx(ctx, reason)` эталона (`umir.actor='system'`, причины `ttl-sweep`, `reconciler-pick`); запрос обработки попытки идёт уже в контексте её ресторана. Собственной обёртки и политики для проходов в Serving нет.

### 5.3 Порты, адаптеры, транзакционные границы

`internal/adapters/postgres` реализует `application.ProposalStore` и `application.ConfirmationAttemptStore` одним типом `Store`, привязанным к ресторану при создании (`NewStore(pool, orgID, restaurantID)`), так что порты остаются без параметра ресторана (U6). Для регистраций вводится порт в `bootstrap` (потребитель определяет порт): `RegistrationStore{Generation, Register, Close, Get}` с реализациями `memory` и `postgres`. Правила `Register` и `Close` переносятся без изменений из `registry.go:112-135,173-222` (тот же `CommandConflict`, `ScopeDeleted`, `Forbidden`, повышение `settingsVersion`, идемпотентность). Чистые функции `acceptedPositionsWithin` и `requestedFromAccepted` переезжают из `memory/proposals.go` в `internal/domain`, чтобы оба адаптера использовали одну логику.

Ни одна транзакция не держится на время вызова склада. Каждая операция порта — одна короткая транзакция, которая первым делом берёт `SELECT state FROM serving_restaurants ... FOR SHARE`; `Close` берёт ту же строку `FOR UPDATE`. Поэтому после коммита `Close` любая запись в хранилище отказывает (`ScopeDeleted`) на любой реплике, а транзакции, начатые до него, завершаются раньше.

| Операция порта | Транзакция |
| --- | --- |
| `Create` | fence; лимит активных предложений (5.8); `INSERT` предложения. |
| `BeginAttempt` | fence; `INSERT … ON CONFLICT (restaurant_id, command_id) DO NOTHING`; существующая строка сравнивается по `proposal_id`, `fingerprint`, `accepted_positions` (иначе `ErrCommandIdentityConflict`); при `Pending=true` резерв предложения (нарушение partial unique → `ErrConfirmationPending`). |
| `ReserveAttempt` | `SELECT … FROM serving_proposals FOR UPDATE`; предложение заменено → `ErrProposalReplaced`; `UPDATE attempts SET pending=true`; чужая команда → нарушение unique → `errPendingMismatch`. Идемпотентно для той же команды. |
| `RememberWarehouseResult` | один `UPDATE` со сравнением: другой результат → конфликт; `replacement` записывается один раз и не перезаписывается. |
| `CompleteAttempt` | `FOR UPDATE` предложения и попытки; снять `pending`; при замене `INSERT` нового предложения (конфликт id → `ErrProposalExists`), `superseded_by` и `settled_at` на старом; терминальное состояние и результат; повтор идентичного завершения — без эффекта. Всё в одной транзакции (атомарная замена). |
| Lease | `AcquireLease`, `ReleaseLease` — по одному оператору, раздел 5.4. |

Ошибка хранилища **до** отправки commit в склад — определённый отказ без `unknown`: 503 `ServiceUnavailable`, `not_committed` для записи, `unavailable` для чтения (C16); после отправки — `unknown` и сохранённая попытка (`unknownCompletionError`, `orders_attempts.go:493-495`). `COMMIT` с неизвестным исходом (`CommitUnknownError` эталона) трактуется как ошибка после отправки. Любой неожиданный сбой `GetAttempt` — `unknown` (не доказывает «не отправлено»).

### 5.4 Сериализация на предложение: lease вместо мьютекса

Процессный мьютекс `lockProposal` (`orders.go:101-120`) удаляется. Вместо него `ConfirmationAttemptStore` получает `AcquireLease(ctx, proposalID, holder, ttl) (Lease, bool, error)` и `ReleaseLease(ctx, Lease)`. Строка предложения хранит `lease_holder/lease_until` и **fencing-счётчик** `lease_seq`.
- Захват: `UPDATE serving_proposals SET lease_holder=$h, lease_until=now()+$ttl, lease_seq=lease_seq+1 WHERE … AND (lease_until IS NULL OR lease_until < now()) RETURNING lease_seq`. Время берётся из БД, не из процесса.
- Lease держится на время сетевой части подтверждения и Resolve (обе точки: `orders_attempts.go:22,341`), TTL по умолчанию 30 с (не меньше `RequestTimeout + 5 с`). Если lease занят, вызов ждёт опросом не дольше `SERVING_LEASE_WAIT` (1,5 с, по контексту) и затем отвечает `ConfirmationPending`; это не ошибка и не доказательство `not_committed`.
- Мутирующие методы (`ReserveAttempt`, `RememberWarehouseResult`, `CompleteAttempt`) получают токен из контекста (`application.WithLease`, как `WithWarehouseBinding`) и выполняются только при совпадении `lease_seq`; иначе `ErrLeaseLost` → `unknown`, повторный вызов подберёт состояние пассивно. Истечение срока само по себе запись не запрещает: запрещает только захват другим держателем (счётчик вырос).
- **Логическая** принадлежность — партиальный уникальный индекс `WHERE pending`: она не имеет срока и переживает любой lease. Lease отвечает только за то, кто сейчас ходит в склад.
- Память реализует то же (в процессе), чтобы `OrdersService` имел один путь кода.

### 5.5 FIN-04 и фоновый доводчик

Состояния попытки склада на `GET /order-attempts` по DUR1 (C21): `Unseen`, `Pending` (попытка выполняется), `Interrupted` (выполнение прекратилось без записанного исхода), `Completed`, `Rejected`. Переходы описывает DUR1: `Pending` → `Interrupted`; `Interrupted` разрешает точный повтор commit той же командой. `contract.OrderAttemptState` и разбор в `warehousehttp/client.go:348-384` получают новое состояние только в scoped-клиенте; неизвестное состояние остаётся `ProtocolError`; прежнее рабочее имя `Abandoned` не используется.

Автомат для сохранённой неоконченной попытки (держатель lease; тот же код для запроса клиента и для доводчика):
1. Сначала `FindRecordedCommand` (W1-2). Найдена → завершить по записи.
2. `Completed` → `RememberWarehouseResult` + `CompleteAttempt`; `Rejected` с `not_committed` → терминальный отказ.
3. `Pending` (выполняется) → `ConfirmationPending`; `check_count++`, `next_check_at` с backoff 2, 4, 8 … не больше 60 с.
4. `Unseen` или `Interrupted` → если запрос клиента и `retryScopeMatches` (тот же org, tenant, generation склада, те же настройки): один повтор того же commit (`retryCommitBudget` 10 с, те же `commandID`, `proposalID`, accepted positions); дубль исключает идемпотентность склада по `commandID`. Иначе `ConfirmationPending`.
5. Смена generation склада, org, tenant или несовместимые настройки → автоматических повторов нет; пассивные чтения для известного исхода работают (W1-3). Пустое локальное состояние никогда не доказывает `not_committed`.
6. После 20 проверок (≈15 мин) строка помечается `stuck_at`, пишется `confirmation.stuck` (ERROR); проверки продолжаются раз в 15 мин. Возраст и число таких строк приёмка читает SQL-запросом (C33). Попытка не удаляется.
7. Автодоводка прекращается через 30 дней от `created_at` (C25; Warehouse хранит квитанции попыток не меньше 35 дней). Строка получает `unresolved_at`, пишется `confirmation.unresolved` (ERROR), строка остаётся в хранилище и видна в чтении (Resolve отдаёт `ConfirmationPending` с пассивным чтением по запросу клиента); доводчик её пропускает, **commit не повторяется** ни доводчиком, ни клиентом.

**Доводчик** — горутина на каждой реплике: раз в `SERVING_RECONCILER_INTERVAL` (5 с) в `InSystemTx(ctx, "reconciler-pick")` выбирает до 50 строк `WHERE pending AND unresolved_at IS NULL AND next_check_at <= now() ORDER BY next_check_at FOR UPDATE SKIP LOCKED` (реплики не пересекаются), сдвигает `next_check_at`, затем для каждой строки в контексте ресторана берёт lease (держатель `reconciler/<instanceID>`, без ожидания) и запускает автомат. Закрытые рестораны пропускаются. Доводчик **не отправляет commit**: повтор делает только точный повтор клиента (токен пользователя есть только там). Это закрывает: заказ записан, локальное завершение не сохранилось (результат склада уже в `warehouse_result`, доводчик завершает без обращения к складу), и опрос `Completed/Rejected`.

Пассивные чтения доводчика идут с сервисным токеном `order.resolve` (C11): Serving → Warehouse, один ресторан, текущая generation склада, только `/order-attempts` и `/commands`; токен выдаёт Auth (волна 2 Auth), принимает Warehouse (DUR1). Клиент склада сегодня принимает только пользовательскую область запроса с токеном (`warehousehttp/scoped.go:39-70`), поэтому в Serving добавляется сервисная область для двух пассивных операций. До выпуска токена Auth доводчик работает по заглушке DUR1: выполняет только локальную часть (`warehouse_result` без обращения к складу). Повтор commit остаётся за клиентом.

### 5.6 Проекция реестра: R5 и R17

`bootstrap` держит `projection`: ограниченная (`SERVING_PROJECTION_MAX_ENTRIES`, 20 000, вытеснение неактивных) память реплики «ресторан → запись»: `orgID`, `lifecycle` (`registering|ready|deleting|deleted` или надгробие), `lifecycleVersion`, `settings`, generation склада из готового пира `warehouse`, готовый пир `serving` с нашей generation, `sourceRevision`, `fetchedAt`. Заполняется лениво при первом запросе ресторана (singleflight на ресторан, не более 32 одновременных чтений Auth, таймаут 2 с); при старте реестр не читается и ресторан не регистрируется заново. `serving_restaurants` читается точечно вместе с проекцией (org и версия жизненного цикла должны совпасть с токеном, `state='registered'`).
- Источник — точечное чтение `/registry/{restaurantID}` у Auth с `ETag`/`If-None-Match` (C13; `authhttp.Client.RestaurantEntry`). Запись отражает подтверждённую готовность, живого `Status` в чтении нет; удалённый ресторан приходит надгробием, оно бессрочно. Прежняя валидация записи (`client.go:253-282`) сохраняется для одной записи.
- Запись свежа не дольше `SERVING_PROJECTION_MAX_AGE` (30 с). Устаревшая или не прошедшая проверку запись не продлевает доступ: Resolve этого ресторана отвечает `AuthUnavailable`/`RuntimeNotReady` (исход по `peerDiscoveryError`, `scoped_http.go:53-66`), остальные рестораны не затронуты. JWT и права проверяются как раньше на каждом запросе по JWKS.
- Инвалидация: ответ склада `RuntimeReset` сбрасывает запись; события `umir.auth` (R17, имена по C1) сбрасывают запись ресторана или всей организации:
  - `auth.restaurant.deleting`, `auth.organization.deleting` — сразу перестать обслуживать: запись (организации — всех её ресторанов по `orgID`) сбрасывается и помечается в проекции закрытой, пока Auth при точечном чтении не подтвердит иное;
  - `auth.restaurant.deleted`, `auth.organization.deleted` — сброс записи и надгробие в проекции; данные Serving удаляются по сроку после `closed_at` (5.8);
  - `auth.organization.settings_changed` — сброс проекции всех ресторанов организации.
  `auth.membership.revoked` и `auth.assignment.revoked` Serving не нужны: членство проверяет JWT. Очередь `serving.auth.<instance>`: exclusive, auto-delete (единственная очередь, которую Serving объявляет сам), привязка к `umir.auth` по пяти ключам выше, prefetch 32, ack после сброса. Сигнал ничего не разрешает и нигде не сохраняется: запись только сбрасывается (повтор, задержка и перестановка безвредны), поэтому **inbox не нужен**. Неизвестная `schemaVersion` и нечитаемое сообщение — сброс всей проекции (не чаще раза в 5 с) и запись в лог.
- Брокер недоступен или потребитель отключён: `MAX_AGE` сокращается до `SERVING_PROJECTION_DEGRADED_MAX_AGE` (5 с); после переподключения проекция сбрасывается целиком. Брокер не входит в готовность.
- Удаляются: `Registry.Reconcile`, поток опроса реестра, `ready` как функция сверки, `authhttp.Registry/WarehouseEpoch` (полное чтение), `Snapshot.Projection`, глобальный канал `registering`. Регистрация (`PUT`) теперь один `INSERT … ON CONFLICT` и не обращается к реестру Auth; `Runtime` создаётся лениво из строки и проекции, хранится в ограниченной карте (вытесняются только простаивающие, `active==0`).

### 5.7 Готовность

`GET /internal/serving/v2/runtime` отдаёт `ready` процесса: БД доступна (проверка раз в 2 с в фоне, ответ из атомарного снимка), схема совместима, generation загружена и совпадает с базой. Не входят: Auth, JWKS, брокер, состав ресторанов. Готовность ресторана — его строка `registered` и проекция; недоступный ресторан отвечает только за себя. Два адреса, без ключа (C32): `GET /healthz` — процесс жив; `GET /readyz` — основная возможность готова, тело `{instance, generation, capabilities: {<имя>: ok|degraded|down}}`. Возможности: `postgres` и `schema` (обязательны: `down` даёт 503), `auth_jwks`, `events` (`degraded` без 503); имена сверяются с `configuration.md`. Healthcheck compose и подкоманда `healthcheck` смотрят `/readyz`; подпутей `/readyz/*` нет. Состояние JWKS и проекции приёмка читает из `/readyz` и логов.

### 5.8 Лимиты и очистка по R9

- Лимиты (C15, числа утверждаются на G1): позиций в заказе не больше 200 — превышение даёт 400 `InvalidArgument`, `not_committed`; активных предложений на ресторан не больше 2000 — превышение даёт 409 `LimitExceeded`, `not_committed`. Позиции проверяются в `PrepareOrder` до вызова склада; активные (`settled_at IS NULL` и моложе 24 ч) считаются в `Create` запросом с `LIMIT`. Ответ явный, молчаливого усечения нет; квота на ресторан, межресторанного исчерпания нет. Длина `commandID`/`proposalID` — не больше 256 кодовых точек (C43), превышение — 400 `InvalidArgument`, `not_committed`, до обращения к хранилищу и складу. Переменные `SERVING_MAX_POSITIONS` и `SERVING_MAX_ACTIVE_PROPOSALS` задают те же числа.
- Очистка: горутина раз в `SERVING_TTL_SWEEP_INTERVAL` (1 мин), `DELETE … WHERE ctid IN (SELECT ctid … FOR UPDATE SKIP LOCKED LIMIT 1000)` (реплики не пересекаются).

| Срок (раздел 6 п. 4 главной) | SQL-условие |
| --- | --- |
| Предложение без попытки, 24 ч после `evaluatedAt` | `evaluated_at < now()-24h AND NOT EXISTS (попытка с этим proposal_id)` |
| Терминальная попытка, 30 дней | `NOT pending AND state IN ('Completed','Rejected') AND updated_at < now()-30d` |
| Pending/unknown попытка | не удаляется; автодоводка прекращается через 30 дней (`unresolved_at`, 5.5) |
| Попытка без отправленного commit, 24 ч (C25) | `state='Pending' AND NOT pending AND warehouse_result IS NULL AND updated_at < now()-24h` (резерв `pending` ставится до отправки, `orders_attempts.go:164`) |
| Ресторан закрыт, 7 дней после `closed_at` у владельца (C23) | строки попыток и предложений удаляются; строка ресторана остаётся как надгробие без срока |

Неоконченные попытки с несовпадающей generation склада (склад сброшен) остаются до срока выше: ответ по ним `RuntimeReset/indeterminate`, и удаление изменило бы его на `ProposalNotFound/unknown`.

### 5.9 RabbitMQ, Redis, outbox/inbox

RabbitMQ: только потребитель `umir.auth` (5.6); коннектор и обёртки — из эталона. Serving ничего не публикует (C38) и не привязывается к `umir.events`. Обменники и durable-очереди объявляет задание `mq-init` (C2); Serving объявляет только свою exclusive-очередь `serving.auth.<instance>`, обменник проверяет пассивно. Outbox и inbox эталона (форма C4) в Serving **не создаются**; если появится событие Serving, оно попросит CR (запись в outbox в той же транзакции, что и переход). Второй `OrderCommitted` не публикуется. Redis: **не используется** (U3); ключей, лимитера и аренд в Redis нет, аренды — строки Postgres (блокировка предложения — строка с lease и fence в таблице предложений, вне эталона, C4; `worker_leases` Serving не нужны: доводчик и очистка идут через `SKIP LOCKED`).

### 5.10 Конфигурация

Контракт переменных — `docs/durable/configuration.md` (C10). Режима `memory` и переменной `SERVING_STORAGE` нет. Новые: `SERVING_DATABASE_URL_FILE` (роль `umir_serving`; DSN читается как эталонный `LoadDSN`: заданные вместе `SERVING_DATABASE_URL` и файл — ошибка), `SERVING_MIGRATE_DATABASE_URL_FILE` (роль `umir_serving_owner`, только для задания `migrate-serving` и `generation rotate`; в среду `serve` не передаётся; `serve` сам не мигрирует и только проверяет версию схемы и `AssertRuntimeRole`), `SERVING_INSTANCE_ID`, `SERVING_DB_MAX_CONNS` (20), `SERVING_DB_ACQUIRE_TIMEOUT` (2 с), `SERVING_AMQP_URL_FILE` (нет или брокер недоступен → события выключены, проекция в режиме degraded; Redis-переменных нет), `SERVING_PROJECTION_MAX_AGE` (30 с), `SERVING_PROJECTION_DEGRADED_MAX_AGE` (5 с), `SERVING_PROJECTION_MAX_ENTRIES`, `SERVING_LEASE_TTL` (30 с), `SERVING_LEASE_WAIT` (1,5 с), `SERVING_RECONCILER_INTERVAL`, `SERVING_TTL_SWEEP_INTERVAL`, `SERVING_MAX_POSITIONS`, `SERVING_MAX_ACTIVE_PROPOSALS`. Эндпоинта метрик и `SERVING_METRICS_ADDR` нет (C33). Остаются: `AUTH_INTERNAL_URL`, `WAREHOUSE_INTERNAL_URL`, таймауты; ключи `SERVING_AUTH_KEY`, `AUTH_RUNTIME_KEY`, `GATEWAY_SERVING_KEY`, `SERVING_WAREHOUSE_KEY` получают варианты `*_FILE` уже в волне 1 (W1-11, C14). Удаляется `REGISTRY_POLL_INTERVAL`. Секреты только в `_FILE`, в логи не попадают.

### 5.11 Блоки задач

1. **Основа:** копия эталона (`internal/adapters/{postgres,rabbitmq}`), миграция, `storage_meta`, контекст `Scope`/`InSystemTx`, `AssertRuntimeRole`, конфиг, `/healthz` и `/readyz`, подкоманды `migrate`, `serve`, `generation rotate --reason` (`serve` без generation не стартует), разделение generation и экземпляра (R2), удаление `newEpoch`.
2. **Хранилище и контрактные тесты:** `Store` для двух портов, вынос доменных функций, lease, лимиты, TTL; общий пакет `tests/storecontract` для memory и Postgres.
3. **Регистрации и Registry:** `RegistrationStore`, ленивые `Runtime`, close-fence, удаление сверки и `ready` как функции реестра; обновление `tests/fixtures/server/process.go` (вместо `Epoch`/`RegistryPollInterval` — memory-хранилище).
4. **Проекция R5/R17:** клиент точечного чтения Auth, потребитель `umir.auth`, degraded-режим.
5. **FIN-04:** состояние `Interrupted`, автомат, доводчик, `unresolved`, логи; чтения по сервисному токену `order.resolve` включаются, когда Auth его выдаёт.
6. **Отказы, документация, чистка:** интеграционные и отказные тесты, фикстура ресторанов для R13, README, CHANGELOG, удаление мёртвого кода.

Порядок: 1 → (2 ∥ 3) → 4 → 5 → 6. Блоки 3–5 используют заглушки DUR1 (раздел 6).

## 6. Интерфейсы с другими группами

**Serving выдаёт.**

| Кому | Что | Основание |
| --- | --- | --- |
| Gateway, Frontend | `runtimeEpoch` стабилен при рестарте и смене реплики; `RuntimeReset` только при новой generation; `CommandConflict/not_committed`; 400 `InvalidArgument` на 201-ю позицию и 409 `LimitExceeded` на 2001-е активное предложение (оба `not_committed`); поведение `ConfirmationPending` и Resolve прежнее | DUR1/R2, C29, C15 |
| Auth | `GET …/runtime`: `epoch` = generation, `ready` процесса; `PUT/DELETE registry` идемпотентны, переживают рестарт и принимаются любой репликой; `PUT` больше не ждёт готовности склада (готовность ресторана учитывает Auth по ответам пиров) | R2, R5 |
| Factory | Образ с подкомандами `migrate`, `serve`, `generation rotate --reason`, `healthcheck`; роли `umir_serving_owner`/`umir_serving`; переменные 5.10; шаг `stack.sh restore`: после восстановления БД выполнить `generation rotate` до запуска реплик (C9); уникальный `SERVING_INSTANCE_ID` на реплику; 2 реплики за одним адресом; `/healthz` и `/readyz` | R15, U7 |

**Serving потребляет.**

| От кого | Что | Основание |
| --- | --- | --- |
| G-factory | Эталон `reference/durable` (pgx, `InTx` с повтором 40001/40P01, миграции под advisory lock, RLS-контекст, AMQP-коннектор), `durable-test-infra.sh` | R15, раздел 6.6 |
| G-auth | Точечное чтение `/registry/{restaurantID}` с `ETag`/`If-None-Match` (единственный источник «generation склада и готовности»); события `umir.auth` по C1; сервисный токен `order.resolve` (C11) | DUR1/R5, C13, R17 |
| G-warehouse | Состояние `Interrupted` (C21); квитанции попыток не меньше 35 дней (C25); идемпотентность `order-commits` по `commandID` в durable-режиме; в конверте пассивных чтений настройки текущего токена (уже так); ответ на новый `commandID` для уже оформленного предложения — `warehouse.md` W1-2 (200 с тем же `orderID`, не второй заказ); приём токена `order.resolve`; `CommandConflict` вместо `CommandIdentityConflict` (C29) | DUR1, R14 |

**CR.** Прежние CR сняты решениями раздела 10: токен доводчика — C11, имена событий и точечное чтение — C1 и C13, лимиты — C15, шаг `generation rotate` — C9, системный контекст RLS — C5. Новых CR группа не открывает; при расхождении с эталоном или DUR1 — по правилам раздела 7 главной.

## 7. Тесты и проверки

- **Unit:** `go test -race ./...`; `make check`, `make quality-test`; тесты волны 1 (раздел 4) остаются регрессией. Тест сборки (C10): боевой бинарник без DSN не стартует, режима memory нет. Тест `serve`: сам не мигрирует и под ролью-владельцем или суперпользователем не стартует (`AssertRuntimeRole`); без generation не стартует.
- **Контрактные тесты хранилища** `tests/storecontract.Run(t, newStore)` — один набор для memory (`internal/adapters/memory`) и Postgres (`internal/adapters/postgres`, тег `integration`): неизменяемость и копирование; круг по времени с наносекундами (`time.Equal`); идемпотентность и конфликт `BeginAttempt`; 16 параллельных `ReserveAttempt` разных команд — ровно один победитель; `CompleteAttempt` с заменой атомарна (сбой вставки замены оставляет попытку и предложение прежними); повтор завершения без эффекта; `replacement` записывается один раз; lease (захват, захват после истечения, устаревший токен отклоняется, свежий не затронут); лимиты; TTL не удаляет pending и недоставленные попытки только после 24 ч; изоляция ресторанов; регистрации (повторная, повышение настроек, конфликт, повтор закрытия, fence блокирует запись после `Close`). Существующие `tests/proposalstore_contract_test.go` и `memory/attempts_test.go` переезжают сюда.
- **Интеграционные** (`make integration`, тег `integration`, `UMIR_TEST_DATABASE_URL`, `UMIR_TEST_AMQP_URL`; временная база и vhost создаёт тест, `FLUSHALL` запрещён, без переменных набор падает): миграции идемпотентны и параллельны (две «реплики»); план запросов горячих путей без Seq Scan на 100 тыс. строк; RLS с настоящей не-владельческой ролью `umir_serving` и повторным использованием соединения пула (после транзакции ресторана A транзакция без контекста видит 0 строк; `InSystemTx` видит строки только с причиной и не утекает в пул); два экземпляра приложения на одной базе: параллельный одинаковый Confirm даёт одну попытку и ровно один commit у счётчика-склада.
- **Процесс-уровня:** `handler()` дважды на одной БД — тот же `runtimeEpoch`, предложение и попытка сохранились, ни одного обращения к полному реестру Auth (фальшивый Auth считает запросы к `/internal/auth/v1/registry`, ждём 0), рестораны не перерегистрируются.
- **Сценарии отказов #17, которыми владеет G-serving:**
  - F1 крах процесса до и после коммита БД (до `ReserveAttempt`; после ответа склада, до `CompleteAttempt`): новый экземпляр завершает по `warehouse_result` или пассивным чтением, commit у склада один;
  - F2 потерянный ACK `COMMIT` (обёртка возвращает `CommitUnknownError` на `Reserve`/`Complete`): исход `unknown`, повтор сходится;
  - F3 недоступность и рестарт БД: до отправки — определённый 503 `ServiceUnavailable`, после — `unknown`; `ready=false` только пока БД недоступна; восстановление без перерегистрации; сбой записи (имитация «диск полон») мапится так же;
  - F4 истёкший lease и конкурирующие реплики: зависший держатель, второй захватывает, поздняя запись первого отклонена `ErrLeaseLost`; две реплики на один `commandID` — один эффект;
  - F5 потребитель `umir.auth`: дубль, задержка, перестановка, нечитаемое и неизвестной версии сообщение; `deleting` закрывает ресторан сразу, `organization.settings_changed` сбрасывает все рестораны организации; запоздавшее событие не воскрешает удалённый ресторан;
  - F6 недоступность и рестарт брокера: проекция уходит в degraded (≤ 5 с), удаление/отзыв продолжает действовать, ready не падает;
  - F7 межресторанные негативные: настоящая роль, пул, подделанное событие чужой организации;
  - F8 склад: таймаут после commit, `Pending`, `Interrupted`, `Unseen`, `Completed`, `Rejected`, смена настроек, смена generation склада;
  - F9 рестарт: предложения и команды живы, `runtimeEpoch` прежний; рестарт только Serving и всей сборки.
  Автодоводка 30 дней: попытка старше срока получает `unresolved_at`, commit не отправляется. Публикацию в брокер, outbox и confirms Serving не владеет.
- **Нагрузка (R13, часть Serving):** фикстура 10 000 ресторанов с надгробиями в фальшивом Auth: стоимость запроса ресторана не растёт с числом ресторанов (< 2× при росте в 10×), 0 полных чтений реестра, пул БД, p50/p95/p99, время восстановления проекции; числа утверждает G1.
- **Логи и наблюдаемость (C33):** тест «канарейки» (токены, тела, ПДн не попадают). Эндпоинта метрик нет: приёмка читает SQL владельца (возраст самой старой pending-попытки, число `stuck_at` и `unresolved_at`, число активных предложений), `/readyz`, логи (события раздела 4, W1-9, плюс `confirmation.unresolved`, `projection.reset`, `auth_events.degraded`) и management API RabbitMQ. Запросы приводятся в README.

## 8. Приёмка группы

- [ ] Все одиннадцать правок волны 1 влиты в ветку, каждая с падавшим сначала тестом (кроме W1-8, W1-10), `make check`, `make quality-test`, `go test -race ./...` зелёные; числа в отчёте волны.
- [ ] Рестарт процесса сохраняет `runtimeEpoch`, активные предложения, все pending и терминальные попытки с точным составом, generation склада, отпечатком, результатом и ссылкой замены; надгробие Auth по-прежнему закрывает ресторан.
- [ ] Две реплики на одной базе: параллельные Confirm одной команды дают одну попытку и один эффект склада; конфликтующие отпечатки — `CommandConflict/not_committed`; pending-попытка не перезаписывается новой командой (partial unique индекс проверен тестом).
- [ ] Транзакция Postgres не охватывает вызов склада (тест с блокирующим складом и параллельным доступом к строке).
- [ ] Сбой после commit склада и до локального завершения, рестарт: заказ находится без второго commit; таймаут до commit: точный повтор сходится; проверены `Pending`, `Interrupted`, `Unseen`, `Completed`, `Rejected`.
- [ ] Доводчик без клиента завершает попытку с сохранённым `warehouse_result`; пассивные проверки идут с токеном `order.resolve`; есть `confirmation.stuck` в логе и в SQL-запросе приёмки; через 30 дней попытка становится `unresolved` без повтора commit; повторы не идут при смене org, tenant, generation склада или настроек.
- [ ] После смены настроек известный исход читается; для `Unseen` остаётся `unknown`, дубля и несанкционированного commit нет.
- [ ] Обычный запрос не обращается к полному реестру Auth; рестарт не перерегистрирует рестораны; устаревшая или неполная запись отказывает только для своего ресторана; удаление, отзыв и надгробие не воскресают от запоздавшего события.
- [ ] Регистрация или закрытие одного ресторана не меняет готовность остальных; `ready` процесса и состояние ресторана раздельны (проверено в сборке Factory).
- [ ] Сроки по таблице 5.8 (C23–C25): pending/unknown не удаляются, без commit — 24 ч, закрытый ресторан — 7 дней после `closed_at`, терминальные — 30 дней; лимиты C15 отвечают 400 `InvalidArgument` и 409 `LimitExceeded`.
- [ ] RLS с реальной ролью `umir_serving` и пулом проверена; `AssertRuntimeRole` при старте; контекст не утекает; `InSystemTx` используется только проходами; сам себя Serving не мигрирует.
- [ ] Боевой бинарник без DSN не стартует; режима memory и `SERVING_STORAGE` нет (R20, C10); «запасного» пути при недоступности БД нет; `*_FILE` ключей работают (C14).
- [ ] Удалено: `lockProposal`, `newEpoch`, сверка реестра и её `ready`, полное чтение реестра, `REGISTRY_POLL_INTERVAL`, `storage new-generation`, `SERVING_METRICS_ADDR`, мёртвые методы порта; README и CHANGELOG отражают фактическое поведение.
- [ ] Финальное ревью ветки (Opus) — Ready; отчёт `.superpowers/sdd/2026-10-15-durable/umir-serving/wave-<N>-report.md`.

## 9. Риски и вопросы к оркестратору

1. Закрыто: C11 — токен `order.resolve` (Serving → Warehouse, один ресторан, только пассивные `/order-attempts` и `/commands`); повтор commit остаётся за клиентом.
2. Закрыто: C23–C25 — сроки Serving внесены в таблицу раздела 6 п. 4 (24 ч без commit, 30 дней терминальная, 7 дней после `closed_at` у владельца, автодоводка 30 дней → `unresolved`).
3. Закрыто: C15 и C43 — 200 позиций (400 `InvalidArgument`), 2000 активных предложений (409 `LimitExceeded`), длина `commandID`/`proposalID` не больше 256 кодовых точек (400 `InvalidArgument`); числа утверждаются на G1.
4. Закрыто: «Вопросы групп», Serving Q4 — свежесть проекции 30 с, при недоступном брокере 5 с. Токен действует 60 с, запись в хранилище после `Close` отказывает сразу; более жёсткая граница удаления для чтений оплачивается нагрузкой на Auth (точечные чтения, R13).
5. **`PUT registry` больше не ждёт готовности склада.** Auth считает ресторан готовым только по ответам всех пиров, поэтому внешне это безопасно; проверку в тестах пиров делает G-auth (сверка №5 по `srv`). Раздел 10 вопрос не закрывает.
6. Закрыто: C27 и C17 — Redis в Serving не нужен; общий кеш проекции рассматривается по замерам R13, решение на G2.
7. Закрыто: Warehouse W1-2 (`warehouse.md`) — commit с новым `commandID` для уже оформленного предложения даёт 200 с тем же `orderID`. `orders_attempts.go:134-139,151-153` пока отвечает `ProposalNotFound/unknown`; проверка совместимости с W1-2 входит в интеграцию волны 3, код волны 2 не меняется до неё.
