# Auth, волна 1: отчёт

Ветка fix/durable-w1-auth, worktree /Users/yahi3123/projects/umir-2026/_wt/durable/umir-auth. База 79188bf, итоговый SHA c08cb8e, 18 коммитов. Не опубликовано (push и PR запрещены, решает пользователь).

## Коммиты
- d680a44 fix(auth): purge expired email challenges and tombstone their command records (4.1)
- f7fbf21 fix(auth): replay stored enrollment results after the challenge is purged (правка по ревью задачи 1)
- 3d3631e perf(auth): avoid aggregate clones on readiness and idle worker ticks (4.2)
- fb6b93b fix(auth): drop stateful preauth issue table (4.3)
- 7541b2c fix(auth): do not count length-rejected logins in the throttle (4.4)
- 25488e3 fix(auth): coalesce peer status calls and batch readiness confirmations (4.5)
- f3887e3 fix(auth): classify worker errors and add structured redacted logging (4.6)
- 75db972 fix(auth): hash passwords outside the accounts mutex (4.7)
- 3e25b76 fix(auth): verify unknown logins against a dummy verifier (L1)
- c8b6c84 fix(auth): retry SMTP dial failures instead of marking them unknown (L2)
- acaae0b fix(auth): compare invitation addresses without case (L3)
- 66846e7 test(auth): check the listener address type in the SMTP dial test
- 7bcd042 test(auth): pin OTP budgets and clock-step behaviour (4.9)
- a91ddfe docs(auth): add OTP threat model (4.9)

## Проверки (GOWORK=off GOTOOLCHAIN=go1.26.9)
- `make check` на a91ddfe: lint 0 issues, arch-lint ok, все пакеты зелёные, кроме одного флака `tests/fixtures/protocol TestUA1ObservationCacheDoesNotEvictOrReadAfterCapacity` (400 на «allowed observation 23»), который упал только в прогоне под нагрузкой от других исполнителей. Тест отдельно `-count=3` зелёный; весь пакет protocol дважды зелёный (16 с и 18 с); повторный `go test ./... -count=1 -v` без FAIL.
- Число верхнеуровневых тестов: 313 PASS (`go test ./... -count=1 -v`), 0 FAIL.
- `make quality-test`: 37 сценариев прошли. `make integration` не объявлен (C37).
- `-race` для tests/integration, internal/application, bootstrap зелёные после задач 5, 6, 7 (integration 145 с и 143 с, bootstrap 51-68 с).
- Frontend к Auth не относится.

## Пункты подспеки 4.1–4.9
| Пункт | Статус | Коммит |
| --- | --- | --- |
| 4.1 K1 очистка вызовов, надгробия | сделано | d680a44, f7fbf21 |
| 4.2 K2 безкопийное чтение (`Inspect`, Ready, пустой тик) | сделано | 3d3631e |
| 4.3 H1 preauth без таблицы | сделано | fb6b93b |
| 4.4 H2 счётчик входа и длина/UTF-8 | сделано | 7541b2c |
| 4.5 H3/H5 вызовы к пирам | сделано | 25488e3 |
| 4.6 H4/M7 воркеры, готовность, журнал | сделано (без `/readyz` по возможностям и метрик: волна 2, C32/C33) | f3887e3 |
| 4.7 M4 Argon2 вне мьютекса | сделано | 75db972 |
| 4.8 L1, L2, L3 | сделано, три отдельных коммита | 3e25b76, c8b6c84, acaae0b |
| 4.9 M1, M2, M5 тесты и модель угроз | сделано (тесты фиксируют текущее поведение, «падения до правки» нет) | 7bcd042, a91ddfe |
Не делалось по плану: T2 волны 2 (разрез порта Store), чистка сессий и идемпотентности, реже продление idle, L4, L6, L7.

## Ruling (отклонения и решения)
1. 4.1: тест потока вызовов усечён (4000 засеянных строк и 4 живых окна вместо 11,5 ч): полный цикл 10 минут под -race ломает таймаут пакета. Цена: слабее покрыт полный сценарий.
2. Правка Important 1: повтор команды ищется по ключу до проверки доступа к вызову для любой ErrChallengeUnavailable; в `registrationPasswordAccess` поиск повтора поставлен перед `challengeAccess`. Цена: другой код ошибки только при совпадении ключа актора.
3. 4.2: тест меряет байты кучи (`runtime.MemStats`), а не `AllocsPerRun`: клон слайса это одна аллокация независимо от размера. Допуск 4096 Б. Бенчмарк `BenchmarkReady100k` строит фикстуру через `&testing.T{}` (хак, работает, пока фикстура не падает).
4. 4.4: попытка с паролем невалидной длины или UTF-8 во время действующего кулдауна отвечает InvalidCredentials (раньше rate limited): без записи счётчика проверить кулдаун нечем.
5. 4.5: статусы пиров спрашиваются параллельно и объединяются (flight оторван от отмены первого вызывающего, пятисекундный срок); после смены ревизии статусы переиспользуются, агрегат перезагружается и факты оцениваются заново. В проходе сверки pending не пишется до IO, проверка статуса после Register убрана: одна запись на ресторан, пир, перезапущенный между Register и концом прохода, замечается итоговым наблюдением. Тест на 24 вызова синхронизируется счётчиком загрузок и `Gosched`, без sleep; микроокно у отставших возможно, в 20 прогонах не наблюдалось.
6. 4.6: формат журнала из umir-factory/docs/observability.md §3 (файл есть и совпадает), время в Europe/Moscow. `ErrClosed` тоже считается фатальной. Нефатальные ошибки повторяются бесконечно с паузой до 30 с; постоянная нефатальная ошибка видна только в логе `auth.worker.degraded`. Существующий тест `TestMailDeadlineDoesNotHideUnknownAuthorityOutcome` переписан: теперь Serve возвращает ErrUpdateOutcomeUnknown (это и есть требование (б)). В `MailDelivery` добавлены `Attempt` и `Age`.
7. 4.7: вместо `mu.TryLock()` в фейке (mu приватный, тесты внешние) проверяется, что Peek и вход другого пользователя завершаются, пока хеш удержан.
8. L1: dummy verifier считается один раз выбранным хешером при первом неизвестном логине внутри слота gate.
9. L3: исправлена проверка в приложении (`sameEmail`); ключ уникальности в доменном валидаторе не менялся.

## Открытые CR
Нет. Новых CR-файлов не создавалось.

## Отложенные замечания
- Minor 1 и Minor 2 из ревью задачи 1: текст ревью утерян, не восстановлены.
- `LookupCommand` для надгробия отвечает 409 unknown (`ErrCommandResultCleared`); подтвердить у владельца контракта. Чужой интерфейс не менялся.
- `jsonstore.WithClock` опционален: в проде проводка верна, без неё хранилище берёт системные часы.
- Двухпроходная очистка вызова с живым заданием (после долгого простоя воркера).
- Флак protocol-теста под нагрузкой (см. проверки) и ранее известный `TestRealHTTPOrganizationSettingsReplayAndMinimumSchedulerAuthority`.
- Публикация: push в Kxqusos/umir-auth и draft PR в JustIkra/umir-auth были отклонены правами, сделает пользователь. CI в PR из форка может ждать одобрения владельца.

## Финальное ревью (a91ddfe): Needs fixes, исправлено одним циклом
Ревью: final-review.md (0 Critical, 1 Important, 4 Minor). Все пункты закрыты, итоговый SHA c08cb8e.
- Important (4.6): 6275021. `Accounts.RecoveryPending()`; `supervise` дописывает `ErrRecoveryRequired` к ошибке воркера, если запрос перевёл Accounts в восстановление. Тест `TestRecoveryStartedByARequestStopsTheProcess` (красный без правки: процесс остаётся жив).
- Minor 2 (README про Reconcile): 6d31e51.
- Minor 3 (dummyVerifier): f455979, ошибка Hash больше не кэшируется; тест `TestDummyVerifierFailureIsRetried` (красный до правки).
- Minor 5 (instance_id): c08cb8e, `AUTH_INSTANCE_ID`, случайный ID только запасной; формат `[a-z0-9-]{1,63}`, неверное значение останавливает запуск; тест `TestInstanceIDComesFromTheConfiguredReplicaName`; README и CHANGELOG.
- Minor 4 (Status): Ruling: описано в README (6d31e51), код не менялся — вызывающий может получить ответ, снятый до его старта, не старше 5 с (срок вызова); окно не шире прежнего IO, `TestPeerObservationCannotGrantStaleAuthority` зелёный. Цена ошибки: устаревшая эпоха на срок до 5 с; чтобы исключить, нужен отдельный вызов на каждого, т.е. возврат к 900 вызовам Status.
- Проверки после правок: `make check` exit 0 (lint 0, arch ok, все пакеты, включая -race); `make quality-test` 37 сценариев; `go test -race ./... -count=1 -v`: 316 PASS, 0 FAIL.
