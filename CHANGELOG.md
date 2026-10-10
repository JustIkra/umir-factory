# Изменения

## Не выпущено

- Added the draft gateway OpenAPI v1 contract with typed business operations, bounded decimal strings, Moscow timestamps and JSON examples. Documented the separate production resolve requirements.
- Проверки check-images и smoke перенесены на автономный Go CLI; заменённые Python-скрипты удалены.
- Добавлены реальный bridge и совместные бизнес-тесты двух Runtime, локальный runner и проверка фиксированных commit через временный workspace.
- Добавлены закреплённые gates для обоих вложенных Go-модулей и opt-in business job для реализованных выбранных SHA.
- Bridge явно переводит DishDetails, сохраняет EvaluatedAt/StateVersion и копирует вложенный Dish.

- Локальные сборки используют отдельные dev-теги и больше не перезаписывают SHA-теги GHCR.
- CI проверяет существование коммитов и успешные задания проверки и публикации сервиса для выбранного SHA.
- После pull проверяются OCI source/revision и наличие HEALTHCHECK во всех четырёх образах.
- Smoke использует опубликованные адреса и порты Compose; добавлены выбор env-файла и явные URL.
- README описывает наследуемые HEALTHCHECK и точные границы проверок образов.
- Production image SHA pins не изменялись; бизнес-HTTP, постоянные хранилища и пользовательские экраны остаются следующими этапами.

## [0.1.0] - 2026-10-10

- Добавлены сборка четырёх сервисов по SHA образов и локальная сборка.
- CI проверяет обе конфигурации Compose и формат SHA-тегов, скачивает закреплённые образы, запускает четыре контейнера через `up --wait` и проверяет frontend и оба маршрута gateway smoke-тестом.

