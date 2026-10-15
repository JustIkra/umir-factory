import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const docsDir = resolve(root, "docs/durable");
const eventsPath = resolve(docsDir, "events.md");
const envelopePath = resolve(docsDir, "event-envelope.schema.json");
const taskPath = resolve(docsDir, "task-message.schema.json");

// ---------------------------------------------------------------------------
// A small JSON Schema (draft 2020-12) validator. It supports only the keywords
// the two catalog schemas use and throws on any other keyword, so a typo in a
// schema can never silently weaken a check.
// ---------------------------------------------------------------------------

const annotationKeywords = new Set(["$schema", "$id", "$comment", "$defs", "title", "description", "examples"]);
const dateTimePattern = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])T([01]\d|2[0-3]):[0-5]\d:[0-5]\d(\.\d+)?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$/;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function typeOf(value) {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  if (Number.isInteger(value)) return "integer";
  return typeof value;
}

function matchesType(value, type) {
  const actual = typeOf(value);
  return actual === type || (type === "number" && actual === "integer");
}

function resolveRef(root, ref) {
  assert.ok(ref.startsWith("#/"), `unsupported $ref ${ref}`);
  let node = root;
  for (const part of ref.slice(2).split("/")) {
    node = node?.[part];
  }
  assert.ok(node !== undefined, `unresolved $ref ${ref}`);
  return node;
}

function validate(rootSchema, schema, value, path = "$") {
  const errors = [];
  if (schema === true) return errors;
  if (schema === false) return [`${path}: schema is false`];
  for (const key of Object.keys(schema)) {
    if (annotationKeywords.has(key)) continue;
    switch (key) {
      case "$ref":
        errors.push(...validate(rootSchema, resolveRef(rootSchema, schema.$ref), value, path));
        break;
      case "type": {
        const types = Array.isArray(schema.type) ? schema.type : [schema.type];
        if (!types.some((type) => matchesType(value, type))) errors.push(`${path}: type must be ${types.join("|")}`);
        break;
      }
      case "const":
        if (value !== schema.const) errors.push(`${path}: must equal ${JSON.stringify(schema.const)}`);
        break;
      case "enum":
        if (!schema.enum.includes(value)) errors.push(`${path}: not in enum`);
        break;
      case "pattern":
        if (typeof value === "string" && !new RegExp(schema.pattern, "u").test(value)) errors.push(`${path}: does not match ${schema.pattern}`);
        break;
      case "minLength":
        if (typeof value === "string" && [...value].length < schema.minLength) errors.push(`${path}: shorter than ${schema.minLength}`);
        break;
      case "maxLength":
        if (typeof value === "string" && [...value].length > schema.maxLength) errors.push(`${path}: longer than ${schema.maxLength}`);
        break;
      case "minimum":
        if (typeof value === "number" && value < schema.minimum) errors.push(`${path}: below ${schema.minimum}`);
        break;
      case "maximum":
        if (typeof value === "number" && value > schema.maximum) errors.push(`${path}: above ${schema.maximum}`);
        break;
      case "format":
        if (typeof value === "string") {
          if (schema.format === "date-time" && (!dateTimePattern.test(value) || Number.isNaN(Date.parse(value)))) errors.push(`${path}: not a date-time`);
          else if (schema.format === "uuid" && !uuidPattern.test(value)) errors.push(`${path}: not a uuid`);
          else if (!["date-time", "uuid"].includes(schema.format)) throw new Error(`unsupported format ${schema.format}`);
        }
        break;
      case "items":
        if (Array.isArray(value)) value.forEach((item, index) => errors.push(...validate(rootSchema, schema.items, item, `${path}[${index}]`)));
        break;
      case "minItems":
        if (Array.isArray(value) && value.length < schema.minItems) errors.push(`${path}: fewer than ${schema.minItems} items`);
        break;
      case "maxItems":
        if (Array.isArray(value) && value.length > schema.maxItems) errors.push(`${path}: more than ${schema.maxItems} items`);
        break;
      case "uniqueItems":
        if (schema.uniqueItems && Array.isArray(value) && new Set(value.map((item) => JSON.stringify(item))).size !== value.length) errors.push(`${path}: items are not unique`);
        break;
      case "required":
        if (typeOf(value) === "object") for (const name of schema.required) if (!(name in value)) errors.push(`${path}: missing ${name}`);
        break;
      case "properties":
        if (typeOf(value) === "object") {
          for (const [name, sub] of Object.entries(schema.properties)) if (name in value) errors.push(...validate(rootSchema, sub, value[name], `${path}.${name}`));
        }
        break;
      case "additionalProperties":
        if (typeOf(value) === "object") {
          for (const name of Object.keys(value)) {
            if (name in (schema.properties ?? {})) continue;
            if (schema.additionalProperties === false) errors.push(`${path}: unexpected property ${name}`);
            else errors.push(...validate(rootSchema, schema.additionalProperties, value[name], `${path}.${name}`));
          }
        }
        break;
      case "allOf":
        for (const sub of schema.allOf) errors.push(...validate(rootSchema, sub, value, path));
        break;
      case "if":
      case "then":
      case "else":
        if (key === "if") {
          const matched = validate(rootSchema, schema.if, value, path).length === 0;
          const branch = matched ? schema.then : schema.else;
          if (branch !== undefined) errors.push(...validate(rootSchema, branch, value, path));
        }
        break;
      case "not":
        if (validate(rootSchema, schema.not, value, path).length === 0) errors.push(`${path}: must not match`);
        break;
      default:
        throw new Error(`unsupported keyword ${key} at ${path}`);
    }
  }
  return errors;
}

function loadJSON(path) {
  assert.ok(existsSync(path), `${path} must exist`);
  return JSON.parse(readFileSync(path, "utf8"));
}

function readEvents() {
  assert.ok(existsSync(eventsPath), `${eventsPath} must exist`);
  return readFileSync(eventsPath, "utf8");
}

function examples(text, kind) {
  const found = [];
  const pattern = /<!-- example: (envelope|task) -->\s*```json\n([\s\S]*?)\n```/g;
  for (const match of text.matchAll(pattern)) {
    if (match[1] === kind) found.push(JSON.parse(match[2]));
  }
  return found;
}

const warehouseTypes = {
  ProductRegistered: ["catalog", "menu"],
  DishDefined: ["catalog", "menu"],
  DeliveryReceived: ["stock", "menu", "analytics"],
  OrderPlaced: ["stock", "menu", "orders", "analytics"],
  BatchExpiryPending: ["stock", "menu", "analytics"],
  BatchWriteoffConfirmed: ["stock", "analytics"],
  BatchExpired: ["stock", "menu", "analytics"],
};

const authTypes = [
  "restaurant.deleting",
  "restaurant.deleted",
  "organization.deleting",
  "organization.deleted",
  "organization.settings_changed",
  "membership.revoked",
  "assignment.revoked",
];

const goodEnvelope = () => ({
  eventID: "wh-7f3a-000042",
  owner: "warehouse",
  schemaVersion: 1,
  orgID: "org-1",
  restaurantID: "rest-1",
  aggregateID: "rest-1",
  aggregateRevision: 42,
  generation: "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
  occurredAt: "2026-10-15T09:30:00Z",
  topics: ["stock", "analytics"],
});

// ---------------------------------------------------------------------------

test("the validator itself rejects what it should", () => {
  const schema = { type: "object", required: ["a"], properties: { a: { type: "integer", minimum: 1 } }, additionalProperties: false };
  assert.deepEqual(validate(schema, schema, { a: 1 }), []);
  assert.notEqual(validate(schema, schema, { a: 0 }).length, 0);
  assert.notEqual(validate(schema, schema, { a: 1, b: 2 }).length, 0);
  assert.notEqual(validate(schema, schema, {}).length, 0);
  assert.throws(() => validate({ frobnicate: 1 }, { frobnicate: 1 }, {}), /unsupported keyword/);
});

test("both schemas are valid JSON Schema 2020-12 documents with closed objects", () => {
  for (const path of [envelopePath, taskPath]) {
    const schema = loadJSON(path);
    assert.equal(schema.$schema, "https://json-schema.org/draft/2020-12/schema");
    assert.equal(typeof schema.$id, "string");
    assert.equal(schema.type, "object");
    assert.equal(schema.additionalProperties, false);
    assert.ok(Array.isArray(schema.required) && schema.required.length > 0);
    for (const name of schema.required) assert.ok(name in schema.properties, `${name} must be declared`);
    // Every keyword must be known to the validator.
    validate(schema, schema, {});
  }
});

test("envelope schema fixes the field set and the topics dictionary", () => {
  const schema = loadJSON(envelopePath);
  assert.deepEqual(
    Object.keys(schema.properties).sort(),
    ["aggregateID", "aggregateRevision", "causationID", "correlationID", "eventID", "generation", "occurredAt", "orgID", "owner", "restaurantID", "schemaVersion", "topics"],
  );
  assert.deepEqual(schema.properties.owner.enum, ["warehouse", "auth"]);
  assert.deepEqual(schema.properties.topics.items.enum, ["catalog", "stock", "menu", "orders", "analytics"]);
  assert.equal(schema.properties.eventID.$ref, "#/$defs/opaqueID");
  assert.equal(schema.$defs.opaqueID.maxLength, 256);
  assert.ok(!schema.required.includes("topics"), "topics is conditionally required");
  assert.ok(schema.allOf?.some((rule) => rule.if && rule.then), "topics requirement must be an if/then rule");
});

test("task message schema fixes the field set", () => {
  const schema = loadJSON(taskPath);
  assert.deepEqual(
    Object.keys(schema.properties).sort(),
    ["channel", "correlationID", "generation", "jobID", "messageID", "occurredAt", "owner", "schemaVersion"],
  );
  assert.ok(!schema.required.includes("channel"), "channel is optional");
  for (const name of ["messageID", "owner", "schemaVersion", "jobID", "generation", "occurredAt", "correlationID"]) {
    assert.ok(schema.required.includes(name), `${name} is required`);
  }
});

test("every example in events.md is valid and every event type has one", () => {
  const text = readEvents();
  const envelope = loadJSON(envelopePath);
  const task = loadJSON(taskPath);

  const envelopes = examples(text, "envelope");
  assert.ok(envelopes.length >= 14, `expected an envelope example per event type (14), got ${envelopes.length}`);
  for (const [index, example] of envelopes.entries()) {
    assert.deepEqual(validate(envelope, envelope, example), [], `envelope example ${index + 1}`);
    assert.ok(JSON.stringify(example).length < 1024, "bodies stay small");
  }
  assert.ok(envelopes.filter((example) => example.owner === "warehouse").length >= 7);
  assert.ok(envelopes.filter((example) => example.owner === "auth").length >= 7);

  const tasks = examples(text, "task");
  assert.ok(tasks.length >= 3, `expected a task example per queue (3), got ${tasks.length}`);
  for (const [index, example] of tasks.entries()) {
    assert.deepEqual(validate(task, task, example), [], `task example ${index + 1}`);
  }
  assert.deepEqual(new Set(tasks.map((example) => example.owner)), new Set(["auth", "feedback"]));
});

test("envelope mutations are rejected", () => {
  const schema = loadJSON(envelopePath);
  const reject = (mutate, label) => {
    const value = goodEnvelope();
    mutate(value);
    assert.notEqual(validate(schema, schema, value).length, 0, `${label} must be rejected`);
  };
  assert.deepEqual(validate(schema, schema, goodEnvelope()), []);

  reject((v) => { v.extra = 1; }, "extra field");
  reject((v) => { v.email = "guest@example.org"; }, "personal data field email");
  reject((v) => { v.token = "secret"; }, "token field");
  reject((v) => { delete v.topics; }, "warehouse without topics");
  reject((v) => { v.topics = []; }, "warehouse with empty topics");
  reject((v) => { v.topics = ["stock", "reports"]; }, "unknown topic");
  reject((v) => { v.topics = ["stock", "stock"]; }, "duplicate topic");
  reject((v) => { delete v.restaurantID; }, "warehouse without restaurantID");
  reject((v) => { v.eventID = "x".repeat(257); }, "eventID over 256 characters");
  reject((v) => { v.eventID = ""; }, "empty eventID");
  reject((v) => { v.eventID = "has space"; }, "eventID with whitespace");
  reject((v) => { v.owner = "serving"; }, "serving as owner");
  reject((v) => { v.occurredAt = "2026-10-15 09:30:00"; }, "date without T/Z");
  reject((v) => { v.occurredAt = "yesterday"; }, "date not RFC 3339");
  reject((v) => { v.generation = "not-a-uuid"; }, "generation not a UUID");
  reject((v) => { v.aggregateRevision = "42"; }, "string revision");
  reject((v) => { v.aggregateRevision = 0; }, "revision below 1");
  reject((v) => { v.schemaVersion = 0; }, "schemaVersion below 1");
  reject((v) => { delete v.orgID; }, "missing orgID");

  const auth = { ...goodEnvelope(), owner: "auth" };
  delete auth.topics;
  assert.deepEqual(validate(schema, schema, auth), [], "auth event without topics and restaurantID is valid");
  delete auth.restaurantID;
  assert.deepEqual(validate(schema, schema, auth), [], "organization-level auth event has no restaurantID");
  assert.notEqual(validate(schema, schema, { ...auth, topics: ["stock"] }).length, 0, "auth events carry no topics");
});

test("task message mutations are rejected", () => {
  const schema = loadJSON(taskPath);
  const good = () => ({
    messageID: "msg-0001",
    owner: "feedback",
    schemaVersion: 1,
    jobID: "job-0001",
    channel: "smtp",
    generation: "6b1f6a52-3c58-4d3e-9a38-0c1b9a1f1c10",
    occurredAt: "2026-10-15T09:30:00Z",
    correlationID: "req-0001",
  });
  assert.deepEqual(validate(schema, schema, good()), []);
  const noChannel = good();
  delete noChannel.channel;
  assert.deepEqual(validate(schema, schema, noChannel), []);

  const reject = (mutate, label) => {
    const value = good();
    mutate(value);
    assert.notEqual(validate(schema, schema, value).length, 0, `${label} must be rejected`);
  };
  reject((v) => { v.extra = 1; }, "extra field");
  reject((v) => { v.email = "guest@example.org"; }, "email field");
  reject((v) => { v.text = "complaint text"; }, "message text field");
  reject((v) => { v.phone = "+70000000000"; }, "phone field");
  reject((v) => { v.code = "123456"; }, "one-time code field");
  reject((v) => { delete v.jobID; }, "missing jobID");
  reject((v) => { delete v.correlationID; }, "missing correlationID");
  reject((v) => { v.channel = "sms"; }, "unknown channel");
  reject((v) => { v.owner = "warehouse"; }, "warehouse as task owner");
  reject((v) => { v.messageID = "m".repeat(257); }, "messageID over 256 characters");
  reject((v) => { v.occurredAt = "soon"; }, "bad date");
});

test("events.md covers exchanges, events, queues, return policy and publishers", () => {
  const text = readEvents();
  for (const name of ["umir.events", "umir.auth", "umir.tasks", "umir.dlx"]) assert.ok(text.includes(`\`${name}\``), `exchange ${name}`);
  for (const name of Object.keys(warehouseTypes)) assert.ok(text.includes(`warehouse.${name}`), `warehouse.${name}`);
  for (const name of authTypes) assert.ok(text.includes(`auth.${name}`), `auth.${name}`);
  for (const name of ["auth.mail", "feedback.smtp", "feedback.telegram", "feedback.auth"]) {
    assert.ok(text.includes(`\`${name}\``), `queue ${name}`);
    if (name !== "feedback.auth") assert.ok(text.includes(`\`${name}.retry\``) || text.includes("`<q>.retry`"), `${name}.retry`);
  }
  for (const name of ["gateway.live.<instance>", "gateway.auth.<instance>", "serving.auth.<instance>"]) assert.ok(text.includes(`\`${name}\``), name);
  for (const phrase of [
    "message_id", "content_type", "x-umir-schema", "x-umir-restaurant-id", "x-umir-generation",
    "x-delivery-limit", "reject-publish", "mq-init", "aggregateRevision", "mandatory",
    "Serving ничего не публикует", "warehouse.#", "p95", "256", "outbox.id",
    "accept", "retry", "exclusive", "quorum", "не подряд",
  ]) {
    assert.ok(text.includes(phrase), `events.md must mention ${phrase}`);
  }
  assert.match(text, /30 с/);
  assert.match(text, /\|\s*Возврат\s*\|/i, "routing-key table must have a return column");
});

test("topics table matches the Warehouse mapping and uses the five-word dictionary", () => {
  const text = readEvents();
  const found = new Map();
  for (const line of text.split("\n")) {
    const match = /^\|\s*`warehouse\.(\w+)`\s*\|\s*((?:`\w+`(?:,\s*)?)+)\s*\|\s*$/.exec(line);
    if (match) found.set(match[1], [...match[2].matchAll(/`(\w+)`/g)].map((item) => item[1]));
  }
  assert.deepEqual(Object.fromEntries(found), warehouseTypes);
});

test("routing-key table gives every key a return policy", () => {
  const text = readEvents();
  const policy = new Map();
  for (const line of text.split("\n")) {
    const match = /^\|\s*`(umir\.(?:events|auth|tasks))`\s*\|\s*`([\w.]+)`\s*\|.*\|\s*(accept|retry)\s*\|\s*$/.exec(line);
    if (match) policy.set(`${match[1]} ${match[2]}`, match[3]);
  }
  for (const name of Object.keys(warehouseTypes)) assert.equal(policy.get(`umir.events warehouse.${name}`), "accept", name);
  for (const name of authTypes) assert.equal(policy.get(`umir.auth auth.${name}`), "accept", name);
  for (const key of ["auth.mail", "feedback.smtp", "feedback.telegram"]) assert.equal(policy.get(`umir.tasks ${key}`), "retry", key);
  assert.equal(policy.size, 7 + 7 + 3);
});

// Permission model of RabbitMQ: a user pattern is a regular expression that
// has to match the whole resource name.
function parseRights(text) {
  const start = text.indexOf("## Права");
  assert.ok(start >= 0, "events.md must have a «Права» section");
  const rights = new Map();
  for (const line of text.slice(start).split("\n")) {
    const cells = line.replaceAll("\\|", "\u0000").split("|").map((cell) => cell.replaceAll("\u0000", "|").trim());
    // | user | configure | write | read | notes |
    const user = /^`(umir_[a-z_]+)`$/.exec(cells[1] ?? "");
    if (!user) continue;
    const pattern = (cell) => {
      const match = /^`(.*)`$/.exec(cell);
      assert.ok(match, `${user[1]}: right must be a code span, got ${cell}`);
      return match[1];
    };
    rights.set(user[1], { configure: pattern(cells[2]), write: pattern(cells[3]), read: pattern(cells[4]) });
  }
  return rights;
}

test("RabbitMQ rights: name templates instead of .*, minimal per service", () => {
  const rights = parseRights(readEvents());
  assert.deepEqual([...rights.keys()].sort(), ["umir_auth", "umir_feedback", "umir_gateway", "umir_mq_init", "umir_serving", "umir_warehouse"]);

  const allowed = (user, kind, resource) => new RegExp(`^(?:${rights.get(user)[kind]})$`, "u").test(resource);
  for (const [user, set] of rights) {
    for (const kind of ["configure", "write", "read"]) {
      if (user === "umir_mq_init") continue;
      assert.ok(!set[kind].includes(".*"), `${user}.${kind} must not use .*`);
    }
  }
  assert.ok(allowed("umir_mq_init", "configure", "anything") && allowed("umir_mq_init", "write", "anything") && allowed("umir_mq_init", "read", "anything"));

  // Only Warehouse publishes events; nobody else may write umir.events.
  assert.ok(allowed("umir_warehouse", "write", "umir.events"));
  for (const user of ["umir_auth", "umir_serving", "umir_feedback", "umir_gateway"]) assert.ok(!allowed(user, "write", "umir.events"), `${user} must not publish umir.events`);
  for (const kind of ["configure", "read"]) assert.ok(!allowed("umir_warehouse", kind, "umir.events"), `warehouse ${kind} umir.events`);
  // Serving is a pure consumer of umir.auth and never binds to umir.events (C38).
  assert.ok(allowed("umir_serving", "read", "umir.auth"));
  assert.ok(!allowed("umir_serving", "read", "umir.events"));
  assert.ok(!allowed("umir_serving", "write", "umir.auth") && !allowed("umir_serving", "write", "umir.tasks") && !allowed("umir_serving", "write", "umir.dlx"));
  assert.ok(allowed("umir_serving", "configure", "serving.auth.serving-a") && allowed("umir_serving", "write", "serving.auth.serving-a") && allowed("umir_serving", "read", "serving.auth.serving-a"));
  assert.ok(!allowed("umir_serving", "configure", "serving.other") && !allowed("umir_serving", "configure", "gateway.live.gateway-a"));
  // Gateway reads umir.events and umir.auth, owns only its two fan-out queue families, never publishes.
  assert.ok(allowed("umir_gateway", "read", "umir.events") && allowed("umir_gateway", "read", "umir.auth"));
  assert.ok(!allowed("umir_gateway", "write", "umir.events") && !allowed("umir_gateway", "write", "umir.auth") && !allowed("umir_gateway", "write", "umir.tasks") && !allowed("umir_gateway", "write", "umir.dlx"));
  for (const name of ["gateway.live.gateway-a", "gateway.auth.gateway-b"]) {
    assert.ok(allowed("umir_gateway", "configure", name) && allowed("umir_gateway", "write", name) && allowed("umir_gateway", "read", name), name);
  }
  assert.ok(!allowed("umir_gateway", "configure", "gateway.live") && !allowed("umir_gateway", "configure", "auth.mail") && !allowed("umir_gateway", "configure", "serving.auth.serving-a"));
  // Auth publishes umir.auth and umir.tasks and consumes only auth.mail.
  assert.ok(allowed("umir_auth", "write", "umir.auth") && allowed("umir_auth", "write", "umir.tasks"));
  assert.ok(allowed("umir_auth", "read", "auth.mail") && !allowed("umir_auth", "read", "auth.mail.dlq") && !allowed("umir_auth", "read", "feedback.smtp") && !allowed("umir_auth", "read", "feedback.auth"));
  // Feedback publishes tasks and consumes its own three queues only.
  assert.ok(allowed("umir_feedback", "write", "umir.tasks"));
  for (const name of ["feedback.smtp", "feedback.telegram", "feedback.auth"]) assert.ok(allowed("umir_feedback", "read", name), name);
  assert.ok(!allowed("umir_feedback", "read", "auth.mail") && !allowed("umir_feedback", "write", "umir.auth") && !allowed("umir_feedback", "write", "umir.events"));
  // Nobody except mq-init declares durable queues or exchanges.
  for (const user of ["umir_auth", "umir_warehouse", "umir_feedback"]) {
    for (const name of ["auth.mail", "feedback.smtp", "feedback.auth", "umir.events", "umir.tasks", "auth.mail.retry", "auth.mail.dlq"]) assert.ok(!allowed(user, "configure", name), `${user} configure ${name}`);
  }
  // The default exchange (amq.default) is never writable.
  for (const [user, set] of rights) if (user !== "umir_mq_init") assert.ok(!new RegExp(`^(?:${set.write})$`, "u").test("amq.default"), `${user} must not write amq.default`);
});

test("every local link in events.md resolves", () => {
  const text = readEvents();
  const targets = [];
  for (const match of text.matchAll(/\]\(([^)\s]+)\)/g)) {
    const target = match[1];
    if (/^[a-z][a-z0-9+.-]*:/i.test(target) || target.startsWith("#")) continue;
    targets.push(target.split("#")[0]);
  }
  assert.ok(targets.includes("event-envelope.schema.json") && targets.includes("task-message.schema.json"), "events.md links both schemas");
  for (const target of targets) assert.ok(existsSync(resolve(docsDir, target)), `broken link ${target}`);
});

test("documents carry no personal data, secrets or message texts", () => {
  const text = readEvents();
  assert.doesNotMatch(text, /[A-Za-z0-9._%+-]+@(?!example\.(org|com)\b)[A-Za-z0-9.-]+\.[a-z]{2,}/, "no real e-mail addresses");
  assert.doesNotMatch(text, /amqps?:\/\/[^\s`]*:[^\s`]*@/, "no AMQP URL with credentials");
  assert.doesNotMatch(text, /\+\d{10,}/, "no phone numbers");
  for (const example of [...examples(text, "envelope"), ...examples(text, "task")]) {
    for (const name of Object.keys(example)) assert.ok(!/mail|phone|login|name|password|token|code|text|message$/i.test(name) || name === "messageID", `field ${name} could carry personal data`);
  }
});
