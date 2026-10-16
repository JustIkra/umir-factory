// Shared helpers for the completion infra tests. Everything here is synthetic;
// OpenSSL is the real binary, and its output is read only through
// `x509 -noout -startdate -enddate -serial`, whose lines are the same across
// OpenSSL 1.1, 3.x and 4.x.
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  lstatSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

export const prepareScript = new URL(
  "../../../scripts/completion/prepare.mjs",
  import.meta.url,
).pathname;

export function fixture(t) {
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
export const digest = (path) =>
  createHash("sha256").update(readFileSync(path)).digest("hex");


const months = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");
function opensslDate(text) {
  const m = /^(\w{3})\s+(\d+) (\d\d):(\d\d):(\d\d) (\d{4}) GMT$/.exec(text);
  if (m === null) throw new Error(`unexpected OpenSSL date: ${text}`);
  return new Date(
    Date.UTC(+m[6], months.indexOf(m[1]), +m[2], +m[3], +m[4], +m[5]),
  );
}
/** First certificate of a PEM file: validity window and serial number. */
export function certInfo(path) {
  const out = execFileSync(
    "openssl",
    ["x509", "-noout", "-startdate", "-enddate", "-serial", "-in", path],
    { encoding: "utf8" },
  );
  const field = (name) =>
    new RegExp(`^${name}=(.+)$`, "m").exec(out)?.[1].trim();
  return {
    notBefore: opensslDate(field("notBefore")),
    notAfter: opensslDate(field("notAfter")),
    serial: BigInt("0x" + field("serial")),
  };
}
export const days = (from, to) => (to - from) / 86400000;
/** True when `openssl verify` trusts `certificate` through the CA bundle. */
export function verifies(caFile, certificate) {
  try {
    execFileSync("openssl", ["verify", "-CAfile", caFile, certificate], {
      stdio: "pipe",
    });
    return true;
  } catch {
    return false;
  }
}
const publicKey = (args) =>
  execFileSync("openssl", args, { encoding: "utf8" }).trim();
/** True when the private key belongs to the certificate. */
export const keyMatches = (certificate, key) =>
  publicKey(["x509", "-in", certificate, "-noout", "-pubkey"]) ===
  publicKey(["pkey", "-in", key, "-pubout"]);
export const pemCount = (path) =>
  (readFileSync(path, "utf8").match(/-----BEGIN CERTIFICATE-----/g) ?? [])
    .length;

/** Service directories private-init keeps at the top of a target volume. */
export const serviceDirectory = /^\.(previous(\.tmp)?|next-[^/]*)(\/|$)/;
function files(root, prefix = "") {
  return readdirSync(join(root, prefix)).flatMap((name) => {
    const path = prefix === "" ? name : `${prefix}/${name}`;
    return lstatSync(join(root, path)).isDirectory()
      ? files(root, path)
      : [path];
  });
}
/**
 * Independent implementation of the documented tree digest (see prepare.mjs):
 * sha256 over the lines "<sha256 of file>  <path>\n" of every file below the
 * target except the top-level .ready and the service directories of a volume
 * (.previous, .previous.tmp, .next-*), in byte order of the path.
 */
export function treeSha(target) {
  const listing = files(target)
    .filter((path) => path !== ".ready" && !serviceDirectory.test(path))
    .sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)))
    .map(
      (path) =>
        createHash("sha256")
          .update(readFileSync(join(target, path)))
          .digest("hex") +
        "  " +
        path +
        "\n",
    )
    .join("");
  return createHash("sha256").update(listing).digest("hex");
}
export const targets = ["auth", "transport", "trust", "health", "keys"];
export function readReady(target) {
  const m =
    /^completion-private-v2 generation=(\d+)\ntree-sha256=([a-f0-9]{64})\n$/.exec(
      readFileSync(join(target, ".ready"), "utf8"),
    );
  return m === null ? null : { generation: Number(m[1]), tree: m[2] };
}
export const trees = (directory) =>
  Object.fromEntries(targets.map((t) => [t, treeSha(join(directory, t))]));
/** Every file of the bundle except bundle.json and the generation store. */
export function snapshot(directory) {
  return Object.fromEntries(
    files(directory)
      .filter(
        (path) =>
          path !== "bundle.json" &&
          !path.startsWith(".generations/") &&
          !path.startsWith(".next/"),
      )
      .sort()
      .map((path) => [path, digest(join(directory, path))]),
  );
}
export const bundle = (directory) =>
  JSON.parse(readFileSync(join(directory, "bundle.json"), "utf8"));
export const rewriteInputs = (path, change) => {
  const value = JSON.parse(readFileSync(path, "utf8"));
  change(value);
  writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
};
