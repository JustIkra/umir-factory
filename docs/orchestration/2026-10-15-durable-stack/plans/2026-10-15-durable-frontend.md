# План: frontend, волна 1 перехода на durable-стек

Подспека: `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/frontend.md`, раздел 4 (F1-1 … F1-6) плюс C41 и C42 главной спеки. Репозиторий `umir-frontend`, ветка `fix/durable-w1-frontend`, worktree `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-frontend`, база `origin/main` `4a6452f`.

**Цель.** Попытки поставки, списания, продукта и блюда переживают перезагрузку вкладки; `/logout` выходит только по кнопке; Nginx получает безопасные заголовки, кеш, gzip, журнал без адреса клиента, доверенный прокси, закрепление SSE, `/readyz`; политика cookie называет реальные ключи.

**Архитектура.** Пять слоёв по `AGENTS.md`: порт `AttemptStore` в `src/application`, адаптер в `src/adapters/browser-storage`, сборка в `src/app/main.tsx`, презентация не трогает `sessionStorage`. Nginx: шаблон `/etc/nginx/templates/default.conf.template` с фильтром `NGINX_ENVSUBST_FILTER=^UMIR_`.

**Проверки.** Тесты на Bun всегда `npx -p bun@1.4.3 …`. Тесты Nginx: `COMPOSE_PROJECT_NAME=umir-durable-fe-nginx FRONTEND_PORT=18190 npm run test:nginx` (порты 5432, 6379, 3003, 8001 чужие). Тексты интерфейса: простой язык сайта, «вы» и «мы», без технических слов (`commandID`, `sessionStorage`, «попытка» и т. п. на экран не выводятся; допустимо «перезагрузка страницы», «история склада»). Каждая задача: сначала красный тест, затем код.

**Правила для исполнителей.** README.md, CHANGELOG.md и подспеку не править (их ведёт задача T9). Коммитить только свои пути (`git add <пути>`), Conventional Commits на английском, без футеров `Co-Authored-By` и generated-by, `git diff --check` перед коммитом. Если `index.lock` занят другой задачей, повторить. Субагентов не запускать. Нагрузка на машине высокая (load average ~50): таймауты 5 с в `tests/quality/architecture.test.mjs` — шум, перезапускать файл отдельно.

## Две дорожки (параллельно)

- Дорожка A (Nginx, файлы `nginx.conf`, `Dockerfile`, `compose.yaml`, `tests/nginx/**`): N1 → N2 → N3 → N4.
- Дорожка B (`src/**`, `tests/unit/**`): L1, A1 → A2 → A3 → A4; затем T9.

## Задачи

### N1. Заголовки, кеш, gzip, журнал, `/readyz` (F1-3, C42)

Файлы: `nginx.conf`, новый `security-headers.conf`, `Dockerfile`, `tests/nginx/{compose.yaml,check.mjs}`, новый каталог `tests/nginx/site/`.
- Красные проверки в `check.mjs` (раздел «Тесты» F1-3 подспеки): `/` и `/login` — `no-cache`, CSP, `nosniff`, `X-Frame-Options`, Referrer-Policy; `/assets/app-*.js` с gzip — `Content-Encoding: gzip`, `immutable`, `Vary`; `/assets/missing.js` — 404 без `immutable`, но с заголовками безопасности; `/invite/<uuid>` — `no-referrer`, `no-cache`; 413/502/504 — заголовки безопасности и `no-store`; `/api/v2/…` без `Content-Encoding`; SSE без сжатия; журнал без адреса клиента, `Referer` и строки запроса. Тест о приглашении обновить под общий заголовок.
- `/readyz`: 200, тело `{"capabilities":{"static":"ok"}}`, `Content-Type: application/json`, без кеша, `access_log off`; проверка при остановленной заглушке Gateway (контейнер `gateway` остановить на время проверки). `HEALTHCHECK` в `Dockerfile` смотрит `/readyz`; `/healthz` остаётся.
- Заглушка статики в тесте: `tests/nginx/site/` (`index.html`, `assets/app-<hash>.js` > 2 КБ, `assets/app-<hash>.css`, один из корневых файлов вроде `favicon.svg`), монтируется в `/usr/share/nginx/html`; `security-headers.conf` монтируется по тому же пути, что в образе.
- Код: `security-headers.conf` (CSP из подспеки дословно, `nosniff`, `X-Frame-Options: DENY`, Referrer-Policy `strict-origin-when-cross-origin`, все `always`), `include` в `server` и повторно в каждом `location`/`@api_*` со своим `add_header` (`/invite/` без общей Referrer-Policy: там свой `no-referrer`; обеспечить отсутствие дубля заголовка). Кеш и gzip по подсказке подспеки. `log_format` без `$remote_addr`, `$http_referer` и строки запроса (в пути `/invite/<id>` идентификатор допустим; строку запроса отрезать, например логировать `$uri`).
- Коммит: `feat(nginx): add security headers, caching, gzip and readiness`.

### N2. Доверенный прокси (F1-4, C34)

Файлы: `nginx.conf` → шаблон, `Dockerfile`, `tests/nginx/**`.
- Красные проверки: два экземпляра frontend в тестовом compose («доверяющий» `UMIR_TRUSTED_PROXY=0.0.0.0/0`, «недоверяющий» `192.0.2.0/24`, второй порт — `FRONTEND_PORT_UNTRUSTED`, по умолчанию 18191); заглушка Gateway записывает `x-forwarded-for`, `forwarded`, `x-real-ip`; запрос с `X-Forwarded-For: 1.1.1.1, 10.9.9.9` → доверяющий отдаёт ровно `10.9.9.9`, недоверяющий — адрес сокета; `Forwarded` и `X-Real-IP` не доходят; то же для SSE; журнал без адреса клиента.
- Код: `Dockerfile` копирует конфигурацию в `/etc/nginx/templates/default.conf.template`, `ENV NGINX_ENVSUBST_FILTER=^UMIR_`, `ENV UMIR_TRUSTED_PROXY=127.0.0.1/32`; `set_real_ip_from ${UMIR_TRUSTED_PROXY}; real_ip_header X-Forwarded-For; real_ip_recursive off;`; в `/api/` и SSE `proxy_set_header X-Forwarded-For $remote_addr; Forwarded ""; X-Real-IP "";`. Проверить, что шаблонная подстановка работает под `USER 101` в закреплённом образе; иначе запасной путь `trusted-proxy.conf` (описан в подспеке) и запись `Ruling`.
- Тестовый compose монтирует шаблон в `/etc/nginx/templates/` (подстановку делает entrypoint образа), а не готовый `conf.d`.
- Коммит: `feat(nginx): rewrite X-Forwarded-For from a trusted proxy`.

### N3. Закрепление SSE за репликой (F1-5, C20)

Файлы: шаблон Nginx, `tests/nginx/**`.
- Красные проверки: заглушка Gateway в двух репликах, каждая отдаёт `X-Stand-In` с именем узла; один cookie → 10 запросов на один узел; 40 разных значений cookie попадают на оба узла; то же для `__Host-umir_operator_session` и `umir_operator_session`; запрос без cookie не падает; после остановки одной реплики запрос с её cookie доходит до второй; прежние проверки SSE зелёные.
- Код: `map $http_cookie $umir_sid`, `map $umir_sid $umir_live_key`, `upstream gateway_live { zone gateway_live 64k; hash $umir_live_key consistent; server gateway:8080 resolve; }` (текст в подспеке), только SSE-блок получает `proxy_pass http://gateway_live; proxy_next_upstream error timeout; proxy_next_upstream_tries 2;`. Остальной `/api/` на `$gateway` с `proxy_next_upstream off`. Если `resolve` в `upstream` не поддерживается закреплённым образом — не придумывать обход, остановиться и сообщить с выводом `nginx -t`.
- Коммит: `feat(nginx): pin live streams to one gateway replica`.

### N4. Проверка CSP в браузере (F1-3, R16)

Без изменений кода, кроме CSP при необходимости. Собрать образ (`docker build`), запустить с заглушкой Gateway (префикс `umir-durable-`), открыть Playwright (`npx playwright` из отдельной рабочей папки в scratchpad, не из репозитория) на лендинге, `/login`, `/app` (с заглушкой, которая отвечает 401 на `/api/v2/me` и пр.), собрать консольные нарушения CSP и сетевые запросы; убедиться, что карта (рамка Яндекса) показывается и что в запросах рамки нет других хостов верхнего уровня, кроме `yandex.ru`. Результат с датой, положением (из России/вне — что возможно с этой машины) и списком наблюдаемых хостов записать в `/Users/yahi3123/projects/umir-2026/.superpowers/sdd/2026-10-15-durable/frontend/csp-check.md`. Если нужен другой хост в `frame-src`, добавить ровно его. Коммит CSP-правки (если была): `fix(nginx): allow the map frame host`.

### L1. `/logout` только по кнопке (F1-2)

Файлы: `src/presentation/auth/logout-page.tsx`, тесты (`tests/unit/routes.test.mjs` или новый `logout-page.test.mjs`).
- Красные тесты: `/logout` с активной сессией — ноль вызовов `signOut`; клик «Выйти» — ровно один; потерянный ответ ведёт на «Не знаем, получилось ли выйти»; анонимный гость уходит на `/login` без вызова; `routes.test.mjs:300` остаётся.
- Код: страница «Выйти из аккаунта?» с кнопками «Выйти» и «Остаться» (в `/app`); `signOut()` только из `onClick`. Состояния после нажатия не менять. Для `unknown` сессии — проверить через существующий `session.check()` по месту, не придумывая новых состояний.
- Коммит: `fix(frontend): sign out only after the button press`.

### A1. Порт и адаптер хранилища попыток (F1-1, ядро)

Файлы: `src/application/attempt-store.ts`, `src/adapters/browser-storage/attempt-store.ts`, `tests/unit/attempt-store.test.mjs` (+ при необходимости помощник в `tests/unit/support`).
- Порт: `load`/`list` возвращают `ok | absent | corrupt | unavailable`; `save` возвращает `ok | unavailable`; `remove`; `discardCorrupt` (только для `corrupt`; на читаемую запись отвечает отказом). Ключ `umir.attempt.<kind>.<userID>.<restaurantID>[.<batchID>]`, `kind` ∈ `delivery | writeoff | product | dish`. Запись `{ v: 1, kind, userID, restaurantID, commandID, epoch, startedAt, payload }`; полезная нагрузка по виду — по подспеке (поставка: `DeliveryInput` и `timeZone`; списание: `batchID`; продукт: `ProductInput`; блюдо: `DishInput`). Проверка формы строгая (в том числе количество `^[1-9][0-9]{0,17}$`, совпадение `userID`/`restaurantID`/`batchID` с ключом, неверный `v`); нераспознанное = `corrupt`, запись не удаляется. Операция перечисления записей пользователя для восстановления (по префиксу ключа с `userID`, чужие не читаются).
- Тесты: подспека F1-1 («`attempt-store.test.mjs`»): тот же пользователь и ресторан, чужой пользователь и ресторан, повреждённая запись остаётся, `getItem`/`setItem` бросают → `unavailable` без исключения, два пользователя в одной вкладке, `discardCorrupt`; один набор поведения для адаптера на `Storage` и для заглушки в памяти (только тесты).
- В записи нет паролей, cookie, токенов, CSRF и моделей чтения; тест на отсутствие лишних полей.
- Коммит: `feat(frontend): add a durable store for command attempts`.

### A2. Попытки в командах склада и каталога (F1-1)

Файлы: `src/application/{warehouse-commands,catalog-commands}.ts`, `tests/unit/{warehouse-commands,catalog-commands}.test.mjs`; в `commands.ts` менять только если без этого нельзя.
- Красные тесты (подспека F1-1): потерянный ответ → «перезагрузка» (новый исполнитель и фабрики на том же `Storage`) → повтор уходит с прежним `commandID`, телом, `deliveryID` и эпохой, один вызов API; `unknown` сохраняет запись, `committed` / определённый отказ / `reset` снимают; хранилище недоступно → команда уходит ровно одним вызовом API, записи нет, фабрика сообщает «не сохранено» (поле/метод, который читает экран); `corrupt` → API не вызывается, новый ID не создаётся, фабрика сообщает «запись повреждена» и предоставляет `discardCorrupt()` (после чего операция снова доступна).
- Код: фабрики принимают `userID` и `AttemptStore`; `save` внутри `execute` до вызова API (как `keepAttempt` в `order-flow.ts`); при создании фабрики читаются записи своего ресторана и вызывается `commands.restore(spec, {commandID, epoch, startedAt})` с замыканием по восстановленному вводу; `pendingDelivery()`, `pendingDeliveryTimeZone()`, `pendingWriteoff()`, `pendingProduct()`, `pendingDish()` возвращают восстановленное. Контракт `CreateOutcome` не ломать (добавлять только то, что нужно).
- Коммит: `feat(frontend): keep delivery, write-off and catalog attempts across reloads`.

### A3. Сборка, выход и заказ (F1-1)

Файлы: `src/app/main.tsx`, `src/application/order-flows.ts`, `src/application/restaurant.ts`, `src/adapters/browser-storage/order-store.ts`, `src/application/order-flow.ts` (только предупреждение), `tests/unit/{order-flows,order-flow,order-store,session,routes}.test.mjs`.
- Красные тесты: `forget()` только гасит потоки и не вызывает `clearAll`; `OrderStorage.clearAll` и адаптер удалены; выход A, вход B, возврат A → попытка A восстановлена, B её не видит; `setItem` бросает → prepare и confirm уходят как сейчас, поток сообщает «не сохранено»; восстановление записей текущего пользователя запускается после активации сессии и не раньше (по записям вызываются `warehouseCommands(r)`/`catalogCommands(r)` каждого ресторана записей), попытка видна в W11.
- Код: `main.tsx` создаёт `createAttemptStore(browserSessionStorage())`, передаёт его и `userID` фабрикам; фабрики и `commands.clear()` при выходе/смене пользователя не стирают записи.
- Коммит: `feat(frontend): restore saved attempts after sign-in`.

### A4. Экраны: предупреждение и повреждённая запись (F1-1)

Файлы: `src/presentation/warehouse/{delivery-page,stock-page,expiry-page}.tsx`, `src/presentation/catalog/{new-product-page,new-dish-page}.tsx`, экран заказа, тесты `warehouse-ui.test.mjs`, `catalog-pages.test.mjs`, `order-flow`-страницы.
- Красные тесты: хранилище недоступно → предупреждение «Браузер не даёт сохранить попытку. Если страница перезагрузится, проверьте результат в истории» (формулировку перечитать в духе «вы»: например, «Браузер не даёт запомнить эту отправку. Если страница перезагрузится, проверьте результат в истории склада»; для заказа — «в истории заказов»), форма работает; `corrupt` → форма закрыта, объяснение «Прошлую отправку прочитать нельзя» и ссылка в историю склада; кнопка «Отменить прошлую отправку» ведёт к подтверждению («Я проверил(а) историю склада»), только после подтверждения запись снимается и форма открывается; читаемую запись так отменить нельзя.
- Тексты на «вы» и «мы», без технических слов.
- Коммит: `feat(frontend): warn when attempts cannot be saved and block on damaged records`.

### T9. Мелкие правки и документы (F1-6)

Файлы: `README.md` (`:48`, `:146`, `:150`, `:218`, `:264` и описание новых заголовков, шаблона, upstream), `CHANGELOG.md` («Не выпущено»), `src/adapters/…/event-source-feed.ts` (очистка `cursors` при `onSignedOut`/`onUserChanged`, тест `live-feed.test.mjs`), `src/presentation/documents/content.ts` (`cookieRows`, фраза про блокировку хранилища, тест «каждый ключ `cookieRows` встречается в коде как префикс ключа»).
- Коммиты: `fix(frontend): clear live cursors on sign-out`, `docs(frontend): describe real storage keys in the cookie policy`, `docs: describe durable attempts, headers and readiness`.

## Итог

После T9: полный `npx -p bun@1.4.3 npm run check`, `npm run test:nginx`, `docker build .`, `git diff --check`; финальное ревью ветки (Opus), один цикл правок; push, draft PR; отчёт `wave-1-report.md`.

## Самопроверка плана по чек-листу

- Покрытие подспеки: F1-1 → A1–A4; F1-2 → L1; F1-3 → N1, N4; F1-4 → N2; F1-5 → N3; F1-6 → T9; C41 → A2–A4; C42 → N1. Журнал без ПДн — N1; нечёткие места (хост Яндекса, подстановка шаблона, `resolve`) вынесены в явные ветки.
- Заглушек и «потом» нет; имена методов описаны ролями, точные сигнатуры решает исполнитель в рамках портов раздела 4 подспеки.
- Порядок: A1 → A2 → A3 → A4 (A3 нужен фабрикам из A2; A4 использует их флаги); дорожки A и B не пересекаются по файлам.
