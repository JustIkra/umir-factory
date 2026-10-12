# Realtime Live Updates (umir-frontend) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Дать будущим экранам `/app` каркас обновлений в реальном времени: порт `LiveFeed` и адаптер `EventSource('/api/live')` с runtime-декодированием, сценарий live-sync, модели чтения на `useSyncExternalStore`, заголовок `X-Umir-Activity: background` для фоновых чтений и `location = /api/live` в Nginx — без экранов.

**Architecture:** `application` владеет портами (`LiveFeed`, `Scheduler`, `PageVisibility`, `StaleTarget`) и сценариями `createLiveSync` и `createReadModel`; время, таймеры, видимость вкладки и сеть подставляются снаружи. `adapters` реализуют порты поверх `EventSource`, `setTimeout` и `document.visibilityState` и задают параметры фонового GET. `app/main.tsx` собирает незапущенный live-sync и передаёт его в `ServicesProvider`; presentation читает состояние через хуки на `useSyncExternalStore`.

**Tech Stack:** React 19.3, TypeScript 6.0 (strict, `exactOptionalPropertyTypes`, `noUncheckedIndexedAccess`), Vite 8.3, ESLint 10 с `typescript-eslint` strictTypeChecked, dependency-cruiser 18.5, Prettier 3.9, Bun 1.4.3 (`bun:test`, `.mjs`-тесты импортируют `.ts`), Nginx 1.30.5 (`nginxinc/nginx-unprivileged`), Docker Compose для `npm run test:nginx`.

**Spec:** docs/superpowers/specs/2026-10-12-realtime-sse-design.md (umir-factory)

**Base:** `JustIkra/umir-frontend`, ветка `feat/frontend-figma-api` после двух шагов, которые в план не входят: (1) незакоммиченный WIP рабочего дерева (публичная часть, `src/presentation/services.tsx`, порт и адаптер feedback, `tests/unit`) закоммичен поверх `962f7a9` (= `origin/feat/frontend-quality`); (2) в ветку влит `origin/main` `2339f1a` (PR #3 — прокси `/api` в Nginx и `tests/nginx`, PR #4 — документы о приёмке на реальных сервисах). От базы требуется: `npm run check` и `npm run test:nginx` проходят; `package.json` содержит `test:nginx`, CI выполняет оба; `nginx.conf` — версия main (серверные `set $gateway`, `proxy_set_header`, таймауты, map `$api_outcome`, именованные `@api_*`) плюс WIP-блок `location = /manifest.webmanifest`; `tests/nginx/*.mjs` отформатированы Prettier репозитория (иначе `format:check` не проходит). `location = /api/live` добавляется в этот объединённый `nginx.conf` сразу после блока `location = /api/feedback` (строки 45–51 объединённого файла). Открытых PR в `JustIkra/umir-frontend` нет (`gh pr list` пуст; #2–#4 слиты), поэтому пересечений с PR нет. С WIP пересекаются `README.md`, `CHANGELOG.md`, `src/app/main.tsx` и `src/presentation/services.tsx`: план правит их версии после коммита WIP. Номера строк `README.md`, `CHANGELOG.md` и `tests/nginx/*` после слияния заранее неизвестны, поэтому для них указаны якоря — заголовки и строки текста.

## Global Constraints

- Поток: `GET /api/live`, ответ `200` с `Content-Type: text/event-stream; charset=utf-8`; первый кадр `retry: 3000`; `: ping` каждые 15 с; при подключении по порядку `change`, `tick`, `session`, `upstream`.
- `change`: `{"epoch": string (1..64 chars, opaque), "seq": DecimalString}`; `seq` декодируется как NonNegativeInt64 фабрики (`0`…`9223372036854775807`, без знака и ведущих нулей) в `BigInt`, никогда в `Number`.
- `tick`: `{"serverTime": MoscowTimestamp}`; `MoscowTimestamp` — `^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.[0-9]{1,9})?\+03:00$` и существующая календарная дата.
- `session`: `{"idleExpiresAt": MoscowTimestamp, "absoluteExpiresAt": MoscowTimestamp}`.
- `session-ended`: `{"reason": "signed_out" | "revoked" | "expired"}`; затем поток закрывается без переподключения и открывается экран «сессия истекла».
- `upstream`: `{"available": boolean}`.
- Порт: `LiveFeed` с `subscribe(onEvent, onStatus) → unsubscribe`; события — объединение `change | tick | session | session-ended | upstream`; статус — `connecting | open | closed`.
- `EventSource('/api/live')` только в `src/adapters`; каждое `data` проверяется во время выполнения; некорректное событие отбрасывается и считается сбоем потока.
- Ошибки потока `401 Unauthenticated`, `403 InvalidOrigin` (ответ `CsrfRejected`), `405 MethodNotAllowed`, `429 TooManyStreams`, `503 AuthUnavailable`: браузер закрывает `EventSource` без переподключения. `EventSource` не сообщает статус, поэтому после `closed` live-sync спрашивает порт `SessionCheck`: фоновый `GET /api/operator/me` с `X-Umir-Activity: background` даёт `200` → `active`, `401` → `ended`, любой другой ответ или сетевая ошибка → `unknown`. `ended` останавливает поток навсегда, как `session-ended` с причиной `expired`. `active` и `unknown` дают новую подписку через 3 с (`reconnectDelayMs = 3_000`), а отсчёт 10 с не сбрасывается. На один обрыв идёт одна проверка; `stop()` отменяет ожидаемую проверку и переподключение.
- `change` с тем же `epoch` и большим `seq` → общие данные устарели; номер не больше известного → ничего; новый `epoch` → перечитываются все модели, сравнение начинается заново.
- `ConfirmationPending`: `resolve` вызывается при каждом новом `change` и после переподключения, по одному вызову за раз, с теми же `commandID` и `proposalID`.
- `tick` → время сервера, перечитывание окна сроков и рекомендаций, перерисовка относительных меток; решение «истекло» остаётся за сервером.
- Состояние «Устаревшие показанные данные»: поток не открыт дольше 10 с или `upstream: false`; показывается со временем последнего успешного чтения.
- Перечитываются только видимые экраны; скрытая вкладка откладывает запросы до `visibilitychange` и делает один запрос, если что-то изменилось; запрос во время идущего запроса → ровно один запрос после него.
- Экран применяет только ответ на свой последний запрос; порядок задаёт счётчик запросов клиента, а не `stateVersion`.
- Перечитывание по сигналу или такту отправляет `X-Umir-Activity: background` (только точное значение `background` имеет смысл); запросы, вызванные пользователем, — без заголовка.
- Presentation получает данные хуком на `useSyncExternalStore`, сценарии — из контекста сервисов; прямых `fetch` и `EventSource` в presentation нет.
- Nginx `location = /api/live`: `proxy_http_version 1.1`, `proxy_set_header Connection ""`, `proxy_buffering off`, `proxy_cache off`, `gzip off`, `proxy_read_timeout 60s`.
- Контракт: публичная OpenAPI `1.0.0-draft.4` → `1.1.0-draft.1`, операция `streamLiveUpdates`, схемы `LiveChangeEvent`, `LiveTickEvent`, `LiveSessionEvent`, `LiveSessionEndedEvent`, `LiveUpstreamEvent`, параметр `ActivityHeader`.
- Репозиторий: Bun 1.4.3 из `.bun-version` (preload проверяет версию), Node.js 24.21.0 в CI; ESLint запрещает в `domain`/`application` `Date`, `setTimeout`, `document`, `window`, `EventSource`, `fetch`, `console`, `Math.random` и др.; inline-подавления запрещены (`noInlineConfig`); Europe/Moscow; Conventional Commits без `Co-Authored-By` и generated-by; README и CHANGELOG «Не выпущено» — в той же задаче, что и поведение.

## Review Focus

1. **Nginx: собственный `proxy_set_header` в `location = /api/live` отменяет наследование серверных `Host $http_host`, `X-Forwarded-For`, `X-Forwarded-Proto`.** Без повторения gateway получает `Host: gateway:8080` вместо адреса браузера. Ожидание: поток доходит до gateway с теми же заголовками браузера, что и любой `/api/`. Тест: Task 7, `'passes /api/live over HTTP/1.1 with the browser headers'` (проверено: без повторения падает с `'gateway:8080'`).
2. **`EventSource` переходит в `CLOSED` после любого ответа, отличного от `200 text/event-stream`, и не сообщает статус: `401` неотличим от `502`.** Так бывает, если сессия истекла, пока gateway перезапускался, если сессии очищены через CLI или если cookie удалена. Ожидание: при `401` от `GET /api/operator/me` поток останавливается навсегда и открывается «сессия истекла» (`sessionEnded: 'expired'`), а не «устаревшие данные» с запросом каждые 3 с. При `200`, `5xx` или сетевой ошибке — новая подписка через 3 с и «устаревшие данные» через 10 с от первого обрыва без сброса отсчёта. `stop()` во время проверки не переподключает. Тесты: Task 1 — `'a source the browser closed reports closed'`. Task 2 — `'a closed feed of an ended session stops for good'`, `'an unknown or failed session check subscribes again'`, `'a closed feed of an active session is subscribed again after the retry delay'`, `'the 10 s count runs while the session is checked'`, `'stop during a session check does not subscribe again'`, `'a restart ignores the answer of the check it replaced'`, `'reconnect attempts do not restart the 10 s count'`. Task 5 — `'the profile status tells the session state'`, `'a network error leaves the session state unknown'`, `'the check is a background read of the operator profile'`.
3. **`seq` больше 2^53** (`9007199254740993` после `9007199254740992`): сравнение через `Number` считает их равными и теряет сигнал. Ожидание: точное сравнение `BigInt`, граница `9223372036854775807` принимается, `9223372036854775808` отклоняется. Тесты: Task 1, `'every contract event is decoded'` и случай `seq "9223372036854775808"`; Task 2, `'the first change and every larger seq mark all models stale'`.
4. **React 19 `StrictMode` при монтировании вызывает subscribe → unsubscribe → subscribe.** Ожидание: один запрос, а не два. Тест: Task 3, `'a remount during the first read does not read twice'`.
5. **Переподключение без новых изменений и отклонённый `resolvePending`.** Ожидание: после переподключения с тем же `epoch` и `seq` попытки проверяются один раз, а данные не перечитываются; отказ `resolvePending` не блокирует очередь «по одному вызову». Тесты: Task 2, `'a reconnect without new changes resolves attempts but re-reads nothing'` и `'a failed resolve call does not block later signals'`.

---

## Подготовка окружения: Bun 1.4.3

Тесты требуют Bun 1.4.3: `tests/quality/setup.mjs` сравнивает `bun.version` с `.bun-version`. В локальном `PATH` сейчас Bun 1.3.6 (`~/.bun/bin/bun`). Не переустанавливайте его и не запускайте установщик `bun.com/install`: он правит `~/.zshrc`. Закреплённый бинарь кладётся в отдельный каталог, и каждая команда с Bun добавляет его в начало `PATH`. `npm test` и `npm run check` вызывают `bun` из `PATH`, поэтому префикс работает и для них.

```sh
UMIR_BUN_DIR="$HOME/.cache/umir/bun-1.4.3"
if [ "$("$UMIR_BUN_DIR/bun" --version 2>/dev/null)" != "1.4.3" ]; then
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64) asset=bun-darwin-aarch64 ;;
    Darwin-x86_64) asset=bun-darwin-x64 ;;
    Linux-aarch64) asset=bun-linux-aarch64 ;;
    Linux-x86_64) asset=bun-linux-x64 ;;
    *) echo "unsupported platform" >&2; exit 1 ;;
  esac
  mkdir -p "$UMIR_BUN_DIR"
  curl -fsSL -o "$UMIR_BUN_DIR/bun.zip" "https://github.com/oven-sh/bun/releases/download/bun-v1.4.3/$asset.zip"
  unzip -q -j -o "$UMIR_BUN_DIR/bun.zip" "$asset/bun" -d "$UMIR_BUN_DIR"
  rm "$UMIR_BUN_DIR/bun.zip"
fi
PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun --version
```

Expected: `1.4.3`. Все команды ниже выполняются из корня `umir-frontend`; команды с Bun начинаются с `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH"`. `npm run test:nginx` требует Docker с Compose и Node.js на хосте.

## File Structure

**Create:**
- `src/application/live-feed.ts` — порт `LiveFeed`, объединение `LiveEvent`, `LiveStatus`, `SessionEndReason`.
- `src/application/live-sync.ts` — сценарий `createLiveSync`: правила `epoch`/`seq`, `resolvePending`, такты, сроки сессии, `session-ended`, устаревшие данные, проверка сессии и переподключение после `closed`, отложенное перечитывание скрытой вкладки; порты `Scheduler`, `PageVisibility`, `SessionCheck`, `StaleTarget`.
- `src/application/read-model.ts` — модель чтения одного экрана: счётчик запросов, склейка, `LoadOptions { background }`, снимок для `useSyncExternalStore`.
- `src/adapters/live/decode.ts` — runtime-декодирование `data` пяти событий по контракту.
- `src/adapters/live/event-source-feed.ts` — `LiveFeed` поверх `EventSource('/api/live')`.
- `src/adapters/browser-page/visibility.ts` — `PageVisibility` через `visibilityState` и `visibilitychange`.
- `src/adapters/browser-page/scheduler.ts` — `Scheduler` через `setTimeout`/`clearTimeout`.
- `src/adapters/http/read-request.ts` — `RequestInit` для GET через gateway с `X-Umir-Activity: background` у фоновых чтений.
- `src/adapters/http/session-check.ts` — `SessionCheck` через фоновый `GET /api/operator/me`.
- `src/presentation/live/use-live.ts` — хуки `useLiveState` и `useReadModel` на `useSyncExternalStore`.
- `tests/unit/live-feed.test.mjs`, `tests/unit/live-sync.test.mjs`, `tests/unit/read-model.test.mjs`, `tests/unit/browser-page.test.mjs`, `tests/unit/read-request.test.mjs`, `tests/unit/session-check.test.mjs` — Bun-тесты этих модулей.

**Modify:**
- `src/presentation/services.tsx:1-9` — поле `live: LiveSync` в `PresentationServices`.
- `src/app/main.tsx:10-13,32-37` — сборка незапущенного live-sync из адаптеров, включая `SessionCheck` на `fetch`.
- `nginx.conf` (объединённый, после строки 51) — `location = /api/live`.
- `tests/nginx/gateway.mjs` — маршруты заглушки `/api/live` и `/api/live?drop`, поля `httpVersion` и `connection` в записи запроса.
- `tests/nginx/check.mjs` — проверки потока.
- `README.md` — слои, подраздел «Обновления в реальном времени», таблица команд, «Прокси API».
- `CHANGELOG.md` — пункты в «Не выпущено».

---

### Task 1: Порт `LiveFeed` и адаптер `EventSource` с runtime-декодированием

**Files:**
- Create: `src/application/live-feed.ts`
- Create: `src/adapters/live/decode.ts`
- Create: `src/adapters/live/event-source-feed.ts`
- Modify: `README.md` (раздел «Архитектура», список под «Сейчас слои заполнены так:»; таблица команд, строка `npm run test`; новый подраздел в конце «Текущее состояние и следующий этап»)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `tests/unit/live-feed.test.mjs`

**Interfaces:**
- Consumes: контракт `GET /api/live` из плана gateway (имена событий и поля `data`).
- Produces:
  - `type SessionEndReason = 'signed_out' | 'revoked' | 'expired'`
  - `type LiveEvent = { type: 'change'; epoch: string; seq: bigint } | { type: 'tick'; serverTime: string } | { type: 'session'; idleExpiresAt: string; absoluteExpiresAt: string } | { type: 'session-ended'; reason: SessionEndReason } | { type: 'upstream'; available: boolean }` (все поля `readonly`)
  - `type LiveStatus = 'connecting' | 'open' | 'closed'`
  - `interface LiveFeed { subscribe(onEvent: (event: LiveEvent) => void, onStatus: (status: LiveStatus) => void): () => void }`
  - `const liveEventNames: readonly ['change', 'tick', 'session', 'session-ended', 'upstream']`, `type LiveEventName`
  - `decodeLiveEvent(name: LiveEventName, data: unknown): LiveEvent | undefined`
  - `const liveStreamPath = '/api/live'`; `interface LiveEventSource { readonly readyState: number; addEventListener(type: string, listener: (event: { readonly data?: unknown }) => void): void; close(): void }`
  - `createEventSourceFeed(open: (url: string) => LiveEventSource): LiveFeed`

Контракт адаптера: подписка начинается в состоянии `connecting` и сообщает только переходы; `error` при `readyState` `CONNECTING` — браузер переподключается сам (`connecting`); `error` при `CLOSED` (`2`) — браузер сдался (`closed`); некорректное событие закрывает источник и сообщает `closed`; после `unsubscribe` колбэки не вызываются. Лишние ключи в `data` отклоняются, как при `additionalProperties: false`, принятом в OpenAPI фабрики.

- [ ] **Step 1: Write the failing test**

Создайте `tests/unit/live-feed.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import { decodeLiveEvent } from '../../src/adapters/live/decode.ts';
import {
  createEventSourceFeed,
  liveStreamPath,
} from '../../src/adapters/live/event-source-feed.ts';

test('every contract event is decoded', () => {
  assert.deepEqual(
    decodeLiveEvent('change', '{"epoch":"01JQ","seq":"9223372036854775807"}'),
    { type: 'change', epoch: '01JQ', seq: 9223372036854775807n },
  );
  assert.deepEqual(decodeLiveEvent('change', '{"epoch":"e","seq":"0"}'), {
    type: 'change',
    epoch: 'e',
    seq: 0n,
  });
  const longEpoch = 'э'.repeat(64);
  assert.equal(
    decodeLiveEvent('change', JSON.stringify({ epoch: longEpoch, seq: '1' }))
      ?.epoch,
    longEpoch,
  );
  assert.deepEqual(
    decodeLiveEvent('tick', '{"serverTime":"2026-10-12T12:01:00.5+03:00"}'),
    { type: 'tick', serverTime: '2026-10-12T12:01:00.5+03:00' },
  );
  assert.deepEqual(
    decodeLiveEvent(
      'session',
      '{"idleExpiresAt":"2026-10-12T12:30:00+03:00","absoluteExpiresAt":"2026-10-12T20:00:00+03:00"}',
    ),
    {
      type: 'session',
      idleExpiresAt: '2026-10-12T12:30:00+03:00',
      absoluteExpiresAt: '2026-10-12T20:00:00+03:00',
    },
  );
  for (const reason of ['signed_out', 'revoked', 'expired']) {
    assert.deepEqual(
      decodeLiveEvent('session-ended', JSON.stringify({ reason })),
      { type: 'session-ended', reason },
    );
  }
  assert.deepEqual(decodeLiveEvent('upstream', '{"available":false}'), {
    type: 'upstream',
    available: false,
  });
});

const malformed = [
  ['change', 'not json'],
  ['change', '[]'],
  ['change', 'null'],
  ['change', '{"epoch":"e","seq":1}'],
  ['change', '{"epoch":"e","seq":"01"}'],
  ['change', '{"epoch":"e","seq":"-1"}'],
  ['change', '{"epoch":"e","seq":"9223372036854775808"}'],
  ['change', '{"epoch":"","seq":"1"}'],
  ['change', JSON.stringify({ epoch: 'e'.repeat(65), seq: '1' })],
  ['change', '{"epoch":"e","seq":"1","extra":true}'],
  ['change', '{"epoch":"e"}'],
  ['tick', '{"serverTime":"2026-10-12T09:01:00Z"}'],
  ['tick', '{"serverTime":"2026-02-30T12:00:00+03:00"}'],
  ['tick', '{"serverTime":"2026-10-12"}'],
  ['session', '{"idleExpiresAt":"2026-10-12T12:30:00+03:00"}'],
  ['session-ended', '{"reason":"timeout"}'],
  ['upstream', '{"available":"false"}'],
];

for (const [name, data] of malformed) {
  test(`rejects ${name} ${data.slice(0, 40)}`, () => {
    assert.equal(decodeLiveEvent(name, data), undefined);
  });
}

test('the leap day exists only in leap years', () => {
  const tick = (date) =>
    decodeLiveEvent(
      'tick',
      JSON.stringify({ serverTime: `${date}T12:00:00+03:00` }),
    );
  assert.notEqual(tick('2028-02-29'), undefined);
  assert.equal(tick('2026-02-29'), undefined);
  assert.equal(tick('2100-02-29'), undefined);
  assert.notEqual(tick('2000-02-29'), undefined);
});

// EventSource stand-in: emit() dispatches like the browser.
class FakeEventSource {
  static last;
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.closed = false;
    this.listeners = new Map();
    FakeEventSource.last = this;
  }
  addEventListener(type, listener) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  close() {
    this.readyState = 2;
    this.closed = true;
  }
  emit(type, data) {
    for (const listener of this.listeners.get(type) ?? []) listener({ data });
  }
}

function subscribe() {
  const events = [];
  const statuses = [];
  const feed = createEventSourceFeed((url) => new FakeEventSource(url));
  const unsubscribe = feed.subscribe(
    (event) => events.push(event),
    (status) => statuses.push(status),
  );
  return { source: FakeEventSource.last, events, statuses, unsubscribe };
}

test('the feed opens the live stream and reports decoded events', () => {
  const { source, events, statuses } = subscribe();
  assert.equal(source.url, liveStreamPath);
  assert.equal(liveStreamPath, '/api/live');
  assert.deepEqual(statuses, []);
  source.readyState = 1;
  source.emit('open');
  source.emit('upstream', '{"available":true}');
  assert.deepEqual(statuses, ['open']);
  assert.deepEqual(events, [{ type: 'upstream', available: true }]);
});

test('a browser retry reports connecting and keeps the source', () => {
  const { source, statuses } = subscribe();
  source.readyState = 0;
  source.emit('error');
  assert.deepEqual(statuses, ['connecting']);
  assert.equal(source.closed, false);
});

test('a source the browser closed reports closed', () => {
  const { source, statuses } = subscribe();
  source.readyState = 2;
  source.emit('error');
  assert.deepEqual(statuses, ['closed']);
});

test('a malformed event closes the source and reports a failure', () => {
  const { source, events, statuses } = subscribe();
  source.emit('open');
  source.emit('change', '{"epoch":"e","seq":1}');
  source.emit('change', '{"epoch":"e","seq":"1"}');
  assert.deepEqual(events, []);
  assert.deepEqual(statuses, ['open', 'closed']);
  assert.equal(source.closed, true);
});

test('nothing is reported after unsubscribe', () => {
  const { source, events, statuses, unsubscribe } = subscribe();
  unsubscribe();
  assert.equal(source.closed, true);
  source.emit('open');
  source.emit('error');
  source.emit('upstream', '{"available":true}');
  assert.deepEqual(events, []);
  assert.deepEqual(statuses, []);
});
```

- [ ] **Step 2: Run it and see it fail**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/live-feed.test.mjs`
Expected: FAIL — `error: Cannot find module '../../src/adapters/live/decode.ts' from '…/tests/unit/live-feed.test.mjs'`.

- [ ] **Step 3: Minimal implementation**

Создайте `src/application/live-feed.ts`:

```ts
// Signals of gateway's GET /api/live stream. The stream carries no business
// data: screens re-read their models with ordinary GET requests. Timestamps
// stay MoscowTimestamp strings; seq is the exact journal number.

export type SessionEndReason = 'signed_out' | 'revoked' | 'expired';

export type LiveEvent =
  | { readonly type: 'change'; readonly epoch: string; readonly seq: bigint }
  | { readonly type: 'tick'; readonly serverTime: string }
  | {
      readonly type: 'session';
      readonly idleExpiresAt: string;
      readonly absoluteExpiresAt: string;
    }
  | { readonly type: 'session-ended'; readonly reason: SessionEndReason }
  | { readonly type: 'upstream'; readonly available: boolean };

export type LiveStatus = 'connecting' | 'open' | 'closed';

// A subscription starts as connecting and reports later transitions only.
// 'closed' means the feed gave up and will not reconnect by itself. No
// callback runs after the returned unsubscribe.
export interface LiveFeed {
  subscribe(
    onEvent: (event: LiveEvent) => void,
    onStatus: (status: LiveStatus) => void,
  ): () => void;
}
```

Создайте `src/adapters/live/decode.ts`:

```ts
import type { LiveEvent, SessionEndReason } from '@/application/live-feed';

export const liveEventNames = [
  'change',
  'tick',
  'session',
  'session-ended',
  'upstream',
] as const;

export type LiveEventName = (typeof liveEventNames)[number];

type Fields = Record<string, unknown>;

const maxInt64 = 9223372036854775807n;
const nonNegativeInt64 = /^(?:0|[1-9][0-9]{0,18})$/;
const moscowTimestamp =
  /^(\d{4})-(\d{2})-(\d{2})T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.[0-9]{1,9})?\+03:00$/;
const endReasons: readonly SessionEndReason[] = [
  'signed_out',
  'revoked',
  'expired',
];

// Accepts an object with exactly the given keys, like the contract's
// additionalProperties: false.
function fields(value: unknown, keys: readonly string[]): Fields | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined;
  }
  const actual = Object.keys(value);
  return actual.length === keys.length &&
    keys.every((key) => actual.includes(key))
    ? (value as Fields)
    : undefined;
}

function decodeSeq(value: unknown): bigint | undefined {
  if (typeof value !== 'string' || !nonNegativeInt64.test(value)) {
    return undefined;
  }
  const seq = BigInt(value);
  return seq <= maxInt64 ? seq : undefined;
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) {
    const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leap ? 29 : 28;
  }
  return [4, 6, 9, 11].includes(month) ? 30 : 31;
}

// MoscowTimestamp: full RFC 3339 time with the +03:00 offset and a real date.
function isMoscowTimestamp(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const match = moscowTimestamp.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  return (
    month >= 1 && month <= 12 && day >= 1 && day <= daysInMonth(year, month)
  );
}

function isEpoch(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const length = Array.from(value).length;
  return length >= 1 && length <= 64;
}

function isEndReason(value: unknown): value is SessionEndReason {
  return endReasons.some((reason) => reason === value);
}

// Returns undefined for anything the contract does not allow: invalid JSON,
// missing, extra or mistyped fields.
export function decodeLiveEvent(
  name: LiveEventName,
  data: unknown,
): LiveEvent | undefined {
  if (typeof data !== 'string') return undefined;
  let value: unknown;
  try {
    value = JSON.parse(data);
  } catch {
    return undefined;
  }
  switch (name) {
    case 'change': {
      const event = fields(value, ['epoch', 'seq']);
      const seq = decodeSeq(event?.seq);
      return event && isEpoch(event.epoch) && seq !== undefined
        ? { type: 'change', epoch: event.epoch, seq }
        : undefined;
    }
    case 'tick': {
      const event = fields(value, ['serverTime']);
      return event && isMoscowTimestamp(event.serverTime)
        ? { type: 'tick', serverTime: event.serverTime }
        : undefined;
    }
    case 'session': {
      const event = fields(value, ['idleExpiresAt', 'absoluteExpiresAt']);
      return event &&
        isMoscowTimestamp(event.idleExpiresAt) &&
        isMoscowTimestamp(event.absoluteExpiresAt)
        ? {
            type: 'session',
            idleExpiresAt: event.idleExpiresAt,
            absoluteExpiresAt: event.absoluteExpiresAt,
          }
        : undefined;
    }
    case 'session-ended': {
      const event = fields(value, ['reason']);
      return event && isEndReason(event.reason)
        ? { type: 'session-ended', reason: event.reason }
        : undefined;
    }
    case 'upstream': {
      const event = fields(value, ['available']);
      return event && typeof event.available === 'boolean'
        ? { type: 'upstream', available: event.available }
        : undefined;
    }
  }
}
```

Создайте `src/adapters/live/event-source-feed.ts`:

```ts
import type { LiveFeed } from '@/application/live-feed';
import { decodeLiveEvent, liveEventNames } from '@/adapters/live/decode';

export const liveStreamPath = '/api/live';

// The part of the browser EventSource the feed uses.
export interface LiveEventSource {
  readonly readyState: number;
  addEventListener(
    type: string,
    listener: (event: { readonly data?: unknown }) => void,
  ): void;
  close(): void;
}

// EventSource.CLOSED: the browser gave up, for example after a response that
// is not 200 text/event-stream (401, 403, 405, 429, a 502 from Nginx).
const closedState = 2;

export function createEventSourceFeed(
  open: (url: string) => LiveEventSource,
): LiveFeed {
  return {
    subscribe(onEvent, onStatus) {
      const source = open(liveStreamPath);
      let active = true;
      const close = () => {
        active = false;
        source.close();
      };
      source.addEventListener('open', () => {
        if (active) onStatus('open');
      });
      source.addEventListener('error', () => {
        if (!active) return;
        if (source.readyState !== closedState) {
          onStatus('connecting');
          return;
        }
        close();
        onStatus('closed');
      });
      for (const name of liveEventNames) {
        source.addEventListener(name, (message) => {
          if (!active) return;
          const event = decodeLiveEvent(name, message.data);
          if (event) {
            onEvent(event);
            return;
          }
          // A malformed event is a stream failure; the owner reconnects.
          close();
          onStatus('closed');
        });
      }
      return close;
    },
  };
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/live-feed.test.mjs && npm run typecheck && npm run lint && npm run arch` Expected: PASS — `24 pass`, `0 fail`; tsc и ESLint без вывода ошибок; `no dependency violations found`.

- [ ] **Step 5: Document the behaviour**

В `README.md`, раздел «Архитектура», список под «Сейчас слои заполнены так:» — допишите в конец пункта `` - `application` — … ``:

```markdown
 Порт `LiveFeed` описывает сигналы потока `/api/live`: события `change`, `tick`, `session`, `session-ended`, `upstream` и состояние соединения `connecting`, `open`, `closed`.
```

и в конец пункта `` - `adapters` — … ``:

```markdown
 `live` открывает `EventSource('/api/live')` и проверяет `data` каждого события во время выполнения.
```

В таблице команд замените описание `npm run test` на:

```markdown
Полный набор тестов: `bun test ./tests`, включая `tests/unit` для формы обратной связи, cookie-уведомления, оглавления документов и обновлений в реальном времени.
```

В конец раздела `## Текущее состояние и следующий этап` (перед `## Разработка`) добавьте:

```markdown
### Обновления в реальном времени

Gateway сообщает об изменениях потоком SSE `GET /api/live` (операция `streamLiveUpdates` в [OpenAPI фабрики](https://github.com/JustIkra/umir-factory/blob/main/docs/api/openapi.yml), версия `1.1.0-draft.1`). Поток передаёт только сигналы, а данные экраны перечитывают обычными GET.

Адаптер разбирает `data` каждого события по контракту. `seq` сохраняется как `BigInt`, время должно быть полным `MoscowTimestamp` со смещением `+03:00` и существующей датой, лишние и пропущенные поля не допускаются. Некорректное событие закрывает `EventSource`, и адаптер сообщает `closed`. После разрыва соединения браузер переподключается сам. После ответа, отличного от `200 text/event-stream` (`401`, `403`, `405`, `429` или `502` от Nginx), браузер закрывает поток, и адаптер тоже сообщает `closed`.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Добавлены порт `LiveFeed` и адаптер потока `GET /api/live` на `EventSource`. Данные каждого события проверяются во время выполнения, `seq` хранится как `BigInt`, а некорректное событие закрывает поток.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/application/live-feed.ts src/adapters/live/decode.ts src/adapters/live/event-source-feed.ts tests/unit/live-feed.test.mjs README.md CHANGELOG.md
git commit -m "feat(live): decode the /api/live event stream"
```

---

### Task 2: Сценарий live-sync

**Files:**
- Create: `src/application/live-sync.ts`
- Modify: `README.md` (пункт `application` в «Сейчас слои заполнены так:»; подраздел «Обновления в реальном времени»)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `tests/unit/live-sync.test.mjs`

**Interfaces:**
- Consumes: `LiveEvent`, `LiveFeed`, `LiveStatus`, `SessionEndReason` из Task 1.
- Produces:
  - `interface Scheduler { schedule(delayMs: number, callback: () => void): () => void }`
  - `interface PageVisibility { isVisible(): boolean; subscribe(onChange: () => void): () => void }`
  - `type SessionStatus = 'active' | 'ended' | 'unknown'`; `interface SessionCheck { check(): Promise<SessionStatus> }`
  - `type StaleScope = 'all' | 'time'`; `interface StaleTarget { markStale(scope: StaleScope): void }`
  - `interface LiveSession { readonly idleExpiresAt: string; readonly absoluteExpiresAt: string }`
  - `interface LiveState { readonly connection: LiveStatus; readonly stale: boolean; readonly serverTime: string | undefined; readonly session: LiveSession | undefined; readonly sessionEnded: SessionEndReason | undefined }`
  - `interface LiveSync { start(): void; stop(): void; track(target: StaleTarget): () => void; readonly subscribe: (listener: () => void) => () => void; readonly getSnapshot: () => LiveState }`
  - `interface LiveSyncOptions { feed: LiveFeed; scheduler: Scheduler; visibility: PageVisibility; sessionCheck: SessionCheck; resolvePending: () => Promise<void> }`
  - `createLiveSync(options: LiveSyncOptions): LiveSync`; `const staleAfterMs = 10_000`; `const reconnectDelayMs = 3_000`

`subscribe` и `getSnapshot` объявлены как `readonly`-свойства-функции, а не методы: их передают в `useSyncExternalStore` без вызова, а правило `@typescript-eslint/unbound-method` из strictTypeChecked отклоняет такую передачу методов. Время сервера и сроки сессии остаются строками `MoscowTimestamp`: ESLint запрещает `Date` в `application`.

Решения, которых нет в спецификации явно:
- Первое `change` после `start()` считается новым `epoch` и перечитывает всё: ответ, прочитанный до открытия потока, мог устареть, а спецификация допускает лишнее перечитывание.
- После `closed` браузер сам не переподключается и не сообщает статус ответа, поэтому `401` неотличим от `502` при перезапуске gateway. Сценарий спрашивает порт `SessionCheck`. `ended` завершает работу так же, как `session-ended` с причиной `expired`: поток не переподключается, а `sessionEnded` открывает экран «сессия истекла». `active` и `unknown` (а также отклонённый промис) дают новую подписку через `reconnectDelayMs`. Отсчёт 10 с во время проверки продолжается. На один обрыв идёт одна проверка. `stop()` отбрасывает её ответ и отменяет переподключение, поэтому после `stop()` и нового `start()` ответ старой проверки не применяется.
- `resolvePending` не откладывается в скрытой вкладке: откладываются только перечитывания экранов.

- [ ] **Step 1: Write the failing test**

Создайте `tests/unit/live-sync.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import {
  createLiveSync,
  reconnectDelayMs,
  staleAfterMs,
} from '../../src/application/live-sync.ts';

// Virtual time: advance() runs due callbacks in order.
function manualScheduler() {
  let now = 0;
  const tasks = [];
  return {
    schedule(delayMs, callback) {
      const task = { at: now + delayMs, callback, done: false };
      tasks.push(task);
      return () => {
        task.done = true;
      };
    },
    advance(ms) {
      const until = now + ms;
      for (;;) {
        const due = tasks
          .filter((task) => !task.done && task.at <= until)
          .sort((left, right) => left.at - right.at)[0];
        if (!due) break;
        now = due.at;
        due.done = true;
        due.callback();
      }
      now = until;
    },
    pending: () => tasks.filter((task) => !task.done).length,
  };
}

function manualFeed() {
  const feed = {
    subscriptions: 0,
    active: undefined,
    subscribe(onEvent, onStatus) {
      feed.subscriptions += 1;
      const subscription = { onEvent, onStatus };
      feed.active = subscription;
      return () => {
        if (feed.active === subscription) feed.active = undefined;
      };
    },
    status: (status) => feed.active.onStatus(status),
    emit: (event) => feed.active.onEvent(event),
  };
  return feed;
}

function manualVisibility() {
  const listeners = new Set();
  const visibility = {
    visible: true,
    isVisible: () => visibility.visible,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    set(visible) {
      visibility.visible = visible;
      for (const listener of listeners) listener();
    },
  };
  return visibility;
}

function recordingTarget() {
  const marks = [];
  return { marks, markStale: (scope) => marks.push(scope) };
}

// Answers every check with the given state.
const sessionIs = (status) => ({ check: () => Promise.resolve(status) });

// Holds each check until the test answers it.
function heldSessionCheck() {
  const answers = [];
  return {
    answers,
    check: () =>
      new Promise((resolve) => {
        answers.push(resolve);
      }),
  };
}

const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

function setup({
  resolvePending = () => Promise.resolve(),
  session = sessionIs('active'),
} = {}) {
  const feed = manualFeed();
  const scheduler = manualScheduler();
  const visibility = manualVisibility();
  const sync = createLiveSync({
    feed,
    scheduler,
    visibility,
    sessionCheck: session,
    resolvePending,
  });
  const target = recordingTarget();
  sync.track(target);
  sync.start();
  feed.status('open');
  return { feed, scheduler, visibility, sync, target };
}

const change = (seq, epoch = 'e1') => ({
  type: 'change',
  epoch,
  seq: BigInt(seq),
});

test('the first change and every larger seq mark all models stale', () => {
  const { feed, target } = setup();
  feed.emit(change('9007199254740992'));
  feed.emit(change('9007199254740993'));
  feed.emit(change('9007199254740993'));
  feed.emit(change('9007199254740991'));
  assert.deepEqual(target.marks, ['all', 'all']);
});

test('a new epoch re-reads everything and restarts the comparison', () => {
  const { feed, target } = setup();
  feed.emit(change('40'));
  feed.emit(change('1', 'e2'));
  feed.emit(change('1', 'e2'));
  feed.emit(change('2', 'e2'));
  assert.deepEqual(target.marks, ['all', 'all', 'all']);
});

test('a tick updates the server time and marks time models stale', () => {
  const { feed, sync, target } = setup();
  feed.emit({ type: 'tick', serverTime: '2026-10-12T12:01:00+03:00' });
  assert.equal(sync.getSnapshot().serverTime, '2026-10-12T12:01:00+03:00');
  assert.deepEqual(target.marks, ['time']);
});

test('session deadlines are stored', () => {
  const { feed, sync } = setup();
  feed.emit({
    type: 'session',
    idleExpiresAt: '2026-10-12T12:30:00+03:00',
    absoluteExpiresAt: '2026-10-12T20:00:00+03:00',
  });
  assert.deepEqual(sync.getSnapshot().session, {
    idleExpiresAt: '2026-10-12T12:30:00+03:00',
    absoluteExpiresAt: '2026-10-12T20:00:00+03:00',
  });
});

test('a hidden tab waits and re-reads once when shown', () => {
  const { feed, visibility, target } = setup();
  visibility.set(false);
  feed.emit(change('1'));
  feed.emit({ type: 'tick', serverTime: '2026-10-12T12:01:00+03:00' });
  feed.emit(change('2'));
  assert.deepEqual(target.marks, []);
  visibility.set(true);
  visibility.set(true);
  assert.deepEqual(target.marks, ['all']);
});

test('a hidden tab that only saw ticks re-reads time models', () => {
  const { feed, visibility, target } = setup();
  feed.emit(change('1'));
  visibility.set(false);
  feed.emit({ type: 'tick', serverTime: '2026-10-12T12:01:00+03:00' });
  visibility.set(true);
  assert.deepEqual(target.marks, ['all', 'time']);
});

test('data turns stale after 10 s without an open stream', () => {
  const { feed, scheduler, sync } = setup();
  feed.status('connecting');
  scheduler.advance(staleAfterMs - 1);
  assert.equal(sync.getSnapshot().stale, false);
  scheduler.advance(1);
  assert.equal(sync.getSnapshot().stale, true);
  feed.status('open');
  assert.equal(sync.getSnapshot().stale, false);
});

test('reconnect attempts do not restart the 10 s count', async () => {
  const { feed, scheduler, sync } = setup();
  feed.status('connecting');
  scheduler.advance(6000);
  feed.status('closed');
  await settled();
  scheduler.advance(reconnectDelayMs);
  assert.equal(feed.subscriptions, 2);
  scheduler.advance(staleAfterMs - 6000 - reconnectDelayMs);
  assert.equal(sync.getSnapshot().stale, true);
});

test('the stream counts as not open from start', () => {
  const feed = manualFeed();
  const scheduler = manualScheduler();
  const sync = createLiveSync({
    feed,
    scheduler,
    visibility: manualVisibility(),
    sessionCheck: sessionIs('active'),
    resolvePending: () => Promise.resolve(),
  });
  sync.start();
  assert.equal(sync.getSnapshot().connection, 'connecting');
  scheduler.advance(staleAfterMs);
  assert.equal(sync.getSnapshot().stale, true);
});

test('an unavailable warehouse makes data stale while the stream is open', () => {
  const { feed, sync } = setup();
  feed.emit({ type: 'upstream', available: false });
  assert.equal(sync.getSnapshot().stale, true);
  feed.emit({ type: 'upstream', available: true });
  assert.equal(sync.getSnapshot().stale, false);
});

test('a closed feed of an active session is subscribed again after the retry delay', async () => {
  const { feed, scheduler } = setup();
  feed.status('closed');
  assert.equal(feed.active, undefined);
  await settled();
  scheduler.advance(reconnectDelayMs - 1);
  assert.equal(feed.subscriptions, 1);
  scheduler.advance(1);
  assert.equal(feed.subscriptions, 2);
});

test('a closed feed of an ended session stops for good', async () => {
  const { feed, scheduler, sync } = setup({ session: sessionIs('ended') });
  feed.status('closed');
  await settled();
  assert.equal(scheduler.pending(), 0);
  scheduler.advance(60_000);
  assert.equal(feed.subscriptions, 1);
  assert.deepEqual(sync.getSnapshot(), {
    connection: 'closed',
    stale: false,
    serverTime: undefined,
    session: undefined,
    sessionEnded: 'expired',
  });
});

test('an unknown or failed session check subscribes again', async () => {
  for (const session of [
    sessionIs('unknown'),
    { check: () => Promise.reject(new Error('offline')) },
  ]) {
    const { feed, scheduler, sync } = setup({ session });
    feed.status('closed');
    await settled();
    scheduler.advance(reconnectDelayMs);
    assert.equal(feed.subscriptions, 2);
    assert.equal(sync.getSnapshot().sessionEnded, undefined);
  }
});

test('the 10 s count runs while the session is checked', () => {
  const session = heldSessionCheck();
  const { feed, scheduler, sync } = setup({ session });
  feed.status('closed');
  scheduler.advance(staleAfterMs);
  assert.equal(session.answers.length, 1);
  assert.equal(sync.getSnapshot().stale, true);
});

test('stop during a session check does not subscribe again', async () => {
  const session = heldSessionCheck();
  const { feed, scheduler, sync } = setup({ session });
  feed.status('closed');
  sync.stop();
  session.answers[0]('active');
  await settled();
  assert.equal(scheduler.pending(), 0);
  scheduler.advance(60_000);
  assert.equal(feed.subscriptions, 1);
});

test('a restart ignores the answer of the check it replaced', async () => {
  const session = heldSessionCheck();
  const { feed, sync } = setup({ session });
  feed.status('closed');
  sync.stop();
  sync.start();
  session.answers[0]('ended');
  await settled();
  assert.equal(sync.getSnapshot().sessionEnded, undefined);
  assert.notEqual(feed.active, undefined);
});

function countingResolver() {
  const calls = [];
  const resolvePending = () =>
    new Promise((resolve) => {
      calls.push(resolve);
    });
  return { calls, resolvePending };
}

test('pending attempts are resolved once per change signal', async () => {
  const resolver = countingResolver();
  const { feed } = setup(resolver);
  feed.emit(change('1'));
  assert.equal(resolver.calls.length, 1);
  resolver.calls[0]();
  await settled();
  feed.emit(change('1'));
  assert.equal(resolver.calls.length, 1);
  feed.emit(change('2'));
  assert.equal(resolver.calls.length, 2);
});

test('a reconnect without new changes resolves attempts but re-reads nothing', async () => {
  const resolver = countingResolver();
  const { feed, target } = setup(resolver);
  feed.emit(change('5'));
  resolver.calls[0]();
  await settled();
  feed.status('connecting');
  feed.status('open');
  feed.emit(change('5'));
  assert.equal(resolver.calls.length, 2);
  assert.deepEqual(target.marks, ['all']);
});

test('resolve calls never overlap and signals during a call add one call', async () => {
  const resolver = countingResolver();
  const { feed } = setup(resolver);
  feed.emit(change('1'));
  feed.emit(change('2'));
  feed.emit(change('3'));
  assert.equal(resolver.calls.length, 1);
  resolver.calls[0]();
  await settled();
  assert.equal(resolver.calls.length, 2);
  resolver.calls[1]();
  await settled();
  assert.equal(resolver.calls.length, 2);
});

test('a failed resolve call does not block later signals', async () => {
  let calls = 0;
  const { feed } = setup({
    resolvePending: () => {
      calls += 1;
      return Promise.reject(new Error('UpstreamUnavailable'));
    },
  });
  feed.emit(change('1'));
  await settled();
  feed.emit(change('2'));
  assert.equal(calls, 2);
});

test('session-ended stops the stream for good', () => {
  const { feed, scheduler, sync, target } = setup();
  feed.emit({ type: 'session-ended', reason: 'expired' });
  assert.equal(feed.active, undefined);
  assert.equal(scheduler.pending(), 0);
  scheduler.advance(60_000);
  assert.equal(feed.subscriptions, 1);
  assert.deepEqual(sync.getSnapshot(), {
    connection: 'closed',
    stale: false,
    serverTime: undefined,
    session: undefined,
    sessionEnded: 'expired',
  });
  assert.deepEqual(target.marks, []);
});

test('stop cancels a scheduled reconnect and the stale timer', async () => {
  const { feed, scheduler, sync } = setup();
  feed.status('closed');
  await settled();
  assert.equal(scheduler.pending(), 2);
  sync.stop();
  assert.equal(scheduler.pending(), 0);
  scheduler.advance(60_000);
  assert.equal(feed.subscriptions, 1);
  assert.equal(sync.getSnapshot().connection, 'closed');
});
```

- [ ] **Step 2: Run it and see it fail**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/live-sync.test.mjs`
Expected: FAIL — `error: Cannot find module '../../src/application/live-sync.ts' from '…/tests/unit/live-sync.test.mjs'`.

- [ ] **Step 3: Minimal implementation**

Создайте `src/application/live-sync.ts`:

```ts
import type {
  LiveEvent,
  LiveFeed,
  LiveStatus,
  SessionEndReason,
} from '@/application/live-feed';

export interface Scheduler {
  // Runs callback once after delayMs; the returned function cancels it.
  schedule(delayMs: number, callback: () => void): () => void;
}

export interface PageVisibility {
  isVisible(): boolean;
  subscribe(onChange: () => void): () => void;
}

export type SessionStatus = 'active' | 'ended' | 'unknown';

// Tells whether the operator session still exists without extending it.
export interface SessionCheck {
  check(): Promise<SessionStatus>;
}

// 'all' follows a state change; 'time' follows a tick and concerns models that
// depend on the current time: the expiry window and recommendations.
export type StaleScope = 'all' | 'time';

export interface StaleTarget {
  markStale(scope: StaleScope): void;
}

export interface LiveSession {
  readonly idleExpiresAt: string;
  readonly absoluteExpiresAt: string;
}

export interface LiveState {
  readonly connection: LiveStatus;
  // Shown data may be outdated: the stream has not been open for
  // staleAfterMs, or gateway reports the warehouse unavailable.
  readonly stale: boolean;
  readonly serverTime: string | undefined;
  readonly session: LiveSession | undefined;
  readonly sessionEnded: SessionEndReason | undefined;
}

export interface LiveSync {
  start(): void;
  stop(): void;
  track(target: StaleTarget): () => void;
  readonly subscribe: (listener: () => void) => () => void;
  readonly getSnapshot: () => LiveState;
}

export interface LiveSyncOptions {
  feed: LiveFeed;
  scheduler: Scheduler;
  visibility: PageVisibility;
  // Asked after the feed gave up: the browser hides whether a 401 or a 502
  // refused the stream.
  sessionCheck: SessionCheck;
  // Re-checks ConfirmationPending attempts with their original commandID and
  // proposalID. A rejection keeps the attempts for the next signal.
  resolvePending: () => Promise<void>;
}

export const staleAfterMs = 10_000;
// Matches the stream's retry: 3000 for a feed that gave up.
export const reconnectDelayMs = 3_000;

export function createLiveSync({
  feed,
  scheduler,
  visibility,
  sessionCheck,
  resolvePending,
}: LiveSyncOptions): LiveSync {
  const targets = new Set<StaleTarget>();
  const listeners = new Set<() => void>();
  let running = false;
  let connection: LiveStatus = 'closed';
  let disconnectedTooLong = false;
  let upstreamAvailable = true;
  let serverTime: string | undefined;
  let session: LiveSession | undefined;
  let sessionEnded: SessionEndReason | undefined;
  let state = snapshot();
  let known: { epoch: string; seq: bigint } | undefined;
  let firstChangeOfConnection = false;
  let deferred: StaleScope | undefined;
  let resolving = false;
  let resolveAgain = false;
  let unsubscribeFeed: (() => void) | undefined;
  let cancelReconnect: (() => void) | undefined;
  let pendingCheck: object | undefined;
  let cancelStaleTimer: (() => void) | undefined;
  let stopVisibility: (() => void) | undefined;

  function snapshot(): LiveState {
    return {
      connection,
      stale: disconnectedTooLong || !upstreamAvailable,
      serverTime,
      session,
      sessionEnded,
    };
  }

  function publish(): void {
    state = snapshot();
    for (const listener of listeners) listener();
  }

  function setConnection(status: LiveStatus): void {
    connection = status;
    if (status === 'open') {
      cancelStaleTimer?.();
      cancelStaleTimer = undefined;
      disconnectedTooLong = false;
      firstChangeOfConnection = true;
    } else if (!cancelStaleTimer && !disconnectedTooLong) {
      cancelStaleTimer = scheduler.schedule(staleAfterMs, () => {
        cancelStaleTimer = undefined;
        disconnectedTooLong = true;
        publish();
      });
    }
    publish();
  }

  function connect(): void {
    setConnection('connecting');
    unsubscribeFeed = feed.subscribe(onEvent, onStatus);
  }

  function disconnect(): void {
    unsubscribeFeed?.();
    unsubscribeFeed = undefined;
    cancelReconnect?.();
    cancelReconnect = undefined;
  }

  function onStatus(status: LiveStatus): void {
    setConnection(status);
    if (status !== 'closed') return;
    disconnect();
    checkSession();
  }

  // An ended session stops the stream for good; otherwise the feed is
  // subscribed again. One check runs per refused stream, and halt() discards
  // its answer.
  function checkSession(): void {
    if (pendingCheck) return;
    const token = {};
    pendingCheck = token;
    sessionCheck.check().then(
      (status) => {
        checked(token, status);
      },
      () => {
        checked(token, 'unknown');
      },
    );
  }

  function checked(token: object, status: SessionStatus): void {
    if (pendingCheck !== token) return;
    pendingCheck = undefined;
    if (status === 'ended') {
      endSession('expired');
      return;
    }
    cancelReconnect = scheduler.schedule(reconnectDelayMs, () => {
      cancelReconnect = undefined;
      connect();
    });
  }

  function endSession(reason: SessionEndReason): void {
    halt();
    sessionEnded = reason;
    publish();
  }

  function onEvent(event: LiveEvent): void {
    switch (event.type) {
      case 'change':
        onChange(event.epoch, event.seq);
        return;
      case 'tick':
        serverTime = event.serverTime;
        publish();
        markStale('time');
        return;
      case 'session':
        session = {
          idleExpiresAt: event.idleExpiresAt,
          absoluteExpiresAt: event.absoluteExpiresAt,
        };
        publish();
        return;
      case 'session-ended':
        endSession(event.reason);
        return;
      case 'upstream':
        upstreamAvailable = event.available;
        publish();
        return;
    }
  }

  // The first change after start or a new epoch re-reads everything: a model
  // read before the stream opened may already be outdated. A smaller or equal
  // seq of the same epoch changes nothing.
  function onChange(epoch: string, seq: bigint): void {
    const reconnected = firstChangeOfConnection;
    firstChangeOfConnection = false;
    const newer = known?.epoch !== epoch || seq > known.seq;
    if (newer) {
      known = { epoch, seq };
      markStale('all');
    }
    if (newer || reconnected) requestResolve();
  }

  // A hidden tab keeps one pending scope and re-reads once it is shown.
  function markStale(scope: StaleScope): void {
    if (!visibility.isVisible()) {
      deferred = deferred === 'all' ? 'all' : scope;
      return;
    }
    for (const target of targets) target.markStale(scope);
  }

  function onVisibilityChange(): void {
    if (deferred === undefined || !visibility.isVisible()) return;
    const scope = deferred;
    deferred = undefined;
    markStale(scope);
  }

  // Attempts are checked one call at a time. Signals during a call cause
  // exactly one more call after it. A rejected call keeps its attempts for
  // the next signal.
  function requestResolve(): void {
    if (resolving) {
      resolveAgain = true;
      return;
    }
    resolving = true;
    resolvePending().then(resolved, resolved);
  }

  function resolved(): void {
    resolving = false;
    const again = resolveAgain && running;
    resolveAgain = false;
    if (again) requestResolve();
  }

  function halt(): void {
    running = false;
    disconnect();
    pendingCheck = undefined;
    cancelStaleTimer?.();
    cancelStaleTimer = undefined;
    stopVisibility?.();
    stopVisibility = undefined;
    deferred = undefined;
    connection = 'closed';
    disconnectedTooLong = false;
  }

  return {
    start() {
      if (running) return;
      running = true;
      known = undefined;
      upstreamAvailable = true;
      session = undefined;
      sessionEnded = undefined;
      stopVisibility = visibility.subscribe(onVisibilityChange);
      connect();
    },
    stop() {
      if (!running) return;
      halt();
      publish();
    },
    track(target) {
      targets.add(target);
      return () => {
        targets.delete(target);
      };
    },
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => state,
  };
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/live-sync.test.mjs && npm run typecheck && npm run lint && npm run arch` Expected: PASS — `22 pass`, `0 fail`; typecheck, lint и arch без ошибок.

- [ ] **Step 5: Document the behaviour**

В `README.md` допишите в конец пункта `` - `application` — … ``:

```markdown
 Сценарий `createLiveSync` реагирует на эти события; таймеры и видимость вкладки он получает через порты `Scheduler` и `PageVisibility`.
```

В конец подраздела «Обновления в реальном времени» добавьте:

```markdown
Сценарий `createLiveSync` из `application/live-sync.ts`:

- `change` с тем же `epoch` и большим `seq` помечает устаревшими все модели чтения. Номер не больше известного ничего не меняет. Новый `epoch` (gateway перезапустился) и первое `change` после запуска перечитывают всё: данные, прочитанные до открытия потока, могли устареть.
- Попытки `ConfirmationPending` проверяются через `resolvePending` после каждого нового `change` и после каждого переподключения, по одному вызову за раз. Сигналы во время вызова дают ровно один следующий вызов.
- `tick` обновляет время сервера и перечитывает модели, зависящие от времени: окно сроков и рекомендации. Относительные метки только форматируют серверный `expiresAt`; истечение определяет сервер.
- `session` обновляет сроки сессии в состоянии сценария.
- `session-ended` закрывает поток без переподключения и сохраняет причину для экрана «сессия истекла».
- Если поток не открыт 10 с или пришло `upstream: false`, флаг `stale` включает состояние «Устаревшие показанные данные». Повторные попытки подключения не начинают отсчёт 10 с заново.
- Если адаптер сообщил `closed`, сценарий проверяет сессию через порт `SessionCheck`: браузер не сообщает, отказал ли gateway с `401` или Nginx с `502`. Закончившаяся сессия останавливает поток навсегда и открывает «сессия истекла» с причиной `expired`. В остальных случаях, в том числе когда проверка не получила ответа, поток подключается снова через 3 с, как `retry: 3000` потока. Отсчёт 10 с при этом не сбрасывается, а `stop()` отменяет ожидаемую проверку и переподключение.
- Скрытая вкладка откладывает перечитывание до `visibilitychange` и затем перечитывает один раз.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Добавлен сценарий live-sync. Номера изменений сравниваются в пределах `epoch`, новый `epoch` перечитывает всё, `tick` перечитывает модели, зависящие от времени, а `session-ended` останавливает поток без переподключения. Через 10 с без открытого потока или при `upstream: false` включается состояние устаревших данных. Скрытая вкладка откладывает перечитывание. Закрытый поток подключается снова через 3 с, если проверка сессии не показала, что сессия закончилась; иначе live-sync останавливается с причиной `expired`.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/application/live-sync.ts tests/unit/live-sync.test.mjs README.md CHANGELOG.md
git commit -m "feat(live): add the live-sync scenario"
```

---

### Task 3: Модель чтения экрана

**Files:**
- Create: `src/application/read-model.ts`
- Modify: `tests/unit/live-sync.test.mjs` (импорт после строки `} from '../../src/application/live-sync.ts';`, тест в конец файла)
- Modify: `README.md` (пункт `application`; подраздел «Обновления в реальном времени»)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `tests/unit/read-model.test.mjs`, `tests/unit/live-sync.test.mjs`

**Interfaces:**
- Consumes: `StaleScope`, `StaleTarget`, `createLiveSync` из Task 2.
- Produces:
  - `interface LoadOptions { readonly background: boolean }`
  - `interface ReadModelSnapshot<T> { readonly value: T | undefined; readonly loadedAt: number | undefined; readonly loading: boolean; readonly failed: boolean }`
  - `interface ReadModel<T> extends StaleTarget { readonly subscribe: (listener: () => void) => () => void; readonly getSnapshot: () => ReadModelSnapshot<T>; refresh(): void }`
  - `interface ReadModelOptions { readonly refreshOnTick: boolean; readonly now: () => number }`
  - `createReadModel<T>(load: (options: LoadOptions) => Promise<T>, options: ReadModelOptions): ReadModel<T>`

Загрузчик выполняет успешное чтение или отклоняет промис при неудаче; `loadedAt` — миллисекунды подставленных часов (`now`), потому что `Date` в `application` запрещён. Первая подписка и `refresh()` читают без фонового заголовка (действие оператора), `markStale` — с `{ background: true }`.

- [ ] **Step 1: Write the failing test**

Создайте `tests/unit/read-model.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import { createReadModel } from '../../src/application/read-model.ts';

// Each load returns a promise the test settles by hand.
function controlledLoader() {
  const calls = [];
  const load = (options) =>
    new Promise((resolve, reject) => {
      calls.push({ options, resolve, reject });
    });
  return { calls, load };
}

const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

function model(loader, { refreshOnTick = false, now = () => 1000 } = {}) {
  return createReadModel(loader.load, { refreshOnTick, now });
}

test('the first subscriber reads once in the foreground', async () => {
  const loader = controlledLoader();
  const readModel = model(loader, { now: () => 5000 });
  readModel.subscribe(() => undefined);
  readModel.subscribe(() => undefined);
  assert.deepEqual(
    loader.calls.map((call) => call.options),
    [{ background: false }],
  );
  assert.equal(readModel.getSnapshot().loading, true);
  loader.calls[0].resolve('остатки');
  await settled();
  assert.deepEqual(readModel.getSnapshot(), {
    value: 'остатки',
    loadedAt: 5000,
    loading: false,
    failed: false,
  });
});

// React StrictMode subscribes, unsubscribes and subscribes again on mount.
test('a remount during the first read does not read twice', () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  const unsubscribe = readModel.subscribe(() => undefined);
  unsubscribe();
  readModel.subscribe(() => undefined);
  assert.equal(loader.calls.length, 1);
});

test('a stale model without subscribers reads on the next subscription', async () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  const unsubscribe = readModel.subscribe(() => undefined);
  loader.calls[0].resolve('v1');
  await settled();
  unsubscribe();
  readModel.markStale('all');
  assert.equal(loader.calls.length, 1);
  readModel.subscribe(() => undefined);
  assert.deepEqual(
    loader.calls.map((call) => call.options),
    [{ background: false }, { background: false }],
  );
});

test('marks during a request cause exactly one background request after it', async () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  readModel.subscribe(() => undefined);
  readModel.markStale('all');
  readModel.markStale('all');
  readModel.markStale('all');
  assert.equal(loader.calls.length, 1);
  loader.calls[0].resolve('v1');
  await settled();
  assert.deepEqual(
    loader.calls.map((call) => call.options),
    [{ background: false }, { background: true }],
  );
  loader.calls[1].resolve('v2');
  await settled();
  assert.equal(loader.calls.length, 2);
  assert.equal(readModel.getSnapshot().value, 'v2');
});

test('only the response to the latest request is applied', async () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  readModel.subscribe(() => undefined);
  readModel.refresh();
  loader.calls[1].resolve('newer');
  await settled();
  loader.calls[0].resolve('older');
  await settled();
  assert.equal(readModel.getSnapshot().value, 'newer');
});

test('a superseded response does not end the loading state', async () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  readModel.subscribe(() => undefined);
  readModel.refresh();
  loader.calls[0].resolve('older');
  await settled();
  assert.deepEqual(readModel.getSnapshot(), {
    value: undefined,
    loadedAt: undefined,
    loading: true,
    failed: false,
  });
});

test('a failed read keeps the last value and its time', async () => {
  let clock = 1000;
  const loader = controlledLoader();
  const readModel = model(loader, { now: () => clock });
  readModel.subscribe(() => undefined);
  loader.calls[0].resolve('v1');
  await settled();
  clock = 7000;
  readModel.refresh();
  loader.calls[1].reject(new Error('UpstreamUnavailable'));
  await settled();
  assert.deepEqual(readModel.getSnapshot(), {
    value: 'v1',
    loadedAt: 1000,
    loading: false,
    failed: true,
  });
});

test('ticks re-read only models that depend on time', async () => {
  const plain = controlledLoader();
  const timed = controlledLoader();
  const plainModel = model(plain);
  const timedModel = model(timed, { refreshOnTick: true });
  plainModel.subscribe(() => undefined);
  timedModel.subscribe(() => undefined);
  plain.calls[0].resolve('v1');
  timed.calls[0].resolve('v1');
  await settled();
  plainModel.markStale('time');
  timedModel.markStale('time');
  assert.equal(plain.calls.length, 1);
  assert.deepEqual(timed.calls[1].options, { background: true });
});

test('subscribers hear every snapshot change', async () => {
  const loader = controlledLoader();
  const readModel = model(loader);
  const seen = [];
  readModel.subscribe(() => {
    seen.push(readModel.getSnapshot().loading);
  });
  loader.calls[0].resolve('v1');
  await settled();
  assert.deepEqual(seen, [true, false]);
});
```

В `tests/unit/live-sync.test.mjs` после блока импорта из `live-sync.ts` добавьте:

```js
import { createReadModel } from '../../src/application/read-model.ts';
```

и в конец файла:

```js
test('signals during a read cause exactly one more read', async () => {
  const feed = manualFeed();
  const sync = createLiveSync({
    feed,
    scheduler: manualScheduler(),
    visibility: manualVisibility(),
    sessionCheck: sessionIs('active'),
    resolvePending: () => Promise.resolve(),
  });
  const calls = [];
  const model = createReadModel(
    (options) =>
      new Promise((resolve) => {
        calls.push({ options, resolve });
      }),
    { refreshOnTick: false, now: () => 0 },
  );
  sync.track(model);
  model.subscribe(() => undefined);
  sync.start();
  feed.status('open');
  feed.emit(change('1'));
  feed.emit(change('2'));
  feed.emit(change('3'));
  calls[0].resolve('v1');
  await settled();
  assert.deepEqual(
    calls.map((call) => call.options),
    [{ background: false }, { background: true }],
  );
});
```

- [ ] **Step 2: Run it and see it fail**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/read-model.test.mjs ./tests/unit/live-sync.test.mjs`
Expected: FAIL — `error: Cannot find module '../../src/application/read-model.ts'` для обоих файлов.

- [ ] **Step 3: Minimal implementation**

Создайте `src/application/read-model.ts`:

```ts
import type { StaleScope, StaleTarget } from '@/application/live-sync';

export interface LoadOptions {
  // True when a live signal or tick caused the read; the request then sends
  // X-Umir-Activity: background and does not extend the session.
  readonly background: boolean;
}

export interface ReadModelSnapshot<T> {
  // The response to the latest settled request, or the previous one if that
  // request failed.
  readonly value: T | undefined;
  // Clock time of the last successful read in milliseconds since the epoch.
  readonly loadedAt: number | undefined;
  readonly loading: boolean;
  readonly failed: boolean;
}

export interface ReadModel<T> extends StaleTarget {
  readonly subscribe: (listener: () => void) => () => void;
  readonly getSnapshot: () => ReadModelSnapshot<T>;
  // A read the operator caused. It starts at once and supersedes the request
  // in flight.
  refresh(): void;
}

export interface ReadModelOptions {
  // Re-read on every tick, for models that depend on the current time.
  readonly refreshOnTick: boolean;
  readonly now: () => number;
}

// Data of one view. Only the response to the latest request is applied: the
// order comes from a client request counter, not from stateVersion. A model
// without subscribers only remembers that it is stale and reads on the next
// subscription.
export function createReadModel<T>(
  load: (options: LoadOptions) => Promise<T>,
  { refreshOnTick, now }: ReadModelOptions,
): ReadModel<T> {
  const listeners = new Set<() => void>();
  let snapshot: ReadModelSnapshot<T> = {
    value: undefined,
    loadedAt: undefined,
    loading: false,
    failed: false,
  };
  let latest = 0;
  let stale = true;

  function update(next: ReadModelSnapshot<T>): void {
    snapshot = next;
    for (const listener of listeners) listener();
  }

  function start(background: boolean): void {
    latest += 1;
    const request = latest;
    stale = false;
    update({ ...snapshot, loading: true });
    load({ background }).then(
      (value) => {
        settle(request, {
          value,
          loadedAt: now(),
          loading: false,
          failed: false,
        });
      },
      () => {
        settle(request, { ...snapshot, loading: false, failed: true });
      },
    );
  }

  function settle(request: number, next: ReadModelSnapshot<T>): void {
    if (request !== latest) return;
    update(next);
    if (stale && listeners.size > 0) start(true);
  }

  return {
    subscribe: (listener) => {
      listeners.add(listener);
      if (listeners.size === 1 && stale && !snapshot.loading) start(false);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => snapshot,
    refresh() {
      start(false);
    },
    markStale(scope: StaleScope) {
      if (scope === 'time' && !refreshOnTick) return;
      stale = true;
      if (listeners.size > 0 && !snapshot.loading) start(true);
    },
  };
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/read-model.test.mjs ./tests/unit/live-sync.test.mjs && npm run typecheck && npm run lint && npm run arch` Expected: PASS — `32 pass`, `0 fail` (9 + 23); typecheck, lint и arch без ошибок.

- [ ] **Step 5: Document the behaviour**

В `README.md` допишите в конец пункта `` - `application` — … ``:

```markdown
 `createReadModel` хранит данные одного экрана и применяет только ответ на последний запрос.
```

В конец подраздела «Обновления в реальном времени» добавьте:

```markdown
Модель чтения `createReadModel` из `application/read-model.ts` хранит ответ одного экрана. Порядок ответов задаёт счётчик запросов клиента, а не `stateVersion`: ответ на более ранний запрос отбрасывается. Модель без подписчиков только запоминает, что устарела, и читает при следующей подписке. Если запрос уже идёт, а модель снова помечена устаревшей, после него выполняется ровно один запрос. Загрузчик получает `{ background }`: `true` при перечитывании по сигналу или такту. Неудачное чтение сохраняет прежние данные и время последнего успешного чтения `loadedAt`.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Добавлены модели чтения экранов. Применяется только ответ на последний запрос по счётчику клиента, а сигналы во время запроса дают ровно один следующий запрос.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/application/read-model.ts tests/unit/read-model.test.mjs tests/unit/live-sync.test.mjs README.md CHANGELOG.md
git commit -m "feat(live): add read models that apply only the latest response"
```

---

### Task 4: Адаптеры видимости вкладки и таймеров

**Files:**
- Create: `src/adapters/browser-page/visibility.ts`
- Create: `src/adapters/browser-page/scheduler.ts`
- Modify: `README.md` (пункт `adapters`)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `tests/unit/browser-page.test.mjs`

**Interfaces:**
- Consumes: `PageVisibility`, `Scheduler` из Task 2.
- Produces:
  - `type VisibilityDocument = Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>`; `createPageVisibility(page: VisibilityDocument): PageVisibility`
  - `type Timers = Pick<Window, 'setTimeout' | 'clearTimeout'>`; `createTimerScheduler(timers: Timers): Scheduler`

- [ ] **Step 1: Write the failing test**

Создайте `tests/unit/browser-page.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import { createTimerScheduler } from '../../src/adapters/browser-page/scheduler.ts';
import { createPageVisibility } from '../../src/adapters/browser-page/visibility.ts';

test('visibility follows the document and its visibilitychange event', () => {
  const listeners = new Set();
  const page = {
    visibilityState: 'visible',
    addEventListener: (type, listener) => {
      assert.equal(type, 'visibilitychange');
      listeners.add(listener);
    },
    removeEventListener: (type, listener) => {
      assert.equal(type, 'visibilitychange');
      listeners.delete(listener);
    },
  };
  const visibility = createPageVisibility(page);
  let changes = 0;
  const unsubscribe = visibility.subscribe(() => {
    changes += 1;
  });
  assert.equal(visibility.isVisible(), true);
  page.visibilityState = 'hidden';
  for (const listener of listeners) listener();
  assert.equal(visibility.isVisible(), false);
  assert.equal(changes, 1);
  unsubscribe();
  assert.equal(listeners.size, 0);
});

test('the scheduler cancels the timer it started', () => {
  const started = [];
  const cleared = [];
  const scheduler = createTimerScheduler({
    setTimeout: (callback, delay) => {
      started.push({ callback, delay });
      return 41;
    },
    clearTimeout: (id) => cleared.push(id),
  });
  const callback = () => undefined;
  const cancel = scheduler.schedule(3000, callback);
  assert.deepEqual(started, [{ callback, delay: 3000 }]);
  cancel();
  assert.deepEqual(cleared, [41]);
});
```

- [ ] **Step 2: Run it and see it fail**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/browser-page.test.mjs`
Expected: FAIL — `error: Cannot find module '../../src/adapters/browser-page/scheduler.ts'`.

- [ ] **Step 3: Minimal implementation**

Создайте `src/adapters/browser-page/visibility.ts`:

```ts
import type { PageVisibility } from '@/application/live-sync';

export type VisibilityDocument = Pick<
  Document,
  'visibilityState' | 'addEventListener' | 'removeEventListener'
>;

export function createPageVisibility(page: VisibilityDocument): PageVisibility {
  return {
    isVisible: () => page.visibilityState === 'visible',
    subscribe(onChange) {
      const listener = () => {
        onChange();
      };
      page.addEventListener('visibilitychange', listener);
      return () => {
        page.removeEventListener('visibilitychange', listener);
      };
    },
  };
}
```

Создайте `src/adapters/browser-page/scheduler.ts`:

```ts
import type { Scheduler } from '@/application/live-sync';

export type Timers = Pick<Window, 'setTimeout' | 'clearTimeout'>;

export function createTimerScheduler(timers: Timers): Scheduler {
  return {
    schedule(delayMs, callback) {
      const id = timers.setTimeout(callback, delayMs);
      return () => {
        timers.clearTimeout(id);
      };
    },
  };
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/browser-page.test.mjs && npm run typecheck && npm run lint && npm run arch` Expected: PASS — `2 pass`, `0 fail`; typecheck, lint и arch без ошибок.

- [ ] **Step 5: Document the behaviour**

В `README.md` допишите в конец пункта `` - `adapters` — … ``:

```markdown
 `browser-page` даёт live-sync видимость вкладки через `visibilitychange` и таймеры `setTimeout`.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Добавлены адаптеры видимости вкладки и таймеров браузера для live-sync.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/adapters/browser-page/visibility.ts src/adapters/browser-page/scheduler.ts tests/unit/browser-page.test.mjs README.md CHANGELOG.md
git commit -m "feat(live): add page visibility and timer adapters"
```

---

### Task 5: Фоновые чтения: `X-Umir-Activity: background` и проверка сессии

**Files:**
- Create: `src/adapters/http/read-request.ts`
- Create: `src/adapters/http/session-check.ts`
- Modify: `README.md` (пункт `adapters`; подраздел «Обновления в реальном времени»)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `tests/unit/read-request.test.mjs`, `tests/unit/session-check.test.mjs`

**Interfaces:**
- Consumes: `LoadOptions` из Task 3; `SessionCheck`, `SessionStatus` из Task 2; публичный заголовок `X-Umir-Activity` из плана gateway; `GET /api/operator/me` (`getCurrentOperator`: `200` `OperatorView`, `401 Unauthenticated`, `502`/`503`/`504`).
- Produces:
  - `const activityHeader = 'X-Umir-Activity'`; `readRequestInit(options: LoadOptions): RequestInit`. Будущий HTTP-клиент передаёт результат в `fetch(path, readRequestInit(options))` внутри загрузчика модели чтения.
  - `const sessionCheckPath = '/api/operator/me'`; `type SendRequest = (path: string, init: RequestInit) => Promise<{ readonly status: number }>`; `createSessionCheck(send: SendRequest): SessionCheck`.

API-клиента пока нет (README: «Ещё не реализованы API-клиент…»), поэтому задача добавляет параметры запроса и проверку сессии, а не клиент: этого достаточно, чтобы контракт заголовка проверялся тестом. Проверка сессии читает только статус ответа: `200` → `active`, `401` → `ended`, любой другой статус или сетевая ошибка → `unknown`. Заголовок `X-Umir-Activity: background` не даёт проверке продлить сессию.

- [ ] **Step 1: Write the failing test**

Создайте `tests/unit/read-request.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import {
  activityHeader,
  readRequestInit,
} from '../../src/adapters/http/read-request.ts';

test('a background read sends exactly X-Umir-Activity: background', () => {
  assert.equal(activityHeader, 'X-Umir-Activity');
  assert.deepEqual(readRequestInit({ background: true }), {
    method: 'GET',
    headers: { 'X-Umir-Activity': 'background' },
  });
});

test('a read the operator caused sends no activity header', () => {
  assert.deepEqual(readRequestInit({ background: false }), {
    method: 'GET',
    headers: {},
  });
});
```

Создайте `tests/unit/session-check.test.mjs`:

```js
import assert from 'node:assert/strict';
import { test } from 'bun:test';
import {
  createSessionCheck,
  sessionCheckPath,
} from '../../src/adapters/http/session-check.ts';

function recordingSend(answer) {
  const calls = [];
  const send = (path, init) => {
    calls.push({ path, init });
    return answer();
  };
  return { calls, send };
}

test('the profile status tells the session state', async () => {
  for (const [status, expected] of [
    [200, 'active'],
    [401, 'ended'],
    [500, 'unknown'],
    [502, 'unknown'],
    [503, 'unknown'],
  ]) {
    const { send } = recordingSend(() => Promise.resolve({ status }));
    assert.equal(
      await createSessionCheck(send).check(),
      expected,
      String(status),
    );
  }
});

test('a network error leaves the session state unknown', async () => {
  const { send } = recordingSend(() =>
    Promise.reject(new TypeError('Failed to fetch')),
  );
  assert.equal(await createSessionCheck(send).check(), 'unknown');
});

test('the check is a background read of the operator profile', async () => {
  const { calls, send } = recordingSend(() => Promise.resolve({ status: 200 }));
  await createSessionCheck(send).check();
  assert.equal(sessionCheckPath, '/api/operator/me');
  assert.deepEqual(calls, [
    {
      path: '/api/operator/me',
      init: { method: 'GET', headers: { 'X-Umir-Activity': 'background' } },
    },
  ]);
});
```

- [ ] **Step 2: Run it and see it fail**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/read-request.test.mjs ./tests/unit/session-check.test.mjs`
Expected: FAIL — `error: Cannot find module '../../src/adapters/http/read-request.ts'` для обоих файлов.

- [ ] **Step 3: Minimal implementation**

Создайте `src/adapters/http/read-request.ts`:

```ts
import type { LoadOptions } from '@/application/read-model';

// Gateway's public header: a background read validates the session without
// extending it. Only the exact value "background" has meaning.
export const activityHeader = 'X-Umir-Activity';

// Options for a GET through gateway. Reads after a live signal or tick are
// background; reads the operator caused send no activity header.
export function readRequestInit({ background }: LoadOptions): RequestInit {
  return {
    method: 'GET',
    headers: background ? { [activityHeader]: 'background' } : {},
  };
}
```

Создайте `src/adapters/http/session-check.ts`:

```ts
import { readRequestInit } from '@/adapters/http/read-request';
import type { SessionCheck } from '@/application/live-sync';

export const sessionCheckPath = '/api/operator/me';

export type SendRequest = (
  path: string,
  init: RequestInit,
) => Promise<{ readonly status: number }>;

// A background read of the operator profile: 401 means the session ended.
// Any other answer, or none, leaves the state unknown. The read never extends
// the session.
export function createSessionCheck(send: SendRequest): SessionCheck {
  return {
    async check() {
      try {
        const { status } = await send(
          sessionCheckPath,
          readRequestInit({ background: true }),
        );
        if (status === 200) return 'active';
        return status === 401 ? 'ended' : 'unknown';
      } catch {
        return 'unknown';
      }
    },
  };
}
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" bun test ./tests/unit/read-request.test.mjs ./tests/unit/session-check.test.mjs && npm run typecheck && npm run lint && npm run arch` Expected: PASS — `5 pass`, `0 fail` (2 + 3); typecheck, lint и arch без ошибок.

- [ ] **Step 5: Document the behaviour**

В `README.md` допишите в конец пункта `` - `adapters` — … ``:

```markdown
 `http/read-request` задаёт параметры GET: фоновое чтение отправляет `X-Umir-Activity: background`, а чтение по действию оператора идёт без этого заголовка. `http/session-check` фоновым `GET /api/operator/me` проверяет, жива ли сессия, и не продлевает её.
```

В конец подраздела «Обновления в реальном времени» добавьте:

```markdown
Перечитывание по сигналу или такту отправляет `X-Umir-Activity: background`: gateway проверяет сессию, но не продлевает её. Смысл имеет только точное значение `background`. Запросы, вызванные оператором, идут без заголовка и продлевают сессию. Тот же заголовок несёт проверка сессии после закрытого потока: `200` от `GET /api/operator/me` означает, что сессия жива, `401` — что она закончилась, а любой другой ответ или его отсутствие оставляют состояние неизвестным.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Фоновые чтения после сигнала потока или такта отправляют `X-Umir-Activity: background` и не продлевают сессию. Чтения по действию оператора идут без заголовка. После закрытого потока live-sync проверяет сессию фоновым `GET /api/operator/me`, чтобы при `401` показать «сессия истекла», а не переподключаться каждые 3 с.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/adapters/http/read-request.ts src/adapters/http/session-check.ts tests/unit/read-request.test.mjs tests/unit/session-check.test.mjs README.md CHANGELOG.md
git commit -m "feat(http): mark background reads and check the session in the background"
```

---

### Task 6: Сборка в `app` и доступ из presentation

**Files:**
- Create: `src/presentation/live/use-live.ts`
- Modify: `src/presentation/services.tsx:1-9`
- Modify: `src/app/main.tsx:10-13,32-37`
- Modify: `README.md` (пункты `presentation` и `app/main.tsx`; подраздел «Обновления в реальном времени»)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `npm run typecheck` (красный шаг), затем `npm run lint`, `npm run arch`, `npm run build`

**Interfaces:**
- Consumes: `createLiveSync`, `LiveSync`, `LiveState` (Task 2); `ReadModel`, `ReadModelSnapshot` (Task 3); `createEventSourceFeed` (Task 1); `createPageVisibility`, `createTimerScheduler` (Task 4); `createSessionCheck` (Task 5).
- Produces: `PresentationServices.live: LiveSync`; `useLiveState(): LiveState`; `useReadModel<T>(model: ReadModel<T>): ReadModelSnapshot<T>`.

Live-sync передаётся незапущенным: поток подключается только внутри `/app` после входа, а экранов `/app` ещё нет. Bun-тестов для хуков нет: в репозитории нет DOM-окружения, а серверный рендер `useSyncExternalStore` требует `getServerSnapshot`, которого клиентскому приложению не нужно. Поведение хуков — два вызова `useSyncExternalStore`; их проверяют typecheck и ESLint (`react-hooks`), а логику — тесты Task 2 и Task 3.

- [ ] **Step 1: Write the failing check**

Создайте `src/presentation/live/use-live.ts`:

```ts
import { useSyncExternalStore } from 'react';
import type { LiveState } from '@/application/live-sync';
import type { ReadModel, ReadModelSnapshot } from '@/application/read-model';
import { useServices } from '@/presentation/services';

export function useLiveState(): LiveState {
  const { live } = useServices();
  return useSyncExternalStore(live.subscribe, live.getSnapshot);
}

// Subscribing mounts the view's reads: the first subscriber loads a model that
// was never read or was marked stale while no view showed it.
export function useReadModel<T>(model: ReadModel<T>): ReadModelSnapshot<T> {
  return useSyncExternalStore(model.subscribe, model.getSnapshot);
}
```

- [ ] **Step 2: Run it and see it fail**

Run: `npm run typecheck`
Expected: FAIL — `src/presentation/live/use-live.ts(7,11): error TS2339: Property 'live' does not exist on type 'PresentationServices'.`

- [ ] **Step 3: Minimal implementation**

`src/presentation/services.tsx`, строки 1–9, замените на:

```tsx
import { createContext, useContext, type ReactNode } from 'react';
import type { CookieNoticeStore } from '@/application/cookie-notice';
import type { FeedbackSubmitter } from '@/application/feedback';
import type { LiveSync } from '@/application/live-sync';

// Application ports the screens use; the app layer assembles the adapters.
export interface PresentationServices {
  cookieNotice: CookieNoticeStore;
  feedback: FeedbackSubmitter;
  // Not started: /app starts it after sign-in and stops it on sign-out.
  live: LiveSync;
}
```

`src/app/main.tsx`, строки 10–13, замените на:

```tsx
import { createTimerScheduler } from '@/adapters/browser-page/scheduler';
import { createPageVisibility } from '@/adapters/browser-page/visibility';
import { createCookieNoticeStore } from '@/adapters/browser-storage/cookie-notice-store';
import { notConnectedFeedbackSender } from '@/adapters/feedback/not-connected-sender';
import { createSessionCheck } from '@/adapters/http/session-check';
import { createEventSourceFeed } from '@/adapters/live/event-source-feed';
import { createFeedbackSubmitter } from '@/application/feedback';
import { createLiveSync } from '@/application/live-sync';
import { routes } from '@/presentation/routes';
```

и объявление `services` (строки 32–37 исходного файла) на:

```tsx
const services: PresentationServices = {
  cookieNotice: createCookieNoticeStore(browserLocalStorage()),
  feedback: createFeedbackSubmitter(notConnectedFeedbackSender, () =>
    crypto.randomUUID(),
  ),
  // /app starts the stream after sign-in. No command client exists yet, so no
  // ConfirmationPending attempt waits for resolution.
  live: createLiveSync({
    feed: createEventSourceFeed((url) => new EventSource(url)),
    scheduler: createTimerScheduler(window),
    visibility: createPageVisibility(document),
    sessionCheck: createSessionCheck((path, init) => fetch(path, init)),
    resolvePending: () => Promise.resolve(),
  }),
};
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `npm run typecheck && npm run lint && npm run arch && npm run build` Expected: PASS — tsc и ESLint без ошибок; `no dependency violations found`; Vite печатает `✓ built in …` (предупреждения `MODULE_LEVEL_DIRECTIVE` из `@ark-ui/react` были и раньше).

- [ ] **Step 5: Document the behaviour**

В `README.md` допишите в конец пункта `` - `presentation` — … ``:

```markdown
 Хуки `useLiveState` и `useReadModel` из `live` читают состояние live-sync и модели чтения через `useSyncExternalStore`.
```

В пункте `` - `app/main.tsx` — … `` замените «собирает адаптеры с `crypto.randomUUID()` и монтирует провайдеры и маршрутизатор» на:

```markdown
собирает адаптеры с `crypto.randomUUID()`, `EventSource`, `fetch`, таймерами и видимостью документа и монтирует провайдеры и маршрутизатор
```

В конец подраздела «Обновления в реальном времени» добавьте:

```markdown
Live-sync передаётся экранам через `ServicesProvider` незапущенным, поэтому публичные страницы поток не открывают. Экраны `/app` после входа вызовут `live.start()`, а при выходе — `live.stop()`. Их модели чтения создаются в `app` и подключаются к сценарию через `live.track(model)`. Пока API-клиента нет, `resolvePending` ничего не проверяет: ожидающих попыток ещё не бывает.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Live-sync доступен экранам через `ServicesProvider` и хуки `useLiveState` и `useReadModel`. Поток не запускается, пока нет экранов `/app`.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add src/presentation/live/use-live.ts src/presentation/services.tsx src/app/main.tsx README.md CHANGELOG.md
git commit -m "feat(app): expose live sync to presentation"
```

---

### Task 7: Nginx `location = /api/live`

**Files:**
- Modify: `nginx.conf` (объединённый файл: вставка после блока `location = /api/feedback`, строки 45–51)
- Modify: `tests/nginx/gateway.mjs` (поля записи после `csrf: …`; условие `/api/drop`; маршрут `/api/live` перед `const status = …`)
- Modify: `tests/nginx/check.mjs` (импорт `readFileSync`; функция `stream` перед `function json`; проверки после `'keeps the general body limit for other API routes'`)
- Modify: `README.md` (подраздел «Прокси API» из main)
- Modify: `CHANGELOG.md` (первый пункт под `## Не выпущено`)
- Test: `npm run test:nginx`

**Interfaces:**
- Consumes: контракт `GET /api/live` (заголовки ответа, `retry: 3000`, пинги 15 с) из плана gateway; серверные `$gateway`, `@api_unavailable`, `@api_timeout` объединённого `nginx.conf`.
- Produces: `location = /api/live`; маршруты заглушки `/api/live` и `/api/live?drop`; поля `httpVersion` и `connection` в `/api/__requests`.

Nginx 1.30.5 и без отдельного блока ходит в upstream по HTTP/1.1, не шлёт `Connection` и сразу передаёт небольшие куски ответа (проверено в образе из `Dockerfile`). Поэтому чёрный ящик не отличает конфигурацию с блоком от конфигурации без него, и проверка дополнительно сверяет блок в `nginx.conf` с контрактом. Таймаут чтения 60 с не проверяется вживую: это заняло бы больше минуты (как и `504`, который уже не проверяется из-за 30 с). Собственный `proxy_set_header` в блоке отменяет наследование серверных заголовков, поэтому `Host`, `X-Forwarded-For` и `X-Forwarded-Proto` повторены; проверка заголовков браузера ловит именно эту ошибку.

- [ ] **Step 1: Write the failing test**

В `tests/nginx/gateway.mjs` в объект записи запроса после `csrf: request.headers['x-csrf-token'] ?? null,` добавьте:

```js
      httpVersion: request.httpVersion,
      connection: request.headers.connection ?? null,
```

замените `if (url.startsWith('/api/drop')) {` на:

```js
    if (url.startsWith('/api/drop') || url === '/api/live?drop') {
```

и перед строкой `const status = /^\/api\/respond\/(\d{3})$/.exec(url);` вставьте:

```js
    // No X-Accel-Buffering here: the check proves Nginx's own settings.
    if (url === '/api/live') {
      response.writeHead(200, {
        'Content-Type': 'text/event-stream; charset=utf-8',
        'Cache-Control': 'no-store',
      });
      response.write(
        'retry: 3000\n\nevent: upstream\ndata: {"available":true}\n\n',
      );
      setTimeout(() => {
        response.end('event: upstream\ndata: {"available":false}\n\n');
      }, 1500);
      return;
    }
```

В `tests/nginx/check.mjs` после `import assert from 'node:assert/strict';` добавьте:

```js
import { readFileSync } from 'node:fs';
```

перед `function json(response) {` вставьте:

```js
// Reads a response to the end and notes when each chunk arrived.
function stream(path, headers = {}) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const request = http.request(
      { host: '127.0.0.1', port, method: 'GET', path, headers },
      (response) => {
        const chunks = [];
        response.on('data', (chunk) => {
          chunks.push({ at: Date.now() - started, text: chunk.toString() });
        });
        response.on('end', () => {
          resolve({
            status: response.statusCode,
            headers: response.headers,
            chunks,
          });
        });
      },
    );
    request.on('error', reject);
    request.end();
  });
}
```

и после проверки `'keeps the general body limit for other API routes'` (перед закрывающей `};` объекта `checks`) добавьте:

```js
  // Nginx 1.30 already streams small chunks and speaks HTTP/1.1 upstream by
  // default, so the contract's settings are checked in the file as well.
  'configures /api/live for a long-lived stream': () => {
    const config = readFileSync(
      new URL('../../nginx.conf', import.meta.url),
      'utf8',
    );
    const block = /location = \/api\/live \{([^}]*)\}/.exec(config)?.[1] ?? '';
    for (const directive of [
      'proxy_http_version 1.1;',
      'proxy_set_header Connection "";',
      'proxy_buffering off;',
      'proxy_cache off;',
      'gzip off;',
      'proxy_read_timeout 60s;',
    ]) {
      assert.ok(block.includes(directive), `missing ${directive}`);
    }
  },

  'passes /api/live over HTTP/1.1 with the browser headers': async () => {
    const headers = {
      Host: 'umir.example.test:8443',
      Cookie: 'umir_operator_session=s',
    };
    assert.equal((await stream('/api/live', headers)).status, 200);
    const last = (await recorded()).filter((r) => r.url === '/api/live').at(-1);
    assert.deepEqual(
      [last.httpVersion, last.connection, last.host, last.cookie],
      ['1.1', null, headers.Host, headers.Cookie],
    );
  },

  'passes each /api/live event at once': async () => {
    const response = await stream('/api/live', { 'Accept-Encoding': 'gzip' });
    assert.match(response.headers['content-type'], /^text\/event-stream/);
    assert.equal(response.headers['content-encoding'], undefined);
    // The stand-in writes the second event 1.5 s after the first.
    const first = response.chunks.at(0);
    const last = response.chunks.at(-1);
    assert.ok(last.at - first.at >= 1000, 'Nginx held the first event back');
    assert.match(first.text, /^retry: 3000\n\nevent: upstream\n/);
    assert.match(last.text, /"available":false/);
  },

  'answers a lost /api/live answer with the JSON error envelope': async () => {
    const before = (await recorded()).filter(
      (r) => r.url === '/api/live?drop',
    ).length;
    const response = await send('GET', '/api/live?drop');
    assert.equal(response.status, 502);
    assert.deepEqual(json(response), {
      code: 'UpstreamUnavailable',
      message: 'gateway did not respond',
      outcome: 'unavailable',
    });
    const after = (await recorded()).filter(
      (r) => r.url === '/api/live?drop',
    ).length;
    assert.equal(after, before + 1, 'the stream request was retried');
  },
```

Run: `npx prettier --write tests/nginx && npm run lint` Expected: файлы отформатированы, ESLint без ошибок.

- [ ] **Step 2: Run it and see it fail**

Run: `npm run test:nginx`
Expected: FAIL — `not ok - configures /api/live for a long-lived stream` с `missing proxy_http_version 1.1;`, команда завершается с кодом 1. Остальные проверки `/api/live` на Nginx 1.30.5 уже проходят: `/api/` сейчас передаёт поток с заголовками сервера.

- [ ] **Step 3: Minimal implementation**

В `nginx.conf` после закрывающей `}` блока `location = /api/feedback` вставьте:

```nginx
    # Live updates (SSE, factory realtime spec section 8). A proxy_set_header here
    # replaces the server-level list, so Host and the forwarding headers are
    # repeated. Gateway pings every 15 s, well inside the 60 s read timeout.
    location = /api/live {
        error_page 502 = @api_unavailable;
        error_page 504 = @api_timeout;
        proxy_pass $gateway;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_cache off;
        gzip off;
        proxy_read_timeout 60s;
    }
```

- [ ] **Step 4: Run the tests and see them pass**

Run: `npm run test:nginx` Expected: PASS — все строки `ok - …`, включая `configures /api/live for a long-lived stream`, `passes /api/live over HTTP/1.1 with the browser headers`, `passes each /api/live event at once`, `answers a lost /api/live answer with the JSON error envelope`; код выхода 0.

Проверьте ловушку наследования: временно удалите из блока три строки `proxy_set_header Host …`, `X-Forwarded-For …`, `X-Forwarded-Proto …` и снова выполните `npm run test:nginx`. Expected: `not ok - passes /api/live over HTTP/1.1 with the browser headers` с `'gateway:8080'` вместо `'umir.example.test:8443'`. Верните строки и убедитесь, что `npm run test:nginx` снова проходит.

- [ ] **Step 5: Document the behaviour**

В `README.md`, подраздел `### Прокси API`, после пункта о `POST /api/feedback` добавьте:

```markdown
- `GET /api/live` (поток SSE) идёт через отдельный `location = /api/live`: HTTP/1.1 с пустым `Connection`, без буферизации, кеша и gzip, таймаут чтения 60 с. Gateway шлёт `: ping` каждые 15 с, поэтому тихий поток не обрывается. Собственный `proxy_set_header` отменяет наследование серверных заголовков, поэтому `Host`, `X-Forwarded-For` и `X-Forwarded-Proto` в этом блоке повторены. Собственные ошибки Nginx `502` и `504` — тот же JSON с `outcome: unavailable`.
```

В том же подразделе замените предложение «Ответ `504` по таймауту автоматически не проверяется: это заняло бы 30 секунд.» на:

```markdown
Ответ `504` по таймауту и 60-секундный таймаут потока автоматически не проверяются: это заняло бы 30 и 60 секунд. Nginx 1.30 и без отдельного блока ходит в gateway по HTTP/1.1 и сразу передаёт события, поэтому проверка дополнительно сверяет блок `/api/live` в `nginx.conf` с контрактом.
```

В `CHANGELOG.md` первым пунктом под `## Не выпущено`:

```markdown
- Nginx передаёт поток `GET /api/live` через отдельный `location`: HTTP/1.1 с пустым `Connection`, без буферизации, кеша и gzip, таймаут чтения 60 с, с заголовками браузера и JSON-ошибками `502` и `504`. `npm run test:nginx` проверяет поток с заглушкой gateway.
```

Run: `npx prettier --write README.md CHANGELOG.md && npm run format:check` Expected: `All matched files use Prettier code style!`

- [ ] **Step 6: Commit**

```bash
git add nginx.conf tests/nginx/gateway.mjs tests/nginx/check.mjs README.md CHANGELOG.md
git commit -m "feat(nginx): stream /api/live without buffering"
```

---

### Task 8: Полная проверка

**Files:** изменений нет; найденная ошибка исправляется в задаче, которой принадлежит код, отдельным коммитом с тем же scope.

- [ ] **Step 1: Run the repository gate**

Run: `PATH="$HOME/.cache/umir/bun-1.4.3:$PATH" npm run check`
Expected: PASS — Prettier, `tsc --noEmit`, ESLint, dependency-cruiser, `bun test ./tests` (`0 fail`; прибавляется 63 теста: 24 + 23 + 9 + 2 + 2 + 3) и `vite build` проходят.

- [ ] **Step 2: Run the Nginx and container checks from CI**

Run: `npm run test:nginx && docker compose build`
Expected: PASS — все `ok - …`; образ собирается (Dockerfile выполняет `npm run typecheck && npm run build`).

- [ ] **Step 3: Check whitespace and the diff**

Run: `git diff --check && git diff --check origin/main...HEAD && git status --short && git log --oneline -8`
Expected: у обеих `git diff --check` нет вывода; `git status --short` пуст; сверху семь коммитов Task 1–7 (плюс исправления, если были) над коммитом слияния базы.

---

## Зависимости вне плана (проверки, а не задачи)

- **Factory, контракт `1.1.0-draft.1`.** Схемы `LiveChangeEvent` и др. должны совпасть с декодером: `additionalProperties: false`, `seq` — NonNegativeInt64 (`"0"` при пустом журнале), `epoch` 1..64 символа, `MoscowTimestamp`. Если фабрика опубликует иначе, правится `src/adapters/live/decode.ts` и его тест (Task 1).
- **Gateway: `X-Umir-Activity` на `GET /api/operator/me`.** Проверка сессии из Task 5 опирается на то, что фоновый заголовок действует и для этого маршрута: `Peek` без продления и без записи файла аккаунтов. Сейчас `getCurrentOperator` продлевает idle-срок обычной сессии. Если план gateway применит заголовок только к `/api/serving/` и `/api/warehouse/`, каждая проверка после обрыва будет продлевать сессию; это нужно согласовать до слияния.
- **Gateway: `/api/live`, `Peek`, `X-Umir-Activity`.** Проверка цепочки браузер → Nginx → настоящий gateway (первые события `change`, `tick`, `session`, `upstream`; `session-ended` после выхода; фоновый GET не сдвигает `idleExpiresAt`) выполняется в `tests/http` фабрики после того, как фабрика закрепит образ frontend с этим `location`.
- **Warehouse `POST /expiration-runs` (gateway#19).** Нужен для `change` без запросов браузера и для `upstream: false`; frontend получает только сигналы.
- **Playwright, две вкладки.** Поставка в одной вкладке видна в другой, список сроков обновляется сам — после появления экранов `/app` (спецификация, раздел 10).
- **Подключение к экранам.** `live.start()` после входа, `live.stop()` при выходе и `401`, модели чтения и `resolvePending` для попыток `ConfirmationPending` — часть работы над экранами `/app` (спецификация, раздел 11, пункт 4).
