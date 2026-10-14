# UMIR Factory

Актуальная [задача backend после checkpoint](docs/backend-next-steps.md) фиксирует завершённый Auth façade, исправления runtime/cooldown, живую HTTP матрицу и оставшиеся API/E2E задачи. Фронтенд и Figma ведёт другой разработчик. Задача не меняет frozen API или selected assembly.

[UI API amendment UA1](docs/ui-api-amendment.md) was approved for implementation on 2026-10-14. Its approved normative artifacts remain frozen separately from C2. Auth, Warehouse and Gateway now have local implementation candidates for product batches, Admin member/invitation snapshots and durable invitation-link mail. Full source gates pass. Real-service observations cover selected checks; all 34 acceptance rows are not yet qualified. The existing frozen API and selected assembly remain the predecessor.

Сборка системы учёта продуктов для одного ресторана. Фабрика закрепляет версии четырёх сервисных образов и запускает их через Docker Compose.

## Репозитории и границы

| Репозиторий | Ответственность |
| --- | --- |
| [umir-factory](https://github.com/JustIkra/umir-factory) | Compose, версии образов, запуск и проверка сборки. |
| [umir-gateway](https://github.com/JustIkra/umir-gateway) | Единый API-вход; маршрутизация к serving и warehouse. |
| [umir-serving](https://github.com/JustIkra/umir-serving) | OrdersService, MenuService (включая стоп-лист) и RecommendationsService. |
| [umir-warehouse](https://github.com/JustIkra/umir-warehouse) | Единый владелец склада: каталог, поставки, оценки, оформление, просрочка и запросы. |
| [umir-frontend](https://github.com/JustIkra/umir-frontend) | React и раздача интерфейса; запросы API через gateway. |
| [umir-feedback](https://github.com/JustIkra/umir-feedback) | Durable intake, SMTP notifications, and Telegram through a private VPN proxy. |

Бизнес-ядро в памяти реализует поставки и партии, приготовление по FEFO, подтверждение сокращённого состава заказа, списание просрочки, стоп-лист и рекомендации по продаже. Warehouse владеет остатками и атомарно фиксирует окончательную продажу вместе с расходом ингредиентов. Serving хранит предложения заказа и координирует подтверждение через склад.

Продукты, блюда и рецептуры хранятся в warehouse вместе с событиями. MenuService в serving работает с ними через порт склада. Мониторинг показывает остатки и сроки партий; отчёт для закупок находится вне текущей версии.

Перед новым оформлением и запросами актуальных остатков warehouse восстанавливает очередь и применяет записанные события наступивших списаний. При невозможности подготовки возвращается техническая ошибка InventoryUnavailable. Оформление подтверждается только записанным OrderPlaced; неизвестный исход записи разрешается повтором исходной попытки.

Остатки и индекс истечения публикуются вместе после полной команды; полная сверка нужна при восстановлении или известном сбое. Чистые расчёты используют захваченное неизменяемое состояние. GetMenu показывает только доступные блюда без количества порций. Снапшоты сохраняются в фоне после полных команд; точка восстановления меняется только после успешного сохранения.

Решение рассчитано на один ресторан. Время — `Europe/Moscow`. Регистрация пользователей, роли приложения и несколько ресторанов находятся вне базовой бизнес-версии. Отдельное расширение gateway реализует личный кабинет одного оператора; OTP-регистрация, OTP-вход и восстановление пароля остаются самостоятельными задачами и не меняют бизнес-контракты.

Основной документ бизнес-контрактов — [docs/business-core.md](docs/business-core.md). При расхождении описаний контрактов в README и docs приоритет имеет docs. Согласованные правила сохранены в документе; отдельно обозначенные предложения остаются на рассмотрении.

[Проект observability](docs/observability.md) описывает JSON-логи операций, связь запросов между сервисами, метрики зависаний и трассировку. Он подготовлен для рассмотрения; инструментация и стек мониторинга ещё не подключены.

## Текущее состояние

Статус на 2026-10-12. Реализация в исходниках, выбранные образы и развёртывание проверяются отдельно.

Warehouse и serving предоставляют независимые Go-модули с публичными contract/bootstrap и сценариями в памяти. Совместные тесты вызывают оба реальных Runtime через bridge фабрики, который сохраняет метаданные `GetDish` и копирует вложенный рецепт. Реальные HTTP-процессы обслуживают публичный API через gateway; serving обращается к private listener warehouse на 8082. Исходники warehouse предоставляют service-only `POST /expiration-runs` для одной атомарной оценки и обработки просрочки; свежие складские операции также продолжают обрабатывать наступившие списания. Warehouse запускает только независимый snapshot worker; `SNAPSHOT_RETRY_INTERVAL` задаёт его паузу после ошибки и по умолчанию равен `1s`. Оба сервиса ожидают завершения своих обработчиков. Новый endpoint warehouse и изменённый snapshot lifecycle реализованы в исходниках, но пока не входят в selected image pins. Планировщик gateway и его SSE остаются следующей частью [gateway #19](https://github.com/JustIkra/umir-gateway/issues/19). HTTP-адаптеры и разрешение незавершённых попыток в рамках работающих процессов реализованы. События и снапшоты хранятся только в памяти: постоянное состояние бизнес-ядра и восстановление попытки после перезапуска остаются следующей задачей. Фабрика отдельно проверяет HTTP-сценарии и сетевые сбои на четырёх точных исходниках или опубликованных образах. Выбранные в compose.yaml образы не меняются вместе с исходным кодом рабочих веток.

Gateway main пока содержит аккаунт и сессии одного оператора вместе с интеграцией формы обратной связи. Форма и её уведомления по SMTP/Telegram остаются в feedback. Исправление SMTP-клиента опубликовано в [feedback commit `6c59bbf0`](https://github.com/JustIkra/umir-feedback/commit/6c59bbf0ad19048a5ddd0fe47d771c1c567ca4dd): после TLS он предпочитает объявленный PLAIN, а если его нет — использует объявленный LOGIN; отказ AUTH не запускает повторную попытку другим механизмом. Проверка подтвердила TLS, AUTH и MAIL FROM; реальная доставка письма не проверялась. Выбранные образы остаются на прежних SHA.

Будущий приватный `umir-auth` будет владеть аккаунтами, сессиями и OTP и отправлять собственные письма через ту же SMTP-привязку, что и feedback. Перенос делает JustIkra по [Gateway #18](https://github.com/JustIkra/umir-gateway/issues/18); реализация auth service и перенос ещё не выполнены, gateway сейчас по-прежнему владеет аккаунтом и сессиями. OTP-код из [feedback PR #3](https://github.com/JustIkra/umir-feedback/pull/3) теперь находится в feedback main ([merge commit `e98747a1`](https://github.com/JustIkra/umir-feedback/commit/e98747a1771cd3de1e6defa1c849fdff3ea88154)) и выключен по умолчанию. Это код для переноса или повторного использования; интеграция и окончательный контракт будущего auth ещё не определены. Пины, production delivery и deployment не менялись.

Frontend main на `2339f1a` рендерит `null`. Опубликованная ветка [feat/frontend-quality, `962f7a9`](https://github.com/JustIkra/umir-frontend/commit/962f7a9ef1f2d5d2b7218e181e8f59bfb490c5c4) содержит TypeScript, React, SCSS, пять слоёв, Bun 1.4.3 и 35 quality-тестов. Она ещё не объединена с main; экранов и API-клиента нет в обеих версиях. Chakra UI запланирована, Stylelint отложен. [Figma Screens](https://www.figma.com/design/6Y1Jglyvc1xVsTzII54A2u/UmirHack?node-id=42-2) уже существует; оставшиеся экраны, результаты заказа и исправления переходов описаны в [дизайн-брифе](docs/frontend-design-brief.md).

## Оставшиеся задачи

Рекомендуемый порядок: завершить дизайн и перенести quality-каркас в main → реализовать API-клиент и UI → проверить браузерный путь через frontend → gateway → реальные serving/warehouse. Межмодульные тесты двух Runtime в tests/business и HTTP-приёмка реальных процессов в tests/http уже реализованы. Они используются как основа общей приёмки. Следующие issues отражают незакрытые критерии на 2026-10-12.

| Приоритет | Issue и владелец | Оставшаяся работа | Зависимость |
| --- | --- | --- | --- |
| 1 | [Factory #2](https://github.com/JustIkra/umir-factory/issues/2) — factory | Завершить общий frontend/Figma handoff и браузерную приёмку на реальных сервисах; межмодульные и HTTP-проверки backend уже есть. | Реализация экранов frontend #1. |
| 1 | [Frontend #1](https://github.com/JustIkra/umir-frontend/issues/1) — frontend | Перенести quality-каркас в main и реализовать API/UI, восстановление pending и browser checks. В Figma каталоги, формы и исходы заказа добавлены; остались пустой повторный вход, навигация, валидация пустой поставки, прокрутка длинных экранов и привязка F4 к определённому результату warehouse. | Реальные HTTP-сервисы готовы; browser evidence входит в общий handoff factory #2. |
| 2 | [Gateway #10](https://github.com/JustIkra/umir-gateway/issues/10) — gateway + frontend | Завершить UX ожидания сессии: предупреждения о простое и абсолютном сроке, продолжение сессии по действию пользователя. Feedback route и серверная интеграция уже есть. | Экраны и browser acceptance frontend #1. |
| 2 | [Gateway #19](https://github.com/JustIkra/umir-gateway/issues/19) — gateway + frontend | Warehouse `POST /expiration-runs`, отдельный snapshot retry, Factory OpenAPI `1.1.0-draft.1` и тесты `--realtime` реализованы. Остались gateway journal/SSE/scheduler, Nginx `/api/live` и клиентское обновление `/app`. | Сквозные R01–R07 запускаются с точными gateway/frontend revisions после их реализации; selected pins пока прежние. |
| 3 | [Gateway #18](https://github.com/JustIkra/umir-gateway/issues/18) — gateway, JustIkra | Перенести владение аккаунтами, сессиями и OTP в будущий приватный auth service; auth отправляет собственные письма через общую с feedback SMTP-привязку. | Auth service и миграция ещё не реализованы. OTP-код из feedback PR #3 уже в main, но выключен по умолчанию и остаётся кандидатом для переноса/повторного использования, не финальной интеграцией. |
| 3 | [Feedback #2](https://github.com/JustIkra/umir-feedback/issues/2), [Gateway #13](https://github.com/JustIkra/umir-gateway/issues/13) — прежний кандидат | Эти задачи описывали OTP через feedback private challenge API. Их исходные границы заменены решением для Gateway #18; согласовать нужный API в рамках миграции, не выводить контракт из кандидата PR #3. | Auth #18. |
| 4 | [Gateway #12](https://github.com/JustIkra/umir-gateway/issues/12) — gateway + frontend | Предыдущая постановка OTP-входа и восстановления пароля предшествует решению вынести auth и отправку писем в приватный сервис. | Уточнить контракт и клиент при выполнении Gateway #18. |

На 2026-10-12 [Gateway #1](https://github.com/JustIkra/umir-gateway/issues/1), [Gateway #8](https://github.com/JustIkra/umir-gateway/issues/8) и [Feedback #1](https://github.com/JustIkra/umir-feedback/issues/1) закрыты как выполненные для сервисного кода и согласованной исходной интеграции. Приёмка gateway #1 подтверждается межмодульными тестами и HTTP-проверками реальных сервисов. В комментариях приведены merged PR и evidence. Их закрытие не меняет selected pins и не подтверждает deployment. Публикация новых SHA, настройка секретов на ВМ и проверка реальных каналов доставки остаются отдельным этапом.

## Бизнес-ядро и разработка

### Рекомендации и используемые технологии

Слайд 8 исходной презентации кейса «Мониторинг остатков, сроков годности и стоп-листов» содержит рекомендации, а выбор языка оставляет участникам.

| Область | Рекомендуется организаторами | Используется в проекте |
| --- | --- | --- |
| Backend | C#/.NET/ASP.NET, Java, Go | Go, net/http, хранилища в памяти |
| Frontend | JavaScript или TypeScript со SCSS, React, Chakra | В main JavaScript и React/Vite; в опубликованной quality-ветке TypeScript и SCSS. Chakra UI запланирована. |
| Типы и тесты frontend | Конкретные инструменты не заданы | В quality-ветке TypeScript, ESLint, dependency-cruiser и Bun. openapi-typescript и browser/E2E-проверки добавляются при реализации клиента и экранов. |
| Проверка и сборка системы | Конкретные инструменты не заданы | Go/race и HTTP-проверки фабрики, Docker Compose, Nginx, GitHub Actions |

Таблица различает установленный стек и согласованные дополнения. Правила подключения frontend-инструментов находятся в [handoff](docs/gateway-frontend-handoff.md#11-стек-и-проверки-frontend) и README frontend. Рекомендации презентации не меняют согласованные бизнес-контракты и владельцев сервисов.

Начните с [business-core](docs/business-core.md), затем прочитайте [архитектуру Go](docs/go-architecture.md) и README изменяемого сервиса. Эти документы задают поведение и границы исходного ядра; сетевую интеграцию контейнеров нужно проверить отдельно.

Отдельный разработчик может выполнять [задачу gateway/frontend](docs/gateway-frontend-handoff.md) с уже реализованными HTTP-сервисами. Эта спецификация определяет интерфейсы gateway, wire contract и межмодульную приёмку. [Задание на Figma](docs/frontend-design-brief.md) описывает дизайн и evidence; [проектный review-skill](skills/umir-pr-review/SKILL.md) хранит особенности будущих PR. Порядок начала работы и список задач находятся в связанных GitHub issues. Межмодульные, HTTP и браузерные проверки подтверждают разные уровни поведения; восстановление бизнес-состояния после перезапуска остаётся отдельной задачей.

| Репозиторий | Работа первого этапа | Свидетельство результата |
| --- | --- | --- |
| warehouse | Значения, события, FEFO, единый допуск, in-memory EventStore/SnapshotStore, полные снапшоты | Собственные контрактные и приёмочные Go-тесты. |
| serving | Потребительские порты, предложения, подтверждение, меню, стоп-лист, рекомендации, in-memory ProposalStore | Собственные Go-тесты сценариев и переходов предложения. |
| factory | Три Go-модуля: сборочный runner, совместные business-проверки и HTTP-приёмка | Сценарии двух Runtime, реальные HTTP-процессы, сетевые сбои и точные identities сборки. |
| gateway | Proxy публичного HTTP API с конечными бюджетами и graceful shutdown | Тесты маршрутизации, таймаутов и завершения; приёмка реальной сборки через публичный gateway. |
| frontend | Существующая точка входа; OpenAPI v1 задаёт схему API | Текущая сборка Vite; экраны и браузерные сценарии ещё впереди. |

Совместная проверочная сборка находится в `tests/business`; её bridge связывает публичные contract/bootstrap двух сервисов и явно переводит значения. Сервисные модули независимы; приложение serving не импортирует warehouse. Автономный модуль `tools/assembly-checks` предоставляет команды `business`, `check-images`, `smoke` и `http-acceptance` на Go. Реальный HTTP harness находится в отдельном модуле `tests/http`:

```sh
GOWORK=off go -C tools/assembly-checks build -o /tmp/umir-checks
/tmp/umir-checks business --factory-dir "$PWD" --local
/tmp/umir-checks business --factory-dir "$PWD" --warehouse-ref <full-commit-sha> --serving-ref <full-commit-sha>
```

Каждая команда требует существующий абсолютный `--factory-dir`. Локальный режим использует соседние checkout `umir-warehouse` и `umir-serving`; режим фиксированных источников клонирует полные SHA из публичных репозиториев и проверяет `HEAD`. Runner создаёт временный workspace через `go work init` с абсолютными путями и `GOWORK=off`; версия учитывает требования выбранных модулей. Он удаляет только свои временные файлы, не меняет сервисные `go.mod` и не выполняет `go work sync`. Совместная бизнес-проверка работает в памяти и не требует контейнеров; она не проверяет сетевое восстановление pending. CI собирает бинарник и вызывает его для сохранения точного кода дочернего процесса. CI запускает business-suite автоматически на выбранных warehouse и serving SHA. Эти исходники должны поддерживать текущий контракт тестов; HTTP candidate принимает отдельные четыре точных SHA.

Перед PR сверяйте изменения с business-core, запускайте проверки владельца и обновляйте его README. Фикстуры хранятся в тестах; настройки и workers собираются в точке запуска. Warehouse больше не запускает expiration worker; свежие складские операции сохраняют обработку срока. Gateway владеет будущим расписанием `POST /expiration-runs`, но его реализация остаётся внешней зависимостью. Snapshot retry независим и задаётся `SNAPSHOT_RETRY_INTERVAL`. Смена закреплённых образов остаётся отдельным изменением сборки.

## HTTP Acceptance

CI reads the canonical `x-umir-http-acceptance` marker with `scripts/http-acceptance-stage.sh`. Selected HTTP acceptance is enabled only by `selected`; `health-only` is an explicit older-stage choice. Missing, invalid or duplicate markers fail CI. `bash tests/infra/http-stage-checks.sh` verifies activation without optional search tools.

Fixture dates use warehouse `inventory.evaluatedAt` through the public gateway. The harness validates the Moscow timestamp and stops if the clock sample is unavailable or invalid. It does not fall back to host time. Delivery failures retain the exact synthetic request, host send time and a warehouse clock sample; they do not trigger automatic POST retries.

`tests/http` is a separate Go module. `make check-http` runs its build, lint, architecture and race-test gates without Docker. The compiled assembly runner has two mutually exclusive modes:

```sh
/tmp/umir-checks http-acceptance --factory-dir "$PWD" --candidate \
  --warehouse-ref <40-character-sha> --serving-ref <40-character-sha> \
  --gateway-ref <40-character-sha> --frontend-ref <40-character-sha>
/tmp/umir-checks http-acceptance --factory-dir "$PWD" --selected
```

Candidate mode verifies each source checkout and builds local test images from the exact commits. It does not require registry publication. Selected mode checks the selected commit publication, pulls those exact images, and checks their OCI labels and HEALTHCHECK. Both run normal public-gateway scenarios on the four-service assembly, then start a separate fault-fixture assembly for network-failure cases. Only gateway and frontend receive ephemeral loopback host ports. Warehouse's internal listener stays on the Compose network at port `8082`.

The manual CI candidate gate accepts four complete commit SHA inputs. Selected HTTP acceptance remains gated on published full-SHA images selected in Compose. Scenario outcomes and image/container identities are retained as JSON evidence. Candidate images use stable local tags derived from each exact source SHA. The fault fixture tag uses the factory commit SHA and dirty-tree content hash. Compose project names remain unique for each run, while Docker reuses these tags and their shared layers. Acceptance cleanup removes only its owned Compose projects; it does not remove cached images.

### Operator auth candidate

`compose.auth.yaml` turns the assembly into the one-port operator auth topology of gateway#8 and gateway#10. Applied after `compose.yaml`, it removes the gateway host port, so frontend is the only application port. It also configures operator sign-in with a required exact `OPERATOR_PUBLIC_ORIGIN` and keeps the account file on the `operator-data` volume. Its top-level line `x-umir-auth-acceptance: candidate` is the explicit capability marker. The selected assembly never loads the file, and nothing detects auth from responses.

```sh
/tmp/umir-checks http-acceptance --factory-dir "$PWD" --candidate --auth \
  --warehouse-ref <40-character-sha> --serving-ref <40-character-sha> \
  --gateway-ref <40-character-sha> --frontend-ref <40-character-sha>
```

`--auth` requires `--candidate` and the marker. For each run the runner does the following:
- **Operator.** It creates a disposable operator. The candidate gateway image runs `hash-password` with a random password on stdin, never in argv or the environment. The password stays in a private file of the run workspace.
- **Port.** It picks a free loopback port that is both the frontend host port and the exact public origin. Before any scenario, `docker compose ps` must show that only frontend publishes a port, exactly `127.0.0.1:<port>->8080`. Any other published container of the project stops the phase; the only exception is the control port 9090 of the two fault fixtures in the faults phase, on `127.0.0.1` only.
- **Requests.** Smoke checks and the harness use the frontend URL for the API. The harness signs in through frontend and sends the session cookie with every request, and the exact `Origin` and `X-CSRF-Token` with every unsafe one.
- **Scenarios.** Before the business scenarios it records:
  - A01: business requests without a session get `401` and change nothing, and any success fails the run;
  - A02: sign-in and `/me`;
  - A03: missing or wrong tokens, foreign origins and cross-site requests get `403` and change nothing;
  - A04: logout of a second session.
- **Result.** The matrix requires A01–A04 in addition to H01–H25. Evidence records `"auth": true`, and cleanup also removes the disposable account volume.

The manual CI candidate gate runs this mode with the `http_auth` input. Selected acceptance keeps its published draft.2 contract until the coordinated image publication and pin update.

### Realtime candidate acceptance

The optional `--realtime` phase requires both `--candidate` and `--auth`. It must use an actual supplied gateway candidate that serves `/api/live`; absence of that route fails the phase. The runner must not silently skip the phase, substitute a mock stream, or weaken selected acceptance. Normal and fault suites record the flag and exact source revisions. The phase checks the SSE headers, `retry: 3000`, initial event order, coalesced business changes, session deadlines, gateway epoch changes, passive expiration history, logout/EOF, and the upstream unavailable/recovered transition.

The harness and Docker host must have synchronized clocks. R07 compares the fixture's fault and recovery timestamps with the SSE reader's receive timestamps. Receive time is captured before an event is queued, so a buffered early transition cannot be justified by a later response.

```sh
/tmp/umir-checks http-acceptance --factory-dir "$PWD" --candidate --auth --realtime \
  --warehouse-ref <40-character-sha> --serving-ref <40-character-sha> \
  --gateway-ref <40-character-sha> --frontend-ref <40-character-sha>
```

This is a candidate contract and acceptance mode. Full real-process evidence depends on the gateway and Nginx candidate owned by the other developer. Nginx must configure exact `location = /api/live`, HTTP/1.1, empty `Connection`, buffering/cache/gzip off and a 60-second read timeout. Because Nginx hides `X-Accel-*` response headers by default, configure `proxy_pass_header X-Accel-Buffering;` for the required browser-visible response header ([official documentation](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_hide_header)). The selected image pins and public assembly remain unchanged by candidate acceptance.

### Feedback candidate

`compose.feedback.yaml` adds private feedback and `telegram-vpn` to the one-port auth candidate. It declares `x-umir-feedback-acceptance: candidate`. Gateway uses `FEEDBACK_URL=http://feedback:8084`; feedback listens on 8084, while the proxy listens on 8080. The feedback service has no host port. SMTP uses the application network; Telegram uses the internal `telegram-link` and VPN-only uplink.

The candidate service source is [feedback 366fe265](https://github.com/JustIkra/umir-feedback/commit/366fe265f64581a0b638aec43cf6cf42b7e612ad). Gateway [PR #16](https://github.com/JustIkra/umir-gateway/pull/16) and [PR #17](https://github.com/JustIkra/umir-gateway/pull/17), together with frontend [PR #3](https://github.com/JustIkra/umir-frontend/pull/3), supply its public boundary. This candidate extends [factory PR #5](https://github.com/JustIkra/umir-factory/pull/5); selected image pins are unchanged.

```sh
/tmp/umir-checks http-acceptance --factory-dir "$PWD" --candidate --auth \
  --warehouse-ref 3e37af2f74a498c425c2dcb561be1bd3c9c01160 \
  --serving-ref 0fb867e7ff58e3106fa819355fc1ee298da58074 \
  --gateway-ref 0e7dbfd628fcab71188a67bb903044966ccd4248 \
  --frontend-ref 429ff994abf726d45926fcaf06d8bd3f33d61e50 \
  --feedback-ref 366fe265f64581a0b638aec43cf6cf42b7e612ad
```

Feedback acceptance builds the server and VPN images from the same exact service commit and records their source/revision labels and image identities. It creates private synthetic mail/bot configuration and proxy files for the owned Compose project; no user credentials are used. F01–F05 cover anonymous and session-bound submission, exact replay, identity conflict, validation/consent errors, honeypot acknowledgement, Origin/Fetch Metadata/CSRF, the 16 KiB boundary, and per-context limits with required `Retry-After`. Service-level tests cover journal atomicity, recovery, retention, and provider protocols; public acceptance does not inspect honeypot storage or claim live provider delivery.

VPN health can be unavailable in the synthetic run. The runner waits for application services and feedback, while requiring the VPN supervisor to run; intake never depends on VPN health. Only frontend publishes an application port. Cleanup removes only disposable data in this acceptance project. The manual CI candidate gate accepts optional `feedback_ref` together with `http_auth`.

For runtime composition, apply `compose.yaml`, `compose.auth.yaml`, then `compose.feedback.yaml`. Set `FEEDBACK_IMAGE` and `TELEGRAM_VPN_IMAGE` to tested full-SHA images. Set `FEEDBACK_ENV_FILE` to closed mail/bot and public configuration. Set `TELEGRAM_VPN_SUBSCRIPTION_URL_FILE` to a closed raw URL file and `TELEGRAM_PROXY_USERNAME_FILE`/`TELEGRAM_PROXY_PASSWORD_FILE` to separate random credentials. Set `TELEGRAM_VPN_DNS` to the controlled bootstrap resolver. The dotenv file from a secret form is not a raw URL file. Compose file-backed secrets do not reliably apply UID/mode; provision source files for UID/GID 65532 with mode 0400 or 0600. Existing state directories require owner 65532 and mode 0700.

Project runtime credentials are kept locally under the Git-ignored `.env.secrets/` directory. Keep the directory at mode `0700` and files at `0600` or `0400`. Prepared dotenv files are `feedback.env`, `telegram-vpn.env`, `auth-smtp.env`, and `auth-mail.env`; raw per-service files are grouped under `feedback/`, `auth/`, and `vpn/`. Names describe configuration only; the files contain private values and must stay out of Git and build contexts. The factory `.dockerignore` excludes `.env`, `.env.*`, and `.env.secrets/` while allowing `.env.example`. Prepared files are configuration inputs, not a complete runtime environment. Add all required service settings and validate them against the service README. This protects Docker build contexts; it does not configure Compose or inject these files into containers. File-backed Docker secrets need explicit runtime wiring, and the source files must actually be readable by UID/GID 65532 with the required mode on Linux because Compose file-secret UID/mode settings are advisory for bind-mounted files.

Feedback stores requests in its volume and physically removes them after required `FEEDBACK_RETENTION_DAYS`; acceptance uses 30 days and consent version v1. Deployed retention and placement must be configured before selection. Operator email copies are outside this retention. Telegram receives only the ID and Moscow time. See the [service README](https://github.com/JustIkra/umir-feedback#readme) for queue, retry, export/delete, capacity, and configuration details. OTP ownership and migration are tracked in [gateway #18](https://github.com/JustIkra/umir-gateway/issues/18); the OTP code merged through [feedback PR #3](https://github.com/JustIkra/umir-feedback/pull/3) is disabled by default and remains only a transfer/reuse candidate.

Before either phase starts, the runner executes the three warehouse owner, recheck, and HTTP pending tests against the exact selected warehouse source with `go test -race -count=1 -json`. Evidence records the source SHA and test names and preserves raw JSON and stderr files. The runner writes its evidence record before source resolution, image build, or pull can fail, so early failures retain the facts already known.
The current selected image pins remain unchanged until tested HTTP service commits are published and selected.

Полный Go gate фабрики проверяет все вложенные модули из `.go-quality-modules`:

```sh
make tools
make check-tools
make check-business
make check-http
make check
```

`check-tools` проверяет автономный runner с GOWORK=off. `check-business` создаёт временный workspace с соседними сервисами; другие пути задаются через WAREHOUSE_DIR/SERVING_DIR. `check-http` изолирован от service modules и Docker. Gates включают форматирование, сборку, закреплённые linters, архитектуру и полные race-тесты. Версии и правила находятся в `.go-quality-versions`, `.golangci.yml` и `.go-arch-*.yml`. Необъявленный или отсутствующий вложенный модуль вызывает ошибку. Для форматирования отдельных файлов выберите QUALITY_MODULE=tools, business или http и передайте FILES как список путей с разделением новой строкой.

## Gateway OpenAPI

Operator routes describe frontend proxy failures as 502 `UpstreamUnavailable` and 504 `UpstreamTimeout`. GET responses use `outcome: unavailable`; modifying requests use `outcome: unknown`. A lost response does not prove that the account or session stayed unchanged. Resolve a lost display-name response with `/api/operator/me`, and a lost password response by signing in. The proxy does not retry modifying requests.

[OpenAPI v1](docs/api/openapi.yml), revision `1.1.0-draft.1`, defines the public gateway contract for 15 business operations, three health routes, operator account routes and the public `POST /api/feedback` boundary. The current auth candidate serves the implemented account/session and feedback operations; the selected images retain their earlier draft.2 behavior. Revision 1.1.0-draft.1 also specifies candidate-dependent `GET /api/live`, `X-Umir-Activity` and strict SSE event/error schemas; selected images do not implement these additions. The auth service extraction remains gateway #18 owner work. Real gateway, serving and warehouse processes exchange business requests over HTTP. Business values use bounded decimal strings and Moscow `+03:00` timestamps; serving recipe/order DTOs map to wire `ingredients` and `acceptedPositions`. [The public warehouse service contract](docs/api/warehouse-public.yml) defines `POST /expiration-runs` on the private Compose listener. It has no gateway/browser route, and `/api/warehouse/expiration-runs` returns 404. [The internal warehouse contract](docs/api/warehouse-internal.yml), revision `1.0.1-draft.1`, defines the private serving-to-warehouse listener and recorded command/attempt results. It references common scalar schemas in `openapi.yml`; distribute the referenced documents or bundle them. API revisions are separate from service releases and selected image pins. These local drafts are contracts, not claims that selected images implement their candidate-only features.

Data and confirmation attempts stay in process memory. Restarts lose the state owned by the restarted service; a missing proposal has outcome `unknown`. Resolve is passive and exact retries replay saved results. `ProposalSuperseded` is a normal HTTP 200 result; pending uses 202 and command identity conflicts use 409. Invalid read input returns `unavailable`, rejected modifying input returns `not_committed` for that input, and invalid Resolve input leaves the original confirmation `unknown`. A valid Resolve can replay a saved owner refusal as `not_committed`. Missing warehouse dishes can return a cached `DishNotFound` refusal after a warehouse memory reset. Inventory and transport failures on write paths keep outcome `unknown`. Preparation first evaluates stock through the non-mutating internal `POST /order-evaluations`; its authoritative read errors use `unavailable`, and no proposal is created. A repeated preparation creates a new proposal ID.

Contract sources are the local [gateway handoff](docs/gateway-frontend-handoff.md) and [business rules](docs/business-core.md), updated with the reviewed delivery form fields and timestamp semantics.

## Запуск опубликованной сборки

Требуются Git, Docker Engine и Docker Compose v2. Образы публичные; для скачивания из GHCR вход не требуется.

```sh
git clone https://github.com/JustIkra/umir-factory.git
cd umir-factory
cp .env.example .env
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose ps
```

| Адрес по умолчанию | Назначение |
| --- | --- |
| `http://127.0.0.1:8080/` | Точка входа frontend. |
| `http://127.0.0.1:8080/healthz` | Проверка frontend-контейнера. |
| `http://127.0.0.1:8081/healthz` | Проверка gateway. |
| `/api/serving/healthz` через порт 8080 или 8081 | Запрос через gateway к serving. |
| `/api/warehouse/healthz` через порт 8080 или 8081 | Запрос через gateway к warehouse. |

Healthz проверяет работоспособность процессов. Serving и warehouse не публикуют порты хоста в этой сборке.

Compose наследует `HEALTHCHECK` из Dockerfile каждого сервиса. Go-образы выполняют `/server healthcheck`, frontend выполняет `wget` к своему `/healthz`. Проверки запускаются каждые 10 секунд с timeout 3 секунды и start period 5 секунд; три последовательные ошибки дают статус unhealthy. Эти проверки нужны для `depends_on: service_healthy` и `up --wait`; новый образ должен сохранить рабочий HEALTHCHECK. CI проверяет его наличие в скачанном образе до запуска контейнеров.

Настройки запуска задаются в `.env`:

| Переменная | Значение по умолчанию |
| --- | --- |
| `UMIR_BIND` | `127.0.0.1` |
| `UMIR_WEB_PORT` | `8080` |
| `UMIR_API_PORT` | `8081` |

### Сборка с личным кабинетом оператора

Кандидат с входом оператора собирается из исходников gateway и frontend с поддержкой кабинета: соседние checkout через `compose.dev.yaml` и `compose.auth.yaml` после него. Наружу публикуется только frontend, и только на `127.0.0.1`: `compose.auth.yaml` задаёт этот адрес сам, `UMIR_BIND` на него не влияет, а gateway всегда работает в режиме `loopback-development`. Это сборка для разработки и приёмки, её HTTP-вход не выходит за пределы машины. У gateway нет порта на хосте. Хэш пароля даёт образ gateway, пароль вводится в stdin и никуда не записывается:

```sh
docker compose -f compose.yaml -f compose.dev.yaml build gateway
docker run --rm -it umir-gateway:dev hash-password
export OPERATOR_PUBLIC_ORIGIN=http://127.0.0.1:8080 OPERATOR_BOOTSTRAP_LOGIN=operator \
  OPERATOR_BOOTSTRAP_DISPLAY_NAME='Оператор' OPERATOR_BOOTSTRAP_PASSWORD_HASH='<PHC из hash-password>'
docker compose -f compose.yaml -f compose.dev.yaml -f compose.auth.yaml up -d --build --wait --wait-timeout 120
```

`OPERATOR_PUBLIC_ORIGIN` обязателен и должен точно совпадать с адресом frontend в браузере, включая порт (`UMIR_WEB_PORT`). Если хэш записан в `.env`, возьмите его в одинарные кавычки: Compose подставляет `$` в значениях env-файла. Учётная запись создаётся при первом запуске на томе `operator-data`; дальше файл главнее `OPERATOR_BOOTSTRAP_*`. API доступен только через `http://127.0.0.1:8080/api/`, вход описан в [README gateway](https://github.com/JustIkra/umir-gateway#личный-кабинет-оператора).

Если пауза смены пароля не активна и новый пароль допустим, `currentPassword` длиннее 32 Unicode code points отклоняется до проверки учётных данных: `422 PasswordRejected` с `fieldErrors.currentPassword: TooLong`. Такой запрос не расходует счётчик неверных проверок и не отзывает сессию. Пятая фактическая неверная проверка за скользящие 60 секунд отзывает только вызывающую сессию и запускает cooldown на 60 секунд; остальные сессии сохраняются. Пароли не обрезаются и не нормализуются. Подробные ошибки и состояния клиента определены в [OpenAPI](docs/api/openapi.yml) и [design brief](docs/frontend-design-brief.md#вход-и-кабинет-оператора).

### Публичный контур

Публичная сборка на ВМ ещё не развёрнута. Она появится вместе с переводом выбранных пинов на образы с входом оператора. Схема по gateway#8:
- **HTTPS-вход.** Nginx Proxy Manager принимает HTTPS на 443 и завершает TLS. Его Proxy Host пересылает настроенный домен по сети ZeroTier на `http://<адрес ZeroTier ВМ>:8080`, то есть на frontend Nginx.
- **Порты.** На ВМ порт хоста есть только у frontend: 8080 и только на адресе ZeroTier. У gateway, serving и warehouse портов на хосте нет.
- **Режим gateway.** `OPERATOR_COOKIE_MODE=secure`, `OPERATOR_PUBLIC_ORIGIN=https://<домен в виде A-label>`. Cookie остаются `Secure` с префиксом `__Host-`, хотя внутренние участки идут по HTTP: защиту cookie и допустимый origin задаёт настройка, а не заголовки `X-Forwarded-*`.
- **Повторы.** Ни NPM, ни frontend Nginx не повторяют запросы. Таймаут, разрыв или 502/504 после отправки означают неизвестный исход.
- **Параметры.** Адрес ZeroTier и домен — параметры развёртывания, в репозитории их нет. Это переменные GitHub Actions `UMIR_BIND`, `UMIR_PUBLIC_URL`, `OPERATOR_PUBLIC_ORIGIN` и `OPERATOR_COOKIE_MODE`. `compose.auth.yaml` для ВМ не подходит: он всегда держит frontend на `127.0.0.1` в режиме `loopback-development`.
- **Проверка.** Локальные проверки Compose не доказывают публичный TLS и маршрут. Домен и сертификат проверяются отдельно, на самой ВМ.

## Локальная сборка исходников

Из каталога, который содержит `umir-factory`, клонируйте остальные репозитории:

```sh
git clone https://github.com/JustIkra/umir-gateway.git
git clone https://github.com/JustIkra/umir-serving.git
git clone https://github.com/JustIkra/umir-warehouse.git
git clone https://github.com/JustIkra/umir-frontend.git
cd umir-factory
docker compose -f compose.yaml -f compose.dev.yaml up -d --build --wait --wait-timeout 120
```

Итоговая структура:

```text
workspace/
├── umir-factory/
├── umir-gateway/
├── umir-serving/
├── umir-warehouse/
└── umir-frontend/
```

Файл `compose.dev.yaml` использует соседние каталоги как контексты сборки, отдельные локальные теги `umir-<service>:dev` и pull_policy=build. Он не перезаписывает выбранные SHA-теги. Порты и сеть остаются такими же, как у опубликованной сборки.

## CI/CD и обновление образов

Сервисные workflows проверяют push в ветки, PR в main и ручные запуски. Warehouse и serving проходят закреплённый make check с форматированием, build, lint, архитектурой и race-тестами; frontend проходит npm ci и сборку Vite. Сервисы проверяют Docker-сборку.

После успешной проверки main публикуется образ `ghcr.io/justikra/umir-<service>:sha-<полный SHA коммита>` для `linux/amd64` и `linux/arm64`. Публикация использует штатный `GITHUB_TOKEN`; разрешение `packages:write` задано только заданию публикации. Это правило действует и для коммитов с изменением документации.

Новый образ не меняет выбранную сборку автоматически:

1. Найдите успешный запуск сервиса и полный SHA его коммита.
2. Измените тег соответствующего образа в `compose.yaml` в рабочей ветке фабрики.
3. Откройте PR с описанием изменения и ссылкой на CI сервиса.
4. Дождитесь успешного CI фабрики и merge от `JustIkra`.
5. Выполните `docker compose pull` и `docker compose up -d --wait --wait-timeout 120` на целевом хосте.

CI фабрики устанавливает Go 1.26.9, проверяет runner, собирает umir-checks, проверяет оба Compose и SHA-теги. Общий скрипт verify-assembly проверяет существование выбранных commit и успешные Check/Publish image на main до pull. После pull он проверяет OCI source/revision и HEALTHCHECK, запускает четыре контейнера и проверяет frontend и маршруты gateway. При сбое показывает логи временной сборки. Совместный business job запускается после успешной проверки сборки на каждом push, PR и ручном запуске; оба точных SHA извлекаются из Compose через Go. Проверки сборки выполняются командами:

```sh
docker compose --env-file .env.example config --quiet
docker compose --env-file .env.example -f compose.yaml -f compose.dev.yaml config --quiet
make check-tools
bash tests/infra/image-checks.sh
bash tests/infra/assembly-checks.sh
bash tests/infra/http-compose-checks.sh
GOWORK=off go -C tools/assembly-checks run . check-images --factory-dir "$PWD"
GOWORK=off go -C tools/assembly-checks run . smoke --factory-dir "$PWD" --env-file .env.example
```

`check-images` читает JSON от `docker compose config` с .env.example. `smoke` требует запущенную сборку; он определяет адреса через Compose port frontend/gateway 8080. --env-file задаёт файл относительно factory-dir; по умолчанию Compose читает .env. Для других адресов используйте --web-url и --api-url. Оба явных URL позволяют обойти Docker discovery. Для CI используйте собранный бинарник, чтобы сохранить точный код возврата проверки.

Полную проверку выбранных образов на локальном Docker daemon запускайте из checkout фабрики:

```sh
bash scripts/verify-assembly.sh --env-file .env.example
```

Нужны Docker Compose, GitHub CLI с `gh auth login` либо `GH_TOKEN`, jq и Go 1.26.9. Через `UMIR_CHECKS_BIN` можно передать абсолютный путь к заранее собранному umir-checks, как делает CI. Скрипт использует только production compose.yaml, собственное уникальное имя проекта с префиксом `umir-check-` и свободные порты на 127.0.0.1. Параметры текущей production-сборки и переопределения Compose не используются. После проверки скрипт удаляет только свои контейнеры и сеть; при частичном старте или ошибке smoke он сначала показывает их логи. Ошибка очистки также завершает проверку неуспешно. Деплой на целевой хост остаётся отдельным шагом после успешного CI.

Фабрика не публикует собственный контейнер. Автоматический деплой на сервер пока не настроен.

## Ведение документации

Обновляйте документацию в том же PR, где меняются реализация, стек, установка, архитектура, проверки или эксплуатация. Рекомендации организаторов, согласованные дополнения, планы и фактически используемые инструменты обозначайте отдельно. В командах и результатах проверок описывайте то, что действительно существует и выполнено.

| Документ | Что в нём поддерживать |
| --- | --- |
| README каждого репозитория | Используемый стек, требования и версии среды, установка, реальные команды разработки/проверок/сборки, архитектура, текущее состояние и эксплуатация. |
| [Business core](docs/business-core.md) | Согласованные бизнес-правила, владельцы решений и приёмка. |
| [Публичная OpenAPI](docs/api/openapi.yml), отдельный внутренний контракт в docs/api | Поля, маршруты, варианты результатов, ошибки и версии соответствующего HTTP API. Frontend использует публичную схему. |
| [Gateway/frontend handoff](docs/gateway-frontend-handoff.md) | Межсервисные границы, стек frontend, состояния клиента и интеграционные проверки. |
| [Дизайн-бриф](docs/frontend-design-brief.md) | Визуальные решения, Figma, токены, доступность и критерии приёмки интерфейса. |
| AGENTS.md | Короткие инструкции репозитория и ссылки на подробные документы. |
| CHANGELOG.md | Изменения поведения, контрактов, проверок и эксплуатационных процедур под «Не выпущено» до релиза. |

README и AGENTS ссылаются на канонические контракты; копии бизнес-правил и HTTP-схем в них не создаются. Если меняются сами контракт или дизайн, обновляйте соответствующий документ и согласуйте изменение с владельцем. Issue и PR содержат краткое решение, ссылки на точные commit-ревизии документов и API, а также фактические результаты проверок. Локальный файл или незакоммиченная схема не считаются опубликованной ревизией. Версии зависимостей и lockfiles меняются вместе с инструментами; рабочие заметки агентов не заменяют документацию разработчика.

## Совместимость и версии

Публичная поверхность фабрики — Compose-настройки, точные identities выбранных образов и CLI umir-checks (аргументы, проверки и код выхода). Business suite использует только публичные пакеты сервисов. Данные Runtime существуют в памяти процесса; такая сборка не проверяет HTTP, позднюю запись по сети или восстановление pending после перезапуска.

Используется SemVer. До 1.0 исправления получают patch, добавления или несовместимые изменения — minor с описанием миграции. После 1.0 несовместимые изменения требуют major. До выпуска изменения остаются в CHANGELOG под «Не выпущено». Корневые теги имеют вид vX.Y.Z; независимо публикуемые вложенные модули используют префиксы tools/assembly-checks/vX.Y.Z и tests/business/vX.Y.Z. При v2+ import path содержит /v2; завершающий каталог major не дублируется в префиксе тега. Выпущенные теги неизменны. SemVer не заменяет точные SHA образов фабрики.

## Доступ и совместная работа

- Все пять репозиториев публичные; issue включены.
- Только `JustIkra` может обновлять main и объединять PR. Активный ruleset main имеет единственное исключение для его аккаунта.
- `Kxqusos` добавлен во все репозитории с ролью write: работа в остальных ветках и PR.
- Другие пользователи GitHub могут создать fork, рабочую ветку в нём и PR.
- `.github/CODEOWNERS` указывает `JustIkra` как владельца кода.

Перед PR запускайте проверки затронутого сервиса. Согласовывайте изменения межсервисных контрактов с обеими сторонами. Настоящие `.env`, токены и рабочие данные ресторана в Git не сохраняются.

## Development standard and releases

Repository instructions are in [AGENTS.md](AGENTS.md). Public compatibility covers documented requests, results, error states and operational interfaces. Warehouse and serving publish their Go contract surfaces; internal refactoring does not automatically change those contracts.

The full [development standard](docs/development-standard.md) describes Go, Git and versioning. The [module inventory](.go-quality-modules) declares three real nested Go modules. `make go-readiness` checks that inventory against the repository; `make check-http` runs the HTTP harness build, lint, architecture and race gates without Docker. `make check` runs the gates for all three modules. The effective lint and architecture configurations are `.golangci.yml` and `.go-arch-*.yml`. Factory HTTP acceptance separates ordinary public-gateway scenarios from network-fault scenarios, then records exact image and container IDs, scenario evidence and cleanup.

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Stable incompatible changes increment major, compatible additions minor and fixes patch. Our pre-1.0 convention uses patch for compatible fixes and minor for additions or breaking changes. Mark breaking behavior and migration explicitly.

Release repositories independently. Use annotated `vX.Y.Z` tags for actual releases; published nested Go modules prefix tags with the logical module subdirectory (excluding a trailing major-version directory), and Go v2+ needs the matching major import path. Published tags/versions stay immutable. Record changes under Unreleased before releasing. Preserve Umir `sha-<full-SHA>` image identities and exact factory component pins; SemVer does not replace them with `latest`. API versions and service releases are separate.

Preserve unrelated local edits, stage reviewed paths and check `git diff --cached` and `git diff --check`. Tagging, publishing and shared-history rewrites require the corresponding user instruction. This standard does not create a release or change the current version.
