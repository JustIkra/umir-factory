# Текущий план: версия для защиты, редакция 2

Дата: 14.10.2026, Europe/Moscow. Статус: единственный текущий план, выбран пользователем. Регистрация плана не означает выполнения изменений сервисов.

Решение пользователя: «Запиши её как текущий план,и все остальные отбрось». Этот документ заменяет все прежние планы исполнения. Канонический план исполнения — этот документ. Архивные планы, их незавершённые задачи и прежние требования завершить полную qualification не являются дополнительными обязанностями этого плана.

Основание редакции 2: пользователь передал изменённый `artifact.html` с сообщением «вот такие изменения». В нём явно включены минимальный М6, улучшение горячих предложений и вывод FEFO/партий; остальные дополнительные функции исключены. CI Auth остаётся обязательным минимумом. Исходный приложенный файл, SHA256 `ada516435ac21027f21373db54cbdf776c8aa555bfccfdddabaa3882b57b7b4a`.

Сохраняются предыдущие решения: переход просрочки в «ожидает списания» и списание человеком правильны; аналитика не приоритет. Предложение считать состояние от снапшота остаётся FIN-10, но по приложенной редакции выполняется только после обязательных пунктов. На вопрос о дате защиты пользователь ответил «опустим»; календарный дедлайн не задаётся.

## 1. Цель и границы

Получить одну воспроизводимую стабильную сборку с настоящим Auth, работающими заказами и готовым интерфейсом, дополненную ровно выбранными пунктами М6/горячих предложений/FEFO. Исправить препятствия запуску, перенести существующие процедуры в Factory и довести ту же сборку до стенда по ссылке. Реализация сервисов и deployment этим документальным обновлением не запускаются.

Основной источник замечаний — [ревью «Ровного хода»](https://claude.ai/artifact/Vqz4hXgA4S643waahiN75C), разделы «План до защиты», «Что мешает показать проект», Auth/Serving и «Поправки и оговорки». Артефакт прочитан в браузере 14.10.2026. Его CI, ветки, образы и domain status относятся к снимку отчёта; перед исполнением их проверяют заново.

Предыдущая backend qualification и её backlog all34/all67/307, F3/F4/J1/E1–E5/J2 отменены как план исполнения. Старые manifests, reviews и results сохраняются как исторические bytes и не получают нового PASS. Рабочие product contracts, authority, FEFO, exact quantities и правила безопасности действующего кода сохраняются; отмена плана не отменяет необходимые regression checks или полные repository-declared gates.

FIN-11/12 оформляются отдельным контрактным дополнением до owner/consumer integration: version, schemas, operation/error mapping, acceptance rows и переход affected cases фиксируют явно. Если ранжирование меняет старый ожидаемый порядок, это явная contract policy change с соответствующими проверками. Старое evidence не переименовывают в новый результат. Проверять текущую сборку нужно по FIN acceptance и declared gates, без автоматического возобновления архивного qualification проекта.

Исходный код точечно прочитан в локальных qualification checkout. Auth HEAD — `029474a615112b80dc1ecc21c5b58b8deefed2ed`; Serving HEAD — `2e60b73a79705bb8053a67960eca3679cfc8baa5`. Это inspected source identities, не результаты новых runtime-прогонов. Текущие изменения UA1 другого исполнения сохраняются. Перед правками проверяют HEAD, diff и ownership.

В пакет входят минимальный М6 и ограниченное изменение формулы/причин существующих рекомендаций, заданные в приложенном файле. Не входят Д3/Д4, новые страницы аналитики, генератор нагрузки, Grafana, персистентность склада, v4, новая инфраструктура и редизайн. Warehouse/Serving сохраняют согласованный in-memory режим; после restart демо-данные восстанавливают поддерживаемым seed. FIN-10 не блокирует версию для защиты.

Просрочка сохраняет текущую семантику: истечение запрещает использование партии и переводит её в «ожидает списания»; окончательное списание подтверждает пользователь с ролью «Склад». Полное автоматическое списание не является недостающей функцией этого пакета.

## 2. Изменения и критерии приёмки

### FIN-01 — CI Auth: обязательный первый результат

**Сейчас.** В inspected Auth checkout есть production Dockerfile и declared quality gates, но нет `.github/workflows`. Локальные проверки не заменяют remote CI.

**Изменить.** Добавить workflow проверок на pull request и push в main. Использовать Go и quality tools из repository pins, явные configs и `GOWORK=off`. Выполнять `make check`, `make quality-test`, `go mod verify` и проверку отсутствия diff после `go mod tidy`; проверить инвентарь modules. `make check` должен сохранять build, форматирование, lint, architecture и полный race suite. Собрать production image из проверенного exact commit.

На pull request достаточно проверки и сборки без registry credentials. Публикация в GHCR идёт отдельным job после gates, только из разрешённого main push; publishing permissions ограничены этим job. Публикация требует отдельного поручения на доставку. Образ — `sha-<full-commit-SHA>`, с OCI source/revision и записанным digest. Fixture image не подменяет production image. Версии actions закрепляют в workflow по проверенным immutable revisions.

**Принять.** Есть успешный CI run именно для final source SHA; failing gate блокирует image publication. Проверены PR без publishing secrets и production image start/health. После разрешённой публикации GHCR digest, OCI revision и проверенный commit совпадают. Только локальная сборка оставляет remote CI/publication pending.

**Владелец:** Auth. **Зависимость:** final source FIN-02/03 до итогового CI.

### FIN-02 — Auth: временный сбой не оставляет постоянный 503

**Сейчас.** `bootstrap/serve.go` передаёт любую ошибку mail processor в `failWorker`; `bootstrap/runtime.go` сохраняет worker error, который закрывает readiness. `internal/application/reconciliation.go` вызывает runtime status под `Accounts.mu`. Эти механизмы присутствуют в inspected source.

**Изменить.** Разделить transient IO/attempt timeout, штатную cancellation и реальные ошибки authority/storage/worker invariants. Transient failure сохраняет настоящий durable mail outcome и разрешённый retry, но не закрывает Auth навсегда. Ошибки authority продолжают закрывать admission. Вынести сетевые peer calls за глобальный mutex. После повторного захвата перечитать и проверить revision, lifecycle intent, scope и peer epochs до commit; устаревший ответ не может сделать ресторан ready.

**Принять.** Реальный медленный/недоступный peer и mail timeout воспроизводят исходную проблему. После восстановления peers Auth обслуживает JWKS и независимые account requests, ресторан проходит registering→ready без restart. Storage corruption по-прежнему закрывает admission. Полные owner gates/race и затронутые UA1/profile gates проходят; jobID, attempts, retry deadlines и sent/failed/unknown semantics сохраняются.

**Владелец:** Auth. **Зависимость:** до финальной сборки и приёмки readiness.

### FIN-03 — Auth: изоляция входа и чтение текстовых секретов

**Сейчас.** Login использует один `loginFailures` для процесса; bootstrap передаёт password file bytes как строку. Отчёт указывает блокировку всех пользователей и включение завершающего перевода строки в пароль.

**Изменить.** Привязать login failure throttle к сравнимому login по существующим правилам нормализации. Сохранить действующие лимиты и bounded capacity; unknown login проходит тот же путь без раскрытия наличия User. Восстановление лимита одного пользователя не сбрасывает чужие counters. Не ослаблять отдельные global resource/mail budgets.

Для явно текстовых bootstrap password/SMTP credential files установить одно правило: удалить один завершающий LF или CRLF, сохранить остальные bytes и значимые пробелы; пустой результат отвергается. Raw binary owner/signing/key material не проходит через такую нормализацию. Сохраняются private-file bounds, mode, symlink и ownership checks. Startup сообщает безопасную категорию и этап отказа без secret values и private file content.

**Принять.** Пять неверных входов A не запрещают корректный вход B; cooldown и concurrency одного login соблюдаются. Проверены known/unknown login, capacity, LF/CRLF/no-newline, пустой файл и значимые пробелы. Ни ответы, ни logs не раскрывают password, SMTP credential или existence User. Совместимость и targeted acceptance changes фиксируют отдельно от frozen manifests.

**Владелец:** Auth. **Зависимость:** до final CI и demo setup.

### FIN-04 — Serving: завершение неизвестного подтверждения

**Сейчас.** При сохранённой pending attempt `resolveAttempt` возвращает `ConfirmationPending` для warehouse `unseen`, не отправляя сохранённый commit. Механизм присутствует в inspected source.

**Изменить.** Разрешить bounded повтор исходного commit с теми же commandID, payload/fingerprint, proposal и scope после проверки сохранённого Warehouse epoch. Действительно pending работа не запускается параллельно повторно. Сначала учесть известный warehouse result; terminal result завершает локальную попытку. Другой commandID не служит обходом неопределённого исхода.

**Принять.** Разрыв до доставки и потеря ответа после реального commit в том же Warehouse epoch сходятся к одному terminal result после восстановления связи. Списание и продажа выполняются ровно один раз. Payload conflict отвергается, concurrent retries безопасны. После Warehouse restart/смены epoch старый неопределённый исход не объявляется успешным и не исполняется автоматически заново. Проверить owner/race gates и затронутые frozen C2 predicates.

**Владелец:** Serving; Warehouse contract сохраняется. **Зависимость:** до итогового order flow.

### FIN-05 — исправить зависающий Gateway CI и получить актуальные образы

**Сейчас.** Поправка пользователя подтверждена remote readback: [CI/CD run 38587777715](https://github.com/JustIkra/umir-gateway/actions/runs/38587777715) для `9a8461eaa5457229f8a6b424b05379428e93b546` завершился failure. Лог содержит `test timed out after 10m0s`, `TestExpiryEndsTheStreamAfterPeekConfirmsIt (9m52s)` и заблокированное чтение в `streams_test.go:186/306`. Наличие ранее принятого fix не доказывает готовность текущего Linux CI. Attachment отмечает, что `136a3fe` уже входит в этот main. Прежнее утверждение этой спеки об устранённом Gateway дефекте заменено; исторические записи сохранены.

**Изменить.** Для Gateway ограничить чтение тестового потока deadline и сделать clock/timer progression детерминированным по реальному barrier, а не по sleep. Сохранить assertions expiry/terminal/EOF и production guarantees; timeout не маскируется skip или большим global budget. Проверить focused repeated Linux race runs и полный declared gate. Сверить remote runs/GHCR с точными final SHA. Для отдельно включаемого Feedback исправить CI reference на удалённый `otp-smoke-probe`, используя существующий production target и полный quality/race gate. Не добавлять нового почтового владельца вместо Auth.

**Принять.** Gateway CI на final main зелёный, test deadline и детерминированный clock не ослабляют terminal assertions, image опубликован после разрешённой доставки. Для Auth/Gateway/Warehouse/Serving/Frontend указаны source SHA, CI run, image digest и OCI revision. Feedback добавляет свои identities и gates только для optional profile. Test fixture executable не становится production owner. Старый зелёный CI или образ другого commit не закрывает результат.

**Владельцы:** соответствующие repositories. **Зависимость:** FIN-01/02/03/04 и handoff frontend.

### FIN-06 — один поддерживаемый запуск в Factory

**Сейчас.** Selected Compose исторически описывает старую сборку; текущая версия запускается дополнительными локальными скриптами. Требуется перенести существующую процедуру, а не придумать новый deployment stack.

**Изменить.** Подготовить candidate Compose/manifest с Auth, Gateway, Warehouse, Serving, готовым Frontend и существующим proxy. Feedback вынести в opt-in profile `feedback`; базовая сборка и основной сценарий не зависят от него, Telegram/VPN или Feedback SMTP. Auth invitation/OTP mail и его private inputs сохраняются по собственному контракту. Включить существующие генерацию private config/transport keys, Auth bootstrap, health/readiness и реальные peer associations. Candidate использует exact source/image identities; test fixtures остаются в отдельном qualification recipe. Credentials — private файлы и target-scoped mounts, не repository или public artifact.

Отключённый Feedback означает недоступность его формы/notification routes, а не fake success. Условия opt-in, SMTP/Telegram/VPN inputs и поведение отключённой формы описать в README. Старый полный Feedback/C2/profile qualification проект не продолжать. При включении optional profile проверяются его фактические owners, declared gates и текущая интеграция; отключённый профиль не маркируется проверенным.

Первый запуск создаёт отсутствующие secret/config files; повторный сохраняет существующую согласованную Auth authority и её owner secrets. Partial state приводит к явному отказу, а не regeneration. Auth volume persistent; остальные owners сохраняют текущую storage model. Backend ports доступны только внутри Compose network; наружу выходит proxy. Secure cookies работают на HTTPS origin.

**Принять.** Новый checkout воспроизводит запуск без файлов чужой рабочей директории. Без profile `feedback` проходит FIN-09, отсутствуют ложные dependencies/readiness failures и поддельные ответы отключённой формы. Проверены cold start, повторный start без сброса authority, полномочия/registry/ready и доступ через Gateway. `.env.example` содержит имена/безопасные placeholders. README описывает setup, private inputs, stop/reseed, optional profile и диагностику. Selected pins меняют после принятой candidate assembly последним шагом доставки.

**Владелец:** Factory; service configs согласуют owners. **Зависимость:** проверенные final images/builds.

### FIN-07 — существующие demo data в репозитории

**Изменить.** Перенести seed под `tests/examples` или существующий dev/examples каталог Factory. Это фиксированные demo data и повторяемые supported public APIs, без генератора нагрузки. Не записывать Auth JSON вручную и не включать реальные адреса/credentials. Одна documented команда восстанавливает данные изолированного стенда после in-memory reset. Допустим opt-in automatic seed только при достоверно пустом fresh demo epoch, с явным demo mode/project guard; непустое, неизвестное или недоступное состояние не принимается за пустое. Пользовательская/production authority автоматически не засевается и не сбрасывается.

**Принять.** Данные показывают заказ/отказ, стоп-лист, FIN-11 предложения поставки, FIN-12 ранжирование и близкие сроки партий. Повтор в том же epoch безопасен с прежними command identities. После reset используется fresh-epoch seed без автоматического повтора старых unknown заказов. На VM данные восстанавливаются одной documented командой без storage edits. Проверяются fresh-empty, already-seeded, partial seed и unavailable states.

**Владелец:** Factory. **Зависимость:** FIN-06.

### FIN-08 — доставить уже готовый интерфейс

**Изменить.** Получить от frontend owner exact commits/зависимости существующего стека и согласовать его с API. Новые разделы не добавлять; FIN-11 размещается в существующем «Складе», FIN-12 использует текущие «Рекомендации», FIN-13 раскрывает существующие данные. Скрыть пустые «Меню»/«Аналитика» и ссылки на них из сайдбара/«Обзора», если страницы не готовы. Удалить fake-organization flow из основного интерфейса; отдельный demo screen, если сохраняется, явно маркирован и не выдаёт fake organizations за live authority. Menu/stop-list и рекомендации имеют общий минутный fallback refetch при обрыве SSE; scope change, logout и unmount отменяют старые запросы/таймеры. Публикация стека и SHA image идёт по отдельному поручению; write scope разработчика сохраняется.

**Принять.** Existing frontend gates проходят, interface commit воспроизводим. Основные ссылки не ведут к пустым/fake страницам. Реальный UI показывает login, склад/партии, заказ/отказ, стоп-лист, М6, новые причины рекомендаций и просрочку. При обрыве SSE menu/stop-list/recommendations обновляются одним fallback cadence без выдачи старого scope за текущий. Роли/secure cookies используют настоящий Auth/Gateway. Mock screenshots не дают live acceptance.

**Владелец:** текущий frontend разработчик. Этот документ не передаёт его write scope агенту. **Зависимость:** существующий стек и FIN-06/07.

### FIN-09 — проверить одну сборку и развернуть стенд по ссылке

**Изменить.** На одной candidate assembly пройти вход → поставка → остатки/партии → заказ/FEFO → отказ при нехватке → стоп-лист → горячие предложения → предложение поставки → просрочка → подтверждение списания. Проверить SSE/refetch, reload, роли и peer recovery. Сохранить source/image/config identities, actual steps/results и cleanup. Affected regression checks и полные declared gates повторяют после исправлений; старую полную qualification не добавляют как параллельную задачу. После принятой candidate и разрешённой доставки повторить тот же сценарий на [публичном адресе](https://ровный-ход.empty-lab.ru), с seed по FIN-07.

Deploy на существующую VM/Nginx Proxy Manager/HTTPS — финальный результат этого пакета, а не исключённая будущая задача. Подготовить exact delivery diff: pins, manifest, команды, private input names, rollback и README; новую инфраструктуру не создавать. Push/merge/image publication, selected-pin switch и deploy выполняются по поручению на соответствующее действие. Передача документа разрешает изменение спецификации, но не является командой сейчас опубликовать или развернуть сервисы.

**Принять.** Обязательны FIN-01–09 и FIN-11–13; FIN-10 не блокирует. Local verified, published и deployed имеют отдельные статусы. Пакет для защиты завершён только после permitted publication/HTTPS deployment и readback той же assembly: адрес доступен, seed восстановим, secure-cookie login и весь сценарий работают, rollback записан. Credentials/OTP/private URLs в public report отсутствуют. Demo smoke не выдаётся за 34/67/307 PASS. Неисполненный deploy оставляет пакет prepared/local verified, а не complete.

**Владелец:** Factory координатор; frontend и deployment подтверждают свои результаты.

### FIN-10 — Warehouse: снапшоты и копирование, если останется время

**Сейчас.** В inspected Warehouse `41cce8038b0f7d04bb4b1d7e08d190cf9c369bf3` уже есть `SnapshotStore`, фоновые полные snapshots и recovery из подходящего snapshot с replay хвоста. Однако `ApplyRecordedBatch` клонирует весь `InventoryState` на каждом batch, `Owner.Prepare` возвращает полную копию, а существующий `Analytics.Movements` отдельно начинает replay с пустого состояния. Снапшот сам по себе не убирает стоимость этих копий.

**Приоритет.** Только после обязательных пунктов. Attachment сообщает примерно 0,84 с на 1000 заказов; это чужое измерение, не новый benchmark этого исполнения и не доказательство всех latency paths. Оптимизация ядра не блокирует сборку/deploy. Если отдельный текущий обязательный flow действительно выходит в timeout, сначала нужен узкий repair этого flow.

**Изменить при включении FIN-10.** Использовать existing projection/qualified snapshot и хвост. Измерить command/read paths; убрать доказанное клонирование растущей истории минимальным owner-local diff. Сохранить coherent state version остатков, expiry index и command results. Publication atomic: ошибка/cancellation не показывает partial state, readers/snapshots не получают mutable aliases. Новая база/API/сервис для этого не нужны.

**Принять.** Snapshot+tail и полный replay дают одинаковые business state, state version, command results, FEFO, pending writeoffs и expiry index. Сохраняются rejection повреждённого snapshot, проверка coverage/sequence/epoch и существующий fallback/closed-admission policy. Проверить failed append, concurrent readers, replay/conflict, snapshot во время commit и owner race suite. На одном synthetic corpus 1000/4000/8000 заказов снять одинаковым методом before/after latency и allocations для уже существующих command/read operations. Исправление принимают по уменьшению доказанного копирования истории и отсутствию timeout в поддерживаемом текущем сценарии, без ослабления guards. Числа из внешнего отчёта не являются измерениями этого исполнения.

**Отложено.** Новая аналитика и отдельная переработка `Analytics.Movements` не блокируют этот пакет. Если текущий report endpoint уже используется обязательным сценарием и воспроизводимо выходит в timeout, разрешён узкий performance repair существующего endpoint: validated state на том же prefix вместо replay с нуля. Движения за выбранный период всё равно считают по событиям периода или существующей исторической projection; текущий snapshot не содержит весь оборот за период. Сохраняются RecordedAt, timezone, captured prefix, pagination и incomplete coverage semantics. Новые отчёты/поля не добавляют.

**Владелец:** Warehouse. **Зависимость:** завершены обязательные пункты; отдельное inclusion FIN-10 не меняет frozen guarantees. Аналитическая часть низкого приоритета.

### FIN-11 — минимальное предложение поставок, М6

**Изменить.** Warehouse рассчитывает по продукту расход за последние N дней из реальных заказов и подтверждённых списаний, средний суточный расход и дни покрытия пригодным запасом. Просроченные и pending-writeoff партии в доступный запас не входят; само событие перевода в pending не считается повторным расходом. Предложение появляется при coverage ниже порога или нехватке продукта, которая держит блюдо в стоп-листе. Объём доводит запас до целевого уровня и не меньше нехватки на одну порцию; причина содержит факты расчёта.

В attachment приведены примеры порога 3 дня и цели 7 дней; это примеры, а N не задан. До реализации отдельное contract supplement фиксирует N/порог/цель, точные границы периода и RecordedAt, rounding до существующей минимальной единицы, aggregation нехватки нескольких блюд и empty/incomplete-history behavior. Эти величины не приписывают пользователю как уже выбранные defaults. Расчёт детерминирован и использует exact quantities с checked arithmetic; разные base units не складываются.

Добавить отдельное warehouse read operation и согласованные Gateway/Frontend consumers через тот же reviewed supplement. Оно не резервирует остатки, не создаёт заказ/поставку и не меняет history. Period events и текущий пригодный stock относятся к coherent scope/epoch/prefix; incomplete coverage не маркируется полной. Не выполнять полный replay с клонированием state ради нового чтения. Реализация FIN-11 не требует общего FIN-10 refactor.

В интерфейсе — блок/вкладка существующего «Склада», без новой страницы, цен и поставщиков. Доступ проверяется по принятой capability matrix на каждом запросе.

**Принять.** Seed показывает positive cases малого coverage и stop-list deficit с exact quantity/reason. Продукт с нулевым расходом, достаточным запасом и без deficit не получает предложение. Проверены пустая/неполная история, нулевой расход, scope/epoch change, threshold до/на границе, expiry/pending исключение, отсутствие double count, rounding и overflow. Вызов read не вызывает mutations. Supplement schemas и acceptance fixtures согласованы до consumer integration.

**Владельцы:** Warehouse, Gateway, Frontend; Factory владеет contract/acceptance. **Зависимость:** отдельный принятый supplement и текущий stock/history API.

### FIN-12 — горячие предложения с объёмом под риском

**Изменить.** Развить существующий `RecommendationsService` и вкладку «Рекомендации», не создавать новый рекомендательный сервис. В пределах горизонта H=72 часа взять пригодные партии; учитывать только `evaluatedAt < expiresAt <= evaluatedAt+H`. Для продукта W_p — пригодный остаток под риском, r_d,p — его расход на одну порцию, t_p — время до ближайшего рискового срока. Рецептуры и партии получены на совместимом scope/epoch/state version. Блюда из стоп-листа и блюда без рискового ингредиента исключаются.

Использовать предложенную в [исходном ревью, serving §5](https://claude.ai/artifact/Vqz4hXgA4S643waahiN75C) безразмерную формулу:

- `U(t) = 1 / (1 + t/τ)`, где в примере ревью τ=12 часов.
- `A_d,p = min(1, W_p / r_d,p)` — доля порции, которая тратит рисковый объём.
- `E_d,p = min(1, C_d(t_p) * r_d,p / W_p)` — доля рискового объёма, которую блюдо успевает израсходовать.
- `score_d = sum(U(t_p) * A_d,p * E_d,p)` по ингредиентам рецепта с W_p>0.

`C_d(t)` в ревью предлагается брать из OrderPlaced history, а без history — из фиксированного fallback (пример: 2 порции/час). До реализации supplement фиксирует τ, источник/окно/coverage demand estimate, fallback и arithmetic/precision правила. Скрытый прогноз или случайная величина не допускаются. Модель demand используется только для ранжирования; она не резервирует и не гарантирует продажи. Дополнительные competition penalty/alternative costs и Д4 из полного отчёта не входят в эту работу.

Выдавать top-N, default 10. Сортировать по score убыванию, затем ближайшему рисковому expiresAt и dishID. Причина показывает продукт, рисковое количество, срок и целевое «нужно продать около N порций». Эта цель объясняет использование рискового остатка, а не обещает доступность N порций. Quantity, remaining time, score и округление цели задаются новым typed response schema.

Serving получает данные через существующий warehouse expiring-batches механизм и минимальный consumer-defined port. Точные required поля/new response schema и strict Gateway decoding оформляются в supplement, не дописываются молча в frozen C2. Старые recommendation tests обновляют под новую явную policy, сохраняя прежние cases/историю; affected old-order predicates получают отдельный reviewed transition.

**Принять.** Проба ревью: рыба с 3 кг под риском и 300 г/порцию выше блюда с 1 г петрушки; рис со сроком 6 месяцев отсутствует. Fixture фиксирует все сроки, recipes, остальные пригодные ингредиенты и demand/fallback, чтобы обе сравниваемые позиции реально были доступны. Проверены W<r, W=0, horizon boundary, ties/dishID, top-N, stop-list, exact quantity, unit separation, overflow/nonfinite score и scope/version mismatch. Результат повторяем, read не меняет stock. Реальный UI показывает новую причину в текущей вкладке.

**Владельцы:** Serving; Warehouse/Gateway для typed reads, Frontend для причины; Factory для supplement. **Зависимость:** coherent warehouse data и reviewed FIN-11/12 contract package.

### FIN-13 — FEFO и партии в существующем интерфейсе

**Изменить.** В результате заказа и history «Продажа» показать фактические `order.allocations`: product, batch, quantity и expiry в реальном порядке расхода. Не вычислять allocation заново по текущему stock. В остатках раскрывать продукт и читать партии через существующий composed UA1 `GET …/warehouse/products/{productID}/batches`; сохранить declared FEFO ordering, scope/capture/version и refresh-required behavior.

Generated client types и `api:check` используют composed UA1 source, не старый standalone C2. New FIN-11/12 fields подключаются только после принятого supplement. Исправить типы/decoder definitions вместо цепочек casts. Текущие frontend layout/tokens сохраняются.

**Принять.** После заказа видны реальные партии и exact quantities, сумма allocations соответствует фактическому расходу. History после новых операций не показывает пересчитанный FEFO для старой продажи. Product batches показывают остаток, срок и remaining time, включая явный pending статус; сортировка соответствует API. Проверены отсутствие доступа, stale capture, пустые партии и scope switch. `api:check`, decoder/render tests и frontend gates проходят; на 390 строки и quantities не выходят за карточку. Browser evidence использует реальный API.

**Владелец:** текущий Frontend owner; Warehouse/Serving/Gateway contracts сохраняют actual facts. **Зависимость:** существующий UA1 batches и sale allocations.

## 3. Порядок выполнения

1. Сверить branches/ownership/current CI и оценить трудоёмкость пунктов перед реализацией; дату защиты не запрашивать повторно. Подготовить FIN-01 и выполнить FIN-02–04 плюс FIN-05 Gateway repair. Итоговый CI относится к final SHA, а не промежуточному исправлению.
2. Подготовить и принять отдельное FIN-11/12 supplement с numeric/time/shape decisions и affected-case transition. Выполнять FIN-11–13 и подготовку FIN-06–08 по их зависимостям с disjoint ownership. Этот документ не запускает parallel agents и не передаёт frontend ownership.
3. Собрать base candidate без optional Feedback, подготовить seed с М6/ranking cases и exact delivery diff. Полную qualification из прежних планов не возобновлять.
4. Пройти FIN-09 на одной assembly, затем по поручению выполнить публикацию/selected switch/deploy и public-address readback. Изменение source требует affected recheck и итогового сценария для новых identities.
5. FIN-10 выполнять после обязательных пунктов, если включён. Новую аналитику, генератор нагрузки, Д3/Д4 и прочие дополнительные функции не добавлять.

Проверки выбирать по необходимому охвату и прекращать повторение без новой причины. Time budget может остановить работу со статусом incomplete, но не отменяет обязательный CI, image identity, readback или acceptance evidence и не превращает непроверенный результат в PASS. Сейчас трудоёмкость не оценена: это требует current-source audit при переходе к реализации.

## 4. Изменения редакции 2

- CI Auth стал отдельным обязательным deliverable с PR/main gates, проверкой production image и exact identity publication.
- Добавлены конкретные препятствия существующей работе Auth и Serving с проверяемыми before/after условиями.
- FIN-05 исправлен по прямому GitHub evidence: main CI зависает на expiry SSE test даже с ранее принятым fix.
- FIN-11/12/13 добавлены ровно по приложенному артефакту: М6, ограниченное ранжирование/причины, FEFO и партии на существующих страницах.
- FIN-06 делает Feedback optional для base demo; прежний full qualification backlog отменён. FIN-07 добавляет reseed одной командой и ограниченный opt-in fresh-demo auto seed.
- FIN-08 скрывает пустые/fake flows и согласует fallback refetch; frontend ownership сохранён.
- FIN-09 завершается реальным HTTPS стендом по ссылке после разрешённой доставки. FIN-10 стал необязательным и не блокирует результат.
- Новые contracts отделены от frozen 34/67/307. Calendar deadline исключён по ответу пользователя «опустим». Редакция 1 и её review/validation сохранены в локальной истории как exact historical snapshots.

Этот документ — единственный действующий план; [HTML-артефакт](current-plan.html) представляет его пользователю. Локальные review и validation проверяют текущую редакцию. Все старые планы отброшены как указания к исполнению и сохранены только в истории. Регистрация плана не создала implementation commits, CI runs, images или deployment.

Публикация плана: 15.10.2026, Europe/Moscow. Пользователь поручил начать реализацию и запушить стабильные правки. Проверка current sources начата; исходные FIN-пункты этим документальным commit не закрыты.

Статус на 16.10.2026, Europe/Moscow: FIN1 доставлен 15.10, см. `completion-stand.json`. Тот же стенд 15.10 обновлён до профиля REG1 (регистрация по почте включена по поручению пользователя). Записи выше сохранены как история и не пересматривались.
