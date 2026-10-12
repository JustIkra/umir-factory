# Спецификация передачи gateway и frontend

Дата: 2026-10-12. Часовой пояс: Europe/Moscow.

Статус на 2026-10-12: публичные gateway → serving → warehouse API работают по HTTP с процессной памятью. Resolve может продолжить попытку в рамках работающих процессов. Постоянное бизнес-состояние и восстановление незавершённого оформления после потери процессов ещё не реализованы. [README фабрики](../README.md#gateway-openapi) и [internal warehouse contract](api/warehouse-internal.yml) описывают транспорт и ограничения перезапуска.

Аккаунт и сессии одного оператора, private feedback и отдельная candidate-сборка реализованы в текущих исходниках. Выбранные образы сохраняют прежние pins до согласованной публикации. Frontend main и опубликованная quality-ветка пока не содержат экранов и API-клиента. Текущие версии и оставшиеся задачи перечислены в [README](../README.md#текущее-состояние).

[Figma Screens](https://www.figma.com/design/6Y1Jglyvc1xVsTzII54A2u/UmirHack?node-id=42-2) существует; незавершённый объём дизайна описан в [брифе](frontend-design-brief.md). [Business core](business-core.md) задаёт бизнес-правила, а [OpenAPI](api/openapi.yml) — публичный wire contract.

Постановка задачи, порядок начала работы, рабочие ветки и ссылки на PR находятся в GitHub issues. Этот документ хранит контракт и критерии результата; [задание на дизайн](frontend-design-brief.md) задаёт визуальные материалы. [Проектный review-skill](../skills/umir-pr-review/SKILL.md) используется при проверке будущих PR.

## 1. Владельцы и результат

| Область | Владелец | Результат |
| --- | --- | --- |
| Warehouse/serving business core | Разработчик ядра | Состояние, FEFO, события, предложения, идемпотентность и согласованный допуск. |
| Публичный wire contract | Интеграционный разработчик вместе с владельцем ядра | Версионированный OpenAPI в factory; транспортные поля и результаты сохраняют business-core. |
| Gateway | Интеграционный разработчик | Тонкая маршрутизация с внедряемыми HTTP-зависимостями, реальным proxy и проверками маршрутов. |
| Frontend и Figma | Интеграционный разработчик | Клиент gateway, экраны, явно различимые результаты, editable Figma и проверенный браузерный поток. |
| HTTP-адаптеры реальных бизнес-сервисов | Разработчик ядра | Реализованы для warehouse и serving; Resolve работает, пока процессы живы. Durable business state и восстановление pending после перезапуска остаются следующей задачей. |
| Factory | Оба разработчика в пределах своих PR | Документация, контрактные fixtures и воспроизводимая проверочная сборка. |

Отдельный stack с подменёнными serving/warehouse больше не требуется: бизнес-правила уже доступны через реальные runtime этих модулей. Factory проверяет их совместно через bridge и отдельно по HTTP на реальных процессах. Go-проверка bridge не покрывает транспорт и браузер; HTTP acceptance не заменяет UI E2E, который потребуется после реализации экранов. Восстановление бизнес-состояния после перезапуска остаётся вне текущего этапа.

### 1.1. Стек и проверки frontend

Слайд 8 презентации организаторов рекомендует JavaScript или TypeScript со SCSS, React и Chakra; выбор языка остаётся за участником. Опубликованная ветка frontend `feat/frontend-quality` (`962f7a9`) вводит TypeScript, React, SCSS, пять слоёв, Bun 1.4.3 и 35 quality checks, но ещё не объединена с main и не содержит экранов или API-клиента. В main по-прежнему JavaScript/React/Vite и `render(null)`. Chakra UI остаётся запланированным выбором и не установлена. Stylelint отложен; Vitest не выбран для текущего Bun toolchain. Сверяйте фактические scripts и зависимости с README frontend при реализации.

Разрешены openapi-typescript как dev-инструмент генерации типов публичного API, Vitest для unit-тестов и Playwright для браузерных/E2E-сценариев. При генерации типов используется точная принятая ревизия публичной OpenAPI. Рабочая редакция и опубликованная схема могут различаться; PR указывает commit фабрики и info.version использованной схемы. CI проверяет актуальность генерируемых типов и отдельно выполняет `tsc --noEmit`, если выбран TypeScript. Unit/browser проверки, Vite и контейнерная сборка входят в приёмку frontend.

Клиент проверяет JSON во время выполнения и полноту обработки `kind/type`. Неизвестный результат не становится успехом или доказанным отсутствием записи. Точные целые сохраняются строками или BigInt. При реализации UI браузерные сценарии проходят через frontend/Nginx → gateway → serving → warehouse на реальных процессах.

SCSS и тема Chakra используют согласованные токены, общую шкалу точек перелома и относительные размеры. Компоненты сохраняют состояния, доступность и визуальные решения из дизайн-брифа. README frontend, AGENTS, npm scripts/CI и changelog обновляются в том же PR по правилам [документации фабрики](../README.md#ведение-документации).

## 2. Абстракции gateway

Gateway остаётся HTTP-прокси. Он не выбирает партии, не готовит предложения, не вычисляет отпечатки, не создаёт commandID и не решает, была ли продажа записана.

Предлагаемая структура Go:

| Каталог | Ответственность |
| --- | --- |
| `cmd/server` | Настройки, сборка, lifecycle, команда healthcheck. |
| `internal/config` | Проверенные PORT/SERVING_URL/WAREHOUSE_URL и конечные настройки timeout. |
| `internal/httpapi` | Маршруты, healthz, разделение upstream и обработка транспортной ошибки. |
| `internal/adapters/httpupstream` | Конкретный ReverseProxy с внедрённым transport. |
| `tests` и `*_test.go` | Контрактные проверки и управляемые зависимости, без production seed. |

Используемые интерфейсы — стандартные http.Handler и http.RoundTripper. Это реальные границы потребителя; интерфейс на каждый Service или отдельный доменный BFF не требуется. Целевые сигнатуры:

```go
type Upstreams struct {
    Serving   http.Handler
    Warehouse http.Handler
}
func NewRouter(upstreams Upstreams) (http.Handler, error)
func NewProxy(target *url.URL, transport http.RoundTripper) (*httputil.ReverseProxy, error)
```

NewRouter находится в internal/httpapi, NewProxy — в internal/adapters/httpupstream. Router не читает env. Точка запуска создаёт оба proxy и передаёт их router; тест может подставить два handler или управляемый RoundTripper. Нулевые зависимости и неверные URL дают явную ошибку сборки. Gateway не импортирует Go-модули warehouse/serving.

Сохраняются GET /healthz, /api/serving/healthz и /api/warehouse/healthz. Префиксы /api/serving и /api/warehouse снимаются перед upstream; метод, оставшийся путь, query, тело, пользовательские end-to-end заголовки и upstream status/body сохраняются. Удаление hop-by-hop заголовков — штатная обязанность proxy. Перенаправление пути не меняет идентичность команды.

Запросы получают конечный бюджет ожидания и распространяют cancellation upstream. Gateway не выполняет собственный цикл повтора изменяющего запроса. Timeout/обрыв после отправки не доказывает отсутствие записи. Ошибка proxy не должна раскрывать credentials или внутренний URL; response status сам по себе не означает OrderConfirmed.

## 3. Wire contract v1 для клиентов

Wire v1 сохранён в [docs/api/openapi.yml](api/openapi.yml), редакция `1.0.0-draft.4`. В ней кроме бизнес-операций описаны вход одного оператора, сессия и CSRF (gateway#8) и публичная форма обратной связи (gateway#10); их обслуживает кандидат `compose.auth.yaml`, выбранная сборка работает без входа до согласованной смены образов. Контракт описывает реализованные HTTP-сервисы и используется клиентом и интеграционными проверками. Изменение согласованных бизнес-полей требует сверки с владельцем ядра; транспортные уточнения фиксируются вместе со схемой. Фабрика закрепляет опубликованные HTTP-образы отдельными SHA и проверяет выбранную сборку. Условия приёмки реальных сервисов и ограничения памяти описаны в разделе 7.

### Значения

- JSON-имена совпадают с business-core: commandID, proposalID, dishID, productID, lineNo, evaluatedAt, stateVersion. Идентификаторы — непустые opaque strings; UUID-ограничение не добавляется.
- Quantity, portions, lineNo, ShelfLife.value, sequence, stateVersion и remainingSeconds передаются каноническими десятичными строками целых чисел. Для положительного входа используется `[1-9][0-9]*`, для неотрицательного результата — `0|[1-9][0-9]*`; предел соответствующего значения — MaxInt64. schemaVersion=1 и HTTP-коды остаются обычными небольшими числами.
- quantity="40000" означает 40 базовых единиц g/ml/pcs; quantity="500" — 0,5 единицы. В JavaScript эти строки нельзя пропускать через Number для хранения/сравнения. Форматирование использует точную строку или BigInt; это отображение единиц, не оценка доступности.
- Даты передаются полным RFC3339 timestamp с +03:00, включая время. Date-only отклоняется при чтении исходной строки; полная полночь допустима. Календарные правила остаются в warehouse. Frontend не превращает дату без времени в скрытую полночь. Поставки вводятся только через интерактивную форму: createdAt, manufacturedAt и receivedAt обязательны и содержат дату и время. Загрузка и распознавание документов не входят в текущую версию.
- Порядок позиций и исходные lineNo сохраняются. Повтор dishID допустим. Меню содержит dishID/name без количества доступных порций; каталог — отдельный полный результат.
- Запросы чтения содержат evaluatedAt/stateVersion из своего сервиса. Gateway их не пересчитывает. Две независимые HTTP-оценки меню и стоп-листа не объявляются одной версией автоматически.
- Все обязательные payload соответствуют разделам 5–11 business-core. OpenAPI описывает отдельные варианты результатов, required поля и ошибку неизвестного результата, а не один произвольный object.

### Маршруты

| Через gateway | Upstream и операция |
| --- | --- |
| GET /api/serving/products | serving: GetProducts. |
| POST /api/serving/products | serving: RegisterProduct с commandID. |
| GET /api/serving/dish-catalog | serving: GetDishCatalog. |
| GET /api/serving/dish?dishID=... | serving: GetDish; query ID percent-encoded. |
| POST /api/serving/dishes | serving: DefineDish с commandID. |
| GET /api/serving/menu | serving: GetMenu. |
| GET /api/serving/stop-list | serving: GetStopList. |
| GET /api/serving/recommendations | serving: GetRecommendations. |
| POST /api/serving/order-proposals | serving: PrepareOrder. |
| POST /api/serving/order-confirmations | serving: ConfirmOrder с commandID/proposalID. |
| POST /api/serving/order-confirmations/resolve | serving: разрешение исходной попытки по commandID/proposalID. Не новая команда продажи. |
| GET /api/warehouse/inventory | warehouse: GetInventory. |
| GET /api/warehouse/expiring-batches?cutoffAt=... | warehouse: GetExpiringBatches с явным timestamp. |
| GET /api/warehouse/history | warehouse: GetHistory, факты в порядке записи. |
| POST /api/warehouse/deliveries | warehouse: ReceiveDelivery с commandID/deliveryID. |

Идентификаторы подтверждения передаются в JSON, а не подразумеваются как UUID в path. Значения query экранируются и должны сохранять Unicode и зарезервированные символы. Публичные команды ручного ExpireBatch/ProcessDueExpirations не входят в этот интерфейс: ими управляет приложение склада.

В форме поставки number называется «Номер поставки», createdAt — «Дата и время создания поставки». Номер не считается уникальным; защита повторов использует commandID и deliveryID по business-core. Для той же отправки сохраняются исходные идентификаторы и неизменный вход.

### Ответы и ошибки

Успешный запрос/команда возвращает свой типизированный результат. Подтверждение имеет обязательный kind: OrderConfirmed, RequiresConfirmation, EmptyOrder, ProposalSuperseded или ConfirmationPending. OrderConfirmed содержит записанный order; RequiresConfirmation — полное новое proposal; ProposalSuperseded — идентификатор замены. Остальные поля определены business-core. HTTP 2xx без распознанного kind/order не доказывает оформление.

| Результат | HTTP status | Смысл для frontend |
| --- | --- | --- |
| Запрос чтения или завершённый бизнес-результат | 200 | Использовать payload, включая пустой список, EmptyOrder и ProposalSuperseded с ID замены. |
| ConfirmationPending | 202 | Исходная попытка не завершена; не показывать продажу. |
| InvalidArgument, неподдерживаемая единица, некорректная точность/переполнение | 400 | Исправить вход. Чтение возвращает unavailable; отклонённый изменяющий вход — not_committed только для этого входа; неверный Resolve — unknown для исходного подтверждения. Валидный Resolve может повторить сохранённый отказ владельца с not_committed. |
| DishNotFound/ProposalNotFound | 404 | Confirm/Resolve ProposalNotFound возвращает unknown. DishNotFound может повторять определённый отказ склада с not_committed после сброса его памяти. Эти исходы нельзя смешивать. |
| CommandIdentityConflict, ProductAlreadyExists, DishAlreadyExists, DeliveryAlreadyReceived | 409 | Показать точный код отказа для этого входа; не менять исход отдельной неизвестной попытки. |
| InventoryUnavailable | 503 | Актуальная оценка недоступна. Чтение и складская оценка при PrepareOrder возвращают unavailable; изменяющий вызов — unknown. Этот status не доказывает отсутствие отдельной ранее начатой записи. |
| Недоступный upstream/timeout/невалидный результат изменяющего вызова | 502/504 либо ошибка клиента | Техническая неопределённость, пока сервис-владелец не установит результат. |

Gateway сохраняет уже полученный upstream status/body. Только собственная транспортная ошибка proxy получает его JSON error: code, message, outcome. outcome для изменяющего вызова — unknown; для чтения — unavailable. Семантические коды сервиса не выводятся из HTTP status без payload. Неожиданный Content-Type/сломанный JSON распознаётся клиентом как техническая ошибка; POST при этом остаётся неопределённым.

Resolve пассивно читает исходную попытку и отвечает ConfirmationPending либо установленным результатом. Он не вызывает commit. Только сохранённый определённый отказ владельца операции содержит outcome=not_committed. Успешный записанный заказ возвращается независимо от текущей неисправности очереди склада. Unseen/Pending, Lookup 404, ошибка чтения и истечение клиентского timeout не равны outcome=not_committed.

Точный повтор commandID/proposalID возвращает сохранённый результат или сохранённый отказ. После определённого отказа для нового оформления требуется новый commandID; после RequiresConfirmation новое предложение требует явного действия и нового commandID. После serving restart старое proposal может отсутствовать, поэтому ProposalNotFound имеет outcome=unknown. База данных и восстановление памяти после перезапуска не входят в текущий этап.

После неизвестного исхода frontend сохраняет исходные commandID/proposalID и неизменный запрос. Действие проверки вызывает resolve или точный повтор исходного ConfirmOrder; новый commandID до разрешения не создаётся. Для нового предложения после RequiresConfirmation нужен новый явный пользовательский ConfirmOrder и новый commandID. Черновик состояния попытки переживает reload в пределах браузерной сессии; потеря такого черновика не сообщает об отсутствии заказа. Это клиентская удобная связь с попыткой, не механизм восстановления serving после перезапуска.

## 4. Интеграционные сценарии и данные

Совместный модуль `tests/business` создаёт реальные warehouse и serving Runtime и связывает их factory bridge через публичные contract/bootstrap. Он проверяет бизнес-состояние, преобразование значений и ошибок между модулями, включая потерянный ответ commit, pending и запрет второй команды. Этот bridge не запускает HTTP-процессы.

`tests/http` запускает реальные frontend/Nginx, gateway, serving и warehouse процессы. Обычные сценарии отправляют запросы через публичный gateway и проверяют результаты по состоянию каталога, склада, меню и истории. Для предсказуемых сетевых сбоев отдельная принадлежащая запуску fault-сборка управляет forwarding, удержанием и потерей ответа. Бизнес-решения и запись состояния остаются у реальных сервисов.

| Сценарий | Реальное состояние и контроль | Проверяемый результат |
| --- | --- | --- |
| Пустой каталог и меню | Чистая память реальных процессов; без fault rule. | Пустые каталоги, меню, стоп-лист и остатки согласованы. |
| Каталог, доступность и FEFO | Продукт, блюдо и партии созданы через публичные маршруты; времена и количества заданы сценарием. | Доступность, выбор партии, остаток и записанное событие соответствуют warehouse state. |
| Сокращённое предложение и повторная оценка | Реальный запас меньше запроса; между оценками состояние меняется другим заказом, истечением партии или поставкой. | Предложение объясняет сокращение и не списывает запас; замена требует отдельного явного подтверждения. Рост запаса не восстанавливает удалённую строку. |
| Точный повтор и конфликт идентичности | Реальные delivery/order команды повторяются с исходным или изменённым payload. | Каждый точный повтор возвращает исходный результат без новой записи; изменённый payload с тем же ID получает конфликт. |
| Pending до commit | Fault control удерживает внутренний commit до отправки в warehouse; клиентский запрос ограничен по времени. | Resolve показывает pending/unknown; после освобождения той же попытки результат разрешается без второго commit. |
| Потеря ответа после commit | Fault control пересылает commit в реальный warehouse и отбрасывает ответ. | Resolve или точный повтор возвращает записанный заказ; история содержит одну продажу, остаток списан один раз. |
| Неопределённость transport | Fault control задерживает, обрывает или портит ответ между реальными процессами. | Для изменяющего запроса клиент сохраняет unknown до ответа владельца; ошибка не превращается в подтверждение или доказательство отсутствия записи. |

Все fixture-данные и fault controls остаются в тестовой сборке вне production-пакетов и образов. Приёмка фиксирует точные исходные revisions, image/container identities и JSON evidence. Runner использует собственные Compose projects и удаляет только принадлежащие запуску ресурсы. Команды и точная матрица сценариев остаются в [README фабрики](../README.md#http-acceptance); выбранные pins в `compose.yaml` не меняются этой спецификацией.

## 5. Frontend и Figma

Целевая структура frontend соответствует пяти слоям quality-ветки: независимый от фреймворка `src/domain`, сценарии и потребительские порты в `src/application`, HTTP-клиент и runtime-decoding в `src/adapters`, React-компоненты в `src/presentation` и сборка зависимостей в `src/app`. Компоненты получают типизированные результаты и не вызывают fetch или browser storage напрямую. Production API base ведёт только через gateway. Для Vite dev нужен proxy /api или единый gateway base; сейчас dev proxy отсутствует.

Нужны рабочие области: заказ/предложение/подтверждение, доступное меню/стоп-лист/рекомендации, продукты/блюда/рецептуры, поступления, остатки/сроки и история. Вход одного оператора с сессией и CSRF-токеном реализован в gateway (gateway#8, OpenAPI `1.0.0-draft.4`) и доступен в отдельной candidate-сборке. Исходный business handoff исключал аутентификацию; кабинет — отдельное дополнение. OTP-регистрация, OTP-вход и восстановление пароля остаются задачами [gateway #13](https://github.com/JustIkra/umir-gateway/issues/13), [feedback #2](https://github.com/JustIkra/umir-feedback/issues/2) и [gateway #12](https://github.com/JustIkra/umir-gateway/issues/12). Роли, несколько операторов и ресторанов и закупочный отчёт не добавляются. Сортировку рекомендаций и пригодность партий UI получает от сервиса.

[Design brief](frontend-design-brief.md) задаёт обязательные Figma components, экраны, состояния и evidence. Дизайн и OpenAPI входят в результат передачи. Текущий [Figma Screens](https://www.figma.com/design/6Y1Jglyvc1xVsTzII54A2u/UmirHack?node-id=42-2) — частичная основа, а не завершённая передача; полное покрытие и проверки прототипа перечислены в брифе и [frontend #1](https://github.com/JustIkra/umir-frontend/issues/1). Наличие названия frame, ссылки или успешной сборки не заменяет просмотр фактического дизайна.

## 6. Приёмка интеграции

| Область | Обязательные проверки |
| --- | --- |
| Business bridge | Совместные тесты вызывают реальные warehouse/serving Runtime через contract/bootstrap bridge. Отдельно проверяются результаты, ошибки, большие значения, временные метки, lineNo, события, FEFO, повтор команды и явное подтверждение сокращённого состава. Эта проверка не доказывает HTTP или browser behavior. |
| Gateway | Существующие unit-тесты с внедрёнными handlers/transport проверяют health и маршруты, URL, method/path/query/body/headers/status, escaped IDs, decimal strings, cancellation и ошибки. Они дополняют, но не заменяют приёмку процессов. |
| OpenAPI и тестовые данные | Запросы и ответы проверяются по той же API revision, включая варианты результатов, неверный ввод, date-only, большие int64, порядок и lineNo, schema version и content type. Точные значения вроде `9007199254740993` сохраняются через HTTP, декодирование и отображение. |
| Реальный HTTP | Factory запускает frontend/Nginx → gateway → serving → warehouse с точными revisions. Обычные сценарии проверяют фактическое состояние; отдельная fault-сборка управляет сетевыми сбоями. Auth/feedback кандидат проверяется только при релевантной работе над этими потоками. |
| Frontend state | Pending/unknown/confirmed различимы; исходные commandID, proposalID и запрос сохраняются при неопределённости; новый commandID не создаётся до разрешения; замена требует явного подтверждения; рост остатка не увеличивает состав; unavailable отличается от пустого каталога. |
| Browser/UI | После реализации экранов отдельный UI E2E проходит через опубликованный frontend/Nginx и всю реальную цепочку сервисов. Проверяются доставка, история, рекомендации, повтор после неопределённости, доступность клавиатуры и состояния ошибок. До появления UI нельзя заявлять browser acceptance. |
| Figma и сборка | Editable components, состояния и prototype просмотрены; desktop/tablet/mobile и keyboard/error/loading evidence приложены. Проходят Go format/build/lint/architecture/race, frontend unit/browser/Vite и Docker gates. Нужные runtime packages включены в production-образы, тестовые fixtures и fault controls исключены. Фактические frontend-команды записаны в README/CI владельца. |

Управляемые HTTP fault fixtures допустимы только для воспроизведения транспортного поведения. Тестовые подмены внешних SMTP/Telegram-провайдеров остаются в приёмке соответствующих интеграций. Команды фабрики остаются в [README](../README.md#http-acceptance). Для результата фиксируются точные revisions, API revision и Figma node revision; временные проекты очищают только принадлежащие им ресурсы. Результат candidate-проверки не меняет выбранные образы автоматически.

## 7. Граница подключения реальных сервисов

`tests/business` проверяет бизнес-правила вызовами реальных Runtime в памяти одного процесса; его bridge только переводит публичные значения и ошибки. `tests/http` проверяет HTTP-контракт на реальных процессах и управляемые transport faults. Эти уровни не доказывают browser behavior: UI E2E и доступность в работающем интерфейсе обязательны после реализации frontend. Сервисные проверки внутренних гарантий остаются ответственностью модулей-владельцев; factory fault proxy не заменяет их. Состояние бизнес-ядра и попытки хранятся в памяти: после перезапуска процессы не восстанавливают их. Смена исходных revisions не меняет выбранные production pins, пока отдельное изменение не обновит compose. Эта граница сохраняет ограничения раздела 11 business-core и не переносит бизнес-решения в gateway или frontend.
