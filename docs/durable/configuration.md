# Конфигурация durable-стека: контракт имён, команд и здоровья

Статус: контракт волны 0 (решение C10). Документ обязателен для групп Auth, Gateway, Warehouse, Serving, Feedback, Frontend и для сборки Factory (`compose.durable.yaml`, `scripts/durable/secrets.sh`, `scripts/durable/stack.sh`). Если группа выбрала иное имя, она оформляет CR, а не меняет контракт молча. Числа сроков и бюджетов здесь не фиксируются: они в [retention.md](retention.md) и [admission.md](admission.md) со статусом «утверждено G1 16.10.2026».

Связанные документы: каталог событий и права RabbitMQ — [events.md](events.md); смысл `runtimeEpoch`, форма `/readyz` и коды ошибок на проводе — [durable-api-amendment.md](../durable-api-amendment.md); обоснование выбора хранилищ — [ADR 0002](../adr/0002-postgres-rabbitmq-redis.md); тестовая инфраструктура — `scripts/durable-test-infra.sh`.

Источники решений: C6, C8, C9, C10, C14, C17, C19, C20, C32, C33, C34, C35, C42, C44, C47, C49, C50, C51, C52 главной спеки; R2, R7, R8, R11, R12, R20 и W0.5, W2.1, W3.1 подспеки Factory; конфигурационные разделы подспек групп. Проверено по коду `Dockerfile` и `cmd/*` веток волны 1 каждого репозитория (только чтение). Если подспека группы расходится с этим документом или с главной спекой, расхождение записано в разделе 8.

## 1. Общие правила

1. Секреты задаются только файлами. Для каждого секрета есть переменная `<ИМЯ>_FILE` с путём к файлу; значение читается целиком, завершающий перевод строки отбрасывается. К секретам относятся: DSN Postgres (содержит пароль), URL RabbitMQ и Redis (содержат пароль), сервисные ключи, ключи HMAC и шифрования, пароль первого администратора, SMTP-логин и SMTP-пароль, токены провайдеров. Файлы создаёт `scripts/durable/secrets.sh` с режимом `0600` и владельцем `65532:65532`; в окружение контейнера значения секретов не попадают (Compose-тест проверяет это).
2. Файл и переменная вместе — ошибка запуска. Чтение работает как `LoadDSN(getenv, prefix)` эталона `reference/durable`: если заданы и `<P>_DATABASE_URL`, и `<P>_DATABASE_URL_FILE`, сервис не стартует; победителя нет. То же правило действует для сервисных ключей и URL брокера и Redis. Незашифрованные имена (`<P>_DATABASE_URL`, `AUTH_SERVICE_KEY` без `_FILE`) остаются только для локальной отладки и legacy-профилей (C28); durable-профиль их не задаёт.
3. Значения секретов никогда не показываются: ни в тексте ошибок (ошибка называет переменную и причину, но не значение и не URL целиком), ни в логах, ни в теле `/readyz`, ни в выводе `stack.sh`. Группы проверяют это тестом с «канарейкой».
4. Режима `memory` в боевых бинарниках нет, переключателя хранилища `<SVC>_STORAGE` нет, переменных `*ALLOW_MEMORY` нет (R20, C10). Бинарник без DSN не стартует; это проверяет тест сборки каждой группы. Compose-профиль не задаёт ни одной такой переменной, а `stack.sh preflight` и тест Compose запрещают их наличие в окружении (W2.3, W3.1). Memory-адаптеры живут только в тестовом коде и в тестовых инструментах (фикстуры RPC1.1.0 Feedback, `tests/http/cmd/feedback-fixture`; C46).
5. Два DSN на сервис с базой (C8): `<SVC>_DATABASE_URL_FILE` — роль среды выполнения `umir_<svc>`; `<SVC>_MIGRATE_DATABASE_URL_FILE` — роль-владелец `umir_<svc>_owner`, видна только заданиям `migrate-<svc>` и `generation rotate` и никогда не передаётся в `serve`. Сам себя ни один сервис не мигрирует; при старте каждый сервис вызывает `AssertRuntimeRole` и `VerifySchema` эталона.
6. Идентификатор экземпляра — `<SVC>_INSTANCE_ID` (у Auth — поле `instanceId`), уникальный на каждую реплику (C19). Он виден в `/readyz` и логах, входит в имена очередей и держателя аренды и не попадает в бизнес-ответы. Подробности — раздел 5.
7. Эндпоинта метрик нет (C33): переменных `*_METRICS_ADDR` и `*_METRICS_ADDRESS` нет. Состояние читается из `/readyz`, логов, SQL владельца, management API RabbitMQ и `INFO` Redis.
8. Prod-подключения к Postgres, RabbitMQ и Redis идут по TLS с проверкой имени (`verify-full`) и общим частным CA; тестовая инфраструктура — без TLS (раздел 7).
9. Все контейнеры работают в зоне `Europe/Moscow` (`TZ`), если контракт не требует другого.

Снятые и запрещённые переменные. Durable-профиль не задаёт ни одной из них, а `stack.sh preflight` отвергает их в окружении:

| Переменная | Статус |
| --- | --- |
| `AUTH_STORAGE`, `WAREHOUSE_STORAGE`, `SERVING_STORAGE`, `FEEDBACK_STORAGE`, `GATEWAY_STORAGE` | запрещены (R20, C10): режима хранилища в окружении нет |
| `*_ALLOW_MEMORY`, например `WAREHOUSE_ALLOW_MEMORY` | запрещены: разрешения на память нет |
| `FEEDBACK_STORE_PATH`, `FEEDBACK_MAX_STORE_BYTES`, `FEEDBACK_RETENTION_DAYS` | запрещены: файловый журнал Feedback удалён (C46) |
| `OPERATOR_STORE_PATH`, `OPERATOR_BOOTSTRAP_*`, `OPERATOR_IDLE_TIMEOUT`, `OPERATOR_ABSOLUTE_TIMEOUT`, `JOURNAL_CAPACITY` | запрещены: файловое хранилище операторов Gateway удалено (gateway.md) |
| `REGISTRY_POLL_INTERVAL` (Serving) | запрещена: опрос реестра заменён событиями и чтением по ревизии |
| `GATEWAY_METRICS_ADDR`, `SERVING_METRICS_ADDR` и любые `*_METRICS_ADDR(ESS)` | запрещены (C33) |
| `FEEDBACK_RUNTIME_ROLE` | запрещена: имя роли среды выполнения зафиксировано в миграции (C8) |
| `AUTH_RUNTIME_SERVICE_KEY_FILE`, `PRIVATE_LISTEN_HOST`, `PRIVATE_PORT` (Gateway) | удаляются в PR-2 Gateway (R3, C7): `stack.sh preflight` отвергает их в окружении Gateway после слияния PR-2; до него они необязательны и профилем не задаются |

## 2. Переменные по сервисам

Колонки: «Обязательна» — для запуска durable-профиля; «Откуда значение» — файл секрета, параметр Compose, JSON-конфигурация или значение по умолчанию в коде; «Кто задаёт» — Factory (Compose, `secrets.sh`, `redis-init`) или группа (значение по умолчанию в коде, настройка по замерам). Пути файлов в Compose — внутри контейнера, каталог секретов монтируется только на чтение.

### 2.1 Auth

Auth настраивается JSON-файлом, не отдельными переменными окружения (C10): путь задаёт `UMIR_AUTH_CONFIG_FILE`, файл пишет `secrets.sh` по схеме auth.md 5.10 (как сейчас `prepare.mjs`). Поля ниже — имена JSON, сервисных переменных `AUTH_*_URL_FILE` нет. Redis-переменных у Auth нет и ограничителя Redis нет (C51): бюджеты по логину, адресу, выдачам кодов и неверным кодам лежат в Postgres, бюджеты по клиенту держит Gateway. Поля `limiterGeneration` у Auth нет, маркер C47 его не касается.

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `UMIR_AUTH_CONFIG_FILE` | Auth | да (в образе по умолчанию `/etc/umir-auth/config.json`) | путь в Compose | Factory (Compose) |
| `database.urlFile` | Auth | да (`serve`, `init`) | файл с DSN роли `umir_auth` | Factory (`secrets.sh`) |
| `database.migrationUrlFile` | Auth | только `migrate` и `generation rotate` | файл с DSN роли `umir_auth_owner`; не попадает в конфигурацию реплики `serve` | Factory (`secrets.sh`) |
| `database.maxConns` | Auth | нет | значение по умолчанию в коде | группа Auth |
| `amqp.urlFile` | Auth | да | файл с URL брокера (`amqps://`, vhost `umir`) | Factory (`secrets.sh`) |
| `instanceId` | Auth | да | уникальный на реплику (C19) | Factory (`secrets.sh`, по реплике) |
| `worker.leaseTtlSeconds`, `retention`, `budgets` | Auth | нет | значения раздела 6 главной спеки по умолчанию | группа Auth |
| `serviceKeyFiles.{gateway,serving,warehouse,feedback}` | Auth | да | файлы ключей сервисов, которыми пиры обращаются к Auth | Factory (`secrets.sh`) |
| `peerKeyFiles.{warehouse,serving}`, `peerURLs.{warehouse,serving}` | Auth | да | ключи и адреса пиров, которых Auth вызывает; `gateway` не задаётся (R3) | Factory (`secrets.sh`) |
| `secretsDir` (каталог `owner.json` v2) | Auth | да | каталог `auth-owner-secrets`, только чтение; файл создаёт только `secrets.sh` (C6, C52) | Factory (`secrets.sh`) |
| `initialUser`, `passwordFile` | Auth | только `init` | первый администратор и файл с его паролем | Factory (`secrets.sh`) |
| `smtp`, `smtpUsernameFile`, `smtpPasswordFile`, `smtpCAFile` | Auth | да (почта входа и приглашений) | параметры почты и файлы логина, пароля и CA | Factory (`secrets.sh`, из `UMIR_SMTP_*`, см. 2.7) |
| `tlsCertFile`, `tlsKeyFile`, `privateAddress`, `zoneDataFile`, `zoneDataVersion`, `registration`, `registrationMail`, `invitationMail` | Auth | да | как во FIN1 (`prepare.mjs`), без изменения формы | Factory (`secrets.sh`) |

### 2.2 Warehouse

Redis limiter Warehouse не нужен (warehouse.md 5.8): переменной `WAREHOUSE_LIMITER_GENERATION` нет. Redis cache нужен только для захватов страниц (C27).

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `WAREHOUSE_API_MODE=v2` | Warehouse | да; пустое значение — ошибка запуска (W1-6) | параметр Compose: `v2` в durable-профиле, явный `legacy` в legacy-профилях (C35) | Factory (Compose) |
| `WAREHOUSE_DATABASE_URL_FILE` | Warehouse | да (`serve`) | файл с DSN роли `umir_warehouse` | Factory (`secrets.sh`) |
| `WAREHOUSE_MIGRATE_DATABASE_URL_FILE` | Warehouse | только `migrate` и `generation rotate` | файл с DSN роли `umir_warehouse_owner` | Factory (`secrets.sh`) |
| `WAREHOUSE_AMQP_URL_FILE` | Warehouse | да | файл с URL брокера | Factory (`secrets.sh`) |
| `WAREHOUSE_REDIS_CACHE_URL_FILE` | Warehouse | да | файл с URL `redis-cache` (`rediss://`, пользователь на префикс `umir:wh:`) | Factory (`secrets.sh`) |
| `WAREHOUSE_INSTANCE_ID` | Warehouse | да (без значения — случайный) | уникальный на реплику, сейчас `warehouse` | Factory (Compose) |
| `AUTH_SERVICE_KEY_FILE`, `AUTH_RUNTIME_SERVICE_KEY_FILE`, `GATEWAY_SERVICE_KEY_FILE`, `SERVING_SERVICE_KEY_FILE` | Warehouse | да | файлы ключей (W1-9, C14); прежние имена без `_FILE` не задаются | Factory (`secrets.sh`) |
| `AUTH_JWKS_URL`, `PORT`, `INTERNAL_PORT`, `SSL_CERT_FILE` | Warehouse | да | как во FIN1 (`compose.completion.yaml`) | Factory (Compose) |
| `WAREHOUSE_HOT_RESTAURANTS`, `WAREHOUSE_HOT_BYTES`, `WAREHOUSE_LOAD_CONCURRENCY`, `EVENTS_PER_SNAPSHOT`, `WAREHOUSE_OUTBOX_BATCH`, `WAREHOUSE_RETENTION_INTERVAL` | Warehouse | нет | значения по умолчанию в коде | группа Warehouse |

### 2.3 Serving

У Serving нет Redis и нет limiter (serving.md, C27).

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `SERVING_DATABASE_URL_FILE` | Serving | да (`serve`) | файл с DSN роли `umir_serving` | Factory (`secrets.sh`) |
| `SERVING_MIGRATE_DATABASE_URL_FILE` | Serving | только `migrate` и `generation rotate` | файл с DSN роли `umir_serving_owner`; в среду `serve` не передаётся | Factory (`secrets.sh`) |
| `SERVING_AMQP_URL_FILE` | Serving | да в durable-профиле (без него события выключены, проекция `degraded`) | файл с URL брокера | Factory (`secrets.sh`) |
| `SERVING_INSTANCE_ID` | Serving | да (без значения — случайный UUID) | уникальный на реплику: `serving-a`, `serving-b`; входит в имя очереди `serving.auth.<instance>` | Factory (Compose) |
| `SERVING_AUTH_KEY_FILE`, `AUTH_RUNTIME_KEY_FILE`, `GATEWAY_SERVING_KEY_FILE`, `SERVING_WAREHOUSE_KEY_FILE` | Serving | да | файлы ключей сервисов (W1-11, C14) | Factory (`secrets.sh`) |
| `AUTH_INTERNAL_URL`, `WAREHOUSE_INTERNAL_URL`, `SSL_CERT_FILE` | Serving | да | как во FIN1 | Factory (Compose) |
| `SERVING_DB_MAX_CONNS`, `SERVING_DB_ACQUIRE_TIMEOUT`, `SERVING_PROJECTION_MAX_AGE`, `SERVING_PROJECTION_DEGRADED_MAX_AGE`, `SERVING_PROJECTION_MAX_ENTRIES`, `SERVING_LEASE_TTL`, `SERVING_LEASE_WAIT`, `SERVING_RECONCILER_INTERVAL`, `SERVING_TTL_SWEEP_INTERVAL`, `SERVING_MAX_POSITIONS`, `SERVING_MAX_ACTIVE_PROPOSALS` | Serving | нет | значения по умолчанию в коде | группа Serving |

### 2.4 Feedback

Профиль `feedback` необязательный. Тестовые фикстуры RPC1.1.0 в контракт не входят.

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `FEEDBACK_DATABASE_URL_FILE` | Feedback | да (`server serve`) | файл с DSN роли `umir_feedback` | Factory (`secrets.sh`) |
| `FEEDBACK_MIGRATE_DATABASE_URL_FILE` | Feedback | только `feedback migrate` и `feedback generation rotate` | файл с DSN роли `umir_feedback_owner` | Factory (`secrets.sh`) |
| `FEEDBACK_AMQP_URL_FILE` | Feedback | да | файл с URL брокера | Factory (`secrets.sh`) |
| `FEEDBACK_DIGEST_KEY_FILE` | Feedback | да (не меньше 32 байт) | файл ключа HMAC отпечатка заявки; входит в список секретов и бэкап (C14) | Factory (`secrets.sh`) |
| `FEEDBACK_INSTANCE_ID` | Feedback | да | уникальный на реплику, сейчас `feedback` | Factory (Compose) |
| `FEEDBACK_REGISTRY_RECONCILE_INTERVAL` | Feedback | нет | по умолчанию 10 минут (C50) | группа Feedback |
| `FEEDBACK_AUTH_URL` | Feedback | да | адрес Auth | Factory (Compose) |
| `FEEDBACK_AUTH_DISCOVERY_KEY` | Feedback | да | сервисный ключ Feedback для Auth (`serviceKeyFiles.feedback`); им же читается приватный реестр (C50); `_FILE`-вариант вводит Feedback в волне 2 (C63), см. раздел 8 | Factory (`secrets.sh`) |
| `FEEDBACK_GATEWAY_KEY` | Feedback | да | ключ, которым Gateway обращается к Feedback; `_FILE`-вариант вводит Feedback в волне 2 (C63), см. раздел 8 | Factory (`secrets.sh`) |
| `FEEDBACK_DB_MAX_CONNS`, `FEEDBACK_LEASE_DURATION`, `FEEDBACK_AUTH_KEYS_MAX_STALE`, `FEEDBACK_WORKER_POLL_INTERVAL`, `FEEDBACK_RETRY_*` | Feedback | нет | значения по умолчанию в коде | группа Feedback |
| `FEEDBACK_RETENTION_ANONYMOUS_DAYS`, `FEEDBACK_RETENTION_RESTAURANT_DAYS`, `FEEDBACK_RETENTION_DELETED_SCOPE_DAYS`, `FEEDBACK_TOMBSTONE_DAYS`, `FEEDBACK_QUOTA_ANONYMOUS_ROWS`, `FEEDBACK_QUOTA_RESTAURANT_ROWS` | Feedback | да, без умолчаний | числа утверждены G1 (G1-2): 30 / 180 / 180 дней, квоты 150 000 / 5 000 ([retention.md](retention.md)) | Factory (Compose) |
| `FEEDBACK_SMTP_*`, `FEEDBACK_TELEGRAM_*`, `FEEDBACK_MAIL_*` | Feedback | по каналам | провайдеры доставки; секретные значения должны идти файлами, см. раздел 8 | Factory (`secrets.sh`) |

### 2.5 Gateway

У Gateway нет базы: `GATEWAY_DATABASE_URL_FILE` и команд `migrate`, `generation rotate` нет. Без `GATEWAY_AMQP_URL_FILE` и `GATEWAY_REDIS_LIMITER_URL_FILE` бинарник не стартует (R20). Redis cache Gateway не нужен.

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `GATEWAY_INSTANCE_ID` | Gateway | да (без значения — случайный UUID) | уникальный на реплику: `gateway-a`, `gateway-b`; входит в имена очередей `gateway.live.<instance>`, `gateway.auth.<instance>`; в `epoch` SSE и курсор идёт отдельный boot-nonce (C19) | Factory (Compose) |
| `GATEWAY_AMQP_URL_FILE` | Gateway | да | файл с URL брокера | Factory (`secrets.sh`) |
| `GATEWAY_REDIS_LIMITER_URL_FILE` | Gateway | да | файл с URL `redis-limiter` (`rediss://`, пользователь на префикс `umir:gw:`) | Factory (`secrets.sh`) |
| `GATEWAY_LIMITER_GENERATION` | Gateway | да | значение из файла `config/limiter-generation`; не секрет; сравнивается с маркером `umir:limiter:generation` (C17, R7) | Factory (Compose из `config/limiter-generation`) |
| `GATEWAY_ADMISSION_KEY_FILE` | Gateway | да | ключ HMAC субъектов допуска, один у всех реплик (C14, C17) | Factory (`secrets.sh`) |
| `GATEWAY_LIVE_CURSOR_KEY_FILE` | Gateway | да | ключ шифрования курсора SSE, один у всех реплик (C14) | Factory (`secrets.sh`) |
| `AUTH_SERVICE_KEY_FILE`, `WAREHOUSE_SERVICE_KEY_FILE`, `SERVING_SERVICE_KEY_FILE`, `FEEDBACK_SERVICE_KEY_FILE` | Gateway | да (`FEEDBACK_SERVICE_KEY_FILE` — при профиле `feedback`) | файлы сервисных ключей (W1.11, C14) | Factory (`secrets.sh`) |
| `AUTH_RUNTIME_SERVICE_KEY_FILE`, `PRIVATE_LISTEN_HOST`, `PRIVATE_PORT` | Gateway | нет | не задаются durable-профилем: относятся к приватным маршрутам `runtime` и `registry` и JWKS-проверке для Auth, которые удаляет PR-2 Gateway (R3, C7); до его слияния при необходимости остаются значения FIN1 | снимаются в PR-2 Gateway |
| `GATEWAY_TRUSTED_PROXY_CIDRS` | Gateway | да | подсеть `front` (`UMIR_FRONT_SUBNET`); без неё ключ допуска по клиенту не включается (C34, R11) | Factory (Compose) |
| `AUTH_URL`, `SERVING_URL`, `WAREHOUSE_URL`, `FEEDBACK_URL`, `OPERATOR_PUBLIC_ORIGIN`, `OPERATOR_COOKIE_MODE`, `LISTEN_HOST`, `SSL_CERT_FILE` | Gateway | да | как во FIN1 (`compose.completion.yaml`) | Factory (Compose) |
| `GATEWAY_REDIS_TIMEOUT`, `GATEWAY_BUDGET_*`, `GATEWAY_LIVE_*`, `GATEWAY_SUMMARY_*`, `GATEWAY_EXPIRATION_*`, `GATEWAY_REGISTRY_SYNC_INTERVAL` | Gateway | нет | значения по умолчанию в коде; бюджеты — по таблице [admission.md](admission.md) | группа Gateway |

### 2.6 Frontend

Секретов, `*_FILE`, DSN и метрик у Frontend нет. Образ — Nginx; экземпляра и generation нет.

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `UMIR_TRUSTED_PROXY` | Frontend | да (по умолчанию в образе `127.0.0.1/32`, то есть не доверяет никому) | CIDR NPM из параметра развёртывания `COMPLETION_NPM_ADDR`; Nginx `set_real_ip_from` (C34, R11) | Factory (Compose) |
| `NGINX_ENVSUBST_FILTER` | Frontend | задан в образе (`^UMIR_`) | `ENV` образа | группа Frontend |

### 2.7 Сборка и тестовая инфраструктура

Это не переменные сервисов. Их читают `secrets.sh`, `stack.sh` и тесты.

| Переменная | Сервис | Обязательна | Откуда значение | Кто задаёт |
| --- | --- | --- | --- | --- |
| `UMIR_SMTP_USERNAME_FILE`, `UMIR_SMTP_PASSWORD_FILE` | Сборка | да при реальной почте | файлы логина и пароля SMTP для `secrets.sh`; прежние `UMIR_SMTP_USERNAME` и `UMIR_SMTP_PASSWORD` в окружении не читаются (W2.1) | пользователь стенда |
| `UMIR_SMTP_HOST`, `UMIR_SMTP_PORT`, `UMIR_SMTP_TLS_MODE` | Сборка | нет | несекретные параметры почты для конфигурации Auth (заготовка `prod-secrets.sh`) | пользователь стенда |
| `COMPLETION_NPM_ADDR`, `UMIR_FRONT_SUBNET` | Сборка | да | адрес NPM и явная подсеть сети `front`, не пересекающаяся с ZeroTier и docker0 | пользователь стенда |
| `config/limiter-generation` | Сборка | да | файл со значением limiter generation; источник `GATEWAY_LIMITER_GENERATION` и маркера `redis-init` | Factory (`secrets.sh`) |
| `UMIR_BACKUP_DIR` | Сборка | да для `backup` | каталог вне томов Docker | пользователь стенда |
| `UMIR_TEST_DATABASE_URL`, `UMIR_TEST_AMQP_URL`, `UMIR_TEST_AMQP_MANAGEMENT_URL`, `UMIR_TEST_REDIS_LIMITER_URL`, `UMIR_TEST_REDIS_CACHE_URL` | Сборка | только интеграционные тесты | вывод `scripts/durable-test-infra.sh env`; без них тесты падают, пропусков нет | разработчик, CI |

Список секретов для хранения и бэкапа (`umir-secrets`, `auth-owner-secrets`): DSN обеих ролей каждой базы, URL брокера и обоих Redis, сервисные ключи (11 имён пар «кто → кого»), `FEEDBACK_DIGEST_KEY`, `GATEWAY_ADMISSION_KEY`, `GATEWAY_LIVE_CURSOR_KEY`, limiter generation, `owner.json` v2, пароль первого администратора, SMTP-логин и SMTP-пароль, CA и ключи TLS.

## 3. Команды и роли базы

Команды привязаны к бинарникам репозиториев (C44). В образах точка входа `ENTRYPOINT ["/server"]`, поэтому Compose задаёт подкоманду как `command`. Исключение — Feedback: второй бинарник `/feedback` вызывается переопределением `entrypoint`. По этой таблице работают `stack.sh` (`up`, `restore`) и задания `migrate-<svc>`. Подкоманда `serve` никогда не мигрирует схему и не создаёт generation и ключи: без схемы, generation или DSN она не стартует (C9).

| Сервис | Бинарник в образе | Команда | Роль БД (DSN) | Кто запускает | Примечание |
| --- | --- | --- | --- | --- | --- |
| Auth | `/server` | `migrate` | `umir_auth_owner` (`database.migrationUrlFile`) | задание `migrate-auth` | advisory lock эталона; первая миграция создаёт `storage_meta` с generation; отказ при схеме новее кода |
| Auth | `/server` | `init` | `umir_auth` (`database.urlFile`, `InSystemTx`) | задание `init-auth` после `migrate-auth` | создаёт первого администратора из `initialUser` и `passwordFile`, если его нет; повтор ничего не меняет; `owner.json` только читает |
| Auth | `/server` | `serve` | `umir_auth` | реплики `auth`, `auth-b` | `AssertRuntimeRole`, `VerifySchema`, чтение generation и ключей; ничего не создаёт |
| Auth | `/server` | `generation rotate --reason <текст>` | `umir_auth_owner` | `stack.sh restore` | только при остановленных репликах; идемпотентна |
| Auth | `/server` | `keys check` | нет (читает `owner.json`) | `init-auth`, проверка в `stack.sh` | проверка формата `owner.json` v2 (в C6 записана как `auth keys check`) |
| Warehouse | `/server` | `migrate` | `umir_warehouse_owner` | задание `migrate-warehouse` | как у Auth |
| Warehouse | `/server` | `serve` | `umir_warehouse` | сервис `warehouse` | при `WAREHOUSE_API_MODE=v2` без DSN не стартует |
| Warehouse | `/server` | `generation rotate --reason <текст>` | `umir_warehouse_owner` | `stack.sh restore` | старая generation пишется в `generation_history` |
| Warehouse | `/server` | `healthcheck` | нет | healthcheck Compose | проверяет `/readyz` (C32) |
| Serving | `/server` | `migrate` | `umir_serving_owner` | задание `migrate-serving` | сервис себя не мигрирует |
| Serving | `/server` | `serve` | `umir_serving` | сервисы `serving-a`, `serving-b` | живая реплика сверяет generation и завершается при несовпадении |
| Serving | `/server` | `generation rotate --reason <текст>` | `umir_serving_owner` | `stack.sh restore` | до запуска реплик |
| Serving | `/server` | `healthcheck` | нет | healthcheck Compose | проверяет `/readyz` |
| Feedback | `/feedback` | `feedback migrate` | `umir_feedback_owner` | задание `migrate-feedback` | вызывается с `entrypoint: [/feedback]`, `command: [migrate]` |
| Feedback | `/server` | `server serve` | `umir_feedback` | сервис `feedback` (профиль `feedback`) | запуск без аргумента больше не принимается |
| Feedback | `/feedback` | `feedback generation rotate --reason <текст>` | `umir_feedback_owner` | `stack.sh restore` | админ-команды `export`, `delete`, `purge`, `status`, `retry` остаются |
| Feedback | `/server` | `server healthcheck` | нет | healthcheck Compose | проверяет `/readyz` |
| Gateway | `/server` | `serve` | нет (базы нет) | сервисы `gateway-a`, `gateway-b` | явный синоним запуска по умолчанию (W1.11); `migrate` и `generation rotate` нет |
| Gateway | `/server` | `healthcheck` | нет | healthcheck Compose | проверяет `/readyz` |
| Frontend | `nginx` | нет подкоманд | нет | сервис `frontend` | проверка `wget -q -O /dev/null http://127.0.0.1:8080/readyz` |

Роли Postgres (C8). Одна база и две роли на сервис с состоянием:

| Сервис | База | Роль владельца и миграций | Роль среды выполнения |
| --- | --- | --- | --- |
| Auth | `auth` | `umir_auth_owner` | `umir_auth` |
| Warehouse | `warehouse` | `umir_warehouse_owner` | `umir_warehouse` |
| Serving | `serving` | `umir_serving_owner` | `umir_serving` |
| Feedback | `feedback` | `umir_feedback_owner` | `umir_feedback` |

Роль среды выполнения не суперпользователь, не владелец таблиц, `NOBYPASSRLS`, без `CREATE` в схеме. Роль-владелец используют только `migrate` (C8) и `generation rotate` (C9); административные роли для `pg_dump` и `pg-roles` — вне сервисов. Порядок восстановления в `stack.sh restore`: остановить реплики, `pg_restore`, `generation rotate` у Auth, Warehouse, Serving, Feedback, затем `redis-init` (пишет маркер limiter, как при каждом `up`) и запуск. Сам `restore` строгий режим R7 не включает: он включается только когда маркер пропал во время работы или не равен настроенному значению (раздел 6).

## 4. Здоровье и готовность

У каждого сервиса два адреса (C32). `GET /healthz` — процесс жив, без зависимостей. `GET /readyz` — основная возможность готова. Подпутей `/readyz/*` и параметров запроса нет. Оба адреса отвечают без сервисного ключа (C52) и не содержат секретов и адресов.

Тело `/readyz`: `{instance, generation, capabilities}`, где `capabilities` — объект «имя → `ok`, `degraded` или `down`». Код 200, когда готовы обязательные возможности сервиса из таблицы, иначе 503. Остальные возможности только показывают состояние и код не меняют. Возможность отключает только свои маршруты, а не весь сервис. Недоступные Redis или RabbitMQ оставляют сервис живым: зависимые возможности отвечают 503 до отправки запроса владельцу. Правила C49: у Gateway и Frontend поля generation нет; Gateway отвечает 200, когда готовы `http` и `limiter`, а `events` при недоступности даёт `degraded` и готовность не снижает. Правило C42: у Frontend тело — `{"capabilities":{"static":"ok"}}`, без instance и generation.

Имена возможностей — строчные латинские буквы, цифры, `_` и `.`; точка отделяет канал (`delivery.smtp`). Имена ниже — контракт: группа добавляет новую возможность через CR.

| Сервис | Обязательные для 200 | Все возможности | Тело `/readyz` | Healthcheck Compose |
| --- | --- | --- | --- | --- |
| Auth | `authorize` | `jwks` (только ключ подписи, без БД), `authorize`, `admission`, `registry`, `mail`, `reconcile` | `{instance, generation, capabilities}` | `/server healthcheck` по `/readyz` на приватном TLS-листенере `8085` без сервисного ключа (C52, C63) |
| Gateway | `http`, `limiter` | `http`, `auth`, `events`, `limiter`, `scheduler` | `{instance, capabilities}` | `/server healthcheck` по `/readyz` |
| Warehouse | `journal` | `journal`, `outbox`, `captures` | `{instance, generation, capabilities}` | `/server healthcheck` по `/readyz` |
| Serving | `postgres`, `schema` | `postgres`, `schema`, `auth_jwks`, `events` | `{instance, generation, capabilities}` | `/server healthcheck` по `/readyz` |
| Feedback | `intake` | `intake`, `delivery.smtp`, `delivery.telegram`, `outbox`, `lifecycle`, `retention` | `{instance, generation, capabilities}` | `/server healthcheck` по `/readyz` |
| Frontend | `static` | `static` | `{"capabilities":{"static":"ok"}}` | `wget` по `/readyz` |

Смысл возможностей, влияющих на поведение:

- Auth: `jwks` не зависит от Postgres и остаётся `ok` при его простое; `admission` при отказе даёт 503 до действия на входе и выдаче кодов, остальное работает; `mail` отказ только копит возраст очереди.
- Gateway: `limiter` — Redis отвечает и непрерывность подтверждена; без него защищённые операции получают 503 до отправки; `events` — потребитель live подключён, при отказе новые SSE получают 503; `scheduler` — лидер, лаг, сбои, на код не влияет.
- Warehouse: `outbox` (брокер, лаг) и `captures` (Redis cache) могут быть `degraded` без влияния на код; ответ `runtime` зависит только от `journal`.
- Serving: `down` у `postgres` или `schema` даёт 503; `auth_jwks` и `events` — `degraded` без 503.
- Feedback: приём (`intake`) не зависит от брокера; `delivery.<канал>` на код не влияет, сбой провайдера готовность не снижает.

Healthcheck Compose всегда смотрит `/readyz`, а не Redis или RabbitMQ целиком. Готовность стека — `up --wait`. Порядок зависимостей между сервисами строится по `service_healthy` (раздел 5).

## 5. Порядок запуска и задания

Проект `umir-durable-*`, перезапуск `unless-stopped`. Порядок (W3.1):

1. Хранилища: `postgres`, `rabbitmq`, `redis-limiter`, `redis-cache`.
2. Одноразовые задания, идемпотентные при каждом `up` и `restore`: `secrets-init`, затем `pg-roles`, `mq-init`, `redis-init`.
3. `migrate-auth`, `migrate-warehouse`, `migrate-serving`, `migrate-feedback` (при профиле `feedback`).
4. `init-auth` (после `migrate-auth`).
5. `auth`, затем `warehouse`, затем `serving-a` и `serving-b`, затем `gateway-a` и `gateway-b`, затем `frontend` (строго после готовности Gateway), `feedback` (профиль `feedback`) — после Auth.

| Задание | Что делает | Учётные данные | Условие повтора |
| --- | --- | --- | --- |
| `secrets-init` | `scripts/durable/secrets.sh ensure`: ключи, DSN, URL, CA, конфигурация Auth, `owner.json` v2, `config/limiter-generation` | пишет в тома `umir-secrets`, `auth-owner-secrets` | не перезаписывает существующее |
| `pg-roles` | создаёт базы и две роли на сервис, `REVOKE ALL FROM PUBLIC` | административная роль Postgres; пароль не в командной строке | идемпотентно |
| `mq-init` | объявляет обменники `umir.events`, `umir.auth`, `umir.tasks`, `umir.dlx`, durable-очереди с `.retry` и `.dlq`; `feedback.auth` только при профиле `feedback`; права пользователей по шаблонам имён ([events.md](events.md)) | административный пользователь RabbitMQ | идемпотентно; сервисы объявляют только свои exclusive-очереди |
| `redis-init` | пишет маркер `umir:limiter:generation` из `config/limiter-generation` | пользователь `redis-init` на `redis-limiter` | идемпотентно, при каждом `up` и `restore` |
| `migrate-<svc>` | `migrate` под ролью-владельцем (раздел 3), затем `VerifySchema` в рантайме | только DSN `<SVC>_MIGRATE_DATABASE_URL_FILE` | завершается; повтор безвреден |
| `init-auth` | `init` и `keys check` | DSN `umir_auth`, `owner.json` только чтение, файл пароля | повтор ничего не меняет |

Сетевые псевдонимы и экземпляры. Каждой реплике уникальный `*_INSTANCE_ID` (C19), равный имени сервиса Compose, если не указано иное:

| Сервис Compose | Псевдоним сети | Идентификатор экземпляра | Примечание |
| --- | --- | --- | --- |
| `auth`, `auth-b` (профиль `auth-ha`) | `auth` | `auth`, `auth-b` | у Auth идентификатор — поле `instanceId` конфигурации, см. раздел 8 |
| `warehouse` | `warehouse` | `warehouse` | одна реплика |
| `serving-a`, `serving-b` | `serving` | `serving-a`, `serving-b` | обе реплики за одним адресом |
| `gateway-a`, `gateway-b` | `gateway` | `gateway-a`, `gateway-b` | псевдоним `gateway` у обеих реплик в сети `front` (C20); frontend резолвит его во все A-записи (`upstream gateway:8080 resolve`) |
| `feedback` | `feedback` | `feedback` | профиль `feedback` |
| `frontend` | `frontend` | нет | единственный опубликованный порт |

Идентификатор экземпляра не используется для сравнения эпох и не появляется на проводе, кроме `epoch` SSE и курсора Gateway, куда Gateway кладёт случайный boot-nonce независимо от `INSTANCE_ID` (R2, C19): рестарт реплики с тем же идентификатором старые курсоры не оживляет.

Сети: `data` (internal: хранилища и их клиенты), `default` (internal), `front` (internal: `gateway-a`, `gateway-b`, `frontend`), `mail`, `edge`. Опубликован только `frontend`. Тома: `pg-data`, `mq-data`, `redis-limiter-data`, `umir-secrets`, `auth-owner-secrets`; тома FIN1 не упоминаются. Профили Compose: `feedback`, `auth-ha`, `ops` (management RabbitMQ на loopback), `faults`.

## 6. Топология Redis

Redis не источник истины и в бэкап не входит. Два экземпляра с TLS и ACL-файлом, пользователь на сервис и только на свой префикс (U3, C17):

| Экземпляр | Персистентность | Вытеснение | `maxmemory` | Клиенты |
| --- | --- | --- | --- | --- |
| `redis-limiter` | AOF `everysec`, том `redis-limiter-data` | `noeviction` | 128mb | Gateway, задание `redis-init` |
| `redis-cache` | нет (`--save ""`, `--appendonly no`) | `allkeys-lru` | 512mb при старте (C48), уточняется замером R13 | Warehouse |

Ключи имеют вид `umir:<owner>:<kind>:…`. Владельцы: `gw` (Gateway) и `wh` (Warehouse). У Auth Redis нет, префикса `umir:auth:` нет (C51); у Serving и Feedback Redis нет.

| Ключ | Владелец | Экземпляр | Содержимое |
| --- | --- | --- | --- |
| `umir:gw:adm:<измерение>` | `gw` | `redis-limiter` | счётчик фиксированного окна одного измерения допуска (ключ измерения содержит HMAC субъекта от `GATEWAY_ADMISSION_KEY_FILE`), TTL окна |
| `umir:gw:admid:<политика>:<sha256(ID)>` | `gw` | `redis-limiter` | запись допуска: хранит sha256 отпечатка, повтор с тем же `ID` не списывает дважды; TTL окна политики |
| `umir:gw:sse:s:<HMAC(сессия)>`, `umir:gw:sse:o:<организация>`, `umir:gw:sse:cell` (и `:u` у ключей с объёмом) | `gw` | `redis-limiter` | ZSET аренд SSE (квоты) |
| `umir:gw:expiry:lease`, `umir:gw:expiry:fence` | `gw` | `redis-limiter` | аренда планировщика просрочки и счётчик fence |
| `umir:wh:cap:{generation}:{restaurantID}:{captureID}` | `wh` | `redis-cache` | сжатый отчёт захвата страниц, TTL 5 мин, значение до 16 MiB |
| `umir:wh:capslots:<generation>:<restaurantID>`, `umir:wh:capbytes:<generation>:<restaurantID>` (и `:u`) | `wh` | `redis-cache` | слоты захватов: число и объём (`Slots` эталона); покрыты шаблоном `umir:wh:*` |
| `umir:limiter:generation` | общий | `redis-limiter` | маркер непрерывности limiter |
| `umir:limiter:strict_since` | общий | `redis-limiter` | момент начала строгого окна |

Маркер limiter (R7, C17, C47):

1. При каждом `up` и `restore` `redis-init` пишет `umir:limiter:generation` значением из `config/limiter-generation`. Сервисы значение не создают при штатном старте, а только сравнивают его с `GATEWAY_LIMITER_GENERATION`: расхождение или отсутствие маркера включает строгий режим R7.
2. Если маркер пропал во время работы, сервис ставит `SET umir:limiter:strict_since <now> NX`; все реплики отсчитывают строгое окно от этого общего момента.
3. Когда окно (самое длинное окно политики, не больше 1 ч) прошло, любой сервис восстанавливает `SET umir:limiter:generation <настроенное значение> NX` и удаляет `strict_since`. Сторожа нет.
4. Любая ошибка Redis или сомнение в непрерывности — это отказ допуска (503 до отправки), а не «разрешено».

ACL (W2.1). Пользователь получает только свои ключи и только нужные команды: допуск, аренды, кеш, скрипты через `+evalsha` и `+script|load` (`+eval` не выдаётся: эталон его не вызывает, C61). Команды подключения go-redis (`HELLO`, `PING`, `CLIENT SETINFO` и `CLIENT SETNAME`) разрешены без ключей. Другие подкоманды `CLIENT`, включая `CLIENT KILL`, запрещены. Внутри скриптов эталон использует `GET`, `SET`, `DEL`, `INCR`, `EXISTS`, `PTTL`, `PEXPIRE`, `TIME`, `ZADD`, `ZREM`, `ZRANGEBYSCORE`, `ZSCORE`, `ZCARD`, `HSET`, `HGET`, `HVALS`, `HDEL` только на ключах пользователя. Запрещены `FLUSHALL`, `FLUSHDB`, `CONFIG`, `KEYS`, `DEBUG`, `SHUTDOWN`; в тестах `FLUSH*` тоже запрещены.

| Пользователь | Экземпляр | Шаблоны ключей | Особенность |
| --- | --- | --- | --- |
| Gateway | `redis-limiter` | `umir:gw:*`, плюс ровно два ключа `umir:limiter:generation` и `umir:limiter:strict_since` | исключение к C17 (C61): на этих двух ключах только `GET`, `SET` (с `NX`) и `DEL`; запись маркеров нужна для C47 |
| Warehouse | `redis-cache` | `umir:wh:*` | Redis limiter ему не выдаётся |
| `redis-init` | `redis-limiter` | `umir:limiter:generation`, `umir:limiter:strict_since` | только маркеры |
| проверка здоровья | оба | нет ключей | `PING` с учётными данными из файла |

## 7. Параметры развёртывания

- R11 и C34. Две переменные, обе выставляет Factory и проверяет `stack.sh preflight`: `UMIR_TRUSTED_PROXY` у Frontend (CIDR NPM, значение — параметр `COMPLETION_NPM_ADDR`) и `GATEWAY_TRUSTED_PROXY_CIDRS` у Gateway (значение — `UMIR_FRONT_SUBNET`). У сети `front` явная подсеть, не пересекающаяся с ZeroTier и docker0. Nginx перезаписывает `X-Forwarded-For` и доверяет входящему только от NPM; Gateway принимает его только от подсети `front`.
- R12. Требование к часам: одна ВМ либо синхронизация часов; допуск `iat` в сервисных токенах нулевой. `stack.sh preflight` сравнивает часы контейнеров и хоста. Требование записано в README.
- TLS. Prod-подключения к Postgres, RabbitMQ и Redis идут с проверкой имени сертификата: DSN с `sslmode=verify-full` и `sslrootcert`, `amqps://`, `rediss://`; общий частный CA на 10 лет, листья на 1 год. Тестовая инфраструктура работает без TLS.
- Логи Postgres, RabbitMQ и Nginx без ПДн; срок логов Nginx — 14 дней (`stack.sh logs-prune`).
- Проверки `stack.sh preflight`: часы (R12), свободные порты, права секретов, отсутствие запрещённых переменных раздела 1 в окружении, значения `UMIR_TRUSTED_PROXY` и `GATEWAY_TRUSTED_PROXY_CIDRS`.

## 8. Расхождения и открытые вопросы (для CR)

Расхождения между подспеками, главной спекой и кодом. Контракт выше принимает решение, указанное в каждой строке; группа возражает через CR.

1. Ключ дайджеста Feedback. Главная спека C14 и factory.md W2.1 пишут `FEEDBACK_DIGEST_KEY`, feedback.md 5.5 — `FEEDBACK_DIGEST_KEY_FILE`. Контракт: `FEEDBACK_DIGEST_KEY_FILE` (правило `_FILE`).
2. Проверка ключей Auth. C6 пишет `auth keys check`, auth.md 5.10 — подкоманда `keys check` бинарника `/server`. Контракт: `/server keys check`.
3. Имя бинарника Serving. serving.md (раздел про generation) пишет `umir-serving generation rotate`, но образ собирает `/server` с `ENTRYPOINT ["/server"]`. Контракт: `/server generation rotate --reason`. Serving правит текст или переименовывает бинарник через CR.
4. `maxmemory` у `redis-cache`. Решено (C62): 512mb (C48), слоты и захваты делят один пул `allkeys-lru`; замер R13 это учитывает.
5. Limiter у Warehouse. factory.md R7 и W3.1 допускают `WAREHOUSE_LIMITER_GENERATION`, «если Warehouse пользуется limiter», а warehouse.md 5.8 говорит: лимитера у Warehouse нет. Контракт: переменной и пользователя на `redis-limiter` нет. Если лимитер появится, нужен CR.
6. Владельцы ключей Redis. C17 перечисляет владельцев `auth`, `gw`, `wh`, а C51 снимает Redis у Auth. Контракт: `gw` и `wh`, префикса `umir:auth:` нет; поле JSON с limiter generation у Auth, упомянутое в C17, не вводится.
7. Auth настраивается JSON-полями, а не переменными. factory.md §6 и C10 перечисляют единые имена `<SVC>_DATABASE_URL_FILE`, `<SVC>_MIGRATE_DATABASE_URL_FILE`, `_AMQP_URL_FILE`, `_INSTANCE_ID`, но auth.md 5.10 задаёт `database.urlFile`, `database.migrationUrlFile`, `amqp.urlFile`, `instanceId`. Контракт: у Auth остаются JSON-поля (переменной `AUTH_DATABASE_URL_FILE` нет). Следствия, которые нужно подтвердить в auth.md: (а) двум репликам нужны разные файлы с `instanceId`, то есть `secrets.sh` пишет конфигурацию на реплику; (б) `database.migrationUrlFile` не должно быть в конфигурации реплики `serve`, поэтому для `migrate-auth` и `generation rotate` нужен отдельный файл конфигурации; (в) имя поля с путём CA брокера и имя каталога `owner.json` в новой схеме (сейчас `secretsDir`) auth.md не называет.
8. Идентификаторы экземпляров Auth. auth.md пишет `auth-a`, `auth-b`, а Compose factory.md W3.1 называет сервисы `auth` и `auth-b`. Контракт: идентификатор равен имени сервиса Compose (`auth`, `auth-b`); группа Auth принимает или Factory переименует сервис. Формат идентификатора никто не задал; предложение: непустая строка из строчных латинских букв, цифр и `-`, до 63 символов (он входит в имена очередей RabbitMQ), уникальная в ячейке.
9. Секреты Feedback не всегда файлами. Решено (C63): `FEEDBACK_SMTP_USERNAME`, `FEEDBACK_TELEGRAM_BOT_TOKEN`, `FEEDBACK_TELEGRAM_PROXY_USERNAME`, `FEEDBACK_TELEGRAM_PROXY_PASSWORD`, `FEEDBACK_GATEWAY_KEY` и `FEEDBACK_AUTH_DISCOVERY_KEY` получают варианты `_FILE` в волне 2 Feedback (волна 1 опубликована без них). До этого тест «нет секретов в окружении» для Feedback не включается.
10. Ключ чтения реестра Auth для Feedback (C50). Новое имя не определено. Контракт предполагает существующий сервисный ключ Feedback (`FEEDBACK_AUTH_DISCOVERY_KEY`, на стороне Auth `serviceKeyFiles.feedback`); Auth добавляет Feedback в читатели реестра в волне 2.
11. Healthcheck Auth. Решено (C63): подкоманда `/server healthcheck` ходит на `/readyz` приватного TLS-листенера `8085`, который отвечает на `/healthz` и `/readyz` без сервисного ключа (C52). Auth добавляет подкоманду в волне 2.
12. `healthcheck` в коде проверяет `/healthz`. В ветках волны 1 подкоманды `healthcheck` Warehouse, Serving и Feedback ходят на `/healthz`, образ Frontend — тоже; `healthcheck` Gateway уже выбирает `/readyz` в режиме с Auth и `/healthz` в legacy-режиме. По C32 Compose должен смотреть `/readyz`; группы переключают это в волне 2, и тест Compose Factory проверяет итог.
13. Обязательные возможности. C32 говорит об одной «основной возможности», но Serving требует две (`postgres`, `schema`), а Gateway две (`http`, `limiter`, C49). Контракт называет колонку «обязательные для 200». Стиль имён тоже разный (`auth_jwks` у Serving, `delivery.smtp` у Feedback); контракт принимает оба как есть. Состояние `limiter` в строгом режиме R7 (`ok` или `degraded`, и отвечает ли `/readyz` Gateway 200 в окне строгости) в gateway.md не определено.
14. ACL маркеров limiter. Решено (C61): исключение к C17. Gateway получает `GET`, `SET` (с `NX`) и `DEL` ровно на `umir:limiter:generation` и `umir:limiter:strict_since`; скрипты через `+evalsha` и `+script|load`, `+eval` не выдаётся. Тест ACL проверяет отказ на чужих ключах и на `FLUSH*`, `CONFIG`, `KEYS`, `DEBUG`, `SHUTDOWN`.
15. `GATEWAY_LIMITER_GENERATION` не секрет. C14 включает limiter generation в список секретов бэкапа, а gateway.md определяет `GATEWAY_LIMITER_GENERATION` обычной переменной (не `_FILE`). Контракт: переменная несекретная, значение берётся из `config/limiter-generation`; тест Compose не считает её секретом.
16. Обязательность необязательных URL. warehouse.md допускает работу без Redis cache, serving.md — без брокера (деградация). Контракт: в durable-профиле Factory задаёт `WAREHOUSE_REDIS_CACHE_URL_FILE` и `SERVING_AMQP_URL_FILE` всегда.
17. CA для `amqps://` и `rediss://`. Только Auth имеет поле для CA брокера. Для остальных сервисов способ не определён; предложение: `SSL_CERT_FILE` на общий CA, как во FIN1, и `sslrootcert` в DSN Postgres.
18. `UMIR_SMTP_*_FILE`. factory.md W2.1 называет `UMIR_SMTP_USERNAME_FILE` и `UMIR_SMTP_PASSWORD_FILE`, а Auth читает `smtpUsernameFile` и `smtpPasswordFile` из JSON. Контракт: `UMIR_SMTP_*_FILE` — входы `secrets.sh`, не переменные сервиса.
19. `peerKeyFiles.gateway`. factory.md убирает `peerURLs.gateway` (R3); `peerKeyFiles.gateway` в подспеках не упомянут, контракт убирает его тоже. Auth подтверждает.
20. Числовые настройки Feedback (сроки, квоты) обязательны без умолчаний. Решено G1-2: 30 / 180 / надгробие 180 дней, квоты 150 000 анонимных и 5 000 на ресторан; сборка задаёт эти значения.
21. Конфликт имён ключей. Имена `AUTH_SERVICE_KEY_FILE` и `SERVING_SERVICE_KEY_FILE` встречаются у Warehouse и у Gateway с разным смыслом («ключ этого сервиса для вызова Auth» и «ключ Gateway для вызова Serving»); различие — файл, который Factory монтирует каждому. Контракт не меняет имён (по подспекам), но `secrets.sh` ведёт мапу «имя → файл» на сервис.
