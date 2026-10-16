import { pathToFileURL } from "node:url";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  writeFileSync,
  readFileSync,
  rmSync,
  lstatSync,
  mkdirSync,
  symlinkSync,
} from "node:fs";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { prepare } from "../../../scripts/completion/prepare.mjs";
import {
  bundle,
  certInfo,
  days,
  digest,
  fixture,
  keyMatches,
  pemCount,
  prepareScript,
  readReady,
  rewriteInputs,
  snapshot,
  targets,
  treeSha,
  trees,
  verifies,
} from "./support.mjs";
import { createHash } from "node:crypto";
import { existsSync, readdirSync } from "node:fs";

test("first preparation writes scoped private inputs and repeats without changing owner credentials", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const key = join(first.directory, "auth/secrets/gateway-auth");
  const before = digest(key);
  assert.equal(lstatSync(key).mode & 0o777, 0o600);
  assert.match(readFileSync(key, "utf8"), /^[a-f0-9]{64}$/);
  const config = JSON.parse(
    readFileSync(join(first.directory, "auth/config.json"), "utf8"),
  );
  assert.equal(config.isolatedHTTP, false);
  assert.equal(config.peerURLs.warehouse, "https://transport:8442");
  assert.equal(config.stateFile, "/var/lib/umir-auth/auth.json");
  assert.equal(config.passwordFile, "/run/umir-auth/secrets/password");
  assert.equal(prepare(options).digest, first.digest);
  assert.equal(digest(key), before);
  const gateway = readFileSync(
    join(first.directory, "env/gateway.env"),
    "utf8",
  );
  assert.ok(gateway.includes("AUTH_SERVICE_KEY="));
  assert.ok(!gateway.includes("SERVING_AUTH_KEY="));
  const warehouse = readFileSync(
    join(first.directory, "env/warehouse.env"),
    "utf8",
  );
  assert.ok(!warehouse.includes("FEEDBACK_SERVICE_KEY="));
  assert.ok(!warehouse.includes("GATEWAY_SERVING_KEY="));
  assert.ok(
    !readFileSync(join(first.directory, "compose.env"), "utf8").includes(
      readFileSync(key, "utf8"),
    ),
  );
});

test("partial or tampered input bundles fail without replacing any surviving credential", (t) => {
  const { options } = fixture(t);
  const result = prepare(options);
  const key = join(result.directory, "auth/secrets/gateway-auth");
  const before = digest(key);
  rmSync(join(result.directory, "auth/secrets/auth-serving"));
  assert.throws(() => prepare(options), /incomplete|changed|integrity/i);
  assert.equal(digest(key), before);
});

test("changed scope, unsafe origin or private-file symlinks do not reprovision authority", (t) => {
  const { options, root } = fixture(t);
  prepare(options);
  assert.throws(
    () => prepare({ ...options, origin: "https://other.invalid" }),
    /changed|binding/i,
  );
  assert.throws(
    () =>
      prepare({
        ...options,
        directory: join(root, "insecure"),
        origin: "http://example.com",
      }),
    /origin/i,
  );
  const linked = join(root, "linked.json");
  symlinkSync(options.inputs, linked);
  assert.throws(
    () =>
      prepare({ ...options, directory: join(root, "linked"), inputs: linked }),
    /private|symlink/i,
  );
});

test("base Compose has one edge, verified TLS and no dependency on optional Feedback", (t) => {
  const { options } = fixture(t);
  const result = prepare(options);
  const factory = new URL("../../../", import.meta.url).pathname;
  const args = [
    "compose",
    "--env-file",
    join(result.directory, "compose.env"),
    "-f",
    join(factory, "compose.completion.yaml"),
    "config",
    "--format",
    "json",
  ];
  const value = JSON.parse(
    execFileSync("docker", args, {
      cwd: factory,
      encoding: "utf8",
      timeout: 10000,
    }),
  );
  assert.deepEqual(
    Object.entries(value.services)
      .filter(([, service]) => service.ports?.length)
      .map(([name]) => name),
    ["frontend"],
  );
  assert.equal(value.services.frontend.ports[0].host_ip, "127.0.0.1");
  assert.ok(!("feedback" in value.services));
  assert.ok(!("feedback" in value.services.gateway.depends_on));
  assert.ok(
    !("AUTH_ISOLATED_TEST_NETWORK" in value.services.gateway.environment),
  );
  assert.equal(
    value.services.gateway.environment.AUTH_URL,
    "https://auth:8085",
  );
  assert.equal(value.networks.default.internal, true);
  assert.ok(
    value.services.auth.volumes.some((v) => v.target === "/var/lib/umir-auth"),
  );
  const delivery = JSON.parse(
    execFileSync("docker", args, {
      cwd: factory,
      encoding: "utf8",
      timeout: 10000,
      env: { ...process.env, COMPLETION_BIND: "192.0.2.10" },
    }),
  );
  assert.equal(delivery.services.frontend.ports[0].host_ip, "192.0.2.10");
  assert.deepEqual(
    Object.entries(delivery.services)
      .filter(([, service]) => service.ports?.length)
      .map(([name]) => name),
    ["frontend"],
  );
  const optional = JSON.parse(
    execFileSync(
      "docker",
      ["compose", "--profile", "feedback", ...args.slice(1)],
      { cwd: factory, encoding: "utf8", timeout: 10000 },
    ),
  );
  assert.ok("feedback" in optional.services);
  assert.ok(!("feedback" in optional.services.gateway.depends_on));
});

const lockURL = new URL("../../../scripts/completion/images.lock.json", import.meta.url);

test("reviewed image updates preserve the existing private authority inputs", async (t) => {
  const { options, root } = fixture(t);
  const script = join(root, "prepare.mjs");
  writeFileSync(
    script,
    readFileSync(
      new URL("../../../scripts/completion/prepare.mjs", import.meta.url),
    ),
  );
  const sourcePath = join(root, "sources.json");
  const lockPath = join(root, "images.lock.json");
  const sources = JSON.parse(
    readFileSync(
      new URL("../../../scripts/completion/sources.json", import.meta.url),
      "utf8",
    ),
  );
  const lock = JSON.parse(readFileSync(lockURL, "utf8"));
  writeFileSync(sourcePath, JSON.stringify(sources));
  writeFileSync(lockPath, JSON.stringify(lock));
  const runtime = await import(pathToFileURL(script).href);
  const original = runtime.prepare(options);
  const key = join(original.directory, "auth/secrets/gateway-auth");
  const config = join(original.directory, "auth/config.json");
  const before = [digest(key), digest(config)];
  sources.services.frontend = "b".repeat(40);
  writeFileSync(sourcePath, JSON.stringify(sources));
  assert.throws(() => runtime.prepare(options), /source|image/i);
  // The new source must be in the image lock too, or the update is refused.
  assert.throws(
    () => runtime.prepare({ ...options, updateImages: true }),
    /lock/i,
  );
  lock.services.frontend = {
    ...lock.services.frontend,
    sourceSHA: "b".repeat(40),
    revision: "b".repeat(40),
    platformDigest: "sha256:" + "d".repeat(64),
  };
  writeFileSync(lockPath, JSON.stringify(lock));
  const updated = runtime.prepare({ ...options, updateImages: true });
  assert.equal(updated.reused, true);
  assert.deepEqual([digest(key), digest(config)], before);
  assert.ok(
    readFileSync(join(updated.directory, "compose.env"), "utf8").includes(
      "COMPLETION_FRONTEND_SHA=" + "b".repeat(40),
    ),
  );
  assert.equal(runtime.prepare(options).digest, updated.digest);
});

test("the documented synthetic mail recipe omits an unpublished privacy URL", (t) => {
  const { root } = fixture(t);
  const directory = join(root, "umir-completion-mail");
  const script = new URL(
    "../../examples/completion/prepare-mail.mjs",
    import.meta.url,
  ).pathname;
  execFileSync("node", [script, directory], { stdio: "pipe", timeout: 10000 });
  const inputs = JSON.parse(
    readFileSync(join(directory, "inputs.json"), "utf8"),
  );
  assert.equal(inputs.registrationMail.PrivacyURL, "");
  assert.equal(inputs.smtp.Host, "demo-smtp");
  const certificate = execFileSync(
    "openssl",
    ["x509", "-in", inputs.smtpCAFile, "-noout", "-text"],
    { encoding: "utf8" },
  );
  assert.match(certificate, /DNS:demo-smtp/);
});

test("public registration is disabled by default and enabled only by an explicit boolean", (t) => {
  for (const enabled of [undefined, false, true, "true"]) {
    const { options } = fixture(t);
    const inputs = JSON.parse(readFileSync(options.inputs, "utf8"));
    if (enabled !== undefined) inputs.registration = { enabled };
    writeFileSync(options.inputs, JSON.stringify(inputs));
    if (enabled === "true") {
      assert.throws(() => prepare(options), /registration/i);
      continue;
    }
    const result = prepare(options);
    const config = JSON.parse(readFileSync(join(result.directory, "auth/config.json"), "utf8"));
    assert.equal(config.registration.enabled, enabled === true);
  }
});

test("registration limits are bounded integers and retained in private config", (t) => {
 const {options}=fixture(t);
 const inputs=JSON.parse(readFileSync(options.inputs,"utf8"));
 const limits={enabled:false,mailLimitPer10Minutes:20,organizationsPerUser:2,organizationCreationsPerHour:1,wrongCodesPerAddressPerHour:5};
 inputs.registration=limits;writeFileSync(options.inputs,JSON.stringify(inputs));
 const result=prepare(options);
 assert.deepEqual(JSON.parse(readFileSync(join(result.directory,"auth/config.json"),"utf8")).registration,limits);
 for (const bad of [{enabled:false,mailLimitPer10Minutes:61},{enabled:true,organizationsPerUser:0},{enabled:true,wrongCodesPerAddressPerHour:1.5},{enabled:true,unknown:1}]) {
  inputs.registration=bad;writeFileSync(options.inputs,JSON.stringify(inputs));assert.throws(()=>prepare(options),/registration/i);
 }
});

const feedbackSHA = /^[a-f0-9]{40}$/;

const sourcesURL = new URL("../../../scripts/completion/sources.json", import.meta.url);

test("the optional Feedback pin lives in sources.json and reaches the Compose environment", (t) => {
  const manifest = JSON.parse(readFileSync(sourcesURL, "utf8"));
  const pin = manifest.optionalServices?.feedback;
  assert.match(pin, feedbackSHA);
  assert.ok(!("feedback" in manifest.services));
  const { options } = fixture(t);
  const result = prepare(options);
  const env = readFileSync(join(result.directory, "compose.env"), "utf8");
  assert.ok(env.includes(`COMPLETION_FEEDBACK_SHA=${pin}\n`));
  assert.ok(
    env.includes(
      `COMPLETION_FEEDBACK_IMAGE=ghcr.io/justikra/umir-feedback:sha-${pin}\n`,
    ),
  );
  const compose = readFileSync(
    new URL("../../../compose.completion.yaml", import.meta.url),
    "utf8",
  );
  assert.ok(!compose.includes(pin), "the Feedback pin must not be repeated in Compose");
  assert.ok(!/umir-feedback:sha-[a-f0-9]{40}/.test(compose));
  const args = (extra) => [
    "compose",
    "--profile",
    "feedback",
    "--env-file",
    join(result.directory, "compose.env"),
    "-f",
    new URL("../../../compose.completion.yaml", import.meta.url).pathname,
    "config",
    "--format",
    "json",
    ...extra,
  ];
  const image = (env) =>
    JSON.parse(
      execFileSync("docker", args([]), {
        encoding: "utf8",
        timeout: 20000,
        env: { ...process.env, ...env },
      }),
    ).services.feedback.image;
  assert.equal(image({}), `ghcr.io/justikra/umir-feedback:sha-${pin}`);
  const reviewed = "ghcr.io/justikra/umir-feedback:sha-" + "c".repeat(40);
  assert.equal(image({ COMPLETION_FEEDBACK_IMAGE: reviewed }), reviewed);
  // A bundle prepared before the pin moved has no Feedback variables: it still
  // resolves while the profile is off.
  const old = readFileSync(join(result.directory, "compose.env"), "utf8")
    .split("\n")
    .filter((line) => !line.startsWith("COMPLETION_FEEDBACK_"))
    .join("\n");
  const oldEnv = join(result.directory, "old-compose.env");
  writeFileSync(oldEnv, old, { mode: 0o600 });
  const legacy = JSON.parse(
    execFileSync(
      "docker",
      [
        "compose",
        "--env-file",
        oldEnv,
        "-f",
        new URL("../../../compose.completion.yaml", import.meta.url).pathname,
        "config",
        "--format",
        "json",
      ],
      { encoding: "utf8", timeout: 20000 },
    ),
  );
  assert.ok(!("feedback" in legacy.services));
});

// An image lock for synthetic source pins: every service has a made-up digest.
function syntheticLock(manifest) {
  const digestFor = (value) => "sha256:" + createHash("sha256").update(value).digest("hex");
  return JSON.stringify({
    profile: manifest.profile ?? "REG1",
    platform: "linux/amd64",
    services: Object.fromEntries(
      Object.entries(manifest.services).map(([service, sha]) => [
        service,
        {
          sourceSHA: sha,
          indexDigest: digestFor("i" + sha),
          platformDigest: digestFor("p" + sha),
          configDigest: digestFor("c" + sha),
          revision: sha,
        },
      ]),
    ),
    auxiliary: {},
    unpublished: manifest.optionalServices ?? {},
  });
}

test("an exact clean Feedback checkout is required only when one exists", async (t) => {
  const { options, root } = fixture(t);
  const git = (cwd, ...args) =>
    execFileSync(
      "git",
      [
        "-c",
        "user.name=Synthetic",
        "-c",
        "user.email=synthetic@invalid",
        "-c",
        "commit.gpgsign=false",
        ...args,
      ],
      { cwd, encoding: "utf8" },
    ).trim();
  const checkout = (directory) => {
    mkdirSync(directory, { recursive: true });
    git(directory, "init", "-q");
    writeFileSync(join(directory, "README"), directory);
    git(directory, "add", "README");
    git(directory, "commit", "-q", "-m", "synthetic");
    return git(directory, "rev-parse", "HEAD");
  };
  const sources = join(root, "checkouts");
  const manifest = JSON.parse(readFileSync(sourcesURL, "utf8"));
  for (const service of Object.keys(manifest.services))
    manifest.services[service] = checkout(join(sources, service));
  manifest.optionalServices = { feedback: "d".repeat(40) };
  const run = join(root, "run");
  mkdirSync(run);
  writeFileSync(
    join(run, "prepare.mjs"),
    readFileSync(new URL("../../../scripts/completion/prepare.mjs", import.meta.url)),
  );
  writeFileSync(join(run, "sources.json"), JSON.stringify(manifest));
  writeFileSync(join(run, "images.lock.json"), syntheticLock(manifest));
  const runtime = await import(pathToFileURL(join(run, "prepare.mjs")).href);
  const at = (name) => ({ ...options, directory: join(root, name), sources });
  runtime.prepare(at("without"));
  const head = checkout(join(sources, "feedback"));
  assert.notEqual(head, manifest.optionalServices.feedback);
  assert.throws(() => runtime.prepare(at("stale")), /feedback/i);
  manifest.optionalServices = { feedback: head };
  writeFileSync(join(run, "sources.json"), JSON.stringify(manifest));
  writeFileSync(join(run, "images.lock.json"), syntheticLock(manifest));
  runtime.prepare(at("exact"));
  writeFileSync(join(sources, "feedback", "dirty"), "x");
  assert.throws(() => runtime.prepare(at("dirty")), /feedback/i);
});

// ---- W1.1: generations, rotations, reconfiguration, rollback ----

const sha = (body) => createHash("sha256").update(body).digest("hex");
const at = (directory, path) => join(directory, path);
const read = (directory, path) => readFileSync(join(directory, path));
const serviceSecrets = (directory) =>
  Object.fromEntries(
    Object.entries(snapshot(directory)).filter(
      ([path]) =>
        path.startsWith("env/") ||
        path === "health/service-key" ||
        (path.startsWith("auth/secrets/") &&
          !path.includes("server.") &&
          !path.includes("smtp-")),
    ),
  );
function assertCurrent(directory, generation) {
  for (const target of targets) {
    const ready = readReady(at(directory, target));
    assert.deepEqual(ready, {
      generation,
      tree: treeSha(at(directory, target)),
    });
  }
  assert.equal(bundle(directory).generation, generation);
}
function assertTrusted(directory) {
  for (const ca of ["trust/ca.crt", "health/ca.crt"])
    for (const leaf of ["auth/secrets/server.crt", "transport/server.crt"])
      assert.ok(
        verifies(at(directory, ca), at(directory, leaf)),
        `${ca} must trust ${leaf}`,
      );
  for (const side of ["auth/secrets", "transport"])
    assert.ok(
      keyMatches(
        at(directory, `${side}/server.crt`),
        at(directory, `${side}/server.key`),
      ),
    );
  assert.deepEqual(read(directory, "auth/secrets/server.crt"), read(directory, "transport/server.crt"));
  assert.deepEqual(read(directory, "trust/ca.crt"), read(directory, "health/ca.crt"));
}

test("the CA is issued for ten years and the leaf for one year with random serial numbers", (t) => {
  const first = prepare(fixture(t).options);
  const second = prepare(fixture(t).options);
  const serials = [];
  for (const result of [first, second]) {
    const ca = certInfo(at(result.directory, "tls/ca.crt"));
    const leaf = certInfo(at(result.directory, "tls/server.crt"));
    assert.ok(Math.abs(days(ca.notBefore, ca.notAfter) - 3650) < 1.1);
    assert.ok(Math.abs(days(leaf.notBefore, leaf.notAfter) - 365) < 1.1);
    for (const serial of [ca.serial, leaf.serial]) {
      assert.ok(serial > 1n && serial < 2n ** 127n);
      serials.push(serial);
    }
    assert.equal(result.generation, 1);
    assertCurrent(result.directory, 1);
    assert.deepEqual(bundle(result.directory).tls, {
      caNotAfter: ca.notAfter.toISOString(),
      leafNotAfter: leaf.notAfter.toISOString(),
    });
    assert.equal(bundle(result.directory).previous, null);
    assertTrusted(result.directory);
  }
  assert.equal(new Set(serials).size, 4);
});

test("rotating the leaf keeps the CA, service keys and the first password and adds a generation", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const caBefore = read(directory, "tls/ca.crt");
  const leafBefore = read(directory, "tls/server.crt");
  const secretsBefore = serviceSecrets(directory);
  const serialBefore = certInfo(at(directory, "tls/server.crt")).serial;
  const treesBefore = trees(directory);
  const rotated = prepare({ ...options, rotateLeaf: true });
  assert.equal(rotated.generation, 2);
  assert.equal(rotated.reused, false);
  assertCurrent(directory, 2);
  assert.deepEqual(read(directory, "tls/ca.crt"), caBefore);
  assert.deepEqual(read(directory, "trust/ca.crt"), caBefore);
  assert.deepEqual(read(directory, "health/ca.crt"), caBefore);
  assert.notDeepEqual(read(directory, "tls/server.crt"), leafBefore);
  assert.notEqual(certInfo(at(directory, "tls/server.crt")).serial, serialBefore);
  assert.deepEqual(serviceSecrets(directory), secretsBefore);
  assert.ok(existsSync(at(directory, "auth/secrets/password")));
  assertTrusted(directory);
  assert.equal(trees(directory).trust, treesBefore.trust);
  assert.equal(trees(directory).health, treesBefore.health);
  assert.notEqual(trees(directory).auth, treesBefore.auth);
  assert.equal(bundle(directory).previous.generation, 1);
  assert.deepEqual(bundle(directory).previous.trees, treesBefore);
  assert.deepEqual(
    read(directory, ".generations/1/auth/secrets/server.crt"),
    leafBefore,
  );
  const settled = snapshot(directory);
  const again = prepare(options);
  assert.equal(again.reused, true);
  assert.equal(again.generation, 2);
  assert.equal(again.digest, rotated.digest);
  assert.deepEqual(snapshot(directory), settled);
});

test("reconfigure changes registration, SMTP files, origin and cookie mode but never keys", (t) => {
  const { options, root } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const secretsBefore = serviceSecrets(directory);
  const envBefore = read(directory, "compose.env").toString();
  const username = join(root, "smtp-user-2");
  const password = join(root, "smtp-password-2");
  const smtpCA = join(root, "smtp-ca.pem");
  writeFileSync(username, "second-user\n", { mode: 0o600 });
  writeFileSync(password, "second-password\n", { mode: 0o600 });
  writeFileSync(smtpCA, "synthetic CA\n", { mode: 0o600 });
  rewriteInputs(options.inputs, (value) => {
    value.registration = { enabled: true, mailLimitPer10Minutes: 20 };
    value.smtpUsernameFile = username;
    value.smtpPasswordFile = password;
    value.smtpCAFile = smtpCA;
    value.smtp.Host = "smtp2.invalid";
  });
  const changed = {
    ...options,
    origin: "http://127.0.0.1:8081",
    cookieMode: "loopback-development",
  };
  const bindingBefore = bundle(directory).binding;
  const result = prepare({ ...changed, reconfigure: true });
  assert.equal(result.generation, 2);
  assert.equal(result.reused, false);
  assertCurrent(directory, 2);
  const config = JSON.parse(read(directory, "auth/config.json"));
  assert.equal(config.registration.enabled, true);
  assert.equal(config.registration.mailLimitPer10Minutes, 20);
  assert.equal(config.smtp.Host, "smtp2.invalid");
  assert.equal(config.invitationMail.frontendOrigin, "http://127.0.0.1:8081");
  assert.equal(config.invitationMail.allowLoopbackHTTP, true);
  assert.equal(config.smtpCAFile, "/run/umir-auth/secrets/smtp-ca.crt");
  assert.equal(read(directory, "auth/secrets/smtp-username").toString(), "second-user\n");
  assert.equal(read(directory, "auth/secrets/smtp-password").toString(), "second-password\n");
  assert.equal(read(directory, "auth/secrets/smtp-ca.crt").toString(), "synthetic CA\n");
  const binding = bundle(directory).binding;
  assert.notEqual(binding.username, bindingBefore.username);
  assert.notEqual(binding.password, bindingBefore.password);
  assert.equal(binding.smtpCA, sha("synthetic CA\n"));
  assert.equal(binding.origin, "http://127.0.0.1:8081");
  assert.equal(binding.cookieMode, "loopback-development");
  assert.deepEqual(serviceSecrets(directory), secretsBefore);
  const env = read(directory, "compose.env").toString();
  assert.ok(env.includes("COMPLETION_ORIGIN=http://127.0.0.1:8081\n"));
  assert.ok(env.includes("COMPLETION_COOKIE_MODE=loopback-development\n"));
  assert.ok(env.includes("COMPLETION_WEB_PORT=8081\n"));
  const pins = (text) => text.split("\n").filter((l) => /_(IMAGE|SHA)=/.test(l));
  assert.deepEqual(pins(env), pins(envBefore));
  assertTrusted(directory);
  // The new inputs are now the binding: repeating is idempotent, the old ones are refused.
  const settled = snapshot(directory);
  const again = prepare(changed);
  assert.equal(again.reused, true);
  assert.equal(again.digest, result.digest);
  assert.deepEqual(snapshot(directory), settled);
  assert.throws(() => prepare(options), /changed|binding/i);
  // Repeating the same reconfiguration changes nothing and adds no generation.
  const same = prepare({ ...changed, reconfigure: true });
  assert.equal(same.reused, true);
  assert.equal(same.generation, 2);
  // Removing the optional SMTP CA removes its file.
  rewriteInputs(options.inputs, (value) => delete value.smtpCAFile);
  prepare({ ...changed, reconfigure: true });
  assert.ok(!existsSync(at(directory, "auth/secrets/smtp-ca.crt")));
  assert.ok(!("smtpCAFile" in JSON.parse(read(directory, "auth/config.json"))));
  assertCurrent(directory, 3);
});

test("reconfigure refuses a different initial user, key material, another project and unknown inputs", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const before = snapshot(directory);
  const original = readFileSync(options.inputs);
  const attempts = [
    [/initial/i, (v) => (v.initialUser.login = "someone.else")],
    [/initial/i, (v) => (v.initialUser.userID = "22222222-2222-4222-8222-222222222222")],
    [/initial/i, (v) => (v.initialUser.displayName = "Other")],
    [/unknown|key/i, (v) => (v.serviceKeys = { "gateway-auth": "a".repeat(64) })],
    [/unknown|key/i, (v) => (v.passwordFile = "/tmp/other")],
  ];
  for (const [message, change] of attempts) {
    writeFileSync(options.inputs, original, { mode: 0o600 });
    rewriteInputs(options.inputs, change);
    assert.throws(() => prepare({ ...options, reconfigure: true }), message);
    assert.deepEqual(snapshot(directory), before);
    assert.equal(bundle(directory).generation, 1);
    assert.ok(!existsSync(at(directory, ".next")));
  }
  writeFileSync(options.inputs, original, { mode: 0o600 });
  assert.throws(
    () => prepare({ ...options, project: "umir-completion-other", reconfigure: true }),
    /changed|binding|project/i,
  );
  assert.deepEqual(snapshot(directory), before);
});

test("rotating the CA by phases never leaves a client that distrusts the running server", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const oldCA = read(directory, "tls/ca.crt");
  const oldCAFile = join(dirname(directory), "old-ca.crt");
  writeFileSync(oldCAFile, oldCA);
  const oldLeaf = read(directory, "tls/server.crt");
  const secretsBefore = serviceSecrets(directory);
  assert.throws(() => prepare({ ...options, rotateCA: "serve" }), /order|phase|trust/i);
  assert.throws(() => prepare({ ...options, rotateCA: "retire" }), /order|phase|serve/i);
  assert.throws(() => prepare({ ...options, rotateCA: "later" }), /phase/i);
  assertCurrent(directory, 1);

  const trust = prepare({ ...options, rotateCA: "trust" });
  assert.equal(trust.generation, 2);
  assertCurrent(directory, 2);
  assert.equal(pemCount(at(directory, "trust/ca.crt")), 2);
  assert.ok(read(directory, "trust/ca.crt").toString().startsWith(oldCA.toString().trim()));
  assert.deepEqual(read(directory, "health/ca.crt"), read(directory, "trust/ca.crt"));
  assert.deepEqual(read(directory, "tls/server.crt"), oldLeaf);
  assert.ok(verifies(oldCAFile, at(directory, "auth/secrets/server.crt")));
  assertTrusted(directory);
  assert.equal(bundle(directory).caRotation.phase, "trust");
  assert.throws(() => prepare({ ...options, rotateCA: "trust" }), /order|phase/i);
  assert.throws(() => prepare({ ...options, rotateCA: "retire" }), /order|phase/i);
  const settledTrust = snapshot(directory);
  assert.equal(prepare(options).reused, true);
  assert.deepEqual(snapshot(directory), settledTrust);

  const serve = prepare({ ...options, rotateCA: "serve" });
  assert.equal(serve.generation, 3);
  assertCurrent(directory, 3);
  assert.equal(pemCount(at(directory, "trust/ca.crt")), 2);
  assert.ok(read(directory, "trust/ca.crt").toString().includes(oldCA.toString().trim()));
  assert.notDeepEqual(read(directory, "tls/server.crt"), oldLeaf);
  assert.ok(!verifies(oldCAFile, at(directory, "auth/secrets/server.crt")));
  assertTrusted(directory);
  assert.equal(bundle(directory).caRotation.phase, "serve");
  assert.throws(() => prepare({ ...options, rotateCA: "serve" }), /order|phase/i);
  const newCA = read(directory, "tls/ca.crt");
  assert.notDeepEqual(newCA, oldCA);

  const retire = prepare({ ...options, rotateCA: "retire" });
  assert.equal(retire.generation, 4);
  assertCurrent(directory, 4);
  assert.equal(pemCount(at(directory, "trust/ca.crt")), 1);
  assert.deepEqual(read(directory, "trust/ca.crt"), newCA);
  assert.ok(verifies(at(directory, "trust/ca.crt"), at(directory, "auth/secrets/server.crt")));
  assert.ok(!verifies(oldCAFile, at(directory, "auth/secrets/server.crt")));
  assertTrusted(directory);
  assert.equal(bundle(directory).caRotation, null);
  assert.deepEqual(serviceSecrets(directory), secretsBefore);
  const ca = certInfo(at(directory, "tls/ca.crt"));
  assert.ok(Math.abs(days(ca.notBefore, ca.notAfter) - 3650) < 1.1);
  assert.ok(ca.serial > 1n);
  assert.deepEqual(
    readdirSync(at(directory, "tls")).filter((n) => /ca-|previous|next/.test(n)),
    [],
  );
  // The whole cycle can start again.
  assert.equal(prepare({ ...options, rotateCA: "trust" }).generation, 5);
});

test("rollback makes a new generation with the content of the previous one", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  assert.throws(() => prepare({ ...options, rollback: true }), /previous/i);
  const treesFirst = trees(directory);
  const leafFirst = read(directory, "tls/server.crt");
  const secrets = serviceSecrets(directory);
  prepare({ ...options, rotateLeaf: true });
  assert.notDeepEqual(trees(directory), treesFirst);
  const rolled = prepare({ ...options, rollback: true });
  assert.equal(rolled.generation, 3);
  assertCurrent(directory, 3);
  assert.deepEqual(trees(directory), treesFirst);
  assert.deepEqual(read(directory, "tls/server.crt"), leafFirst);
  assert.deepEqual(serviceSecrets(directory), secrets);
  assertTrusted(directory);
  assert.equal(bundle(directory).previous.generation, 2);
  const settled = snapshot(directory);
  const again = prepare(options);
  assert.equal(again.reused, true);
  assert.equal(again.generation, 3);
  assert.deepEqual(snapshot(directory), settled);
  // A second rollback returns to the generation we left: numbers never decrease.
  assert.equal(prepare({ ...options, rollback: true }).generation, 4);
  assert.notDeepEqual(trees(directory), treesFirst);
});

test("rollback also restores configuration, binding and the browser origin; a tampered archive is refused", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const configBefore = read(directory, "auth/config.json");
  const envBefore = read(directory, "compose.env");
  rewriteInputs(options.inputs, (value) => (value.registration = { enabled: true }));
  const changed = { ...options, origin: "https://moved.invalid" };
  prepare({ ...changed, reconfigure: true });
  assert.notDeepEqual(read(directory, "auth/config.json"), configBefore);
  prepare({ ...changed, rollback: true });
  assert.deepEqual(read(directory, "auth/config.json"), configBefore);
  assert.deepEqual(read(directory, "compose.env"), envBefore);
  assert.equal(bundle(directory).binding.origin, "https://demo.invalid");
  // The original inputs are the binding again.
  rewriteInputs(options.inputs, (value) => delete value.registration);
  assert.equal(prepare(options).reused, true);
  prepare({ ...options, rotateLeaf: true });
  const archived = at(directory, ".generations/3/auth/config.json");
  writeFileSync(archived, "{}\n");
  const before = snapshot(directory);
  assert.throws(() => prepare({ ...options, rollback: true }), /integrity|changed/i);
  assert.deepEqual(snapshot(directory), before);
});

test("operations exclude each other and --update-images", (t) => {
  const { options } = fixture(t);
  prepare(options);
  const flags = [
    { rotateLeaf: true },
    { rotateCA: "trust" },
    { reconfigure: true },
    { rollback: true },
  ];
  for (const [i, a] of flags.entries())
    for (const b of flags.slice(i + 1))
      assert.throws(() => prepare({ ...options, ...a, ...b }), /exclusive|one operation/i);
  for (const a of flags)
    assert.throws(() => prepare({ ...options, ...a, updateImages: true }), /exclusive|one operation/i);
  assert.equal(bundle(options.directory).generation, 1);
});

test("an interrupted generation write leaves the bundle usable and is cleaned or completed on the next run", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const before = snapshot(directory);
  const crash = (step) => (name) => {
    if (name === step) throw new Error(`crash at ${name}`);
  };
  // Before the commit marker: the unfinished .next is deleted.
  assert.throws(() => prepare({ ...options, rotateLeaf: true, afterStep: crash("staged") }), /crash at staged/);
  assert.ok(existsSync(at(directory, ".next")));
  assert.deepEqual(snapshot(directory), before);
  const repeated = prepare(options);
  assert.equal(repeated.reused, true);
  assert.equal(repeated.generation, 1);
  assert.equal(repeated.digest, first.digest);
  assert.ok(!existsSync(at(directory, ".next")));
  assert.deepEqual(snapshot(directory), before);
  // Foreign debris in .next is deleted too, whatever it holds.
  mkdirSync(at(directory, ".next/auth"), { recursive: true, mode: 0o700 });
  writeFileSync(at(directory, ".next/auth/half"), "x", { mode: 0o600 });
  assert.equal(prepare(options).reused, true);
  assert.ok(!existsSync(at(directory, ".next")));
  // The same operation then succeeds.
  assert.equal(prepare({ ...options, rotateLeaf: true }).generation, 2);
  // After the commit marker: the switch is completed, never half applied.
  for (const step of ["committed", "switched:auth", "switched:tls"]) {
    const generation = bundle(directory).generation;
    assert.throws(() => prepare({ ...options, rotateLeaf: true, afterStep: crash(step) }), /crash at/);
    const completed = prepare(options);
    assert.equal(completed.generation, generation + 1, step);
    assert.equal(completed.reused, true);
    assert.ok(!existsSync(at(directory, ".next")));
    assertCurrent(directory, generation + 1);
    assertTrusted(directory);
  }
});

test("a bundle of the earlier format without a generation is read as generation 0", (t) => {
  const { options } = fixture(t);
  const first = prepare(options);
  const directory = first.directory;
  const treesFirst = trees(directory);
  // Rebuild the old on-disk format by hand.
  for (const target of targets)
    writeFileSync(at(directory, `${target}/.ready`), "completion-private-v1\n");
  const walk = (prefix = "") =>
    readdirSync(join(directory, prefix))
      .sort()
      .flatMap((name) => {
        const path = prefix === "" ? name : join(prefix, name);
        if (prefix === "" && [".generations", ".next", "bundle.json"].includes(name)) return [];
        return lstatSync(join(directory, path)).isDirectory()
          ? walk(path)
          : [{ path, sha256: digest(join(directory, path)) }];
      });
  const legacy = bundle(directory);
  for (const key of ["generation", "tls", "previous", "caRotation", "generatedAt"]) delete legacy[key];
  legacy.files = walk();
  rmSync(at(directory, ".generations"), { recursive: true, force: true });
  writeFileSync(at(directory, "bundle.json"), JSON.stringify(legacy, null, 2) + "\n");
  const reused = prepare(options);
  assert.equal(reused.reused, true);
  assert.equal(reused.generation, 0);
  const rotated = prepare({ ...options, rotateLeaf: true });
  assert.equal(rotated.generation, 1);
  assertCurrent(directory, 1);
  assertTrusted(directory);
  assert.equal(bundle(directory).previous.generation, 0);
  const back = prepare({ ...options, rollback: true });
  assert.equal(back.generation, 2);
  assert.deepEqual(trees(directory), treesFirst);
  assertCurrent(directory, 2);
});

test("the command line exposes the rotation, reconfiguration and rollback operations", (t) => {
  const { options } = fixture(t);
  const base = [
    prepareScript,
    "--directory", options.directory,
    "--inputs", options.inputs,
    "--project", options.project,
    "--origin", options.origin,
  ];
  const run = (...extra) =>
    JSON.parse(execFileSync("node", [...base, ...extra], { encoding: "utf8", timeout: 30000 }));
  assert.equal(run().generation, 1);
  assert.equal(run("--rotate-leaf").generation, 2);
  assert.equal(run("--rotate-ca", "--phase", "trust").generation, 3);
  assert.equal(run("--rotate-ca", "--phase", "serve").generation, 4);
  assert.equal(run("--rotate-ca", "--phase", "retire").generation, 5);
  assert.equal(run("--reconfigure").reused, true);
  assert.equal(run("--rollback").generation, 6);
  const fails = (...extra) => {
    try {
      execFileSync("node", [...base, ...extra], { stdio: "pipe", timeout: 30000 });
    } catch (error) {
      return error.status;
    }
    return 0;
  };
  assert.equal(fails("--rotate-ca"), 1);
  assert.equal(fails("--phase", "trust"), 1);
  assert.equal(fails("--rotate-ca", "--phase", "never"), 1);
  assert.equal(fails("--rotate-leaf", "--reconfigure"), 1);
});

test("compose.env pins every locked service by digest and refuses a service missing from the lock", async (t) => {
  const lock = JSON.parse(readFileSync(lockURL, "utf8"));
  const manifest = JSON.parse(readFileSync(sourcesURL, "utf8"));
  const { options } = fixture(t);
  const result = prepare(options);
  const env = readFileSync(join(result.directory, "compose.env"), "utf8");
  for (const service of Object.keys(manifest.services)) {
    const locked = lock.services[service];
    assert.equal(locked.sourceSHA, manifest.services[service]);
    assert.ok(
      env.includes(
        `COMPLETION_${service.toUpperCase()}_IMAGE=ghcr.io/justikra/umir-${service}:sha-${locked.sourceSHA}@${locked.platformDigest}\n`,
      ),
      service,
    );
    assert.ok(env.includes(`COMPLETION_${service.toUpperCase()}_SHA=${locked.sourceSHA}\n`));
  }
  // Feedback is not published to the registry: the lock records it as such and
  // the bundle keeps the exact-source tag.
  assert.equal(lock.unpublished.feedback, manifest.optionalServices.feedback);
  assert.ok(
    env.includes(
      `COMPLETION_FEEDBACK_IMAGE=ghcr.io/justikra/umir-feedback:sha-${lock.unpublished.feedback}\n`,
    ),
  );

  const { options: other, root } = fixture(t);
  const script = join(root, "prepare.mjs");
  writeFileSync(script, readFileSync(new URL("../../../scripts/completion/prepare.mjs", import.meta.url)));
  writeFileSync(join(root, "sources.json"), readFileSync(sourcesURL));
  delete lock.services.gateway;
  writeFileSync(join(root, "images.lock.json"), JSON.stringify(lock));
  const runtime = await import(pathToFileURL(script).href);
  assert.throws(() => runtime.prepare(other), /gateway.*lock|lock.*gateway/i);
  assert.equal(existsSync(other.directory), false, "nothing is created when the lock is incomplete");
});

test("Feedback service keys are separate 0600 files of the keys target, not environment lines", (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const gateway = at(directory, "keys/gateway-feedback");
  const discovery = at(directory, "keys/feedback-auth");
  for (const path of [gateway, discovery]) {
    assert.equal(lstatSync(path).mode & 0o777, 0o600);
    assert.match(readFileSync(path, "utf8"), /^[a-f0-9]{64}$/);
  }
  // The same values the other side of each pair holds.
  assert.equal(
    readFileSync(discovery, "utf8"),
    readFileSync(at(directory, "auth/secrets/feedback-auth"), "utf8"),
  );
  const env = at(directory, "env/feedback.env");
  assert.ok(!existsSync(env) || !/KEY/.test(readFileSync(env, "utf8")));
  assert.deepEqual(readReady(at(directory, "keys")).generation, 1);
});

test("a bundle from before the keys target gets the same key files at its first change", (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const gateway = readFileSync(at(directory, "keys/gateway-feedback"), "utf8");
  const discovery = readFileSync(at(directory, "keys/feedback-auth"), "utf8");
  // Rebuild the earlier layout: keys in env/feedback.env, no keys target.
  rmSync(at(directory, "keys"), { recursive: true });
  writeFileSync(
    at(directory, "env/feedback.env"),
    `FEEDBACK_GATEWAY_KEY=${gateway}\nFEEDBACK_AUTH_DISCOVERY_KEY=${discovery}\n`,
    { mode: 0o600 },
  );
  const walk = (prefix = "") =>
    readdirSync(join(directory, prefix))
      .sort()
      .flatMap((name) => {
        const path = prefix === "" ? name : join(prefix, name);
        if (prefix === "" && [".generations", ".next", "bundle.json"].includes(name)) return [];
        return lstatSync(join(directory, path)).isDirectory()
          ? walk(path)
          : [{ path, sha256: digest(join(directory, path)) }];
      });
  const legacy = bundle(directory);
  legacy.files = walk();
  writeFileSync(at(directory, "bundle.json"), JSON.stringify(legacy, null, 2) + "\n");
  assert.equal(prepare(options).reused, true);
  const rotated = prepare({ ...options, rotateLeaf: true });
  assert.equal(rotated.generation, 2);
  assert.equal(readFileSync(at(directory, "keys/gateway-feedback"), "utf8"), gateway);
  assert.equal(readFileSync(at(directory, "keys/feedback-auth"), "utf8"), discovery);
  assert.equal(readReady(at(directory, "keys")).generation, 2);
  // Going back to the generation without the target keeps the keys.
  const back = prepare({ ...options, rollback: true });
  assert.equal(back.generation, 3);
  assert.equal(readFileSync(at(directory, "keys/gateway-feedback"), "utf8"), gateway);
});
