# Auth, волна 2: отчёт (остановлено по лимиту, ЧАСТИЧНО)

База c08cb8e (fix/durable-w1-auth). Итоговый SHA: см. `git log` ветки feat/durable-w2-auth (2 коммита: 890da46 + docs). Не опубликовано.

## Сделано
- T1 (890da46): эталон postgres/rabbitmq/relay скопирован через copy.sh @26166c8ba197 без правок; pgx v5.11.0, amqp091 v1.15.0; arch-lint; `make integration`; `tests/itest` (БД с ролями owner/runtime, vhost). Проверки: fmt-check, lint, arch, build зелёные; интеграционные тесты tests/itest 2 PASS на живых Postgres/RabbitMQ. Полный `make check` на ветке не перезапускался (изменения только добавляющие).
- Подготовлено (не закоммичено, в stash `durable-auth-w2-wip`): очистка domain (Aggregate, Validate удалены), порт `application.Store`, новые accounts/sessions/login/user/passwords/profile/enrollment_commands/enrollment_limits. Не компилируется: остальной application и тесты не переведены.

## Не сделано (T2-T12 по плану)
Порт Store и memstore, схема/RLS/миграции и адаптер Postgres, owner.json v2 и keys check, подкоманды, outbox/события/письма, реестр ETag/since/точечное, токены C11, очистка по срокам, /readyz по возможностям, R3 (Gateway из пиров), удаление jsonstore, README/CI/smoke, фикстуры на memstore. Пункты подспеки «Волна 2» 5.1-5.13 не выполнены, кроме основы T1.

## Ruling
- Таблицы в схеме public своей БД (эталон использует search_path) — цена низкая.
- Решения по дизайну порта (в плане/stash): один revision на транзакцию; RLS: глобальные таблицы для любого actor, org-таблицы по членству пользователя, Rescope/EnterOrg; регистрация-конфликт по email_key/login_fold в нижнем регистре; временная персистентность memstore снимком до T5.

## CR
Нет.

## Для волны 3
Ничего от других групп пока; R3 сливать после Gateway PR-1.
