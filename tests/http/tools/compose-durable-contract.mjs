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
  'RegistryEntry', 'RuntimeReadiness', 'RuntimeStatus', 'ServiceAuthorizationRequest', 'ServiceClaims', 'WOrderAttempt',
];
const addedSchemas = ['ExpiryDueList', 'ExpiryDueRestaurant', 'HealthReadiness', 'RegistryPoint', 'RegistryTombstone'];
const newOperations = ['GET /healthz', 'GET /internal/auth/v1/registry/{restaurantID}', 'GET /internal/warehouse/v2/expiry-due', 'GET /readyz'];
const markers = ['deprecated', 'x-umir-removal-gate'];
// Existing operations whose listed top-level fields DUR1 sets, per profile.
// After a patch the script checks that it only added to the predecessor.
const patchedOperations = {
  private: {
    'GET /internal/auth/v1/registry': ['description', 'parameters', 'responses'],
    'GET /internal/gateway/v2/runtime': markers,
    'PUT /internal/gateway/v2/r/{restaurantID}/registry': markers,
    'DELETE /internal/gateway/v2/r/{restaurantID}/registry': markers,
    'POST /internal/auth/v1/authorize': ['description'],
    'POST /internal/auth/v1/access-check': ['description'],
    'POST /internal/auth/v1/service-tokens': ['description'],
  },
  public: {
    'GET /api/v2/r/{restaurantID}/live': ['responses'],
  },
};
// Response codes a patch may add to the predecessor's responses.
const addedResponses = {
  'GET /internal/auth/v1/registry': ['304'],
  'GET /api/v2/r/{restaurantID}/live': ['429'],
};
const deprecatedOperations = Object.keys(patchedOperations.private).filter((key) => patchedOperations.private[key] === markers);
const healthOperations = ['GET /healthz', 'GET /readyz'];
const overlayKeys = ['openapi', 'info', 'paths', 'components', 'x-schema-replacements', 'x-operation-patches', 'x-durable-policy'];

const sorted = (values) => [...values].sort();
// Structural equality; object key order does not matter.
const canonical = (value) => JSON.stringify(value, (_, member) => (member && typeof member === 'object' && !Array.isArray(member)
  ? Object.fromEntries(Object.entries(member).sort(([x], [y]) => (x < y ? -1 : x > y ? 1 : 0)))
  : member));
const same = (a, b) => canonical(a) === canonical(b);
const expectSet = (actual, expected, what) => {
  if (!same(sorted(actual), sorted(expected))) throw Error(`Unexpected ${what}: ${JSON.stringify(sorted(actual))}`);
};
const nonEmpty = (value) => typeof value === 'string' && value.trim() !== '';
const operationsOf = (document) => {
  const result = new Map();
  for (const [route, item] of Object.entries(document.paths)) {
    for (const [method, operation] of Object.entries(item)) {
      if (method !== 'parameters') result.set(`${method.toUpperCase()} ${route}`, operation);
    }
  }
  return result;
};

// A patch only adds to the predecessor: parameters and responses stay, and the
// only new response codes are the declared ones.
const checkPatch = (key, prior, next, fields) => {
  if (fields.includes('description') && !nonEmpty(next.description)) throw Error(`Empty description of ${key}`);
  if (fields.includes('parameters')) {
    const parameters = next.parameters ?? [];
    for (const parameter of prior.parameters ?? []) {
      if (!parameters.some((candidate) => same(candidate, parameter))) throw Error(`Patch of ${key} drops a predecessor parameter`);
    }
    const identities = parameters.map((parameter) => `${parameter.in}/${parameter.name}`);
    if (new Set(identities).size !== identities.length) throw Error(`Patch of ${key} repeats a parameter`);
  }
  if (fields.includes('responses')) {
    const responses = next.responses;
    if (!responses || typeof responses !== 'object' || Object.keys(responses).length === 0) throw Error(`Empty responses of ${key}`);
    for (const [code, response] of Object.entries(prior.responses)) {
      const patched = responses[code];
      if (!patched) throw Error(`Patch of ${key} drops response ${code}`);
      if (!same(patched.content, response.content)) throw Error(`Patch of ${key} changes the content of response ${code}`);
      if (code === 'default' && !same(patched, response)) throw Error(`Patch of ${key} changes the default response`);
      for (const [name, header] of Object.entries(response.headers ?? {})) {
        if (!same(patched.headers?.[name], header)) throw Error(`Patch of ${key} changes header ${name} of response ${code}`);
      }
    }
    const added = Object.keys(responses).filter((code) => !Object.hasOwn(prior.responses, code));
    expectSet(added, addedResponses[key] ?? [], `responses added to ${key}`);
    for (const code of added) if (!nonEmpty(responses[code].description)) throw Error(`Added response ${code} of ${key} has no description`);
  }
};

const profiles = ['public', 'private'].map((visibility) => ({
  visibility,
  base: read(`registration-v1-composed-${visibility}.json`),
  overlay: read(`durable-v1-${visibility}.json`),
}));

for (const { visibility, overlay } of profiles) {
  for (const key of Object.keys(overlay)) {
    if (!overlayKeys.includes(key)) throw Error(`Unexpected overlay key ${key} in ${visibility} DUR1 overlay`);
  }
  if (!same(Object.keys(overlay.components ?? {}), ['schemas'])) throw Error(`Unexpected overlay components in ${visibility} DUR1 overlay`);
}

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
  const declaredPatches = patchedOperations[visibility];
  const wantPatched = Object.keys(declaredPatches);
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
    expectSet(Object.keys(patch), declaredPatches[key], `fields of ${key}`);
  }
  for (const [key, operation] of overlayOperations) {
    if (!nonEmpty(operation.description) || !nonEmpty(operation['x-owner']) || !nonEmpty(operation['x-required-capability'])) throw Error(`Incomplete new operation ${key}`);
    const responses = operation.responses;
    if (!responses || typeof responses !== 'object' || !responses['200']) throw Error(`New operation ${key} needs a 200 response`);
    for (const response of Object.values(responses)) if (!nonEmpty(response.description)) throw Error(`New operation ${key} has a response without description`);
    if (healthOperations.includes(key)) {
      if (!same(operation.security, [])) throw Error(`Health operation ${key} must declare security []`);
    } else if (Object.hasOwn(operation, 'security') && (!Array.isArray(operation.security) || operation.security.length === 0)) {
      throw Error(`New operation ${key} must not open itself with empty security`);
    }
  }
  const priorOperations = new Map([...baseOperations].map(([key, operation]) => [key, structuredClone(operation)]));
  for (const key of deprecatedOperations.filter(() => visibility === 'private')) {
    const gate = patches[key].deprecated === true ? patches[key]['x-umir-removal-gate'] : undefined;
    if (!gate || gate.removeIn !== '2.0.0-DUR.2' || gate.order !== 'C7' || !Array.isArray(gate.requires) || gate.requires.length !== 3) {
      throw Error(`Incomplete removal gate for ${key}`);
    }
  }

  for (const [name, schema] of Object.entries(overlay['x-schema-replacements'])) schemas[name] = structuredClone(schema);
  for (const [name, schema] of Object.entries(overlay.components?.schemas ?? {})) schemas[name] = structuredClone(schema);
  for (const [route, item] of Object.entries(overlay.paths)) base.paths[route] = structuredClone(item);
  for (const [key, patch] of Object.entries(patches)) {
    const operation = baseOperations.get(key);
    Object.assign(operation, structuredClone(patch));
    checkPatch(key, priorOperations.get(key), operation, declaredPatches[key]);
  }

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
