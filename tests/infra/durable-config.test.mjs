import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const docPath = resolve(root, "docs/durable/configuration.md");

const services = ["Auth", "Gateway", "Warehouse", "Serving", "Feedback", "Frontend"];

function readDoc() {
  assert.ok(existsSync(docPath), `${docPath} must exist`);
  return readFileSync(docPath, "utf8");
}

// Returns the body of the level-2 section whose heading starts with the given
// text (after the "N. " number).
function section(text, title) {
  const lines = text.split("\n");
  const start = lines.findIndex((line) => /^## \d+\.\s/.test(line) && line.includes(title));
  assert.ok(start >= 0, `section "${title}" is missing`);
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i += 1) {
    if (/^## /.test(lines[i])) {
      end = i;
      break;
    }
  }
  return lines.slice(start, end).join("\n");
}

function tableRows(text) {
  return text
    .split("\n")
    .filter((line) => line.startsWith("|") && !/^\|[\s:|-]+\|$/.test(line))
    .map((line) => line.split("|").slice(1, -1).map((cell) => cell.trim()));
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

test("document has the eight contract sections", () => {
  const text = readDoc();
  for (const title of [
    "Общие правила",
    "Переменные по сервисам",
    "Команды и роли базы",
    "Здоровье и готовность",
    "Порядок запуска и задания",
    "Топология Redis",
    "Параметры развёртывания",
    "Расхождения и открытые вопросы (для CR)",
  ]) {
    section(text, title);
  }
});

test("general rules state the file-secret contract and forbid memory mode", () => {
  const rules = section(readDoc(), "Общие правила");
  assert.match(rules, /_FILE/);
  assert.match(rules, /ошибк/i);
  assert.match(rules, /LoadDSN/);
  assert.match(rules, /значени[яй] секретов/i);
  assert.match(rules, /memory/);
});

test("every contract variable is named", () => {
  const text = readDoc();
  const names = [
    "WAREHOUSE_DATABASE_URL_FILE",
    "SERVING_DATABASE_URL_FILE",
    "FEEDBACK_DATABASE_URL_FILE",
    "WAREHOUSE_MIGRATE_DATABASE_URL_FILE",
    "SERVING_MIGRATE_DATABASE_URL_FILE",
    "FEEDBACK_MIGRATE_DATABASE_URL_FILE",
    "database.urlFile",
    "database.migrationUrlFile",
    "amqp.urlFile",
    "GATEWAY_AMQP_URL_FILE",
    "WAREHOUSE_AMQP_URL_FILE",
    "SERVING_AMQP_URL_FILE",
    "FEEDBACK_AMQP_URL_FILE",
    "GATEWAY_REDIS_LIMITER_URL_FILE",
    "WAREHOUSE_REDIS_CACHE_URL_FILE",
    "GATEWAY_INSTANCE_ID",
    "WAREHOUSE_INSTANCE_ID",
    "SERVING_INSTANCE_ID",
    "FEEDBACK_INSTANCE_ID",
    "instanceId",
    "GATEWAY_LIMITER_GENERATION",
    "GATEWAY_ADMISSION_KEY_FILE",
    "GATEWAY_LIVE_CURSOR_KEY_FILE",
    "FEEDBACK_DIGEST_KEY_FILE",
    "AUTH_SERVICE_KEY_FILE",
    "AUTH_RUNTIME_SERVICE_KEY_FILE",
    "GATEWAY_SERVICE_KEY_FILE",
    "SERVING_SERVICE_KEY_FILE",
    "WAREHOUSE_SERVICE_KEY_FILE",
    "FEEDBACK_SERVICE_KEY_FILE",
    "SERVING_AUTH_KEY_FILE",
    "AUTH_RUNTIME_KEY_FILE",
    "GATEWAY_SERVING_KEY_FILE",
    "SERVING_WAREHOUSE_KEY_FILE",
    "WAREHOUSE_API_MODE=v2",
    "UMIR_TRUSTED_PROXY",
    "GATEWAY_TRUSTED_PROXY_CIDRS",
    "UMIR_AUTH_CONFIG_FILE",
    "UMIR_SMTP_USERNAME_FILE",
    "UMIR_SMTP_PASSWORD_FILE",
    "FEEDBACK_REGISTRY_RECONCILE_INTERVAL",
    "owner.json",
    "UMIR_TEST_AMQP_MANAGEMENT_URL",
  ];
  for (const name of names) {
    assert.ok(text.includes(name), `variable or token ${name} is missing`);
  }
  const variables = section(text, "Переменные по сервисам");
  for (const service of services) {
    assert.ok(variables.includes(`### 2.`) && variables.includes(service), `${service} missing in variables`);
  }
});

test("Gateway R3 leftovers are optional and listed as removed in PR-2 (R3, C7)", () => {
  const text = readDoc();
  const removed = ["AUTH_RUNTIME_SERVICE_KEY_FILE", "PRIVATE_PORT", "PRIVATE_LISTEN_HOST"];
  const gateway = text.split(/^### /m).find((block) => block.startsWith("2.5 Gateway"));
  assert.ok(gateway, "Gateway subsection is missing");
  const rows = tableRows(gateway);
  for (const name of removed) {
    const own = rows.filter((row) => row[1] === "Gateway" && row[0].includes(`\`${name}\``));
    assert.ok(own.length > 0, `Gateway table lacks ${name}`);
    for (const row of own) {
      assert.equal(row[2].split(/\s/)[0], "нет", `${name} must not be required for Gateway: ${row[2]}`);
      assert.match(row.join(" "), /PR-2/, `${name} row must name Gateway PR-2`);
      assert.match(row.join(" "), /C7/, `${name} row must cite C7`);
    }
    // A row that still lists a removed variable next to required ones is an error.
    for (const row of rows.filter((r) => r[1] === "Gateway" && r[0].includes(`\`${name}\``))) {
      assert.ok(!row[0].includes("`GATEWAY_TRUSTED_PROXY_CIDRS`") && !row[0].includes("`AUTH_URL`"), `${name} shares a row with required variables`);
    }
  }
  const dropped = section(text, "Общие правила");
  for (const name of removed) {
    assert.ok(dropped.includes(name), `removed-variable table lacks ${name}`);
  }
});

test("variable tables have the five contract columns and consistent rows", () => {
  const variables = section(readDoc(), "Переменные по сервисам");
  const tables = variables.split("\n\n").filter((block) => block.startsWith("|"));
  assert.ok(tables.length >= services.length, "one variable table per service is expected");
  for (const block of tables) {
    const rows = tableRows(block);
    const header = rows[0];
    assert.deepEqual(header.slice(0, 5), [
      "Переменная",
      "Сервис",
      "Обязательна",
      "Откуда значение",
      "Кто задаёт",
    ]);
    for (const row of rows) {
      assert.equal(row.length, header.length, `table row width differs: ${row[0]}`);
    }
  }
});

test("Auth has no Redis and no limiter generation (C51)", () => {
  const variables = section(readDoc(), "Переменные по сервисам");
  assert.match(variables, /C51/);
  assert.doesNotMatch(variables, /AUTH_REDIS/);
  assert.doesNotMatch(variables, /AUTH_LIMITER_GENERATION/);
});

test("command table lists every service with its actual commands (C44)", () => {
  const commands = section(readDoc(), "Команды и роли базы");
  const rows = tableRows(commands);
  for (const service of services) {
    assert.ok(
      rows.some((row) => row[0] === service),
      `command table has no row for ${service}`,
    );
  }
  for (const token of [
    "migrate",
    "serve",
    "generation rotate --reason",
    "init",
    "keys check",
    "server serve",
    "feedback migrate",
    "feedback generation rotate",
    "server healthcheck",
    "umir_auth_owner",
    "umir_warehouse",
    "umir_serving_owner",
    "umir_feedback",
  ]) {
    assert.ok(commands.includes(token), `command table lacks ${token}`);
  }
  assert.match(commands, /C8/);
  assert.match(commands, /C9/);
});

test("health table covers all services and capability names (C32, C42, C49)", () => {
  const health = section(readDoc(), "Здоровье и готовность");
  assert.ok(health.includes("/healthz"));
  assert.ok(health.includes("/readyz"));
  assert.ok(health.includes("{instance, generation, capabilities}"));
  const rows = tableRows(health);
  for (const service of services) {
    assert.ok(rows.some((row) => row[0] === service), `health table has no row for ${service}`);
  }
  const capabilities = {
    Auth: ["jwks", "authorize", "admission", "registry", "mail", "reconcile"],
    Gateway: ["http", "auth", "events", "limiter", "scheduler"],
    Warehouse: ["journal", "outbox", "captures"],
    Serving: ["postgres", "schema", "auth_jwks", "events"],
    Feedback: ["intake", "delivery.smtp", "delivery.telegram", "outbox", "lifecycle", "retention"],
    Frontend: ["static"],
  };
  for (const [service, names] of Object.entries(capabilities)) {
    const row = rows.find((candidate) => candidate[0] === service).join(" ");
    for (const name of names) {
      assert.ok(row.includes(`\`${name}\``), `${service} row lacks capability ${name}`);
    }
  }
  const frontend = rows.find((row) => row[0] === "Frontend").join(" ");
  assert.doesNotMatch(frontend, /`generation`/);
  assert.match(health, /\{"capabilities":\{"static":"ok"\}\}/);
  assert.match(health, /degraded/);
  assert.match(health, /C49/);
});

test("startup order names the jobs, aliases and unique instance ids", () => {
  const startup = section(readDoc(), "Порядок запуска и задания");
  for (const token of [
    "pg-roles",
    "mq-init",
    "redis-init",
    "migrate-auth",
    "migrate-warehouse",
    "migrate-serving",
    "migrate-feedback",
    "init-auth",
    "secrets-init",
    "gateway-a",
    "gateway-b",
    "serving-a",
    "serving-b",
    "C19",
    "C20",
  ]) {
    assert.ok(startup.includes(token), `startup section lacks ${token}`);
  }
  assert.match(startup, /псевдоним/);
});

test("Redis topology fixes key families, markers and ACL", () => {
  const redis = section(readDoc(), "Топология Redis");
  for (const token of [
    "umir:gw:",
    "umir:wh:",
    "umir:limiter:generation",
    "umir:limiter:strict_since",
    "C47",
    "ACL",
    "FLUSH",
    "redis-limiter",
    "redis-cache",
  ]) {
    assert.ok(redis.includes(token), `Redis section lacks ${token}`);
  }
});

test("Redis service ACLs limit CLIENT to go-redis setup subcommands", () => {
  const generator = readFileSync(resolve(root, "scripts/durable/secrets.sh"), "utf8");
  const runtime = readFileSync(resolve(root, "tests/infra/durable-runtime-check.sh"), "utf8");
  assert.match(generator, /\+client\|setinfo \+client\|setname/);
  assert.doesNotMatch(generator, /\+client(?:\s|\")/);
  assert.match(runtime, /CLIENT SETINFO LIB-NAME/);
  assert.match(runtime, /CLIENT SETNAME/);
  assert.match(runtime, /CLIENT KILL ID 1/);
});

test("deployment parameters cover R11, R12 and prod TLS", () => {
  const parameters = section(readDoc(), "Параметры развёртывания");
  for (const token of ["R11", "R12", "verify-full", "amqps://", "rediss://", "C34", "UMIR_FRONT_SUBNET", "COMPLETION_NPM_ADDR"]) {
    assert.ok(parameters.includes(token), `parameters section lacks ${token}`);
  }
});

test("storage and memory switches appear only as forbidden", () => {
  const text = readDoc();
  const pattern = /_STORAGE|ALLOW_MEMORY/;
  let seen = 0;
  for (const line of text.split("\n")) {
    if (!pattern.test(line)) {
      continue;
    }
    seen += 1;
    assert.match(line, /запрещ/i, `line allows a forbidden switch: ${line}`);
  }
  assert.ok(seen > 0, "forbidden switches must be listed explicitly");
});

test("CR section records the known mismatches", () => {
  const cr = section(readDoc(), "Расхождения и открытые вопросы (для CR)");
  for (const token of ["FEEDBACK_DIGEST_KEY", "auth keys check", "512mb", "umir-serving", "healthcheck"]) {
    assert.ok(cr.includes(token), `CR section lacks ${token}`);
  }
});

test("local links resolve", () => {
  const text = readDoc();
  for (const target of localLinks(text)) {
    const absolute = resolve(dirname(docPath), target);
    assert.ok(existsSync(absolute), `link target ${target} does not exist`);
  }
});

test("document carries no secret-looking values", () => {
  const text = readDoc();
  assert.doesNotMatch(text, /postgres(ql)?:\/\/[^\s`]*:[^\s`@]+@/);
  assert.doesNotMatch(text, /amqps?:\/\/[^\s`]*:[^\s`@]+@/);
  assert.doesNotMatch(text, /rediss?:\/\/[^\s`]*:[^\s`@]+@/);
  assert.doesNotMatch(text, /\b[0-9a-f]{64}\b/);
});

test("general rules keep the no-memory-mode and time zone rules, decisions sit in section 8", () => {
  const text = readDoc();
  const rules = text.slice(0, text.indexOf("\n## 2."));
  assert.match(rules, /^4\. Режима `memory` в боевых бинарниках нет.*ALLOW_MEMORY/m, "general rule 4 (no memory mode) is missing");
  assert.match(rules, /^9\. Все контейнеры работают в зоне `Europe\/Moscow`/m, "general rule 9 (time zone) is missing");
  const open = section(text, "Расхождения и открытые вопросы");
  assert.match(open, /^4\. `maxmemory` у `redis-cache`\. Решено \(C62\)/m, "section 8 item 4 must carry C62");
  assert.match(open, /^9\. Секреты Feedback.*Решено \(C63\)/m, "section 8 item 9 must carry C63");
});
