import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';

const directory = process.argv[2];
if (!directory) throw Error('Usage: node compose-registration-contract.mjs API_DIRECTORY');
const inputs = {};
const read = (name) => {
  const bytes = readFileSync(path.join(directory, name));
  inputs[name] = createHash('sha256').update(bytes).digest('hex');
  return JSON.parse(bytes);
};
const profiles = ['public', 'private'].map((visibility) => ({
  visibility,
  base: read(`completion-v1-composed-${visibility}.json`),
  overlay: read(`registration-v1-${visibility}.json`),
}));
const allowed = ['ClientCommandID', 'EmailChallengeBegin', 'EmailChallengeComplete'];
for (const { base, overlay } of profiles) {
  if (base.info.version !== '2.0.0-FIN.1') throw Error('Expected FIN1 predecessor');
  if (Object.keys(overlay.paths).length !== 0) throw Error('REG1 reuses existing operations');
  if (JSON.stringify(Object.keys(overlay['x-schema-replacements']).sort()) !== JSON.stringify(allowed)) throw Error('Unexpected REG1 replacements');
}
if (JSON.stringify(profiles[0].overlay['x-schema-replacements']) !== JSON.stringify(profiles[1].overlay['x-schema-replacements'])) throw Error('Different public/private REG1 schemas');
for (const { visibility, base, overlay } of profiles) {
  for (const [name, schema] of Object.entries(overlay['x-schema-replacements'])) {
    if (!Object.hasOwn(base.components.schemas, name)) throw Error('Missing predecessor schema ' + name);
    base.components.schemas[name] = schema;
  }
  base.info = { ...base.info, title: `Umir REG1 composed ${visibility} profile`, version: '2.0.0-REG.1', description: 'Configurable public email registration on FIN1. Frozen predecessor artifacts retain their own evidence.' };
  base['x-registration-source'] = inputs;
  writeFileSync(path.join(directory, `registration-v1-composed-${visibility}.json`), JSON.stringify(base, null, 2) + '\n');
}
