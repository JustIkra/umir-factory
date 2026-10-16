// Signs in as the initial operator of a demo bundle and reads what that account
// may see. Used by completion-acceptance around a certificate rotation and a
// restore; the output never holds a password or a token.
//
//   node session-check.mjs login   --directory <bundle> --session-file <file>
//   node session-check.mjs verify  --session-file <file>
//   node session-check.mjs signin  --directory <bundle>
//
// login   signs in, reads /api/v2/access and keeps the cookies in a private file;
// verify  reads /api/v2/access with the kept cookies (the earlier session works);
// signin  signs in again and reads /api/v2/access (a fresh login works).
// Prints {"organizations": n}; exit status 1 when a step is refused.
import { randomUUID } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";

const [mode, ...rest] = process.argv.slice(2);
const options = Object.fromEntries(
  rest.reduce((pairs, value, i) => {
    if (i % 2 === 0) pairs.push([value.replace(/^--/, ""), rest[i + 1]]);
    return pairs;
  }, []),
);

async function call(origin, jar, method, path, body) {
  let response;
  try {
    response = await fetch(origin + path, {
      method,
      redirect: "manual",
      signal: AbortSignal.timeout(10000),
      headers: {
        Accept: "application/json",
        Origin: origin,
        ...(Object.keys(jar.cookies).length
          ? { Cookie: Object.entries(jar.cookies).map(([k, v]) => `${k}=${v}`).join("; ") }
          : {}),
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
        ...(method !== "GET" && jar.csrf ? { "X-CSRF-Token": jar.csrf } : {}),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
  } catch (error) {
    const code = error?.cause?.code;
    throw new Error(`${method} ${path} could not reach ${origin}${code ? ` (${code})` : ""}`);
  }
  for (const cookie of response.headers.getSetCookie()) {
    const pair = cookie.split(";", 1)[0];
    const at = pair.indexOf("=");
    if (pair.slice(at + 1) === "") delete jar.cookies[pair.slice(0, at)];
    else jar.cookies[pair.slice(0, at)] = pair.slice(at + 1);
  }
  const value = await response.json();
  if (!response.ok) throw new Error(`${method} ${path} refused with HTTP ${response.status}`);
  if (typeof value.csrfToken === "string") jar.csrf = value.csrfToken;
  if (typeof value.session?.csrfToken === "string") jar.csrf = value.session.csrfToken;
  return value;
}

async function signIn(directory) {
  const bundle = JSON.parse(readFileSync(join(directory, "bundle.json"), "utf8"));
  const config = JSON.parse(readFileSync(join(directory, "auth/config.json"), "utf8"));
  const password = readFileSync(join(directory, "auth/secrets/password"), "utf8");
  const origin = bundle.binding.origin;
  const jar = { cookies: {}, csrf: "" };
  await call(origin, jar, "GET", "/api/v2/csrf");
  await call(origin, jar, "POST", "/api/v2/session", {
    commandID: randomUUID(),
    login: config.initialUser.login,
    password,
    rememberMe: false,
  });
  return { origin, jar };
}

try {
  let origin, jar;
  if (mode === "login" || mode === "signin") {
    if (!options.directory || (mode === "login" && !options["session-file"])) throw new Error("usage");
    ({ origin, jar } = await signIn(resolve(options.directory)));
    if (mode === "login")
      writeFileSync(resolve(options["session-file"]), JSON.stringify({ origin, jar }), { mode: 0o600 });
  } else if (mode === "verify") {
    if (!options["session-file"]) throw new Error("usage");
    ({ origin, jar } = JSON.parse(readFileSync(resolve(options["session-file"]), "utf8")));
  } else throw new Error("usage: session-check.mjs login|verify|signin");
  const access = await call(origin, jar, "GET", "/api/v2/access");
  console.log(JSON.stringify({ organizations: access.organizations.length }));
} catch (error) {
  console.error(error instanceof Error ? error.message : "session check failed");
  process.exitCode = 1;
}
