# Финальное ревью serving, волна 1

Ветка fix/durable-w1-serving, HEAD e31944f, 14 коммитов поверх origin/main (196ccc6). Дифф прочитан целиком (39 файлов, +3552/-329).

## Вердикт: Ready

Critical: 0. Important: 0. Minor: 4. Все W1-1…W1-11 сделаны. Поведение T10 (slog) и T11 (`_FILE`), которые не проходили независимое ревью, проверено по коду и тестам: дефектов нет.

## Замечания

### Minor

1. `cmd/server/main.go` (`secretEnv`, строка с `strings.TrimRight(string(data), "\r\n")`).
   - Что не так: `TrimRight` с набором символов снимает все завершающие `\r` и `\n`, а README (раздел про ключи) и CHANGELOG обещают снять ровно один перевод строки. Тест `TestSecretEnvReadsFileAndTrimsOnlyTrailingNewline` случая `key\n\n` не содержит.
   - Сценарий: файл `key\n\n` даёт `key`, документ говорит `key\n`. На практике безвредно (мягче), но документ и код расходятся.
   - Исправление: заменить на `TrimSuffix(TrimSuffix(s, "\n"), "\r")` и добавить кейс `key\n\n`, либо исправить текст README и CHANGELOG на «завершающие переводы строки».

2. `tests/emitted_error_codes_test.go`, CR 01 (открыт).
   - Что не так: три кода `ProposalSuperseded`, `ProductNotFound`, `DishNotFound` помечены `outside: true`. Я подтвердил, что их порождает только legacy-разбор ошибок склада (`warehousehttp/client.go:614-636`). Scoped-клиент идёт через `decodeScopedError` и даёт `ProtocolError`, который `handler.go` превращает в `UpstreamUnavailable`. В v2 эти коды не достижимы, но тест это не закрепляет: scoped-режим таблицы подставляет ошибку прямо в runtime.
   - Сценарий: кто-то добавит эти коды в scoped-разбор, и тест не покраснеет. Gateway отдаст 502/unknown.
   - Исправление: добавить тест «scoped-клиент на ответ склада с этими тремя кодами возвращает `ProtocolError`/`UpstreamUnavailable`» (CR уже предлагает его). Саму замену кодов решать в DUR1, legacy-код уйдёт в волне 3 (C28). Блокером для G1 не считаю.

3. `cmd/server/main.go` (`main`).
   - Что не так: неверный `SERVING_INSTANCE_ID` роняет и `healthcheck`: логгер строится до разбора аргументов.
   - Сценарий: опечатка в имени реплики, контейнер падает на healthcheck ещё и по healthcheck.
   - Исправление: создавать логгер после ветки `healthcheck` либо игнорировать ошибку идентификатора в ней. Для prod влияния нет.

4. Журналы не несут `request_id`, `trace_id` из `observability.md` §3. W1-9 их не требует, поэтому это не недоделка волны, но стоит помнить: связывать записи Serving с Gateway пока можно только по `command_id` и `proposal_id`.

### Принято без замечаний (проверено)

- Лог-записи: канарейки для токена, `Authorization`, тела, ключей и адресов (`TestLogsCarryNoTokensBodiesOrAddresses`); записи реестра пишутся после отпускания мьютекса (`TestLogsAreWrittenOutsideRegistryLocks`); `registry.tenant_mismatch` ограничен по частоте и по памяти.
- JWKS (`authhttp/client.go`): быстрый путь под `RLock`, одна общая горутина обновления с владельцем и `Close()`, `wg.Add` только под замком до `closed`. Fail-closed после 30 с и троттлинг unknown kid сохранены. Тест на 100 мс управляется часами.
- Замок предложения: семафор с `select` по `ctx.Done()`, счётчик ссылок снимается при отмене, есть тест на повторное использование и на число горутин.
- W1-2: `/commands` читается до `ConfirmationPending` и до повторной отправки commit; Resolve не коммитит.
- W1-3: пассивные пути (`/order-attempts`, `/commands`, `/orders`) не сверяют настройки, `/order-commits` остаётся строгим; оба случая покрыты тестами.
- W1-4: `registering` без локальной установки не снимает готовность; локально закрытый ресторан, `ready` без установки, откат ревизии и недоступность Auth дают `false`.
- Старый код `CommandIdentityConflict` в сохранённых попытках читается и пишется как `CommandConflict`. Складской `InternalError` проходит без переименования, как требует подспека.
- Коммиты: Conventional Commits, футеров Co-Authored-By и generated-by нет, мусора и секретов в диффе нет (не-Go файлы: только `.golangci.yml`, `CHANGELOG.md`, `README.md`).

## Таблица полноты

| Пункт | Статус | Комментарий |
|---|---|---|
| W1-1 CommandConflict/TechnicalError | Сделано | Таблица кодов и тесты есть. CR 01 открыт, см. Minor 2 |
| W1-2 `/commands` до Pending | Сделано | Все четыре сценария теста из подспеки |
| W1-3 смена настроек | Сделано | README описывает правило |
| W1-4 готовность и registering | Сделано | Плюс исправление для `r.closed` (6cb4545) |
| W1-5 JWKS без общего шлюза | Сделано | 50 параллельных проверок за 100 мс |
| W1-6 пул соединений | Сделано | `MaxIdleConnsPerHost: 64`; тест считает соединения через `ConnState` (порог 40) |
| W1-7 контекст в блокировке | Сделано | 100 мс дедлайн, ответ не позже 150 мс |
| W1-8 мёртвые методы порта | Сделано | Имён в продовом коде нет |
| W1-9 slog и события | Сделано | Все шесть событий из подспеки есть; `log.Printf`/`log.Fatal` в продовом коде нет |
| W1-10 README | Сделано | Таймаут 5 с и правило unseen совпадают с кодом |
| W1-11 ключи из `_FILE` | Сделано | См. Minor 1 про обрезку |

CHANGELOG: записи в «Не выпущено» по каждой правке. README и CHANGELOG совпадают с кодом, кроме Minor 1.

## Проверки (GOWORK=off GOTOOLCHAIN=go1.26.9, worktree umir-serving)

- `make check`: зелёный, golangci-lint 0 issues, arch-lint, все 14 пакетов с тестами ok.
- `make quality-test`: 37 сценариев пройдено (совпадает с отчётом).
- `go test -race -count=1 ./...`: все пакеты ok, повторов из-за времени не потребовалось.
- `make integration`: цели нет, не требуется (C37).
- Наборы `git log` по футерам и `git diff --name-only`: чисто.
