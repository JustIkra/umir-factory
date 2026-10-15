# Каталог событий и топология RabbitMQ

Документ — контракт волны 0 стека Postgres + RabbitMQ + Redis (решения C1–C3, C30, C38, C39, C45, C48 главной спеки). Его читают все группы; расхождения оформляются как CR к Factory. Числа (TTL, лимиты, цель по задержке) — предложение до G1.

Машинно-читаемые части:

- схема конверта события: [event-envelope.schema.json](event-envelope.schema.json);
- схема сообщения задачи: [task-message.schema.json](task-message.schema.json).

Оба файла — JSON Schema draft 2020-12 с `additionalProperties: false`. Примеры ниже проверяет тест `tests/infra/durable-events.test.mjs`: каждый пример обязан проходить свою схему, а мутации (лишнее поле, поле с персональными данными, отсутствие `topics` у Warehouse, неизвестная тема) обязаны отвергаться. Таблица прав в конце документа проверяется тем же тестом.

Что сюда не входит: HTTP-контракты (DUR1, `docs/durable-api-amendment.md`), имена переменных окружения и здоровье (`configuration.md`), сроки хранения (`retention.md`). Эталон публикации и доставки — `reference/durable` (пакеты `rabbitmq` и `relay`); он реализует всё, что описано ниже.

## Обменники

Все обменники durable, живут в vhost `umir` и объявляются только заданием `mq-init`.

| Обменник | Тип | Кто публикует | Что в нём |
| --- | --- | --- | --- |
| `umir.events` | topic | только Warehouse (C38) | события склада для live; ключ `warehouse.<eventType>` |
| `umir.auth` | topic | только Auth | уведомления жизненного цикла Auth; ключ `auth.<eventType>` |
| `umir.tasks` | direct | Auth, Feedback | сообщения задач; ключи `auth.mail`, `feedback.smtp`, `feedback.telegram` (письма Auth тоже здесь, C2) |
| `umir.dlx` | direct | брокер и потребители задач | повтор через `.retry` и мёртвые письма в `.dlq` |

Serving ничего не публикует (C38): резерва `serving.*` в каталоге нет. Если у Serving появится событие, это CR к Factory с записью в outbox в той же транзакции, что и переход.

## Конверт события

Тело каждого сообщения `umir.events` и `umir.auth` — один JSON-объект по схеме [event-envelope.schema.json](event-envelope.schema.json). Тип события в теле не повторяется: он в свойстве AMQP `type`.

| Поле | Обязательность | Смысл |
| --- | --- | --- |
| `eventID` | всегда | непрозрачная строка, не длиннее 256 символов (печатные ASCII без пробелов), уникальная у владельца; UUID не обязателен (C48) |
| `owner` | всегда | `warehouse` или `auth` |
| `schemaVersion` | всегда | целое от 1: версия конверта и типа события |
| `orgID` | всегда | организация |
| `restaurantID` | у `warehouse.*` всегда; у `auth.*` там, где событие относится к ресторану | ресторан |
| `aggregateID` | всегда | идентификатор агрегата; у `warehouse.*` это `restaurantID` |
| `aggregateRevision` | всегда | версия агрегата, см. раздел ниже (C30) |
| `generation` | всегда | storage generation владельца в момент события (UUID) |
| `occurredAt` | всегда | время события, RFC 3339 |
| `correlationID` | необязательно | идентификатор запроса, из которого возникло событие |
| `causationID` | необязательно | идентификатор команды-причины (у склада — `commandID`) |
| `topics` | у `warehouse.*` всегда, у `auth.*` запрещено | темы live из словаря `catalog`, `stock`, `menu`, `orders`, `analytics` (без повторов, не пусто) |

Ограничения и правила:

- `eventID` не равен `outbox.id`. `outbox.id` — UUID строки outbox, служебное поле владельца; на провод оно не идёт. Повтор публикации с неизвестным исходом (`Unknown`) идёт с прежним `eventID`, поэтому потребитель дедуплицирует по паре (потребитель, `eventID`) в своём inbox.
- В телах нет персональных данных, одноразовых кодов, токенов и текста обращений: схема закрыта, лишнее поле (например `email`) делает сообщение невалидным. Допустимы только идентификаторы, ревизии и время. Для событий Auth жизненного цикла версия жизненного цикла (`lifecycleVersion`) передаётся в `aggregateRevision`; отдельного поля нет.
- Тела складских событий не содержат данных заказа и количеств: только конверт. Подробности потребитель читает по HTTP.
- Событие — подсказка, а не право доступа. Уведомление Auth ничего не разрешает: потребитель сверяет состояние с источником (реестр Auth, склад по HTTP).

### Свойства сообщения AMQP

Потребитель может решить судьбу сообщения до разбора тела.

| Свойство | Значение |
| --- | --- |
| `message_id` | `eventID` (у сообщения задачи — `messageID`) |
| `type` | `<owner>.<eventType>`, например `warehouse.OrderPlaced`, `auth.restaurant.deleting`; у сообщения задачи — ключ маршрутизации (`auth.mail`, `feedback.smtp`, `feedback.telegram`) |
| `content_type` | `application/json` |
| `delivery_mode` | 2 (persistent) |
| заголовок `x-umir-schema` | значение `schemaVersion` (целое); неизвестная версия — сообщение не разбирается |
| заголовок `x-umir-restaurant-id` | `restaurantID`; заголовка нет, если поля нет в теле |
| заголовок `x-umir-generation` | `generation` |

Ключ маршрутизации события равен значению `type`. Заголовки дублируют поля тела и не заменяют их; при расхождении тело главнее, а сообщение считается нечитаемым.

## Сообщение задачи

Тело сообщения `umir.tasks` — своя схема [task-message.schema.json](task-message.schema.json): `messageID`, `owner` (`auth` или `feedback`), `schemaVersion`, `jobID`, `channel` (необязательно: `smtp` или `telegram`; у писем Auth его нет), `generation`, `occurredAt`, `correlationID`. Сообщение — будильник: адресат, текст и полезная нагрузка задания остаются в базе владельца и читаются по `jobID`. Поэтому в сообщении нет ни адреса, ни телефона, ни текста обращения, ни кода, ни токена.

Свойства AMQP те же, кроме `type` (ключ маршрутизации) и отсутствия заголовка `x-umir-restaurant-id`. Заголовки `x-umir-schema` и `x-umir-generation` обязательны.

## aggregateRevision (C30)

| Владелец | Смысл | Гарантия |
| --- | --- | --- |
| `warehouse.*` | версия потока ресторана (`sequence` журнала) | подряд в пределах `generation`; разрыв обнаруживает потребитель и в ответ перечитывает состояние по HTTP |
| `auth.*` | версия сущности (для жизненного цикла — `lifecycleVersion`) | монотонная, но не подряд: непрерывность не гарантируется, потребитель на неё не рассчитывает |

Смена `generation` сбрасывает отсчёт: потребитель складских событий при новом `generation` не считает разрывом первое событие с любой ревизией.

## События Warehouse

Перечень типов — `umir-warehouse/internal/domain/events.go` (константы `EventProductRegistered`, `EventDishDefined`, `EventDeliveryReceived`, `EventOrderPlaced`, `EventBatchExpiryPending`, `EventBatchWriteoffConfirmed`, `EventBatchExpired`). Каждое событие журнала склада даёт ровно одну строку outbox в той же транзакции; ключ маршрутизации `warehouse.<eventType>`.

Таблица `eventType → topics` (словарь из пяти тем). Warehouse кладёт в событие этот узкий набор:

| Тип | topics |
| --- | --- |
| `warehouse.ProductRegistered` | `catalog`, `menu` |
| `warehouse.DishDefined` | `catalog`, `menu` |
| `warehouse.DeliveryReceived` | `stock`, `menu`, `analytics` |
| `warehouse.OrderPlaced` | `stock`, `menu`, `orders`, `analytics` |
| `warehouse.BatchExpiryPending` | `stock`, `menu`, `analytics` |
| `warehouse.BatchWriteoffConfirmed` | `stock`, `analytics` |
| `warehouse.BatchExpired` | `stock`, `menu`, `analytics` |

Gateway пока рассылает клиентам все пять тем, как сейчас (`umir-gateway/internal/httpapi/scoped.go`, функция записи изменений: `Changes.Append(tenant, []string{"catalog", "stock", "menu", "orders", "analytics"})`). Узкий набор включается позже, после теста Gateway и Frontend «каждая мутация обновляет нужные экраны» (C39). До этого момента потребитель не вправе сужать рассылку по `topics`.

## События Auth (C1)

Семь типов, ключи строчные через точку; типа «ресторан создан» нет. Тело — конверт: идентификаторы, ревизии и `lifecycleVersion` в `aggregateRevision`. Тема `topics` у `auth.*` запрещена.

| Тип | Момент | Потребители |
| --- | --- | --- |
| `auth.restaurant.deleting` | удаление принято, необратимо | Gateway, Serving: сразу перестать обслуживать |
| `auth.restaurant.deleted` | очистка ресторана завершена | Gateway, Serving: очистка; Feedback: стирание по scope |
| `auth.organization.deleting` | то же для организации | Gateway, Serving: сразу перестать обслуживать |
| `auth.organization.deleted` | очистка организации завершена | Gateway, Serving: очистка; Feedback: стирание по scope |
| `auth.organization.settings_changed` | изменены настройки организации | Gateway: внеочередная синхронизация реестра; Serving: сброс проекции всех ресторанов организации |
| `auth.membership.revoked` | отзыв членства | Gateway |
| `auth.assignment.revoked` | отзыв назначения | Gateway |

Feedback обрабатывает только `deleted`, ресторана и организации. События Auth — только подсказки: очереди Gateway и Serving exclusive, а `feedback.auth` существует лишь при профиле Feedback. Потребители сверяются с реестром Auth (`?since=<revision>` с надгробиями, C13) при старте и периодически, поэтому пропущенное событие всё равно дойдёт.

## Ключи маршрутизации и возврат `mandatory`

Все сообщения публикуются с `mandatory` и publisher confirms. Если брокер вернул сообщение (`basic.return`, нет подходящей привязки), действует политика ключа (C3, C45):

- `accept` — строка outbox помечается опубликованной с `routed=false`, растёт метрика (счётчик в журнале). Для ключей, у которых потребители fan-out и может не быть ни одного.
- `retry` — строка остаётся в outbox, возврат в очередь с задержкой. Для ключей, где отсутствие привязки — ошибка конфигурации.

`Nacked` и `Unknown` — всегда retry, с прежним `eventID`. Потеря аренды релея останавливает пачку.

| Обменник | Ключ | Производитель | Потребители | Возврат |
| --- | --- | --- | --- | --- |
| `umir.events` | `warehouse.ProductRegistered` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.DishDefined` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.DeliveryReceived` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.OrderPlaced` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.BatchExpiryPending` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.BatchWriteoffConfirmed` | Warehouse | Gateway | accept |
| `umir.events` | `warehouse.BatchExpired` | Warehouse | Gateway | accept |
| `umir.auth` | `auth.restaurant.deleting` | Auth | Gateway, Serving | accept |
| `umir.auth` | `auth.restaurant.deleted` | Auth | Gateway, Serving, Feedback | accept |
| `umir.auth` | `auth.organization.deleting` | Auth | Gateway, Serving | accept |
| `umir.auth` | `auth.organization.deleted` | Auth | Gateway, Serving, Feedback | accept |
| `umir.auth` | `auth.organization.settings_changed` | Auth | Gateway, Serving | accept |
| `umir.auth` | `auth.membership.revoked` | Auth | Gateway | accept |
| `umir.auth` | `auth.assignment.revoked` | Auth | Gateway | accept |
| `umir.tasks` | `auth.mail` | Auth | Auth | retry |
| `umir.tasks` | `feedback.smtp` | Feedback | Feedback | retry |
| `umir.tasks` | `feedback.telegram` | Feedback | Feedback | retry |

Почему все ключи `umir.auth` — accept, включая `auth.*.deleted`: Feedback — необязательный профиль, без него очереди `feedback.auth` нет, и retry шёл бы бесконечно. Пропущенное удаление Feedback находит сверкой с реестром. Очереди задач, наоборот, durable и объявлены всегда: возврат там означает сломанную топологию.

## Очереди

### Очереди задач

Три рабочие очереди `auth.mail`, `feedback.smtp`, `feedback.telegram` (и `feedback.auth`, см. ниже) устроены одинаково. Для очереди `<q>` объявляются три очереди и три привязки на `umir.dlx`:

| Очередь | Тип и аргументы | Назначение |
| --- | --- | --- |
| `<q>` | quorum; `x-delivery-limit` 5; `overflow=reject-publish`; `x-dead-letter-exchange=umir.dlx`; `x-dead-letter-routing-key=<q>.retry`; `x-dead-letter-strategy=at-least-once` | рабочая очередь |
| `<q>.retry` | quorum; `x-message-ttl` 30000 (30 с); `overflow=reject-publish`; `x-dead-letter-exchange=umir.dlx`; `x-dead-letter-routing-key=<q>`; `x-dead-letter-strategy=at-least-once` | пауза 30 с перед повтором |
| `<q>.dlq` | quorum | мёртвые сообщения после пятой неудачи |

Привязки `umir.dlx`: ключ `<q>` в `<q>`, ключ `<q>.retry` в `<q>.retry`, ключ `<q>.dlq` в `<q>.dlq`. Рабочая очередь привязана к своему обменнику: `umir.tasks` с ключом `<q>` для `auth.mail`, `feedback.smtp`, `feedback.telegram`.

Ход обработки (его реализует `Consume` эталона):

1. Потребитель получает сообщение с `prefetch`, вызывает обработчик и подтверждает (`ack`) только после успеха.
2. Ошибка обработчика: `nack` без возврата в очередь. Брокер по `x-dead-letter-routing-key` отправляет сообщение через `umir.dlx` в `<q>.retry`; через 30 с оно возвращается в `<q>`.
3. Номер попытки — 1 плюс сумма `count` по записям заголовка `x-death`, у которых `queue` равна `<q>` (любая причина: `rejected`, `delivery_limit`). Сбой потребителя без `ack` ограничен тем же счётом: после `x-delivery-limit` брокер сам отправляет сообщение в `<q>.retry`.
3a. Если номер попытки, посчитанный до вызова обработчика, уже больше 5 (процесс падал в обработчике, и брокер возвращал сообщение по `x-delivery-limit` через `.retry`), потребитель не вызывает обработчик: он сразу публикует копию в `umir.dlx` с ключом `<q>.dlq`, ждёт confirm и подтверждает исходное (`ack`). Класс ошибки в метаданных копии — `attempts_exceeded`.
4. Пятая неудача: потребитель публикует копию в `umir.dlx` с ключом `<q>.dlq`, ждёт confirm и только затем подтверждает исходное. Задание, попавшее в `.dlq`, не теряется: состояние остаётся в базе владельца.
5. Тело сообщения не попадает ни в журналы, ни в метаданные DLQ; только `message_id`, `type`, номер попытки и класс ошибки.

`overflow=reject-publish` нужен и для `at-least-once` dead-lettering, и чтобы переполненная очередь отвечала издателю `nack`, а не молча теряла сообщения.

### Fan-out очереди

Сигнальные очереди реплик: exclusive, auto-delete, не quorum, без `.retry` и `.dlq`. Каждая реплика объявляет свои очереди сама и пересоздаёт их при переподключении. Нечитаемое сообщение — `reject` без возврата и счётчик; полагаться на доставку нельзя (см. сверку с реестром).

| Очередь | Кто | Привязки | Prefetch |
| --- | --- | --- | --- |
| `gateway.live.<instance>` | Gateway | `umir.events`, ключ `warehouse.#` (только он, C38) | 32 |
| `gateway.auth.<instance>` | Gateway | `umir.auth`, все семь ключей из раздела «События Auth» | 32 |
| `serving.auth.<instance>` | Serving | `umir.auth`: `auth.restaurant.deleting`, `auth.restaurant.deleted`, `auth.organization.deleting`, `auth.organization.deleted`, `auth.organization.settings_changed` | 32 |

`<instance>` — уникальный идентификатор реплики (`gateway-a`, `serving-b`). Serving не привязывается к `umir.events`, Gateway не привязывается к `serving.#`.

### Durable-очередь Feedback

`feedback.auth` — durable quorum, устроена как рабочая очередь выше (с `.retry` и `.dlq`), привязана к `umir.auth` ключами `auth.restaurant.deleted` и `auth.organization.deleted`. Нужна для стирания по scope. `mq-init` объявляет её только при включённом профиле Feedback (C45); без профиля очереди нет, пользователь `umir_feedback` не используется.

## Кто объявляет топологию (C2)

- Обменники, durable-очереди, `.retry`, `.dlq` и привязки `umir.dlx` объявляет только задание `mq-init` (`Declare` эталона). Привязки durable-очередей к `umir.tasks` и `umir.auth` — тоже оно.
- Сервисы объявляют лишь свои exclusive-очереди и их привязки (`DeclareExclusive`). Всё остальное проверяют пассивно (`CheckPassive`) при старте и при переподключении.
- Несовпадение аргументов или отсутствие обменника или очереди делает возможность недоступной (`mail`, `delivery.*`, `lifecycle`, `outbox`, `events` по таблице здоровья в `configuration.md`), но не останавливает процесс и не переопределяет топологию.
- `mq-init` идемпотентен: повторный запуск ничего не меняет, расхождение с уже существующими аргументами очереди — ошибка, а не удаление очереди.

## Целевая задержка relay (C48)

Рекомендация: задержка от коммита транзакции с записью в outbox до подтверждённой публикации — p95 не больше 1 с. Измеряется возрастом старейшей неопубликованной строки (`Stats()` релея) и SQL приёмки. Это цель замера, а не гарантия: брокер недоступен — команды продолжают работать, растёт лаг outbox.

## Права

Пользователи RabbitMQ — по одному на сервис в vhost `umir`; учётные данные сервис получает из файла `*_AMQP_URL_FILE`. Права задаются шаблонами имён, а не `.*`. RabbitMQ применяет шаблон без неявных якорей, поэтому каждый шаблон ниже обёрнут в `^…$` (в таблице `\|` означает знак `|` внутри шаблона). Пустое право записано как `^$`.

| Пользователь | configure | write | read | Примечание |
| --- | --- | --- | --- | --- |
| `umir_auth` | `^$` | `^umir\.(auth\|tasks\|dlx)$` | `^auth\.mail$` | публикует события и письма, потребляет `auth.mail`; запись в `umir.dlx` нужна для копии в `auth.mail.dlq` |
| `umir_warehouse` | `^$` | `^umir\.events$` | `^$` | только публикует события; ничего не читает и не объявляет |
| `umir_serving` | `^serving\.auth\.[A-Za-z0-9_-]+$` | `^serving\.auth\.[A-Za-z0-9_-]+$` | `^(umir\.auth\|serving\.auth\.[A-Za-z0-9_-]+)$` | объявляет и потребляет свою exclusive-очередь, привязывает её к `umir.auth`; ничего не публикует (C38), к `umir.events` не привязывается |
| `umir_feedback` | `^$` | `^umir\.(tasks\|dlx)$` | `^feedback\.(smtp\|telegram\|auth)$` | публикует задачи, потребляет свои три очереди; запись в `umir.dlx` нужна для копии в `.dlq`; очередей не объявляет |
| `umir_gateway` | `^gateway\.(live\|auth)\.[A-Za-z0-9_-]+$` | `^gateway\.(live\|auth)\.[A-Za-z0-9_-]+$` | `^(umir\.(events\|auth)\|gateway\.(live\|auth)\.[A-Za-z0-9_-]+)$` | объявляет и потребляет свои exclusive-очереди, привязывает их к `umir.events` и `umir.auth`; ничего не публикует |
| `umir_mq_init` | `.*` | `.*` | `.*` | администратор топологии |

Правила:

- Привязка очереди требует права `read` на обменник и `write` на очередь: поэтому Gateway и Serving читают обменники, но писать в них не могут, а Serving не может прочитать `umir.events`.
- Публикация требует права `write` на обменник; публикация в обменник по умолчанию (`amq.default`) никому из сервисов не разрешена: все публикации идут в именованные обменники.
- Потребление требует права `read` на очередь. Ни один сервис не читает чужие очереди, `.retry` и `.dlq` (их читает только оператор через `mq-init`-учётную запись или management).
- Сервисы объявляют только свои exclusive-очереди: право `configure` есть лишь у Gateway и Serving и только на их шаблоны имён. У Auth, Warehouse и Feedback оно пустое: они проверяют топологию пассивно. Пассивное объявление не требует `configure`; если приёмка W2.2 покажет, что брокер просит большего, добавляется `read` на конкретный обменник или очередь, а не `configure` и не `.*`.
- Шаблон `<instance>` — `[A-Za-z0-9_-]+`: уникальный `*_INSTANCE_ID` реплики. Имя очереди с точкой в идентификаторе экземпляра шаблону не соответствует.
- `mq-init` — отдельный пользователь `umir_mq_init`, единственный с правами на весь vhost `umir`. Его URL-файл смонтирован только в задание `mq-init`; после завершения задания он никому не доступен. Этот пользователь не получает тегов (management) и не используется сервисами.
- Теги пользователей сервисов пустые: management API им недоступен. Management включается только в профиле `ops` на loopback.
- Остальные права не выдаются: ни `read`, ни `write` на `amq.*`, ни `configure` на обменники.
- Пользователи и права создаёт шаг инициализации брокера (W2.2); приёмка проверяет таблицу действием: каждая запрещённая операция (например, публикация Serving в `umir.tasks` или объявление `auth.rogue` пользователем Auth) обязана завершиться отказом доступа.
- Тестовая инфраструктура (`umir-prod-mq`, vhost `t_<repo>_<rand>`) работает под одним административным пользователем; таблица относится к стенду `compose.durable.yaml` и проверяется там.

## Примеры

Каждый блок проходит свою схему в тесте. Над блоком стоит комментарий-метка `<!-- example: envelope -->` или `<!-- example: task -->`; перед меткой — заголовок с типом события.

### warehouse.ProductRegistered

`type` = `warehouse.ProductRegistered`, `x-umir-schema` = 1.

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000001",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 1,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T09:00:00Z",
  "correlationID": "req-8f21",
  "causationID": "cmd-5a10",
  "topics": ["catalog", "menu"]
}
```

### warehouse.DishDefined

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000002",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 2,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T09:01:00Z",
  "correlationID": "req-8f22",
  "causationID": "cmd-5a11",
  "topics": ["catalog", "menu"]
}
```

### warehouse.DeliveryReceived

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000003",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 3,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T09:10:00Z",
  "correlationID": "req-8f23",
  "causationID": "cmd-5a12",
  "topics": ["stock", "menu", "analytics"]
}
```

### warehouse.OrderPlaced

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000004",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 4,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T09:30:00Z",
  "correlationID": "req-8f24",
  "causationID": "cmd-5a13",
  "topics": ["stock", "menu", "orders", "analytics"]
}
```

### warehouse.BatchExpiryPending

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000005",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 5,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T09:45:00Z",
  "causationID": "expiration/batch-17",
  "topics": ["stock", "menu", "analytics"]
}
```

### warehouse.BatchWriteoffConfirmed

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000006",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 6,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T10:00:00Z",
  "correlationID": "req-8f25",
  "causationID": "cmd-5a14",
  "topics": ["stock", "analytics"]
}
```

### warehouse.BatchExpired

<!-- example: envelope -->
```json
{
  "eventID": "wh-rest-1-000007",
  "owner": "warehouse",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 7,
  "generation": "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  "occurredAt": "2026-10-15T10:05:00Z",
  "causationID": "expiration/batch-18",
  "topics": ["stock", "menu", "analytics"]
}
```

### auth.restaurant.deleting

`type` = `auth.restaurant.deleting`; `aggregateRevision` — `lifecycleVersion` ресторана.

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d1",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 12,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T11:00:00Z",
  "correlationID": "req-9a01"
}
```

### auth.restaurant.deleted

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d2",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "rest-1",
  "aggregateRevision": 13,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T11:05:00Z"
}
```

### auth.organization.deleting

Событие уровня организации: `restaurantID` нет, и заголовка `x-umir-restaurant-id` тоже.

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d3",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "aggregateID": "org-1",
  "aggregateRevision": 4,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T12:00:00Z",
  "correlationID": "req-9a02"
}
```

### auth.organization.deleted

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d4",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "aggregateID": "org-1",
  "aggregateRevision": 5,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T12:10:00Z"
}
```

### auth.organization.settings_changed

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d5",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "aggregateID": "org-1",
  "aggregateRevision": 9,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T12:30:00Z",
  "correlationID": "req-9a03"
}
```

### auth.membership.revoked

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d6",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "aggregateID": "membership-31",
  "aggregateRevision": 3,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T13:00:00Z",
  "correlationID": "req-9a04"
}
```

### auth.assignment.revoked

<!-- example: envelope -->
```json
{
  "eventID": "auth-7c2e90d7",
  "owner": "auth",
  "schemaVersion": 1,
  "orgID": "org-1",
  "restaurantID": "rest-1",
  "aggregateID": "assignment-77",
  "aggregateRevision": 2,
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T13:10:00Z",
  "correlationID": "req-9a05"
}
```

### Сообщения задач

Письмо Auth: ключ `auth.mail`, `type` = `auth.mail`, канала нет.

<!-- example: task -->
```json
{
  "messageID": "auth-task-0001",
  "owner": "auth",
  "schemaVersion": 1,
  "jobID": "mail-job-5521",
  "generation": "0d4a52c8-91f0-4e3b-8c77-2a6b5e0f9a31",
  "occurredAt": "2026-10-15T14:00:00Z",
  "correlationID": "req-9b01"
}
```

Доставка Feedback по почте: ключ `feedback.smtp`.

<!-- example: task -->
```json
{
  "messageID": "fb-task-0001",
  "owner": "feedback",
  "schemaVersion": 1,
  "jobID": "delivery-8812",
  "channel": "smtp",
  "generation": "c3a91f5e-6d2b-4a7e-b0c4-1f8e2d9a7b65",
  "occurredAt": "2026-10-15T14:05:00Z",
  "correlationID": "req-9b02"
}
```

Доставка Feedback в Telegram: ключ `feedback.telegram`.

<!-- example: task -->
```json
{
  "messageID": "fb-task-0002",
  "owner": "feedback",
  "schemaVersion": 1,
  "jobID": "delivery-8813",
  "channel": "telegram",
  "generation": "c3a91f5e-6d2b-4a7e-b0c4-1f8e2d9a7b65",
  "occurredAt": "2026-10-15T14:06:00Z",
  "correlationID": "req-9b03"
}
```
