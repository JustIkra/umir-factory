// Reports when the private TLS material of a completion bundle expires.
//
//   node check-expiry.mjs --directory <bundle> [--live host:port ...]
//                         [--now <ISO 8601>] [--warn-days 30]
//
// Prints {ca, leaf, daysLeft, warnDays[, live]} (dates are ISO 8601 UTC, no
// secrets). Exit status: 0 nothing expires within warn-days, 3 something does,
// 2 the bundle or an endpoint could not be read.
//
// Without --live the certificates in <bundle>/tls are read (ca.crt is the
// signing CA, server.crt the leaf). With --live each endpoint is contacted
// with TLS and its certificate is read without validating the chain: the
// point is the date of the certificate that is really served, and whether it is
// the one in the bundle (an old leaf means the services were not restarted).
import { X509Certificate } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { connect } from "node:tls";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const DAY = 86400000;
const months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");
function parseValidity(text) {
  const m = /^(\w{3})\s+(\d+) (\d\d):(\d\d):(\d\d) (\d{4}) GMT$/.exec(text);
  if (m === null || months.indexOf(m[1]) < 0)
    throw new Error("unreadable certificate validity");
  return new Date(
    Date.UTC(+m[6], months.indexOf(m[1]), +m[2], +m[3], +m[4], +m[5]),
  );
}
const certificateEnd = (certificate) => parseValidity(certificate.validTo);
function readLive(endpoint, timeout = 10000) {
  const m = /^(.+):(\d{1,5})$/.exec(endpoint);
  if (m === null || +m[2] < 1 || +m[2] > 65535)
    throw new Error("--live needs host:port");
  return new Promise((resolvePeer, reject) => {
    const socket = connect({
      host: m[1].replace(/^\[|\]$/g, ""),
      port: +m[2],
      rejectUnauthorized: false,
      timeout,
    });
    socket.once("secureConnect", () => {
      const peer = socket.getPeerCertificate();
      socket.end();
      if (!peer?.raw) reject(new Error(`${endpoint}: no certificate`));
      else resolvePeer(new X509Certificate(peer.raw));
    });
    socket.once("timeout", () => {
      socket.destroy();
      reject(new Error(`${endpoint}: timed out`));
    });
    socket.once("error", () => reject(new Error(`${endpoint}: cannot connect`)));
  });
}

export async function checkExpiry({
  directory,
  live = [],
  now = new Date(),
  warnDays = 30,
}) {
  if (typeof directory !== "string" || directory === "")
    throw new Error("--directory is required");
  if (!Number.isFinite(warnDays) || warnDays < 0)
    throw new Error("--warn-days must be a non-negative number");
  if (Number.isNaN(now.getTime())) throw new Error("--now must be an ISO 8601 date");
  directory = resolve(directory);
  const load = (name) => {
    const path = join(directory, name);
    if (!existsSync(path)) throw new Error(`${name} is missing in the bundle`);
    return new X509Certificate(readFileSync(path));
  };
  const caCertificate = load("tls/ca.crt");
  const leafCertificate = load("tls/server.crt");
  const ca = certificateEnd(caCertificate);
  const leaf = certificateEnd(leafCertificate);
  const recorded = existsSync(join(directory, "bundle.json"))
    ? JSON.parse(readFileSync(join(directory, "bundle.json"), "utf8")).tls
    : undefined;
  if (
    recorded !== undefined &&
    (recorded.caNotAfter !== ca.toISOString() ||
      recorded.leafNotAfter !== leaf.toISOString())
  )
    throw new Error("bundle.json disagrees with the certificates");
  const ends = [ca, leaf];
  const served = [];
  for (const endpoint of live) {
    const peer = await readLive(endpoint);
    const end = certificateEnd(peer);
    ends.push(end);
    served.push({
      endpoint,
      notAfter: end.toISOString(),
      matchesBundle: peer.raw.equals(leafCertificate.raw),
    });
  }
  const daysLeft = (Math.min(...ends.map(Number)) - now.getTime()) / DAY;
  return {
    report: {
      ca: ca.toISOString(),
      leaf: leaf.toISOString(),
      daysLeft: Math.round(daysLeft * 1000) / 1000,
      warnDays,
      ...(live.length > 0 ? { live: served } : {}),
    },
    expiring: daysLeft < warnDays,
  };
}

if (
  process.argv[1] !== undefined &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    const options = { live: [] };
    const argv = process.argv.slice(2);
    for (let i = 0; i < argv.length; i++) {
      const value = argv[i + 1];
      if (value === undefined) throw new Error("invalid argument");
      if (argv[i] === "--directory" && options.directory === undefined)
        options.directory = value;
      else if (argv[i] === "--live") options.live.push(value);
      else if (argv[i] === "--now" && options.now === undefined) {
        if (!/^\d{4}-\d\d-\d\dT[\d:.]+(Z|[+-]\d\d:\d\d)$/.test(value))
          throw new Error("--now must be an ISO 8601 date");
        options.now = new Date(value);
      } else if (argv[i] === "--warn-days" && options.warnDays === undefined) {
        if (!/^\d+(\.\d+)?$/.test(value))
          throw new Error("--warn-days must be a non-negative number");
        options.warnDays = Number(value);
      } else throw new Error("invalid argument");
      i++;
    }
    const { report, expiring } = await checkExpiry(options);
    console.log(JSON.stringify(report));
    process.exitCode = expiring ? 3 : 0;
  } catch (error) {
    console.error(
      error instanceof Error ? error.message : "expiry check failed",
    );
    process.exitCode = 2;
  }
}
