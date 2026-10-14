> Архивный план. Отменён пользователем 14.10.2026. Не возобновлять его незавершённые задачи. Единственный текущий план — [«Версия для защиты», редакция 2](current-plan.md). Текст ниже сохранён как история исходников, contracts и evidence.

# Backend: текущее состояние и оставшийся план

Обновлено: 14.10.2026, Europe/Moscow.

Auth façade завершён и ранее интегрирован в main. UA1 согласован пользователем и реализован в Auth, Warehouse и Gateway. Текущая интеграция переносит проверенные source-изменения UA1 и эту документацию в main. Полная runtime-квалификация UA1 и общая backend E2E приёмка ещё не завершены. Frontend и Figma ведёт другой разработчик.

## Текущий согласованный объём

- Auth хранит identity/session/invitation authority в JSON. Текущий формат Auth — v2 с проверяемым обновлением из v1.
- Warehouse и Serving используют изолированную процессную память. Restart может сбросить business state и незавершённые попытки. RuntimeReset/unknown не разрешают новый commandID вместо неопределённой команды.
- У одного User общая идентичность между организациями. Warehouse и Waiter могут совмещаться в одном ресторане; права объединяются. Admin управляет собственной организацией и имеет рабочие возможности.
- Warehouse включает подтверждение списания. Waiter один не имеет stock.read и writeoff.confirm.
- Удаление Organization/Restaurant закрывает связанный доступ; глобальный User и другие scopes сохраняются.
- Timezone принадлежит организации. Backend владеет expiry, FEFO и календарными расчётами. Новая timezone не переписывает записанные expiry-факты.
- Аналитикой владеет Warehouse; рекомендациями — существующий Serving.
- PostgreSQL, Redis, RabbitMQ, multi-instance и схема v4 остаются следующим отдельным этапом.

Исходный C2: [public API](api/backend-v2-public.json), [private API](api/backend-v2-private.json), [business core](business-core.md). Добавления UA1: [согласованный контракт](ui-api-amendment.md), [public overlay](api/ui-amendment-v1-public.json), [private overlay](api/ui-amendment-v1-private.json). [Composed public](api/ui-amendment-v1-composed-public.json) и [composed private](api/ui-amendment-v1-composed-private.json) генерируются из этих отдельных входов. C2 и его исторические результаты сохранены.

## Что выполнено

| Этап | Результат |
| --- | --- |
| Auth façade | Все 29 исходных Auth операций доступны через executable Gateway. Проверены реальные session/preauth/CSRF, Origin, cookies, command replay/recovery и role/scope отказы. |
| Runtime defects | Исправлены цикл registry/status в Gateway, блокировка Serving при discovery и обновление settings metadata без пересоздания runtime. Warehouse v2 направлен на private listener 8082. |
| UA1 партии | GET product batches возвращает положительные available/pending_writeoff партии, точные строки quantities, единицы и FEFO. Снимки неизменяемы; новая авторизация и settings/epoch проверки выполняются на каждой странице. |
| UA1 организация | Admin получает members с настоящими именами и nullable legacy email, а также invitations с безопасным наблюдением последнего link job. |
| UA1 письма | Create атомарно принимает один link job; отдельный resend-link сохраняет OTP resend. Trusted origin, исходный deadline, общий cooldown/issue budget, durable replay и bounded unknown retries реализованы. |
| Auth storage | Валидированный формат v1 обновляется в v2 с сохранением identities, ciphertext, deadlines и старых C2 результатов. Legacy create replay не добавляет письмо. |
| Gateway UA1 | Новые операции включены в mux/dispatch, строгие схемы и проверки вложенных scope/product/capture/resource identities. |
| Developer checks | Полные owner build/lint/architecture/race gates и три модуля Factory пройдены. Quality suites: Auth 37, Gateway 38, Warehouse 43. Независимое source review закрыто. |

Исходники до текущей main-интеграции: Auth cb66a48dfcd769257f02bf91a574e79fcf77373f, Gateway ab756389a6567a5463d8546c7df749fe1e104a22, Warehouse 2e5af267f2944c5559691a43ddaf8e2c8aaf950c. Gateway/Auth интеграционные merge-коммиты могут иметь другие SHA при том же source tree. Warehouse main также получает предшествующий backend checkpoint 9a87d84df63206c0f96fae4bbe956e6b85c59a98; его полный source gate проверяется в этой интеграции.

Ранее завершённые main-интеграции: [Auth #1](https://github.com/JustIkra/umir-auth/pull/1), [Serving #2](https://github.com/JustIkra/umir-serving/pull/2), [Gateway #21](https://github.com/JustIkra/umir-gateway/pull/21), [Factory #12](https://github.com/JustIkra/umir-factory/pull/12). Старые checkpoint SHA ниже остаются историей.

## Что подтверждено на живом стенде

Изолированный стенд использует Nginx, executable Gateway, настоящие Auth/Warehouse/Serving/Feedback и локальный SMTP с синтетическими адресами. Setup создаёт authority через Auth API; JSON authority, JWT и membership не подделываются. Исходный пользовательский стенд и SMTP сохранены.

- Исходный façade прогон: 118 HTTP requests и отдельная cooldown-фаза из 10 requests. Это backend HTTP coverage, а не browser приёмка.
- Начальный UA1 прогон: 59 HTTP requests, FEFO ties, positive/pending batches, expiry preparation, writeoff, strict queries, link create/replay/conflict/resend/cancel и фактическая SMTP доставка.
- Повтор UA1 проверил свежий вход после паузы, сохранённые команды и явный RefreshRequired для истёкшего cursor.
- Warehouse достиг 128 активных captures, вернул LimitExceeded без truncation и продолжил читать уже сохранённый capture.
- Реальный User прошёл email-bound OTP регистрацию. Объединённые Warehouse/Waiter роли дали stock read; изменение на Waiter-only дало 403. Employee получил 403 для Admin members/invitations.
- Contact snapshot проверен с привязкой к изменённому User: старая continuation сохранила имя/context, свежий список показал новое имя.
- Письмо приглашения отрендерено и визуально проверено на широком и мобильном экране. Это проверка письма, не frontend приложения.

Это отдельные частичные UA1 observations. Ни одна из [34 обязательных строк](api/ui-amendment-v1-acceptance.json) пока не отмечена полностью квалифицированной. Status в исходном row manifest отражает замороженную декларацию; частичный прогон не превращается в PASS всей строки. Исторические исходные all67/307 assertions не изменены; их [transition map](api/ui-amendment-v1-transition.json) хранится отдельно.

Живые наблюдения привязаны к фактически запущенным images: Auth b0373355877bd535d2f43e2a15018a7862f9474b, Gateway 07f5317fa6e39b11b0cee381efef301b61dea4c4, Warehouse 2e5af267f2944c5559691a43ddaf8e2c8aaf950c, Serving 1acd7637d7bb9e693ac931eabf68db42586d9810, Feedback 136a88a96a80eee3b5553cecf5b305bcd8d0e851. Более поздние test/doc/main коммиты не заменяют эти identities в старом runtime evidence.

## Оставшийся план по порядку

| Порядок | Работа | Критерий завершения |
| --- | --- | --- |
| 1 | Дополнить UA1 fixtures для link jobs | Отдельно помеченные typed discovery/receiver/fault controls знают invitation_link без выдуманного challengeID. Test-only serve-ui-ua1 уже задаёт trusted origin; исходные C2/RPC schemas и старые evidence сохраняются. |
| 2 | Полностью квалифицировать 34 UA1 строки | Реальный public Gateway/owners плюс source evidence, SMTP/storage failures и unknown, restart, retry identity, injected-time expiry, cancel/accept/delete races, cross-org/actor/session/cursor проверки, Auth capacity/row/byte bounds, settings/DST и epochs. Каждая строка получает полный результат с точными source/image/config identities. |
| 3 | Закрыть Auth/Feedback owner profile handoffs | Source/spec approvals сохранены. Нужны отдельные текущие profile approvals; Feedback — coherent redacted projection настоящего legacy archive и разрешённый write scope. Старый Auth fixture deadline дефект уже исправлен; полное profile approval этим не заменяется. |
| 4 | Квалифицировать provisioning/discovery и observation/fault/issuer механизмы | Реальная интеграция по ранее одобренным supplements; затем runner и shared F3 helpers. Спецификация сама по себе не runtime PASS. |
| 5 | Завершить исходный all67 corpus и F3/F4 | Сохранить 17 написанных case bodies, закончить остальные 50; проверить все 67 сценариев и 307 исходных assertions. Написанный case не равен принятому. Изменения наблюдений из UA1 называются явно через transition map. |
| 6 | Выполнить J1, реальные E1–E5 и независимый J2 | Проверки текущей сборки и coverage/identity отчёт; отдельные UA1 и исходные результаты. |
| 7 | Frontend integration и browser приёмка | Разработчик frontend подключает current APIs; реальные browser→Nginx→Gateway→owner сценарии, tenant switch, session expiry, SSE revoke/reconnect, unknown/replay/reset, timezone/DST, доступность и responsive states. |
| Следующая фаза | v4 | Отдельно определить storage/recovery/migration требования и согласовать PostgreSQL/RLS, Redis, RabbitMQ/outbox и multi-instance. Текущий JSON/in_memory этап этим не расширяется. |

При failed/unknown сохранять commandID, payload, scope и наблюдения. Не пропускать тест, не менять ожидаемое правило и не подменять auth/business state. Принятые исходные A1–A8, W1–W6 и profile/discovery/case-evidence specifications не перезапускаются ради нового статуса; проверяются новые source commits и оставшийся scope.

## Frontend и эксплуатация

Frontend использует /app/r/{restaurantID}/orders, /warehouse, /analytics, scoped API/live, отдельные service/restaurant epochs и отмену старых запросов/stream при смене scope. Quantities остаются точными строками или BigInt; отображение времени использует timezone организации. Service credentials остаются вне browser.

Members, invitations и batches уже реализованы в UA1. Построение страницы /invite/{invitationID} и подключение данных принадлежат frontend-разработчику. Открытие ссылки не предоставляет membership: нужны существующие email-bound OTP/proof и атомарная acceptance. [Issue #1](https://github.com/JustIkra/umir-frontend/issues/1), [design brief](frontend-design-brief.md) и [handoff](gateway-frontend-handoff.md) сохраняют владельца frontend/Figma; browser результаты пока не подтверждены этой стадией.

Настройка trusted origin, Auth v1→v2 upgrade и rollback guard описаны в README Auth. До upgrade остановить старый writer, защитить offline backup и сохранить encryption keys. Нельзя восстановить старый backup поверх подтверждённых команд/отзывов или запустить два authority writers.

Обновление main публикует исходники. Оно не выбирает production assembly, не создаёт SemVer release и не развёртывает сервисы. Selected pins, migration живой authority, release/tag и deployment остаются отдельными действиями.

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
