# reference/durable

Эталонный Go-модуль durable-стека: `github.com/JustIkra/umir-factory/reference/durable` (Go 1.26.9; `pgx/v5`, `amqp091-go`, `go-redis/v9`). Сервисы **копируют** пакеты в свои `internal/adapters/{postgres,rabbitmq,redis,relay}` и **не импортируют** модуль. Решения и их номера (C1–C53) — в [главной спеке](../../docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md) и [ADR 0002](../../docs/adr/0002-postgres-rabbitmq-redis.md); имена и топология — в [docs/durable/](../../docs/durable/events.md).

## Правила копирования

```
reference/durable/copy.sh <пакет>[:<имя>]... <каталог сервиса>
reference/durable/copy.sh --check <пакет>[:<имя>]... <каталог сервиса>
```

- Копируются файлы пакета без `_test.go` и SQL-фрагменты в `<сервис>/internal/adapters/<имя>` (имя по умолчанию равно имени пакета; другое имя переписывает `package`).
- Каждый Go-файл получает первой строкой `// Source: umir-factory reference/durable@<sha>` и пустую строку.
- Правки логики делаются только в эталоне (CR в каталог запросов оркестратора). В копии допустимы только импорт-пути.
- `--check` ничего не меняет: сообщает `missing`, `differs` (строка `Source` не считается), `extra` и ревизию, с которой сделана копия; код возврата 1 при любом расхождении. Тесты не копируются: сервис пишет свои, а набор эталона запускается из репозитория Factory.
- Тест скрипта: `tests/infra/durable-copy.test.sh` (`make check-infra`).

Пакеты не импортируют друг друга. `relay` описывает свои интерфейсы (`OutboxStore`, `Publisher`); связующий код (преобразование строк и исходов) сервис пишет в корне композиции, как описано в `relay/doc.go`. В тестах `relay` использует `postgres`, `rabbitmq` и `itest` (`.go-arch-durable.yml`).

## Пакеты и функции

### postgres

| Что | Функции |
| --- | --- |
| Соединение | `Open(ctx, Config{URL, ApplicationName, MaxConns, ConnectTimeout}) (*DB, error)`, `Close`, `Ping`, `Pool`; ошибки открытия не содержат учётных данных |
| Секреты | `LoadDSN(getenv, prefix)` (`<P>_DATABASE_URL` или `<P>_DATABASE_URL_FILE`, оба — ошибка, значение в ошибках не показывается; файл: обычный, не символическая ссылка, ограниченного размера), `LoadMigrateDSN` (только `<P>_MIGRATE_DATABASE_URL_FILE`) |
| Транзакции | `(*DB).InTx(ctx, pgx.TxOptions, fn)` (до 3 попыток при 40001/40P01 с паузой и джиттером; `*CommitUnknownError`, если исход COMMIT неизвестен), `InScopedTx(ctx, Scope{Actor, OrgID, RestaurantID, UserID}, opts, fn)`, `InSystemTx(ctx, reason, opts, fn)`; `SetScope`/`SetSystem` ставят GUC `umir.*` через `set_config(..., true)`, внутри транзакции |
| Роль | `AssertRuntimeRole(ctx, db)`: не суперпользователь, не владелец таблиц, `NOBYPASSRLS`, нет `CREATE` в схеме |
| Миграции | `Migrate(ctx, db, Spec{Service, FS, Dir})` под `pg_advisory_xact_lock`, `VerifySchema(ctx, db, Spec)`; `ErrSchemaNewer`, `ErrSchemaOutdated`, `ErrMigrationMismatch`; директива включения SQL-фрагментов |
| RLS | `RLSPolicySQL(table, column, ...RLSOption)`, `RLSUUIDColumn()`; политика читает GUC через `NULLIF`, без контекста строк нет |
| Generation | `Generation(ctx, db)`, `RotateGeneration(ctx, db, reason)`; фрагмент `StorageMetaSQL()` |
| Аренда | `NewLease(db, name, holder, ttl)`, `Acquire` (возвращает `fence`), `Renew`, `Release`, `Fence`; фрагмент `WorkerLeasesSQL()` |
| Outbox | `NewOutbox(db)`, `AddEvent(ctx, tx, Event)`, `Claim(ctx, owner, limit, lease) ([]Row, fence, error)`, `MarkPublished(ctx, owner, fence, ids, routed)` (`ErrLeaseLost`), `Release(..., delay)`, `DeleteOlderThan`, `Stats`; фрагмент `OutboxSQL()` |
| Inbox | `NewInbox(db)`, `Apply(ctx, consumer, eventID, fn) (applied, err)`, `DeleteOlderThan`; фрагмент `InboxSQL()` |

### rabbitmq

| Что | Функции |
| --- | --- |
| Подключение | `Connector{URL, Name, Heartbeat, Backoff, Dialer, Logger}`, `Run`; переподключение с `ExponentialBackoff` (0,5–30 с); `LoadURL(getenv, prefix) (BrokerURL, error)`; `BrokerURL` не печатает себя, ошибки очищены от учётных данных |
| Топология | `DefaultTopology(TopologyOptions)` (обменники `umir.events`, `umir.auth`, `umir.tasks`, `umir.dlx`; quorum-очереди `auth.mail`, `feedback.smtp`, `feedback.telegram` с `.retry` и `.dlq`; `feedback.auth` по профилю), `Declare` (только задание `mq-init`), `CheckPassive` (несовпадение — `ErrTopologyMismatch`/`ErrTopologyMissing`), `DeclareExclusive` и конструкторы `GatewayLiveQueue`, `GatewayAuthQueue`, `ServingAuthQueue` |
| Публикация | `NewPublisher(connector)`, `Run`, `Ready`, `Publish(ctx, Message) (Outcome, error)`; исходы `Confirmed`, `Returned`, `Nacked`, `Unknown`; нулевое значение — не `Confirmed` |
| Потребление | `Consume(ctx, queue, prefetch, Handler, ...ConsumeOption)` (`WithMaxAttempts`, `WithFanout`, `WithSetup`, `WithCopyPause`, `WithLogger`); ack после успешного обработчика; ошибка — копия в `.retry`, после 5-й попытки — `.dlq`; тело сообщений нигде не журналируется |

### redis

| Что | Функции |
| --- | --- |
| Клиент | `Open(ctx, Config)`, `Close`, `Ping`; `LoadURL(getenv, prefix, Kind)` |
| Допуск | `(*Client).Limiter(owner)`, `Admit(ctx, Request{ID, Fingerprint, Policy, Dimensions}) (Decision{Allowed, RetryAfter, Reason}, error)`: один Lua-скрипт, окно фиксированное, повтор с тем же `ID` не списывает; `ErrAdmissionConflict`; любая ошибка Redis — `ErrAdmissionUnavailable`, никогда «разрешено» |
| Непрерывность | `(*Client).Continuity(ContinuityConfig)`, `Check(ctx, configured) (State, error)`: нет маркера `umir:limiter:generation` или он другой — строгий режим; `strict_since` и восстановление по C47 |
| Аренда | `(*Client).Lease(owner)`, `Acquire` (`LeaseGrant` с монотонным fence), `Renew`, `Release` (чужая или истёкшая — безвредный no-op) |
| Слоты | `(*Client).Slots(owner)`, `Acquire(SlotRequest)` с несколькими измерениями (число и объём), `Renew`, `Release` (идемпотентно); ключ измерения вида `<kind>:<rest>` |

### relay

`Relay{Store, Pub, Owner, Batch, Lease, Poll, Policy, ...}`, `Run(ctx)`, `Notify()`, `Stats(ctx)` (возраст старейшей неопубликованной строки, размер очереди и счётчики). Таблица политик возврата `DefaultPolicies()`: `accept` для `umir.events` и `umir.auth` (строка публикуется с `routed=false`, растёт `ReturnedAccepted`), `retry` для `umir.tasks` и всего неизвестного. `Nacked`/`Unknown` останавливают пачку, строки возвращаются с растущей задержкой, `Unknown` повторяет с прежним `eventID`. Потеря аренды останавливает пачку. Подробности — `relay/doc.go`.

### itest

`DatabaseURL(t)`, `NewDatabase(t)` (БД `t_<repo>_<rand>`, удаление `WITH (FORCE)`), `AMQPURL(t)`, `NewVHost(t)` (vhost `t_<repo>_<rand>` через management API), `RedisLimiterURL(t)`, `RedisCacheURL(t)`, `RedisPrefix(t)` (`t:<repo>:<rand>:`, очистка только своих ключей через `SCAN`+`UNLINK`), `TCPProxy` (`NewTCPProxy`, `Addr`, `CutAfterBytes`, `CutAfterMatch`, `Pause`, `Resume`, `DropToClient`, `Close`). Нет переменных окружения — тест падает с понятным текстом, пропусков нет. Удаляются только объекты с собственными именами; `FLUSHALL`/`FLUSHDB` не используются.

## Тесты

Юнит-тесты запускаются без инфраструктуры (`make check-durable`: сборка, lint, архитектура, `-race`). Интеграционные тесты (`//go:build integration`) идут на настоящих Postgres, RabbitMQ и двух Redis:

```
scripts/durable-test-infra.sh up
eval "$(scripts/durable-test-infra.sh env)"   # не выводить на экран
make integration                               # go test -tags integration -p 1 -race ./...
```

| Пакет | Что проверяют интеграционные тесты |
| --- | --- |
| `postgres` | повтор при 40001 и deadlock, `CommitUnknownError` при обрыве сразу после COMMIT, 20 параллельных `Migrate` дают один набор миграций, отказ при версии новее и при смене имени, роль среды выполнения не выполняет DDL и не обходит RLS, изоляция по scope на настоящей роли, утечка контекста через пул (`MaxConns=1`, после отката и ошибки), outbox атомарен с бизнес-строкой, `Claim` двух реляторов не пересекается, просроченная аренда и `ErrLeaseLost`, `Inbox.Apply` и откат `fn`, `Lease` (один владелец, монотонный fence, чужая аренда безвредна) |
| `rabbitmq` | идемпотентность `Declare` и поиск несовпадений, `mandatory` без привязки — `Returned`, обрыв до confirm — `Unknown`, потеря брокера и возврат, цепочка retry → DLQ ровно на 5-й попытке, exclusive-очередь исчезает с соединением, два потребителя fan-out получают оба |
| `redis` | последний слот при 64 параллельных `Admit`, границы окна без продления TTL, тот же `ID` не списывает дважды, смена отпечатка — конфликт, NOSCRIPT после неизвестного SHA, нет маркера — строгий режим, аренда (владелец, fence, продление после истечения), `Slots` не выдают больше лимита |
| `relay` | поток «транзакция → outbox → брокер → inbox» с точками падения до и после COMMIT, до и после confirm и ack: число эффектов равно числу уникальных `eventID`; возврат `mandatory` по `accept` и `retry`; удаление опубликованного старше срока; два реляя и просроченная аренда; задержка публикации меньше секунды |
| `itest` | создание и удаление БД, vhost, префиксов; отказ удалять чужое; `TCPProxy` |

Опасные для общего Redis сценарии (`CONFIG SET maxmemory`, `SCRIPT FLUSH`, остановка) сюда не входят: их выполняет волна 3 на собственном Redis.
