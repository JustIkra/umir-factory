# UMIR Factory

Единственный текущий план исполнения — [«Версия для защиты», редакция 2](docs/current-plan.md), выбранная пользователем 14.10.2026. [HTML-артефакт](docs/current-plan.html). Обязательны FIN-01–09 и FIN-11–13; FIN-10 и Feedback optional. Все прежние планы и незавершённый qualification backlog отменены как задачи исполнения. Действующие contracts и необходимые regression/quality gates сохраняются. FIN1 опубликован и запущен на существующем HTTPS-стенде. Основной сценарий, доставка писем, завершение OTP и права нового пользователя проверены на стенде.

The [REG1 registration supplement](docs/registration-api-amendment.md) extends FIN1 with opt-in public email registration. Generate its composed profiles with `node tests/http/tools/compose-registration-contract.mjs docs/api`; run `make check-contract`. Completion private inputs accept `registration.enabled` (boolean, default `false`). Runtime source acceptance and deployment remain separate.

The [FIN1 API supplement](docs/completion-api-amendment.md) defines supply suggestions and the revised recommendation policy. Its [public profile](docs/api/completion-v1-composed-public.json), [private profile](docs/api/completion-v1-composed-private.json), [examples](docs/api/completion-v1-examples.json), [acceptance predicates](docs/api/completion-v1-acceptance.json) and [policy transition](docs/api/completion-v1-transition.json) are separate from frozen C2/UA1 artifacts. Generate both profiles with `node tests/http/tools/compose-completion-contract.mjs docs/api`. `make check` validates schema examples, malformed values and reproducible composition in the HTTP test module. These checks validate the contract files; they do not establish service or assembly acceptance. The exact deployed profile and current acceptance limits are in [completion-stand.json](docs/completion-stand.json).

Фабрика собирает Auth, Gateway, Warehouse, Serving и Frontend. Поддерживаемый FIN1-запуск описан в разделе [FIN1 with real Auth](#fin1-with-real-auth). Feedback включается отдельным профилем. Публикация образов и доставка на существующий HTTPS-стенд выполняются после приёмки candidate.

## Репозитории и границы

| Репозиторий | Ответственность |
| --- | --- |
| [umir-factory](https://github.com/JustIkra/umir-factory) | Версии контрактов, точные source/image identities, Compose, seed и проверки сборки. |
| [umir-auth](https://github.com/JustIkra/umir-auth) | Аккаунты, сессии, роли, организации/рестораны, OTP и собственная почта. Auth один владеет durable authority и signing secrets. |
| [umir-gateway](https://github.com/JustIkra/umir-gateway) | Публичный API, свежая авторизация, маршрутизация и SSE. |
| [umir-serving](https://github.com/JustIkra/umir-serving) | Меню, стоп-лист, рекомендации и координация подтверждения заказа. |
| [umir-warehouse](https://github.com/JustIkra/umir-warehouse) | Каталог, поставки, партии, FEFO, атомарная продажа, сроки, ручное списание, история и предложение поставки. |
| [umir-frontend](https://github.com/JustIkra/umir-frontend) | Существующий React-интерфейс через Nginx и Gateway; бизнес-решения остаются у backend. |
| [umir-feedback](https://github.com/JustIkra/umir-feedback) | Необязательная обратная связь с отдельными SMTP/Telegram/VPN inputs. |

## Текущее состояние

FIN1 source profile закреплён в `scripts/completion/sources.json`. `compose.completion.yaml` запускает production-сервисы с реальным Auth и одним внешним входом frontend. Private transport использует проверяемый TLS. Auth authority хранится на отдельном постоянном томе; Warehouse и Serving сохраняют текущий режим в памяти. После их reset фиксированный демо-набор восстанавливается [одной командой seed](tests/examples/completion/README.md).

Склад исключает просроченное из пригодного остатка, переводит его в ожидание списания и фиксирует окончательное списание по команде человека. Заказ подтверждается только записанным OrderPlaced, неизвестный исход проверяется исходной командой и в сохранённой эпохе. Интерфейс показывает реальные партии FEFO и исходный расход; исторический срок берётся из записанного поступления той же эпохи.

Минимальный М6 использует фиксированное окно 7×24 часа, порог покрытия 3 дня и цель 7 дней. Рекомендации FIN1 учитывают срок, количество под риском и спрос; обе операции показывают неполную историю явно и не создают закупку. Количества сохраняют точность целых тысячных. Контракт и правила расчёта находятся в [FIN1 supplement](docs/completion-api-amendment.md).

Форматы C2/UA1, прежние manifests и evidence остаются историческими артефактами своих профилей. `compose.yaml` пока закрепляет опубликованный legacy compatibility profile. Он не является новой Auth-сборкой и не доказывает её готовность. Текущий принятый FIN1 profile использует отдельный явный файл `compose.completion.yaml` и pins в `scripts/completion/sources.json`. `compose.yaml` сохраняет прежний compatibility profile со своей историей проверок.

## Доставка текущей версии

15.10.2026 (Europe/Moscow) по поручению пользователя опубликованы пять точных image SHA/digest и запущен FIN1 на [существующем HTTPS-стенде](https://xn----ctbhsteek4c3c.empty-lab.ru). Main CI опубликованных ревизий пяти сервисов и развёрнутого recipe Factory прошли. [Манифест доставки](docs/completion-stand.json) фиксирует исходники, registry index/platform/config digests и границы проверки. Вход с secure cookie, seed/repeat, основной бизнес-сценарий, SSE refresh/change и минутный background refetch проверены через HTTPS. Приглашение и OTP реально доставлены из production Auth с первой попытки; получатель подтвердил оба письма. Регистрация по коду завершена; свежий вход официанта, меню/рекомендации/SSE прошли, чтение склада отклонено с 403. Ранний отказ соединения при повторной проверке не воспроизвёлся; настройки сети не менялись. Доставка сохраняет private inputs и Auth volume; Warehouse/Serving reseed выполняется через публичные API. Старые backlog и отдельные проекты аналитики/observability не входят в этот пакет.

Бизнес-правила исходного ядра описаны в [docs/business-core.md](docs/business-core.md), scoped wire contracts — в [docs/api](docs/api/backend-v2.md), выбранные изменения — в FIN1 supplement. При расхождении описаний приоритет имеет соответствующий контракт. Межмодульные, HTTP и браузерные проверки подтверждают разные уровни поведения; старое evidence не переименовывают в новый PASS.

## Бизнес-ядро и разработка

### Рекомендации и используемые технологии

Слайд 8 исходной презентации кейса «Мониторинг остатков, сроков годности и стоп-листов» содержит рекомендации, а выбор языка оставляет участникам.

| Область | Рекомендуется организаторами | Используется в проекте |
| --- | --- | --- |
| Backend | C#/.NET/ASP.NET, Java, Go | Go, net/http, хранилища в памяти |
| Frontend | JavaScript или TypeScript со SCSS, React, Chakra | В FIN1 candidate TypeScript, React/Vite и Chakra UI с существующей темой. SCSS установлен, но не используется. |
| Типы и тесты frontend | Конкретные инструменты не заданы | TypeScript, ESLint, dependency-cruiser, pinned Bun, генерация OpenAPI-типов и runtime-декодеры. Браузерная приёмка проходит через реальные сервисы. |
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

## FIN1 with real Auth

`compose.completion.yaml` is the current accepted FIN1 runtime profile. `compose.yaml` retains the separately documented legacy compatibility images. Exact application source identities are in `scripts/completion/sources.json`; local builds use `compose.completion.build.yaml`. Required tools are Docker Compose with `env_file.format: raw` and volume subpaths, Node.js 24.21.0 or newer, OpenSSL and Git. CI and the Auth readiness helper pin Node.js 24.21.0.

1. Copy `scripts/completion/inputs.example.json` to a private absolute path. Set the initial operator, real SMTP transport, private credential file references and registration sender details. Input and credential files must be owned regular mode 0600 files with no symlinks. Optional `smtpCAFile` supplies a private SMTP CA file. Do not put credentials in JSON values, shell arguments or repository files.
2. Prepare a new private directory and an explicit project. Use the exact ASCII HTTPS browser origin for the existing Nginx Proxy Manager route. The generated initial operator password is in `<directory>/auth/secrets/password`; read it only through a secure local channel. It is never printed.

```sh
node scripts/completion/prepare.mjs \
  --directory /absolute/private/umir-completion-stand \
  --inputs /absolute/private/inputs.json \
  --project umir-completion-stand \
  --origin https://example.org
```

3. After permitted image publication, start the pinned candidate:

```sh
docker compose --env-file /absolute/private/umir-completion-stand/compose.env \
  -f compose.completion.yaml config --quiet
docker compose --env-file /absolute/private/umir-completion-stand/compose.env \
  -f compose.completion.yaml up -d --wait --wait-timeout 120
```

For a local exact-source build, add `--sources /absolute/checkouts` during preparation and add `-f compose.completion.build.yaml` only to the `build` command. The directory contains clean `auth`, `warehouse`, `serving`, `gateway` and `frontend` checkouts at the recorded SHAs. Re-run preparation before rebuilding to verify their identities. Use the [isolated demo recipe](tests/examples/completion/README.md) for loopback cookies and synthetic mail.

The base assembly publishes frontend on `127.0.0.1:8080`. Keep the existing HTTPS proxy as the external edge. If that proxy runs on another host, set `COMPLETION_BIND` to the exact private interface IP reachable by it. Only frontend is published; do not expose backend ports or bind the app edge to all interfaces. Backend ports remain private. Auth uses verified TLS and a persistent `auth-state` volume. A private Nginx transport terminates verified TLS for the existing internal owner listeners. The readiness sidecar uses the discovery-only Warehouse credential to read JWKS. Auth checks its real runtime readiness before that response; a 401, 403 or unavailable store makes the sidecar unhealthy. Warehouse and Serving keep their existing in-memory storage model.

Existing raw runtime inputs in `.env.secrets/` remain private: directory mode 0700 and regular files mode 0600 or 0400. Git and the root Docker build context exclude them. Copy or reference only the required owner-specific files during preparation; never print their values in Compose output, logs or command arguments. Auth owns auth mail and account state; Feedback keeps its optional transport inputs separately.

Re-running preparation validates the original bundle and preserves every credential. Partial files, changed private bindings and tampering cause an explicit refusal; preparation never repairs or replaces existing authority. Source/image changes require the explicit `--update-images` preparation flag after review. It verifies the existing private bundle, then atomically updates only `compose.env` and its bundle metadata. Auth config, bootstrap input, transport keys and persistent authority remain unchanged. A normal repeat still refuses a source pin change. Initial configuration volumes are copied once and compared on subsequent starts. Auth alone writes its state and owner secrets. Retain all private files and volumes across restart. Use `stop` or `down` without `--volumes`; removing Auth state loses account authority.

For a target whose registry access is unavailable, pull the published `linux/amd64` images on a trusted delivery host, verify OCI source/revision and registry platform/config digests, and transfer a `docker image save --platform linux/amd64` archive over host-key-pinned SSH. Load the archive, then verify the same platform digest references and OCI labels on the target. Use a separate image env override with `ghcr.io/...@sha256:<platform-digest>` after the immutable bundle env. Start with `--pull never`; a missing exact image must fail. Keep host-path/bind overrides in another private env file. Do not replace the bundle, use local build identities as published images, or expose backend ports. Retain the deployment archive, hashes and original private files.

Account creation uses invitations and email OTP. The public chain is `POST /api/v2/orgs/{orgID}/invitations`, `POST /api/v2/invitations/{invitationID}/challenges`, `POST /api/v2/challenges/{challengeID}/verify`, and `POST /api/v2/challenges/{challengeID}/complete`. The initial operator comes from private bootstrap inputs. There is no public self-signup `/register` route. Test-account passwords stay in private files and never enter this receipt or repository. SMTP must be reachable from the deployed Auth network before invitation/OTP acceptance.

Feedback is opt-in with `--profile feedback`. Set `COMPLETION_FEEDBACK_IMAGE` to its reviewed exact image and `COMPLETION_FEEDBACK_ENV_FILE` to its existing private SMTP/Telegram configuration. Missing optional-provider inputs fail that service; the base demo has no readiness or dependency on it. With Feedback off, its endpoint reports unavailable rather than fabricated success. Auth invitation/OTP mail retains its separate SMTP configuration.

The [stand receipt](docs/completion-stand.json) records the observed publication and HTTPS readback. Production invitation delivery, recipient OTP completion and a fresh Waiter login are verified. Menu, recommendations and SSE succeed; stock access returns 403. The selected FIN1 runtime profile is `compose.completion.yaml`, with exact source pins and published image digests recorded in the receipt. Historical compatibility-profile evidence remains separate from these FIN1 runtime observations.

## Опубликованный legacy compatibility profile

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

### REG1 pre-merge review safeguards

The REG1 contract caps new client command IDs at 256 Unicode code points and separates strict login, password-reset and registration completion bodies. Auth defaults registration and email-code login to disabled. Password-reset requests are neutral for unknown addresses. Registration has a separate 30-per-10-minute global mail budget; the normal 60-per-10-minute mail budget remains available to other purposes. Default abuse limits are 10 wrong codes per address per rolling hour, 3 non-deleted organizations created per user and 2 organization creations per rolling hour. Private preparation accepts the bounded integer settings under `registration`; see `scripts/completion/inputs.example.json` and [REG1 operations](docs/registration-api-amendment.md).

Gateway also bounds registration begin requests before Auth by socket peer (30 per 10 minutes) and preauth (3 per 10 minutes), retaining identical retry attempts. Untrusted forwarded headers do not change that peer. Behind Nginx, the peer budget is shared by that proxy's visitors. These are process-local admission limits; Auth owns durable address/account limits.

Keep public registration disabled until the deferred PostgreSQL challenge cleanup is accepted (Auth issue 7). Current JSON state retains up to 4096 challenges. Auth startup migrates state to v3 even when registration is disabled. Before upgrading a stand, stop Auth and back up state with its matched secrets. Rollback requires restoring both; an older image cannot read v3. This source consolidation does not deploy REG1 or alter the accepted FIN1 stand receipt.
