import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const retentionPath = resolve(root, "docs/durable/retention.md");
const admissionPath = resolve(root, "docs/durable/admission.md");
const STATUS = "предложение до G1";

function read(path) {
  assert.ok(existsSync(path), `${path} must exist`);
  return readFileSync(path, "utf8");
}

function splitRow(line) {
  return line
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split(/(?<!\\)\|/)
    .map((cell) => cell.trim());
}

// Parses every GitHub-style table: header row, separator row, data rows.
function parseTables(text) {
  const lines = text.split("\n");
  const tables = [];
  for (let i = 0; i < lines.length - 1; i++) {
    if (!lines[i].startsWith("|") || !/^\|[\s:|-]+\|$/.test(lines[i + 1].trim())) {
      continue;
    }
    const header = splitRow(lines[i]);
    const rows = [];
    let j = i + 2;
    while (j < lines.length && lines[j].startsWith("|")) {
      rows.push(splitRow(lines[j]));
      j++;
    }
    tables.push({ header, rows, line: i + 1 });
    i = j - 1;
  }
  return tables;
}

function findTable(text, mustHave) {
  const table = parseTables(text).find((t) => mustHave.every((h) => t.header.includes(h)));
  assert.ok(table, `no table with columns: ${mustHave.join(", ")}`);
  return table;
}

function column(table, name) {
  const index = table.header.indexOf(name);
  assert.ok(index >= 0, `column ${name} missing`);
  return index;
}

function rowByName(table, firstCell) {
  const row = table.rows.find((r) => r[0].includes(firstCell));
  assert.ok(row, `row not found: ${firstCell}`);
  return row;
}

function localLinks(text) {
  const targets = [];
  for (const match of text.matchAll(/\]\(([^)\s]+)\)/g)) {
    const target = match[1];
    if (/^[a-z][a-z0-9+.-]*:/i.test(target) || target.startsWith("#")) {
      continue;
    }
    targets.push(target.split("#")[0]);
  }
  return targets;
}

// The 22 records of the table in section 6 item 4 of the main spec.
const retentionRecords = [
  "Сессия Auth",
  "Результат повтора входа",
  "Прочие результаты команд Auth",
  "Email-вызов, код, proof",
  "Email-вызов без proof",
  "Счётчики неверных кодов и выдач",
  "Задание письма",
  "Приглашение",
  "Журнал событий склада, факты FEFO",
  "Квитанции команд склада",
  "Все данные закрытого ресторана",
  "Предложение Serving без попытки",
  "Попытка Serving без отправленного commit",
  "Терминальная попытка Serving",
  "Попытка Serving в pending или unknown",
  "Обращение Feedback, анонимное",
  "Обращение Feedback ресторана",
  "Текст и телефон обращения",
  "Outbox, опубликованное",
  "Inbox потребителя",
  "Курсоры и захваты страниц",
  "Надгробия ресторанов и организаций Auth",
];

// Key numbers of the main spec: [row, text that must appear in the Срок cell].
const retentionNumbers = [
  ["Сессия Auth", "7 дней"],
  ["Результат повтора входа", "24 ч"],
  ["Прочие результаты команд Auth", "30 дней"],
  ["Прочие результаты команд Auth", "180 дней"],
  ["Email-вызов, код, proof", "24 ч"],
  ["Email-вызов, код, proof", "180 дней"],
  ["Email-вызов без proof", "1 ч"],
  ["Задание письма", "30 дней"],
  ["Приглашение", "30 дней"],
  ["Квитанции команд склада", "35 дней"],
  ["Все данные закрытого ресторана", "7 дней"],
  ["Предложение Serving без попытки", "24 ч"],
  ["Попытка Serving без отправленного commit", "24 ч"],
  ["Терминальная попытка Serving", "30 дней"],
  ["Попытка Serving в pending или unknown", "30 дней"],
  ["Обращение Feedback, анонимное", "30 дней"],
  ["Обращение Feedback, анонимное", "180 дней"],
  ["Обращение Feedback ресторана", "180 дней"],
  ["Обращение Feedback ресторана", "7 дней"],
  ["Outbox, опубликованное", "7 дней"],
  ["Inbox потребителя", "14 дней"],
  ["Курсоры и захваты страниц", "5 минут"],
];

const retentionColumns = [
  "Запись",
  "Владелец",
  "Начало срока",
  "Срок",
  "Грейс",
  "Основание",
  "Действие очистки",
  "Последствие для повтора команды",
  "Статус",
];

// The 7 operations of section 6 item 5 plus the Auth wrong-code budget (C51).
const budgetRows = [
  "Выдача preauth",
  "Вход",
  "Регистрация, начало",
  "Сброс пароля, начало",
  "Анонимная обратная связь",
  "Потоки SSE",
  "Prepare и confirm заказа",
  "Неверные коды",
];

const budgetColumns = [
  "Операция",
  "По клиенту",
  "По preauth / логину / адресу",
  "Глобально",
  "Владелец",
  "Тип окна",
  "Освобождение повторов",
  "Максимальная кардинальность ключей",
  "Ответ при отказе",
  "Статус",
];

// [row, text that must appear in the whole row].
const budgetNumbers = [
  ["Выдача preauth", "60 в минуту"],
  ["Выдача preauth", "5000 за 10 мин"],
  ["Вход", "20 в минуту"],
  ["Вход", "5 неудач за 60 с"],
  ["Регистрация, начало", "10 за 10 мин"],
  ["Регистрация, начало", "3 за 10 мин"],
  ["Регистрация, начало", "300 за 10 мин"],
  ["Регистрация, начало", "30 за 10 мин"],
  ["Сброс пароля, начало", "10 за 10 мин"],
  ["Сброс пароля, начало", "3 за 10 мин"],
  ["Сброс пароля, начало", "60 за 10 мин"],
  ["Анонимная обратная связь", "20 в час"],
  ["Анонимная обратная связь", "5 за 10 мин"],
  ["Анонимная обратная связь", "200 в час"],
  ["Потоки SSE", "8 на сессию"],
  ["Потоки SSE", "200 на организацию"],
  ["Потоки SSE", "2000 на ячейку"],
  ["Prepare и confirm заказа", "60 в минуту на пользователя"],
  ["Prepare и confirm заказа", "600 в минуту на ресторан"],
  ["Неверные коды", "300"],
];

test("every table in retention.md and admission.md has the status column and every row says the proposal status", () => {
  for (const path of [retentionPath, admissionPath]) {
    const tables = parseTables(read(path));
    assert.ok(tables.length > 0, `${path} has no tables`);
    for (const table of tables) {
      const index = table.header.indexOf("Статус");
      assert.ok(index >= 0, `${path}:${table.line} table without the Статус column`);
      assert.ok(table.rows.length > 0, `${path}:${table.line} table without rows`);
      for (const row of table.rows) {
        assert.equal(row.length, table.header.length, `${path}:${table.line} row width: ${row[0]}`);
        assert.equal(row[index], STATUS, `${path}:${table.line} status of ${row[0]}`);
      }
    }
  }
});

test("retention.md carries the retention table with all required columns and all 22 records of the main spec", () => {
  const table = findTable(read(retentionPath), ["Запись", "Срок", "Статус"]);
  assert.deepEqual(table.header, retentionColumns);
  for (const record of retentionRecords) {
    const row = rowByName(table, record);
    for (const cell of row) {
      assert.ok(cell !== "", `empty cell in row ${record}`);
    }
  }
  assert.ok(table.rows.length >= retentionRecords.length);
});

test("retention.md keeps the numbers of the main spec", () => {
  const table = findTable(read(retentionPath), ["Запись", "Срок", "Статус"]);
  const term = column(table, "Срок");
  for (const [record, number] of retentionNumbers) {
    assert.ok(rowByName(table, record)[term].includes(number), `${record}: ${number}`);
  }
  assert.ok(rowByName(table, "Логи")[term].includes("14 дней"));
});

test("retention.md describes Redis windows, key format, limiter marker, strict mode and the 180-day replay boundary", () => {
  const text = read(retentionPath);
  const mustHave = [
    "## Окна и TTL Redis",
    "## Формат ключей Redis",
    "## Маркер непрерывности limiter и строгий режим",
    "## Граница гарантии повтора",
    "## Расхождения (для CR)",
    "umir:<owner>:<kind>:",
    "umir:limiter:generation",
    "umir:limiter:strict_since",
    "SET umir:limiter:generation",
    "NX",
    "redis-init",
    "GATEWAY_ADMISSION_KEY_FILE",
    "maxmemory 512mb",
    "16 MiB",
    "5 минут",
    "noeviction",
    "HMAC",
    "C47",
    "C48",
    "C51",
    "C53",
    "R7",
    "180 дней",
    "без ПДн",
  ];
  for (const needle of mustHave) {
    assert.ok(text.includes(needle), `retention.md misses: ${needle}`);
  }
  const keys = findTable(text, ["Вид ключа", "Владелец", "TTL", "Статус"]);
  assert.ok(keys.rows.some((r) => r[0].includes("umir:gw:adm:")));
  assert.ok(keys.rows.some((r) => r[0].includes("umir:wh:cap:")));
  assert.ok(keys.rows.every((r) => !r[0].includes("umir:auth:")), "Auth has no Redis keys (C51)");
  const owner = column(keys, "Владелец");
  assert.ok(keys.rows.every((r) => ["gw", "wh", "limiter"].includes(r[owner])));
});

test("admission.md carries the budget table with the added columns and all 8 rows", () => {
  const table = findTable(read(admissionPath), ["Операция", "По клиенту", "Статус"]);
  assert.deepEqual(table.header, budgetColumns);
  assert.equal(table.rows.length, budgetRows.length);
  for (const name of budgetRows) {
    const row = rowByName(table, name);
    for (const cell of row) {
      assert.ok(cell !== "", `empty cell in row ${name}`);
    }
  }
});

test("admission.md keeps the numbers of the main spec", () => {
  const table = findTable(read(admissionPath), ["Операция", "По клиенту", "Статус"]);
  for (const [name, needle] of budgetNumbers) {
    assert.ok(rowByName(table, name).join(" | ").includes(needle), `${name}: ${needle}`);
  }
  const window = column(table, "Тип окна");
  const failure = column(table, "Ответ при отказе");
  for (const row of table.rows) {
    assert.match(row[window], /фиксированное|скользящее|аренда/);
    assert.ok(row[failure].includes("429") || row[failure].includes("503"), `failure answer: ${row[0]}`);
  }
});

test("admission.md splits owners as C12, C26 and C51 say", () => {
  const table = findTable(read(admissionPath), ["Операция", "По клиенту", "Статус"]);
  const owner = column(table, "Владелец");
  for (const name of ["Вход", "Регистрация, начало", "Сброс пароля, начало"]) {
    const value = rowByName(table, name)[owner];
    assert.ok(value.includes("Gateway") && value.includes("Auth"), `${name}: ${value}`);
  }
  assert.ok(rowByName(table, "Неверные коды")[owner].includes("Auth"));
  assert.ok(rowByName(table, "Выдача preauth")[owner].includes("Gateway"));
  const text = read(admissionPath);
  assert.ok(text.includes("C12") && text.includes("C26") && text.includes("C51"));
  assert.match(text, /Redis[^.\n]*Auth[^.\n]*нет|у Auth Redis нет|Auth Redis не использует/);
});

test("admission.md has the contract, client key, SSE quotas, order limits and strict mode sections", () => {
  const text = read(admissionPath);
  const mustHave = [
    "## Контракт допуска",
    "## Ключ клиента",
    "## Квоты SSE",
    "## Лимиты заказа",
    "## Строгий режим",
    "## Ответы при отказе",
    "## Расхождения (для CR)",
    "Admit",
    "Request{ID, Fingerprint, Policy, Dimensions",
    "ErrAdmissionUnavailable",
    "ErrAdmissionConflict",
    "redis.Limiter",
    "R11",
    "preauth",
    "HMAC",
    "GATEWAY_ADMISSION_KEY_FILE",
    "GATEWAY_TRUSTED_PROXY_CIDRS",
    "429",
    "RateLimited",
    "Retry-After",
    "details.retryAfterSeconds",
    "503",
    "ServiceUnavailable",
    "not_committed",
    "unavailable",
    "C16",
    "C36",
    "C15",
    "C43",
    "LimitExceeded",
    "InvalidArgument",
    "maxLength: 256",
    "2000",
    "200",
    "не до нуля",
    "R7",
    "C47",
    "umir:limiter:generation",
  ];
  for (const needle of mustHave) {
    assert.ok(text.includes(needle), `admission.md misses: ${needle}`);
  }
  const strict = findTable(text, ["Квота", "Обычно", "Строгий режим", "Статус"]);
  assert.ok(strict.rows.length >= 7);
  const normal = column(strict, "Обычно");
  const tight = column(strict, "Строгий режим");
  for (const row of strict.rows) {
    const strictNumber = Number.parseInt(row[tight], 10);
    const normalNumber = Number.parseInt(row[normal], 10);
    assert.ok(strictNumber >= 1, `strict value is not zero: ${row[0]}`);
    assert.ok(strictNumber < normalNumber, `strict is tighter than normal: ${row[0]}`);
  }
  const sse = findTable(text, ["Квота", "Предел", "Статус"]);
  for (const needle of ["8", "200", "2000"]) {
    assert.ok(sse.rows.some((r) => r[column(sse, "Предел")].startsWith(needle)), `SSE ${needle}`);
  }
  const orders = findTable(text, ["Лимит", "Значение", "Ответ при превышении", "Статус"]);
  assert.ok(orders.rows.some((r) => r[1].startsWith("200")));
  assert.ok(orders.rows.some((r) => r[1].startsWith("2000")));
  assert.ok(orders.rows.some((r) => r[1].includes("256")));
});

test("local links in retention.md and admission.md resolve", () => {
  for (const path of [retentionPath, admissionPath]) {
    const text = read(path);
    for (const target of localLinks(text)) {
      const resolved = resolve(dirname(path), target);
      assert.ok(existsSync(resolved), `${path}: broken link ${target}`);
    }
  }
  assert.ok(localLinks(read(retentionPath)).some((t) => t.endsWith("admission.md")));
  assert.ok(localLinks(read(admissionPath)).some((t) => t.endsWith("retention.md")));
});
