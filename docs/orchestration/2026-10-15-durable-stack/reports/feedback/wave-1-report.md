# Отчёт группы feedback, волна 1

Статус: DONE_WITH_CONCERNS (код готов и проверен; push и draft PR не выполнены, см. ниже).

- Подспека: /Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/feedback.md (раздел 4)
- Ветка: `fix/durable-w1-feedback`, worktree /Users/yahi3123/projects/umir-2026/_wt/durable/umir-feedback
- SHA базы: fa59b633a24bb003bbc94e2fc810716674c88698 (origin/main); итоговый: 75c3799bd7561e376b03e43b0418929888d5d2a0
- План: /Users/yahi3123/projects/umir-2026/docs/superpowers/plans/2026-10-15-durable-feedback.md
- Журнал: progress.md в этом же каталоге; тело PR: wave-1-pr-body.md

## Коммиты (11)
b930f5d W1-0 domain validate + ErrLeaseLost; ee75ac4 W1-1 JWKS; 045aacb тесты JWKS; e37f38b W1-2 VPN backoff; a1e71a5 fixup лесенки VPN; f38f112 W1-3 Reply-To; 813016e W1-4 observer; a884dfc fixup W1-4; 68d7b7d W1-5 docs + тест iat; ddd633f docs; 75c3799 правки по финальному ревью.

## Проверки
- `make check`: зелёный (0 замечаний lint, arch-lint OK, 11 пакетов ok с `-race`).
- `make quality-test`: 37 сценариев.
- Тестов pass (с подтестами): 272 (база 233).
- `make integration`: не объявлен (C37).
- Красные тесты наблюдались до правок во всех задачах; для W1-2 красный получен на заглушке типа (первый заход написал код до теста); для W1-5 тест границы iat регрессионный, мутация условия краснит его.
- Ревью: ревью каждой задачи (Sonnet) и финальное (Opus): Ready, Critical/Important нет; minor исправлены в последнем коммите, кроме двух отложенных (захват замка записи на быстром пути JWKS, владелец фоновой горутины JWKS).

## Проблема публикации
`git push -u origin fix/durable-w1-feedback` отклонён: 403, `Permission to JustIkra/umir-feedback.git denied to Kxqusos` (gh вошёл как Kxqusos, у этого аккаунта нет права записи в репозиторий). Обходных путей (форк, смена учётных данных) не применялось. Draft PR не создан; готовое тело в wave-1-pr-body.md. Нужно дать права записи или выполнить push/PR от владельца.

## Отклонения (Ruling)
- Ruling: W1-0 выполнена лидером напрямую (3 строки), красный тест подтверждён — слишком мала для отдельного исполнителя.
- Ruling: `golang.org/x/sync` в go.mod нет; single-flight реализован на канале (подспека допускает), `.go-arch-lint.yml` не менялся, новых зависимостей нет.
- Ruling: события `unknown` также для ErrNotFound/ErrLeaseLost (уровень WARN, поле cause); код результата передаётся во всех исходах.
- Ruling: README про `/feedback` при `FEEDBACK_AUTH_URL` описан по факту кода: проверки метода/типа/размера/JSON (405/415/413/400), затем 503 для корректного запроса.
- Ruling: тест MAIL FROM без SMTPUTF8 фиксирует лишь отсутствие расширения на тестовом сервере, который его не объявляет (net/smtp добавляет SMTPUTF8 сам, если сервер объявил его); защита — отсутствие не-ASCII в заголовках.

## Открытые CR
Нет.

## Заметки для волны 2
- Переходы `retry_exhausted` внутри журнала не наблюдаются (будут при замене хранилища).
- Тесты не покрывают склейку VPN-планировщика с `run()` напрямую (покрыт тип планировщика и `refreshTunnel`).
- Каждый повтор VPN останавливает туннель на время обновления (описано в README).
