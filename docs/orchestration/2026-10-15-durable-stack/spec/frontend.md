# Подспека G-frontend: umir-frontend

Приведено к решениям C1–C40 главной спеки (раздел 10), 15.10.2026.

Дата: 15.10.2026. Статус: черновик для ревью. Подчинена главной спеке [00-orchestration.md](00-orchestration.md); номера U*, R*, DUR1, G1 — оттуда. Источники: [frontend #14](https://github.com/JustIkra/umir-frontend/issues/14) (копия `scratchpad/plans/umir-frontend-14.md`), отчёт ревью `scratchpad/main-review/frontend.md`. Все пути ниже проверены по `origin/main` `4a6452f`.

## 1. Цель и границы

**Владеет.** Статический SPA (React + TS + Chakra + Vite), его образ (`Dockerfile`, `nginx.conf`, `compose.yaml`, `tests/nginx`), клиентской памятью попыток команд, выходом из аккаунта, показом отказов 429/503 и текстами документов (`src/presentation/documents/content.ts`).

**Не владеет.** Бизнес-правилами, идемпотентностью и хранением команд (владельцы: Warehouse, Serving, Auth), допуском и лимитами (Gateway), проверкой доверенных адресов на стороне Gateway (G-gateway), внешним Nginx Proxy Manager (NPM) и ZeroTier (G-factory). Frontend не получает адресов Postgres, RabbitMQ, Redis и ключей к ним; в сборке и конфигурации их нет. Ролей и миграций БД (C8), подкоманд и generation (C9), режима `memory` в боевом образе (C10), ключей Redis (C17), метрик (C33) у frontend нет. Хранилище браузера — не источник истины: оно держит только чужую (владельца) попытку до терминального ответа.

## 2. Исходное состояние

`origin/main` frontend: `4a6452f`. Группа записывает свежий SHA в журнал. `npm run check`: 889 тестов зелёные (ревью); `test:nginx` в ревью не запускался (нужен Docker), группа запускает его на старте и фиксирует базу.

Ключевые пути:

| Что | Где |
| --- | --- |
| Исполнитель команд, попытки в `Map` | `src/application/commands.ts` (`createCommandRunner`, `restore`, `discard`, `clear`) |
| Поставка и списание: ввод в замыканиях `delivery`, `deliveryZone` | `src/application/warehouse-commands.ts:43-48,72-135` |
| Продукт и блюдо: `lastProduct`, `lastDish` | `src/application/catalog-commands.ts:57-111` (ключи `catalog.product.<r>`, `catalog.dish.<r>`) |
| Хранение заказа (образец) | `src/adapters/browser-storage/order-store.ts` (`umir.order.<user>.<restaurant>`, `sessionStorage`), порт `OrderStorage` в `src/application/restaurant.ts:231` |
| Стирание заказов при выходе | `src/application/order-flows.ts:27-31` (`forget()` → `clearAll()`), вызовы в `src/app/main.tsx:202,226` |
| Выход по GET | `src/presentation/routes.tsx:136`, `src/presentation/auth/logout-page.tsx:23-27` (`useEffect` → `session.signOut()`); ссылки на `/logout`: `app-shell/account-menu.tsx:316`, переход в `app-shell/account-settings-page.tsx:607`, защита несохранённой формы по `pathname === '/logout'` там же `:372` |
| Nginx | `nginx.conf` (109 строк): нет `Cache-Control`, `gzip`, CSP, `nosniff`, `frame-ancestors`; `Referrer-Policy` только в `location /invite/`; `X-Forwarded-For $proxy_add_x_forwarded_for` на строках 23 и 65; динамическое разрешение имени `set $gateway http://gateway:8080` |
| Образ | `Dockerfile`: `nginxinc/nginx-unprivileged:1.30.5-alpine`, `COPY nginx.conf /etc/nginx/conf.d/default.conf`, `USER 101` |
| Тесты Nginx | `tests/nginx/{run.sh,compose.yaml,check.mjs,gateway.mjs}`; корень статики в тесте — заглушка образа, сборки нет |
| Отказы | `src/adapters/api/decode/common.ts:196` (`decodeFailure(status, body)` берёт `details.retryAfterSeconds`, заголовок `Retry-After` не читает), `src/adapters/http/gateway-client.ts:74`, `src/application/failure.ts` (`rate-limited`), `src/application/commands.ts` (`settledOutcomes`: `committed`, `not_committed`, `unavailable` — попытка снимается) |
| Эпохи | `src/application/epochs.ts`, `src/application/models.ts:112` (смена эпохи → «Данные обновились» и stale всех моделей) |
| Политики | `src/presentation/documents/content.ts` (`[N]` на строках 122, 128, 362; таблица `cookieRows` содержит несуществующий ключ `umir.pendingAttempt`) |

Что грузится кроме своего origin (проверено по `index.html`, `dist/` и `src`): только iframe `https://yandex.ru/map-widget/v1/?text=…` в `src/presentation/landing/office-map.tsx` (`yandexWidgetUrl`) и ссылка `https://yandex.ru/maps/` (переход, CSP не касается). Шрифты — `@fontsource` из `/assets/*.woff2`; внешних скриптов, стилей, картинок, `eval` и inline-скриптов в `dist/index.html` нет; `data:image/svg+xml` встречается в бандле (маски и фон иконок). Заготовок prod-readiness для frontend нет (`stalled-persistence.md` его не упоминает).

## 3. Решения

**Принимаются как есть** (issue #14): сохранение попыток поставки, списания, продукта и блюда с `commandID`, телом, рестораном и эпохой; скрытие, а не удаление записей при выходе и смене пользователя; снятие записи только по терминальному ответу владельца; `/logout` по кнопке; Nginx: кеш `index.html` и `/assets/`, gzip, CSP, `nosniff`, `frame-ancestors`, Referrer-Policy (строгий на `/invite/`); 429 с обратным отсчётом; различие «отказ до отправки» и «исход неизвестен»; отсутствие в браузере доступа к БД, брокеру и Redis. Вне объёма из issue: общая разбивка бандла (≈1 МБ одним чанком) — делается отдельно, если понадобится.

**Меняются правилами.**
- R2: `runtimeEpoch` = storage generation. Клиент по-прежнему считает её непрозрачной строкой и отправляет заголовком `X-Umir-Runtime-Epoch`. Логика `RuntimeReset` остаётся: после R2 ответ означает подтверждённую владельцем потерю или откат истории, а не перезапуск процесса, поэтому стирание записи по `RuntimeReset` остаётся верным (замечание ревью п. 6 снимается именно этим). Код не меняется, меняются README (`README.md:48`) и тесты (раздел 7).
- C19: идентификатор экземпляра бывает и в `epoch` SSE, и в курсоре Gateway (случайный boot-nonce, не `INSTANCE_ID`), поэтому `epoch` потока меняется при смене реплики. Клиент уже перечитывает всё при новом `epoch` (`live-sync.ts`: `onChange` и `refresh-required` → `markStale('all')` + `requestResolve`), код не меняется, нужен тест (F2-3). Этот `epoch` не связан с `runtimeEpoch` чтений и заголовком `X-Umir-Runtime-Epoch`: смена реплики не показывает «Данные обновились» и не стирает попытки.
- R4 и C20: SSE закрепляется за репликой Gateway по сессии на уровне Nginx (раздел 4, задача F1-5): `upstream` с `hash … consistent` и `server gateway:8080 resolve`, `zone`, `resolver`. Factory даёт сетевой псевдоним `gateway` обеим репликам и запускает frontend после Gateway; генерируемого `include` у Factory нет. Клиентский `EventSource` и `live-sync` не меняются: чужой экземпляр или разрыв последовательности приходят как `refresh-required`, который клиент уже обрабатывает.
- R11 и C34: Nginx перезаписывает `X-Forwarded-For`, доверяя входящему только от адреса NPM (`UMIR_TRUSTED_PROXY`; парная переменная Gateway — `GATEWAY_TRUSTED_PROXY_CIDRS`, подсеть frontend; обе выставляет factory, `preflight` проверяет). Заменяет «не считать `proxy_add_x_forwarded_for` авторитетным» из issue.
- R16: CSP разрешает один сторонний источник — рамку Яндекс Карт — и `style-src 'unsafe-inline'` ради Emotion.
- R19: срок хранения обращений в политиках берётся из R9 после G1 (задача F2-5).
- R20 и C41: к frontend применимо так: правила «нет записи — нет отправки» нет, потому что опубликованная политика cookie обещает, что при заблокированном хранилище сервис продолжит работать. Недоступное хранилище не блокирует команду, а только предупреждает пользователя; блокирует лишь повреждённая существующая запись (раздел 4, F1-1).

**Сняты R1 (чистый лист).** Перенос данных frontend не касается. Последствие для клиента: записи в `sessionStorage` со старым стендом (старые эпохи) после включения нового стека получат `RuntimeReset` при повторе и будут сняты по обычному пути; отдельной миграции формата не нужно, запись версионируется полем `v`.

**Вне объёма.** Попытки Auth (вход, `logout-all`, команды организаций, регистрация, приглашение, сброс пароля) остаются в памяти и в существующих `sessionStorage`-хранилищах; в них пароль и код не хранятся. Поведение заказа при недоступном хранилище не меняется (C41, F1-1). Замечания ревью 9 (проверка IANA-зоны) и 12 (предел `parseQuantity`) — не в этой подспеке. Секреты, `FLUSH*`, изменение Figma — не затрагиваются.

## 4. Волна 1 — независимые правки

Ветка `fix/durable-w1-frontend`. Контракт DUR1 не трогается. Порядок по зависимостям: F1-1 → F1-2; F1-3, F1-4, F1-5 независимы друг от друга; F1-6 в конце.

### F1-1. Сохранение попыток поставки, списания, продукта и блюда

**Что не так.** Попытка с неизвестным исходом живёт в `Map` исполнителя и в замыканиях (см. раздел 2). После перезагрузки вкладки ввод и `commandID` теряются, экран разрешает повторить с новым `deliveryID`/`commandID`; README:150 прямо это описывает. Заказ уже сохраняется, но `OrderFlowRegistry.forget()` вызывает `clearAll()` для всех пользователей.

**Решение.**
- Новый порт `AttemptStore` (`src/application/attempt-store.ts`) и адаптер `src/adapters/browser-storage/attempt-store.ts` по образцу `order-store`, на `browserSessionStorage()` из `main.tsx`. Ключ: `umir.attempt.<kind>.<userID>.<restaurantID>[.<batchID>]`, `kind` ∈ `delivery | writeoff | product | dish`. Запись:

  ```
  { v: 1, kind, userID, restaurantID, commandID, epoch, startedAt, payload }
  ```
  Полезная нагрузка: поставка — `DeliveryInput` целиком (с `deliveryID`) и `timeZone`; списание — `batchID`; продукт — `ProductInput`; блюдо — `DishInput`. Пароли, cookie, токены, CSRF и прочитанные модели в запись не попадают.
- Порт возвращает `ok | absent | corrupt | unavailable`, а не молча стирает: нераспознанная запись (чужая форма, неверный `v`, `userID`/`restaurantID` не совпали с ключом, количество не `^[1-9][0-9]{0,17}$` и т. п.) даёт `corrupt` и **не удаляется**. Это отличие от `order-store.load`, который стирает повреждённую запись.
- Запись **до отправки**: исполнитель вызывает `store.save` внутри `execute` перед обращением к API (как `keepAttempt` в `order-flow.ts`). Если хранилище недоступно (accessor бросает исключение или `save` не удалось, `unavailable`), команда всё равно отправляется (C41), попытка живёт только в памяти исполнителя, а экран показывает предупреждение: «Браузер не даёт сохранить попытку. Если страница перезагрузится, проверьте результат в истории». Исход команды разбирается как обычно; запись не создаётся, повтор после перезагрузки невозможен и идёт через историю склада.
- Снятие записи: `committed`, `rejected` (определённый отказ: `not_committed`/`unavailable`), `reset` (`RuntimeReset`, `indeterminate`). `unknown` запись сохраняет. Только по этим исходам, никогда по сроку.
- Восстановление: при активной сессии `main.tsx` по записям текущего `userID` вызывает `services.warehouseCommands(r)`/`catalogCommands(r)` для каждого ресторана записей; фабрики читают записи и вызывают `commands.restore(spec, {commandID, epoch, startedAt})` с замыканием по восстановленному вводу. Поэтому попытка видна в W11 и блокирует смену ресторана, даже если страница поставки не открыта. `pendingDelivery()`, `pendingDeliveryTimeZone()`, `pendingWriteoff()`, `pendingProduct()`, `pendingDish()` возвращают восстановленное.
- `forget()` в `order-flows.ts` только гасит потоки (`dispose`) и не вызывает `clearAll`; `OrderStorage.clearAll` и его адаптер удаляются, вместе с вызовом в `main.tsx`. Записи заказа и новых видов других пользователей не читаются: ключи содержат `userID`, `load` проверяет совпадение. Вернувшийся пользователь видит свои записи.
- `corrupt` по ключу блокирует именно эту операцию в этом ресторане: форма закрыта, экран объясняет, что прошлую попытку прочитать нельзя, и ведёт в историю склада. Выход из блокировки (решение раздела 10): повреждённую запись можно явно «отменить» после того, как пользователь подтвердил, что проверил историю склада; порт получает операцию `discardCorrupt` только для `corrupt`. Целую (читаемую) запись так отменить нельзя: она снимается только терминальным ответом владельца. Текст подтверждения и кнопки — простым языком на «вы».
- Хранилище — `sessionStorage`, как у заказа (решение раздела 10). Закрытие вкладки до результата теряет попытку; другая вкладка её не видит. Это принято, смена на `localStorage` не планируется.
- Заказ ведёт себя как сейчас (C41): `order-store` по-прежнему не блокирует отправку при ошибке записи; предупреждение о недоступном хранилище показывается и на экране заказа тем же текстом.
- Блокирует отправку только `corrupt`: повреждённая существующая запись, до явной отмены (см. выше).

**Тесты (сначала красные).**
- `tests/unit/attempt-store.test.mjs`: возврат записи тому же пользователю и ресторану; чужой пользователь и чужой ресторан не читают; повреждённая запись → `corrupt` и остаётся в хранилище; блокированное хранилище (`getItem`/`setItem` бросают) → `unavailable`, исключение наружу не вылетает; два пользователя в одной вкладке.
- `tests/unit/warehouse-commands.test.mjs`, `catalog-commands.test.mjs`: потерянный ответ → «перезагрузка» (новый исполнитель и новые фабрики на том же `Storage`) → повтор уходит с прежним `commandID`, телом, `deliveryID` и эпохой, один вызов API; `unknown` сохраняет, `committed`/отказ/`reset` снимают; `save` бросает → команда всё равно уходит ровно одним вызовом API, предупреждение показано, записи нет; `corrupt` → API не вызывается.
- `tests/unit/attempt-store.test.mjs` (дополнение): `discardCorrupt` удаляет только `corrupt`, на читаемую запись отвечает отказом; экран отмены требует подтверждения.
- `tests/unit/order-flow.test.mjs`: `setItem` бросает → prepare и confirm уходят как сейчас, предупреждение показано.
- `tests/unit/order-flows.test.mjs`: `forget()` не удаляет неразрешённую попытку; выход пользователя A, вход B, возврат A → попытка A восстановлена, B её не видит.
- `tests/unit/session.test.mjs`/`routes.test.mjs`: восстановление запускается после активации сессии и не раньше.

**Готово.** Для поставки, списания, продукта и блюда по тестам: ровно один вызов с исходным `commandID` после перезагрузки; записи не стираются выходом и сменой пользователя; чужие скрыты; хранилище отключено — команда уходит (в том числе заказ) с предупреждением; запись повреждена — отправки нет и новый ID не создаётся, пока пользователь явно не отменит её после подтверждения; в `sessionStorage` нет секретов. README (строки 48, 146, 150) и CHANGELOG обновлены.

### F1-2. `/logout` — только по кнопке

**Что не так.** `LogoutPage` вызывает `session.signOut()` из `useEffect` при загрузке (`logout-page.tsx:23-27`): переход по ссылке или `<iframe>` завершает сессию без действия пользователя.

**Решение.** Открытие `/logout` ничего не отправляет. Если сессия `anonymous`, переход на `/login` (`replace`) без запросов. Если `active` (или `unknown` после проверки), страница «Выйти из аккаунта?» с кнопками «Выйти» и «Остаться» (в `/app`). `signOut()` вызывается только из `onClick` кнопки. Состояния после нажатия (`pending`/`failed`/`active`, «Выйти ещё раз», «Проверить результат») остаются как есть; CSRF-токен и проверка неизвестного исхода не меняются. Ссылки меню и переход из `account-settings-page.tsx:607` по-прежнему ведут на `/logout` (защита несохранённой формы по `pathname` сохраняется). Тексты — простым языком сайта, на «вы».

**Тесты.** `tests/unit/routes.test.mjs` (или новый `logout-page.test.mjs`): рендер `/logout` с активной сессией — ноль вызовов `signOut`; клик по «Выйти» — ровно один; потерянный ответ ведёт на «Не знаем, получилось ли выйти»; анонимный гость уходит на `/login` без вызова. Тест `routes.test.mjs:300` (ссылка меню) остаётся.

**Готово.** GET `/logout` ничего не меняет; клик даёт один `DELETE /api/v2/session` с CSRF; в тестах нет автоматического выхода.

### F1-3. Nginx: заголовки безопасности, кеш, gzip

**Что не так.** См. раздел 2 (`nginx.conf`): устаревший `index.html` из кеша ссылается на удалённый хешированный бандл (`try_files` отдаёт `index.html` вместо JS); бандл ≈1 МБ без сжатия; нет CSP и защиты от встраивания.

**Решение.**
- Общий файл заголовков `security-headers.conf` (копируется в образ, в тестовый compose монтируется по тому же пути). Подключается через `include` в `server` и **повторно** в каждом `location` и именованном `@api_*`, где есть свой `add_header` (иначе nginx сбросит унаследованное): `@api_too_large`, `@api_unavailable`, `@api_timeout`, `/invite/`, новые `/assets/` и `= /index.html`. Все с `always`, чтобы попадали и в 404/413/502/504:
  - `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src https://yandex.ru; manifest-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'`. Без широких подстановок. `connect-src 'self'` покрывает `fetch` и `EventSource`. `style-src 'unsafe-inline'` нужен Emotion/Chakra (R16);
  - `X-Content-Type-Options: nosniff`; `X-Frame-Options: DENY`;
  - `Referrer-Policy: strict-origin-when-cross-origin`; на `/invite/` остаётся `no-referrer` (там вместо общей строки — своя);
  - HSTS здесь не ставится (TLS завершает NPM; это в G-factory).
- Кеш. `location = /index.html` и `/invite/` (отдаёт `index.html` через `rewrite … break`): `Cache-Control: no-cache`. `location /assets/`: `try_files $uri =404;` и `Cache-Control: "public, max-age=31536000, immutable"` **без** `always` (чтобы 404 не получил `immutable`). Остальные корневые файлы (`favicon`, `manifest`, иконки) — `no-cache`. Ответы `/api/` по-прежнему без кеша.
- Сжатие: `gzip on; gzip_vary on; gzip_min_length 1024; gzip_types text/css application/javascript text/javascript image/svg+xml application/manifest+json` **только** в `location /assets/`, `= /index.html`, `/` (статика). Для `/api/` не включать: ответы несут CSRF-токен (BREACH); в SSE уже `gzip off`.
- Прямая проверка источников: перед фиксацией CSP исполнитель собирает образ, открывает лендинг, `/login`, `/app` в браузере (Playwright из отдельной рабочей папки, как сказано в README) и убеждается, что консоль без нарушений CSP, а карта показывается. Рамка внутри виджета проверяется её собственной политикой, наша `frame-src` проверяет только адрес рамки и редиректы; если Яндекс перебросит рамку на другой хост, добавляется этот один хост, не подстановка. Хост проверяется в браузере (решение раздела 10): из России и вне её, результат в журнал группы.
- Журнал: свой `log_format` без `$remote_addr`, без `$http_referer` и без строки запроса (после F1-4 `$remote_addr` — адрес клиента, правило ПДн из раздела 8 главной спеки). Идентификатор в `/invite/<id>` остаётся в пути. Журнал Nginx хранится 14 дней (решение раздела 10; ротацию задаёт factory), логи без ПДн.

**Тесты (сначала красные, `tests/nginx/check.mjs`).** Тестовый compose монтирует каталог-заглушку `tests/nginx/site/` (`index.html`, `assets/app-<hash>.js` > 2 КБ, `assets/app-<hash>.css`) в корень статики. Проверки: `GET /` и `/login` — `no-cache`, CSP, `nosniff`, `X-Frame-Options`, Referrer-Policy; `GET /assets/app-…js` с `Accept-Encoding: gzip` — `Content-Encoding: gzip`, `immutable`, `Vary`; `GET /assets/missing.js` — `404`, без `immutable`, но с заголовками безопасности; `GET /invite/<uuid>` — `no-referrer`, `no-cache`; `413`/`502`/`504` — заголовки безопасности на месте и `no-store`; `/api/v2/...` без `Content-Encoding`; SSE по-прежнему без сжатия и буферизации (существующие проверки). Существующая проверка «`/login` без Referrer-Policy» (`check.mjs`, тест о приглашении) обновляется под общий заголовок.

**Готово.** Тесты Nginx зелёные; в браузере нет нарушений CSP на лендинге (карта на месте), `/login`, `/app`; `npm run check` зелёный.

### F1-4. Nginx: адрес клиента (R11)

**Что не так.** `nginx.conf:23,65`: `X-Forwarded-For $proxy_add_x_forwarded_for` дописывает присланное клиентом значение. Заголовки `Forwarded` и `X-Real-IP` клиента тоже доходят до Gateway.

**Решение.** В `server`: `set_real_ip_from <адрес NPM>; real_ip_header X-Forwarded-For; real_ip_recursive off;` — доверенный пир задаётся параметром развёртывания. Имена параметров (C34): Nginx — `UMIR_TRUSTED_PROXY` (CIDR NPM), Gateway — `GATEWAY_TRUSTED_PROXY_CIDRS` (подсеть frontend); обе выставляет factory, соответствие проверяет `preflight`. Конфигурация становится шаблоном образа: `Dockerfile` копирует её в `/etc/nginx/templates/default.conf.template`, `ENV NGINX_ENVSUBST_FILTER=^UMIR_` и `ENV UMIR_TRUSTED_PROXY=127.0.0.1/32` (значение по умолчанию доверяет только петле, то есть на практике никому); `set_real_ip_from ${UMIR_TRUSTED_PROXY};`. В `location /api/` и в блоке SSE: `proxy_set_header X-Forwarded-For $remote_addr;` (после `real_ip` это адрес клиента при доверенном пире и адрес пира иначе), `proxy_set_header Forwarded "";`, `proxy_set_header X-Real-IP "";`. `X-Forwarded-Proto $scheme` не меняется (Gateway его не использует, README Gateway:500). Если шаблонная подстановка не работает в `nginx-unprivileged:1.30.5` под `USER 101` (проверить сборкой), запасной путь: включаемый файл `trusted-proxy.conf` с `set_real_ip_from`, который G-factory монтирует поверх файла по умолчанию в образе.

**Тесты.** Тестовый compose поднимает два экземпляра frontend: «доверяющий» (`UMIR_TRUSTED_PROXY=0.0.0.0/0`, только для теста) и «недоверяющий» (`192.0.2.0/24`). Заглушка Gateway записывает `x-forwarded-for`, `forwarded`, `x-real-ip`. Запрос с `X-Forwarded-For: 1.1.1.1, 10.9.9.9`: доверяющий отдаёт Gateway ровно `10.9.9.9`; недоверяющий — адрес сокета, а не присланное; `Forwarded` и `X-Real-IP` не доходят; то же для SSE. Строка журнала не содержит адреса клиента.

**Готово.** Присланный клиентом `X-Forwarded-For` не влияет на то, что видит Gateway; адрес NPM приходит параметром; значение по умолчанию безопасно. Сквозную проверку «разные клиенты — разные бюджеты» (бюджеты по клиенту держит Gateway, C12) делает G-factory на реальной цепочке (интерфейс, раздел 6).

### F1-5. Nginx: закрепление SSE за репликой (R4)

**Что не так.** `location ~ ^/api/v2/r/[0-9a-f-]+/live$` использует `set $gateway` и динамический DNS: при двух репликах переподключение попадает на случайную. Имена cookie сессии в Gateway: `__Host-umir_operator_session` (режим `secure`) и `umir_operator_session` (`loopback-development`) (`umir-gateway/internal/operatorhttp/http.go:32-33`). Дефис в имени не позволяет писать `$cookie_<имя>` для `__Host-…`: nginx оборвёт имя переменной на дефисе.

**Решение (C20).**
- В `http`-контексте шаблона:

  ```
  map $http_cookie $umir_sid {
      ~(?:^|;\s*)(?:__Host-)?umir_operator_session=(?<sid>[^;\s]+) $sid;
      default "";
  }
  map $umir_sid $umir_live_key { "" $remote_addr; default $umir_sid; }
  upstream gateway_live {
      zone gateway_live 64k;
      hash $umir_live_key consistent;
      server gateway:8080 resolve;
  }
  ```
  `resolve` в `upstream` доступен в открытом nginx начиная с 1.27.3 и требует `zone` и `resolver`; поддержку проверяет `npm run test:nginx` (и `nginx -t` в закреплённом образе). `resolver 127.0.0.11 valid=10s` уже задан. Имя `gateway` — сетевой псевдоним обеих реплик, который задаёт factory; он разворачивается во все A-записи. Factory запускает frontend после Gateway; генерируемого `include` с `server gateway-a/b` нет.
- Только блок SSE получает `proxy_pass http://gateway_live;` и `proxy_next_upstream error timeout; proxy_next_upstream_tries 2;` (GET без побочных эффектов; смена реплики для клиента — это `refresh-required`). Остальной `/api/` остаётся на `$gateway` и `proxy_next_upstream off` (повтор записи после отправки запрещён).
- Остальное в блоке SSE (`proxy_buffering off`, `gzip off`, чтение 60 с, `Connection ""`) не меняется.

**Тесты.** Заглушка Gateway в тестовом compose запускается в двух репликах (`deploy.replicas: 2`) и отдаёт своё имя узла заголовком `X-Stand-In`. Проверки: один cookie → 10 запросов на один узел; 40 разных значений cookie попадают на оба узла; тот же результат для имени с `__Host-`; запрос без cookie не падает (ключ — адрес); после остановки одной реплики запрос с её cookie доходит до второй; существующие проверки SSE (поток без буфера, курсор без изменений `?cursor=a%2Fb%2Bc`, `502` JSON) зелёные.

**Готово.** Тесты зелёные; `nginx -t` в закреплённом образе проходит; SSE одной сессии идёт на одну реплику при двух.

### F1-6. Мелкие правки

- README `:218`: убрать устаревший абзац «live всегда отвечает 403» (issue #14, строка «Stale/fixed»). Обновить `:48` (смысл `RuntimeReset` по R2) и `:264` (новые заголовки, upstream для SSE).
- `LiveFeed.forget()`: очищать `cursors` в `event-source-feed.ts` при `onSignedOut`/`onUserChanged` (ревью п. 8; тест `live-feed.test.mjs`).
- `src/presentation/documents/content.ts`: `cookieRows` содержит `umir.pendingAttempt`, которого нет в коде. Заменить строки на реальные ключи: `umir.order.*`, `umir.attempt.*` (F1-1), `umir.invite`, `umir.register`, `umir.passwordReset`, с назначением и сроком «до закрытия вкладки или результата». Фразу про блокировку хранилища (`content.ts:298`) переписать: при блокировке сервис продолжает работать, но поставку, списание, заказ и создание продукта или блюда после перезагрузки страницы придётся проверять в истории. Тексты — простым языком.
- Тест на документы: каждый ключ `cookieRows` встречается в коде как префикс ключа.

## 5. Волна 2 — после G1 и DUR1

Frontend не хранит данные, поэтому схем, RLS, outbox/inbox, фоновых работ, Redis и переменных базы здесь нет. Что есть вместо раздела хранения:

**Конфигурация.** `UMIR_TRUSTED_PROXY` (CIDR NPM; F1-4, C34) — единственный параметр развёртывания. Секретов нет, `*_FILE`, DSN и переменных метрик нет (C33). Здоровье (C32): `GET /healthz` — процесс жив; добавляется `GET /readyz` с тем же ответом 200 (раздача статики готова; от Gateway не зависит), `HEALTHCHECK` в `Dockerfile` и healthcheck compose смотрят `/readyz`; подпутей нет. Frontend не имеет generation и экземпляра, поэтому тело — `{"capabilities":{"static":"ok"}}`; имя возможности вносит factory в `configuration.md` (CR, если там иное). Тест `check.mjs`: `/readyz` 200 при остановленной заглушке Gateway.

**Удаляемый код.** `OrderStorage.clearAll` и вызов (уже в F1-1); запасной текст «после перезагрузки проверьте историю» для поставки и списания; `epoch !== expectedEpoch` в `warehouse-commands.ts` остаётся (защита от записи в другое поколение), но комментарий и README описывают поколение, а не перезапуск процесса.

### F2-1. Синхронизация контракта с DUR1

После публикации DUR1 (`2.0.0-DUR.1`) обновить побайтовые копии в `api/` (`backend-v2-public.json` и примеры), таблицу SHA в `api/README.md`, выполнить `npm run api:types`. `npm run api:check` зелёный. Новые коды ошибок, которые декодер ещё не знает, хранятся текстом (поведение `decodeFailure`); их разбор зависит только от `outcome`, не от кода. По C16 429 несёт и заголовок `Retry-After`, и `details.retryAfterSeconds`: `decodeFailure` берёт поле, а при его отсутствии `gateway-client.ts:74` передаёт значение заголовка (тест `gateway-client.test.mjs`, `api-decode.test.mjs`: оба источника, при расхождении берётся поле). `CommandConflict` (C29) в enum уже есть (`backend-v2.d.ts`); `CommandIdentityConflict` в коде не используется, проверить `grep` после синхронизации.

### F2-2. 429 и 503: разбор исходов и показ (C16)

- `429` `RateLimited` с `outcome: not_committed` (C16; `Retry-After` и `details.retryAfterSeconds`): ввод и экран сохраняются, команда не повторяется сама. Тип `CreateOutcome` получает `{kind: 'rate-limited', retryAfterSeconds}`; страницы поставки, продукта, блюда, заказа (`OrderPhase.refused` получает `retryAfterSeconds`) и чтений (`failure.ts` уже даёт `rate-limited`, `read-failure.tsx` показывает общую плашку) показывают «Слишком много запросов. Повторите через N с» с доступным отсчётом (`useNow`, `formatCountdown` из `ui/countdown.ts`, `aria-live`), кнопка активна по истечении срока. Без `retryAfterSeconds` — разумный срок по умолчанию (в духе `defaultWaitSeconds`).
- `503` `ServiceUnavailable` «отказ допуска до отправки» (определённый, `outcome: not_committed` для записи и `unavailable` для чтения; кода `AdmissionUnavailable` нет): `settledOutcomes` уже снимает попытку; пользователю говорят, что ничего не выполнено и что можно повторить, новая отправка идёт с новым `commandID`. Запись из F1-1 снимается.
- `503`/`502`/`504` и разрыв **после** отправки (`outcome: unknown`, ответ без JSON-ошибки, `Malformed`): попытка и запись остаются, «Проверить результат» повторяет прежнюю команду; «определённым» отказом это не считается. Решает поле `outcome` (C16: сбой после отправки сохраняет прежние коды с `unknown`), не статус.
- Лимиты заказа (C15): 400 `InvalidArgument` при более чем 200 позициях (`not_committed`) и 409 `LimitExceeded` при более чем 2000 активных предложений на ресторан (`not_committed`). Клиентской проверки числа позиций нет, решает сервер (числа утверждаются на G1). Тексты на экране заказа (`order-messages.ts`): для 400 — «В заказе слишком много позиций. Уберите часть и попробуйте снова.», для 409 — «Сейчас у ресторана слишком много неоформленных заказов. Подождите немного и попробуйте снова.»; ввод и корзина сохраняются, автоповтора нет, `commandID` не занят (`not_committed`). Тесты в `order-flow.test.mjs`: оба кода → `refused`, без попытки в хранилище. Общий 409 `LimitExceeded` в других местах (квота ресторанов у Auth, C13) показывается общим текстом «Достигнут предел. Обратитесь к администратору организации» через тот же разбор по коду.
- SSE: превышение квоты потока (8 на сессию, 200 на организацию, 2000 на ячейку; предварительные, C36) — это закрытие `EventSource` с ограниченным откатом `3→60 с`, который уже есть; тест фиксирует, что новый бизнес-вызов при этом не создаётся.
- Тесты: примеры DUR1 для 429 (оба источника срока) и 503 (`not_committed` и `unavailable`) в `tests/unit/commands.test.mjs`, `warehouse-commands.test.mjs`, `order-flow.test.mjs`, `failure.test.mjs`; страницы — по одному `*-pages.test.mjs`/`warehouse-ui.test.mjs`. Для каждого: сохранённость ввода, прежний `commandID` при `unknown`, отсчёт, отсутствие автоповтора.

### F2-3. Поколение хранилища (R2)

Код `epochs.ts`, `models.ts`, `commands.ts` не меняется. Тесты фиксируют три случая: (а) сменился идентификатор процесса, поколение прежнее — эпоха та же, «Данные обновились» не показывается, сохранённая попытка повторяется и доходит; (б) сервер отвечает `RuntimeReset` (подтверждённый сброс поколения) — попытка и запись сняты, экран «Не знаем, … загляните в историю», модели ресторана устарели; (в) поколение сменилось между перезагрузкой и повтором — то же, что (б). Четвёртый случай (C19): `epoch` SSE изменился из-за смены реплики (новый boot-nonce) при прежнем `runtimeEpoch` чтений — `live-sync` помечает все модели устаревшими и перечитывает, «Данные обновились» не показывается, попытки не снимаются (`live-sync.test.mjs`, `live-feed.test.mjs`). Проверить README и тексты `order-messages.ts`/`warehouse` на формулировку «сервис перезапустили».

### F2-4. Проверки на реальной цепочке

Совместно с G-factory (волна 3): сценарий «потерянный ответ после фиксации» для поставки, списания, продукта и блюда в браузере по цепочке Frontend/Nginx → Gateway → реальные Warehouse/Serving на Postgres. Frontend выдаёт сценарии (Playwright, вне репозитория, как в README), G-factory запускает.

### F2-5. R19 — сроки хранения в политиках

После G1: добавить в `content.ts` одну константу (например `feedbackRetentionDays`, значение из R9: анонимное обращение — 30 дней по предложению) и подставить во фразу `content.ts:128` и в срок согласия `:362`. `[N]` рядом с «журналами веб-сервера» (`:122`) и «сроком удаления после отзыва» (`:362`, рабочие дни) — не из R9, остаются отметками; их закрывает владелец документа. Если G1 даст для обращений ресторана иной срок (180 дней), в тексте это отдельная строка, потому что `content.ts` описывает только форму лендинга. Тест: фраза содержит число из константы; других `[N]` в этих двух предложениях нет.

## 6. Интерфейсы с другими группами

**Выдаёт.**
- G-gateway: `X-Forwarded-For` с одним адресом клиента (последний элемент входящего от доверенного пира NPM, иначе адрес пира) и без `Forwarded`/`X-Real-IP`; Gateway доверяет заголовку только из CIDR сети frontend (R11).
- G-factory: образ с параметром `UMIR_TRUSTED_PROXY` (CIDR NPM) и `NGINX_ENVSUBST_FILTER`; требование к compose: имя `gateway` разворачивается во все реплики, frontend стартует после готовности Gateway и перезапускается вместе с ним; псевдоним `gateway` обеим репликам (C20); `COMPLETION_NPM_ADDR` → `UMIR_TRUSTED_PROXY` и `UMIR_FRONT_SUBNET` → `GATEWAY_TRUSTED_PROXY_CIDRS` (C34, проверка в `preflight`); какой адрес пира видит frontend за ZeroTier/docker, проверяет G-factory; healthcheck compose по `/readyz` (C32); ротация журнала Nginx 14 дней; сквозная проверка бюджетов на реальной цепочке; HSTS на NPM.
- Всем: сценарии браузерного восстановления (F2-4).

**Потребляет.**
- DUR1 (G-factory): смысл `runtimeEpoch` (R2); `429` `RateLimited` (`Retry-After` и `details.retryAfterSeconds`) и `503` `ServiceUnavailable` «до отправки» против `unknown` после (C16); лимиты заказа: 200 позиций → 400 `InvalidArgument`, 2000 активных предложений → 409 `LimitExceeded` (C15); `CommandConflict` (C29); состояние попытки `Interrupted` (C21) приходит только через Serving и читается как неизвестный исход, отдельной логики во frontend нет; квоты SSE (C36).
- G-gateway (R4, C19): курсор и `epoch` несут boot-nonce и поколение, чужой экземпляр → `refresh-required` или новый `epoch`; клиент перечитывает; реальный `live` на любой реплике. Узкий набор тем live (C39) включается после теста Gateway и Frontend «каждая мутация обновляет нужные экраны»: этот тест frontend пишет совместно с Gateway; до того клиент получает все пять тем, как сейчас.
- G-warehouse/G-serving: повтор прежней команды после перезапуска процесса возвращает исходный результат (идемпотентность в Postgres); `RuntimeReset` только при подтверждённом сбросе поколения. Это нужно для F1-1 после R2, не для её выпуска: в волне 1 перезапуск процесса порождает `RuntimeReset`, и запись корректно снимается.
- G-factory: примеры DUR1 для тестов; CR не требуется, если DUR1 содержит перечисленное.

## 7. Тесты и проверки

- Unit/Bun: `npx -p bun@1.4.3 npm run check` (prettier, `api:check`, `tsc`, ESLint, dependency-cruiser, 889+ тестов, `vite build`). Слои сохраняются: порт `AttemptStore` в `application`, адаптер в `adapters/browser-storage`, `Storage` подставляется в `main.tsx`; презентация не обращается к `sessionStorage`.
- Nginx: `npm run test:nginx` (Docker; при занятых портах переопределить `COMPOSE_PROJECT_NAME`, `FRONTEND_PORT`). Новые проверки — F1-3, F1-4, F1-5.
- Сборка образа: `docker build .` (CI уже собирает) и `nginx -t` на итоговом шаблоне.
- Контрактные тесты хранилища «memory и Postgres» к frontend не применимы: хранилища БД нет. Аналог — один набор тестов `AttemptStore` для адаптера на `Storage` и для заглушки в памяти (только тестовый код, C10), чтобы порт был определён поведением. `make integration` (C37) к frontend не относится.
- `integration`-теги, `-race`, `FLUSH*`, переменные `UMIR_TEST_*`: не применимы (нет Go, нет БД).
- Браузер (вручную или Playwright вне репозитория): лендинг с картой, `/login`, `/app` — без нарушений CSP; сценарий F2-4.
- Сценарии матрицы отказов #17, которыми владеет frontend: потеря ответа после фиксации и перезагрузка (поставка, списание, продукт, блюдо, заказ); выход и возврат того же пользователя; смена пользователя; отключённое и повреждённое хранилище; 429 с `Retry-After`; `503` «до отправки» против `unknown`; `502`/`504` от Nginx для записи (`outcome: unknown`); разрыв SSE и повторное подключение на другую реплику (`refresh-required`); подделка `X-Forwarded-For`. Серверные части (крах между фиксацией и ответом, брокер, БД) принадлежат владельцам.

## 8. Приёмка группы

- [ ] Свежий `origin/main` записан в журнал; `npm run check` и `npm run test:nginx` зелёные до и после правок, числа тестов в отчёте.
- [ ] F1-1: по тестам одна бизнес-операция после перезагрузки с прежним `commandID` для поставки, списания, продукта, блюда; выход и смена пользователя не стирают записи, чужие скрыты, вернувшийся пользователь получает свои; при недоступном хранилище команда уходит с предупреждением; повреждённая запись не стирается молча, блокирует операцию и отменяется только явно после подтверждения; в хранилище нет секретов.
- [ ] F1-2: GET `/logout` не вызывает API; клик — один `DELETE` с CSRF; потерянный ответ даёт «Не знаем, получилось ли выйти».
- [ ] F1-3: заголовки CSP, `nosniff`, `X-Frame-Options`, Referrer-Policy присутствуют на обычных, статических и ошибочных ответах; `/invite/` — `no-referrer`; `index.html` — `no-cache`; `/assets/` — `immutable`, 404 без `immutable`; gzip на статике, нет gzip на `/api/` и SSE; CSP без подстановок, карта и шрифты работают.
- [ ] F1-4: подделанный `X-Forwarded-For` не доходит до Gateway; `Forwarded`/`X-Real-IP` не доходят; параметр `UMIR_TRUSTED_PROXY` работает; журнал без адреса клиента.
- [ ] F1-5: при двух репликах заглушки одна сессия идёт на одну; без cookie не падает; `upstream … resolve` с `zone` и `resolver` проходит `nginx -t` и `test:nginx`.
- [ ] F1-6: README, CHANGELOG, `cookieRows` соответствуют коду; курсоры ленты сброшены при выходе.
- [ ] После G1: F2-1 (контракт DUR1 синхронизирован, `api:check` зелёный), F2-2 (429 с отсчётом по `details.retryAfterSeconds` или `Retry-After`, `503` определённый и неизвестный различаются, тексты 400 `InvalidArgument` и 409 `LimitExceeded` заказа, ввод и `commandID` сохраняются), F2-3 (три случая поколения и смена `epoch` SSE), F2-5 (срок в политиках единой константой).
- [ ] В сборке и конфигурации нет адресов и ключей Postgres, RabbitMQ, Redis.
- [ ] Новые тексты интерфейса — простым языком сайта, на «вы», без технических терминов; CHANGELOG «Не выпущено» обновлён; финальное ревью ветки Opus — Ready.

## 9. Риски и вопросы к оркестратору

Закрыты разделом 10 главной спеки («Вопросы групп: Frontend») и решениями C*:

1. Закрыто: повреждённая запись попытки (F1-1) — явная «отмена» после подтверждения пользователем, что он проверил историю; для читаемой записи нельзя.
2. Закрыто: C41 — при недоступном хранилище команда (поставка, списание, каталог, заказ) отправляется с предупреждением; блокирует только повреждённая запись.
3. Закрыто: хост рамки Яндекса — проверяется в браузере (F1-3); `frame-src https://yandex.ru` с жёстким CSP.
4. Закрыто: журнал Nginx — без ПДн, 14 дней; `[N]` для журналов веб-сервера в политике берётся отсюда (F2-5).
5. Закрыто: C20 — `upstream … resolve` на псевдоним `gateway`, factory даёт псевдоним и порядок запуска.
6. Закрыто: C34 — `UMIR_TRUSTED_PROXY` и `GATEWAY_TRUSTED_PROXY_CIDRS`, значения и проверка у factory (`preflight`).
7. Закрыто: `sessionStorage` вместо `localStorage` (решение раздела 10).
8. Закрыто: C15 — 200 позиций (400 `InvalidArgument`) и 2000 активных предложений (409 `LimitExceeded`); числа утверждаются на G1.
9. Закрыто: C16 — `Retry-After` и `details.retryAfterSeconds` оба; паритет заказа решён пунктом 2.

Открытых вопросов к оркестратору нет. Остаточный риск: если factory задаст в `configuration.md` иное имя возможности frontend для `/readyz`, группа правит ответ (CR не нужен).
