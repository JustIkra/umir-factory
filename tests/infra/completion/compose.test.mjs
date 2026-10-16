import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  mkdtempSync,
  readFileSync,
  writeFileSync,
  rmSync,
  realpathSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { prepare } from "../../../scripts/completion/prepare.mjs";

const factory = new URL("../../../", import.meta.url).pathname;

// Runs `docker compose ... config --format json` and returns the parsed model.
// `env` entries are added to the process environment; `envFile` is a Compose env file.
function composeJSON(files, { env = {}, envFile, profiles = [] } = {}) {
  const args = ["compose"];
  for (const profile of profiles) args.push("--profile", profile);
  if (envFile !== undefined) args.push("--env-file", envFile);
  for (const file of files) args.push("-f", join(factory, file));
  args.push("config", "--format", "json");
  return JSON.parse(
    execFileSync("docker", args, {
      cwd: factory,
      encoding: "utf8",
      timeout: 20000,
      env: { ...process.env, ...env },
    }),
  );
}

// Prepares a synthetic completion bundle and returns its Compose env file path.
function completionEnv(t) {
  const root = realpathSync(
    mkdtempSync(join(tmpdir(), "umir-completion-compose-")),
  );
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const username = join(root, "smtp-user");
  const password = join(root, "smtp-password");
  writeFileSync(username, "synthetic-user\n", { mode: 0o600 });
  writeFileSync(password, "synthetic-password\n", { mode: 0o600 });
  const inputs = join(root, "inputs.json");
  writeFileSync(
    inputs,
    JSON.stringify({
      initialUser: {
        userID: "11111111-1111-4111-8111-111111111111",
        login: "demo.operator",
        displayName: "Demo Operator",
        credentialVersion: "1",
      },
      smtp: {
        Host: "smtp.invalid",
        Port: 587,
        TLSMode: "starttls",
        From: "auth@demo.invalid",
        To: "inbox@demo.invalid",
      },
      smtpUsernameFile: username,
      smtpPasswordFile: password,
      registrationMail: {
        OperatorName: "Synthetic Operator",
        OperatorAddress: "Isolated test stand",
        SenderName: "Umir",
        PrivacyURL: "",
      },
    }),
    { mode: 0o600 },
  );
  const result = prepare({
    directory: join(root, "private"),
    inputs,
    project: "umir-completion-test-compose",
    origin: "https://demo.invalid",
    cookieMode: "secure",
  });
  return join(result.directory, "compose.env");
}

// Synthetic inputs for the optional overlays; the files are empty and live in a temporary directory.
function overlayEnv(t) {
  const root = realpathSync(
    mkdtempSync(join(tmpdir(), "umir-completion-overlay-")),
  );
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const file = join(root, "empty");
  writeFileSync(file, "", { mode: 0o600 });
  return {
    OPERATOR_PUBLIC_ORIGIN: "http://127.0.0.1:8080",
    FEEDBACK_IMAGE: "ghcr.io/justikra/umir-feedback:sha-" + "a".repeat(40),
    FEEDBACK_ENV_FILE: file,
    TELEGRAM_VPN_IMAGE:
      "ghcr.io/justikra/umir-telegram-vpn:sha-" + "a".repeat(40),
    TELEGRAM_VPN_DNS: "192.0.2.53",
    TELEGRAM_VPN_SUBSCRIPTION_URL_FILE: file,
    TELEGRAM_PROXY_USERNAME_FILE: file,
    TELEGRAM_PROXY_PASSWORD_FILE: file,
  };
}

test("every warehouse service sets WAREHOUSE_API_MODE explicitly", (t) => {
  const legacy = [
    ["compose.yaml"],
    ["compose.yaml", "compose.dev.yaml"],
    ["compose.dev.yaml"],
    ["compose.yaml", "compose.auth.yaml"],
    ["compose.yaml", "compose.feedback.yaml"],
  ];
  const env = overlayEnv(t);
  for (const files of legacy) {
    const model = composeJSON(files, { env });
    assert.equal(
      model.services.warehouse?.environment?.WAREHOUSE_API_MODE,
      "legacy",
      files.join(" + "),
    );
  }
  const completion = composeJSON(["compose.completion.yaml"], {
    envFile: completionEnv(t),
  });
  assert.equal(
    completion.services.warehouse.environment.WAREHOUSE_API_MODE,
    "v2",
  );
});

const hostIP = (model, service) =>
  model.services[service].ports.map((port) => port.host_ip);

test("legacy Gateway stays on loopback unless the public overlay is added", () => {
  const env = { UMIR_BIND: "0.0.0.0" };
  const base = composeJSON(["compose.yaml"], { env });
  assert.deepEqual(hostIP(base, "gateway"), ["127.0.0.1"]);
  assert.deepEqual(hostIP(base, "frontend"), ["0.0.0.0"]);
  assert.deepEqual(
    hostIP(composeJSON(["compose.yaml"]), "gateway"),
    ["127.0.0.1"],
  );
  const open = composeJSON(["compose.yaml", "compose.legacy-public.yaml"], {
    env,
  });
  assert.deepEqual(hostIP(open, "gateway"), ["0.0.0.0"]);
  assert.equal(open.services.gateway.ports.length, 1);
  assert.equal(open.services.gateway.ports[0].target, 8080);
  assert.equal(open.services.gateway.ports[0].published, "8081");
  const defaults = composeJSON(["compose.yaml", "compose.legacy-public.yaml"]);
  assert.deepEqual(hostIP(defaults, "gateway"), ["127.0.0.1"]);
});

test("every completion image carries a digest, except an unpublished optional one", (t) => {
  const envFile = completionEnv(t);
  const lock = JSON.parse(
    readFileSync(
      new URL("../../../scripts/completion/images.lock.json", import.meta.url),
      "utf8",
    ),
  );
  const mailDirectory = realpathSync(
    mkdtempSync(join(tmpdir(), "umir-completion-mail-")),
  );
  t.after(() => rmSync(mailDirectory, { recursive: true, force: true }));
  const model = composeJSON(
    ["compose.completion.yaml", "tests/examples/completion/compose.mail.yaml"],
    {
      envFile,
      profiles: ["feedback"],
      env: { COMPLETION_TEST_MAIL_DIR: mailDirectory },
    },
  );
  const unpublished = Object.keys(lock.unpublished ?? {});
  const untagged = [];
  for (const [name, service] of Object.entries(model.services)) {
    if (/@sha256:[0-9a-f]{64}$/.test(service.image)) continue;
    untagged.push(name);
  }
  assert.deepEqual(untagged, unpublished.length === 0 ? [] : ["feedback"]);
  assert.ok(Object.keys(model.services).length >= 10, "all services were checked");
  // Every digest in Compose is the one in the lock.
  const locked = new Set([
    ...Object.values(lock.auxiliary).map((aux) => aux.ref),
    ...Object.entries(lock.services).map(
      ([service, entry]) =>
        `ghcr.io/justikra/umir-${service}:sha-${entry.sourceSHA}@${entry.platformDigest}`,
    ),
    ...unpublished.map(
      (service) =>
        `ghcr.io/justikra/umir-${service}:sha-${lock.unpublished[service]}`,
    ),
  ]);
  for (const [name, service] of Object.entries(model.services))
    assert.ok(locked.has(service.image), `${name}: ${service.image} is not in the lock`);
});

// Services whose pinned images cannot read their keys from files yet (the
// groups of Warehouse, Serving and Gateway add *_FILE in their wave 1). Shrink
// this list when the profile is re-pinned to images that support it.
const keysStillInEnvironment = ["warehouse", "serving", "gateway"];

test("Feedback gets its service keys as read-only files, never in the environment", (t) => {
  const model = composeJSON(["compose.completion.yaml"], {
    envFile: completionEnv(t),
    profiles: ["feedback"],
  });
  const feedback = model.services.feedback;
  const environment = feedback.environment;
  assert.equal(environment.FEEDBACK_GATEWAY_KEY, undefined);
  assert.equal(environment.FEEDBACK_AUTH_DISCOVERY_KEY, undefined);
  assert.equal(environment.FEEDBACK_GATEWAY_KEY_FILE, "/run/umir-keys/gateway-feedback");
  assert.equal(environment.FEEDBACK_AUTH_DISCOVERY_KEY_FILE, "/run/umir-keys/feedback-auth");
  assert.deepEqual(
    feedback.volumes.filter((v) => v.target === "/run/umir-keys").map((v) => v.read_only),
    [true],
  );
  // No value that looks like a key (64 hex digits) in the environment, and no env_file with keys.
  for (const [name, value] of Object.entries(environment))
    assert.ok(!/^[a-f0-9]{64}$/.test(value), name);
  assert.ok(!JSON.stringify(feedback).includes("env/feedback.env"));
  for (const service of Object.keys(model.services))
    if (!keysStillInEnvironment.includes(service) && service !== "feedback")
      assert.equal(model.services[service].env_file, undefined, service);
});

test("compose.sh runs Compose for a bundle and refuses `config` without --quiet", (t) => {
  const envFile = completionEnv(t);
  const bundle = envFile.slice(0, -"/compose.env".length);
  const script = new URL("../../../scripts/completion/compose.sh", import.meta.url).pathname;
  const run = (...args) => spawnSync("sh", [script, "--bundle", bundle, ...args], { encoding: "utf8", timeout: 20000 });
  const quiet = run("config", "--quiet");
  assert.equal(quiet.status, 0, quiet.stderr);
  assert.equal(quiet.stdout, "");
  for (const args of [["config"], ["config", "--format", "json"], ["--profile", "feedback", "config"]]) {
    const refused = run(...args);
    assert.equal(refused.status, 2, args.join(" "));
    assert.equal(refused.stdout, "");
    assert.match(refused.stderr, /--quiet/);
  }
  assert.equal(spawnSync("sh", [script], { encoding: "utf8" }).status, 2);
  assert.equal(run("config", "--quiet").stdout.includes("key"), false);
});

const longLived = ["transport", "transport-ready", "auth", "auth-ready", "warehouse", "serving", "gateway", "frontend", "feedback"];

test("every long-lived service is read-only, unprivileged and bounded in memory, processes and logs", (t) => {
  const model = composeJSON(["compose.completion.yaml"], {
    envFile: completionEnv(t),
    profiles: ["feedback"],
  });
  for (const name of longLived) {
    const service = model.services[name];
    assert.ok(service, name);
    assert.equal(service.read_only, true, `${name} read_only`);
    assert.deepEqual(service.cap_drop, ["ALL"], `${name} cap_drop`);
    assert.ok(service.security_opt.includes("no-new-privileges:true"), `${name} no-new-privileges`);
    assert.ok(Number(service.mem_limit) > 0, `${name} mem_limit`);
    assert.ok(Number(service.pids_limit) > 0, `${name} pids_limit`);
    assert.equal(service.logging?.driver, "json-file", `${name} logging`);
    assert.equal(service.logging.options["max-size"], "10m");
    assert.equal(service.logging.options["max-file"], "3");
    assert.match(String(service.user), /^[1-9]\d*(:[1-9]\d*)?$/, `${name} runs as a named non-root user`);
    assert.ok(service.restart === "unless-stopped", `${name} restart`);
  }
  const mb = (name) => Number(model.services[name].mem_limit) / 1048576;
  assert.deepEqual(
    Object.fromEntries(["auth", "gateway", "warehouse", "serving", "frontend", "transport", "feedback"].map((n) => [n, mb(n)])),
    { auth: 256, gateway: 256, warehouse: 512, serving: 256, frontend: 64, transport: 64, feedback: 128 },
  );
  // The one-shot job cannot be read-only-free either: no network, only the capabilities it needs.
  const init = model.services["private-init"];
  assert.equal(init.network_mode, "none");
  assert.deepEqual(init.cap_drop, ["ALL"]);
  assert.deepEqual([...init.cap_add].sort(), ["CHOWN", "DAC_OVERRIDE", "FOWNER"]);
  assert.ok(init.security_opt.includes("no-new-privileges:true"));
});

test("the transport has a TLS health check that Auth waits for, and the front network holds only Gateway and Frontend", (t) => {
  const model = composeJSON(["compose.completion.yaml"], {
    envFile: completionEnv(t),
    profiles: ["feedback"],
  });
  const health = model.services["transport-ready"];
  assert.deepEqual(health.healthcheck.test.slice(0, 3), ["CMD", "node", "/transport-health.mjs"]);
  assert.ok(health.healthcheck.test.includes("8442") && health.healthcheck.test.includes("8445"));
  assert.equal(model.services.auth.depends_on["transport-ready"].condition, "service_healthy");
  assert.equal(health.depends_on.transport.condition, "service_started");

  const networks = (name) => Object.keys(model.services[name].networks ?? { default: null }).sort();
  assert.deepEqual(networks("frontend"), ["edge", "front"]);
  assert.deepEqual(networks("gateway"), ["default", "front"]);
  for (const name of Object.keys(model.services))
    if (!["frontend", "gateway"].includes(name))
      assert.ok(!networks(name).includes("front"), `${name} must not join front`);
  assert.equal(model.networks.front.internal, true);
  const subnet = model.networks.front.ipam.config[0].subnet;
  assert.match(subnet, /^172\.29\.16\.0\/24$/);
  // Not ZeroTier (172.25.222.0/24) and not docker0 (172.17.0.0/16).
  assert.ok(!subnet.startsWith("172.25.222.") && !subnet.startsWith("172.17."));
  // The frontend is not on `default`, where Warehouse's internal listener is.
  assert.ok(!networks("frontend").includes("default"));
});
