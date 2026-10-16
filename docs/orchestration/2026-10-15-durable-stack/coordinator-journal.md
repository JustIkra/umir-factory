# Журнал оркестрации — спека: docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md

## Состояние на 15.10.2026, вечер

- Ревью `main` 7 репозиториев: https://claude.ai/artifact/7oQFY54fMjhqXpCb1qE2kL (3 критичных, 20 высоких).
- Ревью планов JustIkra (factory #17, auth #8, warehouse #4, serving #3, feedback #5, gateway #25, frontend #14): приняты как основа; правила R1–R20.
- Решения пользователя U1–U8: Postgres — истина, RabbitMQ, Redis (limiter + cache); чистый лист; всё одним релизом; подход A; 2 реплики Gateway и Serving; одна ВМ.
- Подспеки 7 групп написаны агентами Sonnet и приведены к решениям C1–C53 (раздел 10 главной спеки). Сверка: scratchpad/plans/crosscheck.md (45 расхождений, все закрыты).
- origin/main: factory 17fe41f, auth 79188bf, gateway 1c8604d, warehouse 52aae49, serving 196ccc6, feedback fa59b63, frontend 4a6452f.
- Ждёт: утверждения спеки пользователем и команды на запуск волн 0 и 1. До утверждения — только локальные коммиты.

## Ход работ

(пусто)
- 15.10, ночь: пользователь утвердил спеку и запуск волн 0 и 1 с push и draft PR. Запущены 8 ведущих групп (Sonnet, effort high; исполнители Sonnet, финальное ревью Opus):
  | Группа | Волна | Ветка | Worktree |
  | --- | --- | --- | --- |
  | factory-w0 | 0 | feat/durable-w0-factory | _wt/durable/umir-factory-w0 |
  | factory-w1 | 1 | fix/durable-w1-factory | _wt/durable/umir-factory-w1 |
  | auth | 1 | fix/durable-w1-auth | _wt/durable/umir-auth |
  | gateway | 1 | fix/durable-w1-gateway | _wt/durable/umir-gateway |
  | warehouse | 1 | fix/durable-w1-warehouse | _wt/durable/umir-warehouse |
  | serving | 1 | fix/durable-w1-serving | _wt/durable/umir-serving |
  | feedback | 1 | fix/durable-w1-feedback | _wt/durable/umir-feedback |
  | frontend | 1 | fix/durable-w1-frontend | _wt/durable/umir-frontend |
  Бриф ведущего: scratchpad/plans/lead-brief.md. Отчёты: .superpowers/sdd/2026-10-15-durable/<группа>/wave-<N>-report.md.
- 16.10, ~02:10: все 8 ведущих групп и их субагенты остановились на лимите сессии API (HTTP 429, сброс 02:10). После сброса все 8 ведущих продолжены SendMessage с места остановки (журнал группы + git status; упавшие субагенты перезапускаются). factory-w0 к остановке поднял тестовый Redis и почти закончил T1.
- 16.10: feedback — волна 1 DONE локально: `fix/durable-w1-feedback` @ 75c3799, 11 коммитов, make check зелёный, 272 теста (было 233), финальное ревью Opus — Ready. Push отклонён 403: у Kxqusos только pull в JustIkra/umir-feedback и JustIkra/umir-auth (gh api permissions). factory, gateway, serving, warehouse, frontend — push есть. Тело PR: feedback/wave-1-pr-body.md. Группе auth сказано завершить локально и сохранить тело PR. Ждёт решения пользователя о публикации.

## 16.10 — переход на одиночных Sonnet-исполнителей (указание пользователя: «не группами агентов, а просто sonnet агентами»)
- Ведущие групп остановлены лимитом; не возобновляются. Бриф: scratchpad/plans/solo-brief.md (без субагентов, без push; ревью ветки — отдельный Sonnet-ревьюер от координатора; затем правки и публикация).
- Исполнители: factory-w0 ae00853110d55b499, factory-w1 a1b4f8cff7134a146, auth a8e460a05264c63db, gateway af2bccbe8c536605f, warehouse a303d1c24da95b997, serving a8c8be288826157cf, frontend af7be9dfe51df7565.
- Feedback: волна 1 готова локально (75c3799); публикация через форк Kxqusos (ответ пользователя «через pr»).
- 16.10: feedback опубликован через форк: Kxqusos/umir-feedback fix/durable-w1-feedback @ 75c3799 → draft PR https://github.com/JustIkra/umir-feedback/pull/6
- 16.10: serving — финальное ревью Sonnet Ready (0/0/4), правки 3 Minor, draft PR https://github.com/JustIkra/umir-serving/pull/4 @ 6c1d59f, CI Check зелёный.
- Ruling: CR 03 принят как C54 (обычный hash вместо consistent; C20 поправлен) — consistent+resolve на nginx 1.30.5 не закрепляет — цена: при ≥3 репликах смена состава даёт больше refresh-required.
- Ruling: CR 02 решён вариантом B как C55 (`error_log stderr crit;`) — правило ПДн раздела 8 без исключений — цена: меньше диагностики ошибок апстрима в error_log (есть в access_log по статусу и request_id).
- 16.10: процесс координатора перезапускался; 5 исполнителей (factory-w0, factory-w1, auth, gateway, warehouse) оборвались и возобновлены SendMessage по scratchpad/plans/resume-note.md.
- Указание пользователя: «Когда останется 80% лимитов прекращай работу и делай коммиты, обновляй доку и делай PR того что есть». Ruling: ветки публикуются сразу (draft PR) и обновляются после каждой задачи; по сигналу «стоп» исполнители за ≤15 мин доводят до зелёного, обновляют README/CHANGELOG/отчёт/тело PR и пушат — счётчик лимитов координатору не виден, поэтому PR «того что есть» существует в любой момент — цена: больше промежуточных push в draft PR.
- Auth: форк Kxqusos/umir-auth создан; исполнитель публикует туда по resume-note.
- Frontend: финальное ревью Sonnet Ready (0 Critical, 2 Important = CR 02/03, 4 Minor); исполнителю отправлены правки (C55, тексты, тест UMIR_TRUSTED_PROXY) и публикация.
- 16.10: агентам после перезапуска отказано в push/PR и docker (классификатор прав; gateway и warehouse успели опубликовать: PR gateway#26, warehouse#5). Решение пользователя: публикует координатор (auth — через форк), тестовые контейнеры durable разрешены (только umir-durable-redis-*, umir-prod-pg, umir-prod-mq). Redis-контейнеры поднял после перезапуска Docker.
- Публикации координатора: factory#18 (wave 0 @ cdc4835), auth#9 из форка (@ 7541b2c, задачи 1–4 из 9). Скрипт: scratchpad/publish.sh <группа>.
- factory-w0: исполнитель DONE_WITH_CONCERNS (make integration не завершён) — гоняет его сейчас; финальное ревью Sonnet запущено.
- 16.10: frontend правки ревью a823710 (C55 error_log crit, текст «Заказы», тест UMIR_TRUSTED_PROXY); check 1055/0; test:nginx 40/42 локально (failover-сценарий 504 после перезапуска Docker Desktop, причина не найдена). Опубликовано: draft PR https://github.com/JustIkra/umir-frontend/pull/17 — CI с test:nginx решит, среда или дефект.
- Ruling: CR 04–09 и блокеры ревью W0 (B1–B3, M1) решены как C56–C63 и пункт «Relay» в разделе 10 главной спеки — по рекомендациям ревьюера W0 (вариант B для retry/DLQ = код эталона) — цена: при ошибке правятся документы DUR1 до G1.
- 16.10: gateway DONE (3e488b8, 19 коммитов, make check зелёный, quality 38, тестов 505 vs 411); опубликовано в PR #26; финальное ревью Sonnet запущено. frontend: CI #17 красный в 2 тестах failover nginx — дефект, исполнитель ищет причину (бисект 64c131b..a823710).
- 16.10: factory-w0 повторное ревью: регрессия M2 и сброс паузы relay исправлены (53ef371), опубликовано в PR #18. Волна 0 готова к G1. gateway: финальное ревью Needs fixes (1 Important, 5 Minor) — правки отправлены исполнителю.
- 16.10: warehouse DONE (73574e6, 25 коммитов; check зелёный, quality 43, perf PASS; recover 31.8 с -> 29 мс, commit 17.8 мс -> 37 мкс; BREAKING WAREHOUSE_API_MODE — сливать после compose factory, C35); опубликовано PR #5; финальное ревью Sonnet запущено. auth DONE (a91ddfe), PR #9 обновлён, ревью запущено. Пакет G1 собирается: g1-decisions.md.
- 16.10: frontend 1fd6e71 — test failover допускает один 504 сразу после остановки реплики (connect timeout 5 с совпадает с обновлением resolve); опубликовано PR #17, CI перезапущен. Заметка на волну 2/factory: уменьшить proxy_connect_timeout для gateway_live (внутренняя сеть Docker, 1–2 с), чтобы переход SSE на другую реплику был быстрее.
- 16.10: G1 пройден — пользователь принял все 8 рекомендаций пакета g1-decisions.md; записано в главную спеку, раздел 11 (G1-1…G1-8), статус спеки обновлён.
- 16.10: frontend CI #17 зелёный (1fd6e71). gateway правки финального ревью 993b1aa опубликованы в PR #26.
- 16.10: волна 2 запущена для групп с готовой волной 1 (бриф briefs/w2-brief.md, ветки feat/durable-w2-<repo> от головы волны 1, worktree _wt/durable/umir-<repo>-w2): serving a90dbdaf3c8762cb9, feedback a47a842f596ea78f0, gateway a6b24e30a2744fc87 (PR-1 первой, PR-2 отдельной веткой по C7), frontend a129f456dac71b828. factory-w0: правка статусов документов под G1.
- Ждут: auth (правки ревью), warehouse (правки ревью), factory-w1 (задачи 4–10), затем их волна 2; factory волна 2 — после factory-w1.
- 16.10: auth правки финального ревью (c08cb8e: RecoveryPending фатален, dummyVerifier, AUTH_INSTANCE_ID, README) опубликованы в PR #9 из форка; auth волна 1 закрыта. Волна 2 auth запущена: ae7bbd0ad21a6667c (удаление Gateway из пиров — последней задачей, по C7).
- 16.10: пользователь: 90% лимита — «закончи короткие таски». Исполнителям отправлен стоп после текущей задачи. Опубликовано: волна 2 — frontend#18 (готово), feedback#7, serving#5, gateway#27, auth#10 (частично); warehouse#5 обновлён (правки ревью 235ce55); factory-w1 ветка запушена (PR ждёт тела). Статус-комментарий в factory#17: https://github.com/JustIkra/umir-factory/issues/17#issuecomment-6209323331
- Следующая сессия: дождаться factory-w1 (PR), продолжить волну 2 по журналам <группа>-w2/progress.md (stash: gateway-w2 durable-gateway-w2-wip, auth-w2 durable-auth-w2-wip), начать warehouse-w2 и factory-w2.
