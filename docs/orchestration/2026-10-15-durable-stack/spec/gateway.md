# Подспека G-gateway: umir-gateway в durable-стеке

Приведено к решениям C1–C40 главной спеки (раздел 10), 15.10.2026.

Главная спека: `00-orchestration.md` (дальше «главная», ссылки на разделы и правила U*/R* по её номерам). Подспека уточняет её и не отменяет. Репозиторий: `umir-gateway`, база `origin/main` `1c8604d`. Группа записывает свежий SHA в журнал.

Источники группы (читать по необходимости):
- план: `scratchpad/plans/umir-gateway-25.md`; отчёт ревью: `scratchpad/main-review/gateway.md` (ниже «ревью §3» — раздел «Замечания»); опись заготовок: `scratchpad/main-review/stalled-persistence.md`;
- наблюдаемость и request ID: `umir-factory/docs/observability.md` §2;
- общие контракты и эталон адаптеров: DUR1 и `umir-factory/reference/durable/` (готовит G-factory, волна 0). До их публикации группа работает по заглушкам и ожиданиям из раздела 6.

## 1. Цель и границы

Gateway — браузерный BFF: cookie, Origin/CSRF, маршрутизация `/api/v2/...` к Auth и владельцам, сводка организации, живые уведомления SSE и единственный планировщик просрочки (R6).

Чем владеет после этой работы:
- допуск на входе: csrf/preauth, вход, начало регистрации и сброса пароля, анонимная обратная связь, prepare/confirm заказа, аренды SSE (R10, R11). Все бюджеты «по клиенту» на публичных входах Auth держит Gateway (C12, C26); бюджеты по логину, адресу, preauth и глобальные держит Auth в Postgres, заголовка с ключом клиента в Auth нет;
- live: приём событий Warehouse (`umir.events`) и уведомлений Auth (`umir.auth`) из RabbitMQ и раздача SSE (R4, R17);
- планировщик просрочки под арендой Redis (R6);
- готовность по возможностям; сквозной request ID.

Чем не владеет:
- бизнес-данными и командами (их хранят Warehouse, Serving, Feedback); идентичностью и жизненным циклом ресторанов (Auth);
- **собственной базой.** Postgres в Gateway не используется (R4, U1). Таблиц, RLS, ролей, миграций, outbox, inbox и generation хранилища нет, поэтому C4, C5, C8, C9 (`migrate`, `generation rotate`), C18 и C23–C25 к Gateway не применяются; подкоманда `serve` есть (W1.11);
- отправкой в брокер. Gateway только потребитель. Publisher confirms, outbox relay и аренда relay — у владельцев;
- Redis cache. Gateway его не использует: нет измеренной потребности, а реплей-источником он быть не может (R4); сейчас кеш нужен только Warehouse, кеш чтений решается на G2 по замерам R13 (C27). Реестр держится в памяти (раздел 5).

## 2. Исходное состояние

`origin/main` `1c8604d`; `go build`, `vet` и `go test -race` проходят (411 тестов верхнего уровня). `make check` требует `make tools`.

Два режима, выбор по `AUTH_URL` (`cmd/server/main.go`, `newComponents`):
- **Auth-режим** (`cmd/server/auth_components.go`): это целевой код. Он собирает фасад Auth (`internal/operatorhttp/remote.go`, `facade.go`), scoped-маршруты (`internal/httpapi/scoped.go`, `scoped_summary.go`), SSE (`internal/live/handler.go`), журнал (`internal/journal/scoped.go`), планировщик (`internal/expiration/scoped.go`) и приватный listener (`internal/httpapi/runtime.go`, `:8086`). Используется в `umir-factory/compose.completion.yaml`.
- **Старый режим** (без `AUTH_URL`): оператор в файле, `/api/live`, прокси `/api/serving|warehouse`. Около 3,3 тыс. строк. Пакеты и файлы:
  - `internal/operator`, `internal/adapters/{accountfile,argon2id}`, `internal/live/{streams,sessions,upstream}.go`;
  - `internal/journal/journal.go`, `internal/expiration/runner.go`, `internal/httpapi/router.go`;
  - `internal/operatorhttp/{api,guard,session,profile,feedback,live,activity}.go`;
  - команды `hash-password` и `clear-sessions`.

  Пины factory на старый режим (раздел 5.8; factory выводит legacy-профили из CI и README в волне 3, C28): `compose.yaml`, `compose.auth.yaml`, `compose.feedback.yaml`, `compose.dev.yaml`, `tests/http/cmd/acceptance/{live,realtime,auth}.go`.

Ключевые места кода, на которые опираются задачи (проверены чтением):
- готовность: `httpapi/runtime.go:223-251` (`ready()`), `authclient/registry_cache.go:39-48` (снимок годен 5 с), `authclient/scheduler.go:13-25` (сбой выборки стирает снимок), `expiration/scoped.go:149-188` (`tick` читает реестр раз за итерацию, дальше последовательный цикл);
- слоты: `httpapi/runtime.go:198` (живые и закрытые вместе, потолок 10 000), `journal/scoped.go:69-79` (`get` сразу выделяет кольцо), `:128` (`CloseScope` и `:231` `ObserveRuntime` тоже создают scope), `auth_components.go:48` (`maxScopes` 10 000, ёмкость кольца берётся из `JOURNAL_CAPACITY`);
- SSE: `live/handler.go:56-77` (счётчики 8 на сессию и 64 всего, значения из `live/streams.go:23-24`), `:163-218` (перепроверка Auth каждые 4 с и на каждое изменение);
- локальный Append: `httpapi/scoped.go:492-511` (`publishChange`), `expiration/scoped.go:255,266`;
- допуск: `operatorhttp/registration_limit.go:84-105` (ключ `r.RemoteAddr`), вызов в `facade.go:76`; анонимная обратная связь `httpapi/scoped.go:142-152` без допуска; `authclient/scoped.go:52-69` (`CheckCSRF` вызывает выдачу preauth без cookie);
- сводка: `httpapi/scoped_summary.go:22-83` (последовательно, 5 с на ресторан, дедлайна на запрос нет), `:78` (`evaluatedAt` в TZ процесса), `:102` (32 MiB на отчёт);
- request ID: `httpapi/problem.go:25-37` (ID только в контексте, заголовка ответа нет), `httpapi/scoped.go:284-303` (`scopedHeaders` пропускает `X-Request-ID` браузера как есть).

Из заготовок prod-readiness (`_wt/prod/umir-gateway`, только читать и копировать):
- **Брать с доработкой:** `internal/adapters/rabbitmq/{live.go,lru.go,live_test.go}` и `Scoped.ResetAll`. Заготовка читает собственную форму сообщения (`id`, `restaurantID`, `topics`), а не конверт каталога, и подписана только на `warehouse.#`; так и остаётся (C38: единственный производитель `umir.events` — Warehouse, `serving.#` не привязывается). Переписать под конверт и ключи каталога (`warehouse.*`). Остальное оставить: очередь exclusive auto-delete, ack после применения, сброс всех колец при переподключении. Проверка `restaurantID` — через контрактный `EntityID` (`ValidateScopeWire("EntityID", …)`, как в `runtime.go:78`), а не жёсткий UUID. `amqp091-go` сделать прямой зависимостью. Соединение (`connection.go`) брать из эталона R15; если эталон готов раньше — заготовку не копировать вовсе.
- **Не брать:** два коммита ветки (csrf, тест live): они уже в `main` (`e677d0e`, `870f0de`). Журнал и учётную запись оператора в Postgres не начинали и не нужны.

## 3. Решения

**Принимается из issue #25 без изменений:**
- разделение готовности и планировщика;
- один общий дедлайн сводки с ограниченным параллелизмом, явным partial и сохранением порядка ресторанов и проверок identity отчёта;
- конфигурируемые лимиты SSE с честным допуском по организациям;
- Redis-допуск для csrf, входа, регистрации, сброса пароля, анонимной обратной связи, прочих бюджетов и SSE-аренд (с HMAC-ключами и отпечатком попытки);
- request ID к Auth и владельцам и санитарные структурные логи отказов;
- доверенная цепочка прокси;
- события несут только идентификаторы и темы;
- дубликат, разрыв и истёкший курсор дают `refresh-required`;
- Broker и Auth fail-closed;
- ни одного изменения публичных путей, тел, статусов, CSRF/Origin, свежих решений Auth и «одной отправки без повтора».

**Меняется правилами:**

| Пункт issue | Правило | Итог |
| --- | --- | --- |
| «Auth отличает доступность Gateway от прогресса планировщика» | R3 | Auth вообще не вызывает Gateway. Приватные `runtime\|registry` маршруты удаляются по порядку совместимости (5.6, C7). |
| «Общее состояние курсоров, storage generation, PostgreSQL для метаданных реплея» | R4, R2 | Нет. Реплей — кольцо в памяти реплики. Курсор несёт `epoch` = случайный boot-nonce процесса (C19, не `INSTANCE_ID`) и generation кольца (формат курсора и событий SSE на проводе не меняется). Postgres и Redis cache не участвуют. |
| Мониторинг сроков «Warehouse owns expiry schedules» | R6 | Gateway по-прежнему запускает просрочку. Warehouse отдаёт список ресторанов с наступившими сроками `GET /internal/warehouse/v2/expiry-due?asOf&limit&after` (токен `expiry.due` уровня ячейки, без ресторана) и `nextDueAt` в ответе `expiry-runs` (C11, C22). |
| Реестр целиком в снимке на 5 с | R5 | Реестр по ревизии (C13): список `limit/after` с `ETag`/304 и `?since=<revision>` (изменения, включая надгробия); точечное чтение `/registry/{restaurantID}` с `If-None-Match`. Список отдаёт только живые рестораны. Точные пути — в DUR1. |
| Барьер непрерывности в Postgres | R7 | У Gateway базы нет. Строгий режим при потере маркера limiter реализует эталон R15. Маркер `umir:limiter:generation` пишет задание factory `redis-init` (C17); Gateway лишь сравнивает его с `GATEWAY_LIMITER_GENERATION`: расхождение или отсутствие включает строгий режим. |
| Бюджеты | R10 | Числа из таблицы DUR1 (на G1). В коде значений нет: только конфигурация. Бюджеты «по клиенту» на входе, регистрации и сбросе пароля — у Gateway (C12, C26). |
| Клиентский IP | R11 | Gateway принимает `X-Forwarded-For` только от сети frontend (`GATEWAY_TRUSTED_PROXY_CIDRS`; выставляет factory, проверяет `preflight`, C34). |
| Лимит 10 000 «записей и надгробий вместе» | R14 | Живые рестораны и надгробия считаются раздельно, надгробия — компактное множество вне слотов. Строка issue «out of repository / unverified» про регистрацию неверна: верна строка «Confirmed». |
| Уведомления жизненного цикла Auth | R17 | Gateway подписывается на `umir.auth` (имена событий — C1) и по уведомлению сразу перепроверяет доступ и закрывает scope: по `deleting` сразу перестаёт обслуживать, по `deleted` очищает. Уведомление прав не даёт. |
| Запасной путь | R20 | Нет запасного пути «Redis недоступен → процессный счётчик» и «брокер недоступен → локальный Append». |

**Снято R1 (чистый лист):** переноса данных у Gateway и так нет. Из прошлого остаются только удаляемые вещи: файл оператора `account.json`, том `/data/operator`, команды `hash-password` и `clear-sessions`. Их не мигрируют, а удаляют вместе со старым режимом. Пункт issue «документировать backup/restore PostgreSQL» к Gateway не относится: он у factory и владельцев.

**Вне объёма:**
- Prometheus `/metrics`, OpenTelemetry-трассы и вообще эндпоинт метрик (C33): его нет, переменных `GATEWAY_METRICS_ADDR(ESS)` нет. Счётчики допуска (без ключей и субъектов) идут в структурные логи, состояние возможностей — в тело `/readyz` (C32); приёмка читает логи, `INFO` Redis и management API RabbitMQ;
- замена `CommandIdentityConflict` → `CommandConflict` в Serving (волна 1) и Warehouse (волна 2, через DUR1), C29. Регрессию на границе (enum `Error` в `scoped-contract.json`) ведёт Gateway: W1.12;
- лимит 1024 preauth в Auth (`umir-auth …/preauth.go:55`; Gateway лишь не провоцирует его, см. W1.7);
- очистка протухшей session-cookie на любом scoped-маршруте (ревью §3, мелкое, п. 4): меняет поведение cookie, решает frontend;
- сворачивание пачки изменений в одно SSE-событие: перепроверка Auth перед каждым событием — свойство безопасности (`TestG4RevalidatesBeforeEveryChangeAndStopsReplay`), не меняется. Рост стоимости ограничивает малое кольцо;
- ретраи бизнес-команд в Gateway: их нет и не добавляются.

**Решение по временному процессному лимиту анонимной обратной связи (Ruling этой подспеки, подтверждено C31).** Допустимо **только в волне 1**, при условиях:
1. Он включается вместо отсутствующего допуска, а не как замена Redis при его отказе. R10 запрещает «запасной пустой лимитер», R20 запрещает путь «хранилище недоступно → память». Здесь хранилища ещё нет, а в волне 2 Redis-допуск вводится без запасного пути.
2. Файл помечен `TEMPORARY` и удаляется в том же коммите, который вводит Redis-допуск этого маршрута. Тест волны 2 проверяет, что в `internal/` нет процессных лимитеров допуска (кроме потолка ресурса из 5.4).
3. Волна 1 не выкатывается отдельно (U5): образ с процессным лимитом на стенд не попадёт. Откат — старые образы на старых томах (R1). Умножения квоты «на число реплик» поэтому не будет.
4. Ключ по клиентскому IP включается только при заданном `GATEWAY_TRUSTED_PROXY_CIDRS`. Без него ключ — это адрес frontend, и общий бюджет заблокировал бы всех. В этом случае действуют только бюджеты по preauth и по процессу.

## 4. Волна 1 — независимые правки

Ветка `fix/durable-w1-gateway`. Правки только на текущем коде, без Redis и RabbitMQ и без изменения публичного и приватного контрактов; контрактные изменения — в волне 2 через DUR1. Каждая задача: тест сначала красный, затем исправление. Новые переменные имеют префикс `GATEWAY_`; существующие без префикса остаются.

**W1.1. Готовность не зависит от планировщика (критичное C01).**
- Что не так: см. раздел 2 (`runtime.go:223-251`, `registry_cache.go:44`, `scheduler.go:19-23`, `expiration/scoped.go:149-188`). Итерация дольше 5 с или `EXPIRATION_RUN_INTERVAL` ≥ 5 с делает `ready=false`, и Auth отвечает `RuntimeNotReady` на все авторизации.
- Что сделать:
  - отдельная горутина `RegistryRefresher` (владелец, отмена, ожидание завершения — как у планировщика) выбирает реестр раз в `GATEWAY_REGISTRY_REFRESH_INTERVAL` (по умолчанию 2 с, 1–30 с);
  - неудачная выборка не стирает последний полный снимок: в `authclient` хранятся снимок и время последнего успеха;
  - `ready()` читает снимок возрастом до `GATEWAY_REGISTRY_MAX_AGE` (по умолчанию 30 с, не меньше 3 интервалов);
  - планировщик берёт реестр у refresher, а не делает свою выборку;
  - сводка планировщика (последний старт, успех, длительность, число подряд идущих сбоев) читается через `ScopedRunner.Health()` и отражается в новом `GET /readyz` (на публичном listener рядом с `/healthz`; frontend его наружу не проксирует). Тело по C32 и C49: `{instance, capabilities: {<имя>: ok|degraded|down}}`, поля `generation` у Gateway нет; пауза итерации даёт `scheduler: degraded`, подробности (последний старт, длительность, сбои подряд) — в структурном логе. `instance` — новая `GATEWAY_INSTANCE_ID` (по умолчанию случайный UUID; factory задаёт её уникальной у каждой реплики, C19). Статус 200, пока готовы `http` и (с волны 2) `limiter`; состояние планировщика на готовность не влияет. `GET /healthz` — процесс жив. Команда `healthcheck` образа и healthcheck compose смотрят `/readyz`; подпутей `/readyz/*` нет (C32).
- Красные тесты (`cmd/server`, по образцу `g5_runtime_test.go`):
  - `TestW1ReadyIgnoresSchedulerPace`: планировщик заблокирован или тикает раз в 30 с, часы +10 с, приватный `GET runtime` даёт `ready=true`;
  - `TestW1ReadySurvivesOneFailedRegistryFetch`: после одного сбоя выборки `ready` остаётся `true`;
  - `TestW1ReadyFalseAfterMaxAge`: после `MAX_AGE` без успешной выборки `ready=false`;
  - `TestW1SchedulerHealthShowsLag`: пауза итерации даёт `capabilities.scheduler: degraded` в `/readyz`, `ready` и статус 200 не меняются;
  - `TestW1ReadyzBodyShape`: тело ровно `{instance, capabilities}`, без адресов и секретов.
- Готово: цепочка C01 разорвана. Задержка или сбой итерации дольше 5 с не закрывает бизнес-маршруты любого ресторана, `ready` восстанавливается без перезапуска. Запрет `EXPIRATION_RUN_INTERVAL` ≥ 5 с не нужен.

**W1.2. Планировщик: пул, бюджет итерации, кеш токенов, регистрация без полной выборки (высокое).**
- Что не так: последовательный цикл `expiration/scoped.go:179-187`, два вызова на ресторан без кеша токена (`due`, `:226-247`); на каждый `PUT …/registry` полная выборка реестра `httpapi/runtime.go:136` (на 10 000 ресторанов 0,8 с CPU на проверку, ревью §3).
- Что сделать:
  - пул `GATEWAY_EXPIRATION_CONCURRENCY` (по умолчанию 8) и бюджет итерации `GATEWAY_EXPIRATION_ITERATION_BUDGET` (по умолчанию 10 с). Не уложенное в бюджет откладывается со сдвигом начала, чтобы не морить хвост;
  - кеш `ServiceToken` по (ресторан, эпоха склада) до `exp − 10 с` (токен живёт 60 с, `service_token.go:65`);
  - `PUT …/registry` использует снимок refresher, если он не старше 2 с и содержит запись ресторана. Иначе одна выборка через singleflight. Расхождение со снимком (версия, настройки) перед ответом 409 или 410 перепроверяется выборкой.
- Красные тесты: `TestW1SchedulerRunsDueWithinBudgetAndConcurrencyBound` (`internal/expiration`: 200 ресторанов по 10 мс, не больше 8 одновременных, итерация короче секунды); `TestW1SchedulerRotatesStartWhenBudgetExhausted`; `TestW1ServiceTokenIsReusedWhileValid`; `TestW1RegisterReusesFreshSnapshotAndCoalescesFetch` (`cmd/server`: 100 последовательных `PUT` дают не больше одной выборки за 2 с; параллельные `PUT` неизвестного ресторана дают одну).
- Готово: итерация 1200 ресторанов по 5 мс укладывается в секунды (проба ревью: 6,8 с); `go test -race` чист.

**W1.3. Слоты и кольца (высокое + среднее).**
- Что не так: см. раздел 2 (`runtime.go:198`, `journal/scoped.go:69-79,128,231`, `config.go:34-42`): 10 000 scope без изменений дают 317 МиБ (проба ревью), закрытые рестораны занимают слот и блокируют готовность и новые рестораны.
- Что сделать:
  - кольцо выделяется лениво при первом `Append`; `ObserveRuntime`, `CloseScope`, `Watch` кольцо не создают;
  - ёмкость кольца — отдельная `GATEWAY_LIVE_RING_CAPACITY` (по умолчанию 128, 16–4096); `JOURNAL_CAPACITY` остаётся только старому режиму;
  - generation нового кольца берётся из счётчика процесса, а не `1` (иначе старый курсор совпадёт с пересозданным кольцом);
  - закрытые рестораны — компактное множество вне лимита живых (до 10 000 по контракту), `runtimeEntry` считается раздельно: до 10 000 живых и до 10 000 закрытых.
- Красные тесты: `TestW1ObserveAndCloseAllocateNoRing` (10 000 scope, куча < 2 МиБ); `TestW1ClosedScopesDoNotUseLiveSlots` (воспроизводит пробу: три закрытия при `maxScopes=3`, затем `Append` нового ресторана успешен); `TestW1RuntimeAcceptsTenThousandLiveAndTenThousandClosed`; `TestW1RecreatedRingNeverAcceptsOldCursor`.
- Готово: рост памяти пропорционален активным scope; существующие тесты `TestG4*` зелёные.

**W1.4. Лимиты SSE из конфигурации (высокое).**
- Что не так: 64 потока на весь процесс и 8 на сессию зашиты (`live/streams.go:23-24`, `auth_components.go:52`); один арендатор занимает все 64.
- Что сделать: `GATEWAY_LIVE_MAX_STREAMS` (по умолчанию 64), `…_PER_SESSION` (8), `…_PER_ORG` (по умолчанию 32, то есть половина общего: один арендатор не может занять процесс). Занятие слота организации идёт после первого `AccessCheck` (организация известна только из него; слот сессии занимается до него, как сейчас). Отказ — тот же `429 RateLimited` с `Retry-After: 1`. Значения таблицы DUR1 (8 на сессию, 200 на организацию, 2000 на ячейку) предварительные (C36): нагрузку `AccessCheck` на Auth измеряют в R13, числа утверждаются на G1 и попадают в конфигурацию через аренды волны 2; в волне 1 рост общего лимита — решение оператора с оглядкой на нагрузку на Auth (≈0,33 вызова `AccessCheck` в секунду на поток).
- Красные тесты (`internal/live`): `TestW1StreamLimitsComeFromOptions`; `TestW1OrgCapDoesNotStarveOtherOrganization` (организация A упёрлась в лимит, B допускается); `TestW1ReleaseFreesSessionOrgAndTotalOnEveryExit` (отмена, ошибка, завершение).
- Готово: ни одно значение лимита не зашито в `live`; константы `Default*` остаются только как значения по умолчанию конфигурации.

**W1.5. Доверенный клиентский адрес и регистрация (среднее).**
- Что не так: `registration_limit.go:91-98` берёт `RemoteAddr`, то есть адрес frontend Nginx; 30 попыток за 10 мин общие для всех (проба ревью: настоящий пользователь ждёт 9 мин 59 с).
- Что сделать: чистый пакет `internal/clientaddr` (`Resolver{Trusted []netip.Prefix}.Client(r)`) и `GATEWAY_TRUSTED_PROXY_CIDRS`. Правило: если пир не в списке — адрес пира, заголовки игнорируются. Если пир в списке — идём по `X-Forwarded-For` справа налево и берём первый адрес не из списка; пустой или нечитаемый заголовок — адрес пира. IPv6 агрегируется по `/64`. Без конфигурации поведение прежнее. Переменную выставляет factory (подсеть frontend), проверяет `preflight`; ей соответствует `UMIR_TRUSTED_PROXY` на стороне frontend Nginx (C34). Остальное в `registrationLimits` (30 на клиента, 3 на preauth, дедупликация по отпечатку, проверка тела до допуска) не меняется; бюджеты по клиенту на входе, регистрации и сбросе пароля с числами DUR1 вводятся в волне 2 (5.3).
- Красные тесты: таблица `clientaddr` (подделка слева от доверенного, недоверенный пир, IPv6, одиночный и составной заголовок); `TestW1RegistrationBudgetIsPerClientBehindTrustedProxy` (два клиента через один доверенный пир имеют раздельные бюджеты); существующий тест `TestRegistrationHTTPAdmissionRejectsBeforeAuthAndIgnoresForwardedSpoof` остаётся зелёным без конфигурации и получает пару с конфигурацией; невалидное тело по-прежнему вне бюджета.
- Готово: поддельный `X-Forwarded-For` от недоверенного пира не меняет ключ. Совместная работа с frontend (R11, перезапись заголовка) проверяется в волне 3.

**W1.6. Временный допуск анонимной обратной связи (высокое, C31).**
- Что не так: `httpapi/scoped.go:142-152` — проверка CSRF и `AnonymousToken`, лимита нет; старый режим ограничивал (5 на контекст, 60 на процесс за 10 мин), регресс.
- Что сделать: порт допуска у потребителя (`httpapi`), временная процессная реализация в `internal/admission/temporary_memory.go` под условиями раздела 3. Порядок: валидация схемы → Origin/CSRF → допуск → `AnonymousToken` → отправка. Бюджеты: 5 на preauth за 10 мин, 20 на клиента за час (только при доверенных прокси), 200 на процесс за час; повтор с тем же отпечатком `sha256(preauth‖тело)` не списывается. Отказ (C16): `429`, код `RateLimited`, `Retry-After` и `details.retryAfterSeconds` (в `writeFailure` поле `RetryAfter` уже превращается в заголовок; `details.retryAfterSeconds` схема `Error` допускает, но `writeFailure` его не заполняет, это добавляется здесь).
- Красные тесты (`cmd/server`, по образцу `feedback_test.go`): `TestW1AnonymousFeedbackLimitedPerPreauthBeforeAuthToken` (шестой запрос — 429, `AnonymousToken` и Feedback не вызваны); `…RepeatedFingerprintNotCharged`; `…InvalidBodyOutsideBudget`; `…RotatingPreauthHitsProcessBudget`; `…ClientBucketNeedsTrustedProxy`.
- Готово: бот с циклом «csrf → POST» упирается в 429, Feedback и Auth не получают лишних вызовов.

**W1.7. `CheckCSRF` не выдаёт preauth (высокое, сторона Gateway).**
- Что не так: `authclient/scoped.go:52-69` при пустых session и preauth вызывает `CSRF`, и Auth выпускает новый preauth (расходует 1024 записи Auth); ответ клиенту всё равно 403.
- Что сделать: оба cookie пусты → сразу `403 InvalidCsrfToken` / `not_committed`, без вызова Auth.
- Красный тест: `TestW1CheckCSRFWithoutCookiesDoesNotCallAuth` (`internal/adapters/authclient`, транспорт считает вызовы).
- Готово: ответ клиенту байт в байт прежний, вызовов Auth ноль. Лимит выдачи по клиентскому IP — волна 2.

**W1.8. Сводка организации: общий дедлайн и память (среднее).**
- Что не так: `scoped_summary.go:27-66` — сначала до 100 последовательных `AccessCheck`, потом последовательный обход с 5 с на ресторан; общего бюджета нет, память не ограничена (32 MiB на отчёт × 200). Итог не сверяется со схемой `OrgSummary`.
- Что сделать:
  - `GATEWAY_SUMMARY_DEADLINE` (по умолчанию 20 с; меньше `proxy_read_timeout 30s` из `umir-frontend/nginx.conf:27`), общий контекст запроса;
  - `GATEWAY_SUMMARY_CONCURRENCY` (по умолчанию 4) для проверок доступа и для отчётов; порядок элементов в ответе — порядок `restaurantID` запроса;
  - общий бюджет байт `GATEWAY_SUMMARY_MAX_BYTES` (по умолчанию 64 МиБ) на все отчёты запроса;
  - ресторан, не уложившийся в дедлайн или в бюджет, получает `status: "unavailable"` и `error` с кодом `UpstreamUnavailable`, `outcome: "unavailable"`; общий статус `partial` или `unavailable`, `complete` только при полном успехе;
  - проверка доступа: любой отказ по-прежнему проваливает весь запрос до бизнес-вызовов (`TestG2SummaryFailsWholeCrossOrgBeforeBusinessDispatch`);
  - итог проверяется по `OrgSummary`; `evaluatedAt` форматируется в `Europe/Moscow`, а не в TZ процесса.
- Красные тесты (`cmd/server/scoped_summary_test.go`): `TestW1SummaryRespectsOverallDeadlineAndKeepsOrder`; `TestW1SummaryBoundsConcurrentOwnerCalls`; `TestW1SummaryByteBudgetMarksRemainingUnavailable`; `TestW1SummaryMatchesOrgSummarySchemaAndMoscowInstant`. Существующие `TestG2Summary*` зелёные.
- Готово: 100 ресторанов с медленным владельцем отвечают не позже дедлайна, память ограничена.

**W1.9. Request ID и диагностика отказов (среднее).**
- Что не так: `problem.go:25-37` кладёт ID только в контекст и не отдаёт заголовок; `scopedHeaders` передаёт владельцам `X-Request-ID` браузера как есть; Auth его не получает; `Cause` отказов нигде не пишется (`scoped.go:454-476`). Требование `observability.md` §2: новый ID на внешний запрос, заголовок в ответе (в том числе с ошибкой) и в запросах к внутренним сервисам.
- Что сделать:
  - `WithRequestID` игнорирует входящий заголовок и выставляет `X-Request-ID` в ответе;
  - `scopedHeaders` и клиент Auth (`authclient/client.go` `exchange`, `scoped.go` `scopeExchange`) пишут этот ID в исходящие запросы (ID берётся из контекста);
  - на каждый отказ 502/503/504 из Auth или владельца пишется одна структурная запись (`log/slog`, JSON): `request_id`, `owner`, операция, `restaurant_id`, статус, класс причины (`timeout`, `transport`, `invalid_response`, `schema`, `scope_mismatch`, `upstream_status`) и длительность. Текст `Cause`, адреса, cookie, токены, тела и IP клиента в запись не попадают;
  - фоновые запуски планировщика ID не получают (у них свой `run_id`).
- Красные тесты (`cmd/server`): `TestW1RequestIDIsFreshReturnedAndForwarded` (браузерный `X-Request-ID` затёрт; один и тот же ID виден в ответе, у фейкового Auth и у владельца; в том числе в ответе с ошибкой); `TestW1UpstreamFailureLogHasReasonButNoSecrets`.
- Готово: массовый 502 после расхождения схемы виден в логе с причиной; `umir-factory/docs/observability.md` §2 выполнен.

**W1.10. Мелкое.** Каждое — отдельный маленький коммит с тестом:
- `Accept-Encoding: identity` в запросах владельцам (`scopedHeaders`), чтобы будущее сжатие не превратило все ответы в 502 (ревью, мелкое п. 5);
- дедлайн чтения тела на scoped-маршрутах, кроме `live`, из `UPSTREAM_TIMEOUT` (мелкое п. 1; `http.Server.ReadTimeout` не ставить: он обрывает SSE);
- тест фиксирует нулевой допуск `iat` (`service_token.go:65`) как явное поведение (R12; допуск не вводится);
- CI: `-timeout` в `scripts/go-quality.sh:69` и `timeout-minutes` в задании `ci.yml`; повторяемые SSE-регрессии добавить для боевого `live.Handler` (шаг `ci.yml` сегодня гоняет тесты старого `live.Streams`, это нужно до удаления старого режима).

**W1.11. Сервисные ключи из файлов (C14) и подкоманда `serve` (C9).**
- Что не так: `internal/config/config.go:112,124,136` читает `AUTH_SERVICE_KEY`, `SERVING_SERVICE_KEY`, `WAREHOUSE_SERVICE_KEY`, `FEEDBACK_SERVICE_KEY`, `AUTH_RUNTIME_SERVICE_KEY` только из окружения; тест утечки секретов durable-профиля иначе не пройдёт.
- Что сделать: для каждого ключа вариант `<ИМЯ>_FILE` (файл читается целиком, завершающий перевод строки отбрасывается; прежние правила непустоты и «без пробелов» сохраняются). Заданы и переменная, и файл — ошибка старта (как у эталонного `LoadDSN`, C10). Значение ключа не попадает в сообщения об ошибках и логи. Прежние переменные остаются работать (legacy-профили, C28). Подкоманда `serve` — явный синоним запуска по умолчанию; `migrate` и `generation rotate` у Gateway нет (базы нет).
- Красные тесты (`internal/config`, `cmd/server`): `TestW1ServiceKeyFromFile`; `TestW1ServiceKeyBothSourcesIsError`; `TestW1ServiceKeyErrorDoesNotEchoSecret`; `TestW1ServeSubcommandStartsServer`.
- Готово: durable-профиль передаёт все ключи Gateway только файлами; список секретов factory и бэкап дополняются `GATEWAY_ADMISSION_KEY`, `GATEWAY_LIVE_CURSOR_KEY` и limiter generation (раздел 5.4).

**W1.12. Регрессия enum ошибок на границе (C29).**
- Что не так: `validateResponse` (`httpapi/scoped.go:320-340`) проверяет любой ответ владельца 4xx/5xx по схеме `Error`; код вне enum превращается в 502. В enum сейчас есть `CommandConflict`, `LimitExceeded`, `RateLimited`, `ServiceUnavailable`, но нет `CommandIdentityConflict`. Serving переходит на `CommandConflict` в волне 1, Warehouse в волне 2, Auth отдаёт его при повторе после очистки результата (409, `outcome: unknown`).
- Что сделать: только тесты; код меняется, лишь если тест покажет расхождение схемы. Тесты могут сразу быть зелёными и служат замком при `make sync-contract`.
- Тесты (`cmd/server`, `internal/httpapi`): `TestW1BoundaryPassesCommandConflictFromServingWarehouseAndAuth` (409, `outcome` без изменений; Auth — через фасад); `TestW1BoundaryPassesLimitExceededAndInvalidArgumentOnOrderLimits` (C15: 409 `LimitExceeded` и 400 `InvalidArgument`, оба `not_committed`); `TestW1BoundaryPassesServiceUnavailableAndRateLimitedWithRetryAfterSeconds` (C16); `TestW1BoundaryTurnsUnknownErrorCodeInto502` (код вне enum остаётся 502, поэтому Serving и Warehouse обязаны перейти на `CommandConflict`).
- Готово: любой из названных кодов проходит границу, и расхождение enum после `sync-contract` ловится тестом.

## 5. Волна 2 — состояние, брокер, Redis

Начинается после G1 и волны 1, ветка `feat/durable-gateway`. Хранилища нет, поэтому раздел описывает память, Redis и брокер.

### 5.1. Состояние и границы

| Данные | Где | Граница | Срок / вытеснение |
| --- | --- | --- | --- |
| Кольцо изменений ресторана `{seq, topics}`, generation, ревизия и generation владельца по каждому владельцу | память реплики | ≤ `GATEWAY_LIVE_MAX_SCOPES` (по умолчанию 2000) × ёмкость кольца 128 | простаивающее без подписчиков и событий `GATEWAY_LIVE_RING_IDLE` (10 мин) вытесняется; при заполнении вытесняется самое давнее простаивающее; если все с подписчиками — новый `Watch` получает 503 |
| Множество eventID (дедупликация) | память, LRU | 4096 на потребителя | вытеснение |
| Проекция реестра: ресторан → организация, жизненный цикл, готовность и generation Warehouse; надгробия (множество идентификаторов) | память | ≤ 10 000 живых (контракт) и компактное множество надгробий | обновляется по ревизии (5.5) |
| Кеш `ServiceToken` | память | по ресторану, до `exp − 10 с` | истечение |
| Счётчики допуска и аренды SSE | Redis limiter | ключи в 5.3 | TTL окна или аренды |
| Аренда планировщика и fencing | Redis limiter | 2 ключа | TTL аренды |

Идентификатор экземпляра `GATEWAY_INSTANCE_ID` (вводится в W1.1; factory задаёт его уникальным у каждой реплики: `gateway-a`, `gateway-b`, C19; без значения — случайный UUID) идёт в имя очереди, `/readyz` и логи. В поле `epoch` SSE и курсора идёт отдельный случайный boot-nonce процесса, независимый от `INSTANCE_ID` (C19): рестарт реплики с тем же `INSTANCE_ID` не оживляет старые курсоры. «Эпоха Gateway» больше не регистрируется в Auth и не участвует в готовности. По R2 `runtimeEpoch` владельцев (storage generation) проверяется как раньше; `ObserveRuntime` по-прежнему сбрасывает кольцо при реальной смене, но только у уже существующего кольца и новое не создаёт.

### 5.2. Live: потребитель RabbitMQ и кольцо (R4, R17)

- Порты у потребителя: `live.Journal` (`Watch`, `Read`, `Cursor`, `CloseScope`) и `journal.EventSink` (`Apply(Event)`, `ResetAll()`). Адаптер `internal/adapters/rabbitmq` — копия эталона R15 (`connector`, потребитель) с комментарием-источником (`umir-factory reference/durable@<sha>`) плюс код под Gateway.
- Топология (C1–C3, C38): exclusive auto-delete очередь `gateway.live.<instance>` с единственной привязкой `warehouse.#` на exchange `umir.events`: Warehouse — единственный производитель, `serving.#` не привязывается. Для R17 вторая очередь `gateway.auth.<instance>` на `umir.auth` с привязками `auth.restaurant.deleting`, `auth.restaurant.deleted`, `auth.organization.deleting`, `auth.organization.deleted`, `auth.organization.settings_changed`, `auth.membership.revoked`, `auth.assignment.revoked`. Обменники, durable-очереди, retry и DLQ объявляет только задание `mq-init` (C2): Gateway объявляет свои exclusive-очереди и пассивно проверяет обменники. Prefetch 32, `ack` после применения, неразбираемое сообщение — reject без requeue и счётчик. Очереди не quorum: они сигнальные и пересоздаются.
- Применение события:
  1. `restaurantID` — валидный `EntityID`. Оргидентичность (`orgID`) сверяется с организацией кольца, которую задаёт первый `Watch` по `AccessCheck`. Несовпадение — отбросить и посчитать, не логируя содержимое.
  2. Кольца нет — событие отбрасывается (подписчиков и валидных курсоров быть не может; новый подключившийся без курсора получит `refresh-required`).
  3. `generation` владельца отличается от запомненного — кольцо сбрасывается (generation++, `last=0`), подписчики получают `refresh-required`.
  4. `aggregateRevision` ≤ запомненной — дубликат или опоздавшее, отбросить. Разрыв (ревизия > запомненная + 1; у `warehouse.*` это версия потока ресторана подряд в пределах generation, C30) — сброс кольца. Первое событие после создания кольца задаёт базу.
  5. Иначе `seq++`, запись в кольцо всех пяти тем `catalog|stock|menu|orders|analytics`, как сейчас (C39), пробуждение подписчиков. Поле `topics` события не используется, пока тест Gateway и Frontend «каждая мутация обновляет нужные экраны» не включит узкий набор (`GATEWAY_LIVE_TOPICS=event`). Для `umir.auth` ревизии монотонны, но не подряд (C30): непрерывность по ним не проверяется, `topics` у них нет.
- Переподключение к брокеру: очередь автоматически удалена, поэтому после повторного объявления вызывается `ResetAll` (generation++ у всех колец, подписчики получают `refresh-required`). Пока потребитель не готов, новый поток получает `503` с `Retry-After`, а существующие потоки закрываются после `ResetAll`. Событие не восстанавливается из HTTP-ответа.
- Удаляется локальный `Append` после проксированного ответа и после запуска просрочки: `httpapi/scoped.go` `publishChange` и вызовы `Append` в `expiration/scoped.go:255,266`. Неизвестный исход записи не требует локального сигнала: владелец пишет событие в outbox в той же транзакции, что и изменение, поэтому зафиксированная мутация сигнал даст. `ObserveRuntime` остаётся.
- Курсор: формат `CursorPosition` не меняется (`Epoch` = boot-nonce процесса, C19; `Generation` = generation кольца). Курсор другого экземпляра, прошлого generation или с истёкшим сроком даёт `refresh-required`, а не 403. Ключ шифрования курсора отделяется от `AUTH_SERVICE_KEY` (`GATEWAY_LIVE_CURSOR_KEY_FILE`, один и тот же у всех реплик; ротация обнуляет курсоры, это допустимо).
- Уведомление `umir.auth` (C1): не даёт прав. `…deleting` — сразу перестать обслуживать затронутые scope: новые `Watch` отклоняются, потоки закрываются, не дожидаясь перепроверки раз в 4 с; `…deleted` — очистка кольца, проекции и аренд; `auth.membership.revoked` и `auth.assignment.revoked` будят затронутые потоки, которые тут же делают `AccessCheck`; `auth.organization.settings_changed` запускает внеочередную синхронизацию реестра. Очередь `gateway.auth.<instance>` тоже пересоздаётся, пропущенное не восстановить: после повторного объявления Gateway сразу синхронизирует реестр и перепроверяет активные потоки. Граница безопасности — перепроверка раз в 4 с — не меняется.
- Sticky-сессии (C20): frontend Nginx держит `upstream` с `hash $cookie_<session> consistent` и `server gateway:8080 resolve` (нужны `zone` и `resolver`; поддержку проверяет `npm run test:nginx`); factory даёт сетевой псевдоним `gateway` обеим репликам и запускает frontend после Gateway. Gateway обязан корректно переносить переход на другую реплику: `refresh-required` и далее обычный поток.

### 5.3. Допуск на Redis limiter (R7, R10, R11, R20)

- Порт `admission.Limiter` у потребителя: `Admit(ctx, Rule…) (Decision, error)`. Правило: имя, список измерений (ключ, лимит, окно), отпечаток попытки. Адаптер `internal/adapters/redislimiter` — копия скрипта допуска из эталона R15; Lua вычисляет все измерения правила атомарно, повтор того же отпечатка не списывает бюджет и не продлевает окно.
- Ключи (C17, владелец `gw`, вид `umir:gw:<kind>:…`; ACL сервиса — только на `umir:gw:*`): `umir:gw:adm:<правило>:<измерение>:<HMAC(субъект)>` — ZSET отпечатков, TTL окна. Субъекты (IP, preauth, сессия) хешируются HMAC-ключом `GATEWAY_ADMISSION_KEY_FILE`, один у всех реплик. `umir:gw:sse:s:<HMAC(сессия)>`, `umir:gw:sse:o:<организация>`, `umir:gw:sse:cell` — ZSET аренд (id потока → срок). Маркер `umir:limiter:generation` пишет задание factory `redis-init`; Gateway только читает его и сравнивает с `GATEWAY_LIMITER_GENERATION`, расхождение или отсутствие включает строгий режим R7 (скрипт эталона).
- Маршруты и бюджеты (числа в конфигурации `GATEWAY_BUDGET_<ИМЯ>=<число>/<окно>`, значения по таблице DUR1 на G1):

| Маршрут | Измерения | Где в коде |
| --- | --- | --- |
| `GET /api/v2/csrf`, `GET /api/operator/csrf` | клиент; глобально (только запросы без валидного cookie, включая повторную выдачу после подтверждённого 401 в `facade.go:80`) | `operatorhttp/facade.go`, `remote.go` |
| `POST /api/v2/session` (вход) | клиент (C12); бюджет по логину («5 неудач за 60 с») остаётся у Auth в Postgres | `operatorhttp/facade.go` |
| `POST /api/v2/email-challenges` (`purpose=registration`) | клиент, preauth, грубо глобально (C26); бюджеты по адресу и выдачам, эффективный глобальный предел 30 за 10 мин — у Auth | заменяет `registration_limit.go` |
| `POST /api/v2/email-challenges` (`purpose=password_reset`) | клиент (C12); адрес и глобальный предел — у Auth | `operatorhttp/facade.go` |
| `POST /api/v2/feedback/anonymous` | клиент, preauth, глобально | заменяет `internal/admission/temporary_memory.go` |
| prepare и confirm заказа | пользователь, ресторан (после `authorize`, до вызова владельца) | `httpapi/scoped.go` |
| аренда SSE | сессия, организация, ячейка | `live/handler.go` |

- Аренды SSE: атомарный скрипт занимает три слота или отказывает. Продление раз в `TTL/3` (TTL 30 с), освобождение идемпотентно и действует только на свой `leaseID`. Потеря продления закрывает поток не позже конца аренды. Локальный потолок потоков процесса остаётся как защита ресурса (не политика допуска): при недоступности Redis новые потоки не допускаются.
- Ответы (C16): исчерпан бюджет — `429 RateLimited` с заголовком `Retry-After` и полем `details.retryAfterSeconds`; Redis недоступен, истёк таймаут, ответ неразборчив или непрерывность под вопросом — определённый `503 ServiceUnavailable` до отправки владельцу (`outcome: not_committed` для записи, `unavailable` для чтения; кода `AdmissionUnavailable` нет). Запрос, уже отправленный владельцу, сохраняет свой настоящий исход. Resolve, чтение без допуска, Auth/JWKS отказ Redis не затрагивает.
- Удаляется: `registration_limit.go`, `internal/admission/temporary_memory.go`, их тесты.

### 5.4. Конфигурация (всё через файлы секретов)

Новые: `GATEWAY_AMQP_URL_FILE`, `GATEWAY_REDIS_LIMITER_URL_FILE`, `GATEWAY_ADMISSION_KEY_FILE`, `GATEWAY_LIVE_CURSOR_KEY_FILE`, `GATEWAY_LIMITER_GENERATION`, `GATEWAY_REDIS_TIMEOUT` (по умолчанию 200 мс), `GATEWAY_BUDGET_*`, `GATEWAY_LIVE_MAX_SCOPES`, `GATEWAY_LIVE_RING_IDLE`, `GATEWAY_LIVE_TOPICS` (`all` по умолчанию, `event`; C39), `GATEWAY_REGISTRY_SYNC_INTERVAL`. Остаются из волны 1: `GATEWAY_INSTANCE_ID`, `GATEWAY_TRUSTED_PROXY_CIDRS`, `GATEWAY_LIVE_*`, `GATEWAY_SUMMARY_*`, `GATEWAY_EXPIRATION_*`, `<ИМЯ>_SERVICE_KEY_FILE` (W1.11). Контракт конфигурации — `docs/durable/configuration.md` (C10).
Режима `memory` в боевом бинарнике нет, переменной `GATEWAY_STORAGE` нет (C10): memory-адаптеры допуска и журнала живут только в тестовом коде. Без `GATEWAY_AMQP_URL_FILE` и `GATEWAY_REDIS_LIMITER_URL_FILE` бинарник не стартует (R20); это проверяет тест сборки. Имя маркера limiter зафиксировано C17 (`umir:limiter:generation`).

### 5.5. Реестр и планировщик (R3, R5, R6)

- Проекция реестра (C13) обновляется раз в `GATEWAY_REGISTRY_SYNC_INTERVAL` (по умолчанию 30 с) и сразу по уведомлению `umir.auth`: список `limit/after` с `ETag` и условным запросом (304), далее изменения по `?since=<revision>`; неизвестный ресторан читается точечно `/registry/{restaurantID}` с `If-None-Match`. Список отдаёт только живые рестораны; надгробия приходят через `since` и точечное чтение и закрывают scope. Записи отражают подтверждённую готовность, живых `Status` в чтении нет. Пути и заголовки — DUR1 (в Auth уже сегодня есть `registryRevision`, Gateway его пока не разбирает).
- Аренда: `umir:gw:expiry:lease` (значение — идентификатор экземпляра, `SET NX PX 15000`), `umir:gw:expiry:fence` (`INCR` при каждом захвате). Продление раз в 5 с скриптом «продлить, если значение моё». Лидер перестаёт отправлять запуски, когда до конца собственной аренды остаётся меньше запаса (3 с по монотонным часам). Остальные реплики пробуют захват раз в 5 с. Повторный запуск просрочки безвреден: `RunExpirations` в Warehouse управляется часами, `runID` лишь валидируется (`umir-warehouse internal/adapters/httpapi/scoped.go`, `dueExpiry`), поэтому fencing-токен остаётся в логах и в проверке «аренда ещё моя» перед каждой отправкой, а не в запросе. Warehouse-группа подтверждает идемпотентность (раздел 6).
- Итерация (раз в `EXPIRATION_RUN_INTERVAL`): `GET /internal/warehouse/v2/expiry-due?asOf&limit&after` с сервисным токеном `expiry.due` уровня ячейки, без ресторана (C11, C22; `ServiceToken` в `authclient/scheduler.go` принимает пустой `RestaurantID`; `limit` = `GATEWAY_EXPIRATION_BATCH`, по умолчанию 500, дальше по `after`) → пул `GATEWAY_EXPIRATION_CONCURRENCY` → проекция реестра (ресторан готов, generation склада совпадает) → кеш токена ресторана → `Due`; `nextDueAt` в ответе `expiry-runs` задаёт следующий срок ресторана. Здоровье планировщика (лидер ли, лаг, сбои) — только в `/readyz`.
- `schedulerReady` больше не требует `gateway` и `serving` среди готовности (`expiration/scoped.go:213`); достаточно жизненного цикла `ready` и готового Warehouse с совпадающим generation. Опция `GatewayEpoch` удаляется.

### 5.6. Порядок совместимости R3 (C7)

Слияние строго в этом порядке:

1. **DUR1** помечает операции Gateway (`gatewayRuntime`, Register, Close) как `deprecated`.
2. **Gateway, PR-1** (первый PR Gateway волны 2; W1.1 уже отделил готовность): сам читает реестр по ревизии, готовность от планировщика не зависит, старые маршруты пока работают. Приватный listener, `GET runtime` и `PUT|DELETE …/registry` отвечают для старого Auth. `AUTH_RUNTIME_SERVICE_KEY` и `PRIVATE_*` становятся необязательными: если не заданы, listener не стартует. Планировщик и закрытие scope работают по реестру и не ждут регистрации, а в записи реестра `gateway` среди `services` либо есть, либо нет.
3. **Auth, PR** (волна 2 Auth, R3): перестаёт вызывать Gateway, убирает `gateway` из пиров и готовности.
4. **Gateway, PR-2**: удаляет маршруты `runtime|registry` и JWKS-проверку для Auth: `httpapi/runtime.go`, `privateHandler`, `runPair`, `authclient/control.go` (JWKS и `ControlToken`), опции `PRIVATE_*` и `AUTH_RUNTIME_SERVICE_KEY` из `config`, тесты `TestG5PrivateRuntimeNeedsSeparateAuthIdentity`, `TestG5RuntimeStatusDoesNotCallAuthRegistry`, `TestG5RuntimeRegisterCloseAndTombstone`, `TestG5OwnGatewayEpochRequiresReadiness`. Без `sync-contract`: схемы `RegisterRuntime`/`CloseRuntime` в `scoped-contract.json` остаются, на них больше никто не ссылается.
5. **`2.0.0-DUR.2`** физически удаляет операции; после неё Gateway выполняет `make sync-contract` отдельным небольшим коммитом.

Мост совместимости есть — это шаги 2–4. FIN1-профиль для отката не трогается: у него свои старые образы; условия про `compose.completion.yaml` у PR-2 нет.

Нарушать порядок нельзя: если Gateway уберёт listener раньше, чем Auth перестанет его вызывать, старый Auth снова увидит «Gateway не готов» и закроет все авторизации (исходная связность из C01). PR-2 не сливается раньше PR Auth; порядок PR-1, Auth, PR-2 записывается строкой `Ruling` в общем журнале.

### 5.7. Здоровье и готовность по возможностям (C32)

`GET /healthz` — процесс жив. `GET /readyz` — 200, когда готовы `http` и `limiter` (C49: без limiter защищённые операции получают 503, поэтому сервис с ним не готов); тело `{instance, capabilities: {<имя>: ok|degraded|down}}` без секретов и адресов, поля `generation` у Gateway нет. Healthcheck compose и образа смотрит `/readyz`; подпутей `/readyz/*` и параметров запроса нет. Имена возможностей перечислены в `configuration.md`; Gateway предлагает: `http`, `auth` (последняя успешная синхронизация реестра, не блокирует), `events` (потребитель live подключён), `limiter` (Redis отвечает, непрерывность подтверждена; `strict` — строгий режим R7), `scheduler` (лидер, лаг, сбои). Возможность отключает только свои маршруты:
- `events` недоступна → `degraded`, готовность не падает (C49); новые SSE получают 503;
- `limiter` down → `/readyz` отвечает 503, маршруты из 5.3 получают 503 до отправки;
- `scheduler` down → просрочка не запускается.

Остальные части (`events`, `scheduler`, `auth`) на готовность не влияют, приёмка и тесты читают из `capabilities`. Счётчики допуска (разрешено, отказано, недоступно) пишутся в структурные логи без ключей и субъектов; эндпоинта метрик нет (C33).

### 5.8. Удаляемый код и условие

Ниже — две независимые группы.

Сразу в волне 2: `registration_limit.go`; `internal/admission/temporary_memory.go`; `httpapi.publishChange` и локальный `Append` в планировщике; `GatewayEpoch` и `schedulerState.unknown`; кеш курсоров (`journal/scoped.go:195-229`, избыточен при AEAD-курсоре); `authclient/registry_cache.go` (заменён проекцией).

**Старый режим** — отдельный PR волны 3 (C28), отдельные коммиты. Условие слияния: factory вывела legacy-профили `compose.yaml` (образ `sha-c7a5c02…`), `compose.auth.yaml`, `compose.dev.yaml` и при необходимости `compose.feedback.yaml` из CI и из README (волна 3). Подтверждение — строка `Ruling` в общем журнале. До этого legacy-код остаётся нетронутым: волна 1 и волна 2 его не трогают. `tests/http/cmd/acceptance/{live,realtime,auth}.go` (маршруты `/api/live`, `/api/serving/*`, `/api/warehouse/*`) лежат в этом репозитории и удаляются тем же PR.
Что удаляется: перечень в разделе 2, а также `OPERATOR_STORE_PATH`, `OPERATOR_BOOTSTRAP_*`, `OPERATOR_IDLE_TIMEOUT`, `OPERATOR_ABSOLUTE_TIMEOUT`, `JOURNAL_CAPACITY` (в `config`), том `/data/operator`, `ENV OPERATOR_STORE_PATH` и каталог `/data` в `Dockerfile`, том и переменные в `compose.yaml`, команды `hash-password` и `clear-sessions`, `AUTH_URL` становится обязательным.
Перед удалением перенести оставшиеся ссылки: `operator.SessionKey` (`live/handler.go:124`) в пакет `live` или `livecursor`; `operatorhttp.API` (cookie, Origin, `problem`, `writeJSON`) и ошибки в `respond.go` ссылаются на `internal/operator`. Это границы, которые ещё нужны Auth-режиму. Карту зависимостей составить `grep`-ом до удаления. `.go-arch-lint.yml` обновить. Добавить в CI повтор SSE-тестов для `live.Handler` (W1.10) до того, как исчезнут тесты `live.Streams`.

Обновить README (начало про «систему одного ресторана», раздел «Реальное время», строки «несколько реплик не поддерживаются» в README:363 и :401), `gateway-frontend-handoff.md` в factory (через CR), `CHANGELOG.md` (Unreleased), `Dockerfile`, `compose.yaml` самого репозитория.

## 6. Интерфейсы с другими группами

**Gateway выдаёт:**
- G-factory (CR при расхождении). Переменные раздела 5.4; требования к инфраструктуре:
  - пользователь AMQP `gateway` только читает `umir.events` и `umir.auth` и объявляет свои очереди `gateway.live.*` и `gateway.auth.*` (без публикации, без объявления обменников и durable-очередей: их создаёт `mq-init`);
  - ACL Redis limiter на префикс `umir:gw:*` и чтение `umir:limiter:generation`, без FLUSH; маркер пишет `redis-init`;
  - `/healthz` (процесс жив) и `/readyz` (healthcheck compose), C32;
  - в durable-профиле 2 реплики Gateway с уникальными `GATEWAY_INSTANCE_ID` (`gateway-a`, `gateway-b`, C19) и сетевым псевдонимом `gateway` у обеих (C20);
  - `GATEWAY_TRUSTED_PROXY_CIDRS` (C34), проверка в `preflight`;
  - секреты: сервисные ключи файлами (C14), `GATEWAY_ADMISSION_KEY`, `GATEWAY_LIVE_CURSOR_KEY` и limiter generation в `secrets.sh` и в бэкапе;
  - вывод legacy-профилей из CI и README в волне 3 (5.8, C28).
- G-frontend. Sticky-хеш по cookie сессии на frontend Nginx (`upstream` с `hash … consistent` и `server gateway:8080 resolve`, C20); перезапись `X-Forwarded-For $remote_addr` и `set_real_ip_from` для NPM (R11); `UMIR_TRUSTED_PROXY` на их стороне соответствует `GATEWAY_TRUSTED_PROXY_CIDRS` на нашей (C34). Поведение на проводе: `refresh-required` при смене экземпляра, `429 RateLimited` с `Retry-After`, определённый `503` до отправки. Форматы SSE и ошибок не меняются.
- G-auth. Подтверждение порядка 5.6.

**Gateway потребляет (ожидания, DUR1 должен их зафиксировать; иначе CR):**
- DUR1: смысл `runtimeEpoch` (R2); удалённые приватные маршруты Gateway (R3); ревизия реестра, `If-None-Match`, точечное чтение (R5); `GET /internal/warehouse/v2/expiry-due?asOf&limit&after` и `nextDueAt` в ответе `expiry-runs` (R6, C22); сервисные токены `expiry.due` уровня ячейки (C11); коды допуска (C16): 429 `RateLimited` с `details.retryAfterSeconds`, 503 `ServiceUnavailable`; `CommandConflict` вместо `CommandIdentityConflict` (C29); лимиты заказа 400 `InvalidArgument` и 409 `LimitExceeded` (C15) и состояние попытки `Interrupted` (C21) Gateway только пересылает, а новые значения публичных схем, если появятся, приходят через `make sync-contract` после DUR.2.
- Каталог событий: конверт `umir.events` (`eventID`, `owner`, `orgID`, `restaurantID`, `aggregateRevision`, `generation`, `topics`), ключи `warehouse.*` (C38), семь ключей `umir.auth` из C1 (R17).
- Warehouse: событие на каждое изменение, влияющее на `catalog|stock|menu|orders|analytics`, включая истечение срока (`dueExpiry`); `aggregateRevision` — версия потока ресторана подряд в пределах generation (C30); узкий набор `topics` кладётся в событие, но Gateway пока рассылает все пять (C39); идемпотентность `dueExpiry` при повторе (подтверждено чтением кода, группа Warehouse подтверждает в своей подспеке).
- Serving: ничего не публикует (C38); темы `menu` и `orders` покрывают события Warehouse.
- Auth: R3 (C7) и реестр (C13) как выше; уведомления R17 (C1); бюджеты по клиенту на входе, регистрации и сбросе держит Gateway (C12, C26), заголовка с ключом клиента в Auth нет.
- Factory reference (R15): `connector`, потребитель, скрипт допуска, аренда с fencing — копируются с комментарием-источником, не импортируются.

## 7. Тесты и проверки

Каждая команда с `GOWORK=off GOTOOLCHAIN=go1.26.9`. Волна 1 готова при зелёных `make check` и `make quality-test`: `make integration` в волне 1 нужен, только если цель уже объявлена (C37), а в `Makefile` её сейчас нет. Волна 2 добавляет цель в `Makefile` и CI и готова при зелёных всех трёх.

- **Unit:** все тесты `W1.*` и аналоги волны 2; `go test -race -count=1 ./...`; SSE-регрессии с повтором `-count=100`; тест сборки: бинарник без `GATEWAY_AMQP_URL_FILE` и `GATEWAY_REDIS_LIMITER_URL_FILE` не стартует (C10); тест `/readyz`: тело по C32 (W1.1).
- **Контрактные наборы вместо контрактов хранилища** (база данных у Gateway не используется):
  - `internal/admission/contract` — один набор для `admission.Limiter` на тестовой реализации (только в `_test`, не в продовом коде) и на Redis-адаптере: последний слот, окно и граница TTL, повтор отпечатка, сменённый отпечаток, параллельные запросы;
  - набор для аренд (SSE и планировщика): идемпотентное освобождение, продление чужой аренды, устаревший владелец;
  - набор для `journal` через порт `live.Journal`.
- **Integration** (теги `integration`, `make integration`): `UMIR_TEST_AMQP_URL`, `UMIR_TEST_REDIS_LIMITER_URL` (Postgres и cache Redis не нужны). Временный vhost `t_gateway_<rand>` и префикс `t:gateway:<rand>:`, очистка в конце, `FLUSH*` запрещены, без переменных набор падает с понятной ошибкой.
- **Сценарии отказов #17, которыми владеет Gateway** (интеграционные, с реальными RabbitMQ и Redis; остальные пункты матрицы относятся к владельцам):
  - дубликат, опоздавшее и отравленное сообщение live: нет повторного `sequence`, нет данных чужого tenant, отравленное не блокирует очередь;
  - рестарт брокера или потеря очереди: `ResetAll`, `refresh-required`, повторное чтение клиентом успешно, пока потребитель не готов, новые потоки получают 503;
  - две реплики Gateway: изменение через любую достигает потоков на обеих; переход клиента на другую реплику даёт `refresh-required`;
  - рестарт Gateway, в том числе с тем же `INSTANCE_ID`: старый курсор даёт `refresh-required` (boot-nonce, C19);
  - потеря очереди `gateway.auth.*`: после повторного объявления реестр синхронизируется, активные потоки перепроверяются; `…deleting` останавливает обслуживание сразу;
  - бюджеты «по клиенту» на входе, регистрации и сбросе пароля: два клиента за одним доверенным прокси имеют раздельные бюджеты, при исчерпании ответ 429 с `Retry-After` и `details.retryAfterSeconds`;
  - Redis: таймаут после выполнения скрипта (повтор с тем же отпечатком не списывает дважды), `NOSCRIPT`, OOM при `noeviction`, разрыв сети, рестарт и пустое или повреждённое состояние (строгий режим), параллельные последние слоты двух реплик, одновременный захват аренды планировщика двумя репликами и работа после её истечения; ни одна защищённая операция не проходит в обход допуска;
  - истёкшая аренда SSE: устаревший держатель не освобождает слот преемника;
  - отзыв сессии и удаление ресторана завершают поток в пределах границы безопасности; поток без права не получает чужой курсор и события;
  - отмена сводки по дедлайну, соответствие публичному контракту через реальный путь frontend → Gateway.
- **Нагрузочная проверка** (R13, вместе с factory): 100 × 100 ресторанов — память колец, задержка сводки, число вызовов Auth в секунду, задержка допуска; числа фиксируются на G1.

## 8. Приёмка группы

- [ ] Волна 1: все `W1.*` слиты с красными-сначала тестами, `make check`, `make quality-test`, `-race` зелёные; публичный и приватный контракты не менялись.
- [ ] C01: итерация планировщика дольше 5 с не даёт `ready=false` и `RuntimeNotReady`; восстановление без перезапуска.
- [ ] Допуск: csrf, вход, регистрация, сброс пароля, анонимная обратная связь, prepare/confirm, SSE работают на Redis; бюджеты «по клиенту» на публичных входах Auth — у Gateway; ключи Redis только `umir:gw:*`; процессных лимитеров допуска в `internal/` нет (тест), бюджеты берутся из конфигурации.
- [ ] Redis недоступен → определённый `503` до отправки; нет запасного пути, запросы вне таблицы 5.3 продолжают работать.
- [ ] Две реплики делят один бюджет и одну аренду планировщика; ровно один планировщик работает.
- [ ] Привязки: `warehouse.#` на `umir.events` и семь ключей `umir.auth` (C1), `serving.#` нет; обменники и durable-очереди Gateway не объявляет.
- [ ] Live: событие владельца попадает в потоки обеих реплик (все пять тем, C39); нет локального `Append`; дубликат, разрыв, рестарт и смена реплики дают `refresh-required`; кольцо ленивое, память ограничена.
- [ ] Реестр по ревизии (C13); Gateway не регистрируется в Auth; порядок 5.6 соблюдён (PR-1, Auth, PR-2 без `sync-contract`, sync после DUR.2).
- [ ] Сервисные ключи читаются из `*_FILE` (W1.11); регрессия enum ошибок на границе зелёная (W1.12).
- [ ] Сводка уложена в общий дедлайн, память ограничена, partial честный; `X-Request-ID` проходит насквозь; логи отказов санитарны.
- [ ] `/healthz` и `/readyz` по C32 (тело `{instance, capabilities}`, C49: 200 при готовных `http` и `limiter`, `events` не роняет готовность); эндпоинта метрик нет.
- [ ] Старый режим удалён в волне 3 и только после строки `Ruling` о выводе legacy-профилей из CI и README (C28); README, CHANGELOG, Dockerfile, `compose.yaml` обновлены.
- [ ] `make integration` зелёный на реальных RabbitMQ и Redis; финальное ревью ветки — Ready.

## 9. Риски и вопросы к оркестратору

1. Закрыто: C22 — `GET /internal/warehouse/v2/expiry-due?asOf&limit&after` с токеном `expiry.due` и `nextDueAt` в `expiry-runs`; обход всех ресторанов пулом не нужен.
2. Закрыто: C30, C38, C39 — непрерывность `aggregateRevision` у `warehouse.*` в пределах generation; Serving ничего не публикует, темы `menu` и `orders` покрывает Warehouse; Gateway рассылает все пять тем, узкий набор включается после теста Gateway и Frontend.
3. Закрыто: C48 — цель задержки relay p95 не больше 1 с; собственный результат команды frontend и так перечитывает. Заголовки `X-Umir-Invalidation-Required` после R4 Gateway игнорирует, их удаление — в DUR.2.
4. Закрыто: C16 — отказ хранилища допуска даёт определённый 503 `ServiceUnavailable` с `not_committed` для записи; исключения для `confirm` нет.
5. Закрыто: C16 — `RateLimited` (заголовок `Retry-After` и `details.retryAfterSeconds`) и `ServiceUnavailable`; кода `AdmissionUnavailable` нет.
6. Закрыто: C13 — список отдаёт только живые рестораны, надгробия приходят через `?since` и точечное чтение и хранятся бессрочно; квота 10 000 активных ресторанов.
7. Закрыто: C14 — `GATEWAY_ADMISSION_KEY` и `GATEWAY_LIVE_CURSOR_KEY` входят в список секретов factory и бэкап; ротация курсора допустима (`refresh-required`).
8. Закрыто: C7 — порядок PR-1, Auth, PR-2 записывается строкой `Ruling`; PR-2 без `sync-contract`.
9. Закрыто: C36 — лимиты SSE предварительные; нагрузку `AccessCheck` на Auth измеряют в R13, числа утверждаются на G1.
