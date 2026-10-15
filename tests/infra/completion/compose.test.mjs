import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  mkdtempSync,
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
