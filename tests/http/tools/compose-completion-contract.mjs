import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';

const directory = process.argv[2];
if (!directory) throw Error('Usage: node compose-completion-contract.mjs API_DIRECTORY');
const inputs = {};
const read = (name) => {
  const bytes = readFileSync(path.join(directory, name));
  inputs[name] = createHash('sha256').update(bytes).digest('hex');
  return JSON.parse(bytes);
};
const publicBase = read('ui-amendment-v1-composed-public.json');
const privateBase = read('ui-amendment-v1-composed-private.json');
const publicOverlay = read('completion-v1-public.json');
const privateOverlay = read('completion-v1-private.json');
const sharedReplacements = new Set(['ScopedRecommendations', 'ServingScopedRecommendations', 'AuthorizationResult']);
const outputs = [];
for (const [base, overlay, name] of [[publicBase, publicOverlay, 'public'], [privateBase, privateOverlay, 'private']]) {
  if (base.info.version !== '2.0.0-UA1.1') throw Error('Unexpected predecessor profile');
  for (const [key, schema] of Object.entries(publicOverlay.components.schemas)) {
    if (Object.hasOwn(base.components.schemas, key)) throw Error('Schema collision ' + key);
    base.components.schemas[key] = structuredClone(schema);
  }
  const replacements = { ...publicOverlay['x-schema-replacements'], ...overlay['x-schema-replacements'] };
  for (const [key, schema] of Object.entries(replacements)) {
    if (!sharedReplacements.has(key) && !(name === 'private' && key === 'UserClaims')) throw Error('Undeclared policy transition ' + key);
    if (!Object.hasOwn(base.components.schemas, key)) throw Error('Missing predecessor schema ' + key);
    base.components.schemas[key] = structuredClone(schema);
  }
  for (const [route, methods] of Object.entries(overlay.paths)) {
    if (Object.hasOwn(base.paths, route)) throw Error('Operation collision ' + route);
    base.paths[route] = structuredClone(methods);
  }
  base.info = { ...base.info, title: `Umir FIN1 composed ${name} profile`, version: '2.0.0-FIN.1', description: 'Explicit completion profile on UA1. Original C2 and UA1 artifacts/evidence remain separate.' };
  base['x-fin-source'] = inputs;
  base['x-fin-policy-transition'] = publicOverlay['x-policy-transition'];
  outputs.push([`completion-v1-composed-${name}.json`, JSON.stringify(base, null, 2) + '\n']);
}
for (const [name, bytes] of outputs) writeFileSync(path.join(directory, name), bytes);
