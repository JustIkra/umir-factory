# G-serving, волна 1: независимые правки umir-serving

> Для исполнителей: используйте superpowers:subagent-driven-development. Шаги — чекбоксы.

**Цель.** Одиннадцать независимых правок раздела 4 подспеки `docs/superpowers/specs/2026-10-15-durable-stack/serving.md` (W1-1 … W1-11) в ветке `fix/durable-w1-serving` от `origin/main` `196ccc6`. Контракт DUR1 не трогаем; Postgres, lease, проекция Auth — волна 2 (не делать).

**Архитектура.** Только правки внутри существующих слоёв (`internal/domain` ← `internal/application` ← `internal/adapters/*` ← `bootstrap` ← `cmd/server`), без новых зависимостей в `go.mod`. Порты определяет потребитель; `.go-arch-lint.yml` без исключений.

**Worktree:** `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-serving`. Все команды с `GOWORK=off GOTOOLCHAIN=go1.26.9`.

**Проверки задачи:** целевые тесты красные до кода и зелёные после; `go test -race ./<затронутые пакеты>`; перед коммитом `make check`; `git diff --check`. В конце ветки: `make check`, `make quality-test`, `go test -race ./...`. `make integration` в репозитории не объявлен (C37) — не требуется.

**Коммиты.** Один коммит на задачу (Conventional Commits, английский, без футеров Co-Authored-By/generated-by), с записью в `CHANGELOG.md` раздел «Не выпущено» (формат — свободный абзац в начале списка, как у соседних записей). Правила FIN-04 из `7e9789c` НЕ переносить (в main уже `adf3fd3`).

## Порядок задач

Порядок выбран по пересечению файлов: T1 → T2(W1-8) → T3(W1-7) → T4(W1-2) → T5(W1-3) → T6(W1-10) → T7(W1-4) → T8(W1-5) → T9(W1-6) → T10(W1-9) → T11(W1-11). Задачи выполняются последовательно в одном worktree.

| Задача | Пункт | Суть | Тест сначала |
| --- | --- | --- | --- |
| T1 | W1-1 | `CommandConflict`/`TechnicalError`; перенос `f4db1b4` (`git format-patch` из `_wt/prod/umir-serving`, только чтение, + `git am`/`git apply --exclude=CHANGELOG.md`), `InternalError → TechnicalError`, старый код только читается, таблица `TestEveryEmittedErrorCodeIsInContractEnum`, список enum в `tests/testsupport` с SHA источника | да |
| T2 | W1-8 | Удалить `MarkPending/ResolvePending/Replace` из порта и memory, переписать тесты через `BeginAttempt/ReserveAttempt/CompleteAttempt` | нет (удаление), `grep` пуст |
| T3 | W1-7 | Ожидание блокировки предложения по контексту (канал-семафор + `select`) | да |
| T4 | W1-2 | `resolveAttempt` смотрит `/commands` при `Pending`/`Unseen` | да |
| T5 | W1-3 | Пассивные чтения не сверяют `timeZone/settingsVersion` | да |
| T6 | W1-10 | README: 10 с → 5 с; `:79` против `:154`; текст правила W1-3 | нет |
| T7 | W1-4 | `registering` без локальной установки не сбрасывает `ready` | да |
| T8 | W1-5 | JWKS: `RWMutex` + singleflight с упреждением | да |
| T9 | W1-6 | `MaxIdleConnsPerHost: 64` | да |
| T10 | W1-9 | `*slog.Logger`, события, «канарейки», замена `log.Printf` | да |
| T11 | W1-11 | `secretEnv(name)` с `<NAME>_FILE` для 4 ключей | да |

Для каждой задачи исполнитель читает соответствующий абзац W1-N в подспеке (источник правды), AGENTS.md и README.md, пишет падающий тест, показывает его красный вывод в отчёте, затем код, затем `make check`, коммит. Ревьюер проверяет соответствие подспеке, наличие красного теста, отсутствие лишнего.

## Самопроверка плана (чек-лист writing-plans)

- Каждому пункту W1-1…W1-11 соответствует ровно одна задача: да.
- Нет заглушек «реализуйте позже»: волна 2 явно исключена.
- Типы и имена из подспеки (`secretEnv`, `reservePending`, `TestResolvePendingDoesNotCommit` …) не переименованы.
- Зависимости между задачами: T10 использует сценарии T4, T5, T7 (поэтому после них); T6 после T5; остальные независимы.
- `7e9789c` не переносится; `CHANGELOG` правит каждая задача (кроме T2 — запись тоже нужна по подспеке «каждая правка — отдельный коммит с записью в CHANGELOG»).
