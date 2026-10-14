import {readFileSync, writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import path from 'node:path';

const directory = process.argv[2];
if (!directory) throw Error('Usage: node compose-ui-contract.mjs API_DIRECTORY');
const inputs = {};
const read = name => {
  const bytes = readFileSync(path.join(directory, name));
  inputs[name] = createHash('sha256').update(bytes).digest('hex');
  return JSON.parse(bytes);
};
const pub = read('backend-v2-public.json');
const priv = read('backend-v2-private.json');
const uaPub = read('ui-amendment-v1-public.json');
const uaPriv = read('ui-amendment-v1-private.json');
const rewrite = value => {
  if (Array.isArray(value)) return value.map(rewrite);
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(Object.entries(value).map(([key, child]) => {
    if (key === '$ref' && !child.startsWith('#')) {
      const [file, fragment] = child.split('#');
      if (!['backend-v2-public.json', 'backend-v2-private.json', 'ui-amendment-v1-public.json'].includes(file) || !fragment) throw Error('Unknown external reference');
      return [key, '#'+fragment];
    }
    return [key, rewrite(child)];
  }));
};
const newSchemas = rewrite(uaPub.components.schemas);
for (const [base, overlay, name] of [[pub, uaPub, 'public'], [priv, uaPriv, 'private']]) {
  for (const [key, schema] of Object.entries(newSchemas)) {
    if (Object.hasOwn(base.components.schemas, key)) throw Error('Schema collision '+key);
    base.components.schemas[key] = schema;
  }
  for (const [route, methods] of Object.entries(overlay.paths)) {
    base.paths[route] ??= {};
    for (const [method, op] of Object.entries(methods)) base.paths[route][method] = rewrite(op);
  }
  base.info = {...base.info, title: `Umir UA1 composed ${name} profile`, version: '2.0.0-UA1.1',
    description: 'Generated additive UA1 profile. Original C2 artifacts and acceptance remain separate.'};
  base['x-ua1-source'] = inputs;
  writeFileSync(path.join(directory, `ui-amendment-v1-composed-${name}.json`), JSON.stringify(base, null, 2)+'\n');
}
console.log('Composed UA1 public/private profiles; predecessor inputs remain unchanged.');
