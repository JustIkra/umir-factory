# CR 01 (serving): коды ошибок Serving вне enum Error.code

Что: Serving отдаёт `ProposalSuperseded` (409, not_committed), `ProductNotFound`, `DishNotFound` (404, not_committed), когда их приносит клиент склада (`warehousehttp/client.go:597-616`, legacy-путь v1 и общий разбор), и `handler.go` пишет их в ответ как есть. В `components.schemas.Error.code` (`completion-v1-composed-private.json`, factory 80c2400) этих значений нет, поэтому Gateway по enum превратит ответ в 502/unknown (тот же дефект, что W1-1).
Зачем: W1-1 требует «каждый код ∈ enum»; три кода не покрыты подспекой, выбор замены — догадка.
Кого затрагивает: Serving, Gateway (enum ошибок scoped), factory (DUR1, enum).
Предложение: решить в DUR1 — либо добавить три кода в enum, либо Serving отображает их в существующие (`ProposalSuperseded` -> `ProposalNotFound`/`CommandConflict`?, `ProductNotFound`/`DishNotFound` -> `NotFound`/`ProductConflict`/`DishConflict`). Пока W1-1 фиксирует текущие значения тестом (`outside: true`), он упадёт при любом изменении.

## Дополнение после ревью T1

- Достижимость: `ProposalSuperseded` в публичном OpenAPI — вид результата `ConfirmResult` (`supersededBy`), а не код ошибки. Scoped-клиент склада (`warehousehttp/scoped.go`, `decodeScopedError`) на `ProposalSuperseded`/`ProductNotFound`/`DishNotFound` отвечает «unknown scoped warehouse error», поэтому в v2-пути эти коды, по коду, не возникают; они достижимы только через legacy/non-scoped клиент (v1), который Gateway v2 не видит. Таким образом CR касается только legacy-пути.
- Через `OperationError` legacy-клиента (`client.go:587,602,617`) наружу также проходят `ConcurrentModification`, `ProductAlreadyExists`, `DishAlreadyExists`, `DeliveryAlreadyReceived`, `InsufficientInventory`, `ConfirmationPending` (коды вне enum). То же ограничение: только legacy-путь.
- Предложение: подтвердить тестом (ScopedHandler + scoped-клиент), что v2-путь этих кодов не отдаёт, и считать пометку `outside: true` в таблице меткой v1-пути; legacy-код удаляется вместе с C28 (волна 3).
- Складской `InternalError` (outcome unknown, `warehousehttp/client.go:617-621`, ответ склада о неоднозначной записи) по W1-1 не переименовывается и тоже проходит наружу вне enum (legacy-путь). Если владелец enum решит, что он должен стать `TechnicalError`, правка — одна строка в `handler.go`.
