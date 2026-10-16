# Durable stack, group feedback, wave 1 — implementation plan

> Source of truth: `docs/superpowers/specs/2026-10-15-durable-stack/feedback.md` section 4 (W1-0..W1-5), main spec `00-orchestration.md` (R12, R20, C33, C37). Wave 2 (Postgres, RabbitMQ, retention) is out of scope.

**Goal:** six independent fixes in `umir-feedback` with regression tests that fail before the code change.

**Branch / worktree:** `fix/durable-w1-feedback`, `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-feedback`. Base `origin/main` fa59b63.

**Environment for every Go command:** `GOWORK=off GOTOOLCHAIN=go1.26.9`; tools via `make tools` (done). Final gates: `make check`, `make quality-test`. `make integration` is not declared in the repo, so it is not required in wave 1 (C37).

**Rules for every task:** test first and observe it fail for the right reason; minimal code; `gofmt` via `make fmt FILES=...`; `git diff --check`; tests use only synthetic recipients and fake providers, no real mail or Telegram; no PII (address, name, phone, message, token, IP) in logs; no metrics (C33); one Conventional Commit in English per task, no Co-Authored-By or generated-by footer; CHANGELOG "Unreleased" entry per task (or one grouped entry in W1-5); do not touch `.superpowers`.

## Tasks

### W1-0 Preparation (done by lead)
Cherry-pick of `domain/validate.go` from 277f0e6 (journal now calls `domain.*`); `application.ErrLeaseLost`; `Worker.ProcessOne` treats it like `ErrNotFound`. Test `TestWorkerKeepsRunningWhenLeaseWasTakenOver` was red before the fix. Commit b930f5d.

### W1-1 JWKS client without a lock on the network call
Files: `internal/adapters/authkeys/client.go`, `client_test.go`, `internal/adapters/runtime/config.go` (+ test) for `FEEDBACK_AUTH_KEYS_MAX_STALE`, `bootstrap` wiring, README config table.
Failing tests (a)-(e) from the subspec:
- (a) ten parallel `Key` calls with a blocked HTTP client produce exactly one network call and each caller waits no longer than its context;
- (b) while a refresh hangs, a request for a cached known `kid` returns immediately;
- (c) Auth failing within the stale window: known key still served, unknown `kid` gives `ErrAuthUnavailable`;
- (d) after the window (default 5 min, `FEEDBACK_AUTH_KEYS_MAX_STALE`, range 30 s - 10 min) the known key is rejected too;
- (e) an unknown `kid` triggers a refetch at most once per second, shared by all callers.
Implementation: snapshot under `sync.RWMutex`, refresh deduplicated with a single-flight (own implementation on a channel; `golang.org/x/sync` is not in `go.mod` of this repository and adding a dependency is not worth it for wave 1), background refresh after 30 s, blocking only beyond the stale window. Comment the invariant: token lives 60 s, Auth keeps the previous key in JWKS at least 60 s after the last issue, so a 5-minute window does not extend token validity. Signature, `aud`, `azp`, `exp` checks unchanged. `go test -race` clean. Existing `RetryNow` semantics keep working or are replaced with equivalent tests.

### W1-2 VPN subscription refresh retry with backoff
Files: `internal/adapters/vpn/runtime.go` (+ new scheduler type, test), README refresh section.
Failing test: a retry scheduler with a controlled clock and jitter source (extract a testable type): after a failed start without fresh cache, retries at 30 s, 1, 2, 4, 8, 15 min (cap) with jitter +-20 percent; with a fresh cache and a working tunnel at 5, 10, 15 min; success resets the series and returns to the ordinary `refresh` interval; no retry after context cancel; no branch opens a direct route (`import_test.go` stays green). Log (JSON to stderr, no subscription URL, no response body): `vpn.refresh.failed attempt=N next_in=...`, `vpn.refresh.ok`. `telegram-vpn status` still shows cache freshness.

### W1-3 Reply-To only for an ASCII address
Files: `internal/adapters/delivery/smtp.go` (:254, :288), `delivery_test.go`.
Failing test: for `иван@пример.рф` the built message has no `Reply-To`, all header bytes are ASCII, the address is present in the decoded body; for an ASCII address `Reply-To` as before; the test SMTP server never sees `SMTPUTF8` in `MAIL FROM`. `domain.ValidEmail` unchanged.

### W1-4 Delivery visibility in logs
Files: `internal/application` (port `DeliveryObserver`, `Worker.ProcessOne`), `bootstrap` (slog adapter), `internal/adapters/httpapi/scoped.go` (log 503 `FeedbackStorageUnavailable` at :226, :278, :293, :298 as the inherited handler does).
Port: outcome (sent, retry, failed, unknown), channel, result code, attempt number, `jobID`, `submissionID`; called from `ProcessOne` after the result is stored and on store error (outcome unknown). Failing tests: captured `slog` shows one event per transition; the output contains none of address, name, phone, message, token, IP (checked against all fields of a test submission). No metrics. Worker stop policy unchanged (wave 2).

### W1-5 Docs and clock contract
README: fix "legacy configuration continues to serve /feedback" (with `FEEDBACK_AUTH_URL` the inherited route answers 503); record the single-VM requirement and zero `iat` tolerance (R12). Test in the auth tests: `iat == now+1s` is rejected, `iat == now` is accepted. CHANGELOG "Unreleased" entries for all of wave 1.

## Self-check against the plan checklist
- Spec coverage: W1-0..W1-5 each map to one task; subspec "not in wave 1" list is not touched.
- No placeholders: acceptance for every task is a named failing test.
- Consistency: no new dependencies, no Postgres or RabbitMQ code, `_wt/prod` is read-only.

## Final
Opus review of the whole branch, one fix round; push, draft PR to `main`, one CI look; report `wave-1-report.md`.
