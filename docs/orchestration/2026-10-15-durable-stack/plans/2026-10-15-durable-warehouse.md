# Warehouse, волна 1: независимые правки перед хранением в PostgreSQL

> Для исполнителей: каждая задача идёт по TDD (сначала красный тест на базе `52aae49`, потом код). Чекбоксы `- [ ]` отмечает ведущий в журнале группы.

**Цель.** Выполнить раздел 4 подспеки G-warehouse (W1-1…W1-9), не меняя наружное поведение, кроме W1-6. Подспека: `docs/superpowers/specs/2026-10-15-durable-stack/warehouse.md`; главная: `00-orchestration.md` (разделы 2, 3, 5, 7, 8, 10).

**Репозиторий.** `umir-warehouse`, worktree `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-warehouse`, ветка `fix/durable-w1-warehouse`, база `52aae49`.

**Архитектура решения (кратко).**

- `InventoryState` теряет `Orders`, `OrdersByProposal`, `Commands`; `Deliveries` -> `DeliveryIDs`. Редьюсер строит малый слой изменений `StateDelta` (затронутые записи), проверяет событие против «база + слой» и только при успехе вливает слой. Клонов состояния нет.
- Порт `EventStore` получает `ScanBatches`, `ReadEvents`, `ReadPeriod`, `FindOrderPlacement`. Восстановление идёт потоком (`ScanBatches`) в локальный кандидат; публикуется только при успехе.
- Снапшот `SnapshotSchemaV2`: структурная проверка при загрузке, сверка с журналом один раз при сохранении (предыдущий валидный снапшот + хвост журнала), вне блокировки владельца, в `SnapshotWorker`.
- Мьютекс владельца с контекстом; `usable=false` только при несогласованности журнала и состояния.
- Чтения (`order-commits`, `orders`, `order-attempts`, `commands`, history, planning, `Movements`) используют ограниченные запросы к порту, а не копию журнала.
- Общий контрактный набор хранилищ `tests/storecontract` (память сейчас, Postgres в волне 2).

**Проверки.** `GOWORK=off GOTOOLCHAIN=go1.26.9`: `make tools` (один раз), `make check`, `make quality-test`. `make integration` не требуется (цели нет, C37). Масштабный замер: `make perf` (тег `perf`, добавляется в T3b).

## Порядок задач

| Задача | Пункт подспеки | Суть |
| --- | --- | --- |
| T1 | W1-8 | `tests/storecontract`: контракт `EventStore`/`SnapshotStore` для `memory`; два прежних файла переезжают |
| T2 | W1-2 (порты) | `ScanBatches`, `ReadEvents`, `ReadPeriod`, `FindOrderPlacement` в порте и `memory`; кейсы в контракт |
| T3a | W1-1 | Редьюсер со слоем изменений, усечённое `InventoryState`, `SnapshotSchemaV2`, владелец без клонов |
| T3b | W1-1 | Потоковое восстановление, кандидат вне блокировки, сверка снапшота при сохранении, неизменяемое представление `Prepare`, `tests/perf`, `make perf` |
| T4 | W1-2 | Обработчики и планирование на ограниченных чтениях, исправление С1, `Movements` без журнала |
| T5 | W1-3 | Мьютекс с контекстом, `usable` только при несогласованности, кандидат восстановления, `WriteTimeout` |
| T6 | W1-4 | Повторная регистрация с другими настройками |
| T7 | W1-5 | JWKS single-flight |
| T8 | W1-7 | `zonedata` кеш, `Offer` один клон, изоляция сбоя снапшота по ресторану, нулевой допуск `iat` |
| T9 | W1-9 + W1-6 | `*_FILE` ключей сервисов; явный `WAREHOUSE_API_MODE` (`!`), compose, README, CHANGELOG |
| T10 | — | Замеры «до/после», README/CHANGELOG, финальные проверки, отчёт, PR |

Порядок строгий: T1 -> T2 -> T3a -> T3b -> T4 -> T5 -> T6…T9 -> T10.

---

## T1. W1-8: общий контракт хранилищ

**Файлы.** Создать `tests/storecontract/{eventstore.go,snapshotstore.go,fixture.go}` (обычные не-`_test` файлы пакета `storecontract`, чтобы Postgres-тест волны 2 их вызывал), `tests/storecontract_memory_test.go` (вызов для `memory`). Перенести и удалить `tests/eventstore_contract_test.go`, `tests/snapshotstore_contract_test.go` (кейсы сохранить, дублей не оставлять; `TestFingerprintConfirmOrderIdentity` не относится к контракту — оставить в `tests/`).

**Интерфейс.**

```go
package storecontract

type Stores struct {
    Events    application.EventStore
    Snapshots application.SnapshotStore
}

type Fixture struct {
    New   func(t *testing.T) Stores              // чистый ресторан
    Other func(t *testing.T, s Stores) Stores    // второй ресторан на том же бэкенде
    Close func(t *testing.T, s Stores)           // освобождение (может быть nil)
}

func RunEventStore(t *testing.T, f Fixture)
func RunSnapshotStore(t *testing.T, f Fixture)
```

Для `memory`: `Other` возвращает новую пару хранилищ (изоляция по построению, но кейс обязателен).

**Кейсы `RunEventStore`** (каждый `t.Run`): повтор по `commandID`+отпечатку возвращает прежний результат (`Replayed=true`), другой тип/отпечаток — `ErrCommandIdentityConflict`; CAS по `ExpectedVersion` (устаревшая версия — `ErrConcurrentModification`, повтор уже записанной команды со старой версией — прежний результат); многособытийный batch атомарен, номера подряд; уникальность `eventID`, `proposalID` (через `OrderPlaced`), `orderID`?, `deliveryID` — см. что реально защищает `memory` сегодня (eventID, proposalID); для `orderID`/`deliveryID` добавить проверку в `memory` (дубликат -> `ErrInvalidEvent`/`ErrInvalidEventBatch`, как для proposal), красные тесты сначала; границы `ReadBatches` (не на границе batch -> `ErrInvalidRecordedBatch`, отрицательное значение, `after == version` -> пусто); `FindCommand`, `FindOrderByProposal`; отмена контекста до `AppendBatch` не оставляет следов (`Version` не изменилась, `FindCommand` пусто); изоляция двух ресторанов (одинаковые `commandID`/`eventID`/`proposalID` независимы).

**Кейсы `RunSnapshotStore`:** копирование при `Save`/`LoadLatest` (мутации вызывающего не видны), монотонность (более старый снапшот не заменяет новый), отказ невалидному кандидату без замены предыдущего, отмена контекста до сохранения не оставляет следов, изоляция ресторанов.

Кейсы для `ScanBatches`/`ReadEvents`/`ReadPeriod`/`FindOrderPlacement` добавляет T2.

- [ ] Красный тест на дубликат `orderID`/`deliveryID` в `memory` (если сегодня не отвергается), затем код.
- [ ] Все прежние кейсы зелёные в новом пакете; `make check` зелёный.
- [ ] Коммит: `test(storecontract): add shared event and snapshot store contract`.

## T2. W1-2 (порты): ограниченные чтения хранилища

**Порт `application.EventStore`** (файл `internal/application/ports.go`):

```go
ScanBatches(ctx context.Context, after int64, fn func(domain.RecordedBatch) error) error
ReadEvents(ctx context.Context, after int64, limit int) ([]domain.Event, error)
ReadPeriod(ctx context.Context, q PeriodQuery) ([]domain.Event, error)
FindOrderPlacement(ctx context.Context, orderID string) (OrderPlacement, bool, error)

type PeriodQuery struct {
    From, To     time.Time          // From включительно, To исключительно, по RecordedAt
    Types        []domain.EventType // пусто = все типы
    UpToSequence int64              // события с Sequence <= UpToSequence
}
type OrderPlacement struct {
    Sequence   int64
    RecordedAt time.Time
}
```

Семантика: `ScanBatches(after)` как `ReadBatches(after)` (после `after`, `after` должен лежать на границе batch, иначе `ErrInvalidRecordedBatch`; `after<0` — `ErrInvalidRecordedBatch`), но потоково; `fn` получает копию, ошибка `fn` прерывает обход и возвращается как есть; блокировка хранилища не удерживается во время `fn`. `ReadEvents(after, limit)` — события с `Sequence > after` по возрастанию, не более `limit` (`limit<=0` — `ErrInvalidRecordedBatch`), не зависит от границ batch. `ReadPeriod` — по возрастанию `Sequence`. `FindOrderPlacement` — по `orderID`.

Реализовать в `memory.EventStore` (индекс `orderID -> placement`; `ReadPeriod` линейный). `ReadBatches` остаётся в порте (используется контрактом и ограниченными участками). Дополнить `storecontract.RunEventStore` кейсами: границы `ScanBatches` как у `ReadBatches` и остановка по ошибке `fn`; `ReadEvents` (окно, лимит, середина batch); `ReadPeriod` (граница From включительно, To исключительно, фильтр типов, `UpToSequence`); `FindOrderPlacement` (найден/нет, значения `Sequence`/`RecordedAt` события `OrderPlaced`).

- [ ] Красные тесты (кейсы контракта не компилируются/падают), затем реализация.
- [ ] `make check` зелёный. Коммит: `feat(application): add bounded event store reads`.

## T3a. W1-1 (часть 1): редьюсер без клонирования, усечённое состояние

**Домен.**

1. `models.go`: `InventoryState{Products, Dishes, DeliveryIDs map[string]struct{}, Batches, StateVersion}`; удалить `Orders`, `OrdersByProposal`, `Commands`, `Deliveries`.
2. `state.go`: `StateDelta{Products, Dishes, DeliveryIDs, Batches, StateVersion}`; `PlanBatch(state *InventoryState, batch RecordedBatch) (StateDelta, error)` читает «слой, потом база» и не изменяет `state`; `(*InventoryState).Merge(StateDelta)`; `(*InventoryState).ApplyBatch(RecordedBatch) error` = план + слияние; при ошибке состояние не меняется (в том числе при ошибке на втором событии batch). Проверка дубликата `commandID` из редьюсера уходит (её держит хранилище). Проверка дубликата `orderID`/`proposalID` из `applyOrder` уходит (держит хранилище), остальные проверки событий сохранить. Прежний `ApplyRecordedBatch(state, batch) (InventoryState, error)` убрать из боевого кода; тесты перевести на `ApplyBatch`/`PlanBatch`.
3. `clone.go`: `CloneInventoryState` (копия четырёх карт и `Dish.Ingredients`) остаётся для снапшотов/представлений; убрать клонирование удалённых карт.
4. `snapshot_validation.go`: `SnapshotSchemaV2`; `ValidateSnapshot` принимает только V2 (`V1` -> `ErrUnsupportedSchema`); `ValidateInventoryState` оставляет структурные проверки справочников, блюд, партий (баланс, времена, ссылка партии на продукт и `DeliveryID` из `DeliveryIDs`), убирает `validateOrders`, `validateCommands`, `validateDeliveryCommandResult` и перекрёстную проверку «партия соответствует строке поставки»; `ValidateSnapshotPrefix` убрать; `equalInventoryState` экспортировать как `EqualInventoryState` (сравнение справочников, блюд, `DeliveryIDs`, партий, версии).
5. `ValidateInventoryTotals(state)` -> проверка по затронутым слоем продуктам либо по состоянию с слоем без клона (допустимо считать суммы по `state.Batches` с подменой записями из слоя, без создания копии карты).

**Приложение (минимум, чтобы собиралось и работало как раньше).**

- `Owner.applyCommittedBatch`/`applyProjectionBatch`: `PlanBatch` + `Merge`, индекс сроков `expirations` обновляется на месте по затронутым партиям из слоя (предикат: остаток > 0 и не `PendingWriteoff` -> в индексе, иначе удалить); `cloneExpirationIndex` убрать.
- Проверки `Commands[...]`/`Orders[...]`/`OrdersByProposal[...]` переписать на `EventStore.FindCommand`/`FindOrderByProposal` (`delivery.go`, `orders.go`, `recovery.go`).
- Пробное применение в командах (`CommitAcceptedOrder`, `ReceiveDelivery`, `ConfirmWriteoff`) — через `PlanBatch` (слой), без полного состояния; для поставки «временные» партии берутся из слоя.
- `snapshots.go`: `offerSnapshotLocked` создаёт снапшот `SnapshotSchemaV2`; `SnapshotWorker.Offer` не меняется (T8).
- `recovery.go`: пока по-прежнему `ReadBatches(0)` и `validateJournalContext`, но состояние строится новым редьюсером; снапшот проверяется структурно и хвостом (аудит префикса `auditSnapshotPrefix` убрать вместе с `ValidateSnapshotPrefix`; сверка при сохранении — T3b). Не оставлять промежуточный код, которого нет в конечном решении, кроме указанного.
- Тесты, затронутые удаляемыми полями, исправить по смыслу, а не удалять проверки; потерянные проверки перекрёстных связей снапшота заменить тестами на оставшиеся структурные проверки.

**Красные тесты (на базе падают/не компилируются, добавляются первыми):** `TestRejectedBatchLeavesStateUntouched` (многособытийный batch с невалидным вторым событием; глубокое сравнение состояния до/после), `TestApplyBatchAllocsIndependentOfHistory` (`testing.AllocsPerRun`, состояния после 1000 и 10 000 заказов через реальный редьюсер: аллокации равны в пределах 10 %).

- [ ] Красные, затем код. `make check` зелёный. Коммит(ы): `refactor(domain): apply batches through a change layer`, `refactor(domain)!: drop order and command history from inventory state` (если несовместимость снапшота; миграции нет — снапшоты были только в памяти).

## T3b. W1-1 (часть 2): восстановление потоком, снапшот при сохранении, представление, замеры

**Приложение.**

1. `recoverLocked`: кандидат в локальных переменных; `LoadLatest` -> структурная проверка -> `ScanBatches(after=snapshot.LastSequence)` с потоковым валидатором (непрерывность номеров, `ValidateRecordedEvent`, `ResultForBatch`; множества `commandID`/`eventID` ведёт только обход от нуля); при ошибке хвоста от снапшота — откат к обходу от нуля и запись в лог; итоговая версия сверяется с `Version`; `o.state`, `o.expirations`, `usable` публикуются только при успехе. `validateJournalContext` (O(N) карты) заменить потоковым валидатором; из `planning.go` и `analytics.go` его убрать в T4.
2. `syncProjectionLocked`: хвост через `ScanBatches`, применяется по одному batch на месте (каждый атомарен); ошибки контекста не снимают `usable`.
3. `SnapshotWorker`: функция проверки кандидата вне блокировки владельца до `Save`: предыдущий валидный снапшот (`LoadLatest`) + хвост журнала до `candidate.LastSequence` (`ScanBatches`), сравнение через `EqualInventoryState`; при несовпадении — ошибка, снапшот не сохраняется, предыдущий остаётся. Нет предыдущего — обход от нуля один раз.
4. `Owner.Prepare` отдаёт неизменяемое представление: мелкая копия `Products`/`Dishes`/`Batches` (`DeliveryIDs` не отдаётся); внутренние вызовы под блокировкой (`prepareLocked`) получают заимствованное представление без копирования; документировать «не удерживать и не менять».
5. `GetHistory` (legacy v1) остаётся полным обходом через `ScanBatches(0)` с комментарием «по контракту legacy».

**Тесты.**

- `TestReadStateHasNoAliases` (запись в возвращённые `Products`/`Dishes`/`Batches` под `-race` не видна владельцу и снапшоту).
- `TestSnapshotPlusTailEqualsFullReplay` (в `application`, случайные последовательности команд по сидам: продукты, блюда, поставки, заказы, ленивая просрочка, подтверждение списания; сравнить владельца «непрерывного», восстановленного «снапшот + хвост» и «полный обход»: `StateVersion`, остатки, FEFO (`EvaluateMenu`), `pending`, индекс сроков, результаты команд через `FindCommand`). Зелёный до и после (критерий эквивалентности).
- Тест сверки при сохранении: порченый кандидат (структурно валиден, расходится с журналом) не сохраняется и не заменяет предыдущий.
- `tests/perf` (тег `perf`, перенос `wh-exp/zz_review_scale_test.go`): корпус 1000/4000/8000 заказов (+20 на замер), замеры из воспроизведения ревью плюс: восстановление «с журнала» (владелец без снапшота) и «снапшот + хвост», `Movements`, `commit_avg`, куча свежего владельца (`heap` после восстановления нового `Owner` на том же хранилище, остальное — отдельным числом). Проверки только ростом: `recover_journal(8020)/recover_journal(1020) < 12`, то же для `Movements`, `commit_avg(8020)/commit_avg(1020) < 3`. Абсолютных порогов времени нет; числа печатаются `t.Logf` таблицей. Harness использует только API, существующий на базе `52aae49` (для замера «до»), кроме явно новых вызовов в отдельных файлах.
- `Makefile`: цель `perf` (`go test -tags perf -run 'TestScale' -count=1 -v -timeout 30m ./tests/perf`); README — строка про цель. `.golangci.yml`: `run.build-tags: [perf]` если это не ломает конфигурацию.

- [ ] Красные тесты первыми, затем код. `make check`, `make quality-test` зелёные.
- [ ] `make perf` проходит; таблица замеров записана в журнал.
- [ ] `grep -rn "ReadBatches(ctx, 0)\|validateJournalContext" internal bootstrap cmd` (без тестов) пуст, кроме `GetHistory` legacy. Коммиты логическими группами.

## T4. W1-2 (часть 2): обработчики и планирование на ограниченных чтениях, исправление С1

**Приложение/адаптеры.**

- `Owner.Head(ctx) (Head{StateVersion int64; EvaluatedAt time.Time}, error)` без копии журнала (`events.Version` + часы).
- `Owner.FindOrderPlacement`, `Owner.FindOrder(ctx, orderID)` (placement -> `ReadEvents(seq-1, 1)` -> `Payload.Order`), `Owner.ReadHistoryPage(ctx, from, limit)`.
- Расширить `bootstrap.Runtime` и `httpapi.Runtime` (интерфейсы адаптера) методами `Head`, `FindOrderPlacement`/`FindOrder`, `HistoryPage`; `scoped_orders.go` (`consumerCommit`, `consumerOrder`, `consumerAttempt`), `scoped.go` (`consumerCommand`), `scoped_reads.go` (страница history, лимит 500) переходят на них. Коммит берёт `RecordedAt`/`Sequence` из `FindOrderPlacement` по `orderID` записанного заказа, а не ищет событие по `commandID` (исправление С1).
- `planning.go`: под блокировкой только проверки области, захват версии и мелкого представления состояния; факты периода — `ReadPeriod(UpToSequence=version)` вне блокировки; расчёт вне блокировки; `validateJournalContext` из планирования убрать.
- `analytics.go`: `AnalyticsJournalReader` заменить портом захвата (`CaptureJournal` под блокировкой: версия, часы, мелкое представление `Products`) плюс `ReadPeriod` вне блокировки; `Movements` берёт события периода из `ReadPeriod`, названия из захваченного представления; `ReadAnalyticsJournal` удалить; `ErrUnknownEventSchema` для неизвестной версии схемы события сохранить.

**Красные тесты (на базе падают, добавляются первыми):**

- `TestSameProposalNewCommandOverHTTP` (перенос `wh-exp/zz_review_proposal_test.go` в `tests/integration`): второй ответ 200 с тем же `orderID`, `recordedAt`, `stateVersion`; `order-attempts?commandID=c2` = Completed; повтор `c2` возвращает тот же ответ.
- `TestCommitDoesNotReadJournal`: обёртка над store считает `ReadBatches(0)`/`ScanBatches(0)`; ноль вызовов за commit (HTTP и `CommitAcceptedOrder`) и за пассивные запросы (`orders`, `order-attempts`, `commands`, страница history).
- `TestHistoryPageReadsOnlyWindow`, `TestMovementsReadsOnlyPeriod`.
- `TestPlanningCaptureDoesNotBlockCommits`: блокирующий `ReadPeriod`, параллельный `CommitAcceptedOrder` проходит; ответ planning несёт захваченную `stateVersion` и не видит поздних событий.
- `perf` (в `tests/perf`): на корпусе 8020 заказов commit по HTTP не дороже `CommitAcceptedOrder` больше чем на 1 мс (печатается, проверка — разница, а не абсолют; допускается как лог-проверка при флаппинге, ведущий решает по замеру).

- [ ] Красные -> код. `make check` зелёный. Коммиты: `fix(httpapi): resolve repeated proposal commits`, `perf(application): bound planning and analytics journal reads`.

## T5. W1-3: отмена и дедлайн не портят проекцию

- Мьютекс владельца: `ctxMutex` (канал ёмкости 1; `Lock(ctx) error` сначала проверяет `ctx.Err()`, затем `select` по захвату и `ctx.Done()`; `Unlock`). Все методы `Owner` и `Planning`, принимающие контекст, берут `Lock(ctx)`; ожидающий с истёкшим контекстом уходит без побочных эффектов, ошибка отображается теми же публичными кодами, что и раньше для контекстной ошибки этого метода. Методы без контекста (`ConfigureScope`, `ConfigureTZData`, `ConfigureZoneLoader`, `ValidateAnalyticsScope`, `confirmSnapshotSaved`, захват `snapshotWorker`) берут `lockUncancellable()`.
- `usable=false` только при несогласованности журнала и состояния; перед установкой в каждом месте списка `grep "usable = false"` проверка `isContextErr(err)`. `recoverLocked` уже кандидатный (T3b); проверить отмену посередине.
- `cmd/server/main.go`: `WriteTimeout = requestTimeout + 5s`.

**Красные тесты** (перенос `wh-exp/zz_review_cancel_test.go`, детерминированный барьер вместо `time.Sleep`): `TestCanceledWaiterKeepsProjection`, `TestCanceledVersionReadKeepsProjection`, `TestCanceledRecoveryKeepsPreviousProjection`, `TestWaiterLeavesOnDeadline`, `TestServerWriteTimeoutExceedsRequestTimeout`.

- [ ] Красные -> код. `make check` с `-race` зелёный. Коммит: `fix(application): keep the projection on canceled requests`.

## T6. W1-4: повторная регистрация с другими настройками

`bootstrap/registry.go` (`register`): тот же `lifecycleVersion`; настройки с большим `SettingsVersion` заменяют сохранённые (в записи реестра и в `Owner.scope`), инвентарь сохраняется; меньший — успешный повтор без изменений; тот же номер с другим поясом — `ErrCommandIdentityConflict`; `operationID` как сейчас. Сравнение версий — числовое (`contract.ValidVersion`). Красные тесты: три случая, «потерянный ответ, Auth повторяет операцию с новыми настройками организации», после повторов остатки и журнал неизменны.

- [ ] Красные -> код. Коммит: `fix(bootstrap): accept repeated registration with newer settings`.

## T7. W1-5: JWKS single-flight

`internal/adapters/authkeys/client.go`: обновление выполняет один инициатор на собственном канале (без `x/sync`), с отвязанным контекстом (`context.WithoutCancel` + таймаут клиента), ожидающие ждут результат по своему контексту, сетевой вызов не под `c.mu`; правило «неудачное обновление не отдаёт устаревшие ключи» сохраняется. Красные: `TestCanceledInitiatorDoesNotPoisonRefresh`, `TestConcurrentKeyCallsOneRefresh`, `TestRefreshDoesNotHoldCacheLock`.

- [ ] Красные -> код. Коммит: `fix(authkeys): refresh signing keys once for all callers`.

## T8. W1-7: мелочи ревью

- Н2: кеш `*time.Location` по имени в `zonedata.Load`; красный — аллокации повторного `Load` <= 1 (`testing.AllocsPerRun`) и параллельный тест под `-race`; бенчмарк из `wh-exp/zz_review_zone_test.go`.
- Н5: `SnapshotWorker.Offer` не клонирует до проверки необходимости и клонирует один раз (учесть и `RunOnce`); `Registry.RunSnapshotWorker`: сбой сохранения снапшота одного ресторана не останавливает workers остальных, повтор с выдержкой на ресторан; красный `TestSnapshotSaveFailureIsolatedPerRestaurant`.
- Н4: тест фиксирует нулевой допуск на `iat` (R12): токен с `iat` на секунду в будущем отвергается, с `iat == now` принимается.

- [ ] Красные -> код. Коммиты по пунктам.

## T9. W1-9 и W1-6: конфигурация

- W1-9: для `AUTH_SERVICE_KEY`, `AUTH_RUNTIME_SERVICE_KEY`, `GATEWAY_SERVICE_KEY`, `SERVING_SERVICE_KEY` варианты `<NAME>_FILE`: значение читается из файла, срезается конечный перевод строки; заданы и переменная, и файл — ошибка запуска; значение не попадает в ошибки и логи; пустой файл — ошибка. Читатель файлов внедряется в `loadConfig`. Красные: `TestServiceKeyFromFile`, `TestServiceKeyBothSetIsError`, `TestServiceKeyNotInErrors`. README (таблица переменных) и CHANGELOG.
- W1-6: пустой `WAREHOUSE_API_MODE` — ошибка запуска с понятным текстом; `legacy` только явным значением. Красный `TestLoadConfigRequiresExplicitAPIMode`; обновить `TestLoadConfigDefaultsAndPort`. Явный `WAREHOUSE_API_MODE: legacy` в `compose.yaml` репозитория и во всех запусках фикстур/тестов, которые полагались на умолчание. Коммит с `!`, запись в CHANGELOG, README. В PR отметить зависимость: сливается после правки compose у factory (C35).

- [ ] Красные -> код. Коммиты: `feat(config): read service keys from files`, `feat(config)!: require an explicit API mode`.

## T10. Замеры и сдача

- Замер «до» на базе `52aae49` (временный worktree в scratchpad) и «после» на ветке тем же harness: таблица 1000/4000/8000: commit, menu, supply, recInputs, inventory, history, movements, recover (журнал и снапшот+хвост), аллокации на commit, heap. Числа целей FIN-10 (commit <= 5 мс, replay <= 2 с, `Movements` <= 2 с, heap/ресторан <= 15 MiB) сверить и записать фактические числа.
- README: переменные, `make perf`, ограничения; CHANGELOG «Не выпущено» по каждой правке.
- `make check`, `make quality-test`, `git diff --check`; число тестов; финальное ревью (Opus), один цикл правок; push; draft PR; отчёт `wave-1-report.md`.
