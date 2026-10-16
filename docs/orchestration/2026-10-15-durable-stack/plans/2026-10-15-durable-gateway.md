# Gateway durable-стек, волна 1 — план реализации

> **Для исполнителей:** обязательный навык — superpowers:subagent-driven-development. Шаги отмечаются `- [ ]`. Каждая задача: сначала красный тест, потом код (superpowers:test-driven-development).

**Цель:** разорвать цепочку критичного C01 (готовность Gateway зависит от планировщика) и закрыть независимые правки Gateway из раздела 4 подспеки (W1.1–W1.12) на текущем коде, без Redis, RabbitMQ и без изменения публичного и приватного контрактов.

**Архитектура:** Auth-режим Gateway (`cmd/server/auth_components.go`) остаётся целевым. Добавляются: фоновый `RegistryRefresher` и снимок реестра с временем последнего успеха; `GET /readyz`; пул планировщика с бюджетом итерации; ленивые кольца и раздельный учёт живых/закрытых scope; конфигурируемые лимиты SSE; чистый пакет `internal/clientaddr`; временный процессный допуск анонимной обратной связи (`internal/admission/temporary_memory.go`, помечен `TEMPORARY`); общий дедлайн сводки; сквозной request ID; чтение сервисных ключей из файлов; подкоманда `serve`. Старый режим (без `AUTH_URL`) не трогается (C28).

**Стек:** Go (`GOWORK=off GOTOOLCHAIN=go1.26.9`), только stdlib и уже имеющиеся зависимости, `go-arch-lint`, `golangci-lint` через `make tools`.

**Спека:** подспека `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/gateway.md` (раздел 4 — требования задач, разделы 2–3 — контекст и решения, раздел 3 «Решение по временному процессному лимиту» — условия W1.6). Главная спека `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md` (разделы 2, 3, 8, 10; решения C1–C53 главнее подспеки). Исполнитель читает раздел задачи в подспеке целиком: он авторитетен, а план задаёт порядок, файлы и тесты.

## Глобальные ограничения (действуют для каждой задачи)

- Каждая команда: `GOWORK=off GOTOOLCHAIN=go1.26.9`. Инструменты уже стоят в worktree (`.cache/go-quality/bin`).
- Рабочий каталог: `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-gateway`, ветка `fix/durable-w1-gateway`. Другие каталоги не трогать.
- Публичные пути, тела, статусы, CSRF/Origin, свежие решения Auth, «одна отправка без повтора» не меняются. Приватный контракт (`/internal/gateway/v2/...`) не меняется; JSON-схемы `*-contract.json` не правятся.
- Новые переменные имеют префикс `GATEWAY_`; существующие без префикса остаются. В коде нет значений бюджетов: только конфигурация и константы-по-умолчанию.
- Старый режим (без `AUTH_URL`) и его код не трогать (C28). `JOURNAL_CAPACITY` остаётся только старому режиму.
- Никаких метрик-эндпоинтов, Redis, RabbitMQ, Postgres (волна 2).
- Секреты, адреса, cookie, тела, IP клиентов не попадают в логи и сообщения об ошибках (ПДн).
- Слои и `go-arch-lint` без исключений; порты определяются у потребителя; у горутин есть владелец, отмена и ожидание завершения; время и ID внедряются.
- Коммиты: Conventional Commits на английском, **без** футеров Co-Authored-By и generated-by; запись в `CHANGELOG.md` раздел «Unreleased» (по образцу существующих пунктов) в коммите задачи; `git diff --check` перед коммитом.
- Каждая задача заканчивается зелёными `go build ./...`, `go vet ./...`, `go test -race -count=1` для затронутых пакетов, `make fmt FILES="..."` для изменённых файлов. Полный `make check` запускает ведущий; исполнитель запускает его, если задача трогала линтер/архитектуру (новые пакеты).
- Известная флака: `tests/fixtures/server TestUnixControlSocketDACAndAuthentication` изредка падает при параллельной нагрузке и зелёный при повторе; не маскировать, при падении повторить пакет отдельно.
- Субагентов исполнитель не запускает.

## Rulings плана

- Ruling: `/readyz` в волне 1 содержит возможности `http` (всегда `ok`, пока процесс обслуживает) и `scheduler` (`ok|degraded|down`); `auth`/`events`/`limiter` появятся в волне 2 — имена возможностей в `configuration.md` ещё не опубликованы, лишнего не добавляем — стоимость ошибки: добавить поле позже.
- Ruling: кеш токена и пул планировщика (W1.2) лежат в `internal/expiration` и `authclient`; `PUT …/registry` использует снимок refresher через расширение порта `RuntimeAuth` — стоимость ошибки: переименование метода.
- Ruling: `GATEWAY_REGISTRY_REFRESH_INTERVAL` 1–30 с (по умолчанию 2 с), `GATEWAY_REGISTRY_MAX_AGE` по умолчанию 30 с и не меньше 3 интервалов; нарушение — ошибка старта конфигурации.

## Обзор review focus

- Сбой выборки реестра ровно в момент, когда снимок ещё свеж: `ready` не должна прыгать (T1).
- Остановка refresher при `ctx.Done()` не оставляет горутин (T1, `-race`).
- Закрытый scope после `Append` в нового ресторана при `maxScopes` (T3).
- Подделка `X-Forwarded-For` недоверенным пиром, IPv6 в разных `/64` (T5).
- Невалидное тело и повтор отпечатка не списывают бюджет (T6).
- Дедлайн сводки истёк посреди обхода: порядок и `partial` сохраняются, горутины не утекают (T8).
- Заголовок `X-Request-ID` в ответе с ошибкой и в исходящих запросах; браузерный ID не доверяется (T9).
- Значение ключа в сообщении об ошибке при обоих источниках (T11).

---

### Task 1: W1.1 Готовность не зависит от планировщика (критичное C01)

**Файлы (ориентиры, подтвердить чтением):**
- `internal/adapters/authclient/registry_cache.go`, `scheduler.go` — снимок + время последнего успеха; сбой не стирает снимок.
- Новый `internal/adapters/authclient/refresher.go` (или в `internal/expiration`, где пройдёт `go-arch-lint`) — `RegistryRefresher` с `Run(ctx)`.
- `internal/httpapi/runtime.go` (`ready()`, интерфейс `RuntimeAuth`), `internal/expiration/scoped.go` (`tick` берёт реестр у refresher; `ScopedRunner.Health()`), `internal/config/config.go` (`GATEWAY_REGISTRY_REFRESH_INTERVAL`, `GATEWAY_REGISTRY_MAX_AGE`, `GATEWAY_INSTANCE_ID`), `cmd/server/auth_components.go`, `cmd/server/main.go` (запуск refresher рядом с планировщиком, ожидание завершения; команда `healthcheck` смотрит `/readyz`), `Dockerfile`/`compose.yaml` (healthcheck на `/readyz`, если там указан путь).
- Тесты: `cmd/server/w1_ready_test.go` (по образцу `g5_runtime_test.go`), `internal/adapters/authclient/*_test.go`, `internal/expiration/*_test.go`.

**Интерфейсы:**
- Производит: `authclient.Client.RegistrySnapshot() (expiration.Registry, bool)` теперь годен до `MaxAge` (опция клиента) от последнего *успешного* полного снимка; неудача не стирает снимок. `RegistryRefresher` выбирает реестр раз в `GATEWAY_REGISTRY_REFRESH_INTERVAL`. `ScopedRunner.Health() SchedulerHealth{LastStart, LastSuccess time.Time; LastDuration time.Duration; ConsecutiveFailures int}`. `GET /readyz` на публичном listener → `{"instance":"<GATEWAY_INSTANCE_ID>","capabilities":{"http":"ok","scheduler":"ok|degraded|down"}}`, статус 200, пока процесс обслуживает; состояние планировщика на статус не влияет; подробности (старт, длительность, сбои подряд) — в структурном логе, не в теле. `GATEWAY_INSTANCE_ID` по умолчанию — случайный UUID. Планировщик получает реестр у refresher (не делает своей выборки). Подпутей `/readyz/*` нет.
- Потребляет: ничего от других задач.

- [ ] Шаг 1: написать красные тесты `TestW1ReadyIgnoresSchedulerPace`, `TestW1ReadySurvivesOneFailedRegistryFetch`, `TestW1ReadyFalseAfterMaxAge`, `TestW1SchedulerHealthShowsLag`, `TestW1ReadyzBodyShape` (формулировки — подспека W1.1; часы внедряются, планировщик блокируется/тикает раз в 30 с, часы +10 с, приватный `GET runtime` даёт `ready=true`; после `MAX_AGE` без успеха `ready=false`; тело `/readyz` ровно `{instance, capabilities}` без адресов и секретов).
- [ ] Шаг 2: запустить, убедиться что падают по нужной причине.
- [ ] Шаг 3: реализовать refresher, снимок со временем успеха, `Health()`, `/readyz`, конфигурацию, запуск и остановку в `main.go`, healthcheck на `/readyz`.
- [ ] Шаг 4: тесты конфигурации (диапазоны 1–30 с, `MAX_AGE` ≥ 3 интервалов, дефолты); `go test -race -count=1 ./cmd/server ./internal/...` зелёные; существующие `TestG5*`, `TestG4*` зелёные.
- [ ] Шаг 5: CHANGELOG, `git diff --check`, коммит `fix(gateway): decouple readiness from the expiration scheduler`.

**Готово:** итерация планировщика дольше 5 с или сбой выборки не закрывает бизнес-маршруты; `ready` восстанавливается без перезапуска; запрет `EXPIRATION_RUN_INTERVAL` ≥ 5 с не нужен.

---

### Task 2: W1.2 Планировщик — пул, бюджет итерации, кеш токенов, регистрация без полной выборки

**Файлы:** `internal/expiration/scoped.go` (пул, бюджет, сдвиг начала), `internal/adapters/authclient/scheduler.go` (кеш `ServiceToken` по (ресторан, эпоха) до `exp − 10 с`), `internal/httpapi/runtime.go` (`PUT …/registry` берёт снимок refresher не старше 2 с с записью ресторана, иначе одна выборка через singleflight; расхождение версии/настроек перед 409/410 перепроверяется выборкой), `internal/config/config.go` (`GATEWAY_EXPIRATION_CONCURRENCY`=8, `GATEWAY_EXPIRATION_ITERATION_BUDGET`=10 с), `cmd/server/auth_components.go`.

**Интерфейсы:** потребляет refresher и снимок из Task 1 (`RegistrySnapshot` + время успеха; добавить метод со свежестью, например `RegistrySnapshotWithin(maxAge)`); производит опции `ScopedOptions.Concurrency`, `ScopedOptions.IterationBudget`.

- [ ] Шаг 1: красные тесты `TestW1SchedulerRunsDueWithinBudgetAndConcurrencyBound` (`internal/expiration`: 200 ресторанов по 10 мс, ≤ 8 одновременно, итерация < 1 с), `TestW1SchedulerRotatesStartWhenBudgetExhausted`, `TestW1ServiceTokenIsReusedWhileValid`, `TestW1RegisterReusesFreshSnapshotAndCoalescesFetch` (`cmd/server`: 100 последовательных `PUT` ≤ 1 выборки за 2 с; параллельные `PUT` неизвестного ресторана — одна выборка).
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать пул (ограниченный `errgroup`/семафор, владелец и ожидание завершения), бюджет со сдвигом начала, кеш токена (под мьютексом, ключ — ресторан+эпоха), снимок + singleflight в регистрации. Состояние `states`/`closedScopes` планировщика защитить от гонок.
- [ ] Шаг 4: `go test -race -count=1 ./internal/expiration ./internal/adapters/authclient ./internal/httpapi ./cmd/server` зелёные; итерация 1200 ресторанов по 5 мс укладывается в секунды.
- [ ] Шаг 5: CHANGELOG, коммит `perf(gateway): bound scheduler concurrency and reuse tokens and registry snapshots`.

---

### Task 3: W1.3 Слоты и кольца

**Файлы:** `internal/journal/scoped.go` (ленивое кольцо при первом `Append`; `ObserveRuntime`, `CloseScope`, `Watch` кольцо не создают; generation нового кольца из счётчика процесса; закрытые рестораны — компактное множество), `internal/httpapi/runtime.go` (раздельный учёт: до 10 000 живых и до 10 000 закрытых `runtimeEntry`), `internal/config/config.go` (`GATEWAY_LIVE_RING_CAPACITY` 128, диапазон 16–4096), `cmd/server/auth_components.go` (вместо `cfg.JournalCapacity` для Auth-режима).

**Интерфейсы:** `journal.NewScoped` сохраняет сигнатуру по возможности; если меняется — обновить вызовы и тесты `TestG4*`.

- [ ] Шаг 1: красные тесты `TestW1ObserveAndCloseAllocateNoRing` (10 000 scope, куча < 2 МиБ), `TestW1ClosedScopesDoNotUseLiveSlots` (три закрытия при `maxScopes=3`, затем `Append` нового ресторана успешен), `TestW1RuntimeAcceptsTenThousandLiveAndTenThousandClosed`, `TestW1RecreatedRingNeverAcceptsOldCursor`.
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать.
- [ ] Шаг 4: `TestG4*` и весь `internal/journal`, `internal/live`, `internal/httpapi`, `cmd/server` зелёные с `-race`.
- [ ] Шаг 5: CHANGELOG, коммит `fix(gateway): allocate live rings lazily and keep closed scopes out of live slots`.

---

### Task 4: W1.4 Лимиты SSE из конфигурации

**Файлы:** `internal/live/handler.go`, `internal/live/streams.go` (константы `Default*` остаются как значения по умолчанию), `internal/config/config.go` (`GATEWAY_LIVE_MAX_STREAMS`=64, `GATEWAY_LIVE_MAX_STREAMS_PER_SESSION`=8, `GATEWAY_LIVE_MAX_STREAMS_PER_ORG`=32), `cmd/server/auth_components.go`.

Правило: слот организации занимается после первого `AccessCheck`; слот сессии — до него, как сейчас; отказ — тот же `429 RateLimited` с `Retry-After: 1`; ни один лимит не зашит в `live` вне `Default*`.

- [ ] Шаг 1: красные тесты `TestW1StreamLimitsComeFromOptions`, `TestW1OrgCapDoesNotStarveOtherOrganization`, `TestW1ReleaseFreesSessionOrgAndTotalOnEveryExit` (отмена, ошибка, завершение).
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать (захват/освобождение под одним мьютексом; освобождение идемпотентно на каждом выходе).
- [ ] Шаг 4: `go test -race -count=1 ./internal/live ./cmd/server`; повтор SSE-регрессий `-count=20` на новых тестах.
- [ ] Шаг 5: CHANGELOG, коммит `feat(gateway): configurable SSE stream limits with a per-organization cap`.

---

### Task 5: W1.5 Доверенный клиентский адрес и регистрация

**Файлы:** новый пакет `internal/clientaddr` (`Resolver{Trusted []netip.Prefix}` с методом `Client(r *http.Request) string`; ключ нормализованный: IPv4 как есть, IPv6 — `/64`), `internal/config/config.go` (`GATEWAY_TRUSTED_PROXY_CIDRS`, список через запятую, ошибка при невалидном), `internal/operatorhttp/registration_limit.go` (вместо `r.RemoteAddr`), `cmd/server/auth_components.go`, `.go-arch-lint.yml` (новый пакет — чистый, без зависимостей).

Правило: пир не в списке — адрес пира, заголовки игнорируются; пир в списке — идём по `X-Forwarded-For` справа налево, первый адрес не из списка; пустой/нечитаемый заголовок — адрес пира. Без конфигурации поведение прежнее. Остальное в `registrationLimits` не меняется.

- [ ] Шаг 1: красные тесты: таблица `clientaddr` (подделка слева от доверенного, недоверенный пир, IPv6 `/64`, одиночный и составной заголовок, нечитаемый); `TestW1RegistrationBudgetIsPerClientBehindTrustedProxy`; существующий `TestRegistrationHTTPAdmissionRejectsBeforeAuthAndIgnoresForwardedSpoof` остаётся зелёным без конфигурации и получает пару с конфигурацией; невалидное тело вне бюджета.
- [ ] Шаг 2: убедиться, что красные (пакета нет).
- [ ] Шаг 3: реализовать пакет, конфигурацию, подключение.
- [ ] Шаг 4: `go test -race -count=1 ./internal/clientaddr ./internal/operatorhttp ./internal/config ./cmd/server`; `make arch` зелёный.
- [ ] Шаг 5: CHANGELOG, коммит `feat(gateway): resolve the client address through trusted proxies for registration limits`.

**Интерфейсы:** производит `clientaddr.Resolver` (используется Task 6).

---

### Task 6: W1.6 Временный допуск анонимной обратной связи (C31)

**Файлы:** `internal/httpapi/scoped.go` (порт `Admission` у потребителя, место допуска: валидация схемы → Origin/CSRF → допуск → `AnonymousToken` → отправка; `writeFailure` заполняет `details.retryAfterSeconds` при `RetryAfter`), новый `internal/admission/temporary_memory.go` (первая строка файла-комментарий `TEMPORARY`, условия из подспеки раздел 3 «Решение по временному процессному лимиту»: удаляется в коммите волны 2, вводящем Redis-допуск; не замена Redis при отказе), `cmd/server/auth_components.go` (подключение, Resolver из Task 5), `.go-arch-lint.yml`.

Бюджеты: 5 на preauth за 10 мин; 20 на клиента за час (только при непустом `GATEWAY_TRUSTED_PROXY_CIDRS`); 200 на процесс за час; повтор с тем же отпечатком `sha256(preauth‖тело)` не списывается; невалидное тело вне бюджета. Значения как константы-по-умолчанию временного файла (временный файл — исключение из «в коде нет значений»: подспека W1.6 задаёт числа явно). Отказ: 429 `RateLimited`, `Retry-After` и `details.retryAfterSeconds`. Память ограничена (удаление просроченного, потолок записей).

- [ ] Шаг 1: красные тесты (`cmd/server`, по образцу `feedback_test.go`): `TestW1AnonymousFeedbackLimitedPerPreauthBeforeAuthToken` (шестой запрос — 429, `AnonymousToken` и Feedback не вызваны), `…RepeatedFingerprintNotCharged`, `…InvalidBodyOutsideBudget`, `…RotatingPreauthHitsProcessBudget`, `…ClientBucketNeedsTrustedProxy`; юнит-тесты `internal/admission` (окно, граница, потолок памяти).
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать.
- [ ] Шаг 4: `go test -race -count=1 ./internal/admission ./internal/httpapi ./cmd/server`; существующие `feedback_test.go` зелёные; `make arch`.
- [ ] Шаг 5: CHANGELOG (с пометкой временности), коммит `feat(gateway): add temporary admission limits for anonymous feedback`.

---

### Task 7: W1.7 `CheckCSRF` не выдаёт preauth

**Файлы:** `internal/adapters/authclient/scoped.go` (~52–69): оба cookie пусты → сразу `403 InvalidCsrfToken` / `not_committed` без вызова Auth.

- [ ] Шаг 1: красный тест `TestW1CheckCSRFWithoutCookiesDoesNotCallAuth` (`internal/adapters/authclient`, транспорт считает вызовы; ответ клиенту байт в байт прежний — сравнить с текущим ответом).
- [ ] Шаг 2–3: убедиться, что красный; исправить.
- [ ] Шаг 4: `go test -race -count=1 ./internal/adapters/authclient ./internal/httpapi ./cmd/server`.
- [ ] Шаг 5: CHANGELOG, коммит `fix(gateway): reject CSRF checks without cookies before calling Auth`.

---

### Task 8: W1.8 Сводка организации — общий дедлайн и память

**Файлы:** `internal/httpapi/scoped_summary.go`, `internal/config/config.go` (`GATEWAY_SUMMARY_DEADLINE`=20 с, `GATEWAY_SUMMARY_CONCURRENCY`=4, `GATEWAY_SUMMARY_MAX_BYTES`=64 МиБ), `cmd/server/auth_components.go`.

Правила: общий контекст запроса с дедлайном; ограниченный параллелизм для проверок доступа и для отчётов; порядок элементов — порядок `restaurantID` запроса; общий бюджет байт на все отчёты; не уложившийся ресторан — `status:"unavailable"`, `error` код `UpstreamUnavailable`, `outcome:"unavailable"`; общий статус `partial` или `unavailable`, `complete` только при полном успехе; любой отказ доступа проваливает весь запрос до бизнес-вызовов (`TestG2SummaryFailsWholeCrossOrgBeforeBusinessDispatch`); итог проверяется по `OrgSummary`; `evaluatedAt` форматируется в `Europe/Moscow`.

- [ ] Шаг 1: красные тесты (`cmd/server/scoped_summary_test.go`): `TestW1SummaryRespectsOverallDeadlineAndKeepsOrder`, `TestW1SummaryBoundsConcurrentOwnerCalls`, `TestW1SummaryByteBudgetMarksRemainingUnavailable`, `TestW1SummaryMatchesOrgSummarySchemaAndMoscowInstant`.
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать (горутины имеют владельца и завершаются до ответа; чтение тела отчёта ограничено оставшимся бюджетом).
- [ ] Шаг 4: `go test -race -count=1 ./internal/httpapi ./cmd/server`; `TestG2Summary*` зелёные.
- [ ] Шаг 5: CHANGELOG, коммит `fix(gateway): bound organization summary by one deadline, concurrency and bytes`.

---

### Task 9: W1.9 Request ID и диагностика отказов

**Файлы:** `internal/httpapi/problem.go` (`WithRequestID` игнорирует входящий `X-Request-ID`, ставит заголовок ответа, в том числе при ошибках), `internal/httpapi/scoped.go` (`scopedHeaders` пишет ID из контекста исходящему; запись на каждый 502/503/504), `internal/adapters/authclient/client.go` (`exchange`) и `scoped.go` (`scopeExchange`) — ID из контекста в исходящие запросы к Auth; структурный лог `log/slog` JSON.

Запись отказа: `request_id`, `owner`, операция, `restaurant_id`, статус, класс причины (`timeout`, `transport`, `invalid_response`, `schema`, `scope_mismatch`, `upstream_status`), длительность. Текст `Cause`, адреса, cookie, токены, тела, IP клиента — не пишутся. Фоновые запуски планировщика ID не получают (у них свой `run_id`).

- [ ] Шаг 1: красные тесты (`cmd/server`): `TestW1RequestIDIsFreshReturnedAndForwarded` (браузерный ID затёрт; один ID виден в ответе, у фейкового Auth и у владельца; в ответе с ошибкой тоже), `TestW1UpstreamFailureLogHasReasonButNoSecrets`.
- [ ] Шаг 2: убедиться, что красные.
- [ ] Шаг 3: реализовать; логгер внедряется (для теста — буфер).
- [ ] Шаг 4: `go test -race -count=1 ./internal/httpapi ./internal/adapters/authclient ./cmd/server`; существующие тесты зелёные (заголовок ответа не ломает точные сравнения заголовков — править тесты аккуратно, не маскируя).
- [ ] Шаг 5: CHANGELOG, коммит `feat(gateway): propagate a fresh request ID and log sanitized upstream failures`.

---

### Task 10: W1.10 Мелкое (четыре маленьких коммита)

Каждое — отдельный коммит с тестом:
- (a) `Accept-Encoding: identity` в запросах владельцам (`scopedHeaders`) + тест.
- (b) дедлайн чтения тела на scoped-маршрутах, кроме `live`, из `UPSTREAM_TIMEOUT` (`http.Server.ReadTimeout` не ставить) + тест (медленный клиент тела получает таймаут; SSE не затронут).
- (c) тест, фиксирующий нулевой допуск `iat` (`service_token.go`, ~65) как явное поведение (допуск не вводится).
- (d) CI: `-timeout` в `scripts/go-quality.sh` (~69) и `timeout-minutes` в задании `ci.yml`; в `ci.yml` шаг повторяемых SSE-регрессий дополнить тестами боевого `live.Handler` (не только старый `live.Streams`) — выбрать устойчивые тесты `live.Handler` (например `TestG4*` из `internal/live`), проверить локально `-count=20 -race`; `make quality-test` зелёный.

- [ ] Шаги: по каждому подпункту красный тест → код → `go test -race` затронутых пакетов → CHANGELOG → отдельный коммит (`fix(gateway): …`, `test(gateway): …`, `ci(gateway): …`).

---

### Task 11: W1.11 Сервисные ключи из файлов и подкоманда `serve`

**Файлы:** `internal/config/config.go` (~112–136: `AUTH_SERVICE_KEY`, `SERVING_SERVICE_KEY`, `WAREHOUSE_SERVICE_KEY`, `FEEDBACK_SERVICE_KEY`, `AUTH_RUNTIME_SERVICE_KEY`), `cmd/server/main.go` (подкоманда `serve` — явный синоним запуска по умолчанию; `migrate`/`generation rotate` нет).

Правила: для каждого ключа вариант `<ИМЯ>_FILE`: файл читается целиком, завершающий перевод строки отбрасывается; прежние правила непустоты и «без пробелов» сохраняются; заданы и переменная, и файл — ошибка старта; значение ключа не попадает в сообщения об ошибках и логи; прежние переменные работают.

- [ ] Шаг 1: красные тесты `TestW1ServiceKeyFromFile`, `TestW1ServiceKeyBothSourcesIsError`, `TestW1ServiceKeyErrorDoesNotEchoSecret` (`internal/config`), `TestW1ServeSubcommandStartsServer` (`cmd/server`).
- [ ] Шаги 2–4: красные → реализовать → `go test -race -count=1 ./internal/config ./cmd/server`.
- [ ] Шаг 5: README (раздел переменных окружения: `_FILE`, `serve`, а также новые `GATEWAY_*` переменные из задач 1–8 одним разделом), CHANGELOG, коммит `feat(gateway): read service keys from files and add the serve subcommand`.

---

### Task 12: W1.12 Регрессия enum ошибок на границе (C29)

Только тесты (`cmd/server`, `internal/httpapi`); код меняется, лишь если тест выявит расхождение схемы.

- [ ] Тесты: `TestW1BoundaryPassesCommandConflictFromServingWarehouseAndAuth` (409, `outcome` без изменений; Auth через фасад), `TestW1BoundaryPassesLimitExceededAndInvalidArgumentOnOrderLimits` (409 `LimitExceeded`, 400 `InvalidArgument`, оба `not_committed`), `TestW1BoundaryPassesServiceUnavailableAndRateLimitedWithRetryAfterSeconds`, `TestW1BoundaryTurnsUnknownErrorCodeInto502` (код вне enum, например `CommandIdentityConflict`, остаётся 502).
- [ ] Запустить; если зелёные сразу — это замок, зафиксировать в отчёте; если красные — минимально поправить, объяснить в отчёте.
- [ ] CHANGELOG, коммит `test(gateway): lock the owner error enum at the scoped boundary`.

---

## Самопроверка плана

- Покрытие спеки: W1.1–W1.12 — задачи 1–12; README/CHANGELOG — в задачах 11 и в каждой. Раздел 5 (волна 2) и 5.8 (старый режим) в план не входят намеренно.
- Заглушки: тела тестов задаются именами и поведением по подспеке (авторитетный источник); код пишет исполнитель по TDD.
- Согласованность: `RegistrySnapshot`/refresher из задачи 1 используются в задаче 2; `clientaddr.Resolver` из задачи 5 — в задаче 6; `GATEWAY_*` переменные собираются в README задачей 11.
- Review focus закрыт тестами владельцев (см. раздел выше).
