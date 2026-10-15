import assert from "node:assert/strict";
import { execFile, execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { createServer } from "node:tls";
import { join } from "node:path";
import { test } from "node:test";
import { promisify } from "node:util";
import { prepare } from "../../../scripts/completion/prepare.mjs";
import { bundle, certInfo, fixture } from "./support.mjs";

const script = new URL(
  "../../../scripts/completion/check-expiry.mjs",
  import.meta.url,
).pathname;
const execFileAsync = promisify(execFile);
const day = 86400000;
function run(args) {
  try {
    return {
      code: 0,
      stdout: execFileSync("node", [script, ...args], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
        timeout: 30000,
      }),
      stderr: "",
    };
  } catch (error) {
    return { code: error.status, stdout: error.stdout, stderr: error.stderr };
  }
}
const before = (directory, daysLeft) =>
  new Date(
    certInfo(join(directory, "tls/server.crt")).notAfter.getTime() -
      daysLeft * day,
  ).toISOString();

test("the leaf expiry is recorded in the bundle and warned about 30 days ahead with an injected clock", (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const leaf = certInfo(join(directory, "tls/server.crt")).notAfter;
  const ca = certInfo(join(directory, "tls/ca.crt")).notAfter;
  assert.equal(bundle(directory).tls.leafNotAfter, leaf.toISOString());
  const soon = run(["--directory", directory, "--now", before(directory, 29)]);
  assert.equal(soon.code, 3);
  const later = run(["--directory", directory, "--now", before(directory, 31)]);
  assert.equal(later.code, 0);
  const report = JSON.parse(later.stdout);
  assert.equal(report.leaf, leaf.toISOString());
  assert.equal(report.ca, ca.toISOString());
  assert.ok(Math.abs(report.daysLeft - 31) < 0.01);
  assert.equal(JSON.parse(soon.stdout).daysLeft < 30, true);
  // The threshold is configurable.
  const wide = ["--directory", directory, "--now", before(directory, 31)];
  assert.equal(run([...wide, "--warn-days", "40"]).code, 3);
  assert.equal(run([...wide, "--warn-days", "10"]).code, 0);
  // The real clock sees a fresh certificate.
  assert.equal(run(["--directory", directory]).code, 0);
  // The CA counts too: close to its end the check fails although the leaf lives on.
  const caSoon = new Date(ca.getTime() - 10 * day).toISOString();
  assert.equal(run(["--directory", directory, "--now", caSoon]).code, 3);
});

test("the check reports only dates and never prints secrets", (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const { stdout, stderr } = run(["--directory", directory]);
  for (const secret of [
    "tls/server.key",
    "tls/ca.key",
    "auth/secrets/password",
    "auth/secrets/gateway-auth",
  ]) {
    const body = readFileSync(join(directory, secret), "utf8").trim();
    assert.ok(!stdout.includes(body) && !stderr.includes(body), secret);
  }
  assert.deepEqual(Object.keys(JSON.parse(stdout)).sort(), ["ca", "daysLeft", "leaf", "warnDays"]);
});

test("unreadable or inconsistent bundles exit with 2", (t) => {
  const { options, root } = fixture(t);
  const { directory } = prepare(options);
  assert.equal(run(["--directory", join(root, "missing")]).code, 2);
  assert.equal(run([]).code, 2);
  assert.equal(run(["--directory", directory, "--now", "yesterday"]).code, 2);
  assert.equal(run(["--directory", directory, "--warn-days", "-1"]).code, 2);
  assert.equal(run(["--directory", directory, "--unknown"]).code, 2);
  assert.equal(run(["--directory", directory, "--live", "no-port"]).code, 2);
  // Metadata that disagrees with the certificate is an error, not a silent pass.
  const path = join(directory, "bundle.json");
  const value = JSON.parse(readFileSync(path, "utf8"));
  value.tls.leafNotAfter = "2001-01-01T00:00:00.000Z";
  writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
  assert.equal(run(["--directory", directory]).code, 2);
  // A bundle of the earlier format has no metadata: the certificate alone decides.
  delete value.tls;
  writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
  assert.equal(run(["--directory", directory]).code, 0);
});

async function serve(directory, t) {
  const server = createServer({
    key: readFileSync(join(directory, "transport/server.key")),
    cert: readFileSync(join(directory, "transport/server.crt")),
  });
  server.on("secureConnection", (socket) => socket.end());
  server.on("tlsClientError", () => {});
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());
  return server.address().port;
}
const runAsync = async (args) => {
  try {
    const { stdout } = await execFileAsync("node", [script, ...args], { timeout: 30000 });
    return { code: 0, stdout };
  } catch (error) {
    return { code: error.code, stdout: error.stdout };
  }
};

test("a live endpoint is read without trusting its chain and compared with the bundle", async (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const port = await serve(directory, t);
  const endpoint = `127.0.0.1:${port}`;
  const same = await runAsync(["--directory", directory, "--live", endpoint]);
  assert.equal(same.code, 0);
  const report = JSON.parse(same.stdout);
  assert.equal(report.live.length, 1);
  assert.equal(report.live[0].endpoint, endpoint);
  assert.equal(report.live[0].notAfter, report.leaf);
  assert.equal(report.live[0].matchesBundle, true);
  // The live certificate also counts towards the warning.
  const soon = await runAsync(["--directory", directory, "--live", endpoint, "--now", before(directory, 5)]);
  assert.equal(soon.code, 3);
  // After a rotation the running server still shows the old leaf until restarted.
  prepare({ ...options, rotateLeaf: true });
  const stale = await runAsync(["--directory", directory, "--live", endpoint]);
  assert.equal(stale.code, 0);
  assert.equal(JSON.parse(stale.stdout).live[0].matchesBundle, false);
  // Nothing listening is an error.
  const down = await runAsync(["--directory", directory, "--live", "127.0.0.1:1"]);
  assert.equal(down.code, 2);
});
