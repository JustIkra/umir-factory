// Checks the private transport of the completion profile:
//
//   node transport-health.mjs <host> <port>... --ca <ca.crt>
//                             [--servername transport] [--now <ISO 8601>]
//                             [--healthz <port>,<port>...]
//
// For every port it makes a TLS handshake verified against the CA of the
// bundle, with the name the certificate was issued for, and checks that the
// certificate has not ended (--now injects the clock). Without --healthz the
// backends behind the transport are not contacted (they start later, so this is
// what the health check of the transport uses). With --healthz the listed ports
// also answer GET /healthz with 200, which proves the backend behind them is up
// (used after the stand started). Prints
// {ports: [{port, ok, notAfter, error?}]} without any secret.
// Exit status: 0 all ports are fine, 1 a port is not, 2 wrong usage.
import { readFileSync } from "node:fs";
import { request } from "node:https";
import { connect } from "node:tls";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");
function validTo(text) {
  const m = /^(\w{3})\s+(\d+) (\d\d):(\d\d):(\d\d) (\d{4}) GMT$/.exec(text);
  if (m === null || months.indexOf(m[1]) < 0) throw new Error("unreadable certificate validity");
  return new Date(Date.UTC(+m[6], months.indexOf(m[1]), +m[2], +m[3], +m[4], +m[5]));
}

function probe({ host, port, ca, servername, now, timeout = 3000 }) {
  return new Promise((done) => {
    const fail = (error) => done({ port, ok: false, error });
    const socket = connect({ host, port, ca, servername, timeout });
    socket.once("secureConnect", () => {
      try {
        const end = validTo(socket.getPeerCertificate().valid_to);
        socket.end();
        if (end.getTime() <= now.getTime())
          fail(`the certificate expired at ${end.toISOString()}`);
        else done({ port, ok: true, notAfter: end.toISOString() });
      } catch (error) {
        socket.destroy();
        fail(error.message);
      }
    });
    socket.once("timeout", () => {
      socket.destroy();
      fail("timed out");
    });
    socket.once("error", (error) => fail(error.code ?? "handshake failed"));
  });
}

function healthz({ host, port, ca, servername }) {
  return new Promise((done) => {
    const call = request({ host, port, ca, servername, path: "/healthz", timeout: 3000 }, (response) => {
      response.resume();
      done(response.statusCode === 200 ? null : `/healthz answered ${response.statusCode}`);
    });
    call.once("timeout", () => {
      call.destroy();
      done("/healthz timed out");
    });
    call.once("error", (error) => done(error.code ?? "/healthz failed"));
    call.end();
  });
}

export async function checkTransport({ host, ports, ca, servername = "transport", now = new Date(), healthzPorts = [] }) {
  const results = [];
  for (const port of ports) {
    const result = await probe({ host, port, ca, servername, now });
    if (result.ok && healthzPorts.includes(port)) {
      const error = await healthz({ host, port, ca, servername });
      if (error !== null) Object.assign(result, { ok: false, error });
    }
    results.push(result);
  }
  return { ports: results, ok: results.every((r) => r.ok) };
}

if (process.argv[1] !== undefined && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const argv = process.argv.slice(2);
    const positional = [];
    const options = {};
    for (let i = 0; i < argv.length; i++) {
      if (argv[i].startsWith("--")) {
        const value = argv[++i];
        if (value === undefined || !["--ca", "--servername", "--now", "--healthz"].includes(argv[i - 1]) || options[argv[i - 1]] !== undefined)
          throw new Error("invalid argument");
        options[argv[i - 1]] = value;
      } else positional.push(argv[i]);
    }
    const [host, ...ports] = positional;
    if (host === undefined || ports.length === 0 || options["--ca"] === undefined) throw new Error("usage");
    if (!ports.every((p) => /^\d{1,5}$/.test(p) && +p >= 1 && +p <= 65535)) throw new Error("invalid port");
    if (options["--now"] !== undefined && !/^\d{4}-\d\d-\d\dT[\d:.]+(Z|[+-]\d\d:\d\d)$/.test(options["--now"])) throw new Error("invalid --now");
    const report = await checkTransport({
      host,
      ports: ports.map(Number),
      ca: readFileSync(options["--ca"]),
      servername: options["--servername"],
      now: options["--now"] === undefined ? undefined : new Date(options["--now"]),
      healthzPorts: (options["--healthz"] ?? "").split(",").filter(Boolean).map(Number),
    });
    console.log(JSON.stringify(report));
    process.exitCode = report.ok ? 0 : 1;
  } catch (error) {
    console.error(error instanceof Error ? error.message : "transport check failed");
    process.exitCode = 2;
  }
}
