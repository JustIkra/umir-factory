# Gateway, волна 2: отчёт (СТОП по лимиту, работа не завершена)

База: 993b1aabc9132f1b1df53314f69d11a42ee45f88. Итог: c48713c, 4 коммита (ec8fb79, ecea2f6, 2c16ccb, c48713c). Эталон скопирован @26166c8ba197.
Ветка зелёная: `make check`, интеграционные тесты redisadmission и itest на реальном Redis. `make quality-test` и полный `make integration` на итоговом HEAD не запускались.

| Пункт | Статус |
| --- | --- |
| Копии эталона, `make integration`, CI | сделано |
| Конфигурация 5.4, LoadDurable, приватный listener необязателен | сделано |
| Ядро допуска, строгий режим, контрактные наборы, Redis-интеграция | сделано |
| Допуск на маршрутах, удаление C31 (registration_limit.go, temporary_memory.go) | НЕ сделано: в stash `durable-gateway-w2-wip` (код почти готов, тесты cmd/server/w2_admission_test.go падают: 503 AuthUnavailable из-за фейка Auth, нужны права/пути в фикстуре) |
| Аренды SSE, G1-4 | не начато |
| Журнал Apply/ResetAll, потребитель RabbitMQ, удаление локального Append | не начато |
| Реестр по ревизии (C13), планировщик под арендой | не начато |
| /readyz по возможностям, Dockerfile, compose, README | частично (README-заметка) |
| Сценарии отказов, самопроверка | не начато |
| PR-2 (ветка feat/durable-w2-gateway-pr2) | не начато; ждёт PR Auth |

Ruling: policy-слой написан вместе с тестами, красный прогон не фиксировался; обязательные секреты в config.LoadDurable; SSE-аренда берёт три слота одним вызовом после AccessCheck; существующие потоки при потере брокера остаются открытыми и получают refresh-required после ResetAll (запланировано).
Продолжение: `git stash pop` в worktree, довести T4 и далее по плану docs/superpowers/plans/2026-10-16-durable-w2-gateway.md.
CR: нет.
