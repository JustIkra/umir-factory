import { X509Certificate, createHash, randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  chmodSync,
  closeSync,
  fsyncSync,
  openSync,
  renameSync,
  writeSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sourceFile = fileURLToPath(new URL("./sources.json", import.meta.url));
const lockFile = fileURLToPath(
  new URL("./images.lock.json", import.meta.url),
);
// What a bundle is bound to: the reviewed source pins and the image identities
// locked for them (scripts/completion/images.lock.json, made by `umir-checks
// lock-images`).
const sourceDigest = () =>
  sha(
    Buffer.concat([readFileSync(sourceFile), Buffer.from("\0"), readFileSync(lockFile)]),
  );
const sha = (body) => createHash("sha256").update(body).digest("hex");
const noControls = (value) =>
  typeof value === "string" && value !== "" && !/[\r\n\0$]/.test(value);
function privateFile(path) {
  if (!isAbsolute(path)) throw new Error("private input path must be absolute");
  const stat = lstatSync(path);
  if (
    stat.isSymbolicLink() ||
    !stat.isFile() ||
    stat.nlink !== 1 ||
    (stat.mode & 0o777) !== 0o600 ||
    stat.uid !== process.getuid()
  )
    throw new Error(
      "private input must be an owned regular 0600 file, without a symlink",
    );
  if (realpathSync(path) !== path || stat.size > 1048576)
    throw new Error("private input path or size is invalid");
  return readFileSync(path);
}
function inventory(directory, prefix = "") {
  return readdirSync(join(directory, prefix))
    .sort()
    .flatMap((name) => {
      if (prefix === "" && (name === ".generations" || name === ".next"))
        return [];
      const path = join(prefix, name);
      const full = join(directory, path);
      const stat = lstatSync(full);
      if (stat.isSymbolicLink())
        throw new Error("private bundle symlink refused");
      if (stat.isDirectory()) return inventory(directory, path);
      if (!stat.isFile() || stat.nlink !== 1 || (stat.mode & 0o777) !== 0o600)
        throw new Error("private bundle permissions changed");
      return path === "bundle.json"
        ? []
        : [{ path, sha256: sha(readFileSync(full)) }];
    });
}
function write(directory, path, value) {
  const destination = join(directory, path);
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  writeFileSync(destination, value, { mode: 0o600, flag: "wx" });
}
const CA_DAYS = 3650;
const LEAF_DAYS = 365;
const KEEP_GENERATIONS = 5;
const TARGETS = ["auth", "transport", "trust", "health", "keys"];
// A bundle made before the "keys" target existed has no such directory.
const presentTargets = (directory) =>
  TARGETS.filter((target) => existsSync(join(directory, target)));
// Keys of the service Feedback, delivered as files (name -> bundle key name).
const FEEDBACK_KEY_FILES = {
  FEEDBACK_GATEWAY_KEY: "gateway-feedback",
  FEEDBACK_AUTH_DISCOVERY_KEY: "feedback-auth",
};
// The keys of a bundle that still has them in env/feedback.env.
function legacyKeyFiles(directory) {
  const path = join(directory, "env", "feedback.env");
  if (!existsSync(path)) return [];
  const values = Object.fromEntries(
    readFileSync(path, "utf8")
      .split("\n")
      .filter(Boolean)
      .map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)]),
  );
  return Object.entries(FEEDBACK_KEY_FILES)
    .filter(([variable]) => values[variable] !== undefined)
    .map(([variable, name]) => [`keys/${name}`, Buffer.from(values[variable])]);
}
const CA_PHASES = ["trust", "serve", "retire"];
const LEGACY_READY = "completion-private-v1\n";
const SERVER_EXT =
  "subjectAltName=DNS:auth,DNS:transport\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\n";
function openssl(args, cwd) {
  try {
    execFileSync("openssl", args, { cwd, stdio: "ignore", timeout: 10000 });
  } catch {
    throw new Error("private TLS certificate generation failed");
  }
}
// Random positive serial number of 127 bits, never 0 or 1.
function serial() {
  for (;;) {
    const bytes = randomBytes(16);
    bytes[0] &= 0x7f;
    const hex = bytes.toString("hex");
    if (BigInt("0x" + hex) > 1n) return "0x" + hex;
  }
}
function createCA(cwd, key, certificate, subject) {
  openssl(
    [
      "req",
      "-x509",
      "-newkey",
      "ec",
      "-pkeyopt",
      "ec_paramgen_curve:P-256",
      "-nodes",
      "-sha256",
      "-days",
      String(CA_DAYS),
      "-set_serial",
      serial(),
      "-subj",
      subject,
      "-keyout",
      key,
      "-out",
      certificate,
    ],
    cwd,
  );
}
// Needs ca.crt, ca.key and server.ext in cwd; writes server.key/.csr/.crt.
function createLeaf(cwd) {
  openssl(
    [
      "req",
      "-newkey",
      "ec",
      "-pkeyopt",
      "ec_paramgen_curve:P-256",
      "-nodes",
      "-sha256",
      "-subj",
      "/CN=transport",
      "-keyout",
      "server.key",
      "-out",
      "server.csr",
    ],
    cwd,
  );
  openssl(
    [
      "x509",
      "-req",
      "-sha256",
      "-days",
      String(LEAF_DAYS),
      "-in",
      "server.csr",
      "-CA",
      "ca.crt",
      "-CAkey",
      "ca.key",
      "-set_serial",
      serial(),
      "-extfile",
      "server.ext",
      "-out",
      "server.crt",
    ],
    cwd,
  );
}
function tls(directory) {
  const cwd = join(directory, "tls");
  mkdirSync(cwd, { mode: 0o700 });
  write(directory, "tls/server.ext", SERVER_EXT);
  createCA(cwd, "ca.key", "ca.crt", "/CN=Umir completion private CA");
  createLeaf(cwd);
  for (const name of readdirSync(cwd)) chmodSync(join(cwd, name), 0o600);
}
const months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");
function notAfter(pem) {
  const m = /^(\w{3})\s+(\d+) (\d\d):(\d\d):(\d\d) (\d{4}) GMT$/.exec(
    new X509Certificate(pem).validTo,
  );
  if (m === null) throw new Error("unreadable certificate validity");
  return new Date(
    Date.UTC(+m[6], months.indexOf(m[1]), +m[2], +m[3], +m[4], +m[5]),
  ).toISOString();
}
const tlsMetadata = (files) => ({
  caNotAfter: notAfter(files.get("ca.crt")),
  leafNotAfter: notAfter(files.get("server.crt")),
});

function transportConfig() {
  return [
    [8442, "warehouse:8082"],
    [8443, "serving:8080"],
    [8444, "gateway:8086"],
    [8445, "feedback:8084"],
  ]
    .map(
      ([port, target]) => `server {
  listen ${port} ssl;
  server_name transport;
  ssl_certificate /run/transport/server.crt;
  ssl_certificate_key /run/transport/server.key;
  ssl_protocols TLSv1.2 TLSv1.3;
  resolver 127.0.0.11 valid=5s ipv6=off;
  client_max_body_size 1m;
  location / {
    set $owner ${target};
    proxy_pass http://$owner;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_read_timeout 65s;
  }
}`,
    )
    .join("\n");
}
function validateOrigin(origin, cookieMode) {
  let value;
  try {
    value = new URL(origin);
  } catch {
    throw new Error("invalid browser origin");
  }
  if (
    value.origin !== origin ||
    value.username ||
    value.password ||
    value.search ||
    value.hash
  )
    throw new Error("origin must be exact ASCII origin");
  if (cookieMode === "secure") {
    if (value.protocol !== "https:")
      throw new Error("secure cookies require HTTPS origin");
  } else if (
    cookieMode !== "loopback-development" ||
    value.protocol !== "http:" ||
    !["127.0.0.1", "localhost", "[::1]"].includes(value.hostname)
  )
    throw new Error("loopback cookies require a loopback HTTP origin");
}
// Required services plus optional ones (Feedback): one place for every image pin.
const pins = (manifest) => ({
  ...manifest.services,
  ...manifest.optionalServices,
});
const originVariables = (origin, cookieMode) => ({
  COMPLETION_ORIGIN: origin,
  COMPLETION_COOKIE_MODE: cookieMode,
  COMPLETION_WEB_PORT:
    new URL(origin).port || (cookieMode === "secure" ? "8080" : "80"),
});
// Rewrites only the browser-origin variables and keeps every image identity.
function patchOrigin(text, origin, cookieMode) {
  let patched = text;
  for (const [name, value] of Object.entries(
    originVariables(origin, cookieMode),
  )) {
    const line = new RegExp(`^${name}=.*$`, "m");
    if (!line.test(patched)) throw new Error(`compose.env lacks ${name}`);
    patched = patched.replace(line, () => `${name}=${value}`);
  }
  return patched;
}
// Image of a service: the exact source tag plus the locked digest of its
// linux/amd64 manifest. A service the lock does not know is refused; an optional
// service the registry does not publish (Feedback) keeps its exact-source tag.
function imageReference(service, revision) {
  let lock;
  try {
    lock = JSON.parse(readFileSync(lockFile, "utf8"));
  } catch {
    throw new Error("cannot read scripts/completion/images.lock.json (image lock)");
  }
  const repository = `ghcr.io/justikra/umir-${service}`;
  const locked = lock.services?.[service];
  if (locked !== undefined) {
    if (locked.sourceSHA !== revision || !/^sha256:[0-9a-f]{64}$/.test(locked.platformDigest ?? ""))
      throw new Error(
        `image lock does not match the ${service} source ${revision}: run umir-checks lock-images --write and review the result`,
      );
    return `${repository}:sha-${revision}@${locked.platformDigest}`;
  }
  if (lock.unpublished?.[service] === revision)
    return `${repository}:sha-${revision}`;
  throw new Error(
    `no image lock entry for ${service} at ${revision}: run umir-checks lock-images --write and review the result`,
  );
}
function composeEnvironment(
  { directory, project, origin, cookieMode, sources },
  manifest,
) {
  const compose = {
    COMPOSE_PROJECT_NAME: project,
    COMPLETION_PRIVATE_DIR: directory,
    ...originVariables(origin, cookieMode),
    ...Object.fromEntries(
      Object.entries(pins(manifest)).map(([service, revision]) => [
        `COMPLETION_${service.toUpperCase()}_IMAGE`,
        imageReference(service, revision),
      ]),
    ),
    ...Object.fromEntries(
      Object.entries(pins(manifest)).map(([service, revision]) => [
        `COMPLETION_${service.toUpperCase()}_SHA`,
        revision,
      ]),
    ),
    ...(sources === undefined ? {} : { COMPLETION_SOURCES: sources }),
  };
  return Object.entries(compose)
    .map(([key, value]) => `${key}=${value}\n`)
    .join("");
}
function replacePrivate(directory, name, body) {
  const path = join(directory, name);
  const temporary = path + "." + randomBytes(12).toString("hex") + ".tmp";
  const descriptor = openSync(temporary, "wx", 0o600);
  try {
    writeSync(descriptor, body);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  renameSync(temporary, path);
  syncDirectory(directory);
}
function syncDirectory(directory) {
  const descriptor = openSync(directory, "r");
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

// ---- Generations -----------------------------------------------------------
//
// A bundle has a generation number N (bundle.json "generation"; a bundle made
// before generations existed counts as generation 0). The four targets
// auth, transport, trust and health hold generation N; each carries a
// ".ready" marker with exactly two lines:
//
//   completion-private-v2 generation=<N>
//   tree-sha256=<64 hex digits>
//
// tree-sha256 of a target is the SHA-256 of this listing, one line per regular
// file below the target except the top-level ".ready", ordered by the byte
// order of the path (LC_ALL=C sort):
//
//   <sha256 of the file content as 64 lowercase hex digits>  <relative path>\n
//
// (two spaces: it is the `sha256sum` output). It is reproducible in a busybox
// shell without NUL bytes:
//
//   cd <target> && find . -type f ! -path ./.ready | sed 's|^\./||' \
//     | LC_ALL=C sort | while read -r f; do sha256sum "$f"; done | sha256sum
//
// Paths are limited to [A-Za-z0-9._-] segments joined by "/" so the shell reads
// them back unchanged. private-init uses the same listing.
//
// A new generation is written completely to <dir>/.next (targets, tls,
// compose.env when it changes, state.json), the generation it replaces is
// copied to <dir>/.generations/<N>/ (targets, tls, meta.json), then the marker
// ".next/COMMIT" is written and the pieces are renamed into place; bundle.json
// is rewritten last. A run that finds ".next" without COMMIT deletes it; one
// that finds COMMIT completes the switch, so a crash never leaves a mix.

const SAFE_PATH = /^[A-Za-z0-9._-]+(\/[A-Za-z0-9._-]+)*$/;
const readyText = (generation, tree) =>
  `completion-private-v2 generation=${generation}\ntree-sha256=${tree}\n`;
function treeDigest(entries) {
  for (const [path] of entries)
    if (!SAFE_PATH.test(path)) throw new Error("unsafe private bundle path");
  return sha(
    [...entries]
      .sort(([a], [b]) => Buffer.compare(Buffer.from(a), Buffer.from(b)))
      .map(([path, hash]) => `${hash}  ${path}\n`)
      .join(""),
  );
}
// All regular files below root as Map(relative path -> bytes), symlinks refused.
function readTree(root, prefix = "", into = new Map()) {
  for (const name of readdirSync(join(root, prefix)).sort()) {
    const path = prefix === "" ? name : `${prefix}/${name}`;
    const stat = lstatSync(join(root, path));
    if (stat.isSymbolicLink()) throw new Error("private bundle symlink refused");
    if (stat.isDirectory()) readTree(root, path, into);
    else if (stat.isFile()) into.set(path, readFileSync(join(root, path)));
    else throw new Error("private bundle contains an unsupported entry");
  }
  return into;
}
const targetEntries = (files) =>
  [...files]
    .filter(([path]) => path !== ".ready")
    .map(([path, body]) => [path, sha(body)]);
const treeShaOf = (directory) => treeDigest(targetEntries(readTree(directory)));
const currentTrees = (directory) =>
  Object.fromEntries(
    presentTargets(directory).map((target) => [target, treeShaOf(join(directory, target))]),
  );
// The mutable content of a generation: "<target>/<path>" -> bytes without
// ".ready", the tls directory by file name, and a new compose.env (or null).
function loadPlan(directory) {
  const targets = new Map();
  for (const target of presentTargets(directory))
    for (const [path, body] of readTree(join(directory, target)))
      if (path !== ".ready") targets.set(`${target}/${path}`, body);
  // First change of a bundle from before the key files: they move out of
  // env/feedback.env into the "keys" target, with the same values.
  if (!existsSync(join(directory, "keys")))
    for (const [path, body] of legacyKeyFiles(directory)) targets.set(path, body);
  return {
    targets,
    tls: readTree(join(directory, "tls")),
    composeEnv: null,
  };
}
const sameBytes = (a, b) =>
  a.size === b.size &&
  [...a].every(([key, body]) => b.has(key) && body.equals(b.get(key)));
const canonical = (value) =>
  JSON.stringify(value, (_, v) =>
    v !== null && typeof v === "object" && !Array.isArray(v)
      ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => (a < b ? -1 : 1)))
      : v,
  );

function recoverNext(directory) {
  const next = join(directory, ".next");
  if (!existsSync(next)) return;
  if (existsSync(join(next, "COMMIT"))) finishNext(directory);
  else rmSync(next, { recursive: true, force: true });
}
function openBundle(directory) {
  if (
    !existsSync(directory) ||
    lstatSync(directory).isSymbolicLink() ||
    (lstatSync(directory).mode & 0o777) !== 0o700 ||
    !existsSync(join(directory, "bundle.json"))
  )
    throw new Error("incomplete private bundle or unsafe directory");
  recoverNext(directory);
  return JSON.parse(privateFile(join(directory, "bundle.json")));
}
function verifyIntegrity(directory, current) {
  if (JSON.stringify(current.files) !== JSON.stringify(inventory(directory)))
    throw new Error("private bundle integrity changed or incomplete");
}
function archive(directory, generation, current) {
  const root = join(directory, ".generations");
  mkdirSync(root, { recursive: true, mode: 0o700 });
  const base = join(root, String(generation));
  rmSync(base, { recursive: true, force: true });
  for (const target of [...presentTargets(directory), "tls"])
    for (const [path, body] of readTree(join(directory, target)))
      write(base, `${target}/${path}`, body);
  write(
    base,
    "meta.json",
    JSON.stringify({
      generation,
      binding: current.binding,
      tls: current.tls ?? null,
      caRotation: current.caRotation ?? null,
    }),
  );
}
function finishNext(directory, afterStep) {
  const next = join(directory, ".next");
  for (const item of [...TARGETS, "tls", "compose.env"]) {
    if (!existsSync(join(next, item))) continue;
    rmSync(join(directory, item), { recursive: true, force: true });
    renameSync(join(next, item), join(directory, item));
    afterStep?.(`switched:${item}`);
  }
  syncDirectory(directory);
  const state = JSON.parse(readFileSync(join(next, "state.json"), "utf8"));
  const files = inventory(directory);
  replacePrivate(
    directory,
    "bundle.json",
    JSON.stringify({ ...state, files }, null, 2) + "\n",
  );
  rmSync(next, { recursive: true, force: true });
  const keep = new Set([state.previous?.generation]);
  const stored = readdirSync(join(directory, ".generations"))
    .filter((name) => /^\d+$/.test(name))
    .map(Number)
    .sort((a, b) => b - a);
  for (const generation of stored.slice(KEEP_GENERATIONS))
    if (!keep.has(generation))
      rmSync(join(directory, ".generations", String(generation)), {
        recursive: true,
        force: true,
      });
  return files;
}
// Stages and switches in the next generation. `plan` is the full content of it.
function commitGeneration(
  directory,
  current,
  plan,
  { binding, caRotation, timestamp, afterStep },
) {
  const generation = (current.generation ?? 0) + 1;
  const next = join(directory, ".next");
  mkdirSync(next, { recursive: true, mode: 0o700 });
  try {
    for (const target of TARGETS) {
      const entries = [...plan.targets]
        .filter(([path]) => path.startsWith(`${target}/`))
        .map(([path, body]) => [path.slice(target.length + 1), body]);
      if (entries.length === 0) continue;
      for (const [path, body] of entries) write(next, `${target}/${path}`, body);
      write(
        next,
        `${target}/.ready`,
        readyText(
          generation,
          treeDigest(entries.map(([path, body]) => [path, sha(body)])),
        ),
      );
    }
    for (const [name, body] of plan.tls) write(next, `tls/${name}`, body);
    if (plan.composeEnv !== null) write(next, "compose.env", plan.composeEnv);
    write(
      next,
      "state.json",
      JSON.stringify(
        {
          binding,
          sources: current.sources,
          sourceRoot: current.sourceRoot,
          generation,
          tls: tlsMetadata(plan.tls),
          previous: {
            generation: current.generation ?? 0,
            trees: currentTrees(directory),
          },
          caRotation,
          generatedAt: timestamp,
        },
        null,
        2,
      ),
    );
    archive(directory, current.generation ?? 0, current);
  } catch (error) {
    rmSync(next, { recursive: true, force: true });
    throw error;
  }
  afterStep?.("staged");
  write(next, "COMMIT", String(generation));
  afterStep?.("committed");
  const files = finishNext(directory, afterStep);
  return {
    directory,
    project: binding.project,
    digest: sha(JSON.stringify(files)),
    reused: false,
    generation,
  };
}

// ---- Operations ------------------------------------------------------------

// Writes the CA/leaf state of `plan.tls` into the targets that carry it.
function applyTls(plan, phase) {
  for (const side of ["auth/secrets", "transport"])
    for (const name of ["server.crt", "server.key"])
      plan.targets.set(`${side}/${name}`, plan.tls.get(name));
  // Clients trust the old and the new CA while a rotation is under way.
  const ca = plan.tls.get("ca.crt");
  const trust =
    phase === "trust"
      ? Buffer.concat([ca, plan.tls.get("ca-next.crt")])
      : phase === "serve"
        ? Buffer.concat([plan.tls.get("ca-previous.crt"), ca])
        : ca;
  plan.targets.set("trust/ca.crt", trust);
  plan.targets.set("health/ca.crt", trust);
}
// Runs OpenSSL in a scratch directory inside .next (deleted with it on a crash).
function withScratch(directory, work) {
  const scratch = join(directory, ".next", ".scratch");
  mkdirSync(scratch, { recursive: true, mode: 0o700 });
  try {
    return work(scratch);
  } catch (error) {
    rmSync(join(directory, ".next"), { recursive: true, force: true });
    throw error;
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}
// Issues a new server key and leaf under the signing CA (ca.crt, ca.key).
function issueLeaf(directory, tlsFiles) {
  tlsFiles.set("server.ext", Buffer.from(SERVER_EXT));
  withScratch(directory, (scratch) => {
    for (const name of ["ca.crt", "ca.key", "server.ext"])
      write(scratch, name, tlsFiles.get(name));
    createLeaf(scratch);
    for (const name of ["server.key", "server.csr", "server.crt"])
      tlsFiles.set(name, readFileSync(join(scratch, name)));
  });
}
function rotateLeaf(directory, current, options) {
  const plan = loadPlan(directory);
  issueLeaf(directory, plan.tls);
  applyTls(plan, current.caRotation?.phase ?? null);
  return commitGeneration(directory, current, plan, {
    ...options,
    binding: current.binding,
    caRotation: current.caRotation ?? null,
  });
}
function rotateAuthority(directory, current, phase, options) {
  const state = current.caRotation?.phase ?? null;
  const expected = { trust: null, serve: "trust", retire: "serve" }[phase];
  if (state !== expected)
    throw new Error(
      `CA rotation phase order: ${phase} must follow ${expected ?? "a bundle that is not rotating"}, but the bundle is ${state ?? "not rotating"}`,
    );
  const plan = loadPlan(directory);
  if (phase === "trust") {
    withScratch(directory, (scratch) => {
      createCA(
        scratch,
        "ca-next.key",
        "ca-next.crt",
        `/CN=Umir completion private CA ${randomBytes(4).toString("hex")}`,
      );
      for (const name of ["ca-next.key", "ca-next.crt"])
        plan.tls.set(name, readFileSync(join(scratch, name)));
    });
  } else if (phase === "serve") {
    plan.tls.set("ca-previous.crt", plan.tls.get("ca.crt"));
    for (const [from, to] of [
      ["ca-next.crt", "ca.crt"],
      ["ca-next.key", "ca.key"],
    ]) {
      plan.tls.set(to, plan.tls.get(from));
      plan.tls.delete(from);
    }
    issueLeaf(directory, plan.tls);
  } else plan.tls.delete("ca-previous.crt");
  const next = phase === "retire" ? null : phase;
  applyTls(plan, next);
  return commitGeneration(directory, current, plan, {
    ...options,
    binding: current.binding,
    caRotation: next === null ? null : { phase: next },
  });
}
function rollbackBundle({ directory, project, timestamp, afterStep }) {
  const current = openBundle(directory);
  if (current.binding?.project !== project)
    throw new Error("private bundle binding changed; do not reprovision");
  verifyIntegrity(directory, current);
  const previous = current.previous;
  if (!previous || !Number.isInteger(previous.generation))
    throw new Error("no previous generation to roll back to");
  const base = join(directory, ".generations", String(previous.generation));
  if (!existsSync(base)) throw new Error("previous generation is missing");
  const plan = { targets: new Map(), tls: new Map(), composeEnv: null };
  for (const target of TARGETS) {
    if (!existsSync(join(base, target))) {
      // Service keys never change, so a generation from before the "keys"
      // target keeps the keys of the current one.
      if (target === "keys" && existsSync(join(directory, "keys")))
        for (const [path, body] of readTree(join(directory, "keys")))
          if (path !== ".ready") plan.targets.set(`keys/${path}`, body);
      continue;
    }
    const files = readTree(join(base, target));
    if (treeDigest(targetEntries(files)) !== previous.trees?.[target])
      throw new Error("previous generation integrity changed");
    for (const [path, body] of files)
      if (path !== ".ready") plan.targets.set(`${target}/${path}`, body);
  }
  plan.tls = readTree(join(base, "tls"));
  const meta = JSON.parse(readFileSync(join(base, "meta.json"), "utf8"));
  const env = readFileSync(join(directory, "compose.env"), "utf8");
  const patched = patchOrigin(env, meta.binding.origin, meta.binding.cookieMode);
  plan.composeEnv = patched === env ? null : patched;
  return commitGeneration(directory, current, plan, {
    binding: meta.binding,
    caRotation: meta.caRotation ?? null,
    timestamp,
    afterStep,
  });
}

// ---- Inputs ----------------------------------------------------------------

const INPUT_FIELDS = [
  "initialUser",
  "smtp",
  "smtpUsernameFile",
  "smtpPasswordFile",
  "smtpCAFile",
  "registrationMail",
  "registration",
];
function loadInputs(path) {
  const inputBytes = privateFile(path);
  let settings;
  try {
    settings = JSON.parse(inputBytes);
  } catch {
    throw new Error("invalid private operational input JSON");
  }
  if (
    !settings.initialUser ||
    !settings.smtp ||
    !settings.registrationMail ||
    settings.initialUser.credentialVersion !== "1" ||
    !noControls(settings.initialUser.login) ||
    !["starttls", "tls"].includes(settings.smtp.TLSMode) ||
    typeof settings.smtp.Port !== "number" ||
    settings.smtp.Port < 1 ||
    settings.smtp.Port > 65535
  )
    throw new Error("incomplete private operational inputs");
  const registrationLimits = { mailLimitPer10Minutes: 60, organizationsPerUser: 100, organizationCreationsPerHour: 100, wrongCodesPerAddressPerHour: 100 };
  if (settings.registration !== undefined &&
      (settings.registration === null || Array.isArray(settings.registration) || typeof settings.registration !== "object" ||
       Object.keys(settings.registration).some((key) => key !== "enabled" && !Object.hasOwn(registrationLimits,key)) ||
       typeof settings.registration.enabled !== "boolean"))
    throw new Error("registration.enabled must be an explicit boolean");
  for (const [name, maximum] of Object.entries(registrationLimits)) {
    const value = settings.registration?.[name];
    if (value !== undefined && (!Number.isInteger(value) || value < 1 || value > maximum))
      throw new Error(`registration.${name} must be an integer from 1 to ${maximum}`);
  }
  const username = privateFile(settings.smtpUsernameFile);
  const password = privateFile(settings.smtpPasswordFile);
  const smtpCA =
    settings.smtpCAFile === undefined
      ? undefined
      : privateFile(settings.smtpCAFile);
  if (
    "Username" in settings.smtp ||
    "Password" in settings.smtp ||
    "RootCAs" in settings.smtp
  )
    throw new Error("SMTP credentials must use private file references");
  return { inputBytes, settings, username, password, smtpCA };
}
const authPath = (name) => `/run/umir-auth/secrets/${name}`;
function authConfig({ settings, origin, cookieMode, smtpCA }) {
  return {
    privateAddress: ":8085",
    stateFile: "/var/lib/umir-auth/auth.json",
    secretsDir: "/var/lib/umir-auth/owner-secrets",
    initialUser: settings.initialUser,
    passwordFile: authPath("password"),
    serviceKeyFiles: Object.fromEntries(
      ["gateway", "serving", "warehouse", "feedback"].map((s) => [
        s,
        authPath(`${s}-auth`),
      ]),
    ),
    peerKeyFiles: Object.fromEntries(
      ["warehouse", "serving", "gateway"].map((s) => [
        s,
        authPath(`auth-${s}`),
      ]),
    ),
    peerURLs: {
      warehouse: "https://transport:8442",
      serving: "https://transport:8443",
      gateway: "https://transport:8444",
    },
    zoneDataFile: "/usr/local/lib/umir/zoneinfo.zip",
    zoneDataVersion: "go1.26.9",
    isolatedHTTP: false,
    tlsCertFile: authPath("server.crt"),
    tlsKeyFile: authPath("server.key"),
    smtp: settings.smtp,
    registrationMail: settings.registrationMail,
    registration: settings.registration ?? { enabled: false },
    invitationMail: {
      frontendOrigin: origin,
      allowLoopbackHTTP: cookieMode === "loopback-development",
    },
    smtpUsernameFile: authPath("smtp-username"),
    smtpPasswordFile: authPath("smtp-password"),
    ...(smtpCA === undefined ? {} : { smtpCAFile: authPath("smtp-ca.crt") }),
  };
}
// Applies the new inputs to the current content; keys, the initial user and the
// project cannot change.
function reconfigureBundle(
  directory,
  current,
  { input, binding, origin, cookieMode, timestamp, afterStep },
) {
  if (current.binding?.project !== binding.project)
    throw new Error("private bundle binding changed; do not reprovision existing authority");
  verifyIntegrity(directory, current);
  const unknown = Object.keys(input.settings).filter(
    (name) => !INPUT_FIELDS.includes(name),
  );
  if (unknown.length > 0)
    throw new Error(
      `reconfigure refuses unknown input fields and key material: ${unknown.join(", ")}`,
    );
  const original = loadPlan(directory);
  const existing = JSON.parse(original.targets.get("auth/config.json"));
  if (canonical(existing.initialUser) !== canonical(input.settings.initialUser))
    throw new Error("reconfigure cannot change the initial user");
  const plan = loadPlan(directory);
  plan.targets.set(
    "auth/config.json",
    Buffer.from(
      JSON.stringify(
        authConfig({
          settings: input.settings,
          origin,
          cookieMode,
          smtpCA: input.smtpCA,
        }),
        null,
        2,
      ) + "\n",
    ),
  );
  plan.targets.set("auth/secrets/smtp-username", input.username);
  plan.targets.set("auth/secrets/smtp-password", input.password);
  if (input.smtpCA === undefined) plan.targets.delete("auth/secrets/smtp-ca.crt");
  else plan.targets.set("auth/secrets/smtp-ca.crt", input.smtpCA);
  const env = readFileSync(join(directory, "compose.env"), "utf8");
  const patched = patchOrigin(env, origin, cookieMode);
  if (
    sameBytes(plan.targets, original.targets) &&
    canonical(binding) === canonical(current.binding) &&
    patched === env
  )
    return {
      directory,
      project: binding.project,
      digest: sha(JSON.stringify(current.files)),
      reused: true,
      generation: current.generation ?? 0,
    };
  plan.composeEnv = patched === env ? null : patched;
  return commitGeneration(directory, current, plan, {
    binding,
    caRotation: current.caRotation ?? null,
    timestamp,
    afterStep,
  });
}

export function prepare({
  directory,
  inputs,
  project,
  origin,
  cookieMode = "secure",
  sources,
  updateImages = false,
  rotateLeaf: leaf = false,
  rotateCA,
  reconfigure = false,
  rollback = false,
  now,
  afterStep,
}) {
  const operations = [
    leaf && "rotate-leaf",
    rotateCA !== undefined && "rotate-ca",
    reconfigure && "reconfigure",
    rollback && "rollback",
  ].filter(Boolean);
  if (operations.length > 1 || (operations.length === 1 && updateImages))
    throw new Error(
      "rotate-leaf, rotate-ca, reconfigure, rollback and update-images are exclusive: run one operation at a time",
    );
  if (rotateCA !== undefined && !CA_PHASES.includes(rotateCA))
    throw new Error("rotate-ca needs the phase trust, serve or retire");
  if (
    !/^umir-completion-[a-z0-9-]{1,40}$/.test(project ?? "") ||
    !noControls(directory)
  )
    throw new Error("invalid owned completion project or directory");
  directory = resolve(directory);
  const options = {
    timestamp: new Date(now ?? Date.now()).toISOString(),
    afterStep,
  };
  if (rollback) return rollbackBundle({ directory, project, ...options });
  validateOrigin(origin, cookieMode);
  if (typeof inputs !== "string") throw new Error("private inputs are required");
  inputs = resolve(inputs);
  const input = loadInputs(inputs);
  const { inputBytes, settings, username, password, smtpCA } = input;
  const binding = {
    version: 1,
    project,
    origin,
    cookieMode,
    inputs: sha(inputBytes),
    username: sha(username),
    password: sha(password),
    smtpCA: smtpCA === undefined ? null : sha(smtpCA),
  };
  const manifest = JSON.parse(readFileSync(sourceFile, "utf8"));
  if (sources !== undefined) {
    sources = realpathSync(resolve(sources));
    if (!noControls(sources)) throw new Error("invalid source directory");
    for (const [service, revision] of Object.entries(pins(manifest))) {
      // An optional service is verified only when its checkout is present.
      if (
        manifest.optionalServices?.[service] !== undefined &&
        !existsSync(join(sources, service))
      )
        continue;
      let head, status;
      try {
        head = execFileSync("git", ["rev-parse", "HEAD"], {
          cwd: join(sources, service),
          encoding: "utf8",
          timeout: 5000,
        }).trim();
        status = execFileSync("git", ["status", "--porcelain"], {
          cwd: join(sources, service),
          encoding: "utf8",
          timeout: 5000,
        });
      } catch {
        throw new Error(`cannot verify exact ${service} source`);
      }
      if (head !== revision || status !== "")
        throw new Error(`exact clean ${service} source required`);
    }
  }
  if (existsSync(directory)) {
    const current = openBundle(directory);
    if (reconfigure)
      return reconfigureBundle(directory, current, {
        input,
        binding,
        origin,
        cookieMode,
        ...options,
      });
    if (JSON.stringify(current.binding) !== JSON.stringify(binding))
      throw new Error(
        "private bundle binding changed; do not reprovision existing authority (use --reconfigure for a reviewed change)",
      );
    verifyIntegrity(directory, current);
    if (leaf) return rotateLeaf(directory, current, options);
    if (rotateCA !== undefined)
      return rotateAuthority(directory, current, rotateCA, options);
    if (
      current.sources !== sourceDigest() ||
      current.sourceRoot !== (sources ?? null)
    ) {
      if (!updateImages)
        throw new Error(
          "candidate source binding changed; pass --update-images for a reviewed image update",
        );
      replacePrivate(
        directory,
        "compose.env",
        composeEnvironment(
          { directory, project, origin, cookieMode, sources },
          manifest,
        ),
      );
      current.sources = sourceDigest();
      current.sourceRoot = sources ?? null;
      current.files = inventory(directory);
      replacePrivate(
        directory,
        "bundle.json",
        JSON.stringify(current, null, 2) + "\n",
      );
    }
    return {
      directory,
      project,
      digest: sha(JSON.stringify(current.files)),
      reused: true,
      generation: current.generation ?? 0,
    };
  }
  if (operations.length > 0)
    throw new Error(`${operations[0]} needs an existing private bundle`);
  // Refuse before creating anything when the image lock lacks a pinned service.
  for (const [service, revision] of Object.entries(pins(manifest)))
    imageReference(service, revision);
  mkdirSync(directory, { mode: 0o700 });
  tls(directory);
  const keys = Object.fromEntries(
    [
      "gateway-auth",
      "serving-auth",
      "warehouse-auth",
      "feedback-auth",
      "auth-warehouse",
      "auth-serving",
      "auth-gateway",
      "gateway-warehouse",
      "gateway-serving",
      "gateway-feedback",
      "serving-warehouse",
    ].map((name) => [name, randomBytes(32).toString("hex")]),
  );
  for (const name of [
    "gateway-auth",
    "serving-auth",
    "warehouse-auth",
    "feedback-auth",
    "auth-warehouse",
    "auth-serving",
    "auth-gateway",
  ])
    write(directory, `auth/secrets/${name}`, keys[name]);
  write(directory, "auth/secrets/password", randomBytes(16).toString("hex"));
  write(directory, "auth/secrets/smtp-username", username);
  write(directory, "auth/secrets/smtp-password", password);
  if (smtpCA !== undefined)
    write(directory, "auth/secrets/smtp-ca.crt", smtpCA);
  for (const [target, source] of [
    ["trust/ca.crt", "ca.crt"],
    ["auth/secrets/server.crt", "server.crt"],
    ["auth/secrets/server.key", "server.key"],
    ["transport/server.crt", "server.crt"],
    ["transport/server.key", "server.key"],
    ["health/ca.crt", "ca.crt"],
  ]) {
    write(directory, target, readFileSync(join(directory, "tls", source)));
  }
  write(directory, "health/service-key", keys["warehouse-auth"]);
  write(
    directory,
    "auth/config.json",
    JSON.stringify(authConfig({ settings, origin, cookieMode, smtpCA }), null, 2) +
      "\n",
  );
  const env = {
    warehouse: {
      AUTH_SERVICE_KEY: keys["warehouse-auth"],
      AUTH_RUNTIME_SERVICE_KEY: keys["auth-warehouse"],
      GATEWAY_SERVICE_KEY: keys["gateway-warehouse"],
      SERVING_SERVICE_KEY: keys["serving-warehouse"],
    },
    serving: {
      SERVING_AUTH_KEY: keys["serving-auth"],
      AUTH_RUNTIME_KEY: keys["auth-serving"],
      GATEWAY_SERVING_KEY: keys["gateway-serving"],
      SERVING_WAREHOUSE_KEY: keys["serving-warehouse"],
    },
    gateway: {
      AUTH_SERVICE_KEY: keys["gateway-auth"],
      AUTH_RUNTIME_SERVICE_KEY: keys["auth-gateway"],
      WAREHOUSE_SERVICE_KEY: keys["gateway-warehouse"],
      SERVING_SERVICE_KEY: keys["gateway-serving"],
      FEEDBACK_SERVICE_KEY: keys["gateway-feedback"],
    },
  };
  // Feedback reads its keys from files (FEEDBACK_*_KEY_FILE), not from the environment.
  for (const name of Object.values(FEEDBACK_KEY_FILES))
    write(directory, `keys/${name}`, keys[name]);
  for (const [service, values] of Object.entries(env))
    write(
      directory,
      `env/${service}.env`,
      Object.entries(values)
        .map(([key, value]) => `${key}=${value}\n`)
        .join(""),
    );
  write(directory, "transport/conf/default.conf", transportConfig());
  for (const target of TARGETS)
    write(
      directory,
      `${target}/.ready`,
      readyText(1, treeShaOf(join(directory, target))),
    );
  write(
    directory,
    "compose.env",
    composeEnvironment(
      { directory, project, origin, cookieMode, sources },
      manifest,
    ),
  );
  const files = inventory(directory);
  write(
    directory,
    "bundle.json",
    JSON.stringify(
      {
        binding,
        sources: sourceDigest(),
        sourceRoot: sources ?? null,
        generation: 1,
        tls: tlsMetadata(readTree(join(directory, "tls"))),
        previous: null,
        caRotation: null,
        generatedAt: options.timestamp,
        files,
      },
      null,
      2,
    ) + "\n",
  );
  return {
    directory,
    project,
    digest: sha(JSON.stringify(files)),
    reused: false,
    generation: 1,
  };
}

if (
  process.argv[1] !== undefined &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    const names = new Map([
      ["--directory", "directory"],
      ["--inputs", "inputs"],
      ["--project", "project"],
      ["--origin", "origin"],
      ["--cookie-mode", "cookieMode"],
      ["--sources", "sources"],
      ["--phase", "phase"],
    ]);
    const flags = new Map([
      ["--update-images", "updateImages"],
      ["--rotate-leaf", "rotateLeaf"],
      ["--rotate-ca", "rotateCAFlag"],
      ["--reconfigure", "reconfigure"],
      ["--rollback", "rollback"],
    ]);
    const options = {};
    for (let i = 2; i < process.argv.length; i++) {
      const flag = flags.get(process.argv[i]);
      if (flag !== undefined) {
        if (options[flag]) throw new Error(`duplicate ${process.argv[i]} argument`);
        options[flag] = true;
        continue;
      }
      const name = names.get(process.argv[i]);
      const value = process.argv[++i];
      if (name === undefined || value === undefined || name in options)
        throw new Error("invalid preparation argument");
      options[name] = value;
    }
    const { phase, rotateCAFlag, ...rest } = options;
    if ((phase === undefined) !== !rotateCAFlag)
      throw new Error("--rotate-ca and --phase trust|serve|retire go together");
    console.log(
      JSON.stringify(
        prepare(rotateCAFlag ? { ...rest, rotateCA: phase } : rest),
      ),
    );
  } catch (error) {
    console.error(
      error instanceof Error ? error.message : "completion preparation failed",
    );
    process.exitCode = 1;
  }
}
