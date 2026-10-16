# factory-w0: отчёт по волне 0

База: 17fe41f. Итоговый: cdc4835, draft PR #18 https://github.com/JustIkra/umir-factory/pull/18 (опубликован координатором).

Коммиты после базы: scaffold и infra (T1, T2), contract-sync (T8b), docs/durable (T9a-c), ADR (T10), DUR1 (4923ba5, f81fdda), postgres/rabbitmq/redis (efecc8a, 7aebf72, 17ec45b, 4b75b26), docs-sync 59c98fe, fix-ревью d73764f, relay b3c6123, copy.sh/README/CI 52990cd, docs cdc4835.

## Проверки
- make check-durable: 0 issues, arch OK, 5 пакетов ok.
- make check: все модули зелёные, кроме tests/business из-за чужой копии umir-warehouse рядом; с закреплёнными коммитами (git archive) ok.
- make check-contract ok; make check-infra ok (45 node-тестов + shim).
- make integration (итоговый, на cdc4835): itest, postgres, rabbitmq, redis, relay - все ok (2.4, 14.3, 28.0, 10.1, 17.5 s).
- Frontend-проверки неприменимы (в репозитории нет package.json).

## Ruling
- Правки при сборке: BrokerURL в relay-тестах; ключи слотов `<kind>:<rest>` в тесте; errcheck check-blank через ignoreErr; gofmt-кавычки в комментариях.
- CI поднимает umir-prod-pg/mq вручную и использует durable-test-infra.sh up/env.
- deps.go удалён, go mod tidy без изменений.

## Числа и сроки, оставленные на G1 (все помечены «предложение до G1»)
- Таблицы сроков хранения R9 (docs/durable/retention.md): окна и TTL Redis, захваты страниц 5 мин, аренды 15-60 с, срок логов Nginx 14 дней, 35/30 дней квитанций Warehouse/Serving, 30/180 дней результата и отпечатка команд Auth.
- Бюджеты допуска R10 (docs/durable/admission.md).
- Квоты SSE: 8 на сессию, 200 на организацию, 2000 на ячейку.
- Лимиты: 200 позиций, 2000 активных предложений, 10 000 ресторанов, maxLength 256.
- Задержка relay до секунды (рекомендация).

## Открытые CR
04-09 в каталоге cr/ (retry-DLQ цепочка, CheckPassive, уточнения DUR1, ACL лимитера, maxmemory кэша, противоречия configuration).

## Отложенное
- Нет.

## Цикл правок по итоговому ревью (HEAD 06cc671)
Коммиты: 9583cac (B1/C56), c1ad88c (B2/C59), 18efe3a (B3/C60), 7f4bef3 (M1, Relay), c56e165 (M2, M3, C58, C61, C63), 06cc671 (Low: LoadURL).
- B1: events.md описывает вариант CR04 как в коде (копия в .retry с x-umir-attempt, .dlq-копия с x-umir-failed-attempts, защита x-delivery-limit); тест сверяет аргументы очередей. Повторная подача из .dlq считается с первой попытки (код).
- B2: паттерн имён возможностей `^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)?$`; примеры здоровья по configuration.md, пример Feedback; составные файлы перегенерированы, SHA в amendment обновлены.
- B3: traceID для causationID/correlationID (до 256 кодовых точек без управляющих), тесты.
- M1: пауза relay после Nacked/Unknown 0,2 с -> 5 с, тест на поддельных часах.
- M2/M3: ключи Redis, +evalsha/+script|load, два ключа limiter, restore без строгого режима; README: CheckPassive, Dial.
- C58/C63: Auth healthcheck `/server healthcheck`, секреты Feedback `_FILE` в волне 2.
- Low: rabbitmq/redis LoadURL проверяют симлинк, режим, владельца, размер.
Проверки: make check, check-durable, check-contract, check-infra, make integration зелёные (itest 4.0, postgres 20.8, rabbitmq 45.6, redis 9.8, relay 18.7 s).
Не сделано (заметки для волны 2): x-max-length/x-message-ttl у exclusive fan-out очередей; замер пропускной способности издателя (по одному сообщению); мониторинг used_memory redis-limiter (noeviction 128mb) и Continuity.Check как второй вызов Redis; сигнал готовности при бесконечном retry umir.tasks; порядок выката при точном равенстве VerifySchema (runbook); copy.sh не переносит версии зависимостей go.mod; CommitUnknownError у Claim оставляет строки захваченными до конца аренды (30 с).

- Повторное ревью (53ef371): §1 п. 4 и 9 configuration.md восстановлены, C62/C63 перенесены в §8, добавлен тест; пауза relay не сбрасывается пустым клеймом, добавлен тест.

- G1 (26166c8): пометки заменены на «утверждено G1 16.10.2026»; потолок до G2 — SSE 8/200/2000, срок журнала склада; пороги R13 и общий потолок входа, сброса пароля и заказа не в таблицах, добавятся после R13. Логи Postgres и RabbitMQ — ротация по размеру; Feedback 30/180/180, квоты 150 000/5 000 (строка в retention.md). Шаг 1 порядка R3 уже в DUR1 (операции Gateway deprecated). Политика DUR1 несёт статус G1, SHA пересчитаны.
