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
| `docs/api/durable-v1-composed-public.json` | `614580ad7dd5aa3b299948852e0308cc0601ca00e9cb21f2df8526450322ed1e` |
| `docs/api/durable-v1-composed-private.json` | `d070d324d86a26d69dd13304ff878e96a89b42510ca1cf1c31b9a2b0e7207b4f` |
| `docs/api/durable-v1-public.json` (оверлей) | `170947d10e96bf6107e0c2e7658e8eef4d86877dc7d1fe5d5c9a212d757fd9d1` |
| `docs/api/durable-v1-private.json` (оверлей) | `1726f5346cf40d2ece34a039dfd31b5008df0fb7bb82fd0e05921cb02f32e393` |
| `docs/api/durable-v1-examples.json` | `4e74ca65e394c564c95185f4e0fa6843e50e62b77471a34df6bcb71924273ffe` |
| `docs/api/registration-v1-composed-public.json` (предшественник) | `2be37f544a1ababa92cf16a4652826281db7f9b0e3d64bf1b389be2af0e0c873` |
| `docs/api/registration-v1-composed-private.json` (предшественник) | `2d21db8cd51085196b8294615b7db2afd68b540fcd4cbf90d0e67e4fb50a45d3` |

Потребители (Auth `internal/adapters/httpapi/contract.json`, Gateway `internal/operatorhttp/auth-contract.json`, Frontend `api/backend-v2-public.json`) копируют составные файлы и сверяют их по этим SHA-256 (`umir-checks contract-sync`, отдельная задача). Если оверлей или скрипт меняется, пересчитайте таблицу.

## Что разрешено менять

Скрипт отказывает, если набор отличается от объявленного.

- Замена 17 схем: `AuthPageIdentity`, `DueExpiryResult`, `DurableResult`, `Epoch`, `Error`, `ID`, `IdempotencyResult`, `LiveEvent`, `PrepareOrderRequest`, `ReadinessAck`, `Registry`, `RegistryEntry`, `RuntimeReadiness`, `RuntimeStatus`, `ServiceAuthorizationRequest`, `ServiceClaims`, `WOrderAttempt`. Оба профиля получают одинаковые замены.
- Пять новых схем: `ExpiryDueList`, `ExpiryDueRestaurant`, `HealthReadiness`, `RegistryPoint`, `RegistryTombstone` (без коллизий с предшественником).
- Четыре новые операции только в private: `GET /internal/auth/v1/registry/{restaurantID}` (`registryPoint`), `GET /internal/warehouse/v2/expiry-due` (`expiryDue`), `GET /healthz` (`healthz`), `GET /readyz` (`readyz`). Совпадений `operationId` и путей с существующими нет.
- Правка полей восьми существующих операций (`x-operation-patches`). Private: у `GET /internal/auth/v1/registry` — `description`, `parameters`, `responses`; у `gatewayRuntime`, `gatewayRegister`, `gatewayClose` — только `deprecated` и `x-umir-removal-gate`; у `authorize`, `accessCheck`, `serviceToken` — только `description`. Public: у `GET /api/v2/r/{restaurantID}/live` — только `responses` (ответ `429`). Остальные поля этих операций и все прочие операции предшественника не меняются; удалить операцию нельзя.
- Патч только добавляет. После наложения скрипт проверяет: у `registry` сохранены `200.content`, `default`, заголовки и все прежние параметры, описание непустое, новые ответы — только `304`; у `live` сохранены `200` и `default`, новый ответ — только `429`. Новые операции обязаны иметь описание, `x-owner`, `x-required-capability`, ответ `200` и описания ответов; у `healthz` и `readyz` `security` равен `[]`, у остальных пустого `security` быть не может. Оверлей с неожиданными ключами верхнего уровня отвергается.
- Члены верхнего уровня `x-durable-source` и `x-durable-policy`. Цепочка `x-ua1-source`, `x-fin-source`, `x-registration-source` сохраняется.

Все внутренние `$ref` составного файла проверяются скриптом.

## 1. Смысл Epoch и runtimeEpoch (R2, C19)

`Epoch` остаётся UUID (формат не меняется), но означает storage generation: идентификатор, который владелец создаёт первой миграцией (`storage_meta`, `gen_random_uuid()`) и хранит вместе с данными. Он меняется только при сбросе хранилища или восстановлении на более раннюю историю (`<bin> generation rotate --reason <текст>` у остановленного сервиса под ролью-владельцем). Перезапуск процесса, выкат и смена реплики его не меняют. У Warehouse, Serving и Auth своя generation. `runtimeEpoch`, `readerEpoch`, `expectedEpoch`, `currentEpoch` и заголовок `X-Umir-Runtime-Epoch` несут её же. `RuntimeReset` возвращается только при подтверждённом сбросе generation.

Идентификатор экземпляра (`INSTANCE_ID`: `gateway-a`, `serving-b`) на проводе не передаётся: он виден только в `/readyz` и логах. `epoch` SSE и курсор Gateway несут случайный boot-nonce, который не зависит от `INSTANCE_ID` (C19). Описания в заголовках и параметрах `X-Umir-Runtime-Epoch` берутся через `$ref` на схему `Epoch`, поэтому отдельной замены у операций нет (отклонение от текста подспеки, см. ниже).

## 2. Операции Gateway и порядок слияния (R3, C7)

`gatewayRuntime`, `gatewayRegister` и `gatewayClose` (`/internal/gateway/v2/runtime`, `/internal/gateway/v2/r/{restaurantID}/registry`) получают `deprecated: true` и `x-umir-removal-gate` (`removeIn: 2.0.0-DUR.2`, `order: C7`, три предшествующих шага). Перечисления `service` в `RuntimeStatus`, `RuntimeReadiness`, `ReadinessAck` сужены до `[warehouse, serving]`. Описания `authorize`, `accessCheck`, `serviceToken` говорят, что готовность Gateway не проверяется.

Порядок слияния строго такой:

1. DUR1 помечает операции Gateway как `deprecated`.
2. Gateway, PR-1: сам читает реестр по ревизии, готовность от планировщика не зависит, старые маршруты пока работают.
3. Auth, PR: убирает Gateway из пиров.
4. Gateway, PR-2: убирает маршруты `runtime|registry` и JWKS-проверку для Auth без `sync-contract`.
5. `2.0.0-DUR.2` физически удаляет операции; после этого у Gateway `sync-contract`.

Мост совместимости — шаги 2–4. Профиль FIN1 для отката не трогается. На период моста (шаги 2–4) ответы deprecated-операций Gateway и записи с пиром `gateway` в `LifecycleResult` выходят за суженные схемы; потребители не проверяют их по DUR1. `LifecyclePeerReadiness` и `LifecycleResult.services` в DUR1 не меняются: до шага 3 Auth ещё возвращает пиров Gateway (см. вопрос ниже).

## 3. Реестр Auth (R5, C13)

`GET /internal/auth/v1/registry` принимает `limit` (1–1000, по умолчанию 1000), `after` (непрозрачный курсор `nextAfter`), `since` (ревизия; `0` — всё записанное) и `If-None-Match`. Ответ несёт `ETag` — квотированное значение `registryRevision`; при неизменной ревизии — `304` без тела (проверяется только запрос без `after`). Без `since` список отдаёт только живые рестораны, `tombstones` пуст. С `since` — изменения после ревизии, включая надгробия. Страница держит не больше `limit` записей (записи плюс надгробия), `complete` равен `true` на последней странице, `nextAfter` есть ровно тогда, когда `complete` равен `false`. Обход берёт `registryRevision` первой страницы как R0 и завершает вызовом `?since=R0`, чтобы догнать изменения во время обхода.

`GET /internal/auth/v1/registry/{restaurantID}` возвращает `RegistryPoint`: `registryRevision` — глобальная ревизия реестра на момент чтения (тот же счётчик, что в списке, а не версия ресторана) — и либо запись (`state: live`), либо надгробие (`state: tombstone`), с `ETag` (квотированная `registryRevision`) и `If-None-Match`: `304` означает, что реестр не менялся с этой ревизии, а значит запись прежняя; изменившийся реестр может ответить `200` с той же записью (`304` и `200` не различают, менялась ли именно эта запись). Ресторан, которого не было, — `404 NotFound` (в операции описан явно). Ревизия реестра — положительный счётчик, первая ревизия равна 1 (`since=0` — всё записанное). `RegistryEntry.services` содержит не больше двух записей (`warehouse`, `serving`). Надгробия хранятся бессрочно, без ПДн, читаются через `since` и точечное чтение. Записи реестра отражают подтверждённую готовность, живых `Status` пира в чтении нет. Ни один бизнес-запрос не читает весь реестр; сверка вызывает статус пира один раз на сервис за проход.

Квота: 10 000 активных ресторанов, удалённые не считаются. Превышение при создании ресторана — `409 LimitExceeded`, `outcome: not_committed`. Число подтверждает G1.

## 4. Попытка заказа на складе (FIN-04, C21)

`WOrderAttempt`: `Unseen | Pending | Interrupted | Completed | Rejected`. `Pending` — попытка выполняется. `Interrupted` — выполнение прекратилось без записанного исхода. Переходы: `Pending` → `Interrupted`, `Interrupted` → `Pending` при точном повторе того же commit (тот же `commandID`, `proposalID` и принятые позиции). Новый `commandID` для повтора не нужен. Квитанция команды в журнале всегда сильнее строки попытки. Ответ Warehouse на новый `commandID` для уже оформленного предложения — `200` с тем же `orderID`, второго заказа нет (warehouse.md W1-2). Serving читает `Interrupted` как неизвестный исход, пока не разрешит попытку; прежнее рабочее имя `Abandoned` не используется.

## 5. Расписание сроков (R6, C22)

`GET /internal/warehouse/v2/expiry-due?asOf&limit&after` (`limit` 1–1000, по умолчанию 500) возвращает `ExpiryDueList`: `runtimeEpoch` (generation склада), `asOf`, `items` (`restaurantID`, `nextDueAt`) в устойчивом порядке и непрозрачный `nextAfter`. Нужен токен `expiry.due` уровня ячейки. `DueExpiryResult` получает необязательное поле `nextDueAt` — ближайший срок ресторана после прогона (подсказка планировщику). Ленивая просрочка при чтении остаётся.

## 6. CommandConflict (C29, C53)

Код `CommandConflict` — единственный для повторной команды с другим отпечатком при живой записи и для повтора, чей сохранённый результат уже очищен. Исходы разведены:

- другой отпечаток при живой записи: `409 CommandConflict`, `outcome: not_committed` для POST и `unavailable` для пассивного GET-чтения (backend-v2.md, serving.md); исходный результат не показывается, состояние не меняется;
- `outcome: unknown` — только для повтора команды Auth, чей результат уже очищен (результат 30 дней, надгробие-отпечаток 180 дней, C29, C53): результат больше не известен.

`CommandIdentityConflict` в контракте нет (он есть только в коде Serving `internal/adapters/httpapi/handler.go:464-465`). Serving меняет код в волне 1 (контракт не меняется), Warehouse — в волне 2 через DUR1, регрессию на границе (enum ошибок scoped) ведёт Gateway. Примеры `error-command-conflict-post`, `error-command-conflict-get` и `error-command-conflict` (unknown после очистки) лежат в `durable-v1-examples.json`.

Граница гарантии повтора (C53): после истечения 180-дневного надгробия повтор старой команды выполняется как новая команда.

## 7. Допуск (C16)

`429 RateLimited` несёт заголовок `Retry-After` и поле `details.retryAfterSeconds`, `outcome: not_committed`; схема `Error` это требует. Отказ хранилища допуска до отправки — `503 ServiceUnavailable`: `outcome: not_committed` для записи и `unavailable` для чтения. Нового кода `AdmissionUnavailable` нет. Сбой после отправки сохраняет прежние коды (`ServiceUnavailable`, `UpstreamTimeout`) с `outcome: unknown`. Схема `Error` требует `details.retryAfterSeconds` и `outcome: not_committed` для каждого `RateLimited`: это относится и ко всем существующим ответам `429 RateLimited` (в том числе REG1), Auth обязан довести их до этой формы. Примеры каждого случая, включая `UpstreamTimeout` с `unknown`, — в `durable-v1-examples.json`.

## 8. Лимиты (C15, C43, C53)

- `PrepareOrderRequest.positions` — не более 200; превышение — `400 InvalidArgument`, `not_committed`.
- Активных предложений на ресторан не более 2000; превышение — `409 LimitExceeded`, `not_committed`.
- Схема `ID` получает `maxLength: 256` (кодовые точки). У `ClientCommandID` та же граница уже введена REG1 (`maxLength: 256`), поэтому DUR1 её не заменяет (отклонение от текста подспеки). Владельцы проверяют длину до записи; превышение — `400 InvalidArgument`, `not_committed`.
- `LimitExceeded` зависит от операции: `409` для квот (активные рестораны, активные предложения) и `503` для существующих чтений страниц (`outcome: unavailable`). Оба примера есть.

Числа подтверждает G1.

## 9. Сервисные токены (C11)

`ServiceAuthorizationRequest` теперь одно из двух. Токен ресторана: `restaurantID`, `epoch` и `operation` из `expiry.due`, `runtime.register`, `runtime.close`, `order.resolve`. Токен уровня ячейки: только `operation: expiry.due`, без ресторана и эпохи. `ServiceClaims` имеет три варианта: токен ресторана, `order.resolve` и `expiry.due` уровня ячейки.

- `expiry.due` уровня ячейки: в claims нет `org_id`, `tenant_id`, `runtime_epoch`, `lifecycle_version`; `sub = service:gateway`, `azp = umir-gateway`, `aud = [umir-warehouse]`, `permissions = [expiry.due]`; годен только для списка сроков.
- `order.resolve` (Serving → Warehouse): один ресторан (`tenant_id`), `runtime_epoch` — generation склада, `sub = service:serving`, `azp = umir-serving`, `aud = [umir-warehouse]`, `permissions = [consumer.order.resolve]` (существующая возможность пассивных чтений); годен только для `GET /internal/warehouse/v2/r/{restaurantID}/order-attempts` и `GET /internal/warehouse/v2/r/{restaurantID}/commands`. Warehouse сверяет claim `operation = order.resolve` с маршрутом: `/orders` и все остальные маршруты токен отвергают (C11: только пассивные `/order-attempts` и `/commands`), хотя разрешение `consumer.order.resolve` само по себе открыло бы и `/orders`.

Warehouse принимает оба вида. Повтор commit остаётся за клиентом. Готовность Gateway при выдаче не проверяется.

## 10. Допуск по клиенту (C12, C26)

Бюджеты «по клиенту» на публичных входах Auth держит Gateway. Auth держит надёжные бюджеты по логину, адресу, выдачам кодов и неверным кодам в Postgres в той же транзакции, что и действие; Redis-клиента у Auth нет (C51), ограничитель Argon2 — процессный `verificationGate`. Заголовка с ключом клиента в приватном контракте нет.

## 11. aggregateRevision и readerEpoch (C30)

В результатах команд Auth (`DurableResult`, `IdempotencyResult`) и в `AuthPageIdentity` `aggregateRevision` — монотонное значение последовательности БД, не подряд; потребители на непрерывность не рассчитывают. `readerEpoch` — generation Auth. Непрерывность гарантируется только у `warehouse.*` (`docs/durable/events.md`).

## 12. Здоровье (C32, C42, C49, C52)

`GET /healthz` — процесс жив. `GET /readyz` — основная возможность готова; тело `HealthReadiness`: `{instance, generation, capabilities: {<имя>: ok|degraded|down}}`; `200`, если все возможности, обязательные для готовности по `docs/durable/configuration.md`, в состоянии `ok`, иначе `503` с тем же телом. Подпутей `/readyz/*` нет. У frontend `/readyz` отдаёт `{"capabilities":{"static":"ok"}}` без `instance` и `generation`; у Gateway `generation` нет. Ответы не требуют сервисного ключа. Имена возможностей — в `configuration.md`; имя соответствует `^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)?$` (точка отделяет канал, например `delivery.smtp`, C59).

## 13. Квоты SSE (C36)

8 потоков на сессию, 200 на организацию, 2000 на ячейку. Числа предварительные; G1 утверждает их после замера `AccessCheck` (R13). Отказ — `429 RateLimited`, `Retry-After: 1`; ответ `429` с заголовком `Retry-After` (`#/components/headers/Retry-After`) добавлен к публичной операции `live` патчем. В схеме `LiveEvent` описано, что `epoch` — boot-nonce Gateway, а не storage generation и не `INSTANCE_ID`.

## 14. Прочее из сверки

- Заголовки `X-Umir-Invalidation-Required` не меняются: Gateway после R4 их игнорирует, удаление — в DUR.2 (warehouse.md R4, C48).
- Целевая задержка relay p95 не более 1 с — рекомендация в `docs/durable/events.md`, в контракт не входит.
- Хранение пары Warehouse/Serving (C25): Warehouse хранит квитанции попыток не меньше 35 дней; Serving прекращает автодоводку через 30 дней, такая попытка становится `unresolved`, видна в логах и чтениях, commit не повторяется.

## Границы гарантий (C53)

- Повтор команды с тем же `commandID` и тем же телом возвращает исходный результат, пока жива запись: 30 дней для результата, 180 дней для надгробия-отпечатка (предложение до G1). Между этими сроками повтор получает `409 CommandConflict`, `outcome: unknown`. После надгробия команда выполняется как новая.
- `outcome: not_committed` даёт только отказ до отправки владельцу (допуск, лимиты, проверка входа). Сбой после отправки всегда `unknown`.
- `LimitExceeded`: `409` для квот, `503` для чтений страниц.
- `registryRevision` и `aggregateRevision` Auth монотонны, но не подряд.

## Числа контракта

Числа предварительные до G1; таблица, `x-durable-policy` и схемы сверяются тестом `TestDurablePolicyNumbersMatchAmendmentAndSchemas`.

| Ключ политики | Значение |
| --- | --- |
| `registry.activeRestaurantsMax` | 10000 |
| `registry.pageLimitMax` | 1000 |
| `registry.revisionStartsAt` | 1 |
| `limits.orderPositionsMax` | 200 |
| `limits.activeProposalsPerRestaurantMax` | 2000 |
| `limits.idMaxLength` | 256 |
| `limits.clientCommandIDMaxLength` | 256 |
| `sseQuotas.perSession` | 8 |
| `sseQuotas.perOrganization` | 200 |
| `sseQuotas.perCell` | 2000 |
| `commandConflict.authResultRetentionDays` | 30 |
| `commandConflict.tombstoneDays` | 180 |
| `pairRetention.warehouseAttemptReceiptsMinDays` | 35 |
| `pairRetention.servingAutoResolveStopsAfterDays` | 30 |

## Отклонения от подспеки и открытые вопросы

1. `ClientCommandID` в составном REG1 уже имеет `maxLength: 256`; DUR1 вводит границу только для `ID`, `ClientCommandID` не заменяет.
2. Скрипт допускает не только замену схем, новые операции и флаг `deprecated`, но и точечную правку полей восьми существующих операций (`x-operation-patches`): `limit/after/since/ETag/304` у `GET /internal/auth/v1/registry`, описания `authorize`, `accessCheck`, `serviceToken` и ответ `429` с `Retry-After` у публичной `live` (квоты SSE). Набор, допустимые поля и условие «патч только добавляет» зашиты в скрипт и тест; всё остальное в этих операциях проверяется на неизменность.
3. Описания заголовков и параметров `X-Umir-Runtime-Epoch` отдельно не заменяются: у параметров нет своего описания, они ссылаются на `Epoch`. Описания заголовков ответов в существующих операциях остаются.
4. `LifecyclePeerReadiness` и `LifecycleResult.services` сохраняют `gateway`: до шага 3 порядка C7 Auth ещё возвращает пиров Gateway, а существующие примеры публичного профиля содержат такие записи. Сужение — предложение для `2.0.0-DUR.2`.
5. Токен `order.resolve` несёт разрешение `consumer.order.resolve`: перечисление `Capability` не расширяется.
6. Курсоры `after`/`nextAfter` реестра и списка сроков непрозрачны, порядок — на усмотрение владельца, но устойчив.
7. На период моста C7 ответы deprecated-операций Gateway (`gatewayRuntime`, `gatewayRegister`, `gatewayClose`) и записи `gateway` в `LifecycleResult` лежат вне суженных схем `RuntimeStatus`, `RuntimeReadiness`, `ReadinessAck`; потребители не валидируют их по DUR1, пока `2.0.0-DUR.2` не удалит операции.
8. `RegistryEntry` заменена ради `services.maxItems = 2` (в подспеке её нет). 404 для точечного реестра описан отдельным ответом, его `outcome` в примере — `unavailable`, как для отказа чтения.
