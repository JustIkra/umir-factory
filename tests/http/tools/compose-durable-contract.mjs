import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';

const directory = process.argv[2];
if (!directory) throw Error('Usage: node compose-durable-contract.mjs API_DIRECTORY');
const inputs = {};
const read = (name) => {
  const bytes = readFileSync(path.join(directory, name));
  inputs[name] = createHash('sha256').update(bytes).digest('hex');
  return JSON.parse(bytes);
};

// The declared DUR1 change set. tests/http/cmd/acceptance/durable_contract_test.go
// fixes the same names; a change to either list is a contract change.
const replacedSchemas = [
  'AuthPageIdentity', 'DueExpiryResult', 'DurableResult', 'Epoch', 'Error', 'ID',
  'IdempotencyResult', 'LiveEvent', 'PrepareOrderRequest', 'ReadinessAck', 'Registry',
  'RuntimeReadiness', 'RuntimeStatus', 'ServiceAuthorizationRequest', 'ServiceClaims', 'WOrderAttempt',
];
const addedSchemas = ['ExpiryDueList', 'ExpiryDueRestaurant', 'HealthReadiness', 'RegistryPoint', 'RegistryTombstone'];
const newOperations = ['GET /healthz', 'GET /internal/auth/v1/registry/{restaurantID}', 'GET /internal/warehouse/v2/expiry-due', 'GET /readyz'];
const markers = ['deprecated', 'x-umir-removal-gate'];
const patchedOperations = {
  'GET /internal/auth/v1/registry': ['description', 'parameters', 'responses'],
  'GET /internal/gateway/v2/runtime': markers,
  'PUT /internal/gateway/v2/r/{restaurantID}/registry': markers,
  'DELETE /internal/gateway/v2/r/{restaurantID}/registry': markers,
  'POST /internal/auth/v1/authorize': ['description'],
  'POST /internal/auth/v1/access-check': ['description'],
  'POST /internal/auth/v1/service-tokens': ['description'],
};
const deprecatedOperations = Object.keys(patchedOperations).filter((key) => patchedOperations[key] === markers);

const sorted = (values) => [...values].sort();
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const expectSet = (actual, expected, what) => {
  if (!same(sorted(actual), sorted(expected))) throw Error(`Unexpected ${what}: ${JSON.stringify(sorted(actual))}`);
};
const operationsOf = (document) => {
  const result = new Map();
  for (const [route, item] of Object.entries(document.paths)) {
    for (const [method, operation] of Object.entries(item)) {
      if (method !== 'parameters') result.set(`${method.toUpperCase()} ${route}`, operation);
    }
  }
  return result;
};

const profiles = ['public', 'private'].map((visibility) => ({
  visibility,
  base: read(`registration-v1-composed-${visibility}.json`),
  overlay: read(`durable-v1-${visibility}.json`),
}));

// Both profiles share schemas and policy; only operations differ.
const [first, second] = profiles.map(({ overlay }) => overlay);
for (const key of ['x-schema-replacements', 'x-durable-policy']) {
  if (!same(first[key], second[key])) throw Error(`Different public/private DUR1 ${key}`);
}
if (!same(first.components?.schemas, second.components?.schemas)) throw Error('Different public/private DUR1 schemas');

const outputs = [];
for (const { visibility, base, overlay } of profiles) {
  if (base.info.version !== '2.0.0-REG.1') throw Error('Expected REG1 predecessor');
  if (!overlay['x-durable-policy'] || typeof overlay['x-durable-policy'] !== 'object') throw Error('Missing DUR1 policy');
  const schemas = base.components.schemas;

  expectSet(Object.keys(overlay['x-schema-replacements'] ?? {}), replacedSchemas, 'DUR1 replacements');
  expectSet(Object.keys(overlay.components?.schemas ?? {}), addedSchemas, 'DUR1 additions');
  for (const name of replacedSchemas) {
    if (!Object.hasOwn(schemas, name)) throw Error(`Missing predecessor schema ${name}`);
  }
  for (const name of addedSchemas) {
    if (Object.hasOwn(schemas, name)) throw Error(`Schema collision ${name}`);
  }

  const wantNew = visibility === 'private' ? newOperations : [];
  const wantPatched = visibility === 'private' ? Object.keys(patchedOperations) : [];
  const overlayOperations = operationsOf(overlay);
  expectSet([...overlayOperations.keys()], wantNew, `${visibility} DUR1 operations`);
  for (const route of Object.keys(overlay.paths)) {
    if (Object.hasOwn(base.paths, route)) throw Error(`Operation collision ${route}`);
  }
  const identities = new Set([...operationsOf(base).values()].map((operation) => operation.operationId));
  for (const operation of overlayOperations.values()) {
    if (!operation.operationId || identities.has(operation.operationId)) throw Error(`Operation identity collision ${operation.operationId}`);
    identities.add(operation.operationId);
  }
  const patches = overlay['x-operation-patches'] ?? {};
  expectSet(Object.keys(patches), wantPatched, `${visibility} DUR1 operation patches`);
  const baseOperations = operationsOf(base);
  for (const [key, patch] of Object.entries(patches)) {
    if (!baseOperations.has(key)) throw Error(`Missing predecessor operation ${key}`);
    expectSet(Object.keys(patch), patchedOperations[key], `fields of ${key}`);
  }
  for (const key of deprecatedOperations.filter(() => visibility === 'private')) {
    const gate = patches[key].deprecated === true ? patches[key]['x-umir-removal-gate'] : undefined;
    if (!gate || gate.removeIn !== '2.0.0-DUR.2' || gate.order !== 'C7' || !Array.isArray(gate.requires) || gate.requires.length !== 3) {
      throw Error(`Incomplete removal gate for ${key}`);
    }
  }

  for (const [name, schema] of Object.entries(overlay['x-schema-replacements'])) schemas[name] = structuredClone(schema);
  for (const [name, schema] of Object.entries(overlay.components?.schemas ?? {})) schemas[name] = structuredClone(schema);
  for (const [route, item] of Object.entries(overlay.paths)) base.paths[route] = structuredClone(item);
  for (const [key, patch] of Object.entries(patches)) Object.assign(baseOperations.get(key), structuredClone(patch));

  base.info = {
    ...base.info,
    title: `Umir DUR1 composed ${visibility} profile`,
    version: '2.0.0-DUR.1',
    description: 'Durable-stack contract additions on REG1: storage generation, deprecated Gateway runtime operations, paged registry, order attempt Interrupted, expiry schedule, admission and limit codes, health. Frozen predecessor artifacts retain their own evidence.',
  };
  base['x-durable-source'] = inputs;
  base['x-durable-policy'] = structuredClone(overlay['x-durable-policy']);

  // Every internal reference must resolve in the composed document.
  const pending = [base];
  while (pending.length > 0) {
    const value = pending.pop();
    if (Array.isArray(value)) pending.push(...value);
    else if (value && typeof value === 'object') {
      for (const [key, child] of Object.entries(value)) {
        if (key === '$ref' && typeof child === 'string' && child.startsWith('#/')) {
          let target = base;
          for (const part of child.slice(2).split('/')) target = target?.[part.replaceAll('~1', '/').replaceAll('~0', '~')];
          if (target === undefined) throw Error(`Unresolved reference ${child}`);
        } else pending.push(child);
      }
    }
  }
  outputs.push([`durable-v1-composed-${visibility}.json`, JSON.stringify(base, null, 2) + '\n']);
}
for (const [name, bytes] of outputs) writeFileSync(path.join(directory, name), bytes);
