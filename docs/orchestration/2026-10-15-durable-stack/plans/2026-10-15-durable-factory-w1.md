# Factory W1: независимые правки FIN1-профиля — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Выполнить пункты W1.1–W1.10 подспеки factory для работающего профиля `compose.completion.yaml` (ротация и перенастройка без удаления томов, бэкап и восстановление, CI профиля, дайджесты, ключи файлами, hardening, transport healthcheck и сеть, актуальные документы, явный `WAREHOUSE_API_MODE`, мелочи).

**Architecture:** Расширяем существующие `scripts/completion/prepare.mjs` (поколения пакета, ротации, lock образов, ключи-файлы), `compose.completion.yaml` (private-init с поколениями, hardening, health, сети), добавляем shell-скрипты `backup.sh`/`restore.sh`, node-скрипты `check-expiry.mjs`/`transport-health.mjs`, Go-команды `completion-acceptance` и `lock-images` в `tools/assembly-checks`, задание CI `completion-stand`. Каждая правка начинается с падающего теста.

**Tech Stack:** Node 24 (`node --test`), POSIX sh, Docker Compose v2, openssl, Go 1.26.9 (`GOWORK=off GOTOOLCHAIN=go1.26.9`), GitHub Actions.

**Spec:** Подспека `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/factory.md` (раздел 4, «Волна 1», таблица W1.1–W1.10, а также разделы 2, 3, 7, 8) и главная спека `/Users/yahi3123/projects/umir-2026/docs/superpowers/specs/2026-10-15-durable-stack/00-orchestration.md` (разделы 7, 8, 10; C14, C28, C35). Исполнитель читает строку своего пункта в подспеке целиком.

## Global Constraints

- Worktree `/Users/yahi3123/projects/umir-2026/_wt/durable/umir-factory-w1`, ветка `fix/durable-w1-factory`, база `17fe41f`. Другие worktree и чужие контейнеры не трогать. Параллельно группа factory-w0 правит `docs/durable`, `docs/api` (DUR1), `reference/durable`, `scripts/durable-test-infra.sh`, ADR 0002: эти файлы не трогать. Возможный конфликт только в `README.md`, `CHANGELOG.md`, `Makefile`, `scripts/go-quality.sh`, `.github/workflows/ci.yml`: правки в них делать минимальными, локальными вставками.
- Go: каждую команду с `GOWORK=off GOTOOLCHAIN=go1.26.9`; инструменты `make tools`. Проверки: `make check`, `make check-contract`, `make go-readiness`, `bash tests/infra/*.sh`.
- Docker в тестах: только проекты `umir-completion-test-*` и `umir-completion-ci-*`, уборка только своего проекта; порты 5432 и 6379 и контейнеры `umir-prod-*`, `rksi-*` и прочие чужие не трогать.
- Секреты не печатать и не коммитить; тестовые секреты синтетические. Развёртывание на ВМ и правки живого стенда запрещены.
- Тексты README/CHANGELOG простым языком, без отладочных терминов в пользовательских разделах; CHANGELOG «Не выпущено» на английском в стиле соседних записей (файл так ведётся).
- Коммиты: Conventional Commits на английском, без `Co-Authored-By` и generated-by футеров, `git diff --check` перед коммитом. Несовместимое изменение (`compose.yaml` порт Gateway, W1.9) — `!` и миграция в CHANGELOG.
- Не добавлять режимов `memory`, `*_STORAGE`. DUR1 и ротация сервисных ключей/JWT вне объёма.
- Тестовые утверждения не отключать и не пропускать; если инструмент недоступен локально (например, Docker), тест падает, а не пропускается.
- Профиль `compose.completion.yaml` остаётся совместимым с образами REG1 из `scripts/completion/sources.json`.

## Review Focus

- Повторный `prepare` без флагов на пакете после `--rotate-leaf`/`--reconfigure` не должен менять ключи, пароль первого пользователя и не должен падать из-за нового поколения (идемпотентность).
- Обрыв `private-init` посреди копирования или смены поколения: повторный запуск достраивает, том не блокируется и не получает смесь двух поколений с `.ready` нового.
- `--rotate-ca` по фазам: на каждом шаге `ca.crt` клиента доверяет текущему серверному листу.
- `--reconfigure` не должен тихо менять ключи или первоначального пользователя; смена SMTP-файлов должна менять `binding`, но не секреты сервисов.
- Бэкап с неверным/пустым ключом, с повреждённым архивом, с чужим проектом в манифесте; повторный запуск `restore.sh` в непустой проект должен отказывать.
- Lock образов: образ вне lock, тег без дайджеста, SHA источника не совпадает с меткой OCI `revision`.
- Уборка `completion-acceptance` после ошибки на любом шаге; проект с чужим префиксом никогда не получает `--volumes`.
- Значения ключей не попадают в `docker compose config`, `docker inspect` (Feedback без ключей в окружении).

---

## File Structure

- Изменяются: `scripts/completion/prepare.mjs`, `compose.completion.yaml`, `compose.yaml`, `compose.dev.yaml`, `scripts/completion/sources.json`, `tests/infra/completion/prepare.test.mjs`, `tests/examples/completion/compose.mail.yaml`, `.github/workflows/ci.yml`, `Makefile` (только цель `check-contract`), `README.md`, `CHANGELOG.md`, `docs/business-core.md`, `docs/current-plan.md`, `docs/completion-stand.json` (только согласование с lock, если нужно), `tools/assembly-checks/main.go`, `.env.example`.
- Создаются: `scripts/completion/check-expiry.mjs`, `scripts/completion/transport-health.mjs`, `scripts/completion/backup.sh`, `scripts/completion/restore.sh`, `scripts/completion/compose.sh`, `scripts/completion/images.lock.json`, `tests/infra/completion-backup.test.sh`, `tests/infra/docs.test.mjs`, `tests/infra/completion/private-init.test.mjs`, `tests/infra/completion/expiry.test.mjs`, `tests/infra/completion/compose.test.mjs` (Node-тесты Compose), `tools/assembly-checks/{completion_acceptance.go,completion_acceptance_test.go,lock_images.go,lock_images_test.go,completion_compose_test.go}`, `tests/http/cmd/acceptance` (дополнение теста UA1 в новом файле `ui_amendment_contract_test.go`).
- Каждый Node-тест запускается через `node --test`; `make check-contract` расширяется на новые файлы.

---

### Task 1: W1.10 и W1.9 — явный режим склада, воспроизводимость UA1, Feedback как необязательный источник, привязка порта

**Files:**
- Modify: `compose.yaml` (порт Gateway `127.0.0.1`), `compose.dev.yaml`, `scripts/completion/sources.json`, `scripts/completion/prepare.mjs` (чтение `optionalServices`), `compose.completion.yaml:163` (единый пин Feedback), `tests/infra/completion/prepare.test.mjs`, `Makefile`, `README.md`, `CHANGELOG.md`, `.env.example`.
- Create: `compose.legacy-public.yaml`, `tests/http/cmd/acceptance/ui_amendment_contract_test.go`, `tests/infra/completion/compose.test.mjs`.

**Interfaces:**
- Produces: `tests/infra/completion/compose.test.mjs` с помощником `composeJSON(files, env)` (запускает `docker compose ... config --format json` с синтетическим окружением и возвращает объект); последующие задачи 6, 7, 8 добавляют в него проверки. Синтетическое окружение берётся из `prepare()` на временном пакете (как в существующем тесте «base Compose has one edge»).

- [ ] **Step 1 (W1.10):** в `compose.test.mjs` написать падающий тест: для `compose.yaml`, `compose.yaml`+`compose.dev.yaml`, `compose.completion.yaml` (с окружением из `prepare()`) у сервиса `warehouse` задан `WAREHOUSE_API_MODE` явно: `legacy` в legacy-профилях, `v2` в completion. Запустить: падает для `compose.yaml`.
- [ ] **Step 2:** добавить `WAREHOUSE_API_MODE: legacy` в `compose.yaml` и `compose.dev.yaml` (в dev — отдельным `environment`, чтобы тест на одиночном dev-файле тоже не терял значение). `compose.auth.yaml` и `compose.feedback.yaml` Warehouse не запускают: в тест включить проверку, что в них нет сервиса `warehouse` (иначе проверять и их). Тест зелёный.
- [ ] **Step 3 (W1.9 привязка порта):** падающий тест: при `UMIR_BIND=0.0.0.0` порт Gateway в `compose.yaml` остаётся на `127.0.0.1`; `compose.legacy-public.yaml` (новый, только `ports: !override` Gateway на `${UMIR_BIND:-127.0.0.1}:${UMIR_API_PORT:-8081}:8080`) открывает его явно. Реализовать; убедиться, что существующие Go/shell проверки Compose (`tests/infra/http-compose-checks.sh`, `tools/assembly-checks/*compose_integration_test.go`, `check-images`) остаются зелёными — если они ожидают `UMIR_BIND` у Gateway, обновить их в том же коммите. Обновить `.env.example` и README (безопасный путь запуска — `compose.completion.yaml`).
- [ ] **Step 4 (W1.9 UA1):** по образцу `tests/http/cmd/acceptance/completion_contract_test.go:129` написать `TestUIAmendmentCompositionIsReproducibleAndPreservesInputs` (запуск `tests/http/tools/compose-ui-contract.mjs` во временном каталоге, побайтовое сравнение с закоммиченными составными файлами UA1, неизменность входов). Прочитать, как скрипт `compose-ui-contract.mjs` принимает входы, и повторить способ из теста FIN1. Тест должен падать, если выход не воспроизводится (проверить мутацией временной копии). Расширить регулярное выражение в `Makefile` `check-contract`: `Test(Registration|Completion|UIAmendment)`.
- [ ] **Step 5 (W1.9 Feedback):** падающий тест в `prepare.test.mjs`: `sources.json` содержит `optionalServices.feedback` с SHA, `compose.env` содержит `COMPLETION_FEEDBACK_SHA` и `COMPLETION_FEEDBACK_IMAGE` из него; проверка чистого checkout при `--sources` выполняется для feedback только если каталог `<sources>/feedback` существует. Жёсткий SHA в `compose.completion.yaml` (`:163`) заменить обязательной переменной `COMPLETION_FEEDBACK_IMAGE` с запасным значением из `compose.env` (в Compose значения по умолчанию не должно оставаться, чтобы пин жил в одном месте). Пин Feedback в `sources.json` взять тот же, что сейчас в `compose.completion.yaml` (`136a88a…`); расхождение с `main` Feedback `fa59b63` записать в журнал как вопрос, пин не менять без поручения. Про имена хранилища (`journal.jsonl` vs `feedback.json`) оставить комментарий в README: legacy-профиль и completion-профиль используют разные тома и не смешиваются.
- [ ] **Step 6:** `make check-contract`, `node --test tests/infra/completion/*.test.mjs`, `GOWORK=off GOTOOLCHAIN=go1.26.9 make check-http` и затронутые Go-проверки зелёные. Запись в CHANGELOG (`!` для порта Gateway, миграция: `-f compose.legacy-public.yaml`). Коммиты по логическим группам: `fix(compose): set WAREHOUSE_API_MODE explicitly in legacy profiles`, `test(contract): add UA1 reproducibility test`, `fix(compose)!: bind legacy gateway to loopback`, `fix(completion): move Feedback pin into sources`.

---

### Task 2: W1.8 — устаревшие документы

**Files:**
- Create: `tests/infra/docs.test.mjs`
- Modify: `README.md` (строки около 178, 218, 276, 373; раздел локальной сборки; карта «профиль → контракт → потребитель»), `docs/business-core.md` (строки 9, 19, 25), `docs/current-plan.md` (около 205), `Makefile` (`check-contract` добавляет `node --test tests/infra/docs.test.mjs`), `CHANGELOG.md`.

**Interfaces:**
- Produces: карта «профиль → канонический контракт → кто потребляет» в README (раздел с заголовком `## Профили и контракты`); тест проверяет её наличие.

- [ ] **Step 1:** прочитать строки подспеки W1.8 и найти в README/docs указанные места по тексту (номера строк приблизительные). Написать `tests/infra/docs.test.mjs`: запрещённые фразы («Публичной OpenAPI» применительно к `openapi.yml`, «no public self-signup route», «Публичная сборка на ВМ ещё не развёрнута», «пять репозиториев», «C2 approval is required before implementation», «интерфейс ещё не реализован в main frontend», «хранилища только в памяти» в смысле текущей сборки — точные фразы взять из файлов), README содержит `umir-auth` и `umir-feedback` в разделе локальной сборки, README ссылается на `registration-v1-composed-` как каноническую для развёртывания, `openapi.yml` и `compose.yaml` помечены «исторический/legacy», все локальные markdown-ссылки README и `docs/*.md` разрешаются в существующие файлы, в README есть требование R12 (одна ВМ или синхронизация часов, допуск `iat` нулевой), `docs/current-plan.md` содержит дату 15.10 и «FIN1 доставлен 15.10, см. `completion-stand.json`». Запустить: красный.
- [ ] **Step 2:** исправить документы на месте, не переписывая историю `current-plan.md` (только добавить дату и статус). `docs/observability.md` не менять. README на русском, простым языком.
- [ ] **Step 3:** тест зелёный, `make check-contract` зелёный; коммит `docs: refresh stale README and business-core statements`.

---

### Task 3: W1.1a — поколения пакета, ротации и перенастройка в `prepare.mjs`, `check-expiry.mjs`

**Files:**
- Modify: `scripts/completion/prepare.mjs`, `tests/infra/completion/prepare.test.mjs`
- Create: `scripts/completion/check-expiry.mjs`, `tests/infra/completion/expiry.test.mjs`

**Interfaces:**
- Produces (используют задачи 4, 8, 9): `prepare({..., rotateLeaf, rotateCA: 'trust'|'serve'|'retire', reconfigure, rollback, now})` и CLI-флаги `--rotate-leaf`, `--rotate-ca --phase trust|serve|retire`, `--reconfigure`, `--rollback`. Возвращаемый объект получает поле `generation` (целое, начиная с 1). `bundle.json` получает `generation`, `tls: {caNotAfter, leafNotAfter}` (ISO 8601 UTC), `previous` (описание предыдущего поколения для `--rollback`). Дерево пакета: `<dir>/{auth,transport,trust,health}/` как раньше; каждый `.ready` содержит две строки: `completion-private-v2 generation=<N>` и `tree-sha256=<hex>`, где SHA — по отсортированному списку `<относительный путь>\0<sha256 файла>\n` по всем файлам цели без `.ready` (формат записать в комментарии и использовать в задаче 4 и в private-init одинаково). Прежние поколения хранятся в `<dir>/.generations/<N>/<target>/` (для отката).
- Produces: `scripts/completion/check-expiry.mjs --directory <bundle> [--live host:port ...] [--now <ISO>] [--warn-days 30]`, код выхода 0 / 3 (срок < warn) / 2 (ошибка чтения); печатает JSON `{ca, leaf, daysLeft}` без секретов.

- [ ] **Step 1: падающие тесты** в `prepare.test.mjs` (используют `fixture()` и `openssl x509 -noout -enddate -serial`):
  1. Первый `prepare`: CA ≈ 10 лет (3650 дн.), лист ≈ 1 год, серийный номер листа случайный (два пакета различаются, не равен 1), `bundle.json.generation === 1`, `.ready` содержит поколение и SHA дерева.
  2. `--rotate-leaf`: новый лист под тем же CA (тот же `ca.crt` байт в байт), новый серийный номер, ключи сервисов и `auth/secrets/password` не изменились, `generation` +1, `trust/ca.crt` не изменился; `openssl verify -CAfile` проходит.
  3. Срок определяется с подменой времени: `check-expiry.mjs --now <за 29 суток до конца листа>` выходит с 3, `--now <за 31 сутки>` — с 0; срок записан в `bundle.json`.
  4. `--reconfigure` принимает смену `registration`, SMTP-файлов (и их содержимого) и origin/режима cookie: `auth/config.json` и binding обновлены, поколение +1, ключи сервисов и `initialUser` не изменились; отвергает смену `initialUser`, ключей (изменённый `inputs.json` с другим `initialUser.login` → ошибка).
  5. `--rotate-ca --phase trust`: `trust/ca.crt` содержит старый и новый CA, сервер ещё под старым CA; `--phase serve`: новый лист под новым CA, `ca.crt` содержит оба; `--phase retire`: только новый CA. Тест на каждом шаге проверяет `openssl verify` листа по `ca.crt` клиента (`trust/ca.crt`, `health/ca.crt`, а у Auth — `auth/secrets/*` лист/ключ согласованы) — момента, когда клиент не доверяет работающему серверу, нет. Порядок фаз вне последовательности (`serve` без `trust`) отвергается.
  6. `--rollback` возвращает предыдущее поколение (содержимое и SHA) и увеличивает номер поколения? — решение: откат создаёт поколение N+1 с содержимым N−1 (номер не убывает, чтобы `private-init` не принял его за старое). Тест это проверяет.
  7. Повторный `prepare` без флагов после каждой операции идемпотентен: не меняет файлы, `reused: true`; `--rotate-*` и `--reconfigure` взаимно исключают друг друга.
  Запустить `node --test tests/infra/completion/prepare.test.mjs tests/infra/completion/expiry.test.mjs`: все новые падают.
- [ ] **Step 2: реализация.** `tls()`: CA 3650 дней, лист 365, `-set_serial 0x<16 случайных байт, старший бит 0>` (для CA тоже случайный, `-x509 -set_serial`). Поколения: функции `writeGeneration`, `treeSha`, `applyOperation`. Запись нового поколения идёт во временный каталог `<dir>/.next`, затем переименования; обрыв оставляет `.next`, повторный запуск `prepare` отказывает с понятным сообщением либо удаляет незавершённое `.next` (выбрать удаление и тестом проверить). Бинding: `--reconfigure` допускает смену `registration`, содержимого/путей SMTP файлов, origin, cookieMode; `binding.project` и `initialUser` фиксируются отдельно (смена отвергается). Серийный номер листа ≠ 1. Существующие пакеты со старым форматом `.ready` (`completion-private-v1`, без `generation`) читаются как поколение 0 и обновляются первой же операцией, обычный `prepare` их принимает (тест на обратную совместимость с форматом v1: собрать пакет вручную в тесте). Инвентарь `inventory()` исключает `bundle.json` и `.generations`.
- [ ] **Step 3:** `check-expiry.mjs` читает `tls/server.crt`, `ca.crt` пакета (`openssl x509 -noout -enddate`) и при `--live` делает TLS-подключение `node:tls` без проверки цепочки лишь для чтения срока листа, сравнивает со сроком в пакете; `--now` инъекция времени; выход по пороге; без вывода секретов.
- [ ] **Step 4:** все тесты зелёные; `make check-contract` включает `expiry.test.mjs` (и позже `private-init.test.mjs`) — расширить цель. Раздел README «Сертификаты и перенастройка» (команды, порядок перезапуска: `prepare` → `docker compose up -d --force-recreate private-init` → `restart transport auth`; откат; сроки CA 10 лет и листа 1 год; ссылка на `check-expiry.mjs`) — после задачи 4, где известно точное поведение. Коммиты: `feat(completion): issue CA for 10 years and leaf for 1 year with random serials`, `feat(completion): add bundle generations, leaf/CA rotation, reconfigure and rollback`, `feat(completion): add certificate expiry check`.

---

### Task 4: W1.1b — `private-init` применяет поколения

**Files:**
- Modify: `compose.completion.yaml` (сервис `private-init`), `README.md` (раздел сертификатов), `CHANGELOG.md`
- Create: `tests/infra/completion/private-init.test.mjs`

**Interfaces:**
- Consumes: формат `.ready` и `treeSha` из Task 3 (две строки `completion-private-v2 generation=<N>` / `tree-sha256=<hex>`), каталог пакета `/bootstrap/<target>/` (только чтение), тома `/targets/<target>`.
- Produces: скрипт в `command` сервиса `private-init`; тест извлекает его из YAML (`docker compose config --format json` → `services.private-init.command[2]`, либо парсинг YAML) и выполняет в `sh` на временных каталогах (подставляя `/bootstrap` и `/targets` через переменные-префиксы или `sed`; на macOS без `sha256sum`/`chown` тест подкладывает в `PATH` обёртки-шимы). Скрипт в `compose.completion.yaml` не должен содержать конструкций, которых нет в busybox `sh` из `alpine`.

- [ ] **Step 1: падающие тесты** `private-init.test.mjs` (цели `auth transport trust health`, временные `bootstrap` и `targets`, поколения готовит `prepare()` из Task 3):
  1. Пустой том → копирует, владелец/права (проверка через шим `chown`: вызывается с `65532:65532`; режимы 700/600), `.ready` последним и содержит поколение/SHA.
  2. Тот же номер и тот же SHA → без изменений, успех.
  3. Бóльшее поколение → применяет: после выполнения содержимое тома равно дереву нового поколения, `.ready` новый, предыдущее поколение сохранено в `/targets/<t>/.previous` (или аналог) для отката.
  4. Меньшее поколение, либо то же поколение с другим SHA → отказ (ненулевой код), том не изменён.
  5. Прерванное первое копирование (есть файлы, нет `.ready`) → достраивает и завершает; том, в котором есть чужие файлы и нет маркера намерения, тоже достраивается так же (вместо `ls -A` блокировки; решение записать как Ruling в журнал).
  6. Прерванная смена поколения (имитировать: оставить `.next`/маркер намерения и половину файлов) → повторный запуск завершает без смеси двух поколений под `.ready` нового.
  7. Тело скрипта из YAML совпадает с выполняемым (тест не держит собственной копии).
  Запустить: падают.
- [ ] **Step 2: реализация.** Переписать скрипт `private-init` на поколения: для каждой цели читать `.ready` из `/bootstrap/<t>` и из тома; логика по пунктам 1–6; запись в `/targets/<t>/.next-<N>`, затем пофайловые `mv`, `.ready` последним; перед сменой копия текущего в `.previous`. `network_mode: none`, `user: 0:0`, образ как есть (пин и hardening — Tasks 5 и 7). Служебные подкаталоги тома (`.next-*`, `.previous`) не должны попадать в `diff`/в сервисы: убедиться по Compose-монтированиям, что сервисы читают только известные пути (`transport` монтирует `subpath: conf`).
- [ ] **Step 3:** тесты зелёные (локально должно работать и на Linux, и на macOS). README: порядок применения нового поколения на живом стенде (`prepare` с флагом → `up -d --force-recreate private-init` → `restart transport auth`), откат (`--rollback` и повтор). CHANGELOG. Коммит: `feat(completion): apply private bundle generations atomically in private-init`.
- [ ] **Step 4 (в рамках Task 9):** проверка на живом демо-проекте (`--rotate-leaf` без удаления томов) входит в `completion-acceptance`, здесь только локальный ручной прогон по желанию.

---

### Task 5: W1.4 — дайджесты вместо тегов, `lock-images`

**Files:**
- Create: `scripts/completion/images.lock.json`, `tools/assembly-checks/lock_images.go`, `tools/assembly-checks/lock_images_test.go`
- Modify: `scripts/completion/prepare.mjs` (`composeEnvironment`), `tools/assembly-checks/main.go` (команда `lock-images`, флаг `--lock` у `check-images`), `tools/assembly-checks/images.go`, `compose.completion.yaml` (вспомогательные образы), `tests/examples/completion/compose.mail.yaml`, `tests/infra/completion/prepare.test.mjs`, `tests/infra/completion/compose.test.mjs`, `docs/completion-stand.json` (если требуется согласование), `CHANGELOG.md`, `README.md`.

**Interfaces:**
- Produces: формат `images.lock.json`:
  `{"profile": "REG1", "services": {"<service>": {"sourceSHA","indexDigest","platformDigest","configDigest","revision"}}, "auxiliary": {"<name>": {"image": "alpine:3.23", "ref": "alpine:3.23@sha256:…", "indexDigest", "platformDigest"}}}`
  (форма сервисов — как `images` в `docs/completion-stand.json`; `platform` — `linux/amd64`, запись `"platform": "linux/amd64"` на верхнем уровне). Сервисы: `auth warehouse serving gateway frontend`; необязательно `feedback` (из `sources.json.optionalServices`, Task 1). Вспомогательные: `alpine`, `nginx`, `node`, `mailpit`.
- Produces: `umir-checks lock-images --factory-dir <abs> [--verify] [--write]`: по реестру (через `docker buildx imagetools inspect --raw`/`docker manifest inspect` и `docker buildx imagetools inspect <ref> --format '{{json .}}'`, обёрнутому в `commandExecutor`, чтобы тесты работали на двойниках) создаёт или проверяет lock: метка OCI `org.opencontainers.image.revision` в конфиге образа равна `sourceSHA`, `platformDigest` входит в индекс, `configDigest` совпадает. `umir-checks check-images --factory-dir <abs> --lock` проверяет, что все `image:` в `compose.completion.yaml` и `tests/examples/completion/compose.mail.yaml` (после `docker compose config`) содержат `@sha256:` и совпадают с lock.
- Produces: `prepare.mjs` читает `scripts/completion/images.lock.json`; `compose.env` содержит `COMPLETION_<S>_IMAGE=ghcr.io/justikra/umir-<s>:sha-<rev>@sha256:<platformDigest>` и `COMPLETION_<S>_SHA=<rev>`; сервис из `sources.json`, которого нет в lock (или с другим `sourceSHA`), → `prepare` отказывает.

- [ ] **Step 1: падающие тесты.** (a) `prepare.test.mjs`: `compose.env` ссылается на дайджесты из lock; сервис без записи в lock → ошибка `/lock/i`; (b) `compose.test.mjs`: ни одного `image:` без `@sha256:` в `compose.completion.yaml` и в `compose.mail.yaml` при синтетическом окружении (включая профиль feedback); (c) Go: `TestLockImages…` на двойниках исполнителя: создание lock из ответов реестра, проверка расхождения метки revision, дайджеста платформы вне индекса, отсутствующей записи; `TestCheckImagesLock…`.
- [ ] **Step 2: реализация Go.** Команда пишется в стиле существующих (`runWithExecutor`, `parseOptions`, `writeFailure`); правки `unknown command` и текст ошибки «expected …» в `main.go`. Слои `go-arch-lint` не нарушать (`make arch`).
- [ ] **Step 3: создать lock по реестру** выполнением `lock-images --write` с реальной сетью: сервисы — те SHA, что в `sources.json`; Platform `linux/amd64`. Значения дайджестов для сервисов из `docs/completion-stand.json` должны совпасть с записанными (`platformDigest`, `indexDigest`, `configDigest`); расхождение — остановиться и записать в журнал (это реальный сигнал, не править `completion-stand.json` молча). Вспомогательные образы закрепить: `alpine:3.23`, `nginxinc/nginx-unprivileged:1.30.5-alpine`, `node:24.21.0-alpine`, `axllent/mailpit:v1.31.5` в форме `name:tag@sha256:<индексный дайджест>` (мультиархитектурный индекс, чтобы работало и на arm64, и на amd64; решение записать как Ruling). Feedback из `sources.json.optionalServices` — тоже в lock.
- [ ] **Step 4: реализация Compose/prepare.** Вспомогательные образы в `compose.completion.yaml` и `compose.mail.yaml` заменить на `name:tag@sha256:…` (значения брать из lock; тест Go `check-images --lock` сверяет). `composeEnvironment()` берёт ссылки из lock.
- [ ] **Step 5:** `docs/completion-stand.json` согласован с lock там, где совпадает SHA (проверка в Go-тесте `check-images --lock` или в `compose.test.mjs`). Все тесты и `make check` зелёные. CHANGELOG; README (как обновлять lock: `lock-images --write` и ревью). Коммиты: `feat(assembly): add image lock and lock-images command`, `feat(completion): pin all images by digest`.

---

### Task 6: W1.5 — сервисные ключи файлами

**Files:**
- Modify: `scripts/completion/prepare.mjs` (`env/<service>.env` → `keys/<имя>`), `compose.completion.yaml`, `tests/infra/completion/prepare.test.mjs`, `tests/infra/completion/compose.test.mjs`, `README.md`, `CHANGELOG.md`
- Create: `scripts/completion/compose.sh`

**Interfaces:**
- Consumes: поколения и `.ready` из Task 3/4 (новая цель `keys` в private-init — расширить список целей `auth transport trust health keys`), `inventory()`.
- Produces: каталог пакета `keys/<service>/<ИМЯ_ПЕРЕМЕННОЙ>`... — решение исполнителя записать в журнал; требование: каждый ключ отдельным файлом 0600, Compose монтирует их `:ro`. Для Feedback переменные `FEEDBACK_GATEWAY_KEY_FILE`, `FEEDBACK_AUTH_DISCOVERY_KEY_FILE`; для Warehouse, Serving, Gateway `*_FILE` пока не поддерживаются (C14: делают их группы в своей волне 1), поэтому `env_file` для них остаётся, и в `compose.test.mjs` есть явный список `keysStillInEnvironment = ['warehouse','serving','gateway']`, сокращаемый по мере перепина. `scripts/completion/compose.sh` — обёртка над `docker compose --env-file <bundle>/compose.env -f compose.completion.yaml "$@"`, принимающая `--bundle`, отказывающаяся выполнять `config` без `--quiet` или `--format json` вместе с значениями ключей (т.е. `config` только с `--quiet`).

- [ ] **Step 1: падающие тесты.** В Compose-тесте при профиле `feedback` в `services.feedback.environment` нет значений ключей и нет ключей `FEEDBACK_GATEWAY_KEY`/`FEEDBACK_AUTH_DISCOVERY_KEY`, есть `*_FILE` и монтирования `:ro`; в `prepare.test.mjs` ключи лежат файлами 0600, содержимое равно прежним hex-ключам (миграция: прежние `env/feedback.env` у уже подготовленных пакетов не ломают `prepare` повторно — пакет со старым форматом читается и обновляется операцией поколения (если решено), либо `prepare` отказывает явно с инструкцией; выбрать и протестировать); `compose.sh config` без `--quiet` завершается ошибкой и не печатает ключей.
- [ ] **Step 2: реализация.** Список ключей для Feedback пишется как файлы в целевой каталог `keys` и копируется private-init (или монтируется из `auth`-подобного тома — выбрать минимально); пути в контейнере `/run/umir-keys/<имя>`.
- [ ] **Step 3:** тесты зелёные, README (ключи файлами, список «ещё в окружении»), CHANGELOG. Коммит: `feat(completion): deliver Feedback service keys as files`.

---

### Task 7: W1.6 и W1.7 — hardening, лимиты, healthcheck transport, сеть front

**Files:**
- Modify: `compose.completion.yaml`, `tests/infra/completion/compose.test.mjs`, `README.md`, `CHANGELOG.md`
- Create: `scripts/completion/transport-health.mjs`, `tools/assembly-checks/completion_compose_test.go`

**Interfaces:**
- Produces: в `compose.completion.yaml` блоки `x-hardening` (`read_only: true`, `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, `pids_limit`) и `x-logging` (`json-file`, `max-size: 10m`, `max-file: "3"`), `mem_limit` по сервисам (стартовые: auth 256m, gateway 256m, warehouse 512m, serving 256m, frontend 64m, transport 64m; feedback 128m), `tmpfs` для `/tmp` и `/var/cache/nginx` (и прочих путей, нужных nginx-unprivileged); непривилегированный пользователь у всех долгоживущих сервисов (`user` задан или образ нероот — для образов проверить `docker image inspect` `Config.User`). Исключения (сервис не стартует под `read_only`) — комментарий в YAML и таблица в README. Сеть: `front` (`internal: true`, явная подсеть из переменной `UMIR_FRONT_SUBNET` с запасным значением, не пересекающимся с ZeroTier 172.25.222.0/24 и docker0 172.17.0.0/16, например `172.29.16.0/24`; записать как Ruling) содержит только `gateway` и `frontend`; `frontend` выходит из `default` (остаётся `front` и `edge`); `gateway` в `default` и `front`. `transport-health.mjs <host> <ports…> --ca <path> [--now]` проверяет TLS-рукопожатие по CA пакета на портах 8442–8445 с проверкой имени `transport` и срока, не требует бэкендов; healthcheck `transport` его использует (`node` в образе nginx нет — выбрать реализуемый вариант: либо отдельный сервис-проверка `transport-ready` по образцу `auth-ready` на `node:24.21.0-alpine@…`, либо `openssl s_client` в образе nginx, если он там есть; решение записать как Ruling; `auth` ждёт `service_healthy` соответствующего).

- [ ] **Step 1: падающие тесты.** `compose.test.mjs` и `completion_compose_test.go` (Go: `docker compose … config --format json` с синтетическим окружением, в стиле существующих `*_compose_integration_test.go`) проверяют: каждый долгоживущий сервис (`transport`, `auth`, `auth-ready`, `warehouse`, `serving`, `gateway`, `frontend`, `feedback` под профилем) имеет `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, `mem_limit`, `pids_limit`, `logging.options`, не root; у `transport` (или его проверки) есть healthcheck; `auth.depends_on` содержит `service_healthy` для проверки транспорта; `frontend` не в `default` и не может достичь `warehouse:8082`; `gateway` в обеих сетях; `front` — `internal: true` с подсетью. Однократные задания (`private-init`) исключения, но получают `network_mode: none`, `cap_drop` и т.п. где применимо. Юнит-тест `transport-health.mjs` в `expiry.test.mjs`-стиле: поднять `node:tls`-сервер с листом из `prepare()` на временном порту, проверить успех, неверное имя, просроченный лист (с `--now`), неверный CA.
- [ ] **Step 2: реализация** скрипта и Compose. Проверить `up --wait` на демо-проекте локально (требует образов; arm64: образы amd64 идут через эмуляцию — если это не запускается локально, прогнать только `config` и отметить в журнале, что `up` проверяется в CI-задании Task 9). Таблицу «сервис → mem_limit, pids_limit, измеренное по `docker stats`» заполнить измерением там, где демо поднимается; иначе пометить значения «стартовые, замер в CI-артефакте».
- [ ] **Step 3:** README: границы доверия (mTLS не включаем, решение отдельно), таблица бюджетов, исключения hardening. CHANGELOG. Коммиты: `feat(completion): harden containers and bound resources`, `feat(completion): add transport health and isolate frontend network`.

---

### Task 8: W1.2 — бэкап и восстановление

**Files:**
- Create: `scripts/completion/backup.sh`, `scripts/completion/restore.sh`, `tests/infra/completion-backup.test.sh`
- Modify: `scripts/completion/prepare.mjs` (`--update-images` требует `--backup-receipt <файл>`), `tests/infra/completion/prepare.test.mjs`, `README.md`, `CHANGELOG.md`

**Interfaces:**
- Produces: `backup.sh --project <p> --bundle <dir> --key-file <file> --out <dir>`: проверяет проект (`umir-completion-*`), `docker compose stop auth` (не `down`; запоминает состояние Auth до и возвращает его после, если он был запущен), архивирует том `<project>_auth-state` (через временный контейнер `alpine@sha256:…` из lock, том read-only) и отдельно приватный каталог пакета (без `.generations` — решение исполнителя; обосновать), шифрует каждый архив ключом из файла вне архива (инструмент `openssl enc -aes-256-cbc -pbkdf2` — есть на ВМ, обоснование и ограничения в README; целостность — SHA-256 открытого и шифрованного архива в манифесте плюс проверка при восстановлении), пишет `manifest.json` (SHA-256 открытого и шифрованного архивов данных и ключей раздельно, проект Compose, время UTC, состояние Auth на момент копии, версия формата) и квитанцию `receipt.json` (SHA манифеста, проект, время, путь). Архивы данных (`auth-state`) и ключей (пакет) — раздельные файлы с раздельными SHA. `restore.sh --project <p> --bundle-dir <new dir> --from <backup dir> --key-file <file>`: только в пустой проект (нет томов `<project>_auth-state`, нет каталога пакета), проверяет манифест и SHA до расшифровки, расшифровывает, сверяет SHA открытого, неверный ключ → отказ без записи, создаёт том и пакет, права и владелец прежние.
- Consumes: поколения/`bundle.json` (Task 3) для проверки проекта; pinned alpine (Task 5).
- Produces: `prepare.mjs --update-images --backup-receipt <файл>`: квитанция не старше 24 часов (подмена времени через `now` в `prepare()` для теста), совпадает по проекту, подпись SHA манифеста в квитанции корректна по файлу манифеста рядом; без неё — отказ.

- [ ] **Step 1: падающие тесты.** `prepare.test.mjs`: `--update-images` без квитанции, со старой (>24 ч), с чужим проектом → отказ; с корректной → проходит. `tests/infra/completion-backup.test.sh` (bash, нужен Docker; временный проект `umir-completion-test-<rand>`, создаёт том `auth-state` вручную через `docker volume create`/`docker run alpine` с сентинелом из случайных байт, без запуска сервисов или с минимальным контейнером): сентинел → backup → удаление тома → restore → тот же SHA-256 сентинела; архив не содержит сентинела открытым текстом (`grep -c` по шифрованному файлу); неверный ключ отвергнут и ничего не создано; повреждённый шифрованный архив (SHA манифеста) отвергнут; `restore` в непустой проект отвергнут; уборка в `trap` только своего проекта (`docker volume rm` по префиксу проекта). Скрипт теста завершает ненулевым кодом на любой ошибке, использует `set -euo pipefail`.
- [ ] **Step 2: реализация.** Скрипты на bash с `set -euo pipefail`, без печати ключей, права файлов 0600, каталог 0700, `umask 077`. Операции с Docker — только на ресурсы указанного проекта.
- [ ] **Step 3:** тесты зелёные. README: хранение раздельное (архивы данных и ключей в разных местах), расписание cron (рекомендация), RPO ≤ 24 ч, RTO ≤ 1 ч, команды; замер учения на копии демо-стенда (вход и чтение после восстановления) выполняется в Task 9 `completion-acceptance`, фактическое время RTO записать после прогона (если Docker-стенд локально не поднимается — отметить, что замер берётся из CI-артефакта). CHANGELOG. Коммиты: `feat(completion): add encrypted backup and restore for Auth state`, `feat(completion): require a fresh backup receipt for image updates`.

---

### Task 9: W1.3 — `completion-acceptance` и задание CI `completion-stand`

**Files:**
- Create: `tools/assembly-checks/completion_acceptance.go`, `tools/assembly-checks/completion_acceptance_test.go`
- Modify: `tools/assembly-checks/main.go`, `.github/workflows/ci.yml`, `tests/infra/completion/compose.test.mjs` (при необходимости), `README.md`, `CHANGELOG.md`, `AGENTS.md` (одна строка о новой команде, если уместно)

**Interfaces:**
- Consumes: `prepare-mail.mjs`, `prepare.mjs` (Tasks 3–7), `seed.mjs --demo`, `tests/examples/registration/acceptance.mjs`, `backup.sh`/`restore.sh` (Task 8), `check-expiry.mjs` и `transport-health.mjs`, `lock-images --verify`.
- Produces: `umir-checks completion-acceptance --factory-dir <abs> [--feedback] [--evidence-dir <abs>]`. Порядок шагов (зашит в Go-тест порядка команд на двойниках `commandExecutor`):
  1. проект `umir-completion-ci-<rand>` (генерируется через инжектируемый источник случайности);
  2. `prepare-mail.mjs` во временный каталог `…/umir-completion-mail`;
  3. `prepare.mjs --cookie-mode loopback-development --origin http://127.0.0.1:<port> …`;
  4. `docker compose … config --quiet`;
  5. `pull` точных образов;
  6. `up -d --wait` (вместе с `compose.mail.yaml`, и с `--profile feedback` + синтетическими провайдерами `tests/http/cmd/feedback-fixture` для варианта `--feedback`);
  7. `seed.mjs --demo`;
  8. `tests/examples/registration/acceptance.mjs`;
  9. проверка W1.7: функциональный `/healthz` бэкендов через transport;
  10. W1.1: `prepare.mjs --rotate-leaf` → `up -d --force-recreate private-init` → `restart transport auth` → проверка: имена и идентификаторы томов `auth-state`, `auth-config` неизменны (`docker volume inspect`), прежняя сессия действует, вход работает, `check-expiry.mjs` показывает новый срок; затем `--reconfigure` (например, `registration` меняет значение) и `--rollback`;
  11. W1.2: `backup.sh` → `restore.sh` в пустой проект `umir-completion-ci-<rand2>` → вход и чтение; фиксируется время восстановления;
  12. JSON-свидетельство в `--evidence-dir` (`completion-acceptance-<variant>.json`: шаги, исход, времена, имена проектов, без секретов);
  13. уборка (в `defer`/при ошибке на любом шаге): `docker compose -p <project> down`, с `--volumes` только если имя проекта начинается с `umir-completion-ci-`; каталоги временные удаляются.
- Produces: `.github/workflows/ci.yml`: в `check` добавляются шаги `docker compose -f compose.completion.yaml config --quiet` (с синтетическим окружением из `prepare.mjs` на временных данных или явным набором переменных), `lock-images --verify`, `node --test tests/infra/completion/*.test.mjs tests/infra/docs.test.mjs`, `bash tests/infra/completion-backup.test.sh`; новое задание `completion-stand` (`needs: check`, `ubuntu-24.04`, запуск на push/PR), два варианта матрицы (`feedback: false|true`), артефакт со свидетельствами (`actions/upload-artifact` — взять уже используемый в репозитории SHA действия или закрепить известный полный SHA действия; если нельзя проверить SHA, использовать `gh api` для разрешения тега).

- [ ] **Step 1: падающие Go-тесты** (`completion_acceptance_test.go`, двойники команд записывают порядок): проект с префиксом `umir-completion-ci-`; порядок шагов выше; уборка вызывается при ошибке на каждом из шагов (табличный тест по индексу шага, где двойник возвращает ошибку); `--volumes` только для префикса `umir-completion-ci-` (отказ запускать с другим префиксом/пустым именем); свидетельство без секретов; `--feedback` добавляет профиль и фикстуру. Запустить: красные.
- [ ] **Step 2: реализация** в стиле `http.go`/`auth.go` (`runWithExecutor`, `parseOptions`, `writeFailure`); команда `completion-acceptance` в `main.go`. Реальный запуск шагов с пробросом в `osExecutor`.
- [ ] **Step 3: локальный прогон** `umir-checks completion-acceptance` на этой машине (Docker, образы amd64 на arm64 — по возможности; если эмуляция не запускает сервисы, записать это в журнал и положиться на CI). Ошибки чинить в соответствующих задачах (коммит исправлений к ним).
- [ ] **Step 4: CI** (см. выше). Проверить YAML (`python3 -c 'import yaml…'` или `actionlint`, если есть). Задание проверяется после push (один раз смотрим статус); намеренный слом `private-init` (проверка вручную в ветке) в отчёт: вместо отдельного коммита выполнить локально, чтобы `completion-acceptance` краснел, результат записать в журнал.
- [ ] **Step 5:** README (как запустить `completion-acceptance` локально, что делает CI-задание), CHANGELOG. Коммиты: `feat(assembly): add completion-acceptance command`, `ci: run the completion stand profile`.

---

### Task 10: Финальный проход — README, CHANGELOG, согласованность

**Files:**
- Modify: `README.md`, `CHANGELOG.md`, `AGENTS.md` (при необходимости), `.env.example`

- [ ] **Step 1:** привести в порядок разделы README, добавленные задачами 1–9: порядок (профили и контракты; сертификаты и перенастройка; бэкап и восстановление; бюджеты ресурсов; границы доверия; проверка `completion-acceptance`; требование одной ВМ R12). Удалить дубли. Проверить ссылки `tests/infra/docs.test.mjs`.
- [ ] **Step 2:** полный прогон: `make tools`, `make check`, `make check-contract`, `make go-readiness`, `node --test tests/infra/completion/*.test.mjs tests/infra/docs.test.mjs`, `bash tests/infra/*.sh`; числа тестов записать в журнал. `git diff --check`.
- [ ] **Step 3:** коммит `docs: finalize completion operations guide`.

---

## Self-Review

1. **Покрытие подспеки:** W1.1 — Tasks 3, 4 (+9); W1.2 — Task 8 (+9); W1.3 — Task 9; W1.4 — Task 5; W1.5 — Task 6; W1.6, W1.7 — Task 7; W1.8 — Task 2; W1.9 — Tasks 1 (три подпункта) ; W1.10 — Task 1. Приёмка после W1 (раздел 8): тесты — все задачи; ротация листа/reconfigure/backup на демо — Task 9; блоки W1.6/W1.7 и дайджесты — Tasks 5, 7; `docs.test.mjs` — Task 2.
2. **Известные ограничения, которые нужно честно записать:** `*_FILE` Warehouse/Serving/Gateway ещё нет (C14, волна 1 их групп) — `env_file` для них остаётся; образы amd64 на arm64-разработчике запускаются через эмуляцию; частота ротации ключей сервисов и JWT вне объёма.
3. **Согласованность имён:** `images.lock.json`, `treeSha`, `.ready` v2, `prepare()` опции `rotateLeaf/rotateCA/reconfigure/rollback/now/backupReceipt`, команды `lock-images`, `completion-acceptance` — одинаково в задачах 3–9.
4. **Review Focus** отражён тестами: идемпотентность (Task 3 п.7), обрыв private-init (Task 4 п.5–6), фазы CA (Task 3 п.5), неверный ключ/повреждение/непустой проект (Task 8), lock (Task 5), уборка (Task 9), ключи в окружении (Task 6).
