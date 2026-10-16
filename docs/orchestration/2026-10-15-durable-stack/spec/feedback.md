# Подспека G-feedback: umir-feedback на PostgreSQL + RabbitMQ

Приведено к решениям C1–C40 главной спеки (раздел 10), 15.10.2026.

Дата: 15.10.2026, Europe/Moscow. Статус: черновик для ревью. Главная спека: [00-orchestration.md](00-orchestration.md) (U1–U8, R1–R20, разделы 5, 6, 8, 9, 10). Здесь только то, что специфично для Feedback; общее не повторяется. План (issue): `scratchpad/plans/umir-feedback-5.md`; ревью: `scratchpad/main-review/feedback.md` (в тексте «ревью №N»); опись заготовок: `scratchpad/main-review/stalled-persistence.md`.

## 1. Цель и границы

Feedback принимает заявки (анонимные и ресторанные), хранит согласие и доставляет два уведомления на заявку: SMTP оператору и Telegram через VPN-прокси. Цель группы: убрать единственного писателя (JSONL-журнал под `flock`), перевести состояние в PostgreSQL, доставку на строку задания с RabbitMQ как транспортом-будильником (R18), ввести сроки хранения и стирание по scope (R9, R17), квоты, наблюдаемость и раздельную готовность. Сервис остаётся одной репликой (U7), но без состояния в процессе: тесты гоняют две реплики на одной базе.

Владеет: приёмом и идемпотентностью заявок, согласием, заданиями SMTP/Telegram и их попытками, сроками и стиранием своих данных, SMTP/Telegram/VPN-адаптерами, проверкой JWT Auth (по публичным ключам JWKS).
Не владеет: допуском и лимитами публичной формы (Gateway, R10), жизненным циклом ресторанов и организаций (Auth публикует, Feedback потребляет, R17), письмами регистрации и OTP (Auth), compose, секретами, ролями БД, учётными записями брокера, нагрузкой и бэкапом (G-factory), текстом политики конфиденциальности (G-frontend, R19). Запрет на импорт чужих модулей остаётся (`AGENTS.md`).

## 2. Исходное состояние

База: `origin/main` `fa59b63` (запиши свежий SHA в журнал). Код читался на этом SHA в `_wt/review/umir-feedback`.

| Область | Пути |
| --- | --- |
| Хранилище сейчас | `internal/adapters/journal/journal.go` (1599 строк, `flock` на `feedback.lock`, один мьютекс, `Open` :105, `Accept` :225, `Claim` :295, `FinishScoped` :348, `Purge` :421, `DeleteEmail` :399, `Retry` :461, `Status` :442, `Ready` :495); `legacy_challenge*.go`, `history_observation.go`, `scoped.go` (`Observe`, `ListScoped`) |
| Порты | `internal/application/ports.go` (`Repository`, `ScopedRepository`, `Sender`, `Clock`, `Jitter`, `Normalizer`), `intake.go` (отпечаток, `legacyFingerprint`), `worker.go` (`ProcessOne`) |
| Запуск | `bootstrap/runtime.go` (`Run`, `RunOwner`, `runWorker` :96: ошибка воркера гасит весь процесс), `bootstrap/command.go` (CLI `export|delete|purge|status|retry`), `internal/adapters/runtime/config.go` |
| HTTP | `internal/adapters/httpapi/scoped.go` (v2; процессный кеш курсоров `pages` на 1024 страницы, 5 минут), `health.go` (`/readyz` = `store.Ready`), `auth.go` (JWT, `iat` без допуска), `feedback.go` (унаследованный `/feedback`) |
| Ключи Auth | `internal/adapters/authkeys/client.go` (`Key` :50 держит `c.mu` :54 на время сетевого `refresh` :76/:90; кеш 30 с; при ошибке отказывают и известные ключи) |
| Доставка | `internal/adapters/delivery/smtp.go` (`Reply-To` из адреса формы :254/:288), `telegram.go`, `internal/adapters/vpn/runtime.go` (`defaultRefresh` 24 ч :22, первая загрузка :89, `refreshTicker` :144, `refreshTunnel` :200) |
| Тесты | `internal/adapters/journal/*_test.go` (около 4,2 тыс. строк тестов в пакете и фикстурах), `tests/integration/feedback_test.go`, фикстуры `tests/fixtures/{protocol,server,control}` (RPC1.1.0, работают на `journal.Store`), `tests/container/feedback-smoke.sh` |
| Инструменты | `Makefile` без цели `integration`; `.go-arch-lint.yml` без vendor-записей для pgx, AMQP |

Заготовки prod-readiness (читать, не менять; `_wt/prod/umir-feedback`, ветка `feat/prod-readiness`):

| Что | Откуда | Судьба |
| --- | --- | --- |
| `domain/validate.go` (`ValidateSubmission`, `ValidTransition`, `ValidFinish`, `ValidJobTimeline`, `SameContent`, `RetryCycleStart`, `MoscowLocation`) | закоммитировано, `277f0e6` (объект есть в общем репозитории: `git cherry-pick` или `git checkout 277f0e6 -- internal/domain/validate.go`) | взять в волне 1 без изменения поведения |
| `internal/adapters/postgres/{pool,migrate,store,rows}.go`, `migrations/0001_init.sql` (около 840 строк, не закоммичено) | рабочая копия | основа волны 2, приводится к R15 и R8 (см. раздел 5, список отличий) |
| `tests/storecontract/contract.go` (496 строк, 14 кейсов, `Harness{Open,Reopen}`) | рабочая копия | основа контрактного набора; 14/14 проходили на живом Postgres |
| `ErrLeaseLost` и тест воркера (`worker_test.go`) | рабочая копия | взять (волна 1) |
| `domain.Record` | рабочая копия | не брать (нужен был только импорту, R1) |
| `go.mod`: `pgx v5.11.0` | рабочая копия | версию сверить с эталоном R15 |

Отличия заготовки от целевого вида, которые нужно устранить: нет RLS и разделения ролей; миграции идёт той же ролью, что и работа; колонка `legacy` и ключ-строка `app:…` вместо типизированных колонок; нет аренды с fencing (чужой `Finish` отсекается только по числу попыток; после `Retry` попытки обнуляются, и устаревший воркер может совпасть); нет outbox, inbox, квот, надгробий, стирания, поколения хранилища; `Status` читает всё; `Retry` только для анонимных; свой `inTx`/`migrate` вместо эталона; свои роли (`feedback_migrator`/`feedback_app`), свои значения `umir.actor` (`http-app`/`http-anonymous`) и свои формы аренд и outbox вместо эталонных (C4, C5, C8).

## 3. Решения

**Принимается из issue как есть.** P1 (убрать единственного писателя), P3 (заявка, два задания и outbox в одной транзакции; публикация только после коммита), P5 (сигналы о ёмкости и давлении диска, принятая работа не теряется), P6 (JWKS без замка на сетевом вызове), P7 (видимость сбоев доставки), требования к полной проверке пространства имён в каждом запросе, к отсутствию «чужого» UUID, к `mandatory`+publisher confirms, к тому, что внешняя доставка at-least-once и fencing не даёт exactly-once, к отсутствию ПДн в брокере и логах, VPN: повтор с backoff без прямого выхода в сеть, тесты только с синтетическими провайдерами.

**Меняется правилами.**
- R1: см. ниже «Снято».
- R2: у Feedback нет `runtimeEpoch` на проводе. Вводится внутреннее поколение хранилища `storage_meta.generation`, стабильное при рестарте и смене реплики. Оно создаётся первой миграцией (`gen_random_uuid()`, C9); `serve` без него не стартует и сам его не создаёт. Меняется командой `feedback generation rotate --reason <текст>` (только при остановленном сервисе и под ролью-владельцем) после восстановления более ранней истории (шаг runbook Factory). Идёт в сообщения и конверты outbox (`generation`) и в тело `/readyz` (C32), не в публичный HTTP-контракт.
- R8, C5, C8: RLS обязателен. Роли по factory: `umir_feedback_owner` (владелец таблиц, только задание `migrate-feedback`) и `umir_feedback` (среда выполнения, не владелец, без `BYPASSRLS`); сервис своей миграции не запускает, DSN владельца не получает. При старте вызывается `AssertRuntimeRole` эталона (роль не суперпользователь и не владелец). Контекст ставит эталонная обёртка: `InScopedTx(Scope{Actor, OrgID, RestaurantID, UserID})` и `InSystemTx(ctx, reason)`; GUC `umir.actor` (`tenant`, `user`, `anonymous`, `system`), `umir.org_id`, `umir.restaurant_id`, `umir.user_id`. Feedback использует `tenant` (ресторанная заявка; прежнее `http-app`), `anonymous` (прежнее `http-anonymous`) и `system`; своих GUC нет.
- R9, C23, C24: сроки из таблицы раздела 6 п. 4 главной спеки: анонимные 30 дней, ресторанные 180 дней или 7 дней после `auth.restaurant.deleted`/`auth.organization.deleted`, надгробие-дайджест 180 дней, текст и телефон — после `sent` обоих заданий (при `failed` до срока обращения), outbox 7 дней, inbox 14 дней, курсоры 5 минут. Значения конфигурируются (разд. 5), утверждаются на G1; до G1 в коде нет значений по умолчанию.
- R12: допуск на `iat` остаётся нулевым, пункт issue про «ограниченный допуск» не принимается; ревью №8 закрывается записью требования одной ВМ в README и явным тестом границы. Правка кода не делается.
- R15, C4: адаптеры Postgres и RabbitMQ копируются из `umir-factory reference/durable@<sha>` в `internal/adapters/{postgres,rabbitmq}` с комментарием-источником; заготовка Feedback сверяется с эталоном, расхождения идут в пользу эталона. Аренды фоновых работ (`worker_leases`), outbox и inbox берутся в форме эталона без своих столбцов. Redis-адаптер не копируется.
- R17, C1, C45: потребляются только `auth.restaurant.deleted` и `auth.organization.deleted` из `umir.auth` (очередь `feedback.auth`, существует только при включённом профиле Feedback); `deleting` и остальные события Auth не нужны. События Auth — подсказка: источник истины для стирания — реестр Auth, с которым Feedback сверяет свои scope (разд. 5.3, п. 8).
- R18: строка `feedback_jobs` — истина. Очереди `feedback.smtp` и `feedback.telegram` (обменник `umir.tasks`, C2) — будильник с `.retry` (TTL 30 с) и `.dlq`, `x-delivery-limit` 5; потеря сообщения не теряет задание (сверка, разд. 5).
- R20, C10: в `cmd/server` и `bootstrap` нет режима `memory`, переменной `FEEDBACK_STORAGE` и запасного пути. Память живёт только в тестовом пакете (разд. 5, «Удаляемый и новый код»). Проверка «в проде нет memory» — тест сборки: бинарник без DSN не стартует.

**Решения группы (с обоснованием).**
- Redis не используется. Курсоры становятся бесстатусными (keyset), ключи JWKS кешируются в процессе (публичные, одна проверка на реплику), лимиты у Gateway, аренды фоновых работ лежат в Postgres (`worker_leases` эталона, C4). Redis добавил бы второй источник отказа без выигрыша.
- Inbox эталона нужен только потребителю событий жизненного цикла Auth (меняет состояние, требует дедупликации по `eventID`). Сообщение-будильник доставки идемпотентно (потребитель делает один `Claim`, которому защита — `SKIP LOCKED` и fencing), inbox ему не заводится.
- Почта не из ASCII. Из двух предложенных вариантов выбираю ни тот, ни другой в чистом виде. SMTPUTF8 не согласуется: стандартный `net/smtp` его не поддерживает, а получатель — один оператор с неизвестным реле, поддержку не проверить без реального провайдера; отвергать при приёме нельзя без потери законных адресов `…@пример.рф`. Адрес пользователя попадает только в заголовок `Reply-To` и в тело письма (конверт `MAIL FROM`/`RCPT TO` берётся из конфигурации). Решение: приём не меняется (`ValidEmail` как есть, публичный контракт нетронут), заголовок `Reply-To` ставится только для адреса целиком из ASCII; иначе заголовок опускается, адрес остаётся в теле (уже base64 UTF-8). Письмо больше не получает постоянный отказ 5xx, оператор отвечает вручную из тела. Если оператору нужен автоматический ответ на IDN, это отдельное решение (punycode домена с `x/net/idna` и отказ для не-ASCII локальной части).
- Квоты по пространствам имён: счётчик живых (не стёртых) строк на `anonymous` и на ресторан; превышение даёт тот же `503 FeedbackStorageUnavailable`, что и сегодняшняя нехватка ёмкости (новых кодов на проводе нет). Предложение до G1: анонимные 150 000 (Gateway пропускает до 200 в час, то есть до 144 000 за 30 дней), ресторан 5 000.
- «Payload доставки» (R9, закрыто C24): у Feedback отдельного носителя нет, SMTP читает строку заявки. `message` и `phone` очищаются, когда оба задания в `sent`; при `failed` остаются до конца срока (иначе администратор не сможет сделать `retry`, а ревью №3 как раз о потерянном письме). Имя, адрес и согласие хранятся до `purge_at` как доказательство согласия.
- Отпечаток заявки (`fingerprint`) хранится как HMAC-SHA256 от обычного отпечатка с ключом `FEEDBACK_DIGEST_KEY_FILE`, всегда (и в живых строках, и в надгробиях), поэтому надгробие-дайджест «keyed, не plain hash» и ветка проверки повтора одна. Секрет входит в список factory и в бэкап секретов (C14, разд. 6).
- Унаследованный `/feedback` (анонимный, без Auth) удаляется в волне 2 вместе с файловым журналом (C46): legacy-профили закреплены на старых образах, их это не ломает. До этого, в волне 1, в режиме `FEEDBACK_AUTH_URL` он отвечает 503 (ревью №12), это фиксируется в README.

**Снято R1 (чистый лист, ничего не переносится).** Импортёр JSONL, проверка счётчиков и контрольных сумм импорта, перенос и «скраб» архива OTP-челленджей (архив остаётся только в старых томах, которые новый стек не монтирует), версии записей 1 и 2, `Legacy`, `legacyFingerprint`, детерминированные миграционные UUID заданий, `ObserveHistory`, `legacy_challenge*`. Файловый журнал из прода удаляется целиком (`internal/adapters/journal`), `FEEDBACK_STORE_PATH`, `FEEDBACK_MAX_STORE_BYTES`, `FEEDBACK_RETENTION_DAYS`, `feedback.lock`, уплотнение и ёмкостные расчёты уходят вместе с ним. Остаётся: runbook чистого листа и откат на старые образы и тома до первой записи нового стека (G-factory).

**Вне объёма.** Лимиты и 429 публичной формы (Gateway); создание ролей, баз, vhost, секретов, compose (Factory); публикация событий Auth; текст политики (R19); OTP и закрытие issue #2; реальные провайдеры; HA брокера и базы; закрепление базового образа `golang` по digest и логи `xray` (ревью №12, низкий приоритет); измерение размера записи и скорости заполнения (делает приёмка Factory).

## 4. Волна 1 — независимые правки

Ветка `fix/durable-w1-feedback`. Не трогает контракты DUR1, не зависит от Postgres и RabbitMQ. Каждая правка: сначала красный тест, затем код. Проверки: `GOWORK=off GOTOOLCHAIN=go1.26.9 make check` (после `make tools` в worktree) и `make quality-test`. `make integration` в волне 1 не требуется: цели в репозитории нет, а правило C37 требует её только там, где она уже объявлена (цель появляется в волне 2).

**W1-0. Подготовка без изменения поведения.** Перенести `domain/validate.go` (`277f0e6`) и перевести журнал на него; добавить `application.ErrLeaseLost` и его обработку в `Worker.ProcessOne` (`worker.go:68-76`, аналог заготовки). Тест: существующие тесты зелёные; новый `TestWorkerKeepsRunningWhenLeaseWasTakenOver` сначала падает (ошибка гасит воркера). Готово: `make check` зелёный, `git diff` журнала — только вызовы `domain.*`.

**W1-1. JWKS без замка на сети (ревью №6, issue P6).** `internal/adapters/authkeys/client.go`: `Key` :50 держит `c.mu` на `refresh` :76 и :90.
Падающие тесты в `client_test.go`: (а) десять параллельных `Key` при подвешенном HTTP-клиенте дают ровно один сетевой вызов и все ждут не дольше контекста; (б) пока обновление висит, запрос известного `kid` из кеша возвращается сразу; (в) при отказе Auth в пределах окна устаревания известный ключ отдаётся, неизвестный `kid` даёт `ErrAuthUnavailable`; (г) по истечении окна (по умолчанию 5 минут, `FEEDBACK_AUTH_KEYS_MAX_STALE`, диапазон 30 с – 10 мин) известный ключ тоже отклоняется; (д) повтор неизвестного `kid` не чаще раза в секунду, общий для вызывающих.
Реализация: снимок ключей под `RWMutex`, обновление через `singleflight` (`golang.org/x/sync` уже требуется pgx; добавить vendor-запись в `.go-arch-lint.yml`) или собственный канал, фоновое обновление после 30 с, блокирующее только при выходе за окно устаревания. Инвариант в комментарии: токен живёт 60 с, предыдущий ключ Auth остаётся в JWKS не меньше 60 с после последней выдачи, значит окно 5 минут не продлевает действие токена. Проверки подписи, `aud`, `azp`, `exp` не меняются. Готово: тесты (а)–(д) зелёные, `-race` без замечаний.

**W1-2. Повтор загрузки подписки VPN (ревью №7).** `internal/adapters/vpn/runtime.go`: первая загрузка :89 и плановая :156-170 при неудаче ждут следующего `refreshTicker` (24 ч).
Падающий тест: планировщик повторов с управляемыми часами и источником джиттера (вынести в тестируемый тип): после неудачи старта без свежего кеша повторы идут через 30 с, 1, 2, 4, 8, 15 мин (потолок), с джиттером ±20%; при свежем кеше и работающем туннеле — через 5, 10, 15 мин; успех сбрасывает серию и возвращает обычный `refresh`; повтор не выполняется после отмены контекста; ни одна ветка не открывает прямой маршрут (тест `import_test.go` на маршрутизацию остаётся зелёным). Журнал (JSON в stderr, без URL подписки и без тела ответа): `vpn.refresh.failed attempt=N next_in=…`, `vpn.refresh.ok`. Готово: `telegram-vpn status` по-прежнему показывает свежесть кеша; тесты зелёные; README: раздел про refresh обновлён.

**W1-3. `Reply-To` для не-ASCII адреса (ревью №5).** `delivery/smtp.go:254`, :288.
Падающий тест в `delivery_test.go`: для `иван@пример.рф` собранное письмо не содержит `Reply-To`, все байты заголовков ASCII, адрес присутствует в декодированном теле; для ASCII-адреса `Reply-To` как раньше; тестовый SMTP-сервер не видит `SMTPUTF8` в `MAIL FROM`. Готово: приём (`domain.ValidEmail`) не менялся.

**W1-4. Видимость доставки в логах (ревью №3, issue P7).** Порт `application.DeliveryObserver` (outcome: sent, retry, failed, unknown; channel; код результата; номер попытки; `jobID`, `submissionID`), вызывается из `Worker.ProcessOne` после записи итога и при ошибке записи; адаптер на `slog` в `bootstrap`. Скоуп-обработчик логирует 503 `FeedbackStorageUnavailable` (`scoped.go:226,278,293,298`), как это делает унаследованный. Падающие тесты: перехват `slog` показывает событие на каждый переход; в выводе нет адреса, имени, телефона, текста, токена, IP (проверка по всем полям тестовой заявки). Метрик нет вообще (C33), смена политики остановки воркера тоже в волне 2 (с файловым журналом остановка при сбое записи — гарантия безопасности, см. README).

**W1-5. Документ и контракт часов.** README: исправить «legacy configuration continues to serve /feedback» (:97: при `FEEDBACK_AUTH_URL` унаследованный маршрут отвечает 503); записать требование одной ВМ и нулевого допуска `iat` (R12); тест границы `iat == now+1s` отвергается, `iat == now` принимается (в `auth` тестах). CHANGELOG «Unreleased».

Не входит в волну 1 (по причине): курсоры без состояния, квоты, сроки, стирание, раздельная готовность, поведение воркера при ошибке записи, `ENOSPC` журнала, производительность `Claim` (ревью №1, 2, 4, 9, 10, 11) — снимаются заменой хранилища в волне 2; тест-фикстура с реальным контейнером для скоуп-режима (ревью №12) требует Docker и делается в волне 3 Factory.

## 5. Волна 2 — хранение

Ветка `feat/durable-feedback`, worktree `_wt/durable/umir-feedback`. Старт после G1 и волны 1. Интеграционные тесты под тегом `integration` на настоящих Postgres и RabbitMQ (разд. 7).

### 5.1 Схема (миграция `0001_init.sql`, переписывается: заготовка не выпускалась)

Миграции вперёд, без `down`; применяются командой `feedback migrate` (задание `migrate-feedback`) под `umir_feedback_owner` и `pg_advisory_xact_lock` эталона. Грант `umir_feedback` выдаётся в миграции (имя роли фиксировано, переменной `FEEDBACK_RUNTIME_ROLE` нет). Сервис самомиграции не имеет.

| Таблица | Колонки и ключи | Индексы и назначение |
| --- | --- | --- |
| `schema_migrations` | `version`, `name`, `applied_at` | как в эталоне |
| `storage_meta` | один ряд (`singleton bool PK CHECK`), `generation uuid DEFAULT gen_random_uuid()`, `created_at`, `rotated_at`, `rotate_reason` | R2, C9: ряд создаёт первая миграция; меняет только `feedback generation rotate` |
| `feedback_scopes` | `tenant_id uuid PK`, `org_id uuid NOT NULL`, `UNIQUE (org_id, tenant_id)` | реестр ресторанов, которые присылали заявки; закрепляет связь ресторан → организация (составной FK ниже), заполняется при первой заявке |
| `feedback_scope_fences` | `PK (kind, scope_id)`, `kind IN ('organization','restaurant')`, `deleted_at`, `event_id`, `revision bigint` | надгробие удаления scope (без срока, ПДн нет) |
| `feedback_quota` | `scope_key text PK` (`anonymous` или `restaurant:<uuid>`), `live_rows int CHECK (>= 0)` | квота: `UPDATE … SET live_rows = live_rows+1 WHERE live_rows < $limit` атомарно |
| `feedback_submissions` | `pk bigint identity PK`; `id uuid`; `namespace_kind`; `user_id`, `org_id`, `tenant_id uuid` (NULL для анонимных); `name`, `email`, `phone`, `message text NULL`; `consent_version`, `consent_purpose`; `accepted_at`, `received_at`; `fingerprint text NOT NULL` (HMAC); `payload_cleared_at`, `erased_at`, `purge_at NOT NULL`, `tombstone_until` | `CHECK` по виду пространства (анонимная: все три NULL; app: все три NOT NULL); `CHECK (erased_at IS NULL OR name, email, phone, message все NULL)`; `UNIQUE (id) WHERE anonymous`; `UNIQUE (user_id, org_id, tenant_id, id) WHERE app`; `UNIQUE (pk, org_id, tenant_id)`; `FOREIGN KEY (org_id, tenant_id) → feedback_scopes`; `(org_id, tenant_id, received_at, pk) WHERE app AND erased_at IS NULL` (список); `(purge_at) WHERE erased_at IS NULL`; `(tombstone_until) WHERE erased_at IS NOT NULL`; `(email) WHERE erased_at IS NULL` (команда `delete --email`) |
| `feedback_jobs` | `id uuid PK`; `submission_pk → submissions ON DELETE CASCADE`; `org_id`, `tenant_id` (копия, составной FK `(submission_pk, org_id, tenant_id)` → submissions, чтобы scope задания совпадал с scope заявки); `channel`, `state`, `attempts`, `next_attempt_at`, `retry_started_at`, `started_at`, `confirmed_at`, `error_code`, `provider_id`; `fence bigint` (из последовательности при каждом `Claim`); `last_wake_at` | `UNIQUE (submission_pk, channel)`; частичный `(channel, next_attempt_at) WHERE state = 'pending'`; `(channel, started_at) WHERE state = 'in-flight'` |
| `outbox` (эталон, C4) | `id uuid PK`, `position bigserial UNIQUE`, `exchange`, `routing_key`, `headers jsonb`, `body jsonb`, `org_id`, `restaurant_id` (NULL), `created_at`, `not_before`, `published_at`, `routed bool`, `attempts int`, `claimed_by`, `claim_fence`, `claim_expires_at` | форма эталона без своих столбцов; частичный индекс неопубликованных по `(not_before, position)` — как в эталоне; тело без ПДн (разд. 5.3) |
| `inbox` (эталон, C4) | `consumer`, `message_id`, `received_at`, `PK (consumer, message_id)` | дедупликация событий `umir.auth` (`consumer = feedback.auth`, `message_id = eventID`) |
| `worker_leases` (эталон, C4) | `name PK`, `holder`, `fence bigint`, `expires_at` | аренды фоновых работ (разд. 5.5) |

RLS: `ENABLE` и `FORCE ROW LEVEL SECURITY` на `feedback_submissions` и `feedback_jobs`. Политика `USING`/`WITH CHECK` строится на SQL-хелперах эталона (C5): `umir.actor = 'system'` ИЛИ (`namespace_kind = 'app'` И `umir.actor = 'tenant'` И `org_id::text = current_setting('umir.org_id', true)` И `tenant_id::text = current_setting('umir.restaurant_id', true)`) ИЛИ (`namespace_kind = 'anonymous'` И `umir.actor = 'anonymous'`). Без контекста строк нет (закрытый отказ). Обёртка эталона: `InScopedTx(Scope{Actor: tenant|anonymous, OrgID, RestaurantID})` для HTTP-путей (приём, `LookupScoped`, список), `InSystemTx(ctx, reason)` для `Claim`, `Finish`, сверки, потребителя, очистки и CLI. Колонка `tenant_id` — это ресторан, то есть `umir.restaurant_id` эталона. Значения GUC сверяются с эталоном при копировании; расхождение — CR в эталон, не свой GUC. Таблицы эталона `outbox`, `inbox`, `worker_leases` без RLS: ПДн в них нет.

### 5.2 Порты и адаптеры

| Порт (consumer-owned) | Реализация |
| --- | --- |
| `Repository`, `ScopedRepository` (сохраняются) | `postgres.Store` (приём, `Lookup*`, `Claim`, `Finish*`); `memory.Store` (только тесты) |
| `Scoped lister`: `ListScoped(ctx, org, tenant, after, limit)` (keyset по `(received_at, pk)`; сменяет полный список) | оба адаптера; курсор `base64url(JSON{op, org, tenant, receivedAt, pk, exp})` без подписи: сведений он не раскрывает (запрос всегда ограничен scope из проверенного токена), несовпадение scope, операции или `exp` (5 минут) → `403 Forbidden`, как сейчас |
| `Fingerprinter` (HMAC, ключ из конфигурации) | `adapters/runtime` |
| `Lifecycle.ApplyScopeDeleted(ctx, event)` | `postgres.Store` (одна транзакция с записью inbox) |
| `Retention.Sweep(ctx, now, limits)` | `postgres.Store` |
| `Reconciler.WakeDue(ctx, now)` | `postgres.Store` |
| `Outbox` (эталон R15: `Claim` пачки с fence, `MarkPublished`, политика возврата по ключу) | `postgres` + `rabbitmq.Publisher` |
| `Health` (возможности для `/readyz`, разд. 5.6), `DeliveryObserver` (W1-4) | `bootstrap` |

`Claim` остаётся «взять самое старое готовое задание канала» (сигнатура порта не меняется): сообщение-будильник лишь запускает один `Claim`. `Finish` требует `state = in-flight` и совпадение `fence` (добавить `Job.Fence int64` в домен), иначе `ErrLeaseLost`.

### 5.3 Транзакции, outbox, inbox

События и ключи (имена из каталога главной спеки, раздел 6 п. 3; C1–C3):

| Что | Куда | Ключ маршрутизации | Тело |
| --- | --- | --- | --- |
| Задание готово к попытке | `umir.tasks` (direct); возврат `mandatory` = retry (C3, только ключи `umir.tasks`) | `feedback.smtp`, `feedback.telegram` | сообщение задачи, не конверт события: `messageID`, `owner=feedback`, `schemaVersion`, `jobID`, `channel`, `generation`, `occurredAt`, `correlationID`. Без ПДн и текста |
| Удаление ресторана / организации (подсказка, C45) | потребление из `umir.auth`, очередь `feedback.auth` (durable quorum, объявляется `mq-init` только при включённом профиле Feedback) | `auth.restaurant.deleted`, `auth.organization.deleted` | конверт события: нужны `eventID`, `orgID`, `restaurantID` (для ресторана), `occurredAt`, `aggregateRevision` |

`deleting` и прочие события Auth очередь `feedback.auth` не получает: привязаны только два ключа. `aggregateRevision` у `auth.*` монотонна, но не подряд (C30): Feedback не ищет разрывов и лишь игнорирует устаревшую ревизию.

Границы транзакций (повтор 40001/40P01 делает `InTx` эталона):
1. **Приём** (`tenant`/`anonymous`): проверка fence scope; чтение существующей заявки с сравнением отпечатка (повтор → исходная квитанция, расхождение → `ErrIdentityConflict`); при новой: квота, реестр scope, строка, два задания, два ряда outbox (`not_before = received_at`) — всё одной транзакцией. Параллельные одинаковые запросы разрешает уникальный индекс. Неизвестный исход COMMIT не переоткрывается: клиент повторяет тот же `submissionID`.
2. **Claim** (`system`; не путать с `Outbox.Claim` эталона): `FOR UPDATE SKIP LOCKED`, `state = in-flight`, `attempts + 1`, `fence = nextval`. Просроченная аренда (по умолчанию 5 мин, как в заготовке) возвращается в очередь следующим `Claim`.
3. **Finish** (`system`): проверка fence; при повторе — ряд outbox с `not_before = next_attempt_at`; если оба задания `sent` — очистка `message` и `phone`; если scope помечен удалённым и результат был бы `pending` — задание становится `failed` с кодом `scope_deleted`.
4. **Потребитель жизненного цикла**: вставка в `inbox` эталона (`ON CONFLICT DO NOTHING`; дубликат → ACK без изменений), запись fence scope (устаревшая `revision` игнорируется), `purge_at = LEAST(purge_at, deleted_at + 7 дней)` для строк scope, `pending` задания → `failed/scope_deleted`; затем ACK. Работающий вызов провайдера допускается завершить, результат строку не воскрешает.
5. **Relay** эталона (аренда `feedback.outbox` в `worker_leases`): пачка неопубликованных рядов с наступившим `not_before`, публикация с publisher confirms и `mandatory`; отметка `published_at` и `routed` только после положительного подтверждённого маршрута. Все ключи Feedback в `umir.tasks` имеют политику возврата retry: возвращённое сообщение (нет очереди — ошибка конфигурации топологии) оставляет ряд и увеличивает `attempts`. Feedback не публикует в `umir.auth`. Дубликаты допустимы.
6. **Сверка** (аренда `feedback.reconcile`): для `pending` заданий с наступившим сроком и для `in-flight` с истёкшей арендой, у которых `last_wake_at` старше 60 с, вставляет ряд outbox и обновляет `last_wake_at`. Это страховка от потерянного сообщения и от сообщения, ушедшего в DLQ: задание не зависит от доставки конкретного сообщения.
7. **Потребитель канала** (по одному на канал, prefetch 1): сообщение → `ProcessOne` один раз → ACK. Ошибка хранилища или не доставленный `Finish` → `nack` в `<queue>.retry` (TTL 30 с, возврат в основную очередь); после `x-delivery-limit` 5 → `<queue>.dlq`. Ошибка не гасит процесс (замена `runWorker`): цикл помечает возможность деградированной и ждёт с экспоненциальной паузой.
8. **Сверка scope с реестром Auth** (аренда `feedback.registry` в `worker_leases`; при старте и затем периодически, интервал — переменная `FEEDBACK_REGISTRY_RECONCILE_INTERVAL`, по умолчанию 10 минут, C50): читает реестр Auth `?since=<revision>` вместе с надгробиями (C13), хранит последнюю ревизию в `storage_meta`; для каждого надгробия ресторана или организации, у которых в `feedback_scopes` есть scope, применяет ту же транзакцию, что и п. 4 (запись fence scope, `purge_at`, `pending` → `failed/scope_deleted`), идемпотентно с событием. Потерянное или не доставленное событие `deleted` тем самым всё равно доходит. Закрыто: C50 — приватный реестр Auth читается по своему сервисному ключу Feedback, как у Warehouse и Serving; Auth добавляет Feedback в читатели реестра в своей волне 2, factory выдаёт ключ (`*_KEY_FILE`).

Топологию объявляет только задание `mq-init` factory (C2): обменники `umir.tasks`, `umir.auth`, `umir.dlx`, quorum-очереди `feedback.smtp`, `feedback.telegram`, `feedback.auth` (последнюю только при включённом профиле Feedback, C45) с `.retry` и `.dlq` и привязками. Feedback ничего не объявляет (exclusive-очередей у него нет): при старте и при переподключении он пассивно проверяет очереди, отсутствие или несовпадение делает возможность `delivery.<канал>` или `lifecycle` недоступной (`down`), а не переопределяет топологию.

### 5.4 Redis

Не используется. Причины — в разд. 3. Переменных, адаптера, зависимости нет. Если Factory ожидает Feedback в Redis-зависимостях `compose`, это неверно.

### 5.5 Конфигурация и фоновые работы

Контракт имён — `docs/durable/configuration.md` (C10); `<SVC>_STORAGE` нет. Новые переменные (DSN, URL брокера и ключи — только `_FILE`, чтение как `LoadDSN` эталона: заданные одновременно переменная и файл — ошибка): `FEEDBACK_DATABASE_URL_FILE` (роль `umir_feedback`), `FEEDBACK_MIGRATE_DATABASE_URL_FILE` (роль `umir_feedback_owner`; только команды `feedback migrate` и `feedback generation rotate`), `FEEDBACK_DB_MAX_CONNS` (по умолчанию 10), `FEEDBACK_AMQP_URL_FILE`, `FEEDBACK_DIGEST_KEY_FILE` (не меньше 32 байт), `FEEDBACK_INSTANCE_ID` (уникален на реплику, задаёт factory; попадает в `/readyz` и `holder` аренд, C19), `FEEDBACK_RETENTION_ANONYMOUS_DAYS`, `FEEDBACK_RETENTION_RESTAURANT_DAYS`, `FEEDBACK_RETENTION_DELETED_SCOPE_DAYS`, `FEEDBACK_TOMBSTONE_DAYS`, `FEEDBACK_QUOTA_ANONYMOUS_ROWS`, `FEEDBACK_QUOTA_RESTAURANT_ROWS` (все обязательные, без умолчаний до G1), `FEEDBACK_LEASE_DURATION` (5 мин, аренда `Claim` задания), `FEEDBACK_AUTH_KEYS_MAX_STALE` (W1-1). Переменных метрик и `FEEDBACK_RUNTIME_ROLE` нет (C33, C8). `FEEDBACK_WORKER_POLL_INTERVAL` переопределяется как интервал relay и сверки. Правила повторов (`FEEDBACK_RETRY_*`) и SMTP/Telegram/VPN — без изменений.

Аренды между репликами: `feedback.outbox`, `feedback.reconcile`, `feedback.retention` — строка в `worker_leases` эталона (захват `INSERT … ON CONFLICT DO UPDATE WHERE expires_at < now()`, продление каждую треть срока, `fence` растёт при смене владельца; все записи фоновой работы проверяют `fence`). Аренда на 30 с. Конкурирующие потребители канала не нуждаются в арендах: их разделяют `SKIP LOCKED` и очередь.

Очистка по срокам (`Retention.Sweep`, пачки по 500, `SKIP LOCKED`, сначала строки без `in-flight` задания с неистёкшей арендой): (а) `purge_at <= now` → стереть `name, email, phone, message`, `erased_at = now`, `tombstone_until = now + FEEDBACK_TOMBSTONE_DAYS`, `live_rows − 1`, отпечаток остаётся; (б) `tombstone_until <= now` → удалить строку (задания каскадом); (в) outbox `published_at` старше 7 дней, inbox старше 14 дней; (г) раз в час пересчёт `feedback_quota` по фактическим строкам. `purge_at` при приёме: анонимная `received_at + 30 д`, ресторанная `+ 180 д`. Надгробия scope (`feedback_scope_fences`) без срока. Команда `feedback purge` теперь выполняет один проход `Sweep`; `delete --email` стирает по адресу во всех пространствах имён (ревью №2); `retry` работает для любого пространства имён; `status` постраничный и без ПДн.

### 5.6 Готовность, логи, метрики

C32: `GET /healthz` — процесс жив, без изменений. `GET /readyz` — основная возможность (приём) готова: 200 при `intake` ok, иначе 503; тело `{"instance":…, "generation":…, "capabilities":{"intake":…, "delivery.smtp":…, "delivery.telegram":…, "outbox":…, "lifecycle":…, "retention":…}}`, значения `ok|degraded|down`. Подпутей `/readyz/*` нет, раздельное состояние доставки видно в теле. Healthcheck compose и `server healthcheck` смотрят `/readyz`. Имена возможностей фиксируются в `configuration.md`.
- `intake`: пул отвечает, версия схемы равна ожидаемой (иначе возможность недоступна, процесс живёт и перепроверяет раз в 5 с), `AssertRuntimeRole` (не суперпользователь, не владелец, без `BYPASSRLS`) выполнен при старте (нарушение — процесс не стартует) и перепроверяется, поколение `storage_meta` прочитано. Брокер для приёма не нужен: при его падении приём продолжается, накапливается outbox.
- `delivery.<канал>`: потребитель подключён к очереди, база отвечает, цикл жив (сердцебиение не старше трёх интервалов), очередь есть при пассивной проверке (иначе `down`), нет серии из 3 подряд ошибок хранилища (иначе `degraded`). Значение возможности доставки на код `/readyz` не влияет (200). Сбой провайдера или VPN готовность не снижает: он виден в логах и в SQL-чтении заданий (`provider outage` не стирает работу).

Логи JSON в `slog` по полям `docs/observability.md` §3; события `feedback.intake`, `feedback.delivery`, `feedback.relay`, `feedback.reconcile`, `feedback.lifecycle`, `feedback.retention`, `feedback.worker`; в полях только `submission_id`, `job_id`, `event_id`, канал, код, счётчики. Запрещено: адрес, имя, телефон, текст, токены, IP, тело ответа провайдера.
Метрик и их эндпоинта нет (C33). Приёмка читает: SQL владельца — возраст старейшей неопубликованной строки `outbox` (`now() - min(created_at) WHERE published_at IS NULL`), число и возраст `pending` и `in-flight` заданий по каналам (`feedback_jobs`), использование квоты (`feedback_quota.live_rows`), число стёртых по сроку (по `erased_at`); management API RabbitMQ — глубина `feedback.smtp.dlq`, `feedback.telegram.dlq`, `feedback.auth` и `.retry`; логи (события выше). Коды результата доставки — из закрытого набора, в логи попадают только они.

### 5.7 Удаляемый и новый код

Удалить: унаследованный маршрут `/feedback` (`httpapi/feedback.go`, C46), `internal/adapters/journal/*` (включая тесты), `ObserveHistory`, `legacyFingerprint`, `Legacy`, переменные файлового хранилища, `runWorker` как гасящий процесс, процессный кеш `pages`, `bootstrap/*_test.go` на журнале. Добавить: `internal/adapters/{postgres,rabbitmq,memory}` (память только для тестов; `bootstrap` и `cmd` её не импортируют, правило в `.go-arch-lint.yml`), `internal/application/{lifecycle,retention,reconcile,registry}.go`, `bootstrap/supervise.go` (владелец горутин с остановкой и ожиданием), подкоманды C9: `server serve` (запуск без аргумента больше не принимается), `server healthcheck` (теперь `/readyz`), `feedback migrate`, `feedback generation rotate --reason <текст>` (админ-CLI `feedback export|delete|purge|status|retry` остаётся), цель `make integration` (`-tags integration -p 1 -race`), vendor-записи pgx, AMQP, `x/sync`. Фикстуры `tests/fixtures/*` — разд. 9 (C46: на тестовом memory-адаптере).

## 6. Интерфейсы с другими группами

**Выдаёт.**
- Gateway: публичный и приватный HTTP-контракт v2 без изменений (коды 201/200/409/422/503 прежние). Новое поведение: при превышении квоты `503 FeedbackStorageUnavailable`; приём в удалённый scope — `403 Forbidden` (Auth такой токен не выдаст, это защита от гонки). Gateway продолжает считать 503 от Feedback неизвестным или повторяемым исходом по DUR1 (разд. 6 п. 1 главной спеки), повтор с тем же `submissionID` безопасен.
- Factory (волна 3): образ с командами `server serve`, `feedback migrate`, `feedback generation rotate --reason`; `/readyz` для healthcheck (подпутей и порта метрик нет, C32, C33); перечень секретов и переменных (разд. 5.5); схему и RLS как проверяемый артефакт (интеграционные тесты).
- G-frontend: значения сроков (R19) — из утверждённой таблицы R9 после G1.

**Потребляет.**
- DUR1/ADR 0002 (G-factory, волна 0): смысл поколения хранилища (R2), таблица R9, каталог событий (имена `umir.auth`, конверт, схема сообщения задачи, обменник `umir.tasks`), `configuration.md`.
- G-auth: публикация `auth.restaurant.deleted` и `auth.organization.deleted` в `umir.auth` через outbox (R17, C1); конверт каталога с `orgID`, `restaurantID`, `aggregateRevision`, `occurredAt`; политика возврата для всех ключей `umir.auth` — accept (C45): Feedback — необязательный профиль. Feedback дополнительно читает реестр Auth с `?since=<revision>` и надгробиями (C13) по сервисному ключу Feedback (C50). Остальные события Auth не потребляются.
- G-factory (CR при расхождении): роли `umir_feedback_owner` и `umir_feedback` (не владелец, без `BYPASSRLS`), база `feedback`, vhost и пользователь AMQP с правами на публикацию в `umir.tasks` и чтение очередей `feedback.*`; топологию объявляет `mq-init` (в том числе `feedback.auth` и привязки к `umir.auth`; пользователю Feedback права на `configure` не нужны); файлы секретов (URL базы обеих ролей, URL брокера, `FEEDBACK_DIGEST_KEY_FILE` в списке секретов и в бэкапе: потеря ключа превращает повторы в `409`); задание `migrate-feedback` до сервиса; `FEEDBACK_INSTANCE_ID`; healthcheck по `/readyz`.
- Эталон R15 (`umir-factory reference/durable`): пул и `InTx`, миграции, `InScopedTx`/`InSystemTx`, `AssertRuntimeRole`, `worker_leases`, outbox и inbox, relay с политикой возврата, коннектор AMQP, inbox-потребитель. До готовности эталона группа работает по заготовке и сверяет расхождения; записывает SHA эталона в комментарии-источнике.

## 7. Тесты и проверки

- **Unit и race:** `make check` (включает `go test -race`), `make quality-test`. Новые пакеты `adapters/memory`, домен, планировщик повторов VPN, JWKS, HTTP, поведение отказов хранилища через подставные порты.
- **Контрактный набор хранилища:** `tests/storecontract` (14 кейсов заготовки, переименовать `Purge`/`DeleteEmail`-кейс под новую семантику) выполняется для `memory.Store` в unit-запуске и для `postgres.Store` под `integration`. Добавить кейсы: надгробие (повтор того же содержимого после стирания → исходная квитанция, иное содержимое → `ErrIdentityConflict`); квота по пространствам имён (анонимная не мешает ресторанной, превышение не оставляет строк и заданий); стирание не затрагивает чужой scope; `Finish` с устаревшим `fence` → `ErrLeaseLost`, в том числе после `Retry`; очистка `message`/`phone` после двух `sent`; keyset-список, стабильный при вставках; приём в удалённый scope отвергается, `pending` задание удалённого scope не станет живым.
- **Интеграционные (`go test -tags integration -p 1 -race ./...`, `make integration`)**; переменные `UMIR_TEST_DATABASE_URL`, `UMIR_TEST_AMQP_URL` (Redis-переменные не нужны; в волне 2 цель `make integration` объявляется); временная база и vhost `t_feedback_<rand>`, без пропусков; роли для RLS (`umir_feedback_owner`, `umir_feedback`) создаются с случайным суффиксом и удаляются; топологию в тестах объявляет тестовый аналог `mq-init`, сам Feedback ничего не объявляет:
  - миграции: чистая база, повтор, два мигратора одновременно, отказ на схеме новее кода, `umir_feedback` не может выполнить DDL, `AssertRuntimeRole` отклоняет владельца и суперпользователя; бинарник `server serve` без DSN и без generation не стартует и ключей не создаёт;
  - RLS с настоящей непривилегированной ролью через общий пул: контекст `tenant` ресторана A не видит и не меняет строки B и анонимные (значения `umir.actor` — `tenant`, `anonymous`, `system`); соединение, возвращённое в пул, не сохраняет контекст; без контекста строк нет; составной FK не даёт связать чужой `tenant_id` с другой организацией;
  - outbox: приём атомарен (искусственный отказ вставки outbox → нет заявки, заданий, квоты); relay: потеря подтверждения, дубликат, `mandatory`-возврат на `umir.tasks` без очереди оставляет ряд (политика retry, `attempts` растёт); сверка с реестром Auth стирает scope, чьё событие `deleted` потеряно (очередь `feedback.auth` отсутствует или сообщение ушло в DLQ); сбой между публикацией и отметкой даёт дубль;
  - потребители: redelivery, `nack` → `.retry` → после `x-delivery-limit` 5 `.dlq`, отсутствующая очередь при пассивной проверке делает возможность `down`, ПДн в DLQ нет (проверка тел всех очередей на отсутствие адреса и текста), сверка заново будит задание после потери и после DLQ;
  - две реплики на одной базе: каждое задание уходит провайдеру-имитации один раз без отказов; relay, сверка и очистка работают у одной реплики, при остановке владельца аренду берёт вторая с большим `fence`, запись устаревшего владельца отвергается; курсор, выданный репликой A, принимается репликой B;
  - жизненный цикл: дубликат события применяется один раз (inbox), устаревшая `revision` игнорируется, `pending` отменяются, `in-flight` завершается без воскрешения PII, стирание по сроку с управляемыми часами (30, 180, 7 дней, затем 180 дней надгробия);
  - поколение: первая миграция создаёт `storage_meta`; `generation rotate --reason` меняет значение (отказ при запущенном сервисе и под ролью среды выполнения), новые сообщения несут новое;
  - `/readyz`: тело `{instance, generation, capabilities}`, подпутей нет; `intake` down даёт 503, `delivery.*` down — 200;
  - JWKS: недоступность Auth дольше 30 с в пределах окна обслуживает известные ключи.
- **Матрица отказов #17, зона Feedback (эта группа владеет):** падение процесса до и после коммита приёма и потерянный ответ о коммите (повтор того же `submissionID` даёт исходную квитанцию, одна заявка, два задания); публикация до и после подтверждения брокера; падение потребителя до и после коммита и до ACK; дубликаты, задержанные, переупорядоченные и ядовитые сообщения (будильник идемпотентен; ядовитое → DLQ без потери задания); недоступность и рестарт брокера (приём продолжается, outbox догоняет); недоступность и рестарт базы (приём 503, после возврата работа продолжается без потерь); заполнение диска (имитация ошибки `53100` в слое хранилища: `503`, готовность приёма падает, принятые данные читаются; настоящий диск проверяет Factory); истёкшая аренда и конкурирующие реплики (допускается дубль письма, подтверждённый тестом, состояние не расходится). Сценарии, зависящие от совместной работы с Auth, Gateway и от дисковой квоты контейнера, выполняет Factory в волне 3.
- Тесты не отправляют настоящих писем и сообщений; ПДн в логах проверяются перехватом.

## 8. Приёмка группы

- [ ] Волна 1: W1-0…W1-5 выполнены, красные тесты были красными до правки; `make check`, `make quality-test` зелёные; отчёт `wave-1-report.md`.
- [ ] В `main`-ветке группы нет `internal/adapters/journal`, нет `FEEDBACK_STORE_PATH`/`MAX_STORE_BYTES`/`RETENTION_DAYS`, нет маршрута `/feedback`, `Legacy`, `legacyFingerprint`, импортёров; нет пути «база недоступна → память или файл» (R20); `cmd` и `bootstrap` не импортируют `adapters/memory` (arch-lint).
- [ ] `feedback migrate` создаёт схему и generation под `umir_feedback_owner`; сервис стартует без DDL-прав и сам не мигрирует; `AssertRuntimeRole` при старте; RLS включён и принудителен; тесты с настоящей ролью зелёные; `feedback generation rotate` работает.
- [ ] Контрактный набор зелёный для `memory` и `postgres`; интеграционные тесты и матрица отказов (разд. 7) зелёные: `make integration` с числами в отчёте.
- [ ] Две реплики на одной базе: задание уходит один раз, фоновые работы у одной реплики, курсор переносим.
- [ ] Outbox, inbox и аренды — таблицы эталона (`worker_leases`), без своих форм; топологию объявляет только `mq-init`; события и очереди названы по C1–C3 (`umir.tasks`, `feedback.auth`).
- [ ] Сверка scope с реестром Auth работает при старте и периодически под арендой; пропущенное `deleted` доходит.
- [ ] Заявка, задания и outbox коммитятся вместе; ни одна запись outbox не помечена опубликованной без положительного подтверждения маршрута; потеря сообщения и DLQ не теряют задание.
- [ ] Стирание: сроки R9 соблюдены на управляемых часах; событие Auth стирает scope по срокам; PII в DLQ и логах нет.
- [ ] Квоты по пространствам имён работают; превышение не оставляет строк; доля заполнения читается из `feedback_quota`.
- [ ] `/readyz` (без подпутей) показывает в теле приём и доставку по каналам; убитый воркер виден; сбой провайдера готовность не снижает.
- [ ] Логи по списку 5.6, SQL-запросы приёмки описаны в README; метрик и их эндпоинта нет; ПДн отсутствуют (тест перехвата).
- [ ] JWKS: один общий сетевой вызов, окно устаревания ограничено и проверено; VPN: повтор с backoff и джиттером проверен.
- [ ] README (конфигурация, CLI, готовность, сроки, требование одной ВМ), CHANGELOG «Unreleased» с `!` и описанием миграции (чистый лист, без импорта); `git diff --check`; финальное ревью ветки (Opus) — Ready.
- [ ] Открытые CR оформлены (разд. 9), отчёт `wave-2-report.md` с SHA, коммитами и проверками.

## 9. Риски и вопросы к оркестратору

1. Закрыто: C24 — текст и телефон очищаются после `sent` обоих заданий; при `failed` остаются до срока обращения, чтобы работал ручной повтор.
2. Закрыто: C14 — `FEEDBACK_DIGEST_KEY_FILE` входит в список секретов factory и в бэкап секретов.
3. Закрыто: C1, C2 — `auth.restaurant.deleted`/`auth.organization.deleted`, обменник `umir.tasks`, очередь `feedback.auth`; сообщение задачи — отдельная схема; топологию объявляет `mq-init`.
4. Закрыто: раздел 10 «Вопросы групп», C46 — фикстуры RPC1.1.0 (`tests/fixtures/*`) нужны factory (`origin/main`, `tools/assembly-checks/backend*.go`: `/fixture-server`, `/fixture-control`, протокол `umir-operational-fixtures/1.1.0`, `fixture-feedback`). Они остаются тестовым инструментом на тестовом memory-адаптере за новым интерфейсом `Store`: `protocol.HandlerOptions.Store` — интерфейс, `Observe` — чтение из памяти, `stateFile` не нужен, `ObserveHistory` и legacy-часть убираются; Postgres им не нужен. Запрет memory (R20, C10) относится только к боевым бинарникам.
5. Закрыто: C46 — `/feedback` и файловый журнал удаляются в волне 2; C28 для Feedback уточнён этим решением.
6. Закрыто: C8 — роли `umir_feedback_owner`/`umir_feedback`, `feedback migrate` как задание `migrate-feedback` до сервиса, самомиграции нет.
7. **Открыто (G1).** Размер записи и скорость заполнения не измерены (ревью №1: около 2 КБ — оценка). Квоты 150 000 / 5 000 — предложение; нагрузочная приёмка Factory должна измерить размер в Postgres с индексами.
8. Закрыто: C4, C5 — таблицы `worker_leases`, `outbox`, `inbox` и контекст RLS берутся из эталона. Если эталон запоздает, волна 2 стартует по заготовке и затем переносит расхождения отдельным коммитом.
