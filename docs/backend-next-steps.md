# Backend: актуальная задача после checkpoint

Обновлено: 14.10.2026, Europe/Moscow.

Статус: работа продолжается. Это задача backend и интеграционного стенда. Фронтенд и Figma ведёт другой разработчик. Документ не меняет frozen API, выбранные production SHA или правила выпуска.

## Согласованный объём

- Текущий этап: Auth JSON и изолированные in-memory runtimes Warehouse/Serving. PostgreSQL, RabbitMQ, Redis и схема v4 остаются следующим этапом.
- Общая User-идентичность сохраняется между организациями. Один User может иметь роли Warehouse и Waiter в одном ресторане; права объединяются. Admin управляет своей организацией и имеет рабочие возможности. Warehouse включает подтверждение списания; Waiter один такого права не имеет.
- Удаление организации/ресторана закрывает соответствующий scope и связанные доступы. Глобальный User и другие scopes сохраняются.
- Timezone задаётся организацией. Backend владеет expiry, FEFO и календарными расчётами. Смена timezone не переписывает записанные expiry-факты.
- Аналитикой владеет Warehouse, рекомендациями — существующий Serving. Новый сервис для этих функций не нужен.
- Business restart может сбросить состояние и незавершённые попытки. RuntimeReset и unknown не разрешают автоматически создавать новую команду.

Нормативные wire-схемы находятся в [public v2 API](api/backend-v2-public.json) и [private v2 API](api/backend-v2-private.json). Бизнес-правила находятся в [business-core](business-core.md). Дополнения ниже требуют отдельного согласования, если они меняют контракт.

## Исходные checkpoint-коммиты

Ветка шести репозиториев: `chore/backend-e2e-checkpoint-20261013`. Это незавершённый source checkpoint, а не release или выбранная production-сборка.

| Репозиторий | Checkpoint SHA |
| --- | --- |
| Auth | `d6ec45e8106dfc3bfa03a5eb1d0c1131721911d5` |
| Gateway | `f691c46510440c2fed5c5d125414ed064b52bcc9` |
| Warehouse | `9a87d84df63206c0f96fae4bbe956e6b85c59a98` |
| Serving | `39e2bcb4bf72c5094134cda45f16e4fb8d23ec8b` |
| Feedback | `136a88a96a80eee3b5553cecf5b305bcd8d0e851` |
| Factory | `66b827cf49fa70777dcba7b54fb7a3d05e64407c` |

## Выполнено 14.10.2026

Два блокера обнаружены на реальном локальном стенде и исправлены отдельно от исходных checkpoint-коммитов.

| Репозиторий | Исправление | Коммит и ветка |
| --- | --- | --- |
| Gateway | Убран цикл Auth registry → Gateway runtime status → Auth registry. Status читает проверенную registry-проекцию и локальные регистрации без обратного HTTP-вызова. Проекция устаревает через 5 секунд; failed read, несовместимое время и отсутствующие регистрации не дают readiness. Сохранены порядок ответов, копирование вложенных slices, signed registration и fresh business authorization. | `99e4bb413f96c9970ecda326235c2ae01274f087`, `fix/auth-facade-composition` |
| Serving | Регистрация освобождает state mutex перед внешним warehouse discovery. Отдельный cancellable gate сохраняет один factory call при одновременных одинаковых регистрациях. Перед публикацией runtime повторно проверяются cancellation, identity и tombstone. Late result не пересоздаёт закрытый scope. | `87ee6c98bab2f21e88d6abfc2ecb64cfdc14a9db`, `fix/registration-status-lock` |

Оба исправления закоммичены локально. Их ветки пока не опубликованы. Не заменяйте исходные checkpoint SHA этими коммитами без отдельного выбора сборки. В локальном стенде использованы оба исправления; это не доказательство готовности исходного checkpoint без них.

Проверки обоих пакетов: Go 1.26.9, полные `make check` с build/lint/architecture/race suites, `make quality-test`, `go mod verify`, проверка `go mod tidy` и staged diff. Quality suites: Gateway 38 сценариев, Serving 37. Независимое read-only code review не нашло actionable defects.

Первый вариант Gateway readiness был отклонён полным gate: постоянный `ready: true` нарушал проверку недоступного Auth. Этот вариант заменён bounded projection; ожидаемый результат существующего теста сохранён. Первый Serving gate выявил несовпадение GID в macOS test TMPDIR. Исправлены group/mode только принадлежащего запуску временного каталога; private-file guards и ожидаемые результаты не ослаблялись. Итоговый полный gate прошёл.

## Фактическое покрытие стенда

Стенд использует реальные Auth, Gateway, Warehouse, Serving и Feedback. Credentials случайные и приватные; provider-переменные не передаются дочерним процессам. SMTP принимает только синтетические адреса через локальный receiver. Проверка TLS сохраняется. Setup не редактирует `auth.json`, не создаёт JWT или membership вручную.

| Проверка | Результат и предел |
| --- | --- |
| Private Auth login и создание организации с первым рестораном | Выполнено через существующий HTTP API с настоящими preauth/session/CSRF и разрешённым caller credential. CommandID, payload и operationID сохраняются для повтора. Это setup, не browser-сценарий создания организации. |
| Lifecycle readiness | Auth вернул `ready`; Warehouse, Serving и Gateway подтвердили текущие epochs и ту же lifecycleVersion. Повтор подготовки сохранил исходные identities. |
| `GET /api/v2/me` через Nginx/Gateway | HTTP 200, ответ содержит `user` и `session`. |
| `GET /api/v2/r/{restaurantID}/serving/menu` через Nginx/Gateway | HTTP 200, scoped `context` и `result`, текущий runtimeEpoch Serving. |
| `GET /api/v2/access` через Nginx/Gateway | HTTP 404, text/plain. Auth façade composition остаётся дефектной. |
| Warehouse `products` и `inventory` GET без query | HTTP 400 `InvalidArgument` на стенде. Причина ещё не установлена; успешное чтение этих API не подтверждено. Не добавлять неописанные query-параметры, чтобы скрыть отказ. |
| Browser/UI, tenant switch, session expiry, scoped SSE revoke/reconnect | Не выполнено в этой стадии. Это отдельная приёмка с владельцем frontend. |
| Feedback intake и доставка уведомлений | Полный живой сценарий не подтверждён. Factory notification receiver требует operational correlation envelope; readiness receiver не означает успешную доставку обычного production Feedback. |

HTTP-пробы, private setup и service gates не заменяют общую J1/E/J2 приёмку. Наличие `ready` не доказывает корректность каждого business route.

## Следующая задача: Gateway Auth façade

Владелец: Gateway. Это исправление реализации существующего frozen API, без нового UI-контракта.

1. Сверить полный набор Auth façade операций с frozen public/private v2 схемами.
2. Исправить одновременно executable mux и RemoteAPI whitelist/dispatch. Сейчас выставлены только `csrf`, `session`, `me`, `me/display-name` и `me/password`; одного добавления префикса недостаточно.
3. Подключить `GET /api/v2/access`, организационные операции, settings, существующие membership/invitation mutations, logout-all, command recovery и email challenge/reset flow.
4. Сохранить session/preauth binding, точный Origin, CSRF, cookie policy, runtime JSON validation, caller identity и отсутствие внутренних tokens/credentials в browser responses.
5. Сохранить исходные commandID/payload/scope при unknown и recovery. Не создавать новый commandID для неопределённой попытки.
6. Проверить все операции через собранный executable Gateway и реальный Auth. Добавить отрицательные role/scope/Origin/CSRF случаи и строгие request/response проверки.
7. Обновить README/CHANGELOG и выполнить полные gates. Проверить состав и identities фактически запускаемых образов.

Критерий завершения: согласованная матрица операций проходит через публичный Gateway к настоящему Auth; отсутствующие routes не помечены PASS. Unit-test одного handler не заменяет executable composition.

Параллельная диагностическая задача Gateway/Warehouse: установить причину HTTP 400 на валидных scoped catalog/inventory GET и проверить результат через тот же Nginx/Gateway/owner путь.

## Отдельное дополнение к контракту для UI

Владелец дополнения: Factory с владельцами Auth/Warehouse. До изменения исходников согласовать и независимо проверить amendment. Старые C2/all67 evidence сохраняются; frozen документы не переписываются молча.

| Требование | Предлагаемый API или поведение | Владелец |
| --- | --- | --- |
| Партии выбранного продукта | `GET /api/v2/r/{restaurantID}/warehouse/products/{productID}/batches` | Warehouse |
| Участники с именем/email | `GET /api/v2/orgs/{orgID}/members` | Auth |
| Список приглашений | `GET /api/v2/orgs/{orgID}/invitations` | Auth |
| Приглашение по ссылке | Create/resend ставит durable idempotent mail job с URL доверенного frontend origin | Auth |

Для партий зафиксировать stock.read, pagination, runtime/state identity, остаток и единицу, receivedAt/expiresAt, available/pending_writeoff и серверный FEFO-порядок.

Для members/invitations зафиксировать admin-only scope собственной организации, безопасные поля, статусы/версии и pagination. Не придумывать email для старого User без адреса.

Для invitation mail зафиксировать enqueue, replay, expiry, cancel и unknown delivery. Открытие URL не выдаёт membership: остаются email-bound OTP и атомарное принятие существующим или новым User. Это изменение нынешней семантики invitation create без письма; оно требует amendment и своих acceptance rows.

## Оставшаяся исходная backend E2E работа

Не запускать повторно уже принятые исходные Auth A1–A8, Warehouse W1–W6 и одобренные profile/discovery/case-evidence specifications ради нового статуса. Новые source commits проверяются отдельно от исторических approvals.

1. Завершить отдельные Auth/Feedback profile handoffs. Не требовать будущий case recorder для одобрения законченного profile scope. Feedback требует coherent redacted projection настоящего legacy archive и разрешённого write scope.
2. Квалифицировать настоящую provisioning/discovery интеграцию по одобренному дополнению.
3. Реализовать назначенные owner observation/fault/issuer механизмы, затем runner и shared F3 helpers.
4. Сохранить 17 написанных case bodies, завершить остальные 50 и проверить все 67 сценариев и 307 исходных assertions. Написанный case не равен принятому case.
5. Завершить F3/F4, полные owner/module gates и текущие source/image/config identities.
6. Выполнить J1, реальные E1–E5 и независимые J2 coverage/identity проверки. UI amendment проверяется своими дополнительными rows.

Failed/unknown результаты сохраняются и возвращаются владельцу. Не скрывать их пропуском теста, fake auth/business state или изменением ожидаемого результата.

## Граница передачи frontend-разработчику

Frontend-разработчик ведёт реализацию и Figma по [handoff](gateway-frontend-handoff.md), [design brief](frontend-design-brief.md) и [frontend issue #1](https://github.com/JustIkra/umir-frontend/issues/1).

Для v2 сохраняются `/app/r/{restaurantID}/orders`, `/warehouse`, `/analytics`, scoped API/live, отдельные restaurant/service epochs и отмена старых запросов/stream при смене scope. Количества остаются точными строками или BigInt; время отображается в timezone организации. Backend владеет бизнес-решениями.

Frontend использует реальные session/CSRF/cookies. В browser не передаются service credentials. До публикации Gateway façade и одобренных read models discovery/switcher, org/settings/members/invitations/link и полный email-reset не считаются живым E2E PASS.

Commit документа или исходников не меняет selected assembly. Публикация backend-веток, merge, release/tag, выбор production SHA и deployment требуют соответствующей инструкции пользователя.
