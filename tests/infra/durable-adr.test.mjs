import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const adrPath = resolve(root, "docs/adr/0002-postgres-rabbitmq-redis.md");

// Documents the ADR must link by exact path; all of them must exist.
const expectedDocs = [
  "docs/durable/events.md",
  "docs/durable/configuration.md",
  "docs/durable/retention.md",
  "docs/durable/admission.md",
  "docs/durable-api-amendment.md",
];

function readAdr() {
  assert.ok(existsSync(adrPath), `${adrPath} must exist`);
  return readFileSync(adrPath, "utf8");
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

// Collects the identifiers listed in the second cell of every table row.
function tableIds(text) {
  const ids = new Set();
  for (const line of text.split("\n")) {
    if (!line.startsWith("|")) {
      continue;
    }
    const cells = line.split("|").slice(1, -1).map((cell) => cell.trim());
    if (cells.length < 3) {
      continue;
    }
    for (const token of cells[1].split(/[\s,;]+/)) {
      if (/^[URC]\d+$/.test(token)) {
        ids.add(token);
      }
    }
  }
  return ids;
}

test("ADR 0002 exists and declares it replaces ADR 0001 from commit bcb573e", () => {
  const text = readAdr();
  assert.match(text, /заменяет/i);
  assert.match(text, /0001/);
  assert.match(text, /bcb573e/);
  assert.match(text, /15\.10\.2026/);
});

test("ADR 0002 states plainly that RabbitMQ is a single node and not HA", () => {
  const text = readAdr();
  assert.match(text, /не HA/);
  assert.match(text, /quorum/i);
  assert.match(text, /один узел/i);
});

test("ADR 0002 decision table covers every U and R item and the main C items", () => {
  const ids = tableIds(readAdr());
  const required = [
    ...Array.from({ length: 8 }, (_, index) => `U${index + 1}`),
    ...Array.from({ length: 20 }, (_, index) => `R${index + 1}`),
    "C1", "C2", "C3", "C4", "C5", "C8", "C9", "C17", "C38", "C47",
    "C7", "C14", "C16", "C21", "C29", "C39", "C48", "C53",
  ];
  const missing = required.filter((id) => !ids.has(id));
  assert.deepEqual(missing, [], `table rows are missing: ${missing.join(", ")}`);
});

test("ADR 0002 keeps the sections the decision needs", () => {
  const text = readAdr();
  for (const heading of [/^## Контекст/m, /^## Решение/m, /^## Последствия/m, /^## Отвергнутые варианты/m]) {
    assert.match(text, heading);
  }
  for (const phrase of [/Redis/, /storage generation/, /verify-full/, /AssertRuntimeRole/, /reference\/durable/, /RPO/]) {
    assert.match(text, phrase);
  }
});

test("ADR 0002 local links resolve", () => {
  const text = readAdr();
  const broken = [];
  for (const target of localLinks(text)) {
    const absolute = resolve(dirname(adrPath), target);
    if (!existsSync(absolute)) {
      broken.push(target);
    }
  }
  assert.deepEqual(broken, []);
});

test("ADR 0002 links every expected durable document by its exact path", () => {
  const text = readAdr();
  const linked = new Set(localLinks(text).map((target) => resolve(dirname(adrPath), target)));
  for (const doc of expectedDocs) {
    assert.ok(linked.has(resolve(root, doc)), `ADR must link ${doc}`);
  }
});

test("ADR 0002 linked durable documents exist", () => {
  const missing = expectedDocs.filter((doc) => !existsSync(resolve(root, doc)));
  assert.deepEqual(missing, []);
});
