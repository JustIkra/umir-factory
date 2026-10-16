# CR 07 (factory-w0): ACL Redis для маркера `umir:limiter:*` и скриптов

Что: маркер непрерывности лежит под `umir:limiter:*` (`generation`, `strict_since`), то есть вне префиксов сервисов из C17 (`umir:gw:*`, `umir:wh:*`). Эталонный `redis.Continuity.Check` выполняет у Gateway скрипт с `GET`, `SET … NX` и `DEL` на этих двух ключах (восстановление маркера после окна и удаление `strict_since`, C47). `gateway.md` (раздел о передаче работы Factory) даёт Gateway только чтение `umir:limiter:generation`, C17 — только свой префикс. Кроме того, список команд для скриптов в `configuration.md` §6 («`+eval`, `+evalsha`, `+script|load`») не совпадает с эталоном: пакет вызывает `EVALSHA` и `SCRIPT LOAD`, а ACL Redis проверяет и команды внутри Lua.

Нужные права пользователя Gateway на `redis-limiter`:
- ключи: `~umir:gw:*`, `~umir:limiter:generation`, `~umir:limiter:strict_since`;
- верхний уровень: `+evalsha`, `+script|load` (`EVAL` пакет не использует), `+ping`, `+hello`, `+client|setinfo`, `+client|setname` (команды подключения go-redis v9 уточнить тестом ACL);
- внутри скриптов: `GET`, `SET`, `DEL`, `INCR`, `PTTL`, `PEXPIRE`, `TIME`, `EXISTS`, `ZADD`, `ZREM`, `ZSCORE`, `ZCARD`, `ZRANGEBYSCORE`, `HSET`, `HGET`, `HDEL`, `HVALS`. `TIME` читает часы Redis для срока слотов и аренд.
Пользователь Warehouse на `redis-cache` (слоты захватов) — те же команды слотов (`ZADD`, `ZREM`, `ZSCORE`, `ZCARD`, `ZRANGEBYSCORE`, `HSET`, `HGET`, `HDEL`, `HVALS`, `PEXPIRE`, `PTTL`, `EXISTS`, `TIME`) на `~umir:wh:*` плюс команды записи и чтения значения захвата.
Задание `redis-init` — запись маркера на тех же двух ключах.

Зачем: без записи на два ключа Gateway не может восстановить маркер и выйти из строгого режима (окно пройдёт, а `SET NX` получит `NOPERM`); без перечисленных внутренних команд скрипты допуска, аренд и слотов падают с отказом ACL, что для допуска означает `ErrAdmissionUnavailable` и 503 на каждой защищённой операции.

Кого затрагивает: Gateway (`gateway.md`, раздел о Factory), Factory (ACL-файлы в `secrets.sh`, W2.1; тест ACL), главная спека (C17: «ACL сервиса — только на свой префикс»), Warehouse (`warehouse.md` 5.8: слоты захватов).

Предложение: принять исключение к C17: Gateway получает запись (`SET NX`, `DEL`) только на ключи `umir:limiter:generation` и `umir:limiter:strict_since`, а не на весь `umir:limiter:*`; `gateway.md` исправить («чтение» → «чтение, `SET NX`, `DEL`»). `configuration.md` §6 уже выдаёт эту запись (см. его §8, расхождение «ACL маркеров limiter»); перечень команд скриптов взять из этого CR вместо `+eval`. Тест ACL Factory проверяет, что чужие ключи и `FLUSH*`, `CONFIG`, `KEYS`, `DEBUG`, `SHUTDOWN` по-прежнему запрещены.

**Решение оркестратора (16.10):** см. раздел 10 главной спеки, «Решения по CR волн 0 и 1» (C56–C63).
