# CR 09 (factory-w0): расхождения между подспеками из `configuration.md` §8, требующие решения

Что: `docs/durable/configuration.md` §8 принимает по каждому расхождению рабочее решение («Контракт: …»). Семь из них меняют чужие подспеки или имена, которые читают другие группы, и нуждаются в решении оркестратора (номера по §8 на момент написания; при сдвиге нумерации искать по теме):
1. Auth: JSON-конфиг против единых `*_FILE` (п. 7). C10 и `factory.md` §6 называют `<SVC>_DATABASE_URL_FILE`, `<SVC>_MIGRATE_DATABASE_URL_FILE`, `_AMQP_URL_FILE`, `_INSTANCE_ID`; `auth.md` 5.10 задаёт `database.urlFile`, `database.migrationUrlFile`, `amqp.urlFile`, `instanceId`. Принято: JSON у Auth. Следствия: `secrets.sh` пишет конфигурацию на реплику (разные `instanceId`); для `migrate-auth` и `generation rotate` нужен отдельный файл без `migrationUrlFile` в конфигурации реплики `serve`; поле с путём CA брокера и имя каталога `owner.json` в новой схеме `auth.md` не называет.
2. Идентификаторы экземпляров Auth (п. 8): `auth.md` пишет `auth-a`, `auth-b`, Compose `factory.md` W3.1 называет сервисы `auth` и `auth-b`. Принято: идентификатор равен имени сервиса Compose. Формат идентификатора никто не задал (он входит в имена очередей RabbitMQ); предложение: строчные латинские буквы, цифры и `-`, до 63 символов.
3. Healthcheck Auth (п. 11): в `auth.md` нет подкоманды `healthcheck`, образ не задаёт `HEALTHCHECK`, FIN1 использовал задание `auth-ready`. Не решено: подкоманда `healthcheck` или `wget` из образа, и на каком listener `/readyz` отвечает без ключа (C52). Строка таблицы раздела 4 неполная.
4. Секреты Feedback не файлами (п. 9): только `FEEDBACK_SMTP_PASSWORD_FILE` — файл; `FEEDBACK_SMTP_USERNAME`, `FEEDBACK_TELEGRAM_BOT_TOKEN`, `FEEDBACK_TELEGRAM_PROXY_USERNAME`, `FEEDBACK_TELEGRAM_PROXY_PASSWORD`, `FEEDBACK_GATEWAY_KEY`, `FEEDBACK_AUTH_DISCOVERY_KEY` читаются из окружения, `feedback.md` говорит «без изменений». Это нарушает правило §1 и тест Compose «нет секретов в окружении» (W3.1). Нужны варианты `_FILE` для каждого.
5. `FEEDBACK_DIGEST_KEY` и `_FILE` (п. 1): C14 и `factory.md` W2.1 пишут `FEEDBACK_DIGEST_KEY`, `feedback.md` 5.5 — `FEEDBACK_DIGEST_KEY_FILE`. Принято `FEEDBACK_DIGEST_KEY_FILE`.
6. `auth keys check` против `keys check` (п. 2): C6 пишет `auth keys check`, `auth.md` 5.10 — подкоманда `keys check` бинарника `/server`. Принято `/server keys check`.
7. Warehouse и limiter (п. 5): `factory.md` R7 и W3.1 допускают `WAREHOUSE_LIMITER_GENERATION`, «если Warehouse пользуется limiter», `warehouse.md` 5.8 лимитера не имеет. Принято: переменной и пользователя Warehouse на `redis-limiter` нет; если лимитер появится, нужен новый CR. Тот же пункт: маркер читает только Gateway.

Зачем: пока подспеки говорят разное, Factory пишет `secrets.sh`, Compose и тесты по одному варианту, а группы реализуют по другому; расхождение проявится на сборке волны 2.

Кого затрагивает (номера пунктов списка выше): Auth (1–3, 6), Feedback (4–5), Warehouse и Gateway (7), Factory (все).

Предложение: оркестратор подтверждает принятые в `configuration.md` решения (или заменяет), правит названные подспеки и главную спеку и сообщает затронутым группам; для пункта 3 назначает вариант healthcheck Auth. Остальные расхождения §8 (пп. 3, 6, 10, 12–21; п. 4 вынесен в отдельный CR о `maxmemory`) остаются в документе как рабочие решения и CR не требуют.

**Решение оркестратора (16.10):** см. раздел 10 главной спеки, «Решения по CR волн 0 и 1» (C56–C63).
