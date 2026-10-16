# CR 03 — frontend: `hash … consistent` не закрепляет при `server … resolve` (nginx 1.30.5)

Группа: frontend, волна 1 (F1-5, C20).

**Что.** C20 и подспека требуют `upstream gateway_live { zone …; hash $umir_live_key consistent; server gateway:8080 resolve; }`. На закреплённом образе `nginxinc/nginx-unprivileged:1.30.5-alpine` `nginx -t` проходит, но запросы идут по кругу (проверено тремя вариантами): `consistent` + `resolve` — чередование; `consistent` + `server gateway:8080` без `resolve` — чередование (кольцо строится из одной записи); `consistent` + два статических `server` — закрепляет; простой `hash $umir_live_key;` + `resolve` — закрепляет (40 из 40 проверок, плюс отработка остановки и запуска реплики).

**Ruling в ветке.** `hash $umir_live_key;` без `consistent`, `resolve` и `zone` сохранены (комментарий в шаблоне). Для двух реплик (U7) разницы нет; при 3 и более репликах смена состава перетасует больше сессий, клиент получит `refresh-required` и перечитает данные. Также: regex в `map` взят в кавычки (иначе `;` обрывает токен), `resolver` вынесен с уровня `server` на уровень `http` (иначе `nginx -t` падает для `upstream … resolve`).

**Кого затрагивает.** Оркестратор (формулировка C20: убрать слово `consistent`), G-factory (псевдоним `gateway` обеим репликам без изменений).

**Предложение.** Принять ruling. Если нужен именно кольцевой `consistent`, factory должен выдать frontend статический список реплик (два `server gateway-a:8080; server gateway-b:8080;`), что противоречит решению C20 «генерируемого include нет».

Затронутые тексты: frontend.md (строка 43 и F1-5), 00-orchestration.md (C20), factory.md:175, gateway.md:234 — формулировка `hash … consistent`.

**Решение оркестратора (16.10):** принято, C54 главной спеки; C20 поправлен.
