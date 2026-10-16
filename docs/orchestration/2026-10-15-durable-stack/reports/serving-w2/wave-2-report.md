# serving wave 2: частичный отчёт (остановлено по лимиту)

База 6c1d59f (fix/durable-w1-serving). Итог: см. git log ветки feat/durable-w2-serving, 3 коммита (e5ae1cb, fcf73d9, 2e16eca + коммит README).

## Сделано
- T1 основа: копия эталона postgres/rabbitmq @26166c8ba197, go.mod/go.sum, arch vendors, миграция 0001 с RLS, tests/itest, tests/pgtest, make integration.
- T2 порты и память: lease, RecordCheck, MarkUnresolved, лимит активных, domain/store.go, memory.Storage (PickDue, Sweep), tests/storecontract.
- T3 pgstore: Store и Backend (PickDue, Sweep), набор на memory и Postgres, RLS на реальной роли с пулом из одного соединения, планы без Seq Scan на 100k строк.
Проверки: make check зелёный, quality-test 37 (на T1), make integration зелёный.

## Не сделано (задачи плана 4-10)
lease в OrdersService вместо lockProposal, 503 и лимиты в Prepare/HTTP, RegistrationStore и Registry без сверки, generation=epoch и подкоманды, /readyz, проекция R5/R17 и потребитель umir.auth, FIN-04 (Interrupted, автомат, доводчик, order.resolve), горутина очистки, отказные тесты F1-F9, нагрузка R13, правка README. Причина: лимит. Ветка зелёная, но сервис пока работает по-старому.

## Ruling
- Store в internal/adapters/pgstore, а не в postgres: copy.sh --check не видит extra.
- Интеграционные тесты в tests/ (arch-lint).
- Права runtime-роли даёт задание ролей; в миграции GRANT нет.

## CR
Открыт прежний cr/01 (коды вне enum). Новых нет.

## Для волны 3
Auth: точечное чтение /registry/{id}, токен order.resolve, события umir.auth. Factory: роли с default privileges, SERVING_INSTANCE_ID.
