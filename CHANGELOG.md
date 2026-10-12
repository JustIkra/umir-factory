# Изменения

## Не выпущено

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

