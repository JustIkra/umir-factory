# Изменения

## Не выпущено

- Add a separate FIN1 candidate Compose with real Auth, exact-source builds, private TLS, persistent Auth authority and optional Feedback. Private input preparation is idempotent and refuses incomplete or changed bundles. Selected production pins are unchanged.

- Define the FIN1 supply and recommendation contract supplement, strict composed schemas, examples, 26 acceptance predicates and explicit transition from the predecessor recommendation policy. Preserve C2/UA1 files and evidence. Add test-only JSON Schema validation and deterministic composition checks; runtime integration remains pending.

- Published the sole user-selected completion plan, revision 2, and its HTML view. Marked the previous backend execution backlog as superseded. This documentation change does not qualify services, select images or deploy the assembly.

- Add a separate typed UA1 invitation-link SMTP receiver library with exact
  reviewed Auth discovery/MIME/Message-ID joins, frozen capture provenance,
  owner-window cutoff and real one-shot permanent/lost-reply effects. Preserve
  ordinary sinks and RPC 1.1.0. Assembly integration and runtime qualification
  remain pending.

- Update the current backend plan after UA1 implementation and main integration. Distinguish source checks, partial real-service observations, mandatory qualification, original F3/F4/J1/E/J2 and the next v4 phase. Mark the historical v1 handoff explicitly.

- Add the approved UA1 contract profile for batch reads, organization contacts and invitation lists, and durable invitation-link delivery. Include separate public/private overlays, composed profiles and 34 mandatory acceptance rows. Owner source implementation passes gates; complete runtime qualification remains pending. Preserve frozen C2 schemas, historical all67 evidence and selected assembly identities.

- Record completion of the 29-operation Auth facade HTTP matrix, real password
  cooldown checks, Serving settings refresh and the Warehouse private-listener
  correction. Preserve API amendments, browser acceptance and full J1/E/J2 as
  remaining work. No selected assembly or release changes.

- Document current backend checkpoint work, local Gateway/Serving runtime fixes, live readiness and HTTP limits, remaining Auth façade and UI contract amendment tasks, and separate frontend/Figma ownership.

- Corrected unpublished backend fixture provisioning: Serving receives four distinct service credentials and a separate control reference. Auth and Serving share the independent `serving-auth` discovery credential; owner/sink service maps exclude controls. Version 2 requires bindings digest `de9c7bfc573ad0887be0f00b1ddc2495939469f58f3524c6dbb66a521fed385e` and refuses its predecessor. Protocol 1.1.0, selected pins and business contracts remain unchanged. Real providers and runtime acceptance remain pending.

- Согласованы OpenAPI, README и состояния клиента с проверкой длины `currentPassword` до credential verification: без активной паузы и при допустимом новом пароле значение длиннее 32 Unicode code points получает `422 PasswordRejected` и `fieldErrors.currentPassword: TooLong`, не увеличивает счётчик неверных проверок и не отзывает сессию. Уточнены точные JSON-ключи, запрет дублей и `null` в запросах кабинета. При интеграции сохранены актуальные статусы HTTP/Resolve, frontend и будущего auth из main.

- OpenAPI draft.4 declares `Cache-Control: no-store` on every operator success response, requires `sessionRevoked` exactly on `PasswordChangeRateLimited`, and bounds `displayName` to 80 code points, as the gateway already does (gateway#8).
- `compose.auth.yaml` is a loopback development and acceptance assembly: frontend binds `127.0.0.1` whatever `UMIR_BIND` says, and gateway always runs in `loopback-development` cookie mode, so its plain-HTTP edge never leaves the host (gateway#8).

- Добавлены явная candidate-приёмка `--realtime` и R01–R07: SSE, подтверждённые изменения, отказ без change, restart epoch, background deadline, сроковая просрочка, logout и управляемая недоступность склада. Тесты потоков не подтверждают ещё отсутствующий runtime gateway/Nginx.
- Добавлена редакция public gateway OpenAPI `1.1.0-draft.1` с candidate-only SSE `GET /api/live`, пятью строгими event schemas, ограничениями потоков и необязательным `X-Umir-Activity: background`. Добавлен private-network контракт warehouse `POST /expiration-runs`; gateway и browser route не объявлены. Handoff, frontend stale-state brief, Go architecture и README отделяют service behavior от pending gateway/Nginx candidate и сохранённых selected pins.
- Уточнено владение просрочкой: warehouse сохраняет обработку due-expiration при свежих складских операциях и через новый сервисный запрос; автономное warehouse-расписание удалено. Gateway владеет будущим scheduler, реализация которого остаётся внешней зависимостью. Snapshot retry остаётся независимым с `SNAPSHOT_RETRY_INTERVAL` по умолчанию `1s`. Зафиксировано, что отдельный auth service и его письма через общий SMTP binding — pending migration по gateway #18.

- Зафиксирована локальная политика runtime-секретов: `.env.secrets/` с закрытыми правами, исключение файлов из Docker build context и ограничения Compose file-backed secrets. Уточнено, что feedback отправляет уведомления формы, а будущий приватный auth владеет аккаунтом/сессиями/OTP и отправляет собственную почту через общую SMTP-привязку. OTP PR #3 уже в feedback main (merge `e98747a1`) и выключен по умолчанию; его код остаётся кандидатом переноса/повторного использования по Gateway #18. Опубликованный feedback SMTP fix `6c59bbf0` исправляет выбор PLAIN/LOGIN; доставка письма не подтверждена.

- Приёмка frontend/gateway использует межмодульные тесты warehouse/serving и HTTP-проверки реальных сервисов вместо отдельного этапа сценарных бизнес-моков. Обновлены handoff и критерии интеграционного ревью; gateway #1 выполнен, factory #2 остаётся открыт для frontend/Figma и браузерной приёмки.

- Обновлён статус документации: HTTP-адаптеры и Resolve в рамках работающих процессов реализованы; постоянное бизнес-состояние и восстановление после перезапуска остаются следующими задачами. Уточнены Figma/frontend, пять слоёв клиента и граница кабинета оператора. README содержит оставшиеся задачи с зависимостями и ссылки на выполненные gateway #8 и feedback #1.

- Completed the six operator operations' OpenAPI 502/504 responses: read failures are unavailable, and lost modifying responses keep an unknown account or session outcome.

- Added the private feedback/VPN candidate overlay and optional exact feedback source revision to authenticated HTTP acceptance. Synthetic F01–F05 cover the public form while SMTP/VPN are unavailable; selected pins remain unchanged.
- Completed feedback draft.4 receipt/error schemas and required rate-limit Retry-After; CI accepts an optional feedback_ref through quoted environment arguments.

- **BREAKING:** OpenAPI `1.0.0-draft.4` describes the single-operator account routes of gateway#8 under `/api/operator` and the public `POST /api/feedback` boundary of gateway#10. In the auth assembly every business operation requires the operator session cookie, and unsafe ones also the exact `Origin` and `X-CSRF-Token`; 401, 403 and 503 `AuthUnavailable` come before dispatch. Selected images keep the draft.2 behaviour until the coordinated pin update; draft.3 was never published.
- Added the one-port operator auth candidate: `compose.auth.yaml` with the `x-umir-auth-acceptance: candidate` marker publishes only frontend and keeps the account on the `operator-data` volume. `umir-checks http-acceptance --candidate --auth` creates a disposable operator per run (hash from the candidate gateway image, password via stdin) and verifies that only frontend publishes a loopback port. The harness signs in through frontend and adds scenarios A01–A04; the manual CI gate gains the `http_auth` input.
- Distinguished the organizer's recommended technologies from the implemented Go/React/Vite stack and the approved TypeScript/SCSS/Chakra frontend additions and test tooling.
- Added the project documentation ownership and update rules, with README, AGENTS, OpenAPI, handoff, design brief and changelog responsibilities.
- Selected HTTP CI activation no longer depends on an optional search tool. Missing, invalid or duplicate stage markers fail instead of silently skipping acceptance.
- Business acceptance initializes its owned temporary workspace with `go work init`, so selected module requirements do not depend on a stale hardcoded Go patch version.
- HTTP fixtures derive dates from the warehouse clock and preserve delivery request/clock diagnostics on failure without retrying the POST.
- Updated pinned GitHub Actions to Node.js 24-compatible releases.
- Updated all factory Go module, CI and HTTP fixture build requirements to Go 1.26.9. Reconciled the three-module inventory, implemented HTTP status and developer documentation without private working-memory links.
- Updated public OpenAPI to `1.0.0-draft.2` and the internal warehouse contract to `1.0.1-draft.1` against the implemented HTTP sources. Corrected confirmation status/results, refusal replay, read validation responses, unknown write examples and source provenance. Shared bounded scalar schemas and corrected internal command names now match the wire adapters.
- Selected published HTTP warehouse `3e37af2f`, serving `0fb867e7`, gateway `c7a5c02e` and frontend `e2fd55f8` images by full commit SHA. Enabled selected HTTP acceptance with the same reviewed image-pin update.
- Added a shared isolated assembly verifier for CI and local checks, with ephemeral host ports, image provenance/metadata checks, smoke and cleanup after partial startup.
- The cross-service business suite now runs automatically on pushes, pull requests and manual CI runs against the two selected service commits.
- Added the draft gateway OpenAPI v1 contract with typed business operations, bounded decimal strings, Moscow timestamps and JSON examples. Documented the separate production resolve requirements.
- Проверки check-images и smoke перенесены на автономный Go CLI; заменённые Python-скрипты удалены.
- Добавлены реальный bridge и совместные бизнес-тесты двух Runtime, локальный runner и проверка фиксированных commit через временный workspace.
- Добавлены закреплённые gates для обоих вложенных Go-модулей и business job для реализованных выбранных SHA.
- Bridge явно переводит DishDetails, сохраняет EvaluatedAt/StateVersion и копирует вложенный Dish.
- Уточнены поля формы поставки и полные даты/время; загрузка и распознавание документов не входят в текущую версию.
- Added Go development, architecture and Git/release rules with pinned quality tooling.

- Локальные сборки используют отдельные dev-теги и больше не перезаписывают SHA-теги GHCR.
- CI проверяет существование коммитов и успешные задания проверки и публикации сервиса для выбранного SHA.
- После pull проверяются OCI source/revision и наличие HEALTHCHECK во всех четырёх образах.
- Smoke использует опубликованные адреса и порты Compose; добавлены выбор env-файла и явные URL.
- README описывает наследуемые HEALTHCHECK и точные границы проверок образов.
- Business HTTP now runs through gateway → serving → warehouse with process-local state. A missing serving proposal after restart keeps outcome=unknown; it does not claim a terminal refusal.
- Added the private 11-route warehouse internal HTTP contract and updated Confirm/Resolve retries to replay saved outcomes. After a definite refusal, a new confirmation requires a new commandID.
- Added the standalone `tests/http` module, `make check-http`, deterministic loopback fault barriers and exact candidate/selected assembly acceptance. Candidate builds do not require publication; selected acceptance verifies published images.
- Added manual CI candidate SHA inputs and automatic selected HTTP acceptance for the published HTTP image pins.
- Documented public HTTP behavior, process restart limits, internal port `8082` and scenario evidence.

## [0.1.0] - 2026-10-10

- Добавлены сборка четырёх сервисов по SHA образов и локальная сборка.
- CI проверяет обе конфигурации Compose и формат SHA-тегов, скачивает закреплённые образы, запускает четыре контейнера через `up --wait` и проверяет frontend и оба маршрута gateway smoke-тестом.

UA1 implementation checkpoint, 2026-10-14: owner candidates pass full source gates. The generated composed profiles and assertion transition map retain the original C2 inputs. Initial Nginx/real-owner/synthetic SMTP observations are partial; mandatory UA1 qualification and original F3/F4/J1/E/J2 remain open. The test executor avoids collisions between dynamic public ports and synthetic private port mappings.
