# Изменения

## Не выпущено

- Add the infrastructure-only W2 durable Compose base: PostgreSQL 17 with TLS and separate owner/runtime roles, RabbitMQ 4 with TLS and `mq-init` topology, and separate Redis limiter/cache services with TLS and ACLs. Add secure file-secret preparation, the continuity-marker init job, pinned registry identities, and isolated infrastructure checks. Owner services, stack control, encrypted backup/restore and W3 acceptance remain incomplete.

- Wait for the second ownership qualification before measuring concurrent public HTTP probes. Preserve the existing 250 ms overlap window and two-second request guard; the test no longer mistakes qualification work for serialized responses.

- Check the types of the G1 policy status fields in contract tests, so the full pinned lint gate accepts them. Correct the README status to the approved G1 decisions.

- Add `umir-checks completion-acceptance` and a CI job `completion-stand` that runs the deployed profile `compose.completion.yaml` on a disposable project `umir-completion-ci-<random>`: prepare, `up --wait`, demo seed, a transport `/healthz` check, a leaf-certificate rotation without removing any volume (the volumes `auth-state` and `auth-config` keep their identity and the earlier session still works), a reconfiguration that turns on registration (the registration scenario then passes), a rollback, an encrypted backup, removal of the project and a restore with sign-in and read, and writes a JSON evidence file without secrets. A second matrix entry also starts Feedback, built from the checkout at the reviewed pin. Cleanup removes volumes only for the `umir-completion-ci-` prefix, also after a failure at any step. The `check` job now verifies the image lock, requires a digest on every completion image, validates the completion Compose, and runs all completion Node tests and the backup test.

- Fix the Feedback state volume of `compose.completion.yaml`: Feedback refuses a state directory that is not mode 0700, so with the profile `feedback` it stopped with `RuntimeUnavailable`; `private-init` now sets owner and mode of the volume.

- Add encrypted backup and restore of the completion Auth state. `scripts/completion/backup.sh` stops only Auth for the time of the copy (it starts it again if it was running), archives the volume `<project>_auth-state` (auth.json and owner-secrets together) and, separately, the private bundle directory without `.generations`, encrypts each with `openssl enc -aes-256-cbc -pbkdf2` using a key file kept outside the archives, and writes `manifest.json` (project, UTC time, Auth state at the copy, SHA-256 of every plain and encrypted archive) and `receipt.json`. `scripts/completion/restore.sh` restores into an empty project only, checks the manifest and both SHA-256 values before and after decryption, and writes nothing on a wrong key, a damaged archive, a foreign project or a non-empty project. `prepare.mjs --update-images` now requires `--backup-receipt <receipt.json>` of the same project, not older than 24 hours, whose manifest SHA-256 matches the manifest beside it. `tests/infra/completion-backup.test.sh` covers the drill with a real Docker volume.

- Harden the long-lived services of `compose.completion.yaml` and add a transport health check. Every service now has a read-only root file system (writable `tmpfs` for `/tmp` and the Nginx cache), `cap_drop: [ALL]`, `no-new-privileges`, an explicit non-root user, `mem_limit` (auth 256m, gateway 256m, warehouse 512m, serving 256m, frontend 64m, transport 64m, feedback 128m), `pids_limit` and `json-file` logs limited to 3 files of 10 MB; the one-shot `private-init` has no network and only the CHOWN, FOWNER and DAC_OVERRIDE capabilities. The new `transport-ready` job runs `scripts/completion/transport-health.mjs` (a TLS handshake verified against the bundle CA, with the name `transport` and the certificate end date, on ports 8442-8445); Auth waits for it. The new internal `front` network (`UMIR_FRONT_SUBNET`, default `172.29.16.0/24`) holds only Gateway and Frontend: Frontend no longer shares a network with the private Warehouse, Serving and Auth listeners. The measured budgets are in the README.

- Deliver the Feedback service keys to `compose.completion.yaml` as files instead of environment variables. `prepare.mjs` writes `keys/gateway-feedback` and `keys/feedback-auth` (mode 0600, same values as before) into a new `keys` target that `private-init` copies into the `keys-config` volume; Feedback reads `FEEDBACK_GATEWAY_KEY_FILE` and `FEEDBACK_AUTH_DISCOVERY_KEY_FILE` from `/run/umir-keys` (read-only), so `docker inspect` and `docker compose config` no longer show them. A bundle prepared before this keeps working while Feedback is off; its first rotation, `--reconfigure` or `--rollback` moves the keys out of `env/feedback.env` into the `keys` target with the same values, and then Feedback can be started. Warehouse, Serving and Gateway still read their keys from `env_file` until their images support `*_FILE`; the list is explicit in `tests/infra/completion/compose.test.mjs`. New `scripts/completion/compose.sh --bundle <dir> ...` runs Compose with the bundle environment and accepts `config` only with `--quiet`.

- Pin every image of `compose.completion.yaml` and of the test mail overlay by digest. `scripts/completion/images.lock.json` records for each service the source commit, index, `linux/amd64` platform and config digests and the OCI `revision` label, and for the auxiliary images (`alpine`, `nginx-unprivileged`, `node`, `mailpit`) a `name:tag@sha256:<index digest>` reference that also works on arm64. `prepare.mjs` writes `COMPLETION_<SERVICE>_IMAGE=ghcr.io/justikra/umir-<service>:sha-<commit>@<platform digest>` from the lock and refuses a service the lock does not know; the lock is part of the bundle's source binding, so a stand prepared before needs a reviewed `--update-images`. New `umir-checks lock-images --write|--verify` resolves the pins in the registry and checks the label, the platform digest in the index and the config digest; `umir-checks check-images --lock` requires a digest on every image of the profile and agrees with `docs/completion-stand.json`. Feedback is not published to the registry: the lock records it as `unpublished` and its image stays an exact-source tag.

- Make `private-init` of `compose.completion.yaml` apply bundle generations. A newer generation is copied into a staging directory inside the target volume, the previous files are kept in `.previous` for a rollback, the files are moved in and the `.ready` marker (generation and tree digest) is written last; the same generation with the same digest changes nothing, an older generation or another digest under the same number is refused. A first copy that was interrupted, also over foreign files, is completed instead of blocking the volume, and an interrupted generation change is finished without mixing two generations. Apply a new generation with `prepare.mjs` (a rotation or `--reconfigure`), `docker compose up -d --force-recreate private-init`, then `docker compose restart transport auth`; roll back with `--rollback` and the same steps. The script is tested as written in the Compose file, on the host and in the pinned Alpine image.

- Add `scripts/completion/check-expiry.mjs`. It reads the private CA and server certificate of a completion bundle, optionally also the certificate a running endpoint serves (`--live host:port`), prints their end dates and the days left without any secret, and exits with 3 when one ends within 30 days (`--warn-days` changes the limit, `--now` fixes the clock for tests) and with 2 when the bundle cannot be read or its recorded dates disagree with the certificates. README describes the certificate lifetimes and the new `prepare.mjs` operations; `make check-contract` runs the new expiry test.

- Number the completion bundle in generations and add operations that change it without touching service keys, the first user or its password. `scripts/completion/prepare.mjs` gets `--rotate-leaf` (new server certificate under the same CA), `--rotate-ca --phase trust|serve|retire` (the new CA is trusted by clients before the server uses it and the old one is dropped last), `--reconfigure` (registration, SMTP files and their content, browser origin and cookie mode; a different initial user or any key material is refused) and `--rollback` (a new generation with the content of the previous one). Each target `.ready` now holds `completion-private-v2 generation=<N>` and `tree-sha256=<hex>` of the target files, `bundle.json` records the generation and the certificate end dates, the last five earlier generations are kept in `.generations/`, an interrupted write (`.next`) is deleted or completed by the next run, and a bundle made before generations existed is read as generation 0. Applying a generation to a running stand needs the `private-init` update that follows.

- Issue the completion private CA for 10 years and the server certificate for 1 year, both with random positive serial numbers (the CA was valid for 1 year and every certificate had serial 1, so a stand stopped a year after preparation). Existing bundles keep their certificates until they are rotated.

- Refresh stale statements in `README.md`, `docs/business-core.md` and `docs/current-plan.md`. README gets one map from each Compose profile to its canonical contract and consumers (`registration-v1-composed-*` for deployment), marks `compose.yaml` and `openapi.yml` as legacy/historical, states the single-VM or synchronized-clock requirement R12 with zero `iat` tolerance, adds `umir-auth` and `umir-feedback` to the local build and replaces the claims about no self-signup, the undeployed VM stand and five repositories. Business core now reflects Auth persistence, roles, the implemented interface and the approved C2 contract; the current plan records that FIN1 was delivered on 15.10. `tests/infra/docs.test.mjs` rejects the stale phrases and unresolved local Markdown links, and `make check-contract` runs it.

- Move the optional Feedback image pin of `compose.completion.yaml` into `scripts/completion/sources.json` (`optionalServices.feedback`, unchanged `136a88a`). Preparation writes `COMPLETION_FEEDBACK_SHA` and `COMPLETION_FEEDBACK_IMAGE` to the bundle, and a Feedback checkout under `--sources` is verified only when its directory exists. Compose reads `COMPLETION_FEEDBACK_IMAGE` instead of repeating the pin; a bundle prepared earlier keeps working with Feedback off and needs one `prepare.mjs --update-images` run before the `feedback` profile is enabled. The README also states that the two Feedback profiles use different storage files and volumes.

- **BREAKING:** `compose.yaml` publishes the legacy gateway port only on `127.0.0.1`; `UMIR_BIND` now applies to the frontend alone. The legacy business API has no sign-in, so `UMIR_BIND=0.0.0.0` used to expose it to the network. Migration: if the gateway port must stay reachable on `UMIR_BIND`, add `-f compose.legacy-public.yaml` after `compose.yaml` on an isolated network only. README now points to `compose.completion.yaml` as the safe way to run a stand.

- Add a reproducibility test for the composed UA1 contract profiles, matching the FIN1 and REG1 gates: it recomposes them in a temporary directory, compares the bytes with the committed files and checks that the inputs stay unchanged. `make check-contract` runs it.

- Set `WAREHOUSE_API_MODE=legacy` explicitly for Warehouse in `compose.yaml` and `compose.dev.yaml`, so the legacy profiles keep starting once Warehouse refuses an empty mode. The completion profile already sets `v2`. A Compose test now checks every profile that runs Warehouse.
- Add wave 0 of the durable-stack transition: DUR1 contract profile `2.0.0-DUR.1` (overlays, examples, composed profiles, reproducibility tests, amendment), ADR 0002, event catalog, configuration contract and proposed retention and admission tables in `docs/durable`, the `reference/durable` adapter module (PostgreSQL, RabbitMQ, Redis, outbox relay) with integration tests and its quality gates, `scripts/durable-test-infra.sh` and `umir-checks contract-sync`. The durable Compose profile and deployment are not part of this change.

- Deploy the selected REG1 sources and merged Frontend PR12/13 on the existing stand. Retain both users, memberships, credentials, owner keys, sessions and business processes through the schema3 upgrade; archive the FIN1 receipt. Enable registration by a separately backed-up policy-only change at the user’s request.

- Select reviewed REG1 Auth/Gateway and the merged map/friendly-copy Frontend for the authorized stand update. Retain Warehouse/Serving revisions, original accounts and private inputs. Document matched Auth migration backups and a targeted update that preserves business processes.

- Address REG1 review issue #16: command ID bound, distinct completion schemas, explicit 403/429/lockout/remember-me semantics and unique example IDs. Document default abuse limits, neutral resets, disabled code login and storage/rollback constraints; validate configurable preparation limits.

- Document the existing private runtime input directory and owner-specific handling; keep the root Docker context exclusion.

- Add the REG1 public email registration supplement, strict overlays, composed profiles, examples and reproducible contract gate. Pass an explicit boolean registration policy through completion preparation; keep it disabled by default. Add the Nginx/Gateway/Auth and STARTTLS SMTP acceptance client.

- Make the restart polling regression cancel on its second readiness attempt with the existing bounded scenario guard. Preserve ownership-drift and unsuccessful-restart assertions; a one-second setup race no longer prevents reaching the tested polling path.

- Publish the exact FIN1 service images after main CI and deploy the pinned profile to the existing HTTPS stand. Record digest identities, secure-cookie login, seed/repeat, FEFO/order recovery, explicit shortage and human writeoff. Verify real production invitation/OTP delivery, recipient code completion and fresh Waiter login/menu/recommendations/SSE with stock 403. Select the explicit FIN1 runtime profile. Preserve legacy profile evidence.

- Add a separate FIN1 candidate Compose with real Auth, exact-source builds, private TLS, persistent Auth authority and optional Feedback. Private input preparation is idempotent and refuses incomplete or changed bundles. Selected production pins are unchanged.

- Define the FIN1 supply and recommendation contract supplement, strict composed schemas, examples, 26 acceptance predicates and explicit transition from the predecessor recommendation policy. Preserve C2/UA1 files and evidence. Add test-only JSON Schema validation and deterministic composition checks; keep contract validation separate from the runtime observations in the stand receipt.

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
