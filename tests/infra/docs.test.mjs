import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const read = (name) => readFileSync(path.join(root, name), 'utf8');
const readme = read('README.md');
const businessCore = read('docs/business-core.md');
const currentPlan = read('docs/current-plan.md');

// A section is the text from its heading up to the next heading of the same or higher level.
function section(text, heading) {
  const level = heading.match(/^#+/)[0].length;
  const lines = text.split('\n');
  const start = lines.findIndex((line) => line.trim() === heading);
  assert.notEqual(start, -1, `missing heading ${heading}`);
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i += 1) {
    const match = lines[i].match(/^(#+)\s/);
    if (match && match[1].length <= level) {
      end = i;
      break;
    }
  }
  return lines.slice(start, end).join('\n');
}

function withoutCode(text) {
  return text.replace(/^```[\s\S]*?^```/gm, '').replace(/`[^`\n]*`/g, '');
}

test('stale statements are gone from README and business-core', () => {
  const forbidden = [
    'Публичная OpenAPI',
    'Публичной OpenAPI',
    'no public self-signup',
    'Публичная сборка на ВМ ещё не развёрнута',
    'пять репозиториев',
    'C2 approval is required before implementation',
    'интерфейс ещё не реализован в main frontend',
    'хранилища в памяти',
  ];
  for (const [name, text] of [['README.md', readme], ['docs/business-core.md', businessCore]]) {
    for (const phrase of forbidden) {
      assert.ok(!text.includes(phrase), `${name} still says «${phrase}»`);
    }
  }
});

test('the profile map names the canonical contract and its consumers', () => {
  const map = section(readme, '## Профили и контракты');
  assert.match(map, /registration-v1-composed-/, 'REG1 composed contract is canonical for deployment');
  assert.match(map, /каноническ/i);
  for (const profile of ['compose.completion.yaml', 'compose.completion.build.yaml', 'compose.yaml', 'compose.dev.yaml', 'compose.auth.yaml', 'compose.feedback.yaml', 'compose.legacy-public.yaml']) {
    assert.ok(map.includes(`\`${profile}\``), `the map does not mention ${profile}`);
  }
  const rows = map.split('\n').filter((line) => line.startsWith('|'));
  const row = (needle) => rows.find((line) => line.includes(needle));
  assert.match(row('`compose.completion.yaml`'), /registration-v1-composed-/);
  assert.match(row('`compose.yaml`'), /legacy/i);
  assert.match(row('docs/api/openapi.yml'), /исторически/i);
  // every row has the three columns: profile, contract, consumer
  for (const line of rows) {
    assert.equal(line.split('|').length - 2, 3, `row must have three columns: ${line.slice(0, 80)}`);
  }
});

test('README states the clock requirement R12', () => {
  assert.match(readme, /R12/);
  const text = readme.split('\n').filter((line) => line.includes('R12')).join('\n');
  assert.match(text, /одной ВМ/);
  assert.match(text, /часы|часов/);
  assert.match(text, /`iat`/);
  assert.match(text, /нулев/);
});

test('local build section covers Auth and Feedback', () => {
  const build = section(readme, '## Локальная сборка исходников');
  assert.match(build, /git clone https:\/\/github\.com\/JustIkra\/umir-auth\.git/);
  assert.match(build, /git clone https:\/\/github\.com\/JustIkra\/umir-feedback\.git/);
  assert.match(build, /├── umir-auth\//);
  assert.match(build, /[├└]── umir-feedback\//);
});

test('current plan records the delivery without rewriting history', () => {
  assert.match(currentPlan, /FIN1 доставлен 15\.10, см\. `completion-stand\.json`/);
  // the original statement of 15.10 is kept as history
  assert.match(currentPlan, /Публикация плана: 15\.10\.2026/);
});

test('every local markdown link resolves to an existing file', () => {
  const files = ['README.md', ...readdirSync(path.join(root, 'docs')).filter((name) => name.endsWith('.md')).map((name) => `docs/${name}`)];
  const broken = [];
  for (const file of files) {
    const text = withoutCode(read(file));
    for (const match of text.matchAll(/\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g)) {
      const target = match[1];
      if (/^[a-z][a-z0-9+.-]*:/i.test(target) || target.startsWith('#')) continue;
      const clean = decodeURIComponent(target.split('#')[0]);
      if (!existsSync(path.resolve(root, path.dirname(file), clean))) broken.push(`${file} -> ${target}`);
    }
  }
  assert.deepEqual(broken, []);
});
