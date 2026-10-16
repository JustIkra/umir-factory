# Auth, волна 1 «Независимые правки» — план реализации

> **Для исполнителей:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Шаги отмечены чекбоксами. Каждая задача: сначала падающий тест (доказать падение запуском), потом код.

**Цель:** закрыть пункты 4.1–4.9 подспеки Auth (K1, K2-часть, H1–H5, M1, M2, M4, M5, M7, L1–L3) без изменения контракта на проводе и без перехода на Postgres.

**Архитектура:** волна 1 остаётся на `jsonstore` и `Accounts.mu`. Тесты пишутся на уровне `application.Accounts` и HTTP-обработчика, чтобы без правок переехать в волну 2. Блок T2 волны 2 (разрез порта `Store`) в этой волне НЕ делается.

**Стек:** Go (`GOWORK=off GOTOOLCHAIN=go1.26.9`), `make check`, `make quality-test`, golangci-lint 2.14.0, go-arch-lint 1.19.0 (бинарники в `.cache/go-quality/bin` worktree).

**Спеки:**
- Подспека: `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/auth.md` (раздел 4 — источник требований, разделы 6–9).
- Главная: `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md` (разделы 3, 8, 10; C18, C29, C37).

Worktree: `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-auth`, ветка `fix/durable-w1-auth`, база `79188bf`.

## Глобальные ограничения (в каждой задаче)

- Каждая go-команда с `GOWORK=off GOTOOLCHAIN=go1.26.9`. Без этих переменных не запускать.
- `internal/application`: `depguard` strict — ни pgx, ни `crypto/hmac`; `time.Now` запрещён, время только через `Clock`. Слои `go-arch-lint` без исключений.
- Контракт на проводе не меняется: коды ошибок, поля ответов, `outcome` остаются прежними (случай «результат очищен» использует существующий `CommandConflict` с `outcome: unknown`, C29).
- Сроки хранения сессий и идемпотентности (R9) в волне 1 НЕ меняются; единственное исключение — очистка email-вызовов (4.1, C18).
- Коммиты: Conventional Commits на английском, без футеров `Co-Authored-By` и generated-by, `git diff --check` перед коммитом, запись в `CHANGELOG.md` «Unreleased» (в той же задаче). Ничего из `.superpowers` не коммитить.
- Секреты, коды, адреса, токены не печатать и не логировать; тестовые секреты синтетические.
- Исполнители и ревьюеры субагентов не запускают.
- `AGENTS.md` в репозитории нет — следовать README и этому плану.
- Тесты, которые в полном `make check` флакают (см. журнал: `TestRealHTTPOrganizationSettingsReplayAndMinimumSchedulerAuthority`), перепроверять отдельным запуском; новых флаков не вносить (никаких `time.Sleep` как синхронизации — каналы и инъектируемые часы).

## Review Focus (вход/сбой, который спека подразумевает, но тесты задач могут не покрыть)

1. Чистка вызовов (4.1) не должна оживлять израсходованный proof и не должна стирать счётчик неверных кодов за последний час — тест в Задаче 1.
2. Повтор команды вызова после чистки не должен исполняться как новая команда и не должен создавать вызов — тест в Задаче 1.
3. Короткий, но невалидный UTF-8 пароль и пароль >32 символов не двигают счётчик входа (4.4) — Задача 4.
4. Изменение ревизии во время вызова пира не должно давать устаревшее разрешение (старый `TestPeerObservationCannotGrantStaleAuthority` остаётся зелёным) — Задача 5.
5. Ошибка воркера не должна оставлять процесс «живым, но 503»; журнал не содержит кодов и адресов — Задача 6.

## Карта файлов

- Домен: `internal/domain/replacement.go`, `validation.go`, `aggregate.go`.
- Прикладной слой: `internal/application/{challenges,outbox,accounts,public_reads,login_failures,reconciliation,authorization,memberships,registry,system_tokens,deletion,enrollment_commands,invitations,registration}.go`.
- Процесс: `bootstrap/{serve,runtime,process}.go`, `cmd/server/main.go`.
- HTTP: `internal/adapters/httpapi/{preauth,router}.go`; почта: `internal/adapters/mail/smtp.go`.
- Тесты: `tests/integration/*`, `internal/application/*_test.go`, `internal/domain/*_test.go`, `bootstrap/*_test.go`.
- Документы: `README.md`, `CHANGELOG.md`.

Порядок задач последовательный (общие файлы `accounts.go`, `challenges.go`): 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9.

---

### Задача 1: K1 — очистка email-вызовов и надгробия команд (подспека 4.1, C18)

**Файлы:**
- Создать: `tests/integration/challenge_retention_test.go`.
- Изменить: `internal/domain/replacement.go` (`ValidateReplacementAt(prev, next ValidatedAggregate, now time.Time) error`; прежний `ValidateReplacement` остаётся обёрткой, если нужен прочим вызовам), `internal/domain/validation.go` (:255, правила для надгробия записи команды), `internal/application/outbox.go` (`SweepEnrollment`), `internal/application/challenges.go` (:223 лимит 4096 остаётся защитным, но чистка не даёт его достичь), `internal/application/command_lookup.go` (повтор надгробия -> `CommandConflict`/`unknown`), `README.md` (строка «At most 4096 challenges are retained» в разделе про OTP и конец раздела «Registration review limits and operations»), `CHANGELOG.md`.

**Интерфейсы:**
- Производит: `domain.ValidateReplacementAt`; запись команды-надгробие = `IdempotencyRecord` с тем же ключом и `Fingerprint`, пустыми `EncryptedResponse` и `JobIDs`.

Требования (подспека 4.1, дословно по смыслу):
- Сроки: вызов без proof удаляется через 1 ч после `max(expiresAt, последняя неверная попытка)`; с proof — через 24 ч после `proofExpiresAt`. Чистка выполняется в `SweepEnrollment` в том же коммите, что и прочее подметание.
- `ValidateReplacementAt` разрешает ровно три вещи вместе: убрать челлендж, убрать его OTP-задания, заменить записи команд `beginInvitationChallenge/beginEmailChallenge/resendChallenge`, у которых все `JobIDs` убраны, надгробием. Всё прочее изменение/удаление по-прежнему ошибка.
- Повтор команды с надгробием (тот же ключ и отпечаток) -> 409 `CommandConflict`, `outcome: unknown`, нового вызова нет. Через 180 дней надгробие не снимается в волне 1 (срок применяет волна 2).

- [ ] **Шаг 1: падающие тесты** в `challenge_retention_test.go` (на `application.Accounts` с управляемыми часами; взять каркас из `tests/integration/registration_review_test.go` и `enrollment_test.go`):
  1. `TestPasswordResetUnknownAddressesStayBounded`: `password_reset` на неизвестные адреса при двигающихся часах (60 за 10 мин) в течение 11,5 ч симулированного времени; после — не более 1000 строк `Challenges` и `BeginInvitationChallenge` проходит (не `ErrChallengeCapacity`).
  2. `TestVerifiedChallengeWithinProofWindowSurvives`: verified-вызов остаётся в окне proof (24 ч) и proof принимается.
  3. `TestRecentWrongCodesSurviveSweep`: неверные коды за последний час сохраняются, бюджет `wrongCodeLimit` не обнуляется.
  4. `TestConsumedProofNotAcceptedAfterSweep`: израсходованный proof не принимается после чистки.
  5. `TestQueuedSendingUnknownJobsAndInWindowReplayKept`: задания в `queued/sending/unknown` и повтор `beginEmailChallenge` внутри срока не теряются.
  6. `TestReplayAfterSweepIsCommandConflictUnknown`: повтор после чистки -> 409 `CommandConflict`, `outcome: unknown` (проверить и на уровне HTTP-обработчика), новый вызов не создан.
  7. Доменные тесты `ValidateReplacementAt`: разрешённое удаление проходит; удаление вызова с живым заданием, удаление без замены записи команды, замена записи с непустыми `JobIDs` — `ErrInvalidAggregate`.
- [ ] **Шаг 2:** `GOWORK=off GOTOOLCHAIN=go1.26.9 go test ./tests/integration -run 'Retention|Sweep|ReplayAfterSweep' -count=1` — убедиться, что тесты падают по правильной причине; сохранить вывод в отчёт.
- [ ] **Шаг 3:** реализовать правку, прогнать целевые тесты и `go test ./internal/... -count=1`.
- [ ] **Шаг 4:** поправить README (две указанные позиции), добавить запись в CHANGELOG.
- [ ] **Шаг 5:** `make check` (флак protocol-теста перепроверить отдельно), `git diff --check`, коммит `fix(auth): purge expired email challenges and tombstone their command records`.

---

### Задача 2: K2 — безкопийный доступ только для чтения (подспека 4.2)

**Файлы:**
- Создать: `internal/application/readonly_alloc_test.go` (или в `tests/integration/`, где проще собрать 100 тыс. записей), бенчмарк рядом.
- Изменить: `internal/domain/aggregate.go` (`ValidatedAggregate.Inspect(func(*Aggregate))`; `Revision()` уже есть), `internal/application/public_reads.go:15` (`Ready`), `internal/application/accounts.go:159`, `bootstrap/serve.go:64-92` (проверка «есть ли работа» без clone), `CHANGELOG.md`.

**Интерфейсы:** производит `func (v ValidatedAggregate) Inspect(fn func(*Aggregate))` — вызывающий не имеет права изменять данные и удерживать указатель после возврата (комментарий в коде).

- [ ] **Шаг 1: падающие тесты:** `testing.AllocsPerRun` — число аллокаций `Ready` и «пустого тика» воркера с 100 и с 100 000 сессий/записей идемпотентности одинаково (не растёт с объёмом; допуск константа); бенчмарк `BenchmarkReady100k`. Фикстуру наполнять напрямую через репозиторий/агрегат, не командами.
- [ ] **Шаг 2:** запустить, увидеть падение (аллокации растут).
- [ ] **Шаг 3:** реализовать `Inspect`; `Ready` и проверку «есть ли работа» перевести на него. Намеренно НЕ делать: продление idle реже и чистку сессий/идемпотентности (R9, волна 2).
- [ ] **Шаг 4:** тесты зелёные, `make check`, CHANGELOG, коммит `perf(auth): avoid aggregate clones on readiness and idle worker ticks`.

---

### Задача 3: H1 — preauth без таблицы (подспека 4.3)

**Файлы:** `internal/adapters/httpapi/preauth.go:55` (убрать карту `issued`, лимит 1024 и мьютекс на горячем пути), тест рядом `internal/adapters/httpapi/preauth_test.go`, `README.md` (лимит выдачи — обязанность Gateway, запись раздела), `CHANGELOG.md`.

- [ ] **Шаг 1: падающий тест:** 1100 вызовов `Issue("")` успешны; каждый токен проходит `Verify`; в структуре `Preauth` нет изменяемого состояния кроме секрета/часов (проверить рефлексией или отсутствием поля).
- [ ] **Шаг 2:** запустить, увидеть падение (1025-й вызов отказ).
- [ ] **Шаг 3:** убрать карту, лимит, мьютекс; токен остаётся самодостаточным (HMAC + время).
- [ ] **Шаг 4:** документировать: лимит выдачи preauth на клиента — у Gateway (R10, строка «Выдача preauth»); `make check`; коммит `fix(auth): drop stateful preauth issue table`.

---

### Задача 4: H2 — счётчик входа не забивается без Argon2 (подспека 4.4)

**Файлы:** `internal/application/accounts.go:193-249` (проверка длины и UTF-8 до `loginFailures.acquire`), `internal/application/login_failures.go`, `tests/integration/login_throttle_test.go`, `README.md` («Credentials and sessions»), `CHANGELOG.md`.

- [ ] **Шаг 1: падающие тесты** (в `login_throttle_test.go`): (а) 4097 входов с разными логинами и паролем из 33 символов, затем вход с верным паролем проходит; (б) пять таких попыток для существующего логина не вызывают кулдаун; (в) невалидный UTF-8 в пароле не двигает счётчик; (г) ответ на такие попытки прежний `InvalidCredentials`; (д) обычные неверные пароли по-прежнему двигают счётчик и вызывают кулдаун (регрессия).
- [ ] **Шаг 2:** запустить, увидеть падение.
- [ ] **Шаг 3:** выполнить проверки длины и UTF-8 до `acquire`; такие попытки счётчик не создают и не двигают.
- [ ] **Шаг 4:** README: отвергнутые по длине или UTF-8 попытки счётчик не двигают; остаточный риск (целевая блокировка известного логина пятью проверяемыми попытками в минуту) записывается в тот же раздел. `make check`; коммит `fix(auth): do not count length-rejected logins in the throttle`.

---

### Задача 5: H3 и H5 — усиление вызовов к пирам (подспека 4.5)

**Файлы:** `internal/application/reconciliation.go:54-73, 155-240`, `authorization.go:159`, `memberships.go:66`, `registry.go:38`, `system_tokens.go:52`, `deletion.go:191`, `tests/integration/peer_io_test.go`, `CHANGELOG.md`.

Требования: Status одного сервиса объединяются (singleflight на уровне реализации, без новых внешних зависимостей — своя небольшая реализация, если `golang.org/x/sync` не в `go.mod`); после IO наблюдения применяются к заново загруженному агрегату с повторной оценкой только использованных фактов (ресторан, версия жизненного цикла, intent, сессия), без повторного IO; сверка берёт Status раз на сервис за проход и пишет подтверждения пачкой. Gateway остаётся пиром: числа считаются для трёх сервисов.

- [ ] **Шаг 1: падающие тесты** (в `peer_io_test.go`; существующий `TestPeerObservationCannotGrantStaleAuthority` и остальные peer-тесты не менять по смыслу): (а) 24 параллельных `AuthorizeToken` при удержанных ответах пиров дают не более 3 вызовов Status (сейчас 900); (б) смена ревизии во время IO не повторяет Status; (в) проход сверки на 1000 готовых ресторанов после смены эпохи одного пира: Status не более 2 на сервис за проход (сейчас 3012), коммитов готовности не более одного на ресторан (сейчас шесть). Счётчики вызовов — через фейк пира с атомарными счётчиками и каналами удержания.
- [ ] **Шаг 2:** запустить, увидеть падение с текущими числами.
- [ ] **Шаг 3:** реализовать; прогнать весь `tests/integration` с `-race -count=1`.
- [ ] **Шаг 4:** `make check`; коммит `fix(auth): coalesce peer status calls and batch readiness confirmations`.

---

### Задача 6: H4 и M7 — воркеры, готовность, журнал (подспека 4.6)

**Файлы:** `bootstrap/serve.go:59-92`, `bootstrap/runtime.go:288-300` (`failWorker`, липкий `workerErr`, `Ready`), `internal/adapters/httpapi/router.go:100` (фильтр `Ready`: JWKS закрывается только неготовностью подписи), `internal/application/accounts.go:159-190`, новый `bootstrap/logging.go` (slog JSON), `cmd/server/main.go`, тесты в `bootstrap/` и `tests/integration/`, `README.md`, `CHANGELOG.md`.

Требования: классификация ошибок воркеров; `slog` JSON по `/Users/yahi3123/projects/umir-2026/umir-factory/docs/observability.md` §3 (поля `time` с +03:00, `event`, `log_schema_version=1`, `service`, `version`, `instance_id`) — если файла в `umir-factory` нет или формат расходится, взять поля из подспеки и записать `Ruling`; события `auth.worker.degraded|recovered|fatal`, `auth.mail.attempt` (только идентификаторы, состояние, номер попытки, возраст). Готовность по возможностям (`/readyz`, C32) и метрики — НЕ в этой волне.

- [ ] **Шаг 1: падающие тесты:** (а) фейковый `MailProcessor` один раз возвращает `ErrStorageUnavailable` — воркер продолжает с паузой 1→30 с (пауза через инъектируемый таймер), JWKS отвечает 200 всё время, `Ready` не падает из-за воркера; (б) `ErrRecoveryRequired` или `ErrUpdateOutcomeUnknown` — `Serve` возвращает ошибку (процесс выходит с кодом 1); (в) журнал в буфере не содержит кодов, адресов получателей, токенов, шифротекста (подать синтетические значения и проверить отсутствие подстрок).
- [ ] **Шаг 2:** запустить, увидеть падение.
- [ ] **Шаг 3:** реализовать; ни одна ошибка воркера не оставляет процесс «живым, но 503». Проверка: нет вызовов `slog` с секретами; `go vet`.
- [ ] **Шаг 4:** `make check`, `make quality-test`; коммит `fix(auth): classify worker errors and add structured redacted logging`.

---

### Задача 7: M4 — Argon2 вне мьютекса (подспека 4.7)

**Файлы:** `internal/application/challenges.go:550` (`CompleteInvitedUser`), `:691` (`password_reset`), `enrollment_commands.go:52` (`enrollmentCommand`), образец `registration.go:79-128` (`registrationPassword`, `registrationPasswordAccess`), `tests/integration/enrollment_flows_test.go` или новый `tests/integration/argon_outside_mutex_test.go`, `CHANGELOG.md`.

- [ ] **Шаг 1: падающие тесты:** блокирующий хешер (канал); пока он занят, `Peek` и вход другого пользователя завершаются; после хеша изменённые proof, версия учётных данных, повторная команда или конфликт идентичности не оставляют следов (команда отвечает прежней ошибкой, агрегат без изменений); точный повтор не вызывает хеширование; `Passwords.Hash` не вызывается внутри `enrollmentCommand` (счётчик вызовов с признаком «под мьютексом» через проверку `mu.TryLock()` в хешере-фейке).
- [ ] **Шаг 2:** запустить, увидеть падение.
- [ ] **Шаг 3:** хеш вне мьютекса через `verificationGate`, перепроверка условий на коммите.
- [ ] **Шаг 4:** `make check`; коммит `fix(auth): hash passwords outside the accounts mutex`.

---

### Задача 8: L1, L2, L3 — мелкие дефекты (подспека 4.8)

**Файлы:** `internal/application/accounts.go:219` (L1), `internal/adapters/mail/smtp.go:123` (L2), `internal/application/invitations.go:129` (L3), тесты рядом, `CHANGELOG.md`.

- [ ] **Шаг 1: три отдельных падающих теста:** L1 — время ответа неизвестного логина не зависит от параметров `Users[0].Verifier` (проверять по числу и параметрам обращений к хешеру через фейк: `Verify` вызывается с фиктивным верификатором выбранного хешера, не с `Users[0]`); L2 — сбой dial оставляет задание в повторе (`queued`/временная ошибка), не в `unknown`; ошибки после установки соединения по-прежнему `unknown`; L3 — `Bob@x` и `bob@x` дают одно ожидающее приглашение (`sameEmail`).
- [ ] **Шаг 2:** запустить, увидеть три падения.
- [ ] **Шаг 3:** реализовать каждое; три отдельных коммита `fix(auth): ...` (L1, L2, L3), в каждом запись CHANGELOG.
- [ ] **Шаг 4:** `make check`.

---

### Задача 9: M1, M2, M5 — только тесты и документ (подспека 4.9)

**Файлы:** новый `tests/integration/otp_budget_pin_test.go`, `tests/integration/clock_step_test.go` (или в `internal/adapters/jwt/`), `README.md` (раздел «Модель угроз OTP» / «OTP threat model», на ревью владельцу), `CHANGELOG.md`.

- [ ] **Шаг 1:** (а) M1 — тест фиксирует текущее поведение: бюджет неверных кодов по адресу 10 в час, общий для челленджей; общий бюджет выдачи 3 за 10 мин на адрес, делится с приглашениями; (б) M5/R12 — тесты шага часов назад (`jwt/signer.go:146` отказывает выпуску и JWKS) и нулевого допуска `iat`: поведение записано, не изменено. Тесты проходят сразу (фиксируют существующее), поэтому в отчёте указать, что «падение до правки» неприменимо.
- [ ] **Шаг 2:** README: раздел модели угроз OTP (выдача, попытки, бюджет по адресу, срок кода, параллельность, несколько аккаунтов) с расчётом из принятой семантики; указать, что число 8,4 % не используется; требование одной ВМ или синхронизации часов (R12).
- [ ] **Шаг 3:** `make check`; коммит `test(auth): pin OTP budgets and clock-step behaviour` + `docs(auth): add OTP threat model`.

---

## Завершение волны

- `make check` и `make quality-test` зелёные, числа тестов — в журнал (`go test -json`-подсчёт или `-v | grep -c '^--- PASS'`). `make integration` не объявлен (C37) — не нужен.
- Финальное ревью всей ветки (Opus), один цикл правок. Затем push `fix/durable-w1-auth`, draft PR, статус CI один раз, отчёт `wave-1-report.md`.

## Самопроверка плана

- Покрытие: 4.1 → З1; 4.2 → З2; 4.3 → З3; 4.4 → З4; 4.5 → З5; 4.6 → З6; 4.7 → З7; 4.8 → З8; 4.9 → З9; C18 (надгробия) → З1.
- Не делается и записывается в отчёте: T2 волны 2; чистка сессий/идемпотентности и реже продление idle; `/readyz` по возможностям; L4, L6, L7.
- Отклонение от шаблона навыка: в планах нет готовых блоков кода тестов — исполнитель пишет их по подробному описанию, потому что код зависит от существующих фикстур (4 000+ строк интеграционных тестов); имена тестов и числа заданы.
