# Auth, волна 1: финальное ревью (fix/durable-w1-auth, a91ddfe)

## Вердикт: Needs fixes (0 Critical, 1 Important, 4 Minor)

Остальное сделано и корректно. Одно Important закрывает цель 4.6 «ни одна ошибка не оставляет процесс живым, но 503» для случая, который тесты не покрывают.

## Замечания

### Important

1. `internal/application/accounts.go:179,195` вместе с `bootstrap/logging.go:fatalWorkerError` и `bootstrap/serve.go:supervise`. Когда `ErrRecoveryRequired` или `ErrUpdateOutcomeUnknown` случился в HTTP-запросе (`commit`, строка 226, или `load`, строка 185), выставляется `s.recovering = true`. Дальше `load` и `inspect` возвращают голый `ErrAuthUnavailable`, без `ErrRecoveryRequired`. Воркеры получают нефатальную ошибку, пишут `auth.worker.degraded` с паузой до 30 с и не выходят. `Ready` при этом тоже 503. Итог: процесс жив и отвечает 503, restart от compose не наступает. Тест (б) проверяет только ошибку, которую воркер получил сам из процессора. Исправить: при `s.recovering` возвращать `errors.Join(ErrAuthUnavailable, ErrRecoveryRequired)` или дать `Accounts.RecoveryPending()`, и считать это фатальным в `supervise`. Добавить тест: сбой commit из запроса, `Serve` возвращает ошибку.

### Minor

2. `README.md` (абзац про `Accounts.Reconcile`): «persists pending/current epoch state before external calls» устарело. После 25488e3 pending пишется пачкой после IO (ruling 5). Ресторан остаётся `ready` на время Register, доступ закрывается проверкой свежих эпох в `runtimeAccess`. Поправить README.
3. `internal/application/accounts.go:dummyVerifier`: `sync.Once` навсегда кэширует ошибку `Hash`. Единичный сбой хешера делает все неизвестные логины `ErrAuthUnavailable` до рестарта. Первый неизвестный логин процесса платит два хеша (одноразовый сигнал по времени, нейтральный по существу). Исправить: считать dummy при старте или не кэшировать ошибку.
4. `internal/application/reconciliation.go:runtimeStatus` и `observed`: слушатель, пришедший к уже летящему вызову, и повтор после смены ревизии используют ответ, снятый до его старта (до 5 с). Окно устаревшей эпохи не шире прежнего IO, `TestPeerObservationCannotGrantStaleAuthority` зелёный. Оставить, но знать перед G1.
5. `bootstrap/process.go`: `instance_id` берётся из случайного `NewID()`, а C19 задаёт `auth-a`/`auth-b` из factory. Нужно читать из конфигурации в волне 2 (вместе с `/readyz`).

## Проверенное без замечаний

- 4.1 (задача 1, ревью заново): `ValidateReplacementAt` разрешает ровно очистку (челлендж, не-live OTP-задания, надгробия записей, у которых все `JobIDs` убраны). Негативные случаи покрыты таблицей тестов. Часы `jsonstore` совпадают с часами Auth (`WithClock` в `open`). Бюджет неверных кодов не теряется: срок равен `max(expiresAt, последняя неверная) + 1 ч`. Повтор verify/complete после очистки отдаёт сохранённый результат (f7fbf21), повтор begin/resend отдаёт 409 `CommandConflict`/`unknown`, нового вызова нет. `LookupCommand` для надгробия отвечает 409 unknown (открытый вопрос владельцу контракта).
- 4.2: `Ready` и пустой тик не клонируют агрегат. `Inspect` read-only, колбэки не удерживают указатели.
- 4.3: в `Preauth` нет состояния.
- 4.4: проверка длины и UTF-8 стоит до throttle. Метка времени не зависит от существования логина. Ответ при действующем кулдауне InvalidCredentials, не rate limited: оправдано (ruling 4), нового оракула нет.
- 4.5: flight отцеплен от отмены первого вызывающего через `WithoutCancel` и пятисекундный срок, горутина ограничена этим сроком, утечек нет. `flight.st` пишется до `close(done)`, порядок событий верный. В `recordReadiness` при конфликте ревизии идёт повтор с нуля.
- 4.6: журнал не содержит кодов, адресов, токенов и шифротекста (в коде вызовы `Log` только в `serve.go` и `logging.go`, поля: идентификаторы, состояния, счётчики, `error_class`). `Ready` не зависит от воркеров. JWKS закрыт только `SigningReady`. `loggingMailSender` не пишет получателя.
- 4.7: хеш вне мьютекса, через gate. Точный повтор хеш пропускает. На коммите проверка proof, версии учётных данных и идентичности повторяется внутри `enrollmentCommand`. Записи идемпотентности не исчезают, потерянного обновления и двойного применения нет. Лишний хеш не оставляет следов.
- 4.8: L1, L2, L3 отдельными коммитами с тестами. 4.9: тесты и раздел README «OTP threat model» есть, арифметика таблицы верна.
- C51: Redis в коде нет. C18: очистка в волне 1. C32/C33 не затронуты.
- Коммиты: Conventional Commits, футеров `Co-Authored-By` и generated-by нет, `.superpowers` и бинарников в диффе нет. CHANGELOG совпадает с кодом (одно замечание в п. 2 касается README).
- Флак `TestUA1ObservationCacheDoesNotEvictOrReadAfterCapacity`: файлы теста и `tests/fixtures` веткой не менялись. Тест однопоточный (последовательные вызовы handler с фейковым часом), горутин и общих данных нет, гонку скрыть не может. `-race -count=5`: 5 из 5 зелёные (10,2 с).

## Таблица полноты (подспека 4.1–4.9)

| Пункт | Статус | Замечание |
| --- | --- | --- |
| 4.1 K1 очистка, надгробия | сделано | текст ревью задачи 1 не восстановлен, код проверен заново |
| 4.2 K2 без clone | сделано | |
| 4.3 H1 preauth | сделано | |
| 4.4 H2 счётчик входа | сделано | |
| 4.5 H3/H5 пиры | сделано | п. 2, 4 |
| 4.6 H4/M7 воркеры, журнал | сделано частично | п. 1 (recovering из запроса), п. 5; `/readyz` и метрики в волне 2 по плану |
| 4.7 M4 Argon2 вне мьютекса | сделано | |
| 4.8 L1, L2, L3 | сделано | п. 3 |
| 4.9 M1, M2, M5 | сделано | |

## Проверки

- `GOWORK=off GOTOOLCHAIN=go1.26.9 make check`: зелёный. golangci-lint 0 issues, go-arch-lint без предупреждений, `go test -race -count=1 ./...` все пакеты ok (bootstrap 46,6 с, protocol 140,4 с, integration 134,6 с). Флака protocol в этом прогоне не было.
- `go test -race -count=5 -run TestUA1ObservationCacheDoesNotEvictOrReadAfterCapacity ./tests/fixtures/protocol`: ok.
- `make quality-test` и счёт тестов не перезапускались; числа исполнителя (37 сценариев, 313 тестов) не перепроверены.
