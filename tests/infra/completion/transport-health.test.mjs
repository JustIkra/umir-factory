import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { createServer as createHTTPS } from "node:https";
import { createServer } from "node:tls";
import { join } from "node:path";
import { test } from "node:test";
import { promisify } from "node:util";
import { prepare } from "../../../scripts/completion/prepare.mjs";
import { certInfo, fixture } from "./support.mjs";

const script = new URL(
  "../../../scripts/completion/transport-health.mjs",
  import.meta.url,
).pathname;
const execFileAsync = promisify(execFile);
const day = 86400000;

async function run(args) {
  try {
    const { stdout } = await execFileAsync("node", [script, ...args], { timeout: 30000 });
    return { code: 0, stdout, stderr: "" };
  } catch (error) {
    return { code: error.code, stdout: error.stdout, stderr: error.stderr };
  }
}

// TLS listeners with the transport certificate of a prepared bundle.
async function listeners(t, directory, count = 2) {
  const servers = [];
  for (let i = 0; i < count; i++) {
    const server = createServer({
      key: readFileSync(join(directory, "transport/server.key")),
      cert: readFileSync(join(directory, "transport/server.crt")),
    }, (socket) => socket.end());
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    servers.push(server);
  }
  t.after(() => servers.forEach((server) => server.close()));
  return servers.map((server) => server.address().port);
}

test("the handshake succeeds on every port with the CA of the bundle and reports no secrets", async (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const ports = await listeners(t, directory);
  const result = await run(["127.0.0.1", ...ports, "--ca", join(directory, "health/ca.crt")]);
  assert.equal(result.code, 0, result.stderr);
  const report = JSON.parse(result.stdout);
  assert.deepEqual(report.ports.map((p) => p.port), ports);
  assert.ok(report.ports.every((p) => p.ok));
  assert.ok(!result.stdout.includes("PRIVATE KEY"));
});

test("a wrong name, a foreign CA, an expired leaf and a closed port fail", async (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const ca = join(directory, "health/ca.crt");
  const [port, second] = await listeners(t, directory, 2);
  // The leaf is issued for "transport": another name is refused.
  const wrongName = await run(["127.0.0.1", port, "--ca", ca, "--servername", "other.invalid"]);
  assert.equal(wrongName.code, 1);
  // A CA that did not sign the leaf.
  const { options: foreign } = fixture(t);
  const other = prepare(foreign);
  const foreignCA = await run(["127.0.0.1", port, "--ca", join(other.directory, "health/ca.crt")]);
  assert.equal(foreignCA.code, 1);
  // The clock is injected: after the end of the leaf.
  const end = certInfo(join(directory, "transport/server.crt")).notAfter.getTime();
  const late = await run(["127.0.0.1", port, "--ca", ca, "--now", new Date(end + day).toISOString()]);
  assert.equal(late.code, 1);
  assert.match(late.stdout + late.stderr, /expired|expire|not valid/i);
  // One bad port among good ones fails the whole check.
  const closed = await run(["127.0.0.1", second, "1", "--ca", ca]);
  assert.equal(closed.code, 1);
  // Bad usage.
  assert.equal((await run(["127.0.0.1", port])).code, 2);
});

test("--healthz also requires /healthz to answer 200 on the listed ports", async (t) => {
  const { options } = fixture(t);
  const { directory } = prepare(options);
  const tlsOptions = {
    key: readFileSync(join(directory, "transport/server.key")),
    cert: readFileSync(join(directory, "transport/server.crt")),
  };
  const servers = [200, 503].map((status) =>
    createHTTPS(tlsOptions, (request, response) => {
      response.statusCode = request.url === "/healthz" ? status : 404;
      response.end();
    }),
  );
  const ports = [];
  for (const server of servers) {
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    ports.push(server.address().port);
  }
  t.after(() => servers.forEach((server) => server.close()));
  const ca = join(directory, "health/ca.crt");
  const good = await run(["127.0.0.1", ports[0], "--ca", ca, "--healthz", String(ports[0])]);
  assert.equal(good.code, 0, good.stderr);
  const bad = await run(["127.0.0.1", ports[1], "--ca", ca, "--healthz", String(ports[1])]);
  assert.equal(bad.code, 1);
  assert.match(bad.stdout, /503/);
  // Without --healthz only the handshake counts.
  assert.equal((await run(["127.0.0.1", ports[1], "--ca", ca])).code, 0);
});
