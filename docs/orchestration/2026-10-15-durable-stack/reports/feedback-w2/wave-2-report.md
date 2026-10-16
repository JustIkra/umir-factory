# Отчёт feedback, волна 2 (частично выполнена, остановка по лимиту)

Статус: PARTIAL. Выполнены T1-T3 из плана. Ветка `feat/durable-w2-feedback`, worktree `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-feedback-w2`.
SHA базы: 75c3799bd7561e376b03e43b0418929888d5d2a0 (fix/durable-w1-feedback). Итоговый: 80c22e4. Коммитов: 3.
План: /Users/yahi3123/projects/umir-2026/docs/superpowers/plans/2026-10-16-durable-w2-feedback.md. Эталон: umir-factory reference/durable@26166c8ba197 (copy.sh --check чистый).

## Коммиты
- build: копия эталона postgres/rabbitmq/relay, pgx и amqp091, make integration, хелперы tests/testsupport/itest
- feat: контракт хранилища (Job.Fence, Submission.Erased, квоты, надгробия, keyset), memory-хранилище, tests/storecontract (23 случая)
- feat: PostgreSQL-хранилище и схема 0001_init.sql (RLS, роли, квоты, fence, outbox в одной транзакции со строкой)

## Проверки
make check ok (0 issues, arch-lint ok), make quality-test 37 сценариев, make integration ok (все пакеты, в т.ч. tests/pgintegration и контракт на Postgres).

## Сделано / не сделано (подспека разд. 5, 8)
| Пункт | Статус |
| --- | --- |
| 5.1 схема, RLS, роли, grants, generation | сделано |
| 5.2 Store: Accept/Lookup/Claim/Finish(fence)/Retry/Status/EraseEmail/ListScoped(keyset)/Sweep/ApplyScopeDeleted(+Once через inbox)/WakeDue/реестр | сделано (код и интеграционные тесты) |
| Контрактный набор memory + postgres | сделано |
| Копия эталона, make integration | сделано |
| 5.3 п.5,7: relay, потребители каналов, потребитель feedback.auth, пассивная проверка топологии | НЕ сделано (T4) |
| Сверка с реестром Auth (C45, C50): HTTP-клиент и цикл | НЕ сделано; хранилище (RegistryRevision/ApplyRegistry) готово |
| Аренды worker_leases для фоновых работ, supervise | НЕ сделано (Guard в Store готов) |
| 5.5 конфигурация, шесть секретов на _FILE (C63), подкоманды migrate / generation rotate / server serve / healthcheck | НЕ сделано (T5). Замечание: в коде волны 1 secretValue уже принимает _FILE для всех шести секретов; нужны тесты и усиление (O_NOFOLLOW, владелец) |
| 5.6 /readyz с capabilities | НЕ сделано |
| 5.7 удаление journal, /feedback, Legacy, runWorker; перевод фикстур на memory | НЕ сделано; сервис всё ещё работает на файловом журнале |
| Матрица отказов, README, итоговое ревью | НЕ сделано (T6) |

## Ruling
- Эталон скопирован с SHA 26166c8ba197 (голова W0 сдвинулась после G1-коммита).
- Код хранилища в pgstore, а не в скопированном postgres/, чтобы copy.sh --check оставался чистым.
- memory остаётся в arch-компоненте adapters (общий сценарий quality-test требует bootstrap->memory); отсутствие memory в боевом бинарнике должно доказываться тестом go list -deps (T5).
- Случайные ID внедряются (application/domain чистые по depguard): mechanisms.NewUUIDv4.
- Гранты в миграции идут роли из настройки сессии umir.runtime_role, по умолчанию umir_feedback: тесты используют роли со случайными именами.
- Fence для всех restaurant-tombstone реестра записывается даже без заявок (закрывает гонку токена перед удалением).
- Сроки и квоты в конфигурации остаются обязательными без умолчаний (по configuration.md).
- Контракт тестов написан после memory-хранилища; красная проверка снята мутацией (снятие проверки fence краснит FinishRejectsStaleFence).

## CR
Открытых нет.

## Нужно от других групп для волны 3
Auth: Feedback в читателях реестра, события auth.*.deleted в umir.auth; Factory: роли umir_feedback(_owner), mq-init с feedback.*, секреты _FILE, FEEDBACK_INSTANCE_ID. Ветка волны 2 Feedback НЕ готова к сборке: нужно довести T4-T6.
