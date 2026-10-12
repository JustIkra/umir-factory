# Спецификация передачи gateway и frontend

Дата: 2026-10-10. Часовой пояс: Europe/Moscow.

Статус на 2026-10-11: публичные gateway → serving → warehouse API работают по HTTP с процессной памятью. [README фабрики](../README.md#gateway-openapi) и [internal warehouse contract](api/warehouse-internal.yml) описывают транспорт и область restart-гарантий. Этот документ сохраняет требования к frontend, gateway границам и UI; экраны и Figma в текущий HTTP-этап не входят. [Business core](business-core.md) задаёт бизнес-правила, а [OpenAPI](api/openapi.yml) — публичный wire contract.

Постановка задачи, порядок начала работы, рабочие ветки и ссылки на PR находятся в GitHub issues. Этот документ хранит контракт и критерии результата; [задание на дизайн](frontend-design-brief.md) задаёт визуальные материалы. [Проектный review-skill](../skills/umir-pr-review/SKILL.md) используется при проверке будущих PR.

## 1. Владельцы и результат

| Область | Владелец | Результат |
| --- | --- | --- |
| Warehouse/serving business core | Разработчик ядра | Состояние, FEFO, события, предложения, идемпотентность и согласованный допуск. |
| Публичный wire contract | Интеграционный разработчик вместе с владельцем ядра | Версионированный OpenAPI в factory; транспортные поля и результаты сохраняют business-core. |
| Gateway | Интеграционный разработчик | Тонкая маршрутизация с внедряемыми HTTP-зависимостями, реальным proxy и проверками моков. |
| Frontend и Figma | Интеграционный разработчик | Клиент gateway, экраны, явно различимые результаты, editable Figma и проверенный браузерный поток. |
| HTTP-адаптеры реальных бизнес-сервисов | Разработчик ядра | Реализация того же wire contract, защита от поздней записи и восстановление pending. |
| Factory | Оба разработчика в пределах своих PR | Документация, контрактные fixtures и воспроизводимая проверочная сборка. |

Первый результат передачи — работающий frontend → gateway → mock serving/warehouse. Подключение реальных сервисов — отдельный результат после появления их HTTP-адаптеров. Готовность первого результата не означает готовность второго.

### 1.1. Стек и проверки frontend

Слайд 8 презентации организаторов рекомендует JavaScript или TypeScript со SCSS, React и Chakra; выбор языка остаётся за участником. Для frontend-задачи разрешены TypeScript, SCSS и Chakra UI в текущем React/Vite проекте. При выборе TS миграция JS-каркаса и интеграция выбранных инструментов выполняются в frontend PR; существующие JS-файлы могут сохраняться.

Разрешены openapi-typescript как dev-инструмент генерации типов публичного API, Vitest для unit-тестов и Playwright для браузерных/E2E-сценариев. При генерации типов используется точная принятая ревизия публичной OpenAPI. Рабочая редакция и опубликованная схема могут различаться; PR указывает commit фабрики и info.version использованной схемы. CI проверяет актуальность генерируемых типов и отдельно выполняет `tsc --noEmit`, если выбран TypeScript. Unit/browser проверки, Vite и контейнерная сборка входят в приёмку frontend.

Клиент проверяет JSON во время выполнения и полноту обработки `kind/type`. Неизвестный результат не становится успехом или доказанным отсутствием записи. Точные целые сохраняются строками или BigInt. Браузерные сценарии используют frontend → gateway → HTTP-моки; приёмка реальных сервисов сохраняется отдельным уровнем.

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

## 3. Wire contract v1 для моков и клиентов

Wire v1 сохранён в [docs/api/openapi.yml](api/openapi.yml), редакция `1.0.0-draft.4`. В ней кроме бизнес-операций описаны вход одного оператора, сессия и CSRF (gateway#8) и публичная форма обратной связи (gateway#10); их обслуживает кандидат `compose.auth.yaml`, выбранная сборка работает без входа до согласованной смены образов. Контракт описывает реализованные HTTP-сервисы и используется клиентами и моками. Изменение согласованных бизнес-полей требует сверки с владельцем ядра; транспортные уточнения фиксируются вместе со схемой. Фабрика закрепляет опубликованные HTTP-образы отдельными SHA и проверяет выбранную сборку. Условия приёмки реальных сервисов и ограничения памяти описаны в разделе 7.

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

## 4. Моки сервисов и самостоятельная разработка

Моки размещаются в factory: `tests/http/mockservices`, fixtures — `tests/http/fixtures`, запускаемый dev harness — `tests/http/cmd/mock-services`. Они предоставляют реальные HTTP handlers serving/warehouse на отдельных локальных адресах. Gateway выбирает эти адреса через существующие SERVING_URL/WAREHOUSE_URL. Frontend обращается к gateway и не переключается на прямой запрос к fixture.

Моки возвращают сценарные результаты по OpenAPI, записывают полученный запрос и сохраняют заданный результат попытки по commandID. Они не воспроизводят FEFO, политику срока или EventStore; сценарии задают ожидаемые ответы. Одного always-success stub недостаточно. Производственные src/internal и опубликованные runtime-образы не содержат mock/seed/fault switches.

| Сценарий | Что должно пройти через настоящий HTTP |
| --- | --- |
| empty-catalog | Пустые определения, меню и остатки; UI предлагает подходящее действие. |
| available | Каталог, доступные блюда, нормальное предложение, OrderConfirmed. |
| partial-order | Уменьшенные порции/удалённые строки и объяснения; явное подтверждение состава. |
| recheck-required | ConfirmOrder возвращает новое proposal; нет автоматического подтверждения замены. |
| empty-replacement/superseded | Пустую замену оформить нельзя; прежний proposal сообщает ID замены. |
| pending/lost-response | Mock сохраняет результат и обрывает ответ; resolve/точный повтор возвращает исходный заказ. |
| inventory-unavailable | Технический отказ подготовки, proposal остаётся активным; нет фиктивного success. |
| identity-conflict | Изменённый вход того же commandID возвращает конфликт; клиент не маскирует его. |
| large-values | quantity/stateVersion/sequence="9007199254740993" не округляются. |
| malformed/unreachable/delayed | Ошибочный payload, обрыв и конечное ожидание; для POST сохраняется неизвестность. |

В fixture рекомендаций одна порция использует две партии с одинаковым ближайшим expiresAt. Reasons сохраняют реальные количества и порядок productID/batchID. Fixture меню и стоп-листа с общим ингредиентом не обещает совместную доступность заказа.

В factory потребуется один документированный runner или Compose override для mock stack. Команда должна поднимать mock services, gateway и frontend, ждать health и выполнять сценарии, а завершение — удалять только созданные временные процессы/контейнеры без пользовательских данных. Выбранная production-сборка compose.yaml и её SHA не меняются ради demo.

## 5. Frontend и Figma

Frontend использует отдельный API-клиент в `src/api`; feature state получает типизированный результат, а компоненты — данные и callbacks. Raw status/error не трактуется каждой кнопкой самостоятельно. Production API base ведёт только через gateway. Для Vite dev задаётся proxy /api или единый gateway base; сейчас dev proxy отсутствует.

Нужны рабочие области: заказ/предложение/подтверждение, доступное меню/стоп-лист/рекомендации, продукты/блюда/рецептуры, поступления, остатки/сроки и история. Вход одного оператора с сессией и CSRF-токеном добавлен в gateway (gateway#8, OpenAPI `1.0.0-draft.4`); роли, несколько операторов и ресторанов и закупочный отчёт не добавляются. Сортировку рекомендаций и пригодность партий UI получает от сервиса.

[Design brief](frontend-design-brief.md) задаёт обязательные Figma components, экраны, состояния и evidence. Дизайн и OpenAPI входят в результат передачи. Figma создаёт разработчик; он предоставляет доступный файл и node links в issue/PR. Наличие названия frame, ссылки или успешной сборки не заменяет просмотр фактического дизайна.

## 6. Приёмка этапа с моками

| Область | Обязательные проверки |
| --- | --- |
| Gateway | Существующие health/маршруты, обе внедрённые зависимости, invalid URL, метод/path/query/body/end-to-end headers/status, escaped IDs, неизменные большие decimal strings, cancellation и failure. |
| OpenAPI/fixtures | Каждый маршрут и variant указан; invalid/date-only/large int64/order/lineNo проверены; fixtures валидируются той же схемой; schema version и content type определены. |
| Frontend state | Pending/unknown/confirmed различимы; same commandID для неопределённого повтора; замена требует явного подтверждения; рост остатков не увеличивает состав; unavailable отличается от пустого каталога. |
| HTTP/browser | Playwright или эквивалент проходит через frontend → gateway → mock HTTP services, включая потерю ответа после mock commit, каталог, поставку, историю и рекомендации. Проверка только mocked fetch не закрывает эту область. |
| Figma/UI | Editable components, состояния и prototype реально просмотрены; desktop/tablet/mobile и keyboard/error/loading evidence приложены. |
| Сборка | Go gofmt/test-race/vet, frontend unit/browser tests и Vite build, Docker build созданных production-пакетов. Новый тестовый скрипт frontend явно указан в README/CI. |

Go gateway Dockerfile сейчас копирует только cmd; новые internal пакеты должны войти в сборку. Тестовые данные остаются вне образа. Проверка Node/Go/Compose воспроизводима из свежего checkout; пропущенные проверки указаны точно. PR связывают issue и фиксируют baseline SHA, API revision, Figma node revision и рассмотренные режимы. Review выполняется до merge владельцем проекта; успешная проверка не меняет права main или выбранные образы автоматически.

## 7. Граница подключения реальных сервисов

Factory проверяет этот HTTP contract на реальных процессах и проверяет H01–H25 по отдельным уровням evidence. Warehouse хранит исход попытки и обеспечивает, что поздний точный запрос не изменит сохранённый результат. Resolve не подтверждает commit. Гарантии действуют, пока процессы-владельцы памяти живы; постоянное восстановление после рестарта остаётся отдельным этапом.

Сервисные тесты отдельно проверяют счётчик Owner-вызовов H09 и барьер после резервации H16. Factory proxy не выдаётся за доказательство этих внутренних переходов. Browser/Figma приёмка, постоянное хранение и восстановление после потери процесса остаются вне текущего этапа. Эта граница сохраняет ограничения раздела 11 business-core и не переносит бизнес-решения в gateway или frontend.
