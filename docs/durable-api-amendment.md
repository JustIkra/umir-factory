# DUR1: контракт durable-стека `2.0.0-DUR.1`

DUR1 дополняет REG1 (`2.0.0-REG.1`). Он задан оверлеями [public](api/durable-v1-public.json) и [private](api/durable-v1-private.json). Проводным контрактом служат [составной public](api/durable-v1-composed-public.json), [составной private](api/durable-v1-composed-private.json) и [примеры](api/durable-v1-examples.json). Файлы C2, UA1, FIN1 и REG1 не меняются и сохраняют свои свидетельства. Решения C1–C53 главной спеки `docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md` главнее подспеки; подпункты ниже следуют W0.1 подспеки Factory.

Перегенерация и проверка:

```sh
node tests/http/tools/compose-durable-contract.mjs docs/api
make check-contract
```

Скрипт читает составные файлы REG1 и два оверлея, ничего не меняет во входах и пишет составные файлы DUR1 с `info.version = 2.0.0-DUR.1`, членом `x-durable-source` (SHA-256 всех четырёх входов) и членом `x-durable-policy` (числа, статусы и правила из этого документа в машиночитаемом виде). Список разрешённых изменений зашит в скрипт и в тест `durable_contract_test.go`; любое изменение списка — изменение контракта.

## Составные файлы и входы (SHA-256)

| Файл | SHA-256 |
| --- | --- |
| `docs/api/durable-v1-composed-public.json` | `fcd57afdc02427dae2533f1dcbf9efb87b3b55ccc4810a7c95a05b47faaf1f09` |
| `docs/api/durable-v1-composed-private.json` | `712a03199b9ab25f2a920f8bdb7b1a92931642fc6b64d7038e38524c30ce12d8` |
| `docs/api/durable-v1-public.json` (оверлей) | `7bbe7d1058e1903978d6dd00412592edbf3f699a12a283e5568a067cd9b0e306` |
| `docs/api/durable-v1-private.json` (оверлей) | `098928a89adb7d2e99c48ac81575dbb93238946649cd16e224b41054088a1030` |
| `docs/api/durable-v1-examples.json` | `a999c517444773059b380949e58afadd82c71705ac5337c3a77530d82747128b` |
| `docs/api/registration-v1-composed-public.json` (предшественник) | `2be37f544a1ababa92cf16a4652826281db7f9b0e3d64bf1b389be2af0e0c873` |
| `docs/api/registration-v1-composed-private.json` (предшественник) | `2d21db8cd51085196b8294615b7db2afd68b540fcd4cbf90d0e67e4fb50a45d3` |

Потребители (Auth `internal/adapters/httpapi/contract.json`, Gateway `internal/operatorhttp/auth-contract.json`, Frontend `api/backend-v2-public.json`) копируют составные файлы и сверяют их по этим SHA-256 (`umir-checks contract-sync`, отдельная задача). Если оверлей или скрипт меняется, пересчитайте таблицу.

## Что разрешено менять

Скрипт отказывает, если набор отличается от объявленного.

- Замена 16 схем: `AuthPageIdentity`, `DueExpiryResult`, `DurableResult`, `Epoch`, `Error`, `ID`, `IdempotencyResult`, `LiveEvent`, `PrepareOrderRequest`, `ReadinessAck`, `Registry`, `RuntimeReadiness`, `RuntimeStatus`, `ServiceAuthorizationRequest`, `ServiceClaims`, `WOrderAttempt`. Оба профиля получают одинаковые замены.
- Пять новых схем: `ExpiryDueList`, `ExpiryDueRestaurant`, `HealthReadiness`, `RegistryPoint`, `RegistryTombstone` (без коллизий с предшественником).
- Четыре новые операции только в private: `GET /internal/auth/v1/registry/{restaurantID}` (`registryPoint`), `GET /internal/warehouse/v2/expiry-due` (`expiryDue`), `GET /healthz` (`healthz`), `GET /readyz` (`readyz`). Совпадений `operationId` и путей с существующими нет.
- Правка полей семи существующих операций (`x-operation-patches`): у `GET /internal/auth/v1/registry` — `description`, `parameters`, `responses`; у `gatewayRuntime`, `gatewayRegister`, `gatewayClose` — только `deprecated` и `x-umir-removal-gate`; у `authorize`, `accessCheck`, `serviceToken` — только `description`. Остальные поля этих операций и все прочие операции предшественника не меняются; удалить операцию нельзя.
- Члены верхнего уровня `x-durable-source` и `x-durable-policy`. Цепочка `x-ua1-source`, `x-fin-source`, `x-registration-source` сохраняется.

Все внутренние `$ref` составного файла проверяются скриптом.

## 1. Смысл Epoch и runtimeEpoch (R2, C19)

`Epoch` остаётся UUID (формат не меняется), но означает storage generation: идентификатор, который владелец создаёт первой миграцией (`storage_meta`, `gen_random_uuid()`) и хранит вместе с данными. Он меняется только при сбросе хранилища или восстановлении на более раннюю историю (`<bin> generation rotate --reason <текст>` у остановленного сервиса под ролью-владельцем). Перезапуск процесса, выкат и смена реплики его не меняют. У Warehouse, Serving и Auth своя generation. `runtimeEpoch`, `readerEpoch`, `expectedEpoch`, `currentEpoch` и заголовок `X-Umir-Runtime-Epoch` несут её же. `RuntimeReset` возвращается только при подтверждённом сбросе generation.

Идентификатор экземпляра (`INSTANCE_ID`: `gateway-a`, `serving-b`) виден в `/readyz` и логах. На проводе он допустим только в `epoch` SSE и курсоре Gateway (C19): Gateway кладёт туда случайный boot-nonce независимо от `INSTANCE_ID`. Описания в заголовках и параметрах `X-Umir-Runtime-Epoch` берутся через `$ref` на схему `Epoch`, поэтому отдельной замены у операций нет (отклонение от текста подспеки, см. ниже).

## 2. Операции Gateway и порядок слияния (R3, C7)

`gatewayRuntime`, `gatewayRegister` и `gatewayClose` (`/internal/gateway/v2/runtime`, `/internal/gateway/v2/r/{restaurantID}/registry`) получают `deprecated: true` и `x-umir-removal-gate` (`removeIn: 2.0.0-DUR.2`, `order: C7`, три предшествующих шага). Перечисления `service` в `RuntimeStatus`, `RuntimeReadiness`, `ReadinessAck` сужены до `[warehouse, serving]`. Описания `authorize`, `accessCheck`, `serviceToken` говорят, что готовность Gateway не проверяется.

Порядок слияния строго такой:

1. DUR1 помечает операции Gateway как `deprecated`.
2. Gateway, PR-1: сам читает реестр по ревизии, готовность от планировщика не зависит, старые маршруты пока работают.
3. Auth, PR: убирает Gateway из пиров.
4. Gateway, PR-2: убирает маршруты `runtime|registry` и JWKS-проверку для Auth без `sync-contract`.
5. `2.0.0-DUR.2` физически удаляет операции; после этого у Gateway `sync-contract`.

Мост совместимости — шаги 2–4. Профиль FIN1 для отката не трогается. `LifecyclePeerReadiness` и `LifecycleResult.services` в DUR1 не меняются: до шага 3 Auth ещё возвращает пиров Gateway (см. вопрос ниже).

## 3. Реестр Auth (R5, C13)

`GET /internal/auth/v1/registry` принимает `limit` (1–1000, по умолчанию 1000), `after` (непрозрачный курсор `nextAfter`), `since` (ревизия; `0` — всё записанное) и `If-None-Match`. Ответ несёт `ETag` — квотированное значение `registryRevision`; при неизменной ревизии — `304` без тела (проверяется только запрос без `after`). Без `since` список отдаёт только живые рестораны, `tombstones` пуст. С `since` — изменения после ревизии, включая надгробия. Страница держит не больше `limit` записей (записи плюс надгробия), `complete` равен `true` на последней странице, `nextAfter` есть ровно тогда, когда `complete` равен `false`. Обход берёт `registryRevision` первой страницы как R0 и завершает вызовом `?since=R0`, чтобы догнать изменения во время обхода.

`GET /internal/auth/v1/registry/{restaurantID}` возвращает `RegistryPoint`: `registryRevision` и либо запись (`state: live`), либо надгробие (`state: tombstone`), с `ETag` и `If-None-Match` (`304`). Ресторан, которого не было, — `404 NotFound`. Надгробия хранятся бессрочно, без ПДн, читаются через `since` и точечное чтение. Записи реестра отражают подтверждённую готовность, живых `Status` пира в чтении нет. Ни один бизнес-запрос не читает весь реестр; сверка вызывает статус пира один раз на сервис за проход.

Квота: 10 000 активных ресторанов, удалённые не считаются. Превышение при создании ресторана — `409 LimitExceeded`, `outcome: not_committed`. Число подтверждает G1.

## 4. Попытка заказа на складе (FIN-04, C21)

`WOrderAttempt`: `Unseen | Pending | Interrupted | Completed | Rejected`. `Pending` — попытка выполняется. `Interrupted` — выполнение прекратилось без записанного исхода. Переходы: `Pending` → `Interrupted`, `Interrupted` → `Pending` при точном повторе того же commit (тот же `commandID`, `proposalID` и принятые позиции). Новый `commandID` для повтора не нужен. Квитанция команды в журнале всегда сильнее строки попытки. Ответ Warehouse на новый `commandID` для уже оформленного предложения — `200` с тем же `orderID`, второго заказа нет (warehouse.md W1-2). Serving читает `Interrupted` как неизвестный исход, пока не разрешит попытку; прежнее рабочее имя `Abandoned` не используется.

## 5. Расписание сроков (R6, C22)

`GET /internal/warehouse/v2/expiry-due?asOf&limit&after` (`limit` 1–1000, по умолчанию 500) возвращает `ExpiryDueList`: `runtimeEpoch` (generation склада), `asOf`, `items` (`restaurantID`, `nextDueAt`) в устойчивом порядке и непрозрачный `nextAfter`. Нужен токен `expiry.due` уровня ячейки. `DueExpiryResult` получает необязательное поле `nextDueAt` — ближайший срок ресторана после прогона (подсказка планировщику). Ленивая просрочка при чтении остаётся.

## 6. CommandConflict (C29, C53)

Код `CommandConflict` — единственный для повторной команды с другим телом и для повтора, чей сохранённый результат уже очищен. `CommandIdentityConflict` в контракте нет (он есть только в коде Serving `internal/adapters/httpapi/handler.go:464-465`). Serving меняет код в волне 1 (контракт не меняется), Warehouse — в волне 2 через DUR1, регрессию на границе (enum ошибок scoped) ведёт Gateway. Повтор команды Auth после очистки её результата — `409 CommandConflict`, `outcome: unknown`.

Граница гарантии повтора (C53): после истечения 180-дневного надгробия повтор старой команды выполняется как новая команда. Результаты команд Auth хранятся 30 дней, отпечаток-надгробие — 180 дней (таблица сроков, предложение до G1).

## 7. Допуск (C16)

`429 RateLimited` несёт заголовок `Retry-After` и поле `details.retryAfterSeconds`, `outcome: not_committed`; схема `Error` это требует. Отказ хранилища допуска до отправки — `503 ServiceUnavailable`: `outcome: not_committed` для записи и `unavailable` для чтения. Нового кода `AdmissionUnavailable` нет. Сбой после отправки сохраняет прежние коды (`ServiceUnavailable`, `UpstreamTimeout`) с `outcome: unknown`. Примеры каждого случая — в `durable-v1-examples.json`.

## 8. Лимиты (C15, C43, C53)

- `PrepareOrderRequest.positions` — не более 200; превышение — `400 InvalidArgument`, `not_committed`.
- Активных предложений на ресторан не более 2000; превышение — `409 LimitExceeded`, `not_committed`.
- Схема `ID` получает `maxLength: 256` (кодовые точки). У `ClientCommandID` та же граница уже введена REG1 (`maxLength: 256`), поэтому DUR1 её не заменяет (отклонение от текста подспеки). Владельцы проверяют длину до записи; превышение — `400 InvalidArgument`, `not_committed`.
- `LimitExceeded` зависит от операции: `409` для квот (активные рестораны, активные предложения) и `503` для существующих чтений страниц (`outcome: unavailable`). Оба примера есть.

Числа подтверждает G1.

## 9. Сервисные токены (C11)

`ServiceAuthorizationRequest` теперь одно из двух. Токен ресторана: `restaurantID`, `epoch` и `operation` из `expiry.due`, `runtime.register`, `runtime.close`, `order.resolve`. Токен уровня ячейки: только `operation: expiry.due`, без ресторана и эпохи. `ServiceClaims` имеет три варианта: токен ресторана, `order.resolve` и `expiry.due` уровня ячейки.

- `expiry.due` уровня ячейки: в claims нет `org_id`, `tenant_id`, `runtime_epoch`, `lifecycle_version`; `sub = service:gateway`, `azp = umir-gateway`, `aud = [umir-warehouse]`, `permissions = [expiry.due]`; годен только для списка сроков.
- `order.resolve` (Serving → Warehouse): один ресторан (`tenant_id`), `runtime_epoch` — generation склада, `sub = service:serving`, `azp = umir-serving`, `aud = [umir-warehouse]`, `permissions = [consumer.order.resolve]` (существующая возможность пассивных чтений); годен только для `GET /order-attempts` и `GET /commands`, остальные маршруты его отвергают.

Warehouse принимает оба вида. Повтор commit остаётся за клиентом. Готовность Gateway при выдаче не проверяется.

## 10. Допуск по клиенту (C12, C26)

Бюджеты «по клиенту» на публичных входах Auth держит Gateway. Auth держит надёжные бюджеты по логину, адресу, выдачам кодов и неверным кодам в Postgres в той же транзакции, что и действие; Redis-клиента у Auth нет (C51), ограничитель Argon2 — процессный `verificationGate`. Заголовка с ключом клиента в приватном контракте нет.

## 11. aggregateRevision и readerEpoch (C30)

В результатах команд Auth (`DurableResult`, `IdempotencyResult`) и в `AuthPageIdentity` `aggregateRevision` — монотонное значение последовательности БД, не подряд; потребители на непрерывность не рассчитывают. `readerEpoch` — generation Auth. Непрерывность гарантируется только у `warehouse.*` (`docs/durable/events.md`).

## 12. Здоровье (C32, C42, C49, C52)

`GET /healthz` — процесс жив. `GET /readyz` — основная возможность готова; тело `HealthReadiness`: `{instance, generation, capabilities: {<имя>: ok|degraded|down}}`; `200`, если все возможности, обязательные для готовности по `docs/durable/configuration.md`, в состоянии `ok`, иначе `503` с тем же телом. Подпутей `/readyz/*` нет. У frontend `/readyz` отдаёт `{"capabilities":{"static":"ok"}}` без `instance` и `generation`; у Gateway `generation` нет. Ответы не требуют сервисного ключа. Имена возможностей — в `configuration.md`.

## 13. Квоты SSE (C36)

8 потоков на сессию, 200 на организацию, 2000 на ячейку. Числа предварительные; G1 утверждает их после замера `AccessCheck` (R13). Отказ — `429 RateLimited`, `Retry-After: 1`. В схеме `LiveEvent` описано, что `epoch` — boot-nonce Gateway, а не storage generation.

## 14. Прочее из сверки

- Заголовки `X-Umir-Invalidation-Required` не меняются: Gateway после R4 их игнорирует, удаление — в DUR.2 (warehouse.md R4, C48).
- Целевая задержка relay p95 не более 1 с — рекомендация в `docs/durable/events.md`, в контракт не входит.
- Хранение пары Warehouse/Serving (C25): Warehouse хранит квитанции попыток не меньше 35 дней; Serving прекращает автодоводку через 30 дней, такая попытка становится `unresolved`, видна в логах и чтениях, commit не повторяется.

## Границы гарантий (C53)

- Повтор команды с тем же `commandID` и тем же телом возвращает исходный результат, пока жива запись: 30 дней для результата, 180 дней для надгробия-отпечатка (предложение до G1). Между этими сроками повтор получает `409 CommandConflict`, `outcome: unknown`. После надгробия команда выполняется как новая.
- `outcome: not_committed` даёт только отказ до отправки владельцу (допуск, лимиты, проверка входа). Сбой после отправки всегда `unknown`.
- `LimitExceeded`: `409` для квот, `503` для чтений страниц.
- `registryRevision` и `aggregateRevision` Auth монотонны, но не подряд.

## Отклонения от подспеки и открытые вопросы

1. `ClientCommandID` в составном REG1 уже имеет `maxLength: 256`; DUR1 вводит границу только для `ID`, `ClientCommandID` не заменяет.
2. Скрипт допускает не только замену схем, новые операции и флаг `deprecated`, но и точечную правку полей семи существующих операций (`x-operation-patches`). Иначе нельзя выразить `limit/after/since/ETag/304` у `GET /internal/auth/v1/registry` и описания `authorize`, `accessCheck`, `serviceToken`. Набор и допустимые поля зашиты в скрипт и тест; всё остальное в этих операциях проверяется на неизменность.
3. Описания заголовков и параметров `X-Umir-Runtime-Epoch` отдельно не заменяются: у параметров нет своего описания, они ссылаются на `Epoch`. Описания заголовков ответов в существующих операциях остаются.
4. `LifecyclePeerReadiness` и `LifecycleResult.services` сохраняют `gateway`: до шага 3 порядка C7 Auth ещё возвращает пиров Gateway, а существующие примеры публичного профиля содержат такие записи. Сужение — предложение для `2.0.0-DUR.2`.
5. Токен `order.resolve` несёт разрешение `consumer.order.resolve`: перечисление `Capability` не расширяется.
6. Курсоры `after`/`nextAfter` реестра и списка сроков непрозрачны, порядок — на усмотрение владельца, но устойчив.
