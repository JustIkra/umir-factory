import assert from 'node:assert/strict';
import { randomBytes, randomUUID } from 'node:crypto';
import { writeFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';

const args = Object.fromEntries(process.argv.slice(2).reduce((pairs, arg, i, all) => {
  if (i % 2 === 0) pairs.push([arg.replace(/^--/, ''), all[i + 1]]);
  return pairs;
}, []));
for (const name of ['origin', 'mailpit', 'output']) assert(args[name], `Missing --${name}`);
const origin = new URL(args.origin).origin;
const email = `registration-${randomUUID()}@umir.invalid`;
const password = randomBytes(12).toString('hex');
const rows = [];
const actor = () => ({ cookies: {}, csrf: '' });
async function call(who, method, route, input, expected = 200) {
  const response = await fetch(new URL(route, origin), {
    method, signal: AbortSignal.timeout(10000),
    headers: { Origin: origin, 'Content-Type': 'application/json',
      Cookie: Object.entries(who.cookies).map(([key, value]) => `${key}=${value}`).join('; '),
      'X-CSRF-Token': who.csrf },
    ...(input === undefined ? {} : { body: JSON.stringify(input) }),
  });
  const value = await response.json();
  assert.equal(response.status, expected, `${method} ${route}: ${value?.code ?? 'unexpected status'}`);
  for (const cookie of response.headers.getSetCookie()) {
    assert(cookie.includes('HttpOnly') && cookie.includes('SameSite=Strict') && cookie.includes('Path=/'));
    if (origin.startsWith('https:')) assert(cookie.includes('Secure'));
    const [pair] = cookie.split(';');
    const at = pair.indexOf('=');
    const key = pair.slice(0, at), token = pair.slice(at + 1);
    if (token) who.cookies[key] = token;
    else delete who.cookies[key];
  }
  if (value.csrfToken) who.csrf = value.csrfToken;
  if (value.session?.csrfToken) who.csrf = value.session.csrfToken;
  for (const forbidden of ['sessionToken', 'preauthToken', 'verifier', 'encryptedResponse']) assert(!JSON.stringify(value).includes(`"${forbidden}"`));
  rows.push({ method, route, status: response.status });
  return value;
}
async function messages() {
  const response = await fetch(new URL('/api/v1/messages', args.mailpit), { signal: AbortSignal.timeout(5000) });
  assert.equal(response.status, 200);
  return (await response.json()).messages;
}
async function receivedCode(challenge) {
  for (let attempt = 0; attempt < 60; attempt++) {
    for (const message of await messages()) {
      if (!message.To?.some((to) => to.Address === email) || !message.MessageID?.includes(`registration.${challenge}.`)) continue;
      const response = await fetch(new URL(`/api/v1/message/${message.ID}`, args.mailpit), { signal: AbortSignal.timeout(5000) });
      assert.equal(response.status, 200);
      const value = await response.json();
      const grouped = value.Text?.match(/\b([0-9]{3})\s+([0-9]{3})\b/);
      if (grouped) return grouped[1] + grouped[2];
    }
    await delay(500);
  }
  throw Error('Registration email was not delivered to the SMTP receiver');
}
const command = (body) => ({ commandID: randomUUID(), ...body });
const browser = actor();
await call(browser, 'GET', '/api/v2/csrf');
const begin = command({ email, purpose: 'registration' });
const challenge = await call(browser, 'POST', '/api/v2/email-challenges', begin);
assert.deepEqual(await call(browser, 'POST', '/api/v2/email-challenges', begin), challenge);
const code = await receivedCode(challenge.challengeID);
const proof = await call(browser, 'POST', `/api/v2/challenges/${challenge.challengeID}/verify`, command({ challengeID: challenge.challengeID, code }));
const complete = command({ challengeID: challenge.challengeID, proof: proof.proof, purpose: 'registration', displayName: 'Registration User', password, rememberMe: true });
await call(browser, 'POST', `/api/v2/email-challenges/${challenge.challengeID}/complete`, { ...complete, proof: 'invalid-proof' }, 404);
await call(browser, 'POST', `/api/v2/email-challenges/${challenge.challengeID}/complete`, { ...complete, newPassword: '' }, 400);
const retained = structuredClone(browser);
const view = await call(browser, 'POST', `/api/v2/email-challenges/${challenge.challengeID}/complete`, complete);
assert.equal(view.user.email, email);
assert.deepEqual(await call(retained, 'POST', `/api/v2/email-challenges/${challenge.challengeID}/complete`, complete), view);
const signedIn = await call(browser, 'GET', '/api/v2/me');
assert.equal(signedIn.user.userID, view.user.userID);
await call(browser, 'POST', '/api/v2/email-challenges', command({ email: `other-${email}`, purpose: 'registration' }), 403);
const loginBrowser = actor();
await call(loginBrowser, 'GET', '/api/v2/csrf');
const loggedIn = await call(loginBrowser, 'POST', '/api/v2/session', command({ login: view.user.login, password, rememberMe: false }));
assert.equal(loggedIn.user.userID, view.user.userID);
const organization = await call(loginBrowser, 'POST', '/api/v2/orgs', command({ name: 'Registration Acceptance', timeZone: 'Europe/Moscow', firstRestaurantName: 'Registration Restaurant' }), 201);
assert(organization.orgID && organization.operationID);
const probe = actor();
await call(probe, 'GET', '/api/v2/csrf');
const occupied = await call(probe, 'POST', '/api/v2/email-challenges', command({ email, purpose: 'registration' }));
assert.equal(occupied.state, 'pending');
await delay(1500);
assert(!(await messages()).some((message) => message.MessageID?.includes(`registration.${occupied.challengeID}.`)), 'Occupied address received a registration OTP');
const receipt = { profile: 'REG1', origin, passed: true, checks: ['nginx-gateway-auth', 'authenticated-starttls-smtp', 'registration', 'same-command-recovery', 'strict-fields', 'signed-in-refusal', 'password-login', 'organization-creation', 'occupied-address-suppression'], userID: view.user.userID, orgID: organization.orgID, rows };
writeFileSync(args.output, JSON.stringify(receipt, null, 2) + '\n', { mode: 0o600 });
console.log(JSON.stringify({ passed: true, checks: receipt.checks, requestCount: rows.length }));
