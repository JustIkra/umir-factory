# Gateway Realtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gateway ведёт журнал изменений в памяти, отдаёт поток SSE `GET /api/live` с сигналами для экранов `/app`, пропускает фоновые запросы без продления сессии и сам запускает проверку сроков на складе.

**Architecture:**
- **`internal/journal`** — кольцевой буфер записей. Итог записи определяется только по HTTP-статусу; запись будит подписчиков.
- **`internal/httpapi`** — записывает допущенные изменяющие запросы.
- **`internal/operator`** — получает `Peek` и наблюдателя сессий.
- **`internal/operatorhttp`** — обрабатывает заголовок `X-Umir-Activity: background` и guard потока.
- **`internal/live`** — обслуживает поток.
- **`internal/expiration`** — запускает `POST /expiration-runs` склада.
- **`cmd/server`** — собирает всё это, закрывает потоки при остановке и владеет горутиной запусков.

**Tech Stack:**
- Go 1.26.9, только стандартная библиотека (`net/http`, `httputil.ReverseProxy`, `http.ResponseController`).
- golangci-lint 2.14.0 и go-arch-lint 1.19.0 через `make check`, тесты `go test -race`.

**Spec:** docs/superpowers/specs/2026-10-12-realtime-sse-design.md (umir-factory)

**Base:** `JustIkra/umir-gateway` `origin/main` `d3a00d8` (Merge PR #16). Код каждой задачи собран и проверен последовательными коммитами на копии этой базы: после каждой задачи `make check` проходил целиком.

Открытый PR #17 (`fix/operator-contract-gaps`) правит те же файлы:
- `README.md`, `CHANGELOG.md`;
- `cmd/server/auth_test.go`;
- `internal/operator/profile.go`, `internal/operator/service.go`;
- `internal/operatorhttp/guard.go`, `internal/operatorhttp/session.go`.

Если #17 слит раньше, начните с актуального `main` и переносите диффы этих файлов вручную. Изменения плана в этих файлах точечные: `defer s.unlock()` вместо `defer s.mu.Unlock()`, вызовы `extendedLocked` и `endedLocked`, ветки `background(r)`.

Issue #18 (выделить `umir-auth`) позже перенесёт `internal/operator`. Тогда `SessionObserver` и `Peek` переходят вместе с сервисом.

## Global Constraints

**`GET /api/live`** (только `GET`, точный путь, без upstream):
- **Ответ `200`** с заголовками:
  - `Content-Type: text/event-stream; charset=utf-8`;
  - `Cache-Control: no-store`;
  - `X-Accel-Buffering: no`.
- **Кадры:**
  - первый кадр — `retry: 3000\n\n`;
  - затем строго по порядку `change`, `tick`, `session`, `upstream`;
  - формат события — `event: <name>\ndata: <JSON в одну строку>\n\n`;
  - пинг — `: ping\n\n` каждые 15 с.
- **События:**
  - `change` — `{"epoch": string, "seq": DecimalString}`. Последний номер журнала. Идёт после записей с итогом `success`, `pending` или `unknown`; серия записей даёт одно событие.
  - `tick` — `{"serverTime"}`. При подключении и в начале каждой минуты часов gateway.
  - `session` — `{"idleExpiresAt", "absoluteExpiresAt"}`. При подключении и после продления сессии пользовательским запросом. У запомненной сессии оба поля равны абсолютному сроку.
  - `session-ended` — `{"reason": "signed_out" | "revoked" | "expired"}`, затем поток закрывается.
  - `upstream` — `{"available": boolean}`. `false` после 3 неудачных запусков проверки сроков подряд, `true` после первого удачного. Начальное значение `true`.
- **Время:** RFC 3339 со смещением `+03:00`, как в `internal/operatorhttp/respond.go`.
- **Отказы до начала потока** (общий конверт problem JSON):
  - `401 Unauthenticated` — нет сессии; cookie первого шага не подходит;
  - `403 InvalidOrigin` — `Sec-Fetch-Site` есть и не `same-origin`; в OpenAPI это ответ `CsrfRejected`;
  - `405 MethodNotAllowed` с `Allow: GET`;
  - `429 TooManyStreams` — больше 8 потоков на сессию или 64 на процесс;
  - `503 AuthUnavailable` — режим восстановления.
- **Поведение потока:**
  - подключение, пинги и такты проверяют сессию через `Peek`, без продления и без записи файла;
  - каждая запись клиенту ограничена 5 с через `http.ResponseController`; клиент, пропустивший срок, отключается;
  - очередь клиента хранит одно, последнее, состояние;
  - поток не проходит через `withDeadline` (`UPSTREAM_TIMEOUT`);
  - при `Shutdown` потоки закрываются через `RegisterOnShutdown`;
  - срок сессии отслеживается таймером на `min(idleExpiresAt, absoluteExpiresAt)` и подтверждается через `Peek`.

**`X-Umir-Activity: background`:**
- Смысл имеет только одно точное значение `background`; иначе заголовок игнорируется.
- Запрос проходит те же проверки, что без заголовка: сессию, а у изменяющих методов ещё `Origin` и CSRF. Сессия проверяется через `Peek`, без продления и записи файла.
- Действует на бизнес-маршруты и на `GET /api/operator/me`.
- В upstream заголовок не уходит.

**Журнал** (`JOURNAL_CAPACITY`, по умолчанию 1000, от 1 до 100000):
- **Что попадает:**
  - изменяющие методы под `/api/serving/` и `/api/warehouse/` после guard;
  - `PATCH /api/operator/me/display-name`;
  - запуски проверки сроков.
- **Поля:** `Seq`, `At`, `Source` (`request` | `expiration` | `external`), `Method`, `Path` без query, `Status` (0 при транспортной ошибке), `Outcome`, `Count`. Тела, идентификаторы сессий, IP и заголовки не хранятся.
- **Итог по статусу:**

| Статус | `Outcome` | Будит подписчиков |
| --- | --- | --- |
| 2xx, кроме 202 | `success` | да |
| 202 | `pending` | да |
| 4xx | `rejected` | нет |
| остальное, включая 0 | `unknown` | да |

- **`epoch`** берётся из генератора ID при запуске: 1–64 байта UTF-8.

**Проверка сроков** (`EXPIRATION_RUN_INTERVAL`, по умолчанию `1s`):
- `POST {WAREHOUSE_URL}/expiration-runs` без тела, `Accept: application/json`, бюджет `UPSTREAM_TIMEOUT`. Запуски не накладываются.
- **Записи по итогу запуска:**
  - непустой `expired` — запись `expiration` с `Count`;
  - версия выросла или изменилась без записей `request` с прошлого удачного запуска — запись `external` с итогом `unknown`;
  - 5xx, тайм-аут или неразобранный ответ — одна запись `expiration`/`unknown` на серию неудач;
  - отказ соединения и 4xx — записи нет.
- Браузер получает `404 NotFound` на `/api/warehouse/expiration-runs`.

**Правила репозитория:**
- **Код:**
  - новые пакеты объявлены в `.go-arch-lint.yml`;
  - без новых зависимостей;
  - golangci `errcheck` с `check-blank`, поэтому никаких `_ = err`;
  - `bodyclose`: тело ответа закрывается в той же функции;
  - без `//nolint`.
- **Время, ID и горутины:**
  - время и ID внедряются;
  - у каждой горутины есть владелец, отмена и ожидание.
- **Процесс:**
  - Conventional Commits без `Co-Authored-By` и generated-by;
  - README и CHANGELOG обновляются вместе с поведением.

## Review Focus

1. **Выход между guard и началом потока.** `GuardLive` проверил сессию, а оператор вышел до того, как поток подписался на события сессии. Ожидание: поток не открывается и не живёт до старого срока. Тест: Task 6, `TestASessionEndedBeforeTheWatchIsRefused` — после подписки `Serve` повторяет `Peek` и отвечает `401`.
2. **Склад отвечает 503 или не отвечает каждую секунду.** Ожидание: экраны не перечитывают данные раз в секунду, а после трёх неудач видят `upstream: false`. Тест: Task 7, `TestFailingRunsSignalOnceAndMarkTheWarehouseUnavailable` — одна запись `unknown` на серию неудач.
3. **Клиент перестал читать поток.** Ожидание: обработчик и слот потока освобождаются через срок записи, а не висят. Тест: Task 6, `TestAClientThatStopsReadingIsDropped`.
4. **Остановка gateway с открытыми потоками.** `Shutdown` ждёт простаивающих соединений, а поток никогда не простаивает. Ожидание: процесс завершается сразу. Тест: Task 6, `TestShutdownEndsLiveStreamsAtOnce`.
5. **Перезапуск склада.** `stateVersion` падает до 0, а запросов через gateway не было. Ожидание: экраны перечитывают данные. Тест: Task 7, `TestAWarehouseRestartIsAnExternalChange`.

---

## Подготовка окружения

Инструменты закреплены в `.go-quality-versions`. `make tools` ставит их в `.cache/go-quality/bin`, и `make check` берёт их оттуда. Все команды выполняются из корня `umir-gateway`, Go-команды — с `GOWORK=off`, как в `scripts/go-quality.sh`.

```bash
make tools
make check
```

Expected: до начала работы `make check` проходит на базе.

## File Structure

**Create:**
- `internal/journal/journal.go` — журнал: записи, итог по статусу, кольцевой буфер, подписка, `LastRequest`.
- `internal/httpapi/record.go` — обёртка, которая сообщает метод, путь и итоговый статус запроса; 0 без ответа upstream.
- `internal/operator/observer.go` — `SessionObserver`, `EndReason`, `SessionKey`, очередь событий и `unlock`, который сообщает их вне мьютекса.
- `internal/operatorhttp/activity.go` — `ActivityHeader`, `background(r)`, `peekAuthorized`.
- `internal/operatorhttp/live.go` — `GuardLive`: метод, Fetch Metadata, `Peek`.
- `internal/live/clock.go` — внедряемые часы и таймеры.
- `internal/live/upstream.go` — доступность склада с подпиской.
- `internal/live/sessions.go` — `SessionEvents`: события сессий для потоков; реализует `operator.SessionObserver`.
- `internal/live/streams.go` — `Streams`: лимиты, кадры, цикл потока, `Close`.
- `internal/expiration/runner.go` — `Runner`: запуски, записи журнала, доступность склада.
- Тесты: `internal/journal/journal_test.go`, `internal/httpapi/record_test.go`, `internal/operator/observer_test.go`, `internal/operatorhttp/activity_test.go`, `internal/operatorhttp/live_test.go`, `internal/live/streams_test.go`, `internal/expiration/runner_test.go`, `cmd/server/journal_test.go`, `cmd/server/live_test.go`.

**Modify:**
- `internal/config/config.go` — `JournalCapacity`, `ExpirationRunInterval`.
- `internal/httpapi/router.go` — `Routes.Record`, `Routes.Live`, запись изменяющих запросов, скрытие `/api/warehouse/expiration-runs`.
- `internal/httpapi/errors.go` — отметка транспортной ошибки для записи.
- `internal/operator/service.go`, `profile.go`, `sessions.go` — `Peek`, `Options.Observer`, события продления и окончания сессий.
- `internal/operatorhttp/api.go`, `guard.go`, `session.go` — `Peek` в `Sessions`, фоновые запросы, `/me` без продления, удаление заголовка перед upstream.
- `cmd/server/main.go` — `components`, `newComponents`, `newServer`, запуск проверки сроков.
- `cmd/server/operator_test.go`, `cmd/server/auth_test.go` — сборка через `assemble`, новый параметр `newAccounts`.
- `.go-arch-lint.yml` — компоненты `journal`, `live`, `expiration`.
- `README.md`, `CHANGELOG.md`.


### Task 1: Журнал изменений `internal/journal`

**Files:**
- Modify: `.go-arch-lint.yml`
- Create: `internal/journal/journal.go`
- Test: `internal/journal/journal_test.go`

**Interfaces:**
- Consumes: ничего.
- Produces (пакет `journal`):
  - `type Source string` с `SourceRequest = "request"`, `SourceExpiration = "expiration"`, `SourceExternal = "external"`;
  - `type Outcome string` с `OutcomeSuccess`, `OutcomePending`, `OutcomeRejected`, `OutcomeUnknown`;
  - `func OutcomeOf(status int) Outcome`;
  - `type Entry struct { Seq int64; At time.Time; Source Source; Method, Path string; Status int; Outcome Outcome; Count int }`;
  - `func New(capacity int, newID func() string, now func() time.Time) (*Journal, error)`;
  - методы `(*Journal)`: `Append(Entry) int64`, `Last() (epoch string, seq int64)`, `LastRequest() int64`, `Subscribe(ctx context.Context) <-chan struct{}`, `Entries(after int64) []Entry`.

- [ ] **Step 1: Write the failing tests**

Создайте `internal/journal/journal_test.go`:

```go
package journal

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var moscow = time.FixedZone("MSK", 3*60*60)

// newTestJournal starts a journal of capacity entries in epoch "epoch-1" at a fixed Moscow time.
func newTestJournal(t *testing.T, capacity int) *Journal {
	t.Helper()
	at := time.Date(2026, 10, 12, 14, 0, 0, 0, moscow)
	j, err := New(capacity, func() string { return "epoch-1" }, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func request(status int) Entry {
	return Entry{Source: SourceRequest, Method: http.MethodPost, Path: "/api/warehouse/receipts", Status: status, Outcome: OutcomeOf(status)}
}

func TestNewRejectsUnusableSettings(t *testing.T) {
	now := func() time.Time { return time.Time{} }
	for name, epoch := range map[string]string{"empty": "", "too long": strings.Repeat("e", 65), "invalid UTF-8": "\xff"} {
		if _, err := New(1, func() string { return epoch }, now); err == nil {
			t.Errorf("%s epoch accepted", name)
		}
	}
	if _, err := New(0, func() string { return "e" }, now); err == nil {
		t.Error("zero capacity accepted")
	}
	if _, err := New(1, func() string { return strings.Repeat("e", 64) }, now); err != nil {
		t.Errorf("64-byte epoch: %v", err)
	}
}

func TestClassifyFollowsTheStatusTable(t *testing.T) {
	for status, want := range map[int]Outcome{
		0: OutcomeUnknown, 200: OutcomeSuccess, 201: OutcomeSuccess, 202: OutcomePending, 204: OutcomeSuccess, 302: OutcomeUnknown,
		400: OutcomeRejected, 401: OutcomeRejected, 409: OutcomeRejected, 422: OutcomeRejected, 499: OutcomeRejected,
		500: OutcomeUnknown, 502: OutcomeUnknown, 503: OutcomeUnknown, 504: OutcomeUnknown,
	} {
		if got := OutcomeOf(status); got != want {
			t.Errorf("OutcomeOf(%d) = %s, want %s", status, got, want)
		}
	}
}

func TestSeqIsGaplessAndLastStartsAtZero(t *testing.T) {
	j := newTestJournal(t, 10)
	if epoch, seq := j.Last(); epoch != "epoch-1" || seq != 0 {
		t.Fatalf("empty journal Last = %q, %d", epoch, seq)
	}
	for i, status := range []int{201, 409, 0, 202} {
		if seq := j.Append(request(status)); seq != int64(i+1) {
			t.Fatalf("Append #%d = %d", i+1, seq)
		}
	}
	if _, seq := j.Last(); seq != 4 {
		t.Fatalf("Last = %d", seq)
	}
	entries := j.Entries(0)
	if len(entries) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) || !e.At.Equal(time.Date(2026, 10, 12, 14, 0, 0, 0, moscow)) {
			t.Errorf("entry %d = %+v", i, e)
		}
	}
	if got := j.Entries(2); len(got) != 2 || got[0].Seq != 3 || got[0].Outcome != OutcomeUnknown || got[1].Outcome != OutcomePending {
		t.Errorf("Entries(2) = %+v", got)
	}
}

func TestRingBufferKeepsTheNewestEntries(t *testing.T) {
	j := newTestJournal(t, 3)
	for range 5 {
		j.Append(request(http.StatusCreated))
	}
	var seqs []int64
	for _, e := range j.Entries(0) {
		seqs = append(seqs, e.Seq)
	}
	if !slices.Equal(seqs, []int64{3, 4, 5}) {
		t.Fatalf("kept %v", seqs)
	}
	if got := j.Entries(4); len(got) != 1 || got[0].Seq != 5 {
		t.Errorf("Entries(4) = %+v", got)
	}
	if got := j.Entries(5); len(got) != 0 {
		t.Errorf("Entries(5) = %+v", got)
	}
}

func TestRejectedEntriesDoNotWakeSubscribers(t *testing.T) {
	j := newTestJournal(t, 10)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wake := j.Subscribe(ctx)
	j.Append(request(http.StatusConflict))
	select {
	case <-wake:
		t.Fatal("a rejected entry woke the subscriber")
	default:
	}
	for _, status := range []int{http.StatusOK, http.StatusAccepted, 0, http.StatusBadGateway} {
		j.Append(request(status))
		select {
		case <-wake:
		default:
			t.Fatalf("status %d did not wake the subscriber", status)
		}
	}
}

func TestBurstsCoalesceIntoOneSignal(t *testing.T) {
	j := newTestJournal(t, 10)
	wake := j.Subscribe(t.Context())
	for range 3 {
		j.Append(request(http.StatusCreated))
	}
	<-wake
	select {
	case <-wake:
		t.Fatal("three entries left more than one signal")
	default:
	}
}

func TestSubscriptionEndsWithItsContext(t *testing.T) {
	j := newTestJournal(t, 10)
	ctx, cancel := context.WithCancel(t.Context())
	j.Subscribe(ctx)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		j.mu.Lock()
		left := len(j.subscribers)
		j.mu.Unlock()
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled subscriber was not removed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEntriesHoldNoRequestData(t *testing.T) {
	var fields []string
	entry := reflect.TypeFor[Entry]()
	for i := range entry.NumField() {
		fields = append(fields, entry.Field(i).Name)
	}
	if want := []string{"Seq", "At", "Source", "Method", "Path", "Status", "Outcome", "Count"}; !slices.Equal(fields, want) {
		t.Fatalf("Entry fields = %v, want %v: bodies, session IDs, addresses and headers stay out", fields, want)
	}
}

func TestLastRequestFollowsOnlyRequestEntries(t *testing.T) {
	j := newTestJournal(t, 4)
	if got := j.LastRequest(); got != 0 {
		t.Fatalf("empty journal: %d", got)
	}
	j.Append(request(http.StatusOK))
	j.Append(Entry{Source: SourceExpiration, Outcome: OutcomeSuccess, Count: 1})
	j.Append(Entry{Source: SourceExternal, Outcome: OutcomeUnknown})
	if got := j.LastRequest(); got != 1 {
		t.Fatalf("after expiration and external entries: %d, want 1", got)
	}
	j.Append(request(http.StatusConflict))
	if got := j.LastRequest(); got != 4 {
		t.Fatalf("a rejected request is still a request: %d, want 4", got)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/journal`
Expected: FAIL, сборка: `undefined: Journal`, `undefined: New`, `undefined: Entry`.

- [ ] **Step 3: Implement**

Измените `.go-arch-lint.yml`:

```diff
--- a/.go-arch-lint.yml
+++ b/.go-arch-lint.yml
@@ -19,6 +19,7 @@ components:
   accountfile: {in: "internal/adapters/accountfile/**"}
   operatorhttp: {in: "internal/operatorhttp/**"}
   strictjson: {in: "internal/strictjson/**"}
+  journal: {in: "internal/journal/**"}
 vendors:
   x-text-norm: {in: "golang.org/x/text/unicode/norm"}
   x-crypto-argon2: {in: "golang.org/x/crypto/argon2"}
@@ -55,3 +56,5 @@ deps:
     mayDependOn: [operatorhttp, operator, httpapi, strictjson]
   strictjson:
     mayDependOn: [strictjson]
+  journal:
+    mayDependOn: [journal]
```

Создайте `internal/journal/journal.go`:

```go
// Package journal keeps the gateway's in-memory record of state-changing exchanges. It sees only
// methods, paths and HTTP statuses, never bodies, so it makes no business decisions.
package journal

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

// Source names what produced an entry.
type Source string

const (
	SourceRequest    Source = "request"
	SourceExpiration Source = "expiration"
	// SourceExternal marks a change the gateway noticed without a request through it.
	SourceExternal Source = "external"
)

// Outcome is what the gateway knows about an exchange from its status alone.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"
	OutcomePending  Outcome = "pending"
	OutcomeRejected Outcome = "rejected"
	OutcomeUnknown  Outcome = "unknown"
)

// OutcomeOf maps an HTTP status to its outcome. Status 0 is a transport error; any status outside
// 2xx and 4xx may have changed state, so it is unknown.
func OutcomeOf(status int) Outcome {
	switch {
	case status == http.StatusAccepted:
		return OutcomePending
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return OutcomeSuccess
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		return OutcomeRejected
	}
	return OutcomeUnknown
}

// Entry is one finished exchange. It never holds bodies, session IDs, addresses or headers.
type Entry struct {
	Seq    int64
	At     time.Time
	Source Source
	// Method and Path, without the query, are empty for expiration and external entries.
	Method string
	Path   string
	// Status is 0 when no response arrived.
	Status  int
	Outcome Outcome
	// Count is the number of batches an expiration run wrote off.
	Count int
}

// Journal is a ring buffer of the latest entries of one gateway process. Seq starts at 1 in every
// epoch and grows by one without gaps.
type Journal struct {
	epoch string
	now   func() time.Time

	mu   sync.Mutex
	ring []Entry
	last int64
	// lastRequest is the seq of the newest request entry, 0 before the first.
	lastRequest int64
	subscribers map[chan struct{}]struct{}
}

// New starts an empty journal of capacity entries in a new epoch named by newID.
func New(capacity int, newID func() string, now func() time.Time) (*Journal, error) {
	if capacity <= 0 || newID == nil || now == nil {
		return nil, errors.New("journal: positive capacity, ID source and clock are required")
	}
	epoch := newID()
	if epoch == "" || len(epoch) > 64 || !utf8.ValidString(epoch) {
		return nil, errors.New("journal: epoch must be 1 to 64 bytes of UTF-8")
	}
	return &Journal{epoch: epoch, now: now, ring: make([]Entry, capacity), subscribers: map[chan struct{}]struct{}{}}, nil
}

// Append numbers and stamps e, keeps it in place of the oldest entry when the buffer is full and
// wakes subscribers unless the owner rejected the exchange. It never blocks on a subscriber.
func (j *Journal) Append(e Entry) int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.last++
	e.Seq, e.At = j.last, j.now()
	j.ring[(e.Seq-1)%int64(len(j.ring))] = e
	if e.Source == SourceRequest {
		j.lastRequest = e.Seq
	}
	if e.Outcome != OutcomeRejected {
		for ch := range j.subscribers {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
	return e.Seq
}

// Last returns the epoch and the seq of the newest entry, 0 while the journal is empty.
func (j *Journal) Last() (epoch string, seq int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.epoch, j.last
}

// LastRequest returns the seq of the newest request entry, 0 before the first. The expiration
// scheduler compares it between runs to tell a change it did not see pass through the gateway.
func (j *Journal) LastRequest() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastRequest
}

// Subscribe returns a channel that holds one signal after any number of entries that may have
// changed state. The subscription ends with ctx; the channel is never closed.
func (j *Journal) Subscribe(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	j.mu.Lock()
	j.subscribers[ch] = struct{}{}
	j.mu.Unlock()
	context.AfterFunc(ctx, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		delete(j.subscribers, ch)
	})
	return ch
}

// Entries returns the kept entries with a seq above after, oldest first.
func (j *Journal) Entries(after int64) []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	first := max(after+1, j.last-int64(len(j.ring))+1, 1)
	entries := make([]Entry, 0, max(j.last-first+1, 0))
	for seq := first; seq <= j.last; seq++ {
		entries = append(entries, j.ring[(seq-1)%int64(len(j.ring))])
	}
	return entries
}
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add .go-arch-lint.yml internal/journal/journal.go internal/journal/journal_test.go
git commit -m "feat(journal): keep an in-memory journal of state-changing exchanges"
```


### Task 2: Настройки `JOURNAL_CAPACITY` и `EXPIRATION_RUN_INTERVAL`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: ничего.
- Produces: поля `config.Config.JournalCapacity int` (по умолчанию 1000, от 1 до 100000) и `config.Config.ExpirationRunInterval time.Duration` (по умолчанию `1s`, положительная Go duration). Ошибка называет переменную, но не значение.

- [ ] **Step 1: Write the failing tests**

Измените `internal/config/config_test.go`:

```diff
--- a/internal/config/config_test.go
+++ b/internal/config/config_test.go
@@ -42,6 +42,9 @@ func TestLoadDefaults(t *testing.T) {
 	if cfg.UpstreamTimeout != 15*time.Second {
 		t.Errorf("timeout: %s", cfg.UpstreamTimeout)
 	}
+	if cfg.JournalCapacity != 1000 || cfg.ExpirationRunInterval != time.Second {
+		t.Errorf("journal capacity: %d, expiration run interval: %s", cfg.JournalCapacity, cfg.ExpirationRunInterval)
+	}
 	want := Operator{
 		CookieMode: CookieModeLoopback, PublicOrigin: "http://127.0.0.1:8080", StorePath: "/data/operator/account.json",
 		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 8 * time.Hour,
@@ -59,6 +62,8 @@ func TestLoadReadsEnvironment(t *testing.T) {
 		"WAREHOUSE_URL":                    "https://warehouse.internal:8443/base",
 		"FEEDBACK_URL":                     "http://127.0.0.1:18083",
 		"UPSTREAM_TIMEOUT":                 "2500ms",
+		"JOURNAL_CAPACITY":                 "250",
+		"EXPIRATION_RUN_INTERVAL":          "500ms",
 		"OPERATOR_COOKIE_MODE":             "secure",
 		"OPERATOR_PUBLIC_ORIGIN":           "https://xn----ctbhsteek4c3c.empty-lab.ru",
 		"OPERATOR_BOOTSTRAP_LOGIN":         "operator",
@@ -72,7 +77,7 @@ func TestLoadReadsEnvironment(t *testing.T) {
 	}
 	if cfg.Port != "18081" || cfg.ListenHost != "0.0.0.0" || cfg.ServingURL.Host != "127.0.0.1:18082" ||
 		cfg.WarehouseURL.String() != "https://warehouse.internal:8443/base" || cfg.FeedbackURL.Host != "127.0.0.1:18083" ||
-		cfg.UpstreamTimeout != 2500*time.Millisecond {
+		cfg.UpstreamTimeout != 2500*time.Millisecond || cfg.JournalCapacity != 250 || cfg.ExpirationRunInterval != 500*time.Millisecond {
 		t.Fatalf("config: %+v", cfg)
 	}
 	want := Operator{
@@ -113,6 +118,13 @@ func TestLoadRejectsInvalidSettings(t *testing.T) {
 		{"UPSTREAM_TIMEOUT", "15"},
 		{"UPSTREAM_TIMEOUT", "0s"},
 		{"UPSTREAM_TIMEOUT", "-1s"},
+		{"JOURNAL_CAPACITY", "0"},
+		{"JOURNAL_CAPACITY", "-5"},
+		{"JOURNAL_CAPACITY", "1e3"},
+		{"JOURNAL_CAPACITY", "100001"},
+		{"EXPIRATION_RUN_INTERVAL", "1"},
+		{"EXPIRATION_RUN_INTERVAL", "0s"},
+		{"EXPIRATION_RUN_INTERVAL", "-1s"},
 		{"OPERATOR_COOKIE_MODE", ""},
 		{"OPERATOR_COOKIE_MODE", "insecure"},
 		{"OPERATOR_COOKIE_MODE", "secure"}, // with the loopback http origin
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/config`
Expected: FAIL, сборка: `cfg.JournalCapacity undefined`, `cfg.ExpirationRunInterval undefined`.

- [ ] **Step 3: Implement**

Измените `internal/config/config.go`:

```diff
--- a/internal/config/config.go
+++ b/internal/config/config.go
@@ -22,9 +22,16 @@ type Config struct {
 	// FeedbackURL is the private feedback service; the gateway sends it only POST /api/feedback.
 	FeedbackURL     *url.URL
 	UpstreamTimeout time.Duration
-	Operator        Operator
+	// JournalCapacity is how many latest entries the change journal keeps.
+	JournalCapacity int
+	// ExpirationRunInterval paces the expiration runs the gateway starts on the warehouse.
+	ExpirationRunInterval time.Duration
+	Operator              Operator
 }
 
+// maxJournalCapacity keeps the journal buffer, allocated at start, within a few megabytes.
+const maxJournalCapacity = 100_000
+
 // Cookie modes of the operator session.
 const (
 	CookieModeSecure   = "secure"
@@ -78,6 +85,14 @@ func Load(getenv func(string) string) (Config, error) {
 	if err != nil || cfg.UpstreamTimeout <= 0 {
 		return Config{}, fmt.Errorf("UPSTREAM_TIMEOUT: expected positive duration such as 15s")
 	}
+	cfg.JournalCapacity, err = strconv.Atoi(value("JOURNAL_CAPACITY", "1000"))
+	if err != nil || cfg.JournalCapacity < 1 || cfg.JournalCapacity > maxJournalCapacity {
+		return Config{}, fmt.Errorf("JOURNAL_CAPACITY: expected an integer from 1 to %d", maxJournalCapacity)
+	}
+	cfg.ExpirationRunInterval, err = time.ParseDuration(value("EXPIRATION_RUN_INTERVAL", "1s"))
+	if err != nil || cfg.ExpirationRunInterval <= 0 {
+		return Config{}, fmt.Errorf("EXPIRATION_RUN_INTERVAL: expected positive duration such as 1s")
+	}
 	if cfg.Operator, err = loadOperator(getenv, value); err != nil {
 		return Config{}, err
 	}
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add JOURNAL_CAPACITY and EXPIRATION_RUN_INTERVAL"
```


### Task 3: Запись допущенных изменяющих запросов в журнал

**Files:**
- Modify: `.go-arch-lint.yml`
- Test: `cmd/server/journal_test.go`
- Modify: `cmd/server/main.go`
- Test: `cmd/server/operator_test.go`
- Modify: `internal/httpapi/errors.go`
- Create: `internal/httpapi/record.go`
- Test: `internal/httpapi/record_test.go`
- Modify: `internal/httpapi/router.go`
- Test: `internal/httpapi/router_test.go`

**Interfaces:**
- Consumes: `journal.New`, `journal.Entry`, `journal.OutcomeOf`, `journal.SourceRequest` (Task 1); `config.Config.JournalCapacity` (Task 2).
- Produces:
  - `httpapi.Routes.Record func(method, path string, status int)`, обязательное поле;
  - `/api/warehouse/expiration-runs` и пути под ним отвечают `404 NotFound`;
  - в `cmd/server`: `type components struct { handler http.Handler; journal *journal.Journal }`, `newComponents(cfg config.Config, logger *log.Logger) (components, error)`, `newHandler(cfg, accounts, transport http.RoundTripper, changes *journal.Journal, logger) (http.Handler, error)`;
  - тестовый помощник `assemble(t, values) components`.

Запись стоит внутри `Protect`, поэтому отказ guard не попадает в журнал. Путь записывается до `StripPrefix`, без query. Статус 0 означает, что ответ upstream не получен: `WriteTransportError` отмечает обмен.

- [ ] **Step 1: Write the failing tests**

Создайте `cmd/server/journal_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustIkra/umir-gateway/internal/journal"
)

func TestAdmittedWritesReachTheJournal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	t.Cleanup(upstream.Close)
	values := operatorEnv(t)
	values["SERVING_URL"], values["WAREHOUSE_URL"] = upstream.URL, upstream.URL
	app := assemble(t, values)
	signedIn := withSession(app.handler, signIn(t, app.handler))
	signedIn.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/warehouse/deliveries?draft=1", strings.NewReader("{}")))
	signedIn.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/warehouse/inventory", nil))
	app.handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/serving/orders", nil))

	entries := app.journal.Entries(0)
	if len(entries) != 1 || entries[0].Source != journal.SourceRequest || entries[0].Method != http.MethodPost ||
		entries[0].Path != "/api/warehouse/deliveries" || entries[0].Status != http.StatusCreated || entries[0].Outcome != journal.OutcomeSuccess {
		t.Fatalf("entries: %+v", entries)
	}
}
```

Измените `cmd/server/operator_test.go`:

```diff
--- a/cmd/server/operator_test.go
+++ b/cmd/server/operator_test.go
@@ -75,12 +75,13 @@ func gatewayApp(t *testing.T, servingURL, warehouseURL, timeout string) http.Han
 // file, like a restarted gateway.
 func gatewayWith(t *testing.T, values map[string]string) http.Handler {
 	t.Helper()
-	cfg := loadConfig(t, values)
-	accounts, err := newAccounts(cfg.Operator)
-	if err != nil {
-		t.Fatal(err)
-	}
-	app, err := newHandler(cfg, accounts, log.Default())
+	return assemble(t, values).handler
+}
+
+// assemble builds the gateway's components from settings.
+func assemble(t *testing.T, values map[string]string) components {
+	t.Helper()
+	app, err := newComponents(loadConfig(t, values), log.Default())
 	if err != nil {
 		t.Fatal(err)
 	}
```

Создайте `internal/httpapi/record_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestRouterRecordsAdmittedWritesOnly(t *testing.T) {
	routes, router := newTestRouter(t)
	for _, tc := range []struct{ method, target string }{
		{http.MethodPost, "/api/warehouse/receipts?draft=1"},
		{http.MethodPut, "/api/serving/menu/items/d1"},
		{http.MethodPatch, "/api/serving/stop-list"},
		{http.MethodDelete, "/api/warehouse/items/%D0%B0"},
		{http.MethodPost, "/api/serving/healthz"},
		{http.MethodGet, "/api/serving/menu"},
		{http.MethodHead, "/api/warehouse/healthz"},
		{http.MethodOptions, "/api/serving/menu"},
		{http.MethodPatch, "/api/operator/me/display-name"},
		{http.MethodGet, "/api/operator/me"},
		{http.MethodPost, "/api/operator/session"},
		{http.MethodDelete, "/api/operator/session"},
		{http.MethodPatch, "/api/operator/me/password"},
		{http.MethodPost, "/api/operator/me/display-name"},
		{http.MethodPost, "/api/feedback"},
	} {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.target, nil))
	}
	want := []string{
		"POST /api/warehouse/receipts 418",
		"PUT /api/serving/menu/items/d1 418",
		"PATCH /api/serving/stop-list 418",
		"DELETE /api/warehouse/items/а 418",
		"POST /api/serving/healthz 418",
		"PATCH /api/operator/me/display-name 418",
	}
	if !slices.Equal(routes.recorded, want) {
		t.Fatalf("recorded %q, want %q", routes.recorded, want)
	}
}

// recordThrough sends one POST through record to upstream and returns the reported status.
func recordThrough(t *testing.T, upstream http.HandlerFunc) (int, *httptest.ResponseRecorder) {
	t.Helper()
	var reported []int
	h := record(upstream, func(_, _ string, status int) { reported = append(reported, status) })
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/serving/order-confirmations", nil))
	if len(reported) != 1 {
		t.Fatalf("reported %v", reported)
	}
	return reported[0], w
}

func TestRecordReportsTheFinalStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		upstream http.HandlerFunc
		want     int
	}{
		{"201", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }, http.StatusCreated},
		{"200 by body", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := io.WriteString(w, "{}"); err != nil {
				t.Error(err)
			}
		}, http.StatusOK},
		{"103 then 202", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusEarlyHints)
			w.WriteHeader(http.StatusAccepted)
		}, http.StatusAccepted},
		{"nothing written", func(http.ResponseWriter, *http.Request) {}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, _ := recordThrough(t, tc.upstream); status != tc.want {
				t.Fatalf("reported %d, want %d", status, tc.want)
			}
		})
	}
}

func TestRecordReportsZeroWithoutAnUpstreamResponse(t *testing.T) {
	status, w := recordThrough(t, func(w http.ResponseWriter, r *http.Request) {
		WriteTransportError(w, r, errors.New("connection reset"))
	})
	if status != 0 || w.Code != http.StatusBadGateway {
		t.Fatalf("reported %d for a %d transport error", status, w.Code)
	}
}

func TestRecordReportsWhenTheProxyAborts(t *testing.T) {
	var reported []int
	h := record(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic(http.ErrAbortHandler) // what ReverseProxy does when the upstream body breaks
	}), func(_, _ string, status int) { reported = append(reported, status) })
	func() {
		defer func() {
			recovered := recover()
			if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				t.Errorf("panic = %v", recovered)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/serving/orders", nil))
	}()
	if !slices.Equal(reported, []int{http.StatusOK}) {
		t.Fatalf("reported %v", reported)
	}
}

// deadlineRecorder accepts write deadlines, as a server connection does.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestRecordKeepsResponseControl(t *testing.T) {
	want := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := record(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(want); err != nil {
			t.Errorf("SetWriteDeadline: %v", err)
		}
	}), func(string, string, int) {})
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/serving/orders", nil))
	if !w.deadline.Equal(want) {
		t.Fatalf("deadline = %v", w.deadline)
	}
}

func TestRouterHidesExpirationRunsFromTheBrowser(t *testing.T) {
	for _, target := range []string{
		"/api/warehouse/expiration-runs",
		"/api/warehouse/expiration-runs?force=1",
		"/api/warehouse/expiration-runs/",
		"/api/warehouse/expiration-runs/123",
		"/api/warehouse/expiration%2Druns",
		"/api/warehouse/expiration-runs%2Fx",
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			t.Run(fmt.Sprintf("%s %s", method, target), func(t *testing.T) {
				routes, router := newTestRouter(t)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, target, nil))
				var body Problem
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusNotFound || body.Code != "NotFound" {
					t.Fatalf("%d %s", response.Code, response.Body)
				}
				if routes.warehouse.request != nil || len(routes.guarded) != 0 || len(routes.recorded) != 0 {
					t.Fatalf("reached warehouse %v, guard %v, journal %v", routes.warehouse.request != nil, routes.guarded, routes.recorded)
				}
			})
		}
	}
}
```

Измените `internal/httpapi/router_test.go`:

```diff
--- a/internal/httpapi/router_test.go
+++ b/internal/httpapi/router_test.go
@@ -2,6 +2,7 @@ package httpapi
 
 import (
 	"encoding/json"
+	"fmt"
 	"net/http"
 	"net/http/httptest"
 	"testing"
@@ -18,7 +19,7 @@ func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 // testRoutes guards with a stub that records what it protected and lets it through.
 type testRoutes struct {
 	serving, warehouse, operator, feedback *recordingHandler
-	guarded, feedbackGuarded               []string
+	guarded, feedbackGuarded, recorded     []string
 }
 
 func newTestRouter(t *testing.T) (*testRoutes, http.Handler) {
@@ -41,6 +42,9 @@ func newTestRouter(t *testing.T) (*testRoutes, http.Handler) {
 			})
 		},
 		NewRequestID: func() string { return "req-1" },
+		Record: func(method, path string, status int) {
+			routes.recorded = append(routes.recorded, fmt.Sprintf("%s %s %d", method, path, status))
+		},
 	})
 	if err != nil {
 		t.Fatal(err)
@@ -57,12 +61,14 @@ func TestNewRouterRequiresEveryRoute(t *testing.T) {
 		"request ID":     func(r *Routes) { r.NewRequestID = nil },
 		"feedback":       func(r *Routes) { r.Feedback = nil },
 		"feedback guard": func(r *Routes) { r.GuardFeedback = nil },
+		"recorder":       func(r *Routes) { r.Record = nil },
 	} {
 		t.Run(name, func(t *testing.T) {
 			pass := func(next http.Handler) http.Handler { return next }
 			routes := Routes{
 				Serving: &recordingHandler{}, Warehouse: &recordingHandler{}, Operator: &recordingHandler{},
 				Feedback: &recordingHandler{}, Protect: pass, GuardFeedback: pass, NewRequestID: func() string { return "" },
+				Record: func(string, string, int) {},
 			}
 			drop(&routes)
 			if _, err := NewRouter(routes); err == nil {
@@ -143,12 +149,14 @@ func TestRouterRejectedRequestNeverReachesUpstream(t *testing.T) {
 			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
 		},
 		NewRequestID: func() string { return "req-1" },
+		Record:       func(string, string, int) { t.Error("a rejected request was recorded") },
 	})
 	if err != nil {
 		t.Fatal(err)
 	}
 	response := httptest.NewRecorder()
 	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/serving/menu", nil))
+	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/serving/orders", nil))
 	if response.Code != http.StatusUnauthorized || serving.request != nil {
 		t.Fatalf("rejected request: %d, upstream called: %v", response.Code, serving.request != nil)
 	}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/httpapi ./cmd/server`
Expected: FAIL, сборка: `undefined: record`, `unknown field Record in struct literal of type Routes`.

- [ ] **Step 3: Implement**

Измените `.go-arch-lint.yml`:

```diff
--- a/.go-arch-lint.yml
+++ b/.go-arch-lint.yml
@@ -38,7 +38,7 @@ deps:
   bootstrap:
     mayDependOn: [bootstrap, transport, proxy]
   process:
-    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp]
+    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp, journal]
     canUse: [x-term]
   testsupport:
     mayDependOn: [testsupport]
```

Измените `cmd/server/main.go`:

```diff
--- a/cmd/server/main.go
+++ b/cmd/server/main.go
@@ -20,6 +20,7 @@ import (
 	"github.com/JustIkra/umir-gateway/internal/adapters/httpupstream"
 	"github.com/JustIkra/umir-gateway/internal/config"
 	"github.com/JustIkra/umir-gateway/internal/httpapi"
+	"github.com/JustIkra/umir-gateway/internal/journal"
 	"github.com/JustIkra/umir-gateway/internal/operator"
 	"github.com/JustIkra/umir-gateway/internal/operatorhttp"
 )
@@ -60,11 +61,36 @@ func newAccounts(cfg config.Operator) (*operator.Service, error) {
 	})
 }
 
-// newHandler assembles the gateway: one proxy per upstream behind the operator guard and the router.
-// Each upstream is wrapped so operator credentials never reach it and it cannot set them.
-func newHandler(cfg config.Config, accounts operatorhttp.Accounts, logger *log.Logger) (http.Handler, error) {
+// components are the parts of one gateway process: the HTTP handler and the change journal.
+type components struct {
+	handler http.Handler
+	journal *journal.Journal
+}
+
+// newComponents assembles the gateway from cfg.
+func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
+	accounts, err := newAccounts(cfg.Operator)
+	if err != nil {
+		return components{}, err
+	}
+	changes, err := journal.New(cfg.JournalCapacity, rand.Text, time.Now)
+	if err != nil {
+		return components{}, err
+	}
 	// Zero Proxy field: internal upstreams never go through HTTP_PROXY from the environment.
 	transport := &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}
+	handler, err := newHandler(cfg, accounts, transport, changes, logger)
+	if err != nil {
+		return components{}, err
+	}
+	return components{handler: handler, journal: changes}, nil
+}
+
+// newHandler assembles the gateway: one proxy per upstream behind the operator guard and the router.
+// Each upstream is wrapped so operator credentials never reach it and it cannot set them. Admitted
+// writes go to changes.
+func newHandler(cfg config.Config, accounts operatorhttp.Accounts, transport http.RoundTripper, changes *journal.Journal,
+	logger *log.Logger) (http.Handler, error) {
 	proxy := func(target *url.URL) (http.Handler, error) {
 		proxy, err := httpupstream.NewProxy(target, transport)
 		if err != nil {
@@ -108,6 +134,9 @@ func newHandler(cfg config.Config, accounts operatorhttp.Accounts, logger *log.L
 		GuardFeedback: func(next http.Handler) http.Handler {
 			return withDeadline(api.GuardFeedback(next), cfg.UpstreamTimeout)
 		},
+		Record: func(method, path string, status int) {
+			changes.Append(journal.Entry{Source: journal.SourceRequest, Method: method, Path: path, Status: status, Outcome: journal.OutcomeOf(status)})
+		},
 	})
 }
 
@@ -205,17 +234,13 @@ func main() {
 		}
 		return
 	}
-	accounts, err := newAccounts(cfg.Operator)
-	if err != nil {
-		log.Fatal(err)
-	}
-	app, err := newHandler(cfg, accounts, log.Default())
+	app, err := newComponents(cfg, log.Default())
 	if err != nil {
 		log.Fatal(err)
 	}
 	server := &http.Server{
 		Addr:              listenAddress(cfg),
-		Handler:           app,
+		Handler:           app.handler,
 		ReadHeaderTimeout: 5 * time.Second,
 		IdleTimeout:       60 * time.Second,
 	}
```

Измените `internal/httpapi/errors.go`:

```diff
--- a/internal/httpapi/errors.go
+++ b/internal/httpapi/errors.go
@@ -13,6 +13,7 @@ import (
 // WriteTransportError answers when no upstream response was received.
 // A write request may still have been recorded upstream, so its outcome is unknown.
 func WriteTransportError(w http.ResponseWriter, r *http.Request, err error) {
+	markTransportFailure(r)
 	if deadlineErr := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
 		log.Printf("upstream error write deadline: %v", deadlineErr)
 		return
```

Создайте `internal/httpapi/record.go`:

```go
package httpapi

import (
	"context"
	"net/http"
)

// exchangeKey carries the *exchange of a recorded request, so WriteTransportError can mark it.
type exchangeKey struct{}

// exchange learns whether the answer came from the gateway because no upstream response arrived.
type exchange struct{ failed bool }

// markTransportFailure tells the recording of r that no upstream response arrived.
func markTransportFailure(r *http.Request) {
	if x, ok := r.Context().Value(exchangeKey{}).(*exchange); ok {
		x.failed = true
	}
}

// record reports the method, path without the query and final status of every request through next:
// 0 when no upstream response arrived or nothing was written. It reports even when next panics, as
// the proxy does when an upstream body breaks after the status was sent.
func record(next http.Handler, report func(method, path string, status int)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x := &exchange{}
		writer := &statusWriter{ResponseWriter: w}
		defer func() {
			status := writer.status
			if x.failed {
				status = 0
			}
			report(r.Method, r.URL.Path, status)
		}()
		next.ServeHTTP(writer, r.WithContext(context.WithValue(r.Context(), exchangeKey{}, x)))
	})
}

// statusWriter remembers the first final status written through it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusOK {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the deadlines and flushing of the wrapped writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
```

Измените `internal/httpapi/router.go`:

```diff
--- a/internal/httpapi/router.go
+++ b/internal/httpapi/router.go
@@ -6,6 +6,8 @@ import (
 	"errors"
 	"log"
 	"net/http"
+	"slices"
+	"strings"
 )
 
 // Routes are the handlers behind the gateway's paths. Every field is required: business routes cannot
@@ -23,18 +25,25 @@ type Routes struct {
 	GuardFeedback func(http.Handler) http.Handler
 	// NewRequestID names each request in error responses and logs.
 	NewRequestID func() string
+	// Record learns the status of each state-changing business request that passed Protect and of each
+	// display-name change: 0 when no upstream response arrived.
+	Record func(method, path string, status int)
 }
 
 func NewRouter(routes Routes) (http.Handler, error) {
 	if routes.Serving == nil || routes.Warehouse == nil || routes.Operator == nil || routes.Protect == nil ||
-		routes.Feedback == nil || routes.GuardFeedback == nil || routes.NewRequestID == nil {
-		return nil, errors.New("router requires serving, warehouse, operator, protect, feedback and its guard, and request IDs")
+		routes.Feedback == nil || routes.GuardFeedback == nil || routes.NewRequestID == nil || routes.Record == nil {
+		return nil, errors.New("router requires serving, warehouse, operator, protect, feedback and its guard, request IDs and a recorder")
 	}
 	mux := http.NewServeMux()
 	mux.HandleFunc("/healthz", health)
 	mux.Handle("/api/operator/", routes.Operator)
-	mux.Handle("/api/serving/", business("/api/serving", routes.Serving, routes.Protect))
-	mux.Handle("/api/warehouse/", business("/api/warehouse", routes.Warehouse, routes.Protect))
+	// The display name is shown on every screen, so its change is recorded like a business write.
+	mux.Handle("/api/operator/me/display-name", recordMethods(routes.Operator, routes.Record, http.MethodPatch))
+	mux.Handle("/api/serving/", business("/api/serving", routes.Serving, routes.Protect, routes.Record))
+	// Only the gateway's expiration scheduler starts runs; the browser never reaches the route.
+	mux.Handle("/api/warehouse/", hide("/api/warehouse/expiration-runs",
+		business("/api/warehouse", routes.Warehouse, routes.Protect, routes.Record)))
 	// Only the exact path: other paths of the feedback service are never public.
 	mux.Handle("/api/feedback", routes.GuardFeedback(toFeedback(routes.Feedback)))
 	return WithRequestID(mux, routes.NewRequestID), nil
@@ -53,10 +62,10 @@ func health(w http.ResponseWriter, r *http.Request) {
 }
 
 // business leaves only the exact GET/HEAD health check of an upstream public. Every other request
-// under prefix passes protect before it can reach the upstream.
-func business(prefix string, upstream http.Handler, protect func(http.Handler) http.Handler) http.Handler {
+// under prefix passes protect before it can reach the upstream; admitted writes are recorded.
+func business(prefix string, upstream http.Handler, protect func(http.Handler) http.Handler, report func(method, path string, status int)) http.Handler {
 	public := http.StripPrefix(prefix, upstream)
-	guarded := protect(public)
+	guarded := protect(recordMethods(public, report, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete))
 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
 		if r.URL.Path == prefix+"/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
 			public.ServeHTTP(w, r)
@@ -66,6 +75,29 @@ func business(prefix string, upstream http.Handler, protect func(http.Handler) h
 	})
 }
 
+// recordMethods records the requests with one of methods and passes the others through unrecorded.
+func recordMethods(next http.Handler, report func(method, path string, status int), methods ...string) http.Handler {
+	recorded := record(next, report)
+	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
+		if slices.Contains(methods, r.Method) {
+			recorded.ServeHTTP(w, r)
+			return
+		}
+		next.ServeHTTP(w, r)
+	})
+}
+
+// hide answers 404 for path and every path under it, however the client escaped them.
+func hide(path string, next http.Handler) http.Handler {
+	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
+		if r.URL.Path == path || strings.HasPrefix(r.URL.Path, path+"/") {
+			WriteProblem(w, r, http.StatusNotFound, Problem{Code: "NotFound", Message: "unknown route"})
+			return
+		}
+		next.ServeHTTP(w, r)
+	})
+}
+
 // toFeedback gives the feedback service its own path for the public form, without the caller's query.
 func toFeedback(service http.Handler) http.Handler {
 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add .go-arch-lint.yml cmd/server/journal_test.go cmd/server/main.go cmd/server/operator_test.go internal/httpapi/errors.go internal/httpapi/record.go internal/httpapi/record_test.go internal/httpapi/router.go internal/httpapi/router_test.go
git commit -m "feat(httpapi): record admitted state-changing requests in the journal"
```


### Task 4: `Peek` и наблюдатель сессий в `internal/operator`

**Files:**
- Create: `internal/operator/observer.go`
- Test: `internal/operator/observer_test.go`
- Modify: `internal/operator/profile.go`
- Modify: `internal/operator/service.go`
- Modify: `internal/operator/sessions.go`

**Interfaces:**
- Consumes: ничего нового.
- Produces (пакет `operator`):
  - `func (s *Service) Peek(id string) (Profile, Session, error)` — проверка без продления и без записи файла;
  - `type EndReason string` с `EndSignedOut`, `EndRevoked`, `EndExpired`;
  - `type SessionObserver interface { SessionExtended(key string, idleExpiresAt, absoluteExpiresAt time.Time); SessionEnded(key string, reason EndReason) }`;
  - поле `Options.Observer SessionObserver`, необязательное;
  - `func SessionKey(token string) string`.

**Когда сервис сообщает наблюдателю:**
- `SessionExtended` — после записи продления обычной сессии: `Authenticate`, `Authorize`, `Current`, `UpdateDisplayName`.
- `SessionEnded` с `signed_out` — после `Logout` и после входа, заменившего прежнюю сессию.
- `SessionEnded` с `revoked` — после смены пароля (все живые сессии), после паузы на неверные пароли и после вытеснения сверх лимита.

**Как это устроено:**
- События копятся под мьютексом, а `unlock` сообщает их уже после его освобождения.
- При неудачной записи событий нет.
- Истечение срока сервис не сообщает: его ничто не записывает, поэтому поток отслеживает срок сам.

- [ ] **Step 1: Write the failing tests**

Создайте `internal/operator/observer_test.go`:

```go
package operator

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// recordingObserver keeps every call as text; onCall, when set, runs inside the call.
type recordingObserver struct {
	mu     sync.Mutex
	calls  []string
	onCall func()
}

func (o *recordingObserver) SessionExtended(key string, idle, absolute time.Time) {
	o.add(fmt.Sprintf("extended %s idle %s absolute %s", key, idle.Format(time.TimeOnly), absolute.Format(time.TimeOnly)))
}

func (o *recordingObserver) SessionEnded(key string, reason EndReason) {
	o.add(fmt.Sprintf("ended %s %s", key, reason))
}

func (o *recordingObserver) add(call string) {
	o.mu.Lock()
	o.calls = append(o.calls, call)
	hook := o.onCall
	o.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// take returns the calls so far and forgets them.
func (o *recordingObserver) take() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	calls := o.calls
	o.calls = nil
	return calls
}

func observed(t *testing.T, change func(*Options)) (fixture, *recordingObserver) {
	t.Helper()
	observer := &recordingObserver{}
	f := newFixture(t, func(o *Options) {
		o.Observer = observer
		if change != nil {
			change(o)
		}
	})
	return f, observer
}

func TestPeekNeitherExtendsNorStores(t *testing.T) {
	f, observer := observed(t, nil)
	login := f.clock.Now()
	session := f.login(t)
	observer.take()
	stored := len(f.store.states())
	f.clock.Advance(10 * time.Minute)
	profile, peeked, err := f.svc.Peek(session.ID)
	if err != nil || profile.Login != "operator" || peeked.CSRFToken != session.CSRFToken {
		t.Fatalf("Peek = %+v, %+v, %v", profile, peeked, err)
	}
	wantDeadlines(t, peeked, login.Add(30*time.Minute), login.Add(8*time.Hour))
	if got := len(f.store.states()); got != stored {
		t.Errorf("Peek stored %d states", got-stored)
	}
	if calls := observer.take(); len(calls) != 0 {
		t.Errorf("Peek notified %v", calls)
	}
	// The idle deadline Peek left alone still ends the session.
	f.clock.Advance(20 * time.Minute)
	if _, _, err := f.svc.Peek(session.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Peek after 30 idle minutes = %v", err)
	}
}

func TestPeekRejectsLikeAuthenticate(t *testing.T) {
	f := newFixture(t, nil)
	session := f.login(t)
	for _, id := range []string{"", "token-999", session.CSRFToken} {
		if _, _, err := f.svc.Peek(id); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Peek(%q) = %v", id, err)
		}
	}
	f.store.setErr(errors.New("rename outcome unknown"))
	if _, err := f.svc.UpdateDisplayName(session.ID, "Смена"); !errors.Is(err, ErrUpdateOutcomeUnknown) {
		t.Fatalf("UpdateDisplayName = %v", err)
	}
	if _, _, err := f.svc.Peek(session.ID); !errors.Is(err, ErrAuthUnavailable) {
		t.Errorf("Peek during recovery = %v", err)
	}
}

func TestObserverHearsExtensionsByUserRequests(t *testing.T) {
	f, observer := observed(t, nil)
	login := f.clock.Now()
	session := f.login(t)
	key := SessionKey(session.ID)
	observer.take()
	steps := []struct {
		name string
		call func() error
	}{
		{"Authenticate", func() error { _, _, err := f.svc.Authenticate(session.ID); return err }},
		{"Authorize", func() error { _, _, err := f.svc.Authorize(session.ID, session.CSRFToken); return err }},
		{"Current", func() error { _, _, err := f.svc.Current(session.ID); return err }},
		{"UpdateDisplayName", func() error { _, err := f.svc.UpdateDisplayName(session.ID, "Смена"); return err }},
	}
	for _, step := range steps {
		f.clock.Advance(time.Minute)
		if err := step.call(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		want := fmt.Sprintf("extended %s idle %s absolute %s", key,
			f.clock.Now().Add(30*time.Minute).Format(time.TimeOnly), login.Add(8*time.Hour).Format(time.TimeOnly))
		if calls := observer.take(); !slices.Equal(calls, []string{want}) {
			t.Errorf("%s notified %v, want %q", step.name, calls, want)
		}
	}
	// A request at the instant already stored moves nothing and reports nothing.
	if _, _, err := f.svc.Authenticate(session.ID); err != nil {
		t.Fatal(err)
	}
	if calls := observer.take(); len(calls) != 0 {
		t.Errorf("unchanged deadline notified %v", calls)
	}
}

func TestObserverIgnoresRememberedSessions(t *testing.T) {
	f, observer := observed(t, nil)
	_, session, err := f.svc.Login(t.Context(), "operator", testPassword, "", true)
	if err != nil {
		t.Fatal(err)
	}
	observer.take()
	f.clock.Advance(time.Minute)
	if _, _, err := f.svc.Authenticate(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Current(session.ID); err != nil {
		t.Fatal(err)
	}
	if calls := observer.take(); len(calls) != 0 {
		t.Errorf("remembered session notified %v", calls)
	}
}

func TestObserverHearsEndedSessions(t *testing.T) {
	t.Run("sign-out", func(t *testing.T) {
		f, observer := observed(t, nil)
		session := f.login(t)
		observer.take()
		if err := f.svc.Logout(session.ID); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.Logout(session.ID); err != nil { // nothing left to end
			t.Fatal(err)
		}
		if calls := observer.take(); !slices.Equal(calls, []string{"ended " + SessionKey(session.ID) + " signed_out"}) {
			t.Errorf("calls = %v", calls)
		}
	})
	t.Run("sign-in that replaces the session", func(t *testing.T) {
		f, observer := observed(t, nil)
		first := f.login(t)
		observer.take()
		if _, _, err := f.svc.Login(t.Context(), "operator", testPassword, first.ID, false); err != nil {
			t.Fatal(err)
		}
		if calls := observer.take(); !slices.Equal(calls, []string{"ended " + SessionKey(first.ID) + " signed_out"}) {
			t.Errorf("calls = %v", calls)
		}
	})
	t.Run("eviction at the bound", func(t *testing.T) {
		f, observer := observed(t, func(o *Options) { o.MaxSessions = 2 })
		oldest := f.login(t)
		f.clock.Advance(time.Second)
		f.login(t)
		observer.take()
		f.clock.Advance(time.Second)
		f.login(t)
		if calls := observer.take(); !slices.Equal(calls, []string{"ended " + SessionKey(oldest.ID) + " revoked"}) {
			t.Errorf("calls = %v", calls)
		}
	})
	t.Run("password change", func(t *testing.T) {
		f, observer := observed(t, nil)
		first := f.login(t)
		second := f.login(t)
		observer.take()
		if err := f.svc.ChangePassword(t.Context(), first.ID, testPassword, "new password value"); err != nil {
			t.Fatal(err)
		}
		calls := observer.take()
		slices.Sort(calls)
		want := []string{"ended " + SessionKey(first.ID) + " revoked", "ended " + SessionKey(second.ID) + " revoked"}
		slices.Sort(want)
		if !slices.Equal(calls, want) {
			t.Errorf("calls = %v, want %v", calls, want)
		}
	})
	t.Run("fifth wrong current password", func(t *testing.T) {
		f, observer := observed(t, nil)
		session := f.login(t)
		other := f.login(t)
		observer.take()
		for range DefaultFailureLimit {
			if err := f.svc.ChangePassword(t.Context(), session.ID, "wrong password value", "new password value"); err == nil {
				t.Fatal("wrong current password accepted")
			}
		}
		if calls := observer.take(); !slices.Equal(calls, []string{"ended " + SessionKey(session.ID) + " revoked"}) {
			t.Errorf("calls = %v; %s stays signed in", calls, SessionKey(other.ID))
		}
	})
}

func TestObserverHearsNothingWhenTheCommitFails(t *testing.T) {
	f, observer := observed(t, nil)
	session := f.login(t)
	observer.take()
	f.clock.Advance(time.Minute)
	f.store.setErr(fmt.Errorf("disk full: %w", ErrStorageUnavailable))
	if _, _, err := f.svc.Authenticate(session.ID); !errors.Is(err, ErrSessionStorageUnavailable) {
		t.Fatalf("Authenticate = %v", err)
	}
	if err := f.svc.Logout(session.ID); !errors.Is(err, ErrSessionStorageUnavailable) {
		t.Fatalf("Logout = %v", err)
	}
	if calls := observer.take(); len(calls) != 0 {
		t.Errorf("failed commits notified %v", calls)
	}
}

func TestObserverRunsOutsideTheServiceMutex(t *testing.T) {
	f, observer := observed(t, nil)
	session := f.login(t)
	peeked := make(chan error, 1)
	observer.mu.Lock()
	observer.onCall = func() {
		_, _, err := f.svc.Peek(session.ID)
		peeked <- err
	}
	observer.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- f.svc.Logout(session.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer called under the service mutex: Logout deadlocked")
	}
	if err := <-peeked; !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("observer saw the session before the commit: %v", err)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/operator`
Expected: FAIL, сборка: `undefined: EndReason`, `o.Observer undefined`, `f.svc.Peek undefined`.

- [ ] **Step 3: Implement**

Создайте `internal/operator/observer.go`:

```go
package operator

import "time"

// EndReason says why a session ended before its deadline.
type EndReason string

const (
	// EndSignedOut is a sign-out, or a new sign-in that replaced the session.
	EndSignedOut EndReason = "signed_out"
	// EndRevoked is a password change, the revocation after wrong current passwords or an eviction
	// at the session bound.
	EndRevoked EndReason = "revoked"
	// EndExpired is a deadline that passed. The service never reports it; watchers find it themselves.
	EndExpired EndReason = "expired"
)

// SessionObserver learns about committed session changes. The service calls it after the commit and
// outside its mutex, so an observer may call the service again; it must not block. key is SessionKey of
// the session's token. Expiry is not reported: nothing commits it, and a watcher knows the deadline.
type SessionObserver interface {
	// SessionExtended reports the deadlines of an ordinary session that a user request extended.
	SessionExtended(key string, idleExpiresAt, absoluteExpiresAt time.Time)
	// SessionEnded reports a session that can no longer be used.
	SessionEnded(key string, reason EndReason)
}

// SessionKey names the session of token in observer calls without the token itself.
func SessionKey(token string) string { return sessionDigest(token) }

// sessionEvent is a committed change waiting for the observer; ended is empty for an extension.
type sessionEvent struct {
	key            string
	ended          EndReason
	idle, absolute time.Time
}

// extendedLocked queues the new deadlines of id after a commit. A remembered session never moves.
func (s *Service) extendedLocked(id string, st *session) {
	if st.rememberMe {
		return
	}
	view := s.view(id, st)
	s.events = append(s.events, sessionEvent{key: sessionDigest(id), idle: view.IdleExpiresAt, absolute: view.AbsoluteExpiresAt})
}

// endedLocked queues the end of the session with digest after a commit.
func (s *Service) endedLocked(digest string, reason EndReason) {
	s.events = append(s.events, sessionEvent{key: digest, ended: reason})
}

// unlock releases s.mu and then reports the changes committed under it, so the observer never runs
// under the mutex.
func (s *Service) unlock() {
	events := s.events
	s.events = nil
	s.mu.Unlock()
	if s.opts.Observer == nil {
		return
	}
	for _, e := range events {
		if e.ended != "" {
			s.opts.Observer.SessionEnded(e.key, e.ended)
			continue
		}
		s.opts.Observer.SessionExtended(e.key, e.idle, e.absolute)
	}
}
```

Измените `internal/operator/profile.go`:

```diff
--- a/internal/operator/profile.go
+++ b/internal/operator/profile.go
@@ -14,7 +14,7 @@ var ErrCurrentPasswordIncorrect = errors.New("current password incorrect")
 // UpdateDisplayName persists the canonical display name for the operator of session id.
 func (s *Service) UpdateDisplayName(id, raw string) (Profile, error) {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	now := s.opts.Now()
 	st, err := s.activeLocked(id, now)
 	if err != nil {
@@ -30,6 +30,7 @@ func (s *Service) UpdateDisplayName(id, raw string) (Profile, error) {
 	if err := s.commitLocked(next, maps.Clone(s.sessions), accountChange); err != nil {
 		return Profile{}, err
 	}
+	s.extendedLocked(id, st)
 	return s.account.Profile(), nil
 }
 
@@ -84,16 +85,23 @@ func (s *Service) ChangePassword(ctx context.Context, id, current, next string)
 	}
 
 	s.mu.Lock()
-	defer s.mu.Unlock()
-	if _, err := s.activeLocked(id, s.opts.Now()); err != nil {
+	defer s.unlock()
+	now = s.opts.Now()
+	if _, err := s.activeLocked(id, now); err != nil {
 		return err
 	}
 	updated := s.account
 	updated.PasswordHash = hash
 	updated.CredentialVersion++
+	revoked := s.sessions
 	if err := s.commitLocked(updated, map[string]*session{}, accountChange); err != nil {
 		return err
 	}
+	for digest, st := range revoked {
+		if !s.expired(st, now) {
+			s.endedLocked(digest, EndRevoked)
+		}
+	}
 	// The earlier wrong passwords were made against a password that no longer exists.
 	s.changeThrottle.failures = s.changeThrottle.failures[:0]
 	return nil
@@ -104,7 +112,7 @@ func (s *Service) ChangePassword(ctx context.Context, id, current, next string)
 // password change and revokes the calling session; the revocation is stored before it is reported.
 func (s *Service) recordWrongCurrent(id string) error {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	// Recovery may have begun during the verification; no answer about the session is reliable then.
 	if s.recovering {
 		return ErrAuthUnavailable
@@ -123,6 +131,7 @@ func (s *Service) recordWrongCurrent(id string) error {
 	if err := s.commitLocked(s.account, next, sessionChange); err != nil {
 		return err
 	}
+	s.endedLocked(sessionDigest(id), EndRevoked)
 	limited.SessionRevoked = true
 	return limited
 }
```

Измените `internal/operator/service.go`:

```diff
--- a/internal/operator/service.go
+++ b/internal/operator/service.go
@@ -59,6 +59,8 @@ type Options struct {
 	FeedbackContextLimit int
 	FeedbackProcessLimit int
 	FeedbackWindow       time.Duration
+	// Observer, when set, hears about extended and ended sessions after each commit.
+	Observer SessionObserver
 }
 
 // Session is what the HTTP layer needs about an authenticated session. ID is the bearer token for the
@@ -97,6 +99,8 @@ type Service struct {
 	changeThrottle failureThrottle
 	// feedback holds the admitted feedback posts of the whole process.
 	feedback attempts
+	// events wait under mu for unlock to report them to the observer.
+	events []sessionEvent
 }
 
 // NewService starts from the persisted state. Stored sessions that expired or belong to an older
@@ -169,7 +173,7 @@ func (s *Service) Login(ctx context.Context, login, password, previousID string,
 	}
 
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	if s.recovering {
 		return Profile{}, Session{}, ErrAuthUnavailable
 	}
@@ -186,6 +190,7 @@ func (s *Service) Login(ctx context.Context, login, password, previousID string,
 	}
 	now := s.opts.Now()
 	next := maps.Clone(s.sessions)
+	replaced := previousID != "" && s.liveLocked(previousID, now) != nil
 	if previousID != "" {
 		delete(next, sessionDigest(previousID))
 	}
@@ -194,7 +199,7 @@ func (s *Service) Login(ctx context.Context, login, password, previousID string,
 			delete(next, digest)
 		}
 	}
-	evictOldest(next, s.opts.MaxSessions-1)
+	evicted := evictOldest(next, s.opts.MaxSessions-1)
 	st := &session{rememberMe: rememberMe, credentialVersion: s.account.CredentialVersion, created: now, lastSeen: now}
 	st.expires = now.Add(s.opts.AbsoluteTimeout)
 	if rememberMe {
@@ -204,13 +209,19 @@ func (s *Service) Login(ctx context.Context, login, password, previousID string,
 	if err := s.commitLocked(s.account, next, sessionChange); err != nil {
 		return Profile{}, Session{}, err
 	}
+	if replaced {
+		s.endedLocked(sessionDigest(previousID), EndSignedOut)
+	}
+	for _, digest := range evicted {
+		s.endedLocked(digest, EndRevoked)
+	}
 	s.throttle.failures = s.throttle.failures[:0]
 	return s.account.Profile(), s.view(token, st), nil
 }
 
 func (s *Service) Authenticate(id string) (Profile, Session, error) {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	now := s.opts.Now()
 	st, err := s.activeLocked(id, now)
 	if err != nil {
@@ -223,7 +234,7 @@ func (s *Service) Authenticate(id string) (Profile, Session, error) {
 // A wrong token neither extends nor revokes the session.
 func (s *Service) Authorize(id, csrfToken string) (Profile, Session, error) {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	now := s.opts.Now()
 	st, err := s.activeLocked(id, now)
 	if err != nil {
@@ -246,15 +257,28 @@ func (s *Service) refreshActivityLocked(id string, st *session, now time.Time) (
 	if err := s.commitLocked(s.account, next, sessionChange); err != nil {
 		return Profile{}, Session{}, err
 	}
+	s.extendedLocked(id, &renewed)
 	return s.account.Profile(), s.view(id, &renewed), nil
 }
 
+// Peek checks session id the way Authenticate does, but neither extends it nor writes the store. Live
+// streams and background requests use it.
+func (s *Service) Peek(id string) (Profile, Session, error) {
+	s.mu.Lock()
+	defer s.mu.Unlock()
+	st, err := s.activeLocked(id, s.opts.Now())
+	if err != nil {
+		return Profile{}, Session{}, err
+	}
+	return s.account.Profile(), s.view(id, st), nil
+}
+
 // Current returns the profile and session for the operator's own view. An ordinary session's renewed
 // idle deadline is stored before it is reported; a remembered session has nothing to renew. If the
 // store definitely failed, the session stays usable and the deadline the store holds is reported.
 func (s *Service) Current(id string) (Profile, Session, error) {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	now := s.opts.Now()
 	st, err := s.activeLocked(id, now)
 	if err != nil {
@@ -277,6 +301,7 @@ func (s *Service) Current(id string) (Profile, Session, error) {
 		stored.lastSeen = st.storedLastSeen
 		return s.account.Profile(), s.view(id, &stored), nil
 	}
+	s.extendedLocked(id, &renewed)
 	return s.account.Profile(), s.view(id, &renewed), nil
 }
 
@@ -325,7 +350,7 @@ func (s *Service) verify(ctx context.Context, password, encoded string) (bool, e
 // not authoritative, so only a request without a session can succeed.
 func (s *Service) Logout(id string) error {
 	s.mu.Lock()
-	defer s.mu.Unlock()
+	defer s.unlock()
 	if s.recovering {
 		if id == "" {
 			return nil
@@ -337,7 +362,11 @@ func (s *Service) Logout(id string) error {
 	}
 	next := maps.Clone(s.sessions)
 	delete(next, sessionDigest(id))
-	return s.commitLocked(s.account, next, sessionChange)
+	if err := s.commitLocked(s.account, next, sessionChange); err != nil {
+		return err
+	}
+	s.endedLocked(sessionDigest(id), EndSignedOut)
+	return nil
 }
 
 func (s *Service) newTokenLocked() (string, error) {
```

Измените `internal/operator/sessions.go`:

```diff
--- a/internal/operator/sessions.go
+++ b/internal/operator/sessions.go
@@ -106,8 +106,9 @@ func (s *Service) restore(stored []StoredSession) {
 	evictOldest(s.sessions, s.opts.MaxSessions)
 }
 
-// evictOldest drops the oldest sessions until at most keep remain.
-func evictOldest(sessions map[string]*session, keep int) {
+// evictOldest drops the oldest sessions until at most keep remain and returns their digests.
+func evictOldest(sessions map[string]*session, keep int) []string {
+	var evicted []string
 	for len(sessions) > keep {
 		var oldestDigest string
 		var oldest *session
@@ -117,7 +118,9 @@ func evictOldest(sessions map[string]*session, keep int) {
 			}
 		}
 		delete(sessions, oldestDigest)
+		evicted = append(evicted, oldestDigest)
 	}
+	return evicted
 }
 
 // stateOf is what the store holds for account and sessions, oldest session first.
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add internal/operator/observer.go internal/operator/observer_test.go internal/operator/profile.go internal/operator/service.go internal/operator/sessions.go
git commit -m "feat(operator): add Peek and report committed session changes to an observer"
```


### Task 5: Фоновые запросы без продления сессии

**Files:**
- Create: `internal/operatorhttp/activity.go`
- Test: `internal/operatorhttp/activity_test.go`
- Modify: `internal/operatorhttp/api.go`
- Modify: `internal/operatorhttp/guard.go`
- Modify: `internal/operatorhttp/session.go`

**Interfaces:**
- Consumes: `(*operator.Service).Peek` (Task 4).
- Produces (пакет `operatorhttp`):
  - `const ActivityHeader = "X-Umir-Activity"`;
  - метод `Peek` в интерфейсе `Sessions`;
  - `Protect` и `GET /api/operator/me` для фонового запроса используют `Peek`;
  - `StripCredentials` удаляет `ActivityHeader` перед upstream.

- [ ] **Step 1: Write the failing tests**

Создайте `internal/operatorhttp/activity_test.go`:

```go
package operatorhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// savedCount is the number of commits the account store received.
func (s *fakeStore) savedCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.saved) }

// idleDeadline reads the session's idle deadline without extending it.
func (f *fixture) idleDeadline(session *http.Cookie) time.Time {
	f.t.Helper()
	_, current, err := f.accounts.Peek(session.Value)
	if err != nil {
		f.t.Fatalf("Peek: %v", err)
	}
	return current.IdleExpiresAt
}

func TestBackgroundRequestsDoNotExtendTheSession(t *testing.T) {
	f := loopbackFixture(t)
	up := &upstream{t: t}
	guarded := f.api.Protect(up)
	session, token := f.login()
	before := f.idleDeadline(session)
	f.clock.Advance(10 * time.Minute)
	saved := f.store.savedCount()
	background := map[string]string{ActivityHeader: "background"}
	for _, c := range []call{
		{method: http.MethodGet, path: "/api/warehouse/expiring-batches", cookies: []*http.Cookie{session}, header: background},
		{method: http.MethodPost, path: "/api/serving/order-confirmations", cookies: []*http.Cookie{session}, csrf: token, origin: testOrigin, header: background},
	} {
		if w := f.send(guarded, c); w.Code != http.StatusAccepted {
			t.Fatalf("background %s: %d %s", c.method, w.Code, w.Body)
		}
	}
	if got := f.idleDeadline(session); !got.Equal(before) || f.store.savedCount() != saved {
		t.Fatalf("background requests moved the idle deadline to %v or wrote %d times", got, f.store.savedCount()-saved)
	}
	// The same request without the header is the operator's action and extends the session.
	if w := f.send(guarded, call{method: http.MethodGet, path: "/api/warehouse/expiring-batches", cookies: []*http.Cookie{session}}); w.Code != http.StatusAccepted {
		t.Fatalf("ordinary GET: %d", w.Code)
	}
	if got := f.idleDeadline(session); !got.Equal(f.clock.Now().Add(30*time.Minute)) || f.store.savedCount() != saved+1 {
		t.Fatalf("ordinary GET: idle %v, writes %d", got, f.store.savedCount()-saved)
	}
}

func TestBackgroundRequestsPassTheSameChecks(t *testing.T) {
	f := loopbackFixture(t)
	up := &upstream{t: t}
	guarded := f.api.Protect(up)
	session, token := f.login()
	_, anonymous := f.anonymous()
	background := map[string]string{ActivityHeader: "background"}
	crossSite := map[string]string{ActivityHeader: "background", "Sec-Fetch-Site": "cross-site"}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"read without session", call{method: http.MethodGet, path: "/api/serving/menu", header: background}, http.StatusUnauthorized, "Unauthenticated"},
		{"write without origin", call{method: http.MethodPost, path: "/api/serving/orders", cookies: []*http.Cookie{session}, csrf: token, header: background}, http.StatusForbidden, "InvalidOrigin"},
		{"write cross-site", call{method: http.MethodPost, path: "/api/serving/orders", cookies: []*http.Cookie{session}, csrf: token, origin: testOrigin, header: crossSite}, http.StatusForbidden, "InvalidOrigin"},
		{"write without token", call{method: http.MethodPost, path: "/api/serving/orders", cookies: []*http.Cookie{session}, origin: testOrigin, header: background}, http.StatusForbidden, "InvalidCsrfToken"},
		{"write with anonymous token", call{method: http.MethodDelete, path: "/api/warehouse/items/1", cookies: []*http.Cookie{session}, csrf: anonymous, origin: testOrigin, header: background}, http.StatusForbidden, "InvalidCsrfToken"},
		{"TRACE", call{method: http.MethodTrace, path: "/api/serving/menu", cookies: []*http.Cookie{session}, header: background}, http.StatusMethodNotAllowed, "MethodNotAllowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantProblem(t, f.send(guarded, tc.c), tc.status, tc.code)
		})
	}
	if calls := up.calls(); len(calls) != 0 {
		t.Fatalf("rejected background requests reached the upstream: %v", calls)
	}
}

func TestOnlyTheExactBackgroundValueSkipsExtension(t *testing.T) {
	for _, values := range [][]string{{"Background"}, {"background "}, {"background", "background"}, {"foreground"}, {""}} {
		t.Run("", func(t *testing.T) {
			f := loopbackFixture(t)
			session, _ := f.login()
			f.clock.Advance(time.Minute)
			r := httptest.NewRequest(http.MethodGet, "/api/serving/menu", nil)
			r.AddCookie(session)
			r.Header[ActivityHeader] = values
			w := httptest.NewRecorder()
			f.api.Protect(&upstream{t: t}).ServeHTTP(w, r)
			if w.Code != http.StatusAccepted || !f.idleDeadline(session).Equal(f.clock.Now().Add(30*time.Minute)) {
				t.Errorf("%q: %d, idle %v; any other value counts as the operator's action", values, w.Code, f.idleDeadline(session))
			}
		})
	}
}

func TestBackgroundProfileReadDoesNotExtendTheSession(t *testing.T) {
	f := loopbackFixture(t)
	session, _ := f.login()
	before := f.idleDeadline(session)
	f.clock.Advance(10 * time.Minute)
	saved := f.store.savedCount()
	w := f.do(call{method: http.MethodGet, path: "/api/operator/me", cookies: []*http.Cookie{session}, header: map[string]string{ActivityHeader: "background"}})
	var body struct {
		Session struct {
			IdleExpiresAt string `json:"idleExpiresAt"`
		} `json:"session"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("background /me: %d %s", w.Code, w.Body)
	}
	if body.Session.IdleExpiresAt != timestamp(before) || f.store.savedCount() != saved {
		t.Fatalf("background /me reported %s and wrote %d times, want %s", body.Session.IdleExpiresAt, f.store.savedCount()-saved, timestamp(before))
	}
}

func TestActivityHeaderNeverReachesTheUpstream(t *testing.T) {
	var got http.Header
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
	r := httptest.NewRequest(http.MethodGet, "/menu", nil)
	r.Header.Set(ActivityHeader, "background")
	StripCredentials(next).ServeHTTP(httptest.NewRecorder(), r)
	if _, ok := got[ActivityHeader]; ok {
		t.Errorf("forwarded headers = %v", got)
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/operatorhttp`
Expected: FAIL, сборка: `undefined: ActivityHeader`.

- [ ] **Step 3: Implement**

Создайте `internal/operatorhttp/activity.go`:

```go
package operatorhttp

import (
	"net/http"

	"github.com/JustIkra/umir-gateway/internal/operator"
)

// ActivityHeader marks a request the operator did not cause, such as a re-read after a live signal.
// Only the exact value background has meaning: such a request is checked without extending the session.
const ActivityHeader = "X-Umir-Activity"

// background reports a request that carries exactly one ActivityHeader with the value background.
func background(r *http.Request) bool {
	values := r.Header.Values(ActivityHeader)
	return len(values) == 1 && values[0] == "background"
}

// peekAuthorized admits an unsafe background request as Authorize would, without extending the session.
func (a *API) peekAuthorized(id, csrfToken string) error {
	_, session, err := a.accounts.Peek(id)
	if err == nil && !operator.MatchToken(session.CSRFToken, csrfToken) {
		err = operator.ErrInvalidCSRFToken
	}
	return err
}
```

Измените `internal/operatorhttp/api.go`:

```diff
--- a/internal/operatorhttp/api.go
+++ b/internal/operatorhttp/api.go
@@ -21,6 +21,8 @@ type Sessions interface {
 	Authenticate(id string) (operator.Profile, operator.Session, error)
 	// Authorize admits an unsafe request that carries the session's CSRF token.
 	Authorize(id, csrfToken string) (operator.Profile, operator.Session, error)
+	// Peek checks a session like Authenticate but neither extends it nor writes the store.
+	Peek(id string) (operator.Profile, operator.Session, error)
 }
 
 // Accounts is what the account routes need besides Sessions; *operator.Service implements it.
```

Измените `internal/operatorhttp/guard.go`:

```diff
--- a/internal/operatorhttp/guard.go
+++ b/internal/operatorhttp/guard.go
@@ -16,19 +16,28 @@ var reserved = map[string]bool{
 // Protect admits a business request only with the operator's live session. GET, HEAD and OPTIONS need
 // the session; POST, PUT, PATCH and DELETE also need the exact Origin and the session's CSRF token.
 // Other methods are refused. Nothing reaches next before these checks pass, and a session that
-// expires after that does not affect the dispatched request.
+// expires after that does not affect the dispatched request. A background request passes the same
+// checks without extending the session.
 func (a *API) Protect(next http.Handler) http.Handler {
 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
 		id := a.cookie(r, a.cookies.session)
 		var err error
 		switch r.Method {
 		case http.MethodGet, http.MethodHead, http.MethodOptions:
-			_, _, err = a.accounts.Authenticate(id)
+			if background(r) {
+				_, _, err = a.accounts.Peek(id)
+			} else {
+				_, _, err = a.accounts.Authenticate(id)
+			}
 		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
 			if !a.admitOrigin(w, r) {
 				return
 			}
-			_, _, err = a.accounts.Authorize(id, r.Header.Get(CSRFHeader))
+			if background(r) {
+				err = a.peekAuthorized(id, r.Header.Get(CSRFHeader))
+			} else {
+				_, _, err = a.accounts.Authorize(id, r.Header.Get(CSRFHeader))
+			}
 		default:
 			w.Header().Set("Allow", "GET, HEAD, OPTIONS, POST, PUT, PATCH, DELETE")
 			a.problem(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "method is not supported")
@@ -42,12 +51,13 @@ func (a *API) Protect(next http.Handler) http.Handler {
 	})
 }
 
-// StripCredentials removes the operator cookies and the CSRF header before a request leaves the
-// gateway. Other cookies keep their original text; the caller's request is not modified.
+// StripCredentials removes the operator cookies, the CSRF header and ActivityHeader before a request
+// leaves the gateway. Other cookies keep their original text; the caller's request is not modified.
 func StripCredentials(next http.Handler) http.Handler {
 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
 		header := r.Header.Clone()
 		header.Del(CSRFHeader)
+		header.Del(ActivityHeader)
 		header.Del("Cookie")
 		for _, line := range r.Header.Values("Cookie") {
 			var kept []string
```

Измените `internal/operatorhttp/session.go`:

```diff
--- a/internal/operatorhttp/session.go
+++ b/internal/operatorhttp/session.go
@@ -99,9 +99,14 @@ func (a *API) logout(w http.ResponseWriter, r *http.Request) {
 	w.WriteHeader(http.StatusNoContent)
 }
 
-// me returns the profile and session; like every authenticated request, it extends the idle deadline.
+// me returns the profile and session; like every authenticated request, it extends the idle deadline,
+// unless it is a background request.
 func (a *API) me(w http.ResponseWriter, r *http.Request) {
-	profile, session, err := a.accounts.Current(a.cookie(r, a.cookies.session))
+	current := a.accounts.Current
+	if background(r) {
+		current = a.accounts.Peek
+	}
+	profile, session, err := current(a.cookie(r, a.cookies.session))
 	if err != nil {
 		a.fail(w, r, err, "")
 		return
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add internal/operatorhttp/activity.go internal/operatorhttp/activity_test.go internal/operatorhttp/api.go internal/operatorhttp/guard.go internal/operatorhttp/session.go
git commit -m "feat(operatorhttp): let background requests pass without extending the session"
```


### Task 6: Поток `GET /api/live`

**Files:**
- Modify: `.go-arch-lint.yml`
- Test: `cmd/server/auth_test.go`
- Test: `cmd/server/live_test.go`
- Modify: `cmd/server/main.go`
- Test: `cmd/server/operator_test.go`
- Modify: `internal/httpapi/router.go`
- Test: `internal/httpapi/router_test.go`
- Create: `internal/live/clock.go`
- Create: `internal/live/sessions.go`
- Create: `internal/live/streams.go`
- Test: `internal/live/streams_test.go`
- Create: `internal/live/upstream.go`
- Create: `internal/operatorhttp/live.go`
- Test: `internal/operatorhttp/live_test.go`

**Interfaces:**
- Consumes:
  - `journal.Journal` как `live.Changes` (`Last`, `Subscribe`) из Task 1;
  - `operator.SessionObserver`, `EndReason`, `SessionKey`, `Peek` из Task 4;
  - `httpapi.WriteProblem`, `httpapi.RequestID`.
- Produces (пакет `live`):
  - часы: `type Clock interface { Now() time.Time; NewTimer(time.Duration) Timer }`, `type Timer interface { C() <-chan time.Time; Stop() bool }`, `SystemClock`;
  - доступность склада: `NewUpstream() *Upstream` с методами `Available() bool`, `Set(bool)`, `Subscribe(ctx) <-chan struct{}`;
  - события сессий: `NewSessionEvents() *SessionEvents`, реализует `operator.SessionObserver`;
  - потоки: `type Options struct { Changes Changes; Upstream *Upstream; Sessions Sessions; Events *SessionEvents; Clock Clock; PerSession, Total int; Ping, WriteTimeout time.Duration }`, `NewStreams(Options) (*Streams, error)`, `(*Streams).Serve(w, r, id string, session operator.Session)`, `(*Streams).Close()`;
  - значения по умолчанию: `DefaultPerSession = 8`, `DefaultTotal = 64`, `DefaultPing = 15s`, `DefaultWriteTimeout = 5s`.
- Produces вне пакета `live`:
  - `operatorhttp.(*API).GuardLive(next func(http.ResponseWriter, *http.Request, string, operator.Session)) http.Handler`;
  - обязательное поле `httpapi.Routes.Live http.Handler` на точном пути `/api/live`;
  - в `cmd/server`: `components.streams`, `newAccounts(cfg config.Operator, observer operator.SessionObserver)`, `newServer(cfg config.Config, app components) *http.Server` с `RegisterOnShutdown(app.streams.Close)`.

После подписки на журнал, доступность и события сессии `Serve` ещё раз вызывает `Peek`. Так выход, случившийся между guard и подпиской, даёт `401`, а не поток до старого срока. В конце потока срок записи сбрасывается: иначе соединение, которое браузер оставит для других запросов, получит старый срок записи.

- [ ] **Step 1: Write the failing tests**

Измените `cmd/server/auth_test.go`:

```diff
--- a/cmd/server/auth_test.go
+++ b/cmd/server/auth_test.go
@@ -89,7 +89,7 @@ func TestInvalidBootstrapFailsWithoutEchoOrFile(t *testing.T) {
 	if err != nil {
 		t.Fatal(err)
 	}
-	if _, err := newAccounts(cfg.Operator); err == nil || strings.Contains(err.Error(), "c2VjcmV0") {
+	if _, err := newAccounts(cfg.Operator, nil); err == nil || strings.Contains(err.Error(), "c2VjcmV0") {
 		t.Fatalf("newAccounts error = %v", err)
 	}
 	if _, err := os.Stat(values["OPERATOR_STORE_PATH"]); !errors.Is(err, os.ErrNotExist) {
```

Создайте `cmd/server/live_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JustIkra/umir-gateway/internal/journal"
)

// openLive opens /api/live with client; the stream closes when the test ends.
func openLive(t *testing.T, client *http.Client, address string) *bufio.Reader {
	t.Helper()
	response, err := client.Get(address + "/api/live")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close stream: %v", err)
		}
	})
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("stream: %d %v", response.StatusCode, response.Header)
	}
	return bufio.NewReader(response.Body)
}

// nextEvent returns the next event, skipping the retry field and pings.
func nextEvent(t *testing.T, reader *bufio.Reader) (name, data string) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			return name, data
		}
	}
}

// greeting reads the four events every stream starts with and checks their order.
func greeting(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	var names []string
	for range 4 {
		name, _ := nextEvent(t, reader)
		names = append(names, name)
	}
	if want := []string{"change", "tick", "session", "upstream"}; !slices.Equal(names, want) {
		t.Fatalf("first events %v, want %v", names, want)
	}
}

// liveGateway serves a gateway whose upstreams answer 200 and signs in over HTTP.
func liveGateway(t *testing.T, timeout string) (components, map[string]string, string, *http.Client) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(upstream.Close)
	values := operatorEnv(t)
	values["SERVING_URL"], values["WAREHOUSE_URL"], values["UPSTREAM_TIMEOUT"] = upstream.URL, upstream.URL, timeout
	app := assemble(t, values)
	front := httptest.NewServer(app.handler)
	t.Cleanup(front.Close) // registered first, so it runs after the streams are closed
	return app, values, front.URL, signInOver(t, front.URL)
}

func TestLiveStreamOutlivesTheUpstreamTimeout(t *testing.T) {
	app, _, address, client := liveGateway(t, "100ms")
	reader := openLive(t, client, address)
	greeting(t, reader)
	time.Sleep(300 * time.Millisecond)
	app.journal.Append(journal.Entry{Source: journal.SourceRequest, Outcome: journal.OutcomeSuccess})
	name, data := nextEvent(t, reader)
	var change struct{ Epoch, Seq string }
	if err := json.Unmarshal([]byte(data), &change); err != nil || name != "change" || change.Seq != "1" || change.Epoch == "" {
		t.Fatalf("after the upstream timeout: %s %s", name, data)
	}
}

// send makes one request with the operator's cookies and returns its status.
func send(t *testing.T, client *http.Client, method, target string, header map[string]string) int {
	t.Helper()
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	if err := errors.Join(readErr, response.Body.Close()); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

func TestStreamAndBackgroundReadsLeaveTheAccountFileAlone(t *testing.T) {
	_, values, address, client := liveGateway(t, "")
	stored := func() []byte {
		data, err := os.ReadFile(values["OPERATOR_STORE_PATH"])
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := stored()
	greeting(t, openLive(t, client, address))
	background := map[string]string{"X-Umir-Activity": "background"}
	for _, target := range []string{"/api/warehouse/stock", "/api/operator/me"} {
		if status := send(t, client, http.MethodGet, address+target, background); status != http.StatusOK {
			t.Fatalf("background %s: %d", target, status)
		}
	}
	if !bytes.Equal(stored(), before) {
		t.Fatal("the stream or a background read wrote the account file")
	}
	if status := send(t, client, http.MethodGet, address+"/api/warehouse/stock", nil); status != http.StatusOK {
		t.Fatalf("ordinary GET: %d", status)
	}
	if bytes.Equal(stored(), before) {
		t.Fatal("an ordinary GET did not store the renewed deadline")
	}
}

func TestLiveStreamEndsWhenTheOperatorSignsOut(t *testing.T) {
	_, _, address, client := liveGateway(t, "")
	reader := openLive(t, client, address)
	greeting(t, reader)
	response, err := client.Get(address + "/api/operator/csrf")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ CSRFToken string }
	if err := errors.Join(json.NewDecoder(response.Body).Decode(&body), response.Body.Close()); err != nil {
		t.Fatal(err)
	}
	if status := send(t, client, http.MethodDelete, address+"/api/operator/session",
		map[string]string{"Origin": testOrigin, "X-CSRF-Token": body.CSRFToken}); status != http.StatusNoContent {
		t.Fatalf("logout: %d", status)
	}
	if name, data := nextEvent(t, reader); name != "session-ended" || data != `{"reason":"signed_out"}` {
		t.Fatalf("after logout: %s %s", name, data)
	}
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("stream stayed open: %q %v", rest, err)
	}
}

func TestShutdownEndsLiveStreamsAtOnce(t *testing.T) {
	values := operatorEnv(t)
	app := assemble(t, values)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopped := make(chan error, 1)
	go func() { stopped <- run(ctx, newServer(loadConfig(t, values), app), listener, 5*time.Second) }()
	address := "http://" + listener.Addr().String()
	reader := openLive(t, signInOver(t, address), address)
	greeting(t, reader)
	stop()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an open stream held the shutdown")
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("stream did not end cleanly: %v", err)
	}
}
```

Измените `cmd/server/operator_test.go`:

```diff
--- a/cmd/server/operator_test.go
+++ b/cmd/server/operator_test.go
@@ -78,13 +78,14 @@ func gatewayWith(t *testing.T, values map[string]string) http.Handler {
 	return assemble(t, values).handler
 }
 
-// assemble builds the gateway's components from settings.
+// assemble builds the gateway's components from settings; the expiration runs are not started.
 func assemble(t *testing.T, values map[string]string) components {
 	t.Helper()
 	app, err := newComponents(loadConfig(t, values), log.Default())
 	if err != nil {
 		t.Fatal(err)
 	}
+	t.Cleanup(app.streams.Close)
 	return app
 }
```

Измените `internal/httpapi/router_test.go`:

```diff
--- a/internal/httpapi/router_test.go
+++ b/internal/httpapi/router_test.go
@@ -18,14 +18,15 @@ func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
 
 // testRoutes guards with a stub that records what it protected and lets it through.
 type testRoutes struct {
-	serving, warehouse, operator, feedback *recordingHandler
-	guarded, feedbackGuarded, recorded     []string
+	serving, warehouse, operator, feedback, live *recordingHandler
+	guarded, feedbackGuarded, recorded           []string
 }
 
 func newTestRouter(t *testing.T) (*testRoutes, http.Handler) {
 	t.Helper()
 	routes := &testRoutes{
 		serving: &recordingHandler{}, warehouse: &recordingHandler{}, operator: &recordingHandler{}, feedback: &recordingHandler{},
+		live: &recordingHandler{},
 	}
 	router, err := NewRouter(Routes{
 		Serving: routes.serving, Warehouse: routes.warehouse, Operator: routes.operator, Feedback: routes.feedback,
@@ -45,6 +46,7 @@ func newTestRouter(t *testing.T) (*testRoutes, http.Handler) {
 		Record: func(method, path string, status int) {
 			routes.recorded = append(routes.recorded, fmt.Sprintf("%s %s %d", method, path, status))
 		},
+		Live: routes.live,
 	})
 	if err != nil {
 		t.Fatal(err)
@@ -62,13 +64,14 @@ func TestNewRouterRequiresEveryRoute(t *testing.T) {
 		"feedback":       func(r *Routes) { r.Feedback = nil },
 		"feedback guard": func(r *Routes) { r.GuardFeedback = nil },
 		"recorder":       func(r *Routes) { r.Record = nil },
+		"live":           func(r *Routes) { r.Live = nil },
 	} {
 		t.Run(name, func(t *testing.T) {
 			pass := func(next http.Handler) http.Handler { return next }
 			routes := Routes{
 				Serving: &recordingHandler{}, Warehouse: &recordingHandler{}, Operator: &recordingHandler{},
 				Feedback: &recordingHandler{}, Protect: pass, GuardFeedback: pass, NewRequestID: func() string { return "" },
-				Record: func(string, string, int) {},
+				Record: func(string, string, int) {}, Live: &recordingHandler{},
 			}
 			drop(&routes)
 			if _, err := NewRouter(routes); err == nil {
@@ -150,6 +153,7 @@ func TestRouterRejectedRequestNeverReachesUpstream(t *testing.T) {
 		},
 		NewRequestID: func() string { return "req-1" },
 		Record:       func(string, string, int) { t.Error("a rejected request was recorded") },
+		Live:         &recordingHandler{},
 	})
 	if err != nil {
 		t.Fatal(err)
@@ -238,3 +242,17 @@ func TestRouterSendsOnlyTheExactFeedbackPathThroughItsGuard(t *testing.T) {
 		}
 	}
 }
+
+func TestRouterSendsOnlyTheExactLivePathToTheStream(t *testing.T) {
+	routes, router := newTestRouter(t)
+	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/live", nil))
+	if routes.live.request == nil || len(routes.guarded) != 0 {
+		t.Fatalf("live: %v, business guard: %v", routes.live.request != nil, routes.guarded)
+	}
+	routes.live.request = nil
+	response := httptest.NewRecorder()
+	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/live/other", nil))
+	if routes.live.request != nil || response.Code != http.StatusNotFound {
+		t.Fatalf("/api/live/other: %d, reached the stream: %v", response.Code, routes.live.request != nil)
+	}
+}
```

Создайте `internal/live/streams_test.go`:

```go
package live

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustIkra/umir-gateway/internal/operator"
)

var start = time.Date(2026, 10, 12, 14, 0, 30, 0, moscow)

// fakeClock fires its timers only when the test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock   *fakeClock
	at      time.Time
	c       chan time.Time
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stopped = true
	return true
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), c: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t
}

// advance moves the clock and fires every timer that is due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, t := range c.timers {
		if t.stopped {
			continue
		}
		if !t.at.After(c.now) {
			t.c <- c.now
			continue
		}
		kept = append(kept, t)
	}
	c.timers = kept
}

// fakeChanges is a journal that the test moves by hand.
type fakeChanges struct {
	mu     sync.Mutex
	seq    int64
	signal chan struct{}
}

func (c *fakeChanges) Last() (string, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return "epoch-1", c.seq
}

func (c *fakeChanges) Subscribe(context.Context) <-chan struct{} { return c.signal }

func (c *fakeChanges) append() {
	c.mu.Lock()
	c.seq++
	c.mu.Unlock()
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

// fakeSessions answers Peek with the session or the error the test sets.
type fakeSessions struct {
	mu      sync.Mutex
	session operator.Session
	err     error
}

func (s *fakeSessions) Peek(string) (operator.Profile, operator.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return operator.Profile{}, s.session, s.err
}

func (s *fakeSessions) set(session operator.Session, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session, s.err = session, err
}

type fixture struct {
	clock    *fakeClock
	changes  *fakeChanges
	upstream *Upstream
	sessions *fakeSessions
	events   *SessionEvents
	streams  *Streams
}

func newFixture(t *testing.T, perSession, total int) *fixture {
	t.Helper()
	f := &fixture{
		clock:    &fakeClock{now: start},
		changes:  &fakeChanges{signal: make(chan struct{}, 1)},
		upstream: NewUpstream(),
		sessions: &fakeSessions{session: operator.Session{IdleExpiresAt: start.Add(30 * time.Minute), AbsoluteExpiresAt: start.Add(8 * time.Hour)}},
		events:   NewSessionEvents(),
	}
	streams, err := NewStreams(Options{
		Changes: f.changes, Upstream: f.upstream, Sessions: f.sessions, Events: f.events, Clock: f.clock,
		PerSession: perSession, Total: total, Ping: DefaultPing, WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.streams = streams
	return f
}

// serve starts a server whose streams belong to the session token; Close runs before the server stops.
func (f *fixture) serve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.streams.Serve(w, r, r.URL.Query().Get("token"), operator.Session{})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(f.streams.Close)
	return server.URL
}

type stream struct {
	t      *testing.T
	reader *bufio.Reader
}

// open returns the status, the headers and the stream; the body closes when the test ends.
func open(t *testing.T, address, token string) (int, http.Header, *stream) {
	t.Helper()
	response, err := http.Get(address + "/?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return response.StatusCode, response.Header, &stream{t: t, reader: bufio.NewReader(response.Body)}
}

// next returns the next event or comment: "name data" or ": ping".
func (s *stream) next() string {
	s.t.Helper()
	var name, data string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			s.t.Fatalf("read: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, ": "):
			return line
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			return name + " " + data
		}
	}
}

// greeted opens a stream and reads its first frames.
func greeted(t *testing.T, address, token string) *stream {
	t.Helper()
	_, _, s := open(t, address, token)
	for range 4 {
		s.next()
	}
	return s
}

func TestStreamStartsWithHeadersRetryAndFourEvents(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	f.changes.append()
	status, header, s := open(t, f.serve(t), "a")
	if status != http.StatusOK || header.Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		header.Get("Cache-Control") != "no-store" || header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("response: %d %v", status, header)
	}
	if line, err := s.reader.ReadString('\n'); err != nil || line != "retry: 3000\n" {
		t.Fatalf("first line %q %v", line, err)
	}
	want := []string{
		`change {"epoch":"epoch-1","seq":"1"}`,
		`tick {"serverTime":"2026-10-12T14:00:30+03:00"}`,
		`session {"idleExpiresAt":"2026-10-12T14:30:30+03:00","absoluteExpiresAt":"2026-10-12T22:00:30+03:00"}`,
		`upstream {"available":true}`,
	}
	for _, w := range want {
		if got := s.next(); got != w {
			t.Fatalf("got %s, want %s", got, w)
		}
	}
}

func TestChangeBurstsArriveAsOneEventWithTheLatestSeq(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	address := f.serve(t)
	s := greeted(t, address, "a")
	f.changes.mu.Lock()
	f.changes.seq = 41
	f.changes.mu.Unlock()
	f.changes.append()
	f.changes.append()
	if got := s.next(); got != `change {"epoch":"epoch-1","seq":"43"}` {
		t.Fatalf("got %s", got)
	}
}

func TestUpstreamChangesAreReported(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	s := greeted(t, f.serve(t), "a")
	f.upstream.Set(false)
	if got := s.next(); got != `upstream {"available":false}` {
		t.Fatalf("got %s", got)
	}
	f.upstream.Set(true)
	if got := s.next(); got != `upstream {"available":true}` {
		t.Fatalf("got %s", got)
	}
}

func TestSessionEventsReachOnlyTheirSession(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	address := f.serve(t)
	mine := greeted(t, address, "a")
	other := greeted(t, address, "b")
	f.events.SessionExtended(operator.SessionKey("a"), start.Add(40*time.Minute), start.Add(8*time.Hour))
	if got := mine.next(); got != `session {"idleExpiresAt":"2026-10-12T14:40:30+03:00","absoluteExpiresAt":"2026-10-12T22:00:30+03:00"}` {
		t.Fatalf("got %s", got)
	}
	f.events.SessionEnded(operator.SessionKey("a"), operator.EndSignedOut)
	if got := mine.next(); got != `session-ended {"reason":"signed_out"}` {
		t.Fatalf("got %s", got)
	}
	if rest, err := io.ReadAll(mine.reader); err != nil || len(rest) != 0 {
		t.Fatalf("stream stayed open: %q %v", rest, err)
	}
	f.changes.append()
	if got := other.next(); got != `change {"epoch":"epoch-1","seq":"1"}` {
		t.Fatalf("the other session heard %s", got)
	}
}

func TestAnEndIsNeverReplacedByALaterExtension(t *testing.T) {
	w := &watcher{ready: make(chan struct{}, 1)}
	w.offer(update{ended: operator.EndRevoked})
	w.offer(update{idle: start, absolute: start})
	if u, ok := w.take(); !ok || u.ended != operator.EndRevoked {
		t.Fatalf("took %+v %v", u, ok)
	}
}

func TestExpiryEndsTheStreamAfterPeekConfirmsIt(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	s := greeted(t, f.serve(t), "a")
	f.sessions.set(operator.Session{}, operator.ErrUnauthenticated)
	f.clock.advance(30 * time.Minute)
	for {
		got := s.next()
		if strings.HasPrefix(got, "tick ") || got == ": ping" {
			continue
		}
		if got != `session-ended {"reason":"expired"}` {
			t.Fatalf("got %s", got)
		}
		return
	}
}

func TestAMovedDeadlineIsReportedInsteadOfAnEnd(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	s := greeted(t, f.serve(t), "a")
	f.sessions.set(operator.Session{IdleExpiresAt: start.Add(50 * time.Minute), AbsoluteExpiresAt: start.Add(8 * time.Hour)}, nil)
	f.clock.advance(30 * time.Minute)
	for {
		got := s.next()
		if strings.HasPrefix(got, "tick ") || got == ": ping" {
			continue
		}
		if got != `session {"idleExpiresAt":"2026-10-12T14:50:30+03:00","absoluteExpiresAt":"2026-10-12T22:00:30+03:00"}` {
			t.Fatalf("got %s", got)
		}
		return
	}
}

func TestTicksFollowTheMinuteAndPingsKeepTheStreamAlive(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	s := greeted(t, f.serve(t), "a")
	f.clock.advance(15 * time.Second)
	if got := s.next(); got != ": ping" {
		t.Fatalf("after 15 s: %s", got)
	}
	f.clock.advance(15 * time.Second)
	var got []string
	got = append(got, s.next(), s.next())
	if !strings.Contains(strings.Join(got, "|"), `tick {"serverTime":"2026-10-12T14:01:00+03:00"}`) ||
		!strings.Contains(strings.Join(got, "|"), ": ping") {
		t.Fatalf("at the minute: %v", got)
	}
}

func TestStreamLimits(t *testing.T) {
	f := newFixture(t, 2, 3)
	address := f.serve(t)
	greeted(t, address, "a")
	greeted(t, address, "a")
	refused := func(token string) {
		t.Helper()
		response, err := http.Get(address + "/?token=" + token)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Code string }
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		if err := errors.Join(decodeErr, response.Body.Close()); err != nil || response.StatusCode != http.StatusTooManyRequests || body.Code != "TooManyStreams" {
			t.Fatalf("%s: %d %+v %v", token, response.StatusCode, body, err)
		}
	}
	refused("a")
	greeted(t, address, "b")
	refused("c")
}

func TestASessionEndedBeforeTheWatchIsRefused(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	f.sessions.set(operator.Session{}, operator.ErrUnauthenticated)
	response, err := http.Get(f.serve(t) + "/?token=a")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestCloseEndsEveryStream(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	s := greeted(t, f.serve(t), "a")
	f.streams.Close()
	if rest, err := io.ReadAll(s.reader); err != nil || len(rest) != 0 {
		t.Fatalf("after Close: %q %v", rest, err)
	}
}

// stalledWriter accepts the first writes and then blocks until its write deadline passes.
type stalledWriter struct {
	header   http.Header
	mu       sync.Mutex
	writes   int
	deadline time.Time
}

func (w *stalledWriter) Header() http.Header { return w.header }

func (w *stalledWriter) WriteHeader(int) {}

func (w *stalledWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	n, deadline := w.writes, w.deadline
	w.mu.Unlock()
	if n <= 5 {
		return len(b), nil
	}
	time.Sleep(time.Until(deadline))
	return 0, errors.New("write deadline exceeded")
}

func (w *stalledWriter) Flush() {}

func (w *stalledWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = t
	return nil
}

func TestAClientThatStopsReadingIsDropped(t *testing.T) {
	f := newFixture(t, DefaultPerSession, DefaultTotal)
	w := &stalledWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.streams.Serve(w, httptest.NewRequest(http.MethodGet, "/api/live", nil), "a", operator.Session{})
	}()
	f.changes.append()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled client held its stream")
	}
	if !f.streams.acquire(operator.SessionKey("a")) {
		t.Fatal("the dropped stream kept its slot")
	}
}
```

Создайте `internal/operatorhttp/live_test.go`:

```go
package operatorhttp

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/JustIkra/umir-gateway/internal/operator"
)

// streamRecorder stands in for the live stream and remembers what the guard passed on.
type streamRecorder struct {
	calls   int
	id      string
	session operator.Session
}

func (s *streamRecorder) serve(w http.ResponseWriter, _ *http.Request, id string, session operator.Session) {
	s.calls++
	s.id, s.session = id, session
	w.WriteHeader(http.StatusOK)
}

func TestGuardLiveRejectsBeforeTheStream(t *testing.T) {
	f := loopbackFixture(t)
	stream := &streamRecorder{}
	guarded := f.api.GuardLive(stream.serve)
	session, _ := f.login()
	preauth, _ := f.anonymous()
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"POST", call{method: http.MethodPost, path: "/api/live", cookies: []*http.Cookie{session}, origin: testOrigin}, http.StatusMethodNotAllowed, "MethodNotAllowed"},
		{"HEAD", call{method: http.MethodHead, path: "/api/live", cookies: []*http.Cookie{session}}, http.StatusMethodNotAllowed, "MethodNotAllowed"},
		{"cross-site", call{method: http.MethodGet, path: "/api/live", cookies: []*http.Cookie{session}, header: map[string]string{"Sec-Fetch-Site": "cross-site"}}, http.StatusForbidden, "InvalidOrigin"},
		{"same-site", call{method: http.MethodGet, path: "/api/live", cookies: []*http.Cookie{session}, header: map[string]string{"Sec-Fetch-Site": "same-site"}}, http.StatusForbidden, "InvalidOrigin"},
		{"without session", call{method: http.MethodGet, path: "/api/live"}, http.StatusUnauthorized, "Unauthenticated"},
		{"pre-auth cookie only", call{method: http.MethodGet, path: "/api/live", cookies: []*http.Cookie{preauth}}, http.StatusUnauthorized, "Unauthenticated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.send(guarded, tc.c)
			wantProblem(t, w, tc.status, tc.code)
			if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != http.MethodGet {
				t.Errorf("Allow = %q", w.Header().Get("Allow"))
			}
			if tc.status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != challenge {
				t.Errorf("WWW-Authenticate = %q", w.Header().Get("WWW-Authenticate"))
			}
		})
	}
	if stream.calls != 0 {
		t.Fatalf("rejected requests opened %d streams", stream.calls)
	}
}

func TestGuardLiveDuringRecovery(t *testing.T) {
	f := loopbackFixture(t)
	stream := &streamRecorder{}
	session, _ := f.login()
	f.store.fail(errors.New("disk vanished"))
	if _, err := f.accounts.UpdateDisplayName(session.Value, "Смена"); !errors.Is(err, operator.ErrUpdateOutcomeUnknown) {
		t.Fatalf("UpdateDisplayName = %v", err)
	}
	wantProblem(t, f.send(f.api.GuardLive(stream.serve), call{method: http.MethodGet, path: "/api/live", cookies: []*http.Cookie{session}}),
		http.StatusServiceUnavailable, "AuthUnavailable")
	if stream.calls != 0 {
		t.Fatal("a stream opened during recovery")
	}
}

func TestGuardLiveAdmitsWithoutExtending(t *testing.T) {
	f := loopbackFixture(t)
	stream := &streamRecorder{}
	session, _ := f.login()
	before := f.idleDeadline(session)
	f.clock.Advance(10 * time.Minute)
	saved := f.store.savedCount()
	for _, site := range []string{"", "same-origin"} {
		c := call{method: http.MethodGet, path: "/api/live", cookies: []*http.Cookie{session}}
		if site != "" {
			c.header = map[string]string{"Sec-Fetch-Site": site}
		}
		if w := f.send(f.api.GuardLive(stream.serve), c); w.Code != http.StatusOK {
			t.Fatalf("Sec-Fetch-Site %q: %d %s", site, w.Code, w.Body)
		}
	}
	if stream.calls != 2 || stream.id != session.Value || !stream.session.IdleExpiresAt.Equal(before) {
		t.Fatalf("stream got %d calls, %q, %+v", stream.calls, stream.id, stream.session)
	}
	if !f.idleDeadline(session).Equal(before) || f.store.savedCount() != saved {
		t.Fatal("opening a stream extended the session or wrote the store")
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/live ./internal/operatorhttp ./internal/httpapi ./cmd/server`
Expected: FAIL, сборка: `undefined: Upstream`, `undefined: SessionEvents`, `undefined: Streams`, `undefined: Timer`.

- [ ] **Step 3: Implement**

Измените `.go-arch-lint.yml`:

```diff
--- a/.go-arch-lint.yml
+++ b/.go-arch-lint.yml
@@ -20,6 +20,7 @@ components:
   operatorhttp: {in: "internal/operatorhttp/**"}
   strictjson: {in: "internal/strictjson/**"}
   journal: {in: "internal/journal/**"}
+  live: {in: "internal/live/**"}
 vendors:
   x-text-norm: {in: "golang.org/x/text/unicode/norm"}
   x-crypto-argon2: {in: "golang.org/x/crypto/argon2"}
@@ -38,7 +39,7 @@ deps:
   bootstrap:
     mayDependOn: [bootstrap, transport, proxy]
   process:
-    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp, journal]
+    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp, journal, live]
     canUse: [x-term]
   testsupport:
     mayDependOn: [testsupport]
@@ -58,3 +59,5 @@ deps:
     mayDependOn: [strictjson]
   journal:
     mayDependOn: [journal]
+  live:
+    mayDependOn: [live, operator, httpapi]
```

Измените `cmd/server/main.go`:

```diff
--- a/cmd/server/main.go
+++ b/cmd/server/main.go
@@ -21,6 +21,7 @@ import (
 	"github.com/JustIkra/umir-gateway/internal/config"
 	"github.com/JustIkra/umir-gateway/internal/httpapi"
 	"github.com/JustIkra/umir-gateway/internal/journal"
+	"github.com/JustIkra/umir-gateway/internal/live"
 	"github.com/JustIkra/umir-gateway/internal/operator"
 	"github.com/JustIkra/umir-gateway/internal/operatorhttp"
 )
@@ -29,8 +30,8 @@ import (
 func newToken() (string, error) { return rand.Text(), nil }
 
 // newAccounts opens the account file, creating it from the bootstrap settings only when it is absent,
-// and starts the operator account module.
-func newAccounts(cfg config.Operator) (*operator.Service, error) {
+// and starts the operator account module; observer, when set, hears about extended and ended sessions.
+func newAccounts(cfg config.Operator, observer operator.SessionObserver) (*operator.Service, error) {
 	hasher, err := argon2id.New(argon2id.Selected, rand.Reader)
 	if err != nil {
 		return nil, err
@@ -57,19 +58,22 @@ func newAccounts(cfg config.Operator) (*operator.Service, error) {
 		MaxQueuedLogins: operator.DefaultMaxQueuedLogins, FailureLimit: operator.DefaultFailureLimit,
 		FailureWindow: operator.DefaultFailureWindow, Cooldown: operator.DefaultCooldown,
 		FeedbackContextLimit: operator.DefaultFeedbackContextLimit, FeedbackProcessLimit: operator.DefaultFeedbackProcessLimit,
-		FeedbackWindow: operator.DefaultFeedbackWindow,
+		FeedbackWindow: operator.DefaultFeedbackWindow, Observer: observer,
 	})
 }
 
-// components are the parts of one gateway process: the HTTP handler and the change journal.
+// components are the parts of one gateway process: the HTTP handler, the change journal and the live
+// streams.
 type components struct {
 	handler http.Handler
 	journal *journal.Journal
+	streams *live.Streams
 }
 
-// newComponents assembles the gateway from cfg.
+// newComponents assembles the gateway from cfg. Nothing runs yet: the process serves handler.
 func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
-	accounts, err := newAccounts(cfg.Operator)
+	events := live.NewSessionEvents()
+	accounts, err := newAccounts(cfg.Operator, events)
 	if err != nil {
 		return components{}, err
 	}
@@ -77,20 +81,27 @@ func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
 	if err != nil {
 		return components{}, err
 	}
+	streams, err := live.NewStreams(live.Options{
+		Changes: changes, Upstream: live.NewUpstream(), Sessions: accounts, Events: events, Clock: live.SystemClock{},
+		PerSession: live.DefaultPerSession, Total: live.DefaultTotal, Ping: live.DefaultPing, WriteTimeout: live.DefaultWriteTimeout,
+	})
+	if err != nil {
+		return components{}, err
+	}
 	// Zero Proxy field: internal upstreams never go through HTTP_PROXY from the environment.
 	transport := &http.Transport{MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second}
-	handler, err := newHandler(cfg, accounts, transport, changes, logger)
+	handler, err := newHandler(cfg, accounts, transport, changes, streams, logger)
 	if err != nil {
 		return components{}, err
 	}
-	return components{handler: handler, journal: changes}, nil
+	return components{handler: handler, journal: changes, streams: streams}, nil
 }
 
 // newHandler assembles the gateway: one proxy per upstream behind the operator guard and the router.
 // Each upstream is wrapped so operator credentials never reach it and it cannot set them. Admitted
-// writes go to changes.
+// writes go to changes; GET /api/live is served by streams.
 func newHandler(cfg config.Config, accounts operatorhttp.Accounts, transport http.RoundTripper, changes *journal.Journal,
-	logger *log.Logger) (http.Handler, error) {
+	streams *live.Streams, logger *log.Logger) (http.Handler, error) {
 	proxy := func(target *url.URL) (http.Handler, error) {
 		proxy, err := httpupstream.NewProxy(target, transport)
 		if err != nil {
@@ -137,6 +148,9 @@ func newHandler(cfg config.Config, accounts operatorhttp.Accounts, transport htt
 		Record: func(method, path string, status int) {
 			changes.Append(journal.Entry{Source: journal.SourceRequest, Method: method, Path: path, Status: status, Outcome: journal.OutcomeOf(status)})
 		},
+		// The stream is not bounded by the upstream deadline: it stays open until the client, the
+		// session or the server ends it.
+		Live: api.GuardLive(streams.Serve),
 	})
 }
 
@@ -171,6 +185,19 @@ func withDeadline(next http.Handler, timeout time.Duration) http.Handler {
 
 const shutdownGrace = 10 * time.Second
 
+// newServer serves app. Shutdown waits for idle connections, and a live stream never becomes idle, so
+// shutdown also ends the streams.
+func newServer(cfg config.Config, app components) *http.Server {
+	server := &http.Server{
+		Addr:              listenAddress(cfg),
+		Handler:           app.handler,
+		ReadHeaderTimeout: 5 * time.Second,
+		IdleTimeout:       60 * time.Second,
+	}
+	server.RegisterOnShutdown(app.streams.Close)
+	return server
+}
+
 // listenAddress binds LISTEN_HOST: loopback for a process started by hand, all interfaces in the image.
 func listenAddress(cfg config.Config) string { return net.JoinHostPort(cfg.ListenHost, cfg.Port) }
 
@@ -238,12 +265,7 @@ func main() {
 	if err != nil {
 		log.Fatal(err)
 	}
-	server := &http.Server{
-		Addr:              listenAddress(cfg),
-		Handler:           app.handler,
-		ReadHeaderTimeout: 5 * time.Second,
-		IdleTimeout:       60 * time.Second,
-	}
+	server := newServer(cfg, app)
 	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
 	defer stop()
 	listener, err := net.Listen("tcp", server.Addr)
```

Измените `internal/httpapi/router.go`:

```diff
--- a/internal/httpapi/router.go
+++ b/internal/httpapi/router.go
@@ -28,12 +28,14 @@ type Routes struct {
 	// Record learns the status of each state-changing business request that passed Protect and of each
 	// display-name change: 0 when no upstream response arrived.
 	Record func(method, path string, status int)
+	// Live serves the exact path /api/live, the stream of change signals; it checks the session itself.
+	Live http.Handler
 }
 
 func NewRouter(routes Routes) (http.Handler, error) {
 	if routes.Serving == nil || routes.Warehouse == nil || routes.Operator == nil || routes.Protect == nil ||
-		routes.Feedback == nil || routes.GuardFeedback == nil || routes.NewRequestID == nil || routes.Record == nil {
-		return nil, errors.New("router requires serving, warehouse, operator, protect, feedback and its guard, request IDs and a recorder")
+		routes.Feedback == nil || routes.GuardFeedback == nil || routes.NewRequestID == nil || routes.Record == nil || routes.Live == nil {
+		return nil, errors.New("router requires serving, warehouse, operator, protect, feedback and its guard, request IDs, a recorder and the live stream")
 	}
 	mux := http.NewServeMux()
 	mux.HandleFunc("/healthz", health)
@@ -44,6 +46,7 @@ func NewRouter(routes Routes) (http.Handler, error) {
 	// Only the gateway's expiration scheduler starts runs; the browser never reaches the route.
 	mux.Handle("/api/warehouse/", hide("/api/warehouse/expiration-runs",
 		business("/api/warehouse", routes.Warehouse, routes.Protect, routes.Record)))
+	mux.Handle("/api/live", routes.Live)
 	// Only the exact path: other paths of the feedback service are never public.
 	mux.Handle("/api/feedback", routes.GuardFeedback(toFeedback(routes.Feedback)))
 	return WithRequestID(mux, routes.NewRequestID), nil
```

Создайте `internal/live/clock.go`:

```go
package live

import "time"

// Clock gives streams their time; tests replace it with a fake one.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer delivers one time on C after its duration unless it is stopped first.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// SystemClock is the process clock.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

func (SystemClock) NewTimer(d time.Duration) Timer { return systemTimer{timer: time.NewTimer(d)} }

type systemTimer struct{ timer *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.timer.C }

func (t systemTimer) Stop() bool { return t.timer.Stop() }
```

Создайте `internal/live/sessions.go`:

```go
package live

import (
	"sync"
	"time"

	"github.com/JustIkra/umir-gateway/internal/operator"
)

// SessionEvents passes committed session changes from the operator service to the streams of that
// session. It implements operator.SessionObserver and never blocks the service.
type SessionEvents struct {
	mu       sync.Mutex
	watchers map[string]map[*watcher]struct{}
}

// NewSessionEvents starts without watchers.
func NewSessionEvents() *SessionEvents {
	return &SessionEvents{watchers: map[string]map[*watcher]struct{}{}}
}

// update is the newest change of one session: either its new deadlines or its end.
type update struct {
	ended          operator.EndReason
	idle, absolute time.Time
}

// watcher keeps only the newest update of its session; an end is never replaced by an extension.
type watcher struct {
	mu      sync.Mutex
	pending *update
	ready   chan struct{}
}

func (w *watcher) offer(u update) {
	w.mu.Lock()
	if w.pending == nil || w.pending.ended == "" {
		w.pending = &u
	}
	w.mu.Unlock()
	select {
	case w.ready <- struct{}{}:
	default:
	}
}

// take returns the pending update and clears it; ok is false when another take got it first.
func (w *watcher) take() (update, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == nil {
		return update{}, false
	}
	u := *w.pending
	w.pending = nil
	return u, true
}

// watch registers a watcher of the session with key until stop.
func (e *SessionEvents) watch(key string) (*watcher, func()) {
	w := &watcher{ready: make(chan struct{}, 1)}
	e.mu.Lock()
	if e.watchers[key] == nil {
		e.watchers[key] = map[*watcher]struct{}{}
	}
	e.watchers[key][w] = struct{}{}
	e.mu.Unlock()
	return w, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.watchers[key], w)
		if len(e.watchers[key]) == 0 {
			delete(e.watchers, key)
		}
	}
}

func (e *SessionEvents) deliver(key string, u update) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for w := range e.watchers[key] {
		w.offer(u)
	}
}

// SessionExtended passes the new deadlines to the streams of the session.
func (e *SessionEvents) SessionExtended(key string, idleExpiresAt, absoluteExpiresAt time.Time) {
	e.deliver(key, update{idle: idleExpiresAt, absolute: absoluteExpiresAt})
}

// SessionEnded tells the streams of the session to end.
func (e *SessionEvents) SessionEnded(key string, reason operator.EndReason) {
	e.deliver(key, update{ended: reason})
}
```

Создайте `internal/live/streams.go`:

```go
// Package live serves GET /api/live: a server-sent event stream that tells the operator's screens when
// state may have changed, the minute ticks of the gateway clock, the session deadlines and whether the
// warehouse answers. It carries signals only; the screens re-read their data with ordinary requests.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/JustIkra/umir-gateway/internal/httpapi"
	"github.com/JustIkra/umir-gateway/internal/operator"
)

// Defaults of Options.
const (
	DefaultPerSession   = 8
	DefaultTotal        = 64
	DefaultPing         = 15 * time.Second
	DefaultWriteTimeout = 5 * time.Second
)

// Changes is the journal as the streams see it.
type Changes interface {
	// Last returns the epoch and the seq of the newest entry, 0 while the journal is empty.
	Last() (epoch string, seq int64)
	// Subscribe signals after entries that may have changed state until ctx ends.
	Subscribe(ctx context.Context) <-chan struct{}
}

// Sessions checks a session without extending it.
type Sessions interface {
	Peek(id string) (operator.Profile, operator.Session, error)
}

// Options are the streams' dependencies and bounds; every field is required.
type Options struct {
	Changes  Changes
	Upstream *Upstream
	Sessions Sessions
	Events   *SessionEvents
	Clock    Clock
	// PerSession and Total bound the open streams of one session and of the process.
	PerSession, Total int
	// Ping paces the keep-alive comments; WriteTimeout bounds every write to a client.
	Ping, WriteTimeout time.Duration
}

// Streams serves the live streams of one gateway process.
type Streams struct {
	opts Options
	// closing ends every stream when the server shuts down.
	closing context.Context
	close   context.CancelFunc

	mu    sync.Mutex
	open  map[string]int
	total int
}

// NewStreams checks opts.
func NewStreams(opts Options) (*Streams, error) {
	if opts.Changes == nil || opts.Upstream == nil || opts.Sessions == nil || opts.Events == nil || opts.Clock == nil ||
		opts.PerSession <= 0 || opts.Total <= 0 || opts.Ping <= 0 || opts.WriteTimeout <= 0 {
		return nil, errors.New("live: changes, upstream, sessions, events, clock and positive bounds are required")
	}
	closing, cancel := context.WithCancel(context.Background())
	return &Streams{opts: opts, closing: closing, close: cancel, open: map[string]int{}}, nil
}

// Close ends every open stream at once; register it with http.Server.RegisterOnShutdown, because
// Shutdown waits for idle connections and a stream never becomes idle.
func (s *Streams) Close() { s.close() }

// Serve streams to the operator whose session id GuardLive admitted as session.
func (s *Streams) Serve(w http.ResponseWriter, r *http.Request, id string, _ operator.Session) {
	key := operator.SessionKey(id)
	if !s.acquire(key) {
		httpapi.WriteProblem(w, r, http.StatusTooManyRequests, httpapi.Problem{Code: "TooManyStreams", Message: "too many live streams"})
		return
	}
	defer s.release(key)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(s.closing, cancel)()
	// Every source is watched before the first frames, so no change between them is lost. The session is
	// checked again after the watch began: a sign-out right after the guard would otherwise go unheard.
	changes := s.opts.Changes.Subscribe(ctx)
	upstream := s.opts.Upstream.Subscribe(ctx)
	sessionChanges, stopWatching := s.opts.Events.watch(key)
	defer stopWatching()
	_, session, err := s.opts.Sessions.Peek(id)
	if err != nil {
		refuse(w, r, err)
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &sender{w: w, controller: http.NewResponseController(w), timeout: s.opts.WriteTimeout}
	defer func() {
		// A connection the client keeps must not inherit the last write deadline. Once the client is gone
		// or the server stops, there is nothing to keep.
		if err := out.release(); err != nil && ctx.Err() == nil {
			log.Printf("live stream %s: clear write deadline: %v", httpapi.RequestID(r), err)
		}
	}()

	clock := s.opts.Clock
	if !out.send(raw("retry: 3000\n\n"), s.change(), tick(clock.Now()), sessionFrame(session.IdleExpiresAt, session.AbsoluteExpiresAt), s.upstream()) {
		return
	}
	expiry := clock.NewTimer(until(clock, earlier(session.IdleExpiresAt, session.AbsoluteExpiresAt)))
	minute := clock.NewTimer(untilNextMinute(clock.Now()))
	ping := clock.NewTimer(s.opts.Ping)
	defer func() {
		expiry.Stop()
		minute.Stop()
		ping.Stop()
	}()
	for {
		var next frame
		select {
		case <-ctx.Done():
			return
		case <-changes:
			next = s.change()
		case <-upstream:
			next = s.upstream()
		case <-sessionChanges.ready:
			change, ok := sessionChanges.take()
			if !ok {
				continue
			}
			if change.ended != "" {
				out.send(ended(change.ended))
				return
			}
			next = sessionFrame(change.idle, change.absolute)
			expiry.Stop()
			expiry = clock.NewTimer(until(clock, earlier(change.idle, change.absolute)))
		case <-expiry.C():
			_, current, err := s.opts.Sessions.Peek(id)
			if errors.Is(err, operator.ErrUnauthenticated) {
				out.send(ended(operator.EndExpired))
				return
			}
			// During account recovery nothing is known; the deadline is checked again a second later. A
			// session that is still valid moved its deadline without an event, as a remembered one never
			// reports it.
			wait := time.Second
			if err == nil {
				next = sessionFrame(current.IdleExpiresAt, current.AbsoluteExpiresAt)
				wait = max(until(clock, earlier(current.IdleExpiresAt, current.AbsoluteExpiresAt)), time.Second)
			}
			expiry = clock.NewTimer(wait)
			if err != nil {
				continue
			}
		case <-minute.C():
			next = tick(clock.Now())
			minute = clock.NewTimer(untilNextMinute(clock.Now()))
		case <-ping.C():
			next = raw(": ping\n\n")
			ping = clock.NewTimer(s.opts.Ping)
		}
		if !out.send(next) {
			return
		}
	}
}

func (s *Streams) acquire(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total >= s.opts.Total || s.open[key] >= s.opts.PerSession {
		return false
	}
	s.total++
	s.open[key]++
	return true
}

func (s *Streams) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total--
	if s.open[key]--; s.open[key] == 0 {
		delete(s.open, key)
	}
}

func (s *Streams) change() frame {
	epoch, seq := s.opts.Changes.Last()
	return event("change", struct {
		Epoch string `json:"epoch"`
		Seq   string `json:"seq"`
	}{epoch, strconv.FormatInt(seq, 10)})
}

func (s *Streams) upstream() frame {
	return event("upstream", struct {
		Available bool `json:"available"`
	}{s.opts.Upstream.Available()})
}

// refuse answers a session that ended between the guard and the start of the stream, with the codes
// the account routes use.
func refuse(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, operator.ErrUnauthenticated) {
		httpapi.WriteProblem(w, r, http.StatusUnauthorized, httpapi.Problem{Code: "Unauthenticated", Message: "sign in to continue"})
		return
	}
	httpapi.WriteProblem(w, r, http.StatusServiceUnavailable, httpapi.Problem{Code: "AuthUnavailable", Message: "authentication is temporarily unavailable"})
}

var moscow = time.FixedZone("MSK", 3*60*60)

func timestamp(t time.Time) string { return t.In(moscow).Format(time.RFC3339) }

func tick(now time.Time) frame {
	return event("tick", struct {
		ServerTime string `json:"serverTime"`
	}{timestamp(now)})
}

func sessionFrame(idle, absolute time.Time) frame {
	return event("session", struct {
		IdleExpiresAt     string `json:"idleExpiresAt"`
		AbsoluteExpiresAt string `json:"absoluteExpiresAt"`
	}{timestamp(idle), timestamp(absolute)})
}

func ended(reason operator.EndReason) frame {
	return event("session-ended", struct {
		Reason operator.EndReason `json:"reason"`
	}{reason})
}

// frame is one event with data encoded as JSON on write, or raw text such as a comment.
type frame struct {
	name string
	data any
	raw  string
}

func event(name string, data any) frame { return frame{name: name, data: data} }

func raw(text string) frame { return frame{raw: text} }

func (f frame) encode() (string, error) {
	if f.name == "" {
		return f.raw, nil
	}
	data, err := json.Marshal(f.data)
	if err != nil {
		return "", err
	}
	return "event: " + f.name + "\ndata: " + string(data) + "\n\n", nil
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func until(clock Clock, t time.Time) time.Duration { return t.Sub(clock.Now()) }

func untilNextMinute(now time.Time) time.Duration {
	return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
}

// sender writes frames to one client; a client that cannot take a write within timeout is dropped.
type sender struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

// send writes frames and flushes them; false means the stream must end.
func (s *sender) send(frames ...frame) bool {
	if err := s.controller.SetWriteDeadline(time.Now().Add(s.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	for _, f := range frames {
		text, err := f.encode()
		if err != nil {
			return false
		}
		if _, err := io.WriteString(s.w, text); err != nil {
			return false
		}
	}
	return s.controller.Flush() == nil
}

// release clears the write deadline.
func (s *sender) release() error {
	if err := s.controller.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}
```

Создайте `internal/live/upstream.go`:

```go
package live

import (
	"context"
	"sync"
)

// Upstream is whether the warehouse answers the expiration runs. It starts available; the scheduler
// changes it and every stream reports the change.
type Upstream struct {
	mu          sync.Mutex
	available   bool
	subscribers map[chan struct{}]struct{}
}

// NewUpstream starts available.
func NewUpstream() *Upstream {
	return &Upstream{available: true, subscribers: map[chan struct{}]struct{}{}}
}

// Available returns the current value.
func (u *Upstream) Available() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.available
}

// Set changes the value and wakes the subscribers when it differs. It never blocks.
func (u *Upstream) Set(available bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.available == available {
		return
	}
	u.available = available
	for ch := range u.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel that holds one signal after any number of changes. The subscription
// ends with ctx; the channel is never closed.
func (u *Upstream) Subscribe(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	u.mu.Lock()
	u.subscribers[ch] = struct{}{}
	u.mu.Unlock()
	context.AfterFunc(ctx, func() {
		u.mu.Lock()
		defer u.mu.Unlock()
		delete(u.subscribers, ch)
	})
	return ch
}
```

Создайте `internal/operatorhttp/live.go`:

```go
package operatorhttp

import (
	"net/http"

	"github.com/JustIkra/umir-gateway/internal/operator"
)

// GuardLive admits GET /api/live with a live session that Peek checks, so opening a stream never extends
// it, and with Fetch Metadata, when the browser sends it, from the same origin. Nothing reaches next
// otherwise; next gets the session ID and the session as Peek saw it.
func (a *API) GuardLive(next func(w http.ResponseWriter, r *http.Request, id string, session operator.Session)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			a.problem(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "use GET")
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
			a.problem(w, r, http.StatusForbidden, "InvalidOrigin", "request origin is not allowed")
			return
		}
		id := a.cookie(r, a.cookies.session)
		_, session, err := a.accounts.Peek(id)
		if err != nil {
			a.fail(w, r, err, "")
			return
		}
		next(w, r, id, session)
	})
}
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add .go-arch-lint.yml cmd/server/auth_test.go cmd/server/live_test.go cmd/server/main.go cmd/server/operator_test.go internal/httpapi/router.go internal/httpapi/router_test.go internal/live/clock.go internal/live/sessions.go internal/live/streams.go internal/live/streams_test.go internal/live/upstream.go internal/operatorhttp/live.go internal/operatorhttp/live_test.go
git commit -m "feat(live): stream change signals, ticks and session events on GET /api/live"
```


### Task 7: Запуски проверки сроков `internal/expiration`

**Files:**
- Modify: `.go-arch-lint.yml`
- Modify: `cmd/server/main.go`
- Create: `internal/expiration/runner.go`
- Test: `internal/expiration/runner_test.go`

**Interfaces:**
- Consumes: `journal.Entry`, `(*journal.Journal).Append` и `LastRequest` (Task 1); `(*live.Upstream).Set` (Task 6); `config.Config.ExpirationRunInterval`, `UpstreamTimeout`, `WarehouseURL`.
- Produces (пакет `expiration`):
  - `type Journal interface { Append(journal.Entry) int64; LastRequest() int64 }`;
  - `type Upstream interface { Set(available bool) }`;
  - `type Options struct { Client *http.Client; Warehouse *url.URL; Interval, Timeout time.Duration; Journal Journal; Upstream Upstream; NewTicker func(time.Duration) (<-chan time.Time, func()); Logger *log.Logger }`;
  - `New(Options) (*Runner, error)`, `(*Runner).Run(ctx context.Context)`.
- Produces в `cmd/server`: `components.expiry`, `newTicker`; `main` запускает `Run` в горутине и ждёт её после `run`.

В журнал при неудачах попадает одна запись `unknown` на серию неудач. Логируются первая неудача, переход к `upstream: false` и восстановление. Поэтому недоступный склад не будит экраны и не пишет в журнал каждую секунду.

- [ ] **Step 1: Write the failing tests**

Создайте `internal/expiration/runner_test.go`:

```go
package expiration

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustIkra/umir-gateway/internal/journal"
)

// upstreamState records what the runner reports about the warehouse.
type upstreamState struct {
	mu     sync.Mutex
	values []bool
}

func (u *upstreamState) Set(available bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.values) == 0 || u.values[len(u.values)-1] != available {
		u.values = append(u.values, available)
	}
}

func (u *upstreamState) changes() []bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]bool(nil), u.values...)
}

// warehouse answers each run with the next of answers; the last one repeats.
type warehouse struct {
	mu      sync.Mutex
	answers []func(w http.ResponseWriter)
	runs    int
}

func (s *warehouse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) != 0 || r.Method != http.MethodPost || r.URL.Path != "/base/expiration-runs" ||
		r.URL.RawQuery != "" || r.Header.Get("Accept") != "application/json" {
		w.WriteHeader(http.StatusTeapot)
		return
	}
	s.mu.Lock()
	answer := s.answers[min(s.runs, len(s.answers)-1)]
	s.runs++
	s.mu.Unlock()
	answer(w)
}

func answer(status int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			return
		}
	}
}

func run(version string, expired int) func(w http.ResponseWriter) {
	list := ""
	for i := range expired {
		if i > 0 {
			list += ","
		}
		list += `{"batchID":"b","productID":"p","writtenOffQuantity":"1000","reason":"EXPIRATION","expiresAt":"2026-10-12T14:00:00+03:00","processedAt":"2026-10-12T14:00:01+03:00"}`
	}
	return answer(http.StatusOK, `{"evaluatedAt":"2026-10-12T14:00:01+03:00","stateVersion":"`+version+`","expired":[`+list+`]}`)
}

type harness struct {
	runner   *Runner
	journal  *journal.Journal
	upstream *upstreamState
}

func newHarness(t *testing.T, address string, timeout time.Duration) *harness {
	t.Helper()
	j, err := journal.New(16, func() string { return "epoch-1" }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(address + "/base")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{journal: j, upstream: &upstreamState{}}
	h.runner, err = New(Options{
		Client: &http.Client{}, Warehouse: base, Interval: time.Second, Timeout: timeout, Journal: j, Upstream: h.upstream,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} },
		Logger:    log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serve(t *testing.T, w *warehouse) string {
	t.Helper()
	server := httptest.NewServer(w)
	t.Cleanup(server.Close)
	return server.URL
}

func (h *harness) entries() []journal.Entry { return h.journal.Entries(0) }

func TestRunsWithoutWriteOffsLeaveNoEntry(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){run("7", 0)}}), time.Second)
	h.runner.runOnce(context.Background())
	h.runner.runOnce(context.Background())
	if got := h.entries(); len(got) != 0 {
		t.Fatalf("entries: %+v", got)
	}
	if got := h.upstream.changes(); len(got) != 1 || !got[0] {
		t.Fatalf("upstream: %v", got)
	}
}

func TestWriteOffsMakeAnExpirationEntry(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){run("9", 2)}}), time.Second)
	h.runner.runOnce(context.Background())
	got := h.entries()
	if len(got) != 1 || got[0].Source != journal.SourceExpiration || got[0].Outcome != journal.OutcomeSuccess ||
		got[0].Count != 2 || got[0].Status != http.StatusOK || got[0].Method != "" || got[0].Path != "" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestAStateChangeOutsideTheJournalMakesAnExternalEntry(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){run("5", 0), run("6", 0), run("8", 0), run("8", 0)}}), time.Second)
	h.runner.runOnce(context.Background()) // the first run only learns the version
	h.runner.runOnce(context.Background()) // 5 → 6 without a request: external
	h.journal.Append(journal.Entry{Source: journal.SourceRequest, Method: http.MethodPost, Path: "/api/warehouse/deliveries", Status: 201, Outcome: journal.OutcomeSuccess})
	h.runner.runOnce(context.Background()) // 6 → 8 after a request: already signalled
	h.runner.runOnce(context.Background()) // unchanged
	got := h.entries()
	if len(got) != 2 || got[0].Source != journal.SourceExternal || got[0].Outcome != journal.OutcomeUnknown || got[1].Source != journal.SourceRequest {
		t.Fatalf("entries: %+v", got)
	}
}

func TestAWarehouseRestartIsAnExternalChange(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){run("40", 0), run("0", 0)}}), time.Second)
	h.runner.runOnce(context.Background())
	h.runner.runOnce(context.Background())
	if got := h.entries(); len(got) != 1 || got[0].Source != journal.SourceExternal {
		t.Fatalf("entries: %+v", got)
	}
}

func TestFailingRunsSignalOnceAndMarkTheWarehouseUnavailable(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){
		answer(http.StatusServiceUnavailable, `{"code":"InventoryUnavailable"}`),
		answer(http.StatusServiceUnavailable, `{"code":"InventoryUnavailable"}`),
		answer(http.StatusServiceUnavailable, `{"code":"InventoryUnavailable"}`),
		run("3", 0),
	}}), time.Second)
	for range 3 {
		h.runner.runOnce(context.Background())
	}
	got := h.entries()
	if len(got) != 1 || got[0].Source != journal.SourceExpiration || got[0].Outcome != journal.OutcomeUnknown || got[0].Status != http.StatusServiceUnavailable {
		t.Fatalf("after three 503: %+v", got)
	}
	if changes := h.upstream.changes(); len(changes) != 1 || changes[0] {
		t.Fatalf("upstream after three failures: %v", changes)
	}
	h.runner.runOnce(context.Background())
	if changes := h.upstream.changes(); len(changes) != 2 || !changes[1] {
		t.Fatalf("upstream after recovery: %v", changes)
	}
}

func TestRejectedRunsLeaveNoEntry(t *testing.T) {
	h := newHarness(t, serve(t, &warehouse{answers: []func(http.ResponseWriter){answer(http.StatusBadRequest, `{"code":"InvalidArgument"}`)}}), time.Second)
	h.runner.runOnce(context.Background())
	if got := h.entries(); len(got) != 0 {
		t.Fatalf("entries: %+v", got)
	}
}

func TestAnUnreachableWarehouseLeavesNoEntry(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	h := newHarness(t, closed.URL, time.Second)
	for range 3 {
		h.runner.runOnce(context.Background())
	}
	if got := h.entries(); len(got) != 0 {
		t.Fatalf("entries: %+v", got)
	}
	if changes := h.upstream.changes(); len(changes) != 1 || changes[0] {
		t.Fatalf("upstream: %v", changes)
	}
}

func TestATimedOutRunIsUnknown(t *testing.T) {
	release := make(chan struct{})
	address := serve(t, &warehouse{answers: []func(http.ResponseWriter){func(http.ResponseWriter) { <-release }}})
	// Registered after serve, so it runs first: the server waits for the held handler when it closes.
	t.Cleanup(func() { close(release) })
	h := newHarness(t, address, 50*time.Millisecond)
	h.runner.runOnce(context.Background())
	if got := h.entries(); len(got) != 1 || got[0].Outcome != journal.OutcomeUnknown || got[0].Status != 0 {
		t.Fatalf("entries: %+v", got)
	}
}

func TestRunsNeverOverlapAndStopWithTheContext(t *testing.T) {
	var inFlight, most atomic.Int32
	release := make(chan struct{})
	slow := &warehouse{answers: []func(http.ResponseWriter){func(w http.ResponseWriter) {
		now := inFlight.Add(1)
		for {
			seen := most.Load()
			if now <= seen || most.CompareAndSwap(seen, now) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		run("1", 0)(w)
	}}}
	h := newHarness(t, serve(t, slow), 5*time.Second)
	ticks := make(chan time.Time, 3)
	h.runner.opts.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runner.Run(ctx)
	}()
	ticks <- time.Now()
	ticks <- time.Now()
	time.Sleep(100 * time.Millisecond)
	close(release)
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	if most.Load() != 1 {
		t.Fatalf("%d runs at once", most.Load())
	}
}

func TestNewRequiresEveryOption(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty options accepted")
	}
}
```

- [ ] **Step 2: Run them and see them fail**

Run: `GOWORK=off go test -count=1 ./internal/expiration`
Expected: FAIL, сборка: `undefined: Runner`, `undefined: New`, `undefined: Options`.

- [ ] **Step 3: Implement**

Измените `.go-arch-lint.yml`:

```diff
--- a/.go-arch-lint.yml
+++ b/.go-arch-lint.yml
@@ -21,6 +21,7 @@ components:
   strictjson: {in: "internal/strictjson/**"}
   journal: {in: "internal/journal/**"}
   live: {in: "internal/live/**"}
+  expiration: {in: "internal/expiration/**"}
 vendors:
   x-text-norm: {in: "golang.org/x/text/unicode/norm"}
   x-crypto-argon2: {in: "golang.org/x/crypto/argon2"}
@@ -39,7 +40,7 @@ deps:
   bootstrap:
     mayDependOn: [bootstrap, transport, proxy]
   process:
-    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp, journal, live]
+    mayDependOn: [process, config, httpapi, upstream, bootstrap, transport, proxy, operator, passwordhash, accountfile, operatorhttp, journal, live, expiration]
     canUse: [x-term]
   testsupport:
     mayDependOn: [testsupport]
@@ -61,3 +62,5 @@ deps:
     mayDependOn: [journal]
   live:
     mayDependOn: [live, operator, httpapi]
+  expiration:
+    mayDependOn: [expiration, journal]
```

Измените `cmd/server/main.go`:

```diff
--- a/cmd/server/main.go
+++ b/cmd/server/main.go
@@ -19,6 +19,7 @@ import (
 	"github.com/JustIkra/umir-gateway/internal/adapters/argon2id"
 	"github.com/JustIkra/umir-gateway/internal/adapters/httpupstream"
 	"github.com/JustIkra/umir-gateway/internal/config"
+	"github.com/JustIkra/umir-gateway/internal/expiration"
 	"github.com/JustIkra/umir-gateway/internal/httpapi"
 	"github.com/JustIkra/umir-gateway/internal/journal"
 	"github.com/JustIkra/umir-gateway/internal/live"
@@ -62,15 +63,17 @@ func newAccounts(cfg config.Operator, observer operator.SessionObserver) (*opera
 	})
 }
 
-// components are the parts of one gateway process: the HTTP handler, the change journal and the live
-// streams.
+// components are the parts of one gateway process: the HTTP handler, the change journal, the live
+// streams and the expiration runs.
 type components struct {
 	handler http.Handler
 	journal *journal.Journal
 	streams *live.Streams
+	expiry  *expiration.Runner
 }
 
-// newComponents assembles the gateway from cfg. Nothing runs yet: the process serves handler.
+// newComponents assembles the gateway from cfg. Nothing runs yet: the process serves handler and
+// starts expiry itself.
 func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
 	events := live.NewSessionEvents()
 	accounts, err := newAccounts(cfg.Operator, events)
@@ -81,8 +84,9 @@ func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
 	if err != nil {
 		return components{}, err
 	}
+	upstream := live.NewUpstream()
 	streams, err := live.NewStreams(live.Options{
-		Changes: changes, Upstream: live.NewUpstream(), Sessions: accounts, Events: events, Clock: live.SystemClock{},
+		Changes: changes, Upstream: upstream, Sessions: accounts, Events: events, Clock: live.SystemClock{},
 		PerSession: live.DefaultPerSession, Total: live.DefaultTotal, Ping: live.DefaultPing, WriteTimeout: live.DefaultWriteTimeout,
 	})
 	if err != nil {
@@ -94,7 +98,19 @@ func newComponents(cfg config.Config, logger *log.Logger) (components, error) {
 	if err != nil {
 		return components{}, err
 	}
-	return components{handler: handler, journal: changes, streams: streams}, nil
+	expiry, err := expiration.New(expiration.Options{
+		Client: &http.Client{Transport: transport}, Warehouse: cfg.WarehouseURL, Interval: cfg.ExpirationRunInterval,
+		Timeout: cfg.UpstreamTimeout, Journal: changes, Upstream: upstream, NewTicker: newTicker, Logger: logger,
+	})
+	if err != nil {
+		return components{}, err
+	}
+	return components{handler: handler, journal: changes, streams: streams, expiry: expiry}, nil
+}
+
+func newTicker(d time.Duration) (<-chan time.Time, func()) {
+	ticker := time.NewTicker(d)
+	return ticker.C, ticker.Stop
 }
 
 // newHandler assembles the gateway: one proxy per upstream behind the operator guard and the router.
@@ -273,7 +289,16 @@ func main() {
 		log.Fatal(err)
 	}
 	log.Printf("umir-gateway listening on %s", server.Addr)
-	if err := run(signals, server, listener, shutdownGrace); err != nil {
+	// The process owns the expiration runs: they stop with the signal and finish before it exits.
+	runs := make(chan struct{})
+	go func() {
+		defer close(runs)
+		app.expiry.Run(signals)
+	}()
+	err = run(signals, server, listener, shutdownGrace)
+	stop()
+	<-runs
+	if err != nil {
 		log.Fatal(err)
 	}
 }
```

Создайте `internal/expiration/runner.go`:

```go
// Package expiration starts the warehouse's expiration runs, so write-offs by expiry pass through the
// gateway and its journal like every other change. The warehouse still decides what has expired.
package expiration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/JustIkra/umir-gateway/internal/journal"
)

// failuresBeforeUnavailable failed runs in a row make the warehouse unavailable for the streams.
const failuresBeforeUnavailable = 3

// maxBody bounds the run response the gateway reads.
const maxBody = 1 << 20

// Journal is where the runs leave their entries.
type Journal interface {
	Append(journal.Entry) int64
	// LastRequest is the seq of the newest request entry, 0 before the first.
	LastRequest() int64
}

// Upstream learns whether the warehouse answers.
type Upstream interface {
	Set(available bool)
}

// Options are the runner's dependencies; every field is required.
type Options struct {
	Client *http.Client
	// Warehouse is the public warehouse address; the runs go to its /expiration-runs.
	Warehouse *url.URL
	Interval  time.Duration
	// Timeout bounds one run, as UPSTREAM_TIMEOUT bounds a proxied request.
	Timeout  time.Duration
	Journal  Journal
	Upstream Upstream
	// NewTicker paces the runs; tests replace it.
	NewTicker func(time.Duration) (<-chan time.Time, func())
	Logger    *log.Logger
}

// Runner starts one run at a time. It is owned by the process, which runs Run until shutdown.
type Runner struct {
	opts   Options
	target string

	// The fields below belong to the Run goroutine.
	failures int
	// reported is set after a failed run that may have changed state left its entry; later failures in
	// the same row add none, so an unreachable warehouse does not signal every second.
	reported bool
	// version and requests are the warehouse state version and LastRequest after the previous successful
	// run; known is false before it.
	version  int64
	requests int64
	known    bool
}

// New checks opts.
func New(opts Options) (*Runner, error) {
	if opts.Client == nil || opts.Warehouse == nil || opts.Interval <= 0 || opts.Timeout <= 0 || opts.Journal == nil ||
		opts.Upstream == nil || opts.NewTicker == nil || opts.Logger == nil {
		return nil, errors.New("expiration: client, warehouse, positive interval and timeout, journal, upstream, ticker and logger are required")
	}
	return &Runner{opts: opts, target: opts.Warehouse.JoinPath("expiration-runs").String()}, nil
}

// Run starts a run on every tick until ctx ends. A slow run delays the next one; runs never overlap.
func (r *Runner) Run(ctx context.Context) {
	ticks, stop := r.opts.NewTicker(r.opts.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			r.runOnce(ctx)
		}
	}
}

// runResult is the part of the warehouse answer the gateway reads.
type runResult struct {
	StateVersion string            `json:"stateVersion"`
	Expired      []json.RawMessage `json:"expired"`
}

func (r *Runner) runOnce(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, r.opts.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.target, nil)
	if err != nil {
		r.failed(0, false, err)
		return
	}
	request.Header.Set("Accept", "application/json")
	response, err := r.opts.Client.Do(request)
	if err != nil {
		if parent.Err() != nil {
			return
		}
		// A refused connection sent nothing; any later failure may follow a recorded run.
		r.failed(0, !dialFailed(err), err)
		return
	}
	result, err := read(response)
	err = errors.Join(err, response.Body.Close())
	switch {
	case err != nil:
		r.failed(response.StatusCode, true, err)
	case response.StatusCode != http.StatusOK:
		r.failed(response.StatusCode, response.StatusCode >= http.StatusInternalServerError, fmt.Errorf("status %d", response.StatusCode))
	default:
		r.succeeded(result)
	}
}

// read decodes a 200 answer and drains any other, so the connection can be reused.
func read(response *http.Response) (runResult, error) {
	body := io.LimitReader(response.Body, maxBody)
	if response.StatusCode != http.StatusOK {
		_, err := io.Copy(io.Discard, body)
		return runResult{}, err
	}
	var result runResult
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return runResult{}, fmt.Errorf("decode run: %w", err)
	}
	return result, nil
}

func (r *Runner) succeeded(result runResult) {
	version, err := strconv.ParseInt(result.StateVersion, 10, 64)
	if err != nil || version < 0 {
		r.failed(http.StatusOK, true, fmt.Errorf("stateVersion %q", result.StateVersion))
		return
	}
	requests := r.opts.Journal.LastRequest()
	switch {
	case len(result.Expired) > 0:
		r.opts.Journal.Append(journal.Entry{
			Source: journal.SourceExpiration, Status: http.StatusOK, Outcome: journal.OutcomeSuccess, Count: len(result.Expired),
		})
	case r.known && version != r.version && requests == r.requests:
		// The state moved, yet no request passed the gateway since the previous run: a fresh warehouse
		// operation wrote off an expired batch inside a read, or the warehouse restarted.
		r.opts.Journal.Append(journal.Entry{Source: journal.SourceExternal, Outcome: journal.OutcomeUnknown})
	}
	if r.failures >= failuresBeforeUnavailable {
		r.opts.Logger.Printf("expiration runs recovered after %d failures", r.failures)
	}
	r.version, r.requests, r.known = version, requests, true
	r.failures, r.reported = 0, false
	r.opts.Upstream.Set(true)
}

// failed counts a run without a usable answer. status is 0 without a response; mayHaveChanged says
// whether the warehouse may have recorded the run.
func (r *Runner) failed(status int, mayHaveChanged bool, cause error) {
	r.failures++
	if mayHaveChanged && !r.reported {
		r.opts.Journal.Append(journal.Entry{Source: journal.SourceExpiration, Status: status, Outcome: journal.OutcomeUnknown})
		r.reported = true
	}
	// The first failure and the switch to unavailable are logged; an outage does not log every second.
	switch r.failures {
	case 1:
		r.opts.Logger.Printf("expiration run failed: %v", cause)
	case failuresBeforeUnavailable:
		r.opts.Upstream.Set(false)
		r.opts.Logger.Printf("expiration runs failed %d times in a row; warehouse marked unavailable: %v", r.failures, cause)
	}
}

// dialFailed reports a request that never reached the warehouse.
func dialFailed(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
```

- [ ] **Step 4: Run the checks and see them pass**

Run: `make check`
Expected: PASS — formatting без различий, `0 issues.`, `OK - No warnings found`, все пакеты `ok` с `-race`.

- [ ] **Step 5: Commit**

```bash
git add .go-arch-lint.yml cmd/server/main.go internal/expiration/runner.go internal/expiration/runner_test.go
git commit -m "feat(expiration): start the warehouse expiration runs from the gateway"
```


### Task 8: README и CHANGELOG

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: поведение Task 1–7.
- Produces: раздел README «Реальное время», строки маршрутов и пакетов, настройки `JOURNAL_CAPACITY` и `EXPIRATION_RUN_INTERVAL`, правило фоновых запросов, три пункта в `[Unreleased]`.

- [ ] **Step 1: Write the change**

Измените `CHANGELOG.md`:

```diff
--- a/CHANGELOG.md
+++ b/CHANGELOG.md
@@ -2,6 +2,10 @@
 
 ## [Unreleased]
 
+- Реальное время для `/app`. Журнал изменений в памяти (`JOURNAL_CAPACITY`, по умолчанию 1000) записывает допущенные изменяющие бизнес-запросы, смену имени оператора и запуски проверки сроков: метод, путь, статус и итог по статусу, без тел и данных запроса. Поток SSE `GET /api/live` передаёт сигналы `change`, минутные `tick`, сроки `session`, `session-ended` и доступность склада `upstream`; он не продлевает сессию, ограничен 8 потоками на сессию и 64 на процесс (`429 TooManyStreams`) и закрывается при остановке.
+- Заголовок `X-Umir-Activity: background` у бизнес-запросов и `GET /api/operator/me`: те же проверки без продления сессии и без записи файла учётной записи. Заголовок не уходит в upstream.
+- Проверку сроков запускает gateway (gateway#19): `POST /expiration-runs` склада раз в `EXPIRATION_RUN_INTERVAL` (по умолчанию `1s`), без наложения запусков, с бюджетом `UPSTREAM_TIMEOUT`. Браузер получает `404` на `/api/warehouse/expiration-runs`. Требует склада с этим маршрутом.
+
 - Persist ordinary business-session activity before dispatch, so restart preserves the renewed idle deadline. Known or unknown storage failures do not dispatch the business request. Reject account/session aggregates above 32 records and session limits above that bound.
 - Align the default private feedback URL with the service listener at port 8084.
```

Измените `README.md`:

```diff
--- a/README.md
+++ b/README.md
@@ -4,7 +4,7 @@
 
 ## Текущее состояние
 
-Работают `GET /healthz`, проверка контейнера `/server healthcheck`, HTTP-прокси и [личный кабинет оператора](#личный-кабинет-оператора).
+Работают `GET /healthz`, проверка контейнера `/server healthcheck`, HTTP-прокси, [личный кабинет оператора](#личный-кабинет-оператора) и [поток изменений `GET /api/live`](#реальное-время).
 Бизнес-маршруты требуют сессию оператора. Без сессии доступны только `GET` и `HEAD` для `/healthz`, `/api/serving/healthz` и `/api/warehouse/healthz`, а также публичная [форма обратной связи](#форма-обратной-связи) `POST /api/feedback`.
 Gateway не содержит бизнес-правил: он не создаёт `commandID`, не проверяет тела и не повторяет запросы.
 
@@ -15,6 +15,8 @@ Gateway не содержит бизнес-правил: он не создаё
 | `/api/serving/<path>` | `serving:8080/<path>` |
 | `/api/warehouse/<path>` | `warehouse:8080/<path>` |
 | `POST /api/feedback` | `POST feedback:8084/feedback`, без query |
+| `GET /api/live` | не уходит в upstream: поток SSE самого gateway |
+| `/api/warehouse/expiration-runs` | не уходит в upstream: браузер получает `404 NotFound`, маршрут вызывает только планировщик gateway |
 
 Префиксы `/api/serving` и `/api/warehouse` снимаются при передаче запроса. Метод, остальной путь, query, тело, end-to-end заголовки, статус и тело ответа upstream передаются без изменений; hop-by-hop заголовки удаляются, `X-Forwarded-*` выставляет gateway. Исходный RawQuery сохраняется, включая ошибочный `%` или `;`, чтобы HTTP-адаптер сервиса мог отклонить неверные параметры. Публичные бизнес-endpoints принадлежат соответствующим сервисам. `WAREHOUSE_URL` указывает на публичный listener склада; внутренний listener `8082` доступен только serving.
 
@@ -24,9 +26,12 @@ Gateway не содержит бизнес-правил: он не создаё
 
 | Пакет | Ответственность |
 | --- | --- |
-| `cmd/server` | Чтение настроек, сборка учётной записи, прокси и router, ограничение времени upstream, lifecycle, `healthcheck` и `hash-password`. |
-| `internal/config` | Проверка `PORT`, `SERVING_URL`, `WAREHOUSE_URL`, `FEEDBACK_URL`, `UPSTREAM_TIMEOUT` и настроек `OPERATOR_*`. |
-| `internal/httpapi` | `NewRouter(Routes)` над внедрёнными `http.Handler` с обязательным guard, `/healthz`, конверт ошибок `Problem` с `requestID`, JSON транспортной ошибки. |
+| `cmd/server` | Чтение настроек, сборка учётной записи, журнала, потоков, прокси и router, ограничение времени upstream, запуск проверки сроков, lifecycle, `healthcheck` и `hash-password`. |
+| `internal/config` | Проверка `PORT`, `SERVING_URL`, `WAREHOUSE_URL`, `FEEDBACK_URL`, `UPSTREAM_TIMEOUT`, `JOURNAL_CAPACITY`, `EXPIRATION_RUN_INTERVAL` и настроек `OPERATOR_*`. |
+| `internal/httpapi` | `NewRouter(Routes)` над внедрёнными `http.Handler` с обязательным guard, `/healthz`, `/api/live`, запись статусов допущенных изменяющих запросов, конверт ошибок `Problem` с `requestID`, JSON транспортной ошибки. |
+| `internal/journal` | Журнал изменений в памяти: кольцевой буфер записей, итог по HTTP-статусу, подписка на возможные изменения. Тела и данные запросов не хранит. |
+| `internal/live` | Поток `GET /api/live`: события `change`, `tick`, `session`, `session-ended`, `upstream`, лимиты потоков, события сессий от `internal/operator`. |
+| `internal/expiration` | Плановые запуски `POST /expiration-runs` склада, их записи в журнал и доступность склада для потоков. |
 | `internal/operatorhttp` | Маршруты `/api/operator/*`, guard бизнес-маршрутов и формы обратной связи, cookie, проверка `Origin` и CSRF, удаление учётных данных оператора перед upstream. |
 | `internal/operator` | Учётная запись, сессии, CSRF без входа, лимиты входа, смены пароля и формы обратной связи, режим восстановления; без HTTP и файлов. |
 | `internal/adapters/argon2id` | Хэши паролей Argon2id в формате PHC. |
@@ -54,6 +59,35 @@ Gateway не содержит бизнес-правил: он не создаё
 
 `message` предназначено для журналов и разработчиков: текст для пользователя фронтенд выбирает по `code` и кодам `fieldErrors`. `fieldErrors` есть только у ответов `422`, `sessionRevoked` — только у `PasswordChangeRateLimited`. Тот же `requestID` записывается в журнал gateway.
 
+## Реальное время
+
+Экраны `/app` узнают об изменениях из потока SSE `GET /api/live` и перечитывают данные обычными запросами с `X-Umir-Activity: background`. Поток несёт только сигналы; данные остаются в бизнес-маршрутах.
+
+**Журнал изменений.** Gateway хранит в памяти последние `JOURNAL_CAPACITY` записей. Запись появляется после допущенного изменяющего запроса под `/api/serving/` и `/api/warehouse/`, после `PATCH /api/operator/me/display-name` и после запуска проверки сроков. Запись хранит номер, время, источник (`request`, `expiration`, `external`), метод и путь без query, HTTP-статус и итог; тела, cookie, адреса и заголовки не хранятся. Итог определяется только по статусу:
+
+| Статус | Итог | Сигнал потокам |
+| --- | --- | --- |
+| 2xx, кроме 202 | `success` | есть |
+| 202 | `pending` | есть |
+| 4xx | `rejected` | нет |
+| 5xx, тайм-аут или транспортная ошибка | `unknown` | есть |
+
+Сигнал после `success` может оказаться лишним, например после подготовки заказа: gateway не разбирает тела ответов. У каждого запуска gateway свой `epoch`; после перезапуска журнал пуст и номера начинаются с 1. Несколько реплик gateway не поддерживаются.
+
+**Проверка сроков.** Gateway раз в `EXPIRATION_RUN_INTERVAL` вызывает `POST /expiration-runs` публичного listener склада (gateway#19); новый запуск не начинается, пока не закончен предыдущий. Какие партии истекли, решает склад. Списанные партии дают запись `expiration`. Если версия состояния склада выросла без запросов через gateway — например, просрочку записала обычная операция склада или склад перезапустился, — появляется запись `external`. Неудачный запуск, после которого состояние могло измениться (5xx, тайм-аут), даёт одну запись `unknown` на серию неудач; отказ соединения записи не даёт. После трёх неудачных запусков подряд потоки получают `upstream: false`, после первого удачного — `upstream: true`.
+
+**Поток `GET /api/live`.** Нужна сессия; подключение её не продлевает. Ответ `200` с `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-store` и `X-Accel-Buffering: no`. Первый кадр — `retry: 3000`, затем по порядку:
+
+| Событие | `data` | Когда |
+| --- | --- | --- |
+| `change` | `{"epoch", "seq"}`, `seq` — десятичная строка | при подключении и после записей с итогом `success`, `pending`, `unknown`; серия записей даёт одно событие с последним номером |
+| `tick` | `{"serverTime"}` | при подключении и в начале каждой минуты |
+| `session` | `{"idleExpiresAt", "absoluteExpiresAt"}` | при подключении и после продления сессии действием пользователя |
+| `session-ended` | `{"reason"}`: `signed_out`, `revoked` или `expired` | выход, отзыв сменой пароля, вытеснение или истечение; затем поток закрывается |
+| `upstream` | `{"available"}` | при подключении и при смене доступности склада |
+
+Время — RFC 3339 со смещением `+03:00`. Каждые 15 секунд идёт комментарий `: ping`. Запись клиенту ограничена 5 секундами: клиент, который перестал читать, отключается. Отказы до начала потока используют общий конверт: `401 Unauthenticated`, `403 InvalidOrigin` (`Sec-Fetch-Site` не `same-origin`), `405 MethodNotAllowed`, `429 TooManyStreams` (больше 8 потоков на сессию или 64 на gateway), `503 AuthUnavailable`. Поток не ограничен `UPSTREAM_TIMEOUT`; при остановке gateway потоки закрываются сразу, и браузер переподключается к новому процессу.
+
 ## Личный кабинет оператора
 
 Один заранее заведённый оператор входит по логину и паролю (gateway#8). Правила учётной записи находятся в `internal/operator`, HTTP — в `internal/operatorhttp`. Отдельного сервиса, базы данных и внешнего провайдера входа нет.
@@ -65,7 +99,7 @@ Gateway не содержит бизнес-правил: он не создаё
 | `GET /api/operator/csrf` | Публичный | `200 {"csrfToken"}` — токен живой сессии, а без неё токен анонимного контекста с cookie `umir_operator_preauth`. Сессию не создаёт и не продлевает. |
 | `POST /api/operator/session` | `Origin` и токен анонимного контекста или живой сессии | `200 {"operator", "session"}` и новая cookie сессии; прежняя сессия отзывается. Тело: `{"login", "password", "rememberMe"}`, где `rememberMe` — необязательный флаг «Запомнить меня». Cookie выдаётся только после записи сессии в файл. |
 | `DELETE /api/operator/session` | `Origin` и токен сессии, без живой сессии — анонимный токен | `204` после записи отзыва в файл, cookie сессии очищается. Повтор безопасен. В режиме восстановления выход без cookie сессии проходит с анонимным токеном из `GET /api/operator/csrf`, а с cookie сессии получает `503 AuthUnavailable`: состояние файла неизвестно, и подтвердить выход нельзя. |
-| `GET /api/operator/me` | Сессия | `200 {"operator", "session"}`. Срок бездействия обычной сессии продлевается и записывается в файл до ответа. Если запись точно не удалась, ответ всё равно `200` со сроком, который уже записан в файле, и сессия продолжает работать. |
+| `GET /api/operator/me` | Сессия | `200 {"operator", "session"}`. Срок бездействия обычной сессии продлевается и записывается в файл до ответа. Если запись точно не удалась, ответ всё равно `200` со сроком, который уже записан в файле, и сессия продолжает работать. С `X-Umir-Activity: background` ответ тот же, но срок не продлевается и файл не пишется. |
 | `PATCH /api/operator/me/display-name` | Сессия, `Origin`, `X-CSRF-Token` | `200 {"operator"}`. Тело: `{"displayName"}`. |
 | `PATCH /api/operator/me/password` | Сессия, `Origin`, `X-CSRF-Token` | `204`: все сессии, включая текущую, отозваны, нужен новый вход. Тело: `{"currentPassword", "newPassword"}`. Пять неверных текущих паролей за 60 секунд, с любых сессий, включают паузу смены пароля на 60 секунд: пятая ошибка отзывает только свою сессию. Во время паузы смена отклоняется до проверки пароля, а новый вход паузу не снимает. |
 
@@ -73,7 +107,7 @@ Gateway не содержит бизнес-правил: он не создаё
 
 ### Сессии, cookie и CSRF
 
-- Обычная сессия живёт 30 минут без активности и не дольше 8 часов от входа. Успешно допущенный бизнес-запрос сохраняет продлённое время активности до dispatch; после перезапуска срок не откатывается. Ошибка записи блокирует dispatch с `503`. `GET /api/operator/me` продлевает срок бездействия по описанному выше правилу, но не дальше абсолютного; `GET /api/operator/csrf` срок не продлевает. Фронтенд продлевает сессию только по действию пользователя, без фоновых таймеров (ГОСТ Р 52872-2019, 2.2.1).
+- Обычная сессия живёт 30 минут без активности и не дольше 8 часов от входа. Успешно допущенный бизнес-запрос сохраняет продлённое время активности до dispatch; после перезапуска срок не откатывается. Ошибка записи блокирует dispatch с `503`. `GET /api/operator/me` продлевает срок бездействия по описанному выше правилу, но не дальше абсолютного; `GET /api/operator/csrf` срок не продлевает. Фронтенд продлевает сессию только по действию пользователя, без фоновых таймеров (ГОСТ Р 52872-2019, 2.2.1). Запросы, которые пользователь не вызывал (перечитывание по сигналу потока или по такту), несут `X-Umir-Activity: background`: они проходят те же проверки, но сессию не продлевают и файл не пишут. Значение имеет только точное `background`; заголовок не уходит в upstream.
 - Вход с `"rememberMe": true` («Запомнить меня») открывает сессию ровно на 30 дней от входа. Активность, `/me` и перезапуск этот срок не сдвигают; новый 30-дневный период начинается только с нового входа по паролю.
 - Все сессии хранятся в файле учётной записи и переживают перезапуск gateway, пока не истёк их срок и не сменился пароль. В файле хранится SHA-256 токена сессии, а не сам токен; CSRF-токен сессии вычисляется из её токена и после перезапуска остаётся прежним. Продление срока бездействия обычными бизнес-запросами сохраняется до их передачи upstream. Вход, выход, вытеснение и смена пароля записываются до ответа.
 - Одновременно живут не больше 32 сессий обоих режимов вместе; при переполнении вытесняется самая старая, и это тоже записывается в файл. Анонимные контексты (до 1024 на 30 минут), счётчики неудачных входов и неверных паролей и счётчики формы обратной связи хранятся только в памяти и сбрасываются при перезапуске. Несколько реплик gateway не поддерживаются.
@@ -90,7 +124,7 @@ Guard проверяет каждый запрос под `/api/serving/` и `/a
 | `POST`, `PUT`, `PATCH`, `DELETE` | Сессия, точный `Origin`, `X-CSRF-Token` этой сессии | ещё `403 InvalidOrigin`, `403 InvalidCsrfToken` |
 | Остальные | — | `405 MethodNotAllowed` с заголовком `Allow` |
 
-Отказ guard означает, что upstream запрос не получал: после входа его можно повторить с тем же `commandID`. Если сессия истекла, когда запрос уже передан в upstream, он завершается как обычно. Транспортные ошибки прокси по-прежнему возвращают `outcome`.
+Отказ guard означает, что upstream запрос не получал: после входа его можно повторить с тем же `commandID`. Допущенный изменяющий запрос попадает в [журнал изменений](#реальное-время) с итогом по статусу ответа. Если сессия истекла, когда запрос уже передан в upstream, он завершается как обычно. Транспортные ошибки прокси по-прежнему возвращают `outcome`.
 
 ### Форма обратной связи
 
@@ -246,7 +280,9 @@ docker compose up --build -d --wait
 | `SERVING_URL` | `http://serving:8080` |
 | `WAREHOUSE_URL` | `http://warehouse:8080` |
 | `FEEDBACK_URL` | `http://feedback:8084`; приватный сервис обратной связи |
-| `UPSTREAM_TIMEOUT` | `15s`; общий бюджет на приём тела запроса от клиента и обмен с upstream, формат Go duration |
+| `UPSTREAM_TIMEOUT` | `15s`; общий бюджет на приём тела запроса от клиента и обмен с upstream, формат Go duration. Ограничивает и каждый запуск проверки сроков |
+| `JOURNAL_CAPACITY` | `1000`; сколько последних записей хранит журнал изменений, от 1 до 100000 |
+| `EXPIRATION_RUN_INTERVAL` | `1s`; период запуска проверки сроков на складе, формат Go duration |
 | `TZ` в контейнере | `Europe/Moscow` |
 | `OPERATOR_*` | см. [настройки личного кабинета](#настройки) |
```

- [ ] **Step 2: Run the checks and see them pass**

Run: `git diff --check`
Expected: без вывода.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md README.md
git commit -m "docs: describe the change journal, the live stream and expiration runs"
```


### Task 9: Полная проверка

**Files:** изменений нет. Найденная ошибка исправляется в задаче, которой принадлежит код, отдельным коммитом с тем же scope.

- [ ] **Step 1: Run the repository gate**

Run: `make check && make quality-test`
Expected: PASS:
- форматирование без различий;
- `0 issues.`;
- `OK - No warnings found`;
- 12 пакетов `ok` с `-race`;
- `Quality enforcement: 21 scenarios passed`.

- [ ] **Step 2: Build the image**

Run: `docker compose build`
Expected: PASS; Dockerfile собирает `/server` без изменений.

- [ ] **Step 3: Check whitespace and the history**

Run: `git diff --check origin/main...HEAD && git status --short && git log --oneline origin/main..HEAD`
Expected:
- `git diff --check` молчит;
- рабочее дерево чистое;
- восемь коммитов Task 1–8 по порядку, плюс исправления, если они были.

---

## Отклонения от спецификации и контракта

- **Код `403` для межсайтового `Sec-Fetch-Site`** — `InvalidOrigin`, как у остальных guard gateway. В OpenAPI это ответ `CsrfRejected` с кодом `InvalidOrigin`. В комментариях gateway#19 и в спецификации код указан как `CsrfRejected`: там нужна поправка.
- **`503 AuthUnavailable` до начала потока** в режиме восстановления учётной записи. Контракт этот случай не называл; ответ совпадает с остальными маршрутами кабинета.
- **Неудачные запуски проверки сроков** дают одну запись `unknown` на серию, а не одну на каждый запуск. Иначе недоступный склад будил бы все экраны каждую секунду.
- **Верхняя граница `JOURNAL_CAPACITY`** — 100000: буфер выделяется при запуске.
- **Повторный `Peek` после подписки** внутри `Serve` закрывает гонку между guard и подпиской на события сессии.

## Зависимости вне плана (проверки, а не задачи)

- **Склад: `POST /expiration-runs` (gateway#19).** Без него каждый запуск получает `404`. Это `rejected`, записи нет, но три таких ответа подряд дают `upstream: false`. **Выкатывать сначала склад**, затем gateway.
- **Factory: OpenAPI `1.1.0-draft.1` и `tests/http`.**
  - Сквозная цепочка nginx → gateway → serving/warehouse проверяется в фабрике: `change` после поставки, отсутствие `change` после `409`, новый `epoch` после перезапуска, `session-ended` после выхода, неизменный `idleExpiresAt` после фонового запроса, `upstream: false` через прокси сбоев.
  - Код `403` у потока — `InvalidOrigin` (см. отклонения).
- **Frontend: `location = /api/live` в nginx и live-sync.** План frontend: `2026-10-12-realtime-frontend.md`. Его проверка сессии после обрыва опирается на `GET /api/operator/me` с фоновым заголовком (Task 5).
