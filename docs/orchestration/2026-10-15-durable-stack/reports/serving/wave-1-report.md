# Группа serving, волна 1: отчёт

База: 196ccc6b60f0d46e80f03cdd0b5281521f04b8c5 (origin/main). Итог: e31944f2a52177f593396d8d9f2ae92bc9d3fea7. Ветка fix/durable-w1-serving, 14 коммитов (не опубликована).

## Коммиты
- 2c6b8ac fix(httpapi): emit CommandConflict and TechnicalError from the contract error enum
- 4a46a22 refactor(application): drop unused proposal store methods
- ed57244 fix(httpapi): store CommandConflict for Warehouse-rejected attempts and drop the InternalError rename
- 8f875ca fix(application): honor context while waiting for a proposal lock
- 0d50473 fix(orders): consult recorded commands before reporting a pending retained attempt
- 67fecb4 fix(warehousehttp): keep passive reads valid after a settings change
- fdd163f docs(readme): correct the Warehouse timeout and the unseen retry rule
- cb83db6 fix(bootstrap): keep process readiness when a registering restaurant is not yet installed
- 975035f fix(authhttp): refresh JWKS keys without blocking token verification
- 6cb4545 fix(bootstrap): do not skip a registering entry for a locally closed restaurant
- c11d874 fix(warehousehttp): keep idle connections for the Warehouse host
- e8118ec feat(observability): emit structured JSON logs for unknown outcomes and reconciliation
- 5659b46 feat(server): read service keys from *_FILE variables
- e31944f fix(observability): drop addresses from the server error log and tighten pending and registry records

## Проверки (GOWORK=off GOTOOLCHAIN=go1.26.9)
- make tools, make check: зелёные (golangci-lint, arch-lint, go test по всем пакетам).
- make quality-test: 37 сценариев.
- go test -race ./...: все пакеты ok.
- make integration: цель не объявлена, в волне 1 не требуется (C37).
- git diff --check: чисто.

## Пункты подспеки
| Пункт | Статус |
|---|---|
| W1-1 CommandConflict/TechnicalError | сделано (2c6b8ac, ed57244, 2c6b8ac); CR 01 |
| W1-2 /commands до Pending | сделано (0d50473) |
| W1-3 смена настроек | сделано (67fecb4) |
| W1-4 готовность и registering | сделано (cb83db6, 6cb4545) |
| W1-5 JWKS без общего шлюза | сделано (975035f) |
| W1-6 пул соединений | сделано (c11d874) |
| W1-7 контекст в ожидании блокировки | сделано (8f875ca) |
| W1-8 мёртвые методы порта | сделано (4a46a22) |
| W1-9 структурные логи | сделано (e8118ec, e31944f); log.Printf/Fatal в продовом коде нет |
| W1-10 README | сделано (fdd163f) |
| W1-11 ключи из файлов | сделано (5659b46) |

CHANGELOG: 11 записей в «Не выпущено».

## Rulings (кратко, подробно в progress.md)
- Enum кодов взят из umir-factory-w0 (blob 0083c4b); таблица в tests/testsupport.
- Пассивное чтение несёт сохранённые настройки попытки.
- Транспорт общий с Auth: лимит 64 и на Auth-хост.
- Refresh JWKS принадлежит клиенту; Client.Close() отменяет и ждёт.
- SERVING_INSTANCE_ID необязателен (иначе UUID); в записях нет текстов ошибок и адресов.
- Пустое NAME_FILE = не задано.

## Открытые CR
- cr/01-serving-codes-outside-enum.md (ProposalSuperseded, ProductNotFound, DishNotFound вне enum).

## Отложенные замечания (low)
- T4: нет явной проверки commits==2 для confirm/Unseen/none; finishFromRecordedCommand в ветке Pending без attempt.Pending; README про повтор не упоминает /commands.
- T5, T8: low-замечания ревью без правок.
- T10/T11: ревью выполнено самостоятельно, независимого ревью не было.

## Финальное ревью (final-review.md): Ready, 4 Minor
- Minor 1 исправлен: `secretEnv` снимает ровно один завершающий перевод строки (`\n` или `\r\n`), тест на `key\n\n`.
- Minor 2: добавлен тест, что scoped-клиент на коды ProposalSuperseded/ProductNotFound/DishNotFound отвечает общей ошибкой `WarehouseUnavailable` (handler отдаёт UpstreamUnavailable), а не этими кодами. Тест фиксирует уже верное поведение, красным не был. CR 01 остаётся открытым.
- Minor 3 исправлен: неверный `SERVING_INSTANCE_ID` не роняет `healthcheck` (`processLogger`), сервер по-прежнему отказывается стартовать.
- Minor 4, заметка для волны 2: в журналах нет `request_id` и `trace_id` из observability.md §3; связь с Gateway пока только по `command_id`/`proposal_id`.
- Повторные проверки: make check, go test -race ./..., make quality-test (37) зелёные. Коммитов теперь 17.
