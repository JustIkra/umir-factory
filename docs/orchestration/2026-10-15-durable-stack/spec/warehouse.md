# Подспека G-warehouse: Warehouse на PostgreSQL, RabbitMQ и Redis

Приведено к решениям C1–C40 главной спеки (раздел 10), 15.10.2026.

Дата: 15.10.2026. Репозиторий `umir-warehouse`, база `origin/main` `52aae49`. Подспека уточняет [главную](00-orchestration.md) (U1–U8, R1–R20, разделы 5, 6, 8); при расхождении действует главная, а в ней раздел 10 (C1–C40) главнее этой подспеки. Источники: план (issue [warehouse #4](https://github.com/JustIkra/umir-warehouse/issues/4), копия `scratchpad/plans/umir-warehouse-4.md`), отчёт ревью `scratchpad/main-review/warehouse.md` (замечания В1–В3, С1–С6, Н1–Н6 ниже по этим номерам), опись `scratchpad/main-review/stalled-persistence.md`, воспроизведения `scratchpad/main-review/wh-exp/`. Путь `scratchpad` — как в главной спеке.

## 1. Цель и границы

Warehouse владеет остатками ресторана, журналом событий, партиями и FEFO, снапшотами, квитанциями команд, попытками подтверждения заказа, расписанием сроков и исходящими сигналами об изменениях. Цель группы: всё это живёт в PostgreSQL; процесс не хранит ничего, что нельзя восстановить из базы; две реплики работают на одной базе без общего состояния в процессе; горячая память ограничена; журнал растёт, а стоимость команды и чтения — нет.

Не владеет и не делает:

- регистрацией, удалением и авторизацией ресторанов (Auth; Warehouse лишь исполняет `PUT/DELETE .../registry` и проверяет JWT);
- запуском просрочки по расписанию (R6: планировщик в Gateway; Warehouse отдаёт сроки);
- доставкой сигналов клиентам (Gateway), предложениями и попытками подтверждения (Serving), правилами FEFO, точностью количеств, формами публичных ответов (не меняются);
- общими адаптерами (R15: эталон `umir-factory/reference/durable`, Warehouse его копирует).

## 2. Исходное состояние

`origin/main` `52aae49`. Всё состояние живёт в памяти процесса; один ресторан = один `Owner` со своими `memory.EventStore` и `memory.SnapshotStore` (`bootstrap/runtime.go`, `NewInMemory`). Ключевые пути:

| Что | Где |
| --- | --- |
| Порты `EventStore` (`AppendBatch`, `ReadBatches`, `FindCommand`, `FindOrderByProposal`, `Version`) и `SnapshotStore` — без `restaurant_id` | `internal/application/ports.go` |
| Владелец проекции, мьютекс, флаг `usable`, `pending` | `internal/application/owner.go`, `recovery.go`, `catalog.go:266` (`appendOwnerCommandLocked`) |
| Редьюсер (клонирует всё состояние, `state.go:18`), `InventoryState` с `Orders`/`Commands`/`Deliveries` | `internal/domain/state.go`, `models.go`, `clone.go`, `snapshot_validation.go` |
| Планирование, аналитика (`ReadAnalyticsJournal`, `Movements`), захваты страниц с TTL 5 мин | `internal/application/planning.go`, `analytics.go`, `product_batches.go` |
| Координатор попыток заказа (Unseen/Pending/Rejected/Completed) только в памяти | `internal/application/order_attempts.go` |
| Реестр ресторанов, tombstone, операции регистрации и закрытия; эпоха процесса | `bootstrap/registry.go`, `cmd/server/main.go` (`newEpoch`) |
| Ворота запросов (fence при закрытии) | `bootstrap/runtime_gate.go` |
| HTTP v2: маршруты, lifecycle, runtime status | `internal/adapters/httpapi/scoped*.go`, `lifecycle.go` |
| Кеш JWKS | `internal/adapters/authkeys/client.go` |
| Конфигурация | `cmd/server/config.go` (`WAREHOUSE_API_MODE` по умолчанию `legacy`) |
| Контрактные тесты портов (два мелких) | `tests/eventstore_contract_test.go`, `tests/snapshotstore_contract_test.go` |

Проверки `52aae49` из ревью: сборка, vet, 565 тестов с `-race` зелёные; `make check` не запускался (нет инструментов, `make tools`). Слои держит `.go-arch-lint.yml`: `application` не импортирует `adapters`, `domain` — ничего.

**Заготовка prod-readiness** (`_wt/prod/umir-warehouse`, не закоммичена, только читать и копировать):

- Взять: `bootstrap/storage.go` (`OpenStorage`, `Ready`; без режима `memory`, C10); `WAREHOUSE_AMQP_URL(_FILE)` и разбор DSN через `LoadDSN` эталона (не копия заготовки) из `cmd/server/{config,main}.go`; цель `make integration` (`-p 1 -race`, только в волне 2, C37); правки `.go-arch-lint.yml`, `.golangci.yml`, `scripts/go-quality.sh`; идеи `internal/adapters/postgres/{db,migrate}.go` (`InTx` с повтором 40001/40P01, `CommitUnknownError`, миграции под advisory lock, отказ при схеме новее кода) и `tests/itest/itest.go` (временная БД, падение без переменных).
- Не брать как есть: схему `0001_init.sql` (эпоха на БД в `runtime_state`, нет таблицы команд и RLS, нет расписания сроков, outbox без envelope из каталога, нет запрета DELETE); подключение `Storage` ничего не делает (`bootstrap/runtime.go:54` всё равно создаёт память); `WAREHOUSE_AMQP_URL` читается и не используется.
- Если эталон `reference/durable` (R15) готов к старту волны 2, `db.go`/`migrate.go`/`itest.go` берутся из него, заготовка не копируется. Если нет, заготовка копируется как временная и заменяется эталоном отдельным коммитом.
- `umir-warehouse-features`: FIN-11 (`supply.go`) выбросить; `be5b76f` (`withinHours`) к хранению не относится, вне объёма (решает оркестратор).

## 3. Решения

**Принимается из issue как есть:**

- PostgreSQL владеет журналом, квитанциями команд, снапшотами, метаданными жизненного цикла и outbox.
- Транзакция команды: проверка повтора по `commandID` и отпечатку, CAS по версии потока, непрерывные последовательности, квитанция и outbox в одной транзакции; успех только после подтверждённого commit; потерянный ответ COMMIT = неизвестный исход, разрешается тем же `commandID`.
- Ограничения БД: `(restaurant, sequence)`, `(restaurant, eventID)`, `(restaurant, commandID)`, уникальность заказа, предложения и поставки; предикат ресторана в каждом запросе.
- Снапшот: проверка перед сохранением, восстановление с последнего валидного и хвоста; повреждённый снапшот откатывается к журналу с сигналом и никогда не обнуляет остатки.
- Планирование, рекомендации и движения читают ограниченные индексные выборки; версия фиксируется под блокировкой, расчёт идёт вне её.
- Outbox: at-least-once, стабильный `eventID`, доставка не блокирует и не откатывает команду.
- Не меняются FEFO, точность, политика просрочки, правила JWT, публичная форма API, синхронный ответ для Gateway. Исправления JWKS (С4), повторной регистрации (С5), legacy по умолчанию (С6), отмены в очереди (В2).

**Меняется правилами:**

- **R2.** `runtimeEpoch` = storage generation (раздел 5.1). Идентификатор экземпляра отдельный, виден только в `/readyz` и логах. Генерация не меняется при рестарте и смене реплики.
- **R4.** События идут из outbox в `umir.events` (topic); Warehouse — единственный производитель этого обменника (C38). Темы `catalog`/`stock`/`menu`/`orders`/`analytics` — в envelope (5.6); Gateway пока рассылает все пять (C39). Заголовки `X-Umir-Invalidation-Required` остаются в ответах без изменений (меняет DUR1, не мы).
- **R6.** Расписание сроков в БД и выдача Gateway ближайшего срока (5.7).
- **R8.** RLS обязателен, роль среды выполнения не владелец таблиц (C8); контекст транзакции — эталонные `Scope{Actor, OrgID, RestaurantID, UserID}` и `InSystemTx(ctx, reason)` с переменными `umir.actor`/`umir.org_id`/`umir.restaurant_id`/`umir.user_id`, своих GUC нет (C5).
- **R9.** Сроки хранения только из таблицы главной спеки (5.10); значения утверждает G1.
- **R15.** Адаптеры pg/amqp/redis копируются из эталона с комментарием-источником. Аренды фоновых работ — таблица эталона `worker_leases`, outbox и inbox — формы эталона без своих столбцов (C4).
- **R20.** В боевом бинарнике нет режима `memory` и переменной `WAREHOUSE_STORAGE`: memory-адаптеры живут только в тестовом коде, проверка «в проде нет memory» — тест сборки (бинарник без DSN не стартует) (5.9, C10).
- **R14.** «Тот же proposal, новый commandID» — подтверждённый дефект С1, не «unproven».
- FIN-04 (состояние `Interrupted` на проводе, C21), `CommandConflict` вместо `CommandIdentityConflict` (C29), ближайший срок (C22), сервисные токены `expiry.due` и `order.resolve` (C11) — контракт DUR1; реализация в волне 2.

**Снято R1 (чистый лист, U4):**

- Раздел issue «Migration, cutover and rollback» п. 2–3: экспорт из памяти живого владельца, импорт с исходной generation, shadow/read-only сравнение.
- Сопутствующий п. 4 в части переноса: остаётся fence-на-старом и пины образов как runbook factory.
- Откат: только до первой записи нового стека, запуском старых образов на старых томах.
- Ни одного импортёра, экспортёра, сверки с памятью. Новый стек стартует с новой generation и пустой базой.

**Вне объёма:**

- Собственный планировщик просрочки и внутренний таймер (R6).
- Удаление legacy v1: отдельным PR волны 3 после того, как factory выведет legacy-профили из CI и README (C28); в волне 1 только запрет неявного включения.
- Допуск на `iat` (Н4): остаётся нулевым по R12, поведение фиксируется тестом.
- Обрезка исчерпанных партий в горячем состоянии, инбокс и потребители RabbitMQ (Warehouse ничего не потребляет).
- Redis-кеш готовых отчётов по ревизии: сначала замер (5.8). Изменение формата событий журнала.
- Cherry-pick `be5b76f`, FIN-11.

## 4. Волна 1 — независимые правки

Ветка `fix/durable-w1-warehouse`; контракты и DUR1 не трогаются, наружное поведение сохраняется (кроме W1-6). Каждая правка: сначала красный тест, затем код, в конце `make check`. Порядок: W1-8 → W1-1, W1-2 (вместе правят редьюсер и порты) → W1-3 → остальные независимо.

**W1-1. FIN-10 (В1): редьюсер без клонирования состояния, горячее состояние без истории.**

- Что не так: `domain/state.go:18` клонирует всё состояние на каждый batch; три клона на команду (`prepareLocked`, пробное применение, `applyCommittedBatch`); `owner.go:120` отдаёт полную копию; `recovery.go:73-140` и `snapshot_validation.go:21-42` перед использованием снапшота воспроизводят весь префикс (O(N²)); `InventoryState` хранит `Orders`, `OrdersByProposal`, `Commands` навсегда. Замер ревью: 8020 заказов — commit 30,8 мс, replay 34 с, 54 MiB.
- Сделать:
  1. Редьюсер применяет batch к рабочему состоянию через малый слой изменений (затронутые записи `Products`/`Dishes`/`Batches`/`DeliveryIDs`); при любой ошибке состояние не меняется, при успехе слой вливается. Полного клона нет.
  2. Из `InventoryState` убрать `Orders`, `OrdersByProposal`, `Commands`; `Deliveries` заменить множеством `DeliveryIDs`. Проверки команды и заказа по `commandID`/`proposalID` идут через `EventStore.FindCommand`/`FindOrderByProposal` (`delivery.go:73`, `orders.go:148-155`, `recovery.go:131`).
  3. Снапшот получает новую версию схемы без этих карт (`SnapshotSchemaV2`; старые снапшоты были только в памяти, совместимость не нужна). Перекрёстные проверки «расход партий = заказы» (`validateOrders`/`validateCommands`) уходят из загрузки; сверка снапшота с журналом делается один раз при сохранении и инкрементально: кандидат равен предыдущему валидному снапшоту плюс хвост журнала (вне блокировки владельца).
  4. `Prepare` отдаёт неизменяемое представление (мелкая копия карт `Batches`/`Dishes`/`Products`, а не истории); читатели и снапшоты не получают изменяемых алиасов.
- Первыми красные: `tests/perf` (тег `perf`, перенос `wh-exp/zz_review_scale_test.go`, корпус 1000/4000/8000 заказов) с проверкой роста, а не абсолюта: `recover(8020)/recover(1020) < 12`, то же для `Movements` (линейно ≈ 8, сейчас ≈ 100) и `commit_avg(8020)/commit_avg(1020) < 3`; `TestApplyRecordedBatchAllocsIndependentOfHistory` (`testing.AllocsPerRun` на состоянии 1000 и 10 000 заказов — равны в пределах 10 %); `TestRejectedBatchLeavesStateUntouched`; `TestReadStateHasNoAliases` (запись в возвращённые карты под `-race` не видна владельцу и снапшоту).
- Эквивалентность (зелёные до и после, критерии FIN-10): `TestSnapshotPlusTailEqualsFullReplay` на случайных последовательностях команд (по сидам): `StateVersion`, остатки, FEFO, pending, индекс сроков, результаты команд.
- Готово: рост линейный по тестам выше; на корпусе 8020 заказов, M-класс, без `-race`, начальные цели (уточняет ведущий после первого замера): commit ≤ 5 мс, replay ≤ 2 с, `Movements` ≤ 2 с, heap/ресторан ≤ 15 MiB; таблицу «до/после» (латентность и аллокации) приложить к отчёту волны.

**W1-2. Ограниченные чтения (С2, С3, `Movements`) и исправление С1.**

- Что не так:
  - `scoped_orders.go:54` (`order-commits`), `:105`, `:113`, `scoped.go` `consumerCommand` и `scoped_reads.go:49` (страница history) читают весь журнал через `Owner.GetHistory`; пассивные запросы и каждая страница стоят O(N).
  - `planning.go` держит `Owner.mu` на `ReadBatches(0)`, `validateJournalContext` и расчёт.
  - `Movements` (`analytics.go:221-238`) воспроизводит весь журнал ради названий продуктов.
  - С1: после коммита по тому же `proposalID` с новым `commandID` обработчик ищет событие с этим `commandID`, не находит, отвечает `500 TechnicalError unknown` навсегда (`scoped_orders.go:54-65`); попытка при этом `Completed`.
- Сделать (внутренние порты, не провод): расширить `EventStore`:
  - `ScanBatches(ctx, after, fn)` — потоковое чтение для восстановления и догона;
  - `ReadEvents(ctx, after, limit)` — страница history;
  - `ReadPeriod(ctx, PeriodQuery{From, To, Types, UpToSequence})` — события периода по номеру не позже захваченной версии;
  - `FindOrderPlacement(ctx, orderID)` — `(sequence, recordedAt)` события `OrderPlaced`.
  Новые методы реализуются в `memory` и входят в общий контракт (W1-8).
- Обработчики: коммит берёт `RecordedAt`/`Sequence` из `FindOrderPlacement` по записанному заказу (не по `commandID` события); `consumerCommand`/`consumerAttempt` берут версию и `evaluatedAt` через `Owner.Head` (версия + часы), а не копию журнала.
- Планирование: под блокировкой только захват версии и представления состояния; факты периода — `ReadPeriod(UpToSequence=version)` вне блокировки; `validateJournalContext` остаётся только в восстановлении. `Movements`: события периода из `ReadPeriod`, названия из захваченного представления; `ReadAnalyticsJournal` удаляется.
- Красные тесты:
  - `TestSameProposalNewCommandOverHTTP` (перенос `wh-exp/zz_review_proposal_test.go`): второй ответ 200 с тем же `orderID`, `recordedAt`, `stateVersion`; `order-attempts?commandID=c2` = Completed; повтор `c2` = тот же ответ.
  - `TestCommitDoesNotReadJournal`: счётчик на обёртке над store, ноль `ReadBatches(0)`/`ScanBatches(0)` за commit и за пассивные запросы.
  - `TestHistoryPageReadsOnlyWindow`; `TestMovementsReadsOnlyPeriod`.
  - `TestPlanningCaptureDoesNotBlockCommits`: блокирующий `ReadPeriod`, параллельный `CommitAcceptedOrder` проходит, ответ planning несёт захваченную `stateVersion` и не видит поздних событий.
- Готово: на корпусе 8020 заказов commit в HTTP не дороже `CommitAcceptedOrder` больше чем на 1 мс; рекомендации Serving не удерживают блокировку дольше захвата.

**W1-3. Отмена и дедлайн не портят проекцию; мьютекс с учётом контекста (В2, Н3).**

- Что не так: `recovery.go:26-29` и `owner.go:105-108` ставят `usable=false` при любой ошибке `Version`, включая `context.Canceled`/`DeadlineExceeded`; `recoverLocked` ставит `usable=false` в начале (`recovery.go:75`) и при отмене посередине оставляет работоспособную проекцию помеченной непригодной; `o.mu.Lock()` везде без контекста. Эксперимент: следующее чтение 1,48 с вместо 2 мс на 3000 заказов; после W1-1 цена ниже, но каскад остаётся.
- Сделать:
  - заменить `sync.Mutex` владельца на мьютекс с `Lock(ctx) error` (канал ёмкости 1 с `select` по `ctx.Done()`), все методы `Owner` и `Planning` (`owner.go`, `catalog.go`, `delivery.go`, `orders.go`, `expiration.go`, `analytics.go:58`, `planning.go`) переходят на него; ожидающий, чей контекст истёк, уходит без побочных эффектов, ошибка отображается теми же публичными кодами, что и сейчас;
  - `usable=false` только при несогласованности журнала и состояния, не при `context` ошибке (проверка `isContextErr` перед установкой в каждом месте списка `grep "usable = false"`);
  - `recoverLocked` собирает кандидата в локальных переменных и публикует его только при успехе; при ошибке прежняя проекция и `usable` не меняются;
  - `WriteTimeout` сервера = `requestTimeout + 5s` (`cmd/server/main.go:90-97`), чтобы записанный результат не терялся на ответе.
- Красные тесты (перенос `wh-exp/zz_review_cancel_test.go` с детерминированным барьером вместо сна): `TestCanceledWaiterKeepsProjection` (следующий запрос не читает журнал с нуля), `TestCanceledVersionReadKeepsProjection`, `TestCanceledRecoveryKeepsPreviousProjection`, `TestWaiterLeavesOnDeadline`, `TestServerWriteTimeoutExceedsRequestTimeout`.

**W1-4. Повторная регистрация с другими настройками (С5, `bootstrap/registry.go:82-84`).**

- Правило: тот же `lifecycleVersion`; настройки выше по `SettingsVersion` заменяют сохранённые, инвентарь сохраняется; ниже — успешный повтор без изменений; тот же номер с другим поясом — `CommandIdentityConflict` (в волне 2 код меняется на `CommandConflict`, C29). `operationID` ведёт себя как сейчас.
- Красные тесты: три случая выше + «потерянный ответ, Auth повторяет операцию с новыми настройками организации» (сценарий ревью). Готово: после повторов остатки и журнал неизменны.

**W1-5. JWKS (С4, `authkeys/client.go:54-98`).** Обновление выполняется один раз на всех (single-flight на собственном канале; пакет `x/sync` не добавлять, `.go-arch-lint.yml` его не разрешает) с отвязанным контекстом (`context.WithoutCancel` + таймаут клиента), ожидающие ждут результат по своему контексту, сетевой вызов не под `c.mu`; правило README «неудачное обновление не отдаёт устаревшие ключи» сохраняется. Красные: `TestCanceledInitiatorDoesNotPoisonRefresh`, `TestConcurrentKeyCallsOneRefresh`, `TestRefreshDoesNotHoldCacheLock`.

**W1-6. Режим API без умолчания (С6, `cmd/server/config.go:26`).** Пустой `WAREHOUSE_API_MODE` — ошибка запуска с понятным текстом; `legacy` включается только явным значением. Красный: `TestLoadConfigRequiresExplicitAPIMode`; обновить `TestLoadConfigDefaultsAndPort`. Коммит с `!`, запись в CHANGELOG. Порядок слияния (C35): factory в своей волне 1 задаёт `WAREHOUSE_API_MODE=v2` в durable-профиле и явный `legacy` в legacy-профилях (`compose.yaml`, `compose.dev.yaml`); W1-6 сливается после этого. Отдельный CR не нужен.

**W1-7. Мелочи ревью.**

- Н2: кеш `*time.Location` по имени в `zonedata.Load` (`zonedata/data.go:15-41`); красный — бенчмарк аллокаций повторного `Load` ≤ 1 и тест под `-race`.
- Н5: `Offer` не клонирует до проверки необходимости и клонирует один раз (`snapshots.go:42-43`, `:186-197`); ошибка сохранения снапшота одного ресторана не останавливает workers остальных (`registry.go:274-280`), повтор с выдержкой на ресторан; красный — `TestSnapshotSaveFailureIsolatedPerRestaurant`.
- Н4: тест фиксирует нулевой допуск на `iat` (R12).

**W1-8. Общий контракт хранилищ (подготовка к волне 2).** Пакет `tests/storecontract` с `RunEventStore(t, fixture)` и `RunSnapshotStore(t, fixture)`; фикстура: `New(t) Stores` плюс `Other(t, Stores) Stores` (второй ресторан на том же бэкенде для проверки изоляции) и `Close`. Кейсы:

- повтор команды по `commandID` и отпечатку, конфликт при другом типе или отпечатке;
- CAS по `expectedVersion`, повтор с устаревшей версией возвращает прежний результат;
- атомарность многособытийного batch и непрерывность номеров;
- уникальность `eventID`, `proposalID`, `orderID`, `deliveryID`;
- границы `ReadBatches`/`ScanBatches` (не на границе batch — `ErrInvalidRecordedBatch`);
- `ReadEvents`, `ReadPeriod` (граница периода включительно/исключительно, `UpToSequence`);
- `FindCommand`, `FindOrderByProposal`, `FindOrderPlacement`;
- отмена контекста до commit не оставляет следов;
- изоляция двух ресторанов (одинаковые `commandID` независимы);
- снапшот: монотонность, отказ невалидному кандидату без замены предыдущего, отмена до commit.

Вызывается для `memory` из `tests/` (обычный `make check`). Существующие два файла контрактных тестов переезжают в пакет.

**W1-9. Ключи сервисов файлами (C14).** Для `AUTH_SERVICE_KEY`, `AUTH_RUNTIME_SERVICE_KEY`, `GATEWAY_SERVICE_KEY`, `SERVING_SERVICE_KEY` (`cmd/server/config.go`) добавить варианты `<NAME>_FILE`: значение читается из файла без перевода строки; заданы и переменная, и файл — ошибка запуска (как `LoadDSN`); значение не попадает в текст ошибок и логи. Прежние переменные остаются для legacy и локальных запусков. Красные: `TestServiceKeyFromFile`, `TestServiceKeyBothSetIsError`, `TestServiceKeyNotInErrors`; README и CHANGELOG.

Порядок и размер: W1-8 → W1-1/W1-2 (самые крупные) → W1-3 → W1-4…W1-7, W1-9. Запись в CHANGELOG «Unreleased» по каждой правке. `make integration` в волне 1 не требуется: цели в репозитории нет, она появляется в волне 2 (C37).

## 5. Волна 2 — хранение

Старт после G1, DUR1 и готового эталона R15. Все решения ниже, кроме отмеченных DUR1, не требуют новых соглашений. Раздел 10 главной спеки (C1–C40) уже учтён.

### 5.1 Выбор модели: журнал + горячий кеш с догоном хвоста

Выбрано: журнал и снапшоты в Postgres; проекция в памяти — ограниченный LRU по ресторанам с догоном хвоста по версии потока. Проекционных таблиц нет.

Обоснование:

- Event sourcing сохранён решением business-core. Вся логика FEFO, оценки меню, редьюсер и инварианты написаны над `InventoryState`; проекционные таблицы дублировали бы их в SQL и расходились бы с редьюсером, а запись каждой команды стала бы записью в нормализованные таблицы вместо одной вставки.
- После W1-1 горячее состояние = справочники + партии (без истории заказов и команд), единицы MiB на ресторан; снапшот + хвост ≤ `EVENTS_PER_SNAPSHOT` восстанавливается за миллисекунды. Холодный ресторан дёшев, поэтому LRU не опасен при цели 100×100 = 10 000 ресторанов, из которых одновременно активна малая доля.
- Две реплики: единственный писатель — не мьютекс процесса, а CAS по версии потока в транзакции. Проигравшая реплика получает `ErrConcurrentModification`, догоняет хвост (`syncProjectionLocked` уже умеет) и повторяет; повторы команд идемпотентны по `commandID`. Распределённой блокировки на ресторан нет.

Правила кеша (`bootstrap`, новый `RuntimeCache`):

- Ёмкость: `WAREHOUSE_HOT_RESTAURANTS` (старт 128) и `WAREHOUSE_HOT_BYTES` (старт 256 MiB; оценка байт по числу партий/справочников, константы подбираются замером heap на seed). Вытеснение только ресторана без запросов в полёте; перед вытеснением, если после последнего снапшота набралось ≥ порога, снапшот предлагается и сохраняется (ограниченно по времени); `pending` не переносится (повтор разрешит его по журналу).
- Загрузка холодного ресторана: single-flight на ресторан, на отвязанном от запроса контексте с таймаутом 30 с (отмена одного запроса не срывает загрузку для остальных), общий семафор `WAREHOUSE_LOAD_CONCURRENCY` (4), чтобы старт после рестарта не вызвал лавину. Источник: последний валидный снапшот + `ScanBatches(after)`; порча снапшота — следующий по возрасту, затем журнал с нуля, запись в логе, остатки не обнуляются.
- Каждый запрос выполняет одну проверку «голова» (`Head`): версия потока + состояние жизненного цикла и generation ресторана одним запросом. Версия БД больше горячей — догон хвоста; меньше — сброс и перезагрузка. Состояние `closed` — `ScopeDeleted` (так вторая реплика не обслужит удалённый ресторан по устаревшему кешу). Несовпадение generation процесса с БД — отказ готовности и завершение (быстрый fence).
- Повторы CAS ограничены (8 попыток с джиттером), затем публичная ошибка неизвестности; число конфликтов CAS пишется в лог.
- Активность по ресторанам: счётчики `hot_restaurants`, `hot_bytes_estimate`, `projection_loads{source}`, `tail_events` пишутся периодической строкой в лог; отдельного эндпоинта метрик нет (C33).

### 5.2 Схема (отдельная база сервиса, роли `umir_warehouse_owner` и `umir_warehouse`)

Роли и права создаёт factory (C8): `umir_warehouse_owner` владеет таблицами и нужна только заданию `migrate-warehouse`, `umir_warehouse` — роль среды выполнения. Таблицы без префикса. `restaurant_id` и `organization_id` типа `uuid`. Миграции только вперёд, подкоманда `migrate` под ролью-владельцем; сам себя сервис не мигрирует: при старте `serve` вызывает `AssertRuntimeRole` (роль не суперпользователь и не владелец) и `VerifySchema`, при несовпадении версии схемы не готов.

```sql
storage_meta(singleton bool PK DEFAULT true CHECK (singleton), generation uuid NOT NULL DEFAULT gen_random_uuid(),
             created_at timestamptz NOT NULL DEFAULT now(), reason text NOT NULL)  -- одна строка, вставляется первой миграцией (C9)
generation_history(generation uuid PK, previous uuid, rotated_at timestamptz, reason text)
restaurants(restaurant_id uuid PK, organization_id uuid NOT NULL, lifecycle_version bigint NOT NULL,
            time_zone text NOT NULL, settings_version bigint NOT NULL,
            state text NOT NULL CHECK (state IN ('active','closed')),
            register_operation_id uuid, close_operation_id uuid,
            coverage_from timestamptz NOT NULL, registered_at timestamptz NOT NULL, closed_at timestamptz)
  -- INDEX (organization_id); INDEX (closed_at) WHERE state='closed'
streams(restaurant_id uuid PK REFERENCES restaurants, version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
        next_due_at timestamptz, updated_at timestamptz NOT NULL)
  -- INDEX (next_due_at) WHERE next_due_at IS NOT NULL                              -- R6
commands(restaurant_id uuid, command_id text, command_type text NOT NULL, fingerprint text NOT NULL,
         first_sequence bigint NOT NULL, final_sequence bigint NOT NULL, result jsonb NOT NULL,
         recorded_at timestamptz NOT NULL, PRIMARY KEY (restaurant_id, command_id),
         UNIQUE (restaurant_id, final_sequence)) PARTITION BY HASH (restaurant_id)  -- 16 секций
events(restaurant_id uuid, sequence bigint, event_id text NOT NULL, command_id text NOT NULL,
       event_type text NOT NULL, schema_version smallint NOT NULL, recorded_at timestamptz NOT NULL,
       proposal_id text, order_id text, delivery_id text, payload jsonb NOT NULL,
       PRIMARY KEY (restaurant_id, sequence), UNIQUE (restaurant_id, event_id),
       FOREIGN KEY (restaurant_id, command_id) REFERENCES commands) PARTITION BY HASH (restaurant_id)
  -- UNIQUE (restaurant_id, proposal_id) WHERE proposal_id IS NOT NULL; то же для order_id, delivery_id
  -- INDEX (restaurant_id, recorded_at, sequence)                                    -- ReadPeriod
snapshots(restaurant_id uuid, last_sequence bigint, schema_version smallint NOT NULL, state jsonb NOT NULL,
          state_sha256 bytea NOT NULL, created_at timestamptz NOT NULL, PRIMARY KEY (restaurant_id, last_sequence))
order_attempts(restaurant_id uuid, command_id text, proposal_id text NOT NULL, request_digest bytea NOT NULL,
               status text NOT NULL CHECK (status IN ('executing','interrupted','rejected','recheck','completed')),
               executor_instance text, lease_until timestamptz, result jsonb, failure_code text,
               created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
               PRIMARY KEY (restaurant_id, command_id))   -- INDEX (updated_at) WHERE status <> 'executing'
outbox(id uuid PK, position bigserial UNIQUE, exchange text, routing_key text, headers jsonb, body jsonb,
       org_id uuid NULL, restaurant_id uuid NULL, created_at timestamptz, not_before timestamptz,
       published_at timestamptz, routed bool, attempts int, claimed_by text, claim_fence bigint,
       claim_expires_at timestamptz)                    -- форма эталона целиком, своих столбцов нет (C4)
worker_leases(name text PK, holder text, fence bigint, expires_at timestamptz)   -- таблица эталона (C4)
  -- индексы outbox (неопубликованные по position; published_at для очистки) — из фрагмента SQL эталона
```

Пояснения:

- Время `timestamptz` хранит микросекунды, точное значение (наносекунды, смещение +03:00) лежит в `payload`; `ReadPeriod` выбирает с запасом 1 мс и добивает точный фильтр в Go. Количества и время в `payload`/`result`/`state` кодируются отдельным кодеком (десятичные строки, RFC3339Nano `+03:00`), не `json`-тегами доменных структур.
- `commands.result` хранит `CommandRecord.Result`, поэтому `FindCommand` не собирает результат из событий. `final_sequence` UNIQUE даёт проверку границы batch для `ReadBatches(after)`.
- `completed` в `order_attempts` пишется только когда квитанции в `commands` нет (дедупликация по `proposalID` с новым `commandID`, С1); для обычного успеха строка `executing` удаляется после commit, остаток подчищает retention. Так таблица не растёт на заказ.
- `outbox` и `worker_leases` — формы эталона (C4). `outbox.id` — uuid строки; `exchange = 'umir.events'`; `body` — envelope (5.6), `eventID` в теле равен `events.event_id` (текст, стабилен при любых повторах публикации); `org_id`/`restaurant_id` заполнены. Уникальность события держит `UNIQUE (restaurant_id, event_id)` журнала, а строка outbox пишется в той же транзакции, поэтому отдельный ключ по событию не нужен.
- Секции (16 по хэшу ресторана) для `events` и `commands` заложены сразу: журнал по R9 без срока, при ориентире 100 заказов в день на ресторан это порядка 10⁶ заказов в сутки на ячейку (оценка ведущему подтвердить на seed, вопрос Q5).
- RLS: `ENABLE` + `FORCE ROW LEVEL SECURITY` на всех таблицах; политики пишутся SQL-хелпером эталона (`RLSPolicySQL`), контекст транзакции — эталонный `Scope` (C5): для обычных путей `umir.actor = 'tenant'`, `restaurant_id = umir.restaurant_id`, для `restaurants` дополнительно `organization_id = umir.org_id`. Служебные задания без ресторана (релей outbox, очистка, выдача сроков, перечисление) работают в `InSystemTx(ctx, reason)` (`umir.actor = 'system'`); обычные пути её не используют. Своих GUC (`umir.system` и подобных) нет. Права `umir_warehouse`: на `events` и `commands` только `SELECT`/`INSERT`; физическое удаление данных закрытого ресторана — `SECURITY DEFINER` функция `purge_restaurant(uuid)`, владелец `umir_warehouse_owner`; она проверяет `closed` и срок (5.10).

### 5.3 Порты и адаптеры

| Порт / роль | Реализация (`internal/adapters/postgres`) |
| --- | --- |
| `application.EventStore` | `EventStore`, привязан к ресторану в конструкторе `NewRestaurantStores(db, scope)`; порт не меняет сигнатур по `restaurant_id` (опись stalled). Методы W1-2 включены |
| `application.SnapshotStore` | `SnapshotStore`, тот же конструктор; `Save` проверяет через `domain.ValidateSnapshot`, пишет `ON CONFLICT DO NOTHING`, оставляет 3 последних |
| реестр ресторанов, generation | `Directory` (`restaurants`, `streams`, `storage_meta`), вызывается из `bootstrap.Registry` |
| координатор попыток | `AttemptStore` (`order_attempts`), использует `OrderAttemptCoordinator` вместо карт в памяти |
| outbox | запись внутри `AppendBatch` через `Outbox.AddEvent` эталона; чтение и публикация — `Relay` эталона (`Claim`/`MarkPublished(fence)`) |
| захваты страниц | Redis (5.8) |

`bootstrap.Options` получает `StoreFactory` (память или Postgres); `runtime.go:54` перестаёт создавать `memory.NewEventStore()` напрямую. `Registry.tenants` становится ограниченным кешем строк `restaurants` + `RuntimeCache`.

### 5.4 Транзакционные границы

- **Команда** (`AppendBatch`, одна транзакция `READ COMMITTED`, `InTx` с повтором 40001/40P01):
  1. найти `commands` по `commandID`: есть — вернуть повтор или конфликт отпечатка;
  2. `UPDATE streams ... FROM restaurants WHERE state='active' AND version=$expected` (0 строк = различить закрытие `ScopeDeleted` и `ErrConcurrentModification`);
  3. вставить `commands`, `events` (непрерывные номера), `outbox` (по строке на событие), записать `streams.version` и `next_due_at` из подсказки владельца (`CommandBatch.NextDueAt`, минимум индекса сроков после batch; владелец считает его при пробном применении, которое уже выполняется перед записью);
  4. уникальные нарушения `event_id`/`proposal_id`/`order_id`/`delivery_id` отображаются теми же доменными ошибками, что и память.
  Ошибка связи при COMMIT — `ErrUnknownWriteOutcome` (разрешает `resolvePendingLocked` по тому же `commandID`). Отмена контекста клиента не отменяет уже начатый COMMIT на сервере.
- **Регистрация/закрытие**: одна транзакция на `restaurants` + `streams`; `close` необратим (tombstone), повторная регистрация закрытого — `ScopeDeleted` навсегда, в т. ч. после смены generation (Auth — источник истины и закрывает свои удаляемые ID, README). Настройки обновляются по W1-4.
- **Попытка заказа**: отдельная короткая транзакция до выполнения (вставка `executing` с `lease_until = now + REQUEST_TIMEOUT + 5s`, или перехват `interrupted`/просроченной `executing`); результат пишется после. Перехват безопасен без fencing: повторная команда с тем же `commandID` идемпотентна в журнале.
- **Снапшот**: вставка + обрезка в одной транзакции. **Релей**: публикация вне транзакции БД, отметка `Outbox.MarkPublished(owner, fence, ids)` эталона (условный `UPDATE` по `claim_fence`, `ErrLeaseLost` при чужом fence).

### 5.5 Generation, реестр, экземпляр (R2)

- Generation создаёт первая миграция: строка `storage_meta` с `gen_random_uuid()` (C9); дальше она не меняется. `serve` без generation не стартует и ничего не создаёт. Меняет её только `server generation rotate --reason <text>` — при остановленном сервисе и под ролью-владельцем (`WAREHOUSE_MIGRATE_DATABASE_URL_FILE`); старая generation записывается в `generation_history`. Команду вызывает runbook восстановления factory при откате базы к более ранней истории; автоматического обнаружения нет, ротация остаётся ручным шагом runbook. Старые токены/курсоры получают `RuntimeReset` как сейчас.
- `GET /internal/warehouse/v2/runtime` отдаёт `epoch` = generation; `Registry.Epoch()` читает её из БД при старте. Рестарт процесса не требует повторной регистрации: `Resolve` поднимает ресторан из БД лениво.
- `instanceID` (`WAREHOUSE_INSTANCE_ID`, уникальный на реплику, задаёт factory, C19; без значения — случайный) — только `/readyz` и логи; им же подписывается `executor_instance` и держатель аренды.
- Реестр и tombstone в БД: удаление данных закрытого ресторана — `purge_restaurant` через 7 дней после `closed_at` (C23); строка ресторана-надгробие остаётся без срока. Операция закрытия не несёт собственного `closedAt`, поэтому отсчёт идёт от `restaurants.closed_at`, который Warehouse записывает при приёме `DELETE registry` от Auth.

### 5.6 Outbox и сообщения (`umir.events`, R4)

- Одна строка на событие в той же транзакции. Ключ маршрутизации `warehouse.<eventType>` (типы журнала: `ProductRegistered`, `DishDefined`, `DeliveryReceived`, `OrderPlaced`, `BatchExpiryPending`, `BatchWriteoffConfirmed`, `BatchExpired`). Warehouse — единственный производитель `umir.events` (C38); Gateway привязывается только к `warehouse.#`.
- Envelope по каталогу: `eventID`, `owner=warehouse`, `schemaVersion` (версия envelope = 1), `orgID`, `restaurantID`, `aggregateID=restaurantID`, `aggregateRevision`, `generation`, `occurredAt=recordedAt`, `correlationID` (идентификатор запроса, если есть), `causationID=commandID`, `topics` (обязательно для `warehouse.*`). `aggregateRevision` = `sequence` журнала, то есть версия потока ресторана: подряд в пределах generation, разрыв обнаруживает потребитель (C30). Тела без данных заказа и количеств: только envelope.
- Отображение событие → `topics`: `ProductRegistered`, `DishDefined` → `catalog`, `menu`; `DeliveryReceived` → `stock`, `menu`, `analytics`; `OrderPlaced` → `stock`, `menu`, `orders`, `analytics`; `BatchExpiryPending`, `BatchExpired` → `stock`, `menu`, `analytics`; `BatchWriteoffConfirmed` → `stock`, `analytics`. Warehouse кладёт этот узкий набор в событие, но Gateway пока рассылает клиентам все пять тем, как сейчас; узкий набор включается после теста Gateway и Frontend «каждая мутация обновляет нужные экраны» (C39). Перечень типов и тем Warehouse передаётся G-factory для `events.md`.
- Релей — эталонный `relay`: аренда `outbox-relay` в `worker_leases` с fence (один активный релей на пару реплик), пачки `WAREHOUSE_OUTBOX_BATCH` (100) по `position` через `Outbox.Claim`/`MarkPublished(fence)`, persistent, publisher confirms, `mandatory`; отметка только после положительного confirm; `Nacked`/`Unknown` — повтор с тем же `eventID`. Возврат `mandatory` для `umir.events` — политика **accept** (C3): строка помечается опубликованной с `routed=false`, счётчик возвратов виден в логе релея и в SQL (число строк с `routed=false`). Это не сбой: у обменника только fan-out потребители (exclusive-очереди Gateway), при остановленном Gateway привязок нет, а живые сигналы не журнал — Gateway при подключении обновляет состояние сам. Политика retry для `umir.events` не включается.
- Обменник `umir.events` (topic, durable) объявляет задание `mq-init` (C2); Warehouse проверяет его пассивно при старте и перед релеем и очередей не объявляет. Отсутствие обменника или расхождение параметров — возможность `outbox` не `ok`. Недоступность брокера не блокирует и не откатывает команду, растёт лаг outbox (возраст старейшей неопубликованной строки: `Stats()` релея и SQL приёмки, C33).
- Inbox и потребители: **не используются** (Warehouse ничего не потребляет).

### 5.7 Расписание сроков для Gateway (R6, контракт DUR1)

`streams.next_due_at` ведётся командой (5.4). По DUR1 (C22, C11) Warehouse выдаёт: (а) `GET /internal/warehouse/v2/expiry-due?asOf&limit&after` — страница ресторанов с `next_due_at <= asOf` по индексу, постранично по `after`; токен `expiry.due` уровня ячейки, без привязки к ресторану; (б) поле `nextDueAt` в ответе `POST expiry-runs`. Gateway использует (а) как основной путь, чтобы не опрашивать каждый ресторан, (б) — как подсказку. Ленивая просрочка при чтении (`prepareLocked`) остаётся и корректна для двух реплик: детерминированные `expiration/...`-команды дают одно `BatchExpiryPending`, остальные получают повтор.

### 5.8 Роль Redis

Redis cache (U3) хранит только перестраиваемое: курсоры и захваты страниц product-batches и analytics (`product_batches.go`, `analytics.go`), TTL 5 мин. Сейчас cache-Redis нужен только Warehouse и только для захватов (C27). Ключ `umir:wh:cap:{generation}:{restaurantID}:{captureID}` (C17; ACL Warehouse — только на `umir:wh:*`), значение — сжатый сериализованный отчёт; число слотов на ресторан (128) и размер (16 MiB на ресторан, 10 000 строк) проверяет Lua-скрипт Warehouse под тем же префиксом (эталонный `Slots` — счётные аренды без учёта размера; если хватает счёта, берётся он, а размер проверяется отдельно); общий предел памяти даёт `maxmemory`/`allkeys-lru`, который factory задаёт как общий бюджет захватов Warehouse (значение захвата до 16 MiB). Промах или вытеснение = `RefreshRequired` (существующая семантика курсора). Redis недоступен: первая страница отчёта и product-batches отвечает `503 ServiceUnavailable` с `outcome: unavailable` (чтение, до отправки; C16), команды и остатки работают, возможность `captures` не `ok`, готовность не меняется. Таймеры `time.AfterFunc` и локальные карты удаляются. Остатки, аллокации, квитанции и сроки в Redis не хранятся. Кеш чтений (меню, рекомендации, `/access`) добавляется по замерам R13, решение на G2; до замеров его в коде нет. Лимитера у Warehouse нет, маркер `umir:limiter:generation` ему не нужен. Решает Н1 (общий бюджет вместо лимита «на ресторан»).

### 5.9 Конфигурация и готовность

Контракт переменных — `docs/durable/configuration.md` (factory, волна 0). Переменной `WAREHOUSE_STORAGE` и режима `memory` нет (C10).

| Переменная | Смысл |
| --- | --- |
| `WAREHOUSE_API_MODE` | `v2` или явный `legacy`; пусто — отказ (W1-6). Задаёт factory (C35). `legacy` с Postgres запрещён |
| `WAREHOUSE_DATABASE_URL_FILE` (`..._URL` для локальной отладки) | DSN роли `umir_warehouse`; разбор — эталонный `LoadDSN`: заданы и файл, и переменная — ошибка (поведение «файл побеждает» убирается). Без DSN `serve` не стартует |
| `WAREHOUSE_MIGRATE_DATABASE_URL_FILE` | DSN роли `umir_warehouse_owner`; только для заданий `migrate` и `generation rotate` (C8), в среде выполнения не задаётся |
| `AUTH_SERVICE_KEY_FILE`, `AUTH_RUNTIME_SERVICE_KEY_FILE`, `GATEWAY_SERVICE_KEY_FILE`, `SERVING_SERVICE_KEY_FILE` | ключи сервисов файлами (W1-9, C14) |
| `WAREHOUSE_AMQP_URL_FILE`, `WAREHOUSE_REDIS_CACHE_URL_FILE` | брокер (vhost внутри URL) и Redis cache |
| `WAREHOUSE_INSTANCE_ID` | идентификатор экземпляра, задаёт factory |
| `WAREHOUSE_HOT_RESTAURANTS`, `WAREHOUSE_HOT_BYTES`, `WAREHOUSE_LOAD_CONCURRENCY` | границы кеша (5.1) |
| `EVENTS_PER_SNAPSHOT` | существующая; при Postgres по умолчанию 500 |
| `WAREHOUSE_OUTBOX_BATCH`, `WAREHOUSE_RETENTION_INTERVAL` | релей (100) и очистка (10 мин) |

Подкоманды бинарника (C9): `migrate`, `serve`, `generation rotate --reason <текст>`, а также существующая `healthcheck`. Memory-адаптеры в режиме `v2` используются только тестовым кодом; режим `legacy` (in-memory v1) остаётся в бинарнике до удаления в волне 3 (C28) и в durable-профиль не попадает. Проверка «в проде нет memory» — тест сборки: `serve` режима `v2` без DSN не стартует.

Размер пула — параметры DSN (`pool_max_conns`); `statement_timeout` ≤ `REQUEST_TIMEOUT`. Секреты не логируются (URL без эха при ошибке). Здоровье (C32): `GET /healthz` — процесс жив; `GET /readyz` — основная возможность готова, тело `{instance, generation, capabilities: {<имя>: ok|degraded|down}}`, подпутей `/readyz/*` нет; healthcheck compose и подкоманда `healthcheck` смотрят `/readyz`. Возможности: `journal` (БД доступна, версия схемы совпадает, generation загружена) — единственное условие готовности `/readyz` и ответа `runtime`; `outbox` (брокер, лаг) и `captures` (Redis) могут быть `degraded` без влияния на готовность. Имена возможностей перечислены в `configuration.md`. Метрик-эндпоинта нет (C33): приёмка читает SQL (возраст outbox и attempts), management API RabbitMQ и логи.

### 5.10 Фоновые работы и очистка (аренды в Postgres)

Все работы — на `worker_leases` (таблица эталона, C4) с fencing (аренда ≈ 3 интервала, продление до половины срока, потерянная аренда останавливает работу сразу), а не на advisory lock живого соединения. Две реплики: релей и очистка идут на одной (держателе), снапшоты — на той, что держит горячий ресторан (конфликт безвреден: `ON CONFLICT DO NOTHING`).

- `outbox-relay` (5.6).
- `retention` (R9), пачками по 1000 строк, раз в `WAREHOUSE_RETENTION_INTERVAL`:
  - `outbox` опубликованное — 7 дней;
  - квитанции попыток `order_attempts` со статусами `rejected`, `recheck`, `interrupted` — 35 дней (C25: Serving прекращает автодоводку через 30 дней, запас 5 дней); строки `executing`, у которых уже есть квитанция в `commands`, — служебная уборка за сутки;
  - данные закрытого ресторана — через 7 дней после `closed_at` (C23), строка-надгробие ресторана остаётся без срока;
  - снапшоты — последние 3.
  Журнал и квитанции команд действующего ресторана без срока (рост журнала выносится на G1).

### 5.11 FIN-04 (контракт DUR1) и удаляемый код

Статусы попытки в БД: `executing` (жива аренда; на проводе `Pending`), `interrupted` (аренда истекла, исполнитель исчез; на проводе `Interrupted`, C21), `rejected`, `completed`, плюс `Unseen`. Переход на проводе: `Pending` → `Interrupted` (выполнение прекратилось без записанного исхода); `Interrupted` разрешает точный повтор commit той же командой, новый `commandID` не нужен. Поиск попытки по `commandID`: сперва квитанция в `commands` (побеждает), затем строка попытки. Переходы описывает DUR1.

Сервисные токены (C11): Warehouse принимает `expiry.due` (уровень ячейки, без ресторана, только список сроков 5.7) и `order.resolve` (Serving → Warehouse: один ресторан, generation склада, только пассивные `GET /order-attempts` и `GET /commands`; остальные маршруты с этим токеном закрыты). Повтор commit остаётся за клиентом.

`CommandConflict` (C29): в волне 2 через DUR1 приватный контракт и внутренние ответы Warehouse заменяют `CommandIdentityConflict` на `CommandConflict` (отображение в `httpapi/scoped.go` уже даёт этот код наружу). Регрессию на границе (enum ошибок scoped) ведёт Gateway.

Удаляется: `memory.EventStore`/`SnapshotStore` как продовые зависимости (остаются для тестов и фикстур), `time.AfterFunc` и карты захватов, процессная эпоха `newEpoch`, процессные карты `attempts`/`other`, заготовка схемы `runtime_state`, `ReadAnalyticsJournal`, неиспользуемое чтение `WAREHOUSE_AMQP_URL`, `WAREHOUSE_STORAGE` и `WAREHOUSE_ALLOW_MEMORY` заготовки. Режим `legacy`: в волне 2 с Postgres запрещён; полное удаление — отдельным PR волны 3, после того как factory выведет legacy-профили из CI и README (C28).

## 6. Интерфейсы с другими группами

**Выдаём:**

| Кому | Что | Основание |
| --- | --- | --- |
| Auth | Стабильный `epoch` (generation) в `GET .../runtime`, регистрация идемпотентна и допускает новые настройки (W1-4); рестарт Warehouse не требует повторной регистрации | R2, С5, DUR1 |
| Gateway | Сообщения `umir.events` (5.6, `aggregateRevision` подряд в пределах generation), `GET /internal/warehouse/v2/expiry-due` и `nextDueAt` в `expiry-runs` для планировщика (токен `expiry.due`), прежний HTTP; локальный `Append` после проксированного ответа Gateway убирает сам. Узкий набор `topics` в событии; Gateway пока рассылает все пять | R4, R6, C22, C39 |
| Serving | Состояния попытки `Pending` / `Interrupted` и точный повтор исходного commit; приём токена `order.resolve` (пассивные `/order-attempts`, `/commands`); все ответы по `commandID` после рестарта; новый `commandID` для оформленного предложения даёт 200 с тем же `orderID` (W1-2); `CommandConflict` вместо `CommandIdentityConflict` в волне 2; квитанции попыток хранятся 35 дней | FIN-04, C11, C21, C25, C29 |
| Factory | Бинарник с подкомандами `migrate`, `serve`, `generation rotate`, `healthcheck`; `/healthz` и `/readyz` с возможностями `journal`, `outbox`, `captures`; перечень секретов (ключи сервисов `*_FILE`), срок 35 дней для квитанций попыток, 7 дней для закрытого ресторана, перечень типов и тем событий Warehouse для `events.md` | R8, R9, C9, C14, C32 |

**Потребляем:**

| От кого | Что |
| --- | --- |
| Factory, волна 0 | DUR1 (R2, `Interrupted`, `expiry-due`/`nextDueAt`, токены `expiry.due` и `order.resolve`, `CommandConflict`); `events.md` (обменник `umir.events`, конверт, политика accept для mandatory-возврата); таблицы сроков и бюджетов раздела 6; эталон `reference/durable` (pg, `LoadDSN`, миграции, `worker_leases`, outbox/inbox, `Scope`/`InSystemTx`, релей, AMQP, Redis); `configuration.md`; `scripts/durable-test-infra.sh` и переменные `UMIR_TEST_*` |
| Factory, волна 1 | `WAREHOUSE_API_MODE=v2` в durable-профиле и явный `legacy` в legacy-профилях (C35); W1-6 сливается после этого |
| Factory, волна 3 | Роли `umir_warehouse_owner`/`umir_warehouse`, база, vhost, задание `mq-init` (обменник `umir.events`) и права, Redis cache с ACL на `umir:wh:*` и `maxmemory` под захваты (C27), секреты файлами, `WAREHOUSE_INSTANCE_ID` на реплику, одноразовое задание `migrate-warehouse`, runbook чистого листа и восстановления (в том числе `generation rotate`), нагрузочный генератор (R13), вывод legacy-профилей из CI и README (C28) |
| Auth | Продолжает `PUT/DELETE registry` по HTTP с `expectedEpoch` из `runtime` (R3/R5 касаются других сервисов); больше не вызывает Warehouse «сверкой всего реестра» после каждого рестарта |

**CR группы.** Все пять прежних CR закрыты решениями раздела 10: CR-W1 (форма ближайшего срока) — C22; CR-W2 (wire-форма статуса попытки) — C21; CR-W3 (типы и темы событий) — C1–C3, C30, C39, перечень типов и тем из 5.6 передаётся G-factory как данные для `events.md`; CR-W4 (`WAREHOUSE_API_MODE` в compose) — C35; CR-W5 (`generation rotate` в runbook) — C9. Открытых CR группа не ведёт.

## 7. Тесты и проверки

Команды: `GOWORK=off GOTOOLCHAIN=go1.26.9 make check`, `make quality-test`; `make integration` (тег `integration`, `-p 1 -race`) — только с волны 2: в волне 1 цели в репозитории нет, и волна 1 не зависит от тестовой инфраструктуры волны 0 (C37); `make tools` один раз в worktree.

- **Unit/race:** все тесты волны 1; `RuntimeCache` (вытеснение, single-flight загрузки, лимит байт, вытеснение не трогает ресторан в полёте); кодек журнала (круговой обход всех типов событий и схем 1 и 2, нанотакты и смещение +03:00); отображение ошибок Postgres (23505 по ограничениям, 40001, 40P01, 53xxx → недоступность без частичного состояния).
- **Контрактные тесты хранилищ:** `tests/storecontract` из W1-8 запускается для `memory` и для Postgres (`internal/adapters/postgres/store_integration_test.go`, тег `integration`). Для Postgres добавляются: `Other` = второй ресторан той же базы, роль `umir_warehouse` без `BYPASSRLS` и не владелец (`AssertRuntimeRole`), утечка контекста через пул (транзакция ресторана A, затем B на том же соединении без `SET`: ни одной строки), `FORCE RLS` на каждой таблице (отдельный запрос по `pg_class`).
- **Интеграционные (`integration`, переменные `UMIR_TEST_DATABASE_URL`, `UMIR_TEST_AMQP_URL`, `UMIR_TEST_REDIS_CACHE_URL`; временные база/vhost/префикс, без пропусков):**
  - миграции с нуля, повтор, отказ при схеме новее, конкурентный запуск двух процессов; первая миграция создаёт generation, `serve` без неё не стартует и ничего не создаёт; `LoadDSN`: заданы и файл, и переменная — ошибка; `serve` режима `v2` без DSN не стартует;
  - перезапуск: поставка, заказ, pending-просрочка, подтверждённое списание → после нового `Registry` и пустого кеша те же остатки, `StateVersion`, FEFO, payload истории, `CoverageFrom`, квитанции и generation; instance меняется;
  - две реплики (два `Registry` на одной базе): одновременные разные команды на ограниченный остаток без перепродажи, одна команда на обе — один эффект, ленивая просрочка на обеих — один `BatchExpiryPending`, закрытие на одной реплике останавливает другую на следующем запросе;
  - outbox: атомарность batch + квитанция + outbox (принудительный сбой посередине), конверт по каталогу, порядок позиций, релей с реальным RabbitMQ.
- **Сценарии отказов #17, которыми владеет группа** (тестовый прокси для обрыва TCP, остановка контейнеров брокера и БД с префиксом `umir-durable-`):
  - **F-WH-1** обрыв до COMMIT: следов нет, повтор исполняется один раз.
  - **F-WH-2** потерянный ответ COMMIT: неизвестный исход, повтор по `commandID` возвращает исходный результат, остатки списаны один раз.
  - **F-WH-3** падение после commit до ответа и до публикации: повтор — повтор результата; релей публикует позже.
  - **F-WH-4** публикация до/после confirm, неизвестный confirm: тот же `eventID`, нет потери; возврат `mandatory` на `umir.events` (Gateway остановлен): строка опубликована с `routed=false`, зависших строк нет (C3).
  - **F-WH-5** простой и рестарт брокера: команды идут, лаг растёт и гасится, порядок позиций сохранён.
  - **F-WH-6** простой и рестарт БД: команды отвечают неизвестным/недоступностью без частичного состояния, готовность падает и восстанавливается, горячие проекции догоняют хвост.
  - **F-WH-7** конкурирующие реплики (см. выше).
  - **F-WH-8** истёкшая аренда релея: старый релей не отмечает опубликованным (fencing), активных одновременно двое не бывает дольше окна аренды.
  - **F-WH-9** диск полон на стороне БД: ошибка класса 53 даёт недоступность без частичной записи (эмуляция ошибки в адаптере; сам сбой диска — на factory).
  - **F-WH-10** откат базы: после `generation rotate` старые токены и курсоры получают `RuntimeReset`, прежние команды не исполняются.
  - **F-WH-11** потеря кеша Redis: `RefreshRequired` на курсорах, команды и остатки без изменений.
  - **F-WH-12** дубликаты и перестановки при потребителе: тестовый потребитель выявляет разрыв `aggregateRevision` (подряд в пределах generation, C30; для Gateway — контракт сообщения).
- **Масштаб:** `tests/perf` из W1-1 на Postgres плюс заполнитель `tests/seed` (тег `scale`, пакетная вставка через `COPY` тем же кодеком) для N ресторанов × M заказов; по умолчанию N=200, M=2000 для CI, полный прогон (10 000 ресторанов) делает factory (R13) тем же заполнителем. Замеряются: p50/p95/p99 команды и чтения, загрузка холодного ресторана, heap кеша, пул БД, лаг outbox. Числовые бюджеты вносятся в отчёт и утверждаются на G1.

## 8. Приёмка группы

- [ ] Волна 1: W1-1…W1-8 выполнены, красные тесты были красными на `52aae49`, `make check`, `make quality-test` зелёные (`make integration` в волне 1 не требуется, C37); `*_FILE` для ключей сервисов (W1-9); таблица «до/после» для 1000/4000/8000 заказов приложена; рост восстановления и `Movements` линейный.
- [ ] `grep -rn "ReadBatches(ctx, 0)\|ReadAnalyticsJournal"` по продовому коду пуст, кроме тестов и явно оставленных мест; `InventoryState` без `Orders`/`Commands`.
- [ ] Отмена или дедлайн запроса не переводят проекцию в «непригодную» и не вызывают повторного восстановления.
- [ ] Контрактный набор хранилищ зелёный на `memory` и на Postgres; миграции на пустой базе, повторные, конкурентные.
- [ ] Рестарт: остатки, история, квитанции, FEFO, `CoverageFrom` восстановлены; generation не изменилась, instance изменился; повторной регистрации ресторанов нет.
- [ ] Две реплики на одной базе: нет перепродажи, одинаковые команды дают один эффект, закрытие виднее второй реплике на следующем запросе.
- [ ] Каждая транзакция команды атомарна (журнал + квитанция + outbox + `next_due_at`); потерянный COMMIT разрешается тем же `commandID`.
- [ ] RLS: рабочая роль `umir_warehouse` не владелец, контекст эталона (`Scope`, `InSystemTx`), `FORCE RLS` на всех таблицах, тест утечки через пул и тест межресторанной изоляции зелёные.
- [ ] Outbox: форма эталона без своих столбцов, конверт по каталогу, релей с арендой в `worker_leases`/fencing/confirms, mandatory-возврат принимается (`routed=false`), отказ брокера не блокирует команды, сообщения с тем же `eventID` при повторе, нет данных заказа и ПДн в телах.
- [ ] Горячая память ограничена `WAREHOUSE_HOT_*`; на сидированном стенде heap плато, а не рост с историей; загрузка холодного ресторана (снапшот + хвост) в бюджете; нет полного replay на старте процесса.
- [ ] Состояния `Pending`/`Interrupted`, `GET .../expiry-due` и `nextDueAt` с токеном `expiry.due`, токен `order.resolve` только на пассивные маршруты, `CommandConflict` — по принятому DUR1; квитанции попыток `rejected`/`recheck`/`interrupted` хранятся 35 дней; данные закрытого ресторана удаляются через 7 дней после `closed_at`.
- [ ] Redis только для захватов, ключи под `umir:wh:`; сценарий F-WH-11 зелёный; команды не зависят от Redis.
- [ ] Режима `memory` и переменных `WAREHOUSE_STORAGE`/`WAREHOUSE_ALLOW_MEMORY` нет, `serve` режима `v2` без DSN не стартует; `legacy` без явного значения отвергнут; DSN читается как эталонный `LoadDSN`; секреты читаются файлами, не логируются; `/readyz` отдаёт `{instance, generation, capabilities}`.
- [ ] Сценарии F-WH-1…F-WH-12 зелёные; для 7 и 8 есть повторяемые прогоны под `-race`.
- [ ] Старый код удалён (5.11), README и CHANGELOG описывают реальный профиль, переменные, команды `migrate`, `serve` и `generation rotate`, ограничения; отчёт волны со SHA, числами и открытыми CR; финальное ревью Opus — Ready.

## 9. Риски и вопросы к оркестратору

- **Q1.** Закрыто: C3 — возврат `mandatory` на `umir.events` принимается (`routed=false`, строка опубликована); C38 — Serving ничего не публикует, durable-очередь на `umir.events` ему не нужна.
- **Q2.** Закрыто: C39 — Warehouse кладёт узкий набор тем, Gateway пока рассылает все пять; C30 — `aggregateRevision` = версия потока.
- **Q3.** Закрыто: C23 — данные закрытого ресторана удаляются через 7 дней после `closed_at`, надгробие остаётся без срока.
- **Q4.** Закрыто: ответ раздела 10 — ротация generation остаётся ручным шагом runbook (C9).
- **Q5.** Закрыто: раздел 10 («Вопросы групп») и «Что решает пользователь на G1» — рост журнала без срока (порядка 10⁶ заказов и 2–3·10⁶ строк журнала и outbox в сутки при 100×100, сотни ГБ в год) выносится на G1; секции заложены, архива нет, размер строки замеряется на seed и вносится в отчёт.
- **Q6.** Закрыто: C22 — `GET /internal/warehouse/v2/expiry-due` и `nextDueAt`.
- **Q7.** Закрыто: C27 — cache-Redis нужен только захватам Warehouse, `maxmemory` задаёт factory.
- **Риск: порядок слияния W1-6.** Он ломает запуск образа без явного режима; сливается только после правки compose в волне 1 factory (C35). Риск: изменение формата снапшота (W1-1) и кодека (5.2) — обратимо, данных нет (R1).
- **Риск: оценка байт горячего кеша** грубая; предел — проверяется замером heap на seed и корректируется до G2.
