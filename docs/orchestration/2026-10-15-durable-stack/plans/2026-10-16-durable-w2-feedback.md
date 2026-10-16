# Feedback durable wave 2 (storage) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. Steps use checkbox syntax. This plan is executed by one agent (no subagents).

**Goal:** Replace the JSONL file journal of umir-feedback by PostgreSQL (state of truth) plus RabbitMQ (delivery alarm, lifecycle hints), with retention, quotas, scope erasure, readiness by capability and `_FILE` secrets.

**Architecture:** Approach A: storage behind existing ports. Reference adapters `postgres`, `rabbitmq`, `relay` are copied by `umir-factory/reference/durable/copy.sh` (never edited). Own code: `internal/adapters/pgstore` (Store on top of copied `postgres`), `internal/adapters/memory` (test-only Store, same contract), `internal/adapters/amqpbridge` (relay glue + consumers), application services (lifecycle, retention, reconcile, registry), `bootstrap` supervisor. Delivery job row in Postgres is the truth; the queue message is an alarm; a reconcile loop re-wakes lost jobs.

**Tech Stack:** Go 1.26.9, pgx v5.11.0, amqp091-go v1.15.0, PostgreSQL (RLS, roles owner/runtime), RabbitMQ quorum queues.

**Spec:** `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/feedback.md` section 5 (and 3, 6, 7, 8); main spec `00-orchestration.md` sections 6, 10 (C1-C63), 11 (G1-1..8).

## Global Constraints

- Every Go command: `GOWORK=off GOTOOLCHAIN=go1.26.9`; checks `make check`, `make quality-test`, `make integration`.
- Reference copies are not edited (R15); defects/needs go to a CR in `.superpowers/sdd/2026-10-15-durable/cr/`.
- No `memory`/file fallback in production binaries (R20, C10); memory adapter only in tests/fixtures; `cmd` and `bootstrap` must not import it (arch-lint).
- Roles `umir_feedback_owner` (migrate, generation rotate only) / `umir_feedback` (runtime); `AssertRuntimeRole` at start; RLS enabled and forced on `feedback_submissions` and `feedback_jobs`.
- GUCs: `umir.actor` in `tenant|anonymous|system`, `umir.org_id`, `umir.restaurant_id` (C5). No own GUC.
- Retention (G1-2, C24): anonymous 30 d; restaurant 180 d or 7 d after `auth.*.deleted`; tombstone 180 d; text and phone cleared when both jobs `sent`; outbox published 7 d; inbox 14 d; quotas 150000 anonymous / 5000 per restaurant. Values come from env, required without defaults.
- Events: consume only `auth.restaurant.deleted`, `auth.organization.deleted` from queue `feedback.auth`; publish only to `umir.tasks` keys `feedback.smtp`, `feedback.telegram`; topology declared only by factory `mq-init`; Feedback checks passively (C57).
- Registry reconcile (C45, C50) with `FEEDBACK_AUTH_DISCOVERY_KEY` via `X-Umir-Service-Key`, interval `FEEDBACK_REGISTRY_RECONCILE_INTERVAL` (default 10 m).
- `/healthz` process; `/readyz` `{instance, generation, capabilities}`; intake mandatory for 200. No metrics endpoint (C33). No PII (address, name, phone, text, token, IP) in logs, broker bodies.
- Secrets: six Feedback secrets (SMTP username, Telegram bot token, proxy username and password, Gateway key, Auth discovery key) accept `_FILE` (C63); both forms at once is an error.
- Conventional Commits in English, no Co-Authored-By/generated-by footers, CHANGELOG "Unreleased" entry, `git diff --check`. No push, no PR.

## Review Focus

- Lost commit reply: repeat of same `submissionID` returns the original receipt, one submission, two jobs (tests in T3, T5).
- Poison/lost alarm message: job is not lost; reconcile wakes it again (T4).
- Stale worker `Finish` after takeover or after admin `Retry` is rejected by fence (T2, T3).
- Pooled connection never keeps RLS context; runtime role cannot DDL or write `storage_meta` (T3).
- Submission into a deleted scope is refused; pending jobs of that scope fail with `scope_deleted` (T3, T4).
- Missing queue makes `delivery.*` `down` but never takes down intake (T5).

---

### Task 1: Reference adapters, dependencies, integration tooling

**Files:** Create `internal/adapters/{postgres,rabbitmq,relay}/*` (copy), `tests/testsupport/itest/*.go` (temporary DB, role, vhost helpers derived from `reference/durable/itest`); Modify `go.mod`, `go.sum`, `.go-arch-lint.yml`, `Makefile` (`integration` target: `go test -tags integration -p 1 -race ./...`), `CHANGELOG.md`.

- [ ] Run `reference/durable/copy.sh postgres rabbitmq relay <worktree>` from factory-w0; check `copy.sh --check` is clean.
- [ ] `go get github.com/jackc/pgx/v5@v5.11.0 github.com/rabbitmq/amqp091-go@v1.15.0` (module cache), `go mod tidy`.
- [ ] Declare vendors pgx and amqp in `.go-arch-lint.yml` (adapters, tests); add component for `internal/adapters/**` keeps adapters allowed; add rule forbidding `bootstrap`/`process` from importing `adapters/memory` by a separate component `memory`.
- [ ] Write itest helper: `NewDatabase`, `NewVHost`, role creation with random suffix (from reference), env check that fails (no skips).
- [ ] A smoke integration test (`//go:build integration`) that opens a DB and a vhost; run `make integration`.
- [ ] `make check`; commit `build(feedback): copy durable reference adapters and add integration target`.

### Task 2: Domain, ports, memory store and store contract

**Files:** Modify `internal/domain/{types,validate}.go` (`Job.Fence int64`, `Submission.Erased bool`, drop `Legacy`), `internal/application/{ports,intake,worker}.go` (`Fingerprinter`, `ErrQuotaExceeded`, `ErrScopeDeleted`, `ListPosition`, `ListedSubmission`, scoped lister, `Retry`), Create `internal/adapters/memory/store.go`, `tests/storecontract/contract.go` (from the prod draft, extended), `tests/storecontract/memory_test.go`.

**Interfaces:** `application.Fingerprinter{ Sum(binding string, content []byte) string }`; `application.Store` = `Repository + ScopedRepository + Lister + Admin`; `Lister.ListScoped(ctx, org, tenant string, after ListPosition, limit int) ([]ListedSubmission, error)`; `Admin{ Status(ctx, after StatusPosition, limit) ([]DeliveryView, error); Retry(ctx, ns, id, ch, now) error; EraseEmail(ctx, email, now) (int, error) }`; `Job.Fence` set by `Claim`, checked by `Finish` -> `ErrLeaseLost`.

- [ ] Failing contract cases (memory): fence rejects stale `Finish` (also after `Retry`); tombstone replay returns original receipt, different content `ErrIdentityConflict`; quota per namespace (over limit leaves no rows/jobs); erase leaves other scope untouched; text and phone cleared after two `sent`; keyset list stable under inserts; accept into deleted scope refused; pending job of deleted scope not live.
- [ ] Implement domain/ports/memory store until green; update Intake for HMAC `Fingerprinter` (remove `legacyFingerprint`).
- [ ] `make check`; commit `feat(feedback): add storage contract, fenced jobs and memory test store`.

### Task 3: PostgreSQL schema and store

**Files:** Create `internal/adapters/pgstore/{store,accept,claim,finish,admin,lifecycle,retention,wake,registry,health}.go`, `internal/adapters/pgstore/migrations/0001_init.sql`, `internal/adapters/pgstore/*_integration_test.go`, `tests/storecontract/pg_integration_test.go`.

**Interfaces:** `pgstore.Open(ctx, Config{DB *postgres.DB, Clock, Policy, Lease, Retention, Quota, Fingerprint...}) (*Store, error)`; `pgstore.Spec postgres.Spec`; `Store` implements `application.Store`, `Lifecycle.ApplyScopeDeleted`, `Retention.Sweep`, `Reconciler.WakeDue`, registry state, `Capabilities`.

- [ ] Schema per spec 5.1 (tables, checks, RLS by hand-written policies using `postgres.ScopeActorSQL` etc., grants by migration to the runtime role, sequence `feedback_job_fence_seq`, `feedback_registry_state`, outbox/inbox/worker_leases/storage_meta via `-- umir:include`).
- [ ] Failing integration tests first: migrations (clean, repeat, two migrators, newer schema), runtime role cannot DDL / write `storage_meta`, RLS isolation with real role and pooled connection (MaxConns=1), composite FK, contract suite on Postgres, atomic accept with outbox (injected outbox failure), two stores on one DB claim each job once, stale fence, quota.
- [ ] Implement; run `make integration`; commit `feat(feedback): add PostgreSQL store with RLS, quotas and outbox`.

### Task 4: Messaging, lifecycle, retention, reconcile, registry

**Files:** Create `internal/application/{lifecycle,retention,reconcile,registry}.go`, `internal/adapters/amqpbridge/{relay,channel_consumer,lifecycle_consumer,topology}.go`, `internal/adapters/registryhttp/client.go`, tests (unit with fakes + integration).

**Interfaces:** task message JSON per `task-message.schema.json`; `amqpbridge.NewRelay(store *pgstore.Store, pub *rabbitmq.Publisher, owner string) *relay.Relay`; `amqpbridge.ConsumeChannel(ctx, conn, ch, worker)`; `amqpbridge.ConsumeLifecycle(ctx, conn, handler)`; `registryhttp.Client.Changes(ctx, since int64, after string) (RegistryPage, error)`.

- [ ] Failing tests: relay publishes and marks only on confirm, mandatory return keeps the row; nack path to `.retry` then `.dlq`; missing queue (passive check) -> `down`; lost alarm and DLQ'd alarm re-woken by `WakeDue`; duplicate `auth.*.deleted` applied once; stale revision ignored; registry tombstone erases scope when event lost; retention with fake clock (30/180/7, tombstone 180); no PII in DLQ bodies.
- [ ] Implement; leases `feedback.outbox|reconcile|retention|registry` through `postgres.Lease` with fence on writes; commit `feat(feedback): add outbox relay, queue consumers, retention and registry reconcile`.

### Task 5: Runtime wiring, config, HTTP, CLI; remove journal

**Files:** Modify `bootstrap/*`, `cmd/server/main.go`, `cmd/feedback/main.go`, `internal/adapters/runtime/config.go`, `internal/adapters/httpapi/{scoped,health,auth}.go`; Create `bootstrap/supervise.go`; Delete `internal/adapters/journal/*`, `httpapi/feedback.go`, legacy tests; Modify `tests/fixtures/*`, `tests/integration/feedback_test.go` (memory store, polling owner moved into fixtures).

- [ ] Failing tests: config (all variables, `_FILE` for the six secrets, both forms is an error, no defaults for retention/quota), `server serve` without DSN does not start, `/readyz` body and codes (intake down 503, delivery down 200, no subpaths), keyset cursor (scope/op/exp, works across stores), 403 for deleted scope, 503 for quota, supervisor survives worker error, CLI `migrate`, `generation rotate --reason`, `status` paginated, `retry --user-id --org-id --restaurant-id`.
- [ ] Implement; port fixtures to `memory.Store` (`Restart()` replaces file reopen); `make check`, `make quality-test`, `make integration`; commit(s) `feat(feedback)!: run on PostgreSQL and RabbitMQ, remove file journal`.

### Task 6: Failure matrix, docs, final verification

**Files:** `tests/integration/matrix_integration_test.go`, `README.md`, `CHANGELOG.md`, report and PR body in the group folder.

- [ ] Failure-matrix tests (crash before/after commit, broker/db restart, disk-full 53100, two replicas, expired lease, poison messages).
- [ ] README (config, CLI, readiness, retention, acceptance SQL, one-VM clock), CHANGELOG `!` with clean-slate migration.
- [ ] Self-check of `git diff fix/durable-w1-feedback...HEAD` against spec section 5/8; `-count=3` for flaky; commit docs.

## Self-review

Spec coverage: 5.1 -> T3; 5.2 -> T2/T3; 5.3 items 1-8 -> T3/T4; 5.4 none; 5.5 config, leases, sweep -> T4/T5; 5.6 -> T5; 5.7 -> T5; section 7 tests -> T2-T6; C63 -> T5; C45/C50 -> T4. Types consistent across tasks as listed in Interfaces.
