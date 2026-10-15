import { pathToFileURL } from "node:url";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdtempSync,
  writeFileSync,
  readFileSync,
  rmSync,
  lstatSync,
  symlinkSync,
  realpathSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { prepare } from "../../../scripts/completion/prepare.mjs";

function fixture(t) {
  const root = realpathSync(
    mkdtempSync(join(tmpdir(), "umir-completion-prepare-")),
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
  return {
    root,
    options: {
      directory: join(root, "private"),
      inputs,
      project: "umir-completion-test",
      origin: "https://demo.invalid",
      cookieMode: "secure",
    },
  };
}
const digest = (path) =>
  createHash("sha256").update(readFileSync(path)).digest("hex");

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
  const sources = JSON.parse(
    readFileSync(
      new URL("../../../scripts/completion/sources.json", import.meta.url),
      "utf8",
    ),
  );
  writeFileSync(sourcePath, JSON.stringify(sources));
  const runtime = await import(pathToFileURL(script).href);
  const original = runtime.prepare(options);
  const key = join(original.directory, "auth/secrets/gateway-auth");
  const config = join(original.directory, "auth/config.json");
  const before = [digest(key), digest(config)];
  sources.services.frontend = "b".repeat(40);
  writeFileSync(sourcePath, JSON.stringify(sources));
  assert.throws(() => runtime.prepare(options), /source|image/i);
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
