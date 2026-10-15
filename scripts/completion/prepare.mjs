import { createHash, randomBytes } from "node:crypto";
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
  writeFileSync,
} from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const sourceFile = fileURLToPath(new URL("./sources.json", import.meta.url));
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
function tls(directory) {
  const args = [
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
      "365",
      "-subj",
      "/CN=Umir completion private CA",
      "-keyout",
      "ca.key",
      "-out",
      "ca.crt",
    ],
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
    [
      "x509",
      "-req",
      "-sha256",
      "-days",
      "365",
      "-in",
      "server.csr",
      "-CA",
      "ca.crt",
      "-CAkey",
      "ca.key",
      "-set_serial",
      "1",
      "-extfile",
      "server.ext",
      "-out",
      "server.crt",
    ],
  ];
  mkdirSync(join(directory, "tls"), { mode: 0o700 });
  write(
    directory,
    "tls/server.ext",
    "subjectAltName=DNS:auth,DNS:transport\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature\nextendedKeyUsage=serverAuth\n",
  );
  for (const command of args) {
    try {
      execFileSync("openssl", command, {
        cwd: join(directory, "tls"),
        stdio: "ignore",
        timeout: 10000,
      });
    } catch {
      throw new Error("private TLS certificate generation failed");
    }
  }
  for (const name of readdirSync(join(directory, "tls")))
    chmodSync(join(directory, "tls", name), 0o600);
}
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
function composeEnvironment(
  { directory, project, origin, cookieMode, sources },
  manifest,
) {
  const compose = {
    COMPOSE_PROJECT_NAME: project,
    COMPLETION_PRIVATE_DIR: directory,
    COMPLETION_ORIGIN: origin,
    COMPLETION_COOKIE_MODE: cookieMode,
    COMPLETION_WEB_PORT:
      new URL(origin).port || (cookieMode === "secure" ? "8080" : "80"),
    ...Object.fromEntries(
      Object.entries(pins(manifest)).map(([service, revision]) => [
        `COMPLETION_${service.toUpperCase()}_IMAGE`,
        `ghcr.io/justikra/umir-${service}:sha-${revision}`,
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
  const parent = openSync(directory, "r");
  try {
    fsyncSync(parent);
  } finally {
    closeSync(parent);
  }
}

export function prepare({
  directory,
  inputs,
  project,
  origin,
  cookieMode = "secure",
  sources,
  updateImages = false,
}) {
  validateOrigin(origin, cookieMode);
  if (
    !/^umir-completion-[a-z0-9-]{1,40}$/.test(project) ||
    !noControls(directory)
  )
    throw new Error("invalid owned completion project or directory");
  directory = resolve(directory);
  inputs = resolve(inputs);
  const inputBytes = privateFile(inputs);
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
    if (
      lstatSync(directory).isSymbolicLink() ||
      (lstatSync(directory).mode & 0o777) !== 0o700 ||
      !existsSync(join(directory, "bundle.json"))
    )
      throw new Error("incomplete private bundle or unsafe directory");
    const previous = JSON.parse(privateFile(join(directory, "bundle.json")));
    if (JSON.stringify(previous.binding) !== JSON.stringify(binding))
      throw new Error(
        "private bundle binding changed; do not reprovision existing authority",
      );
    if (JSON.stringify(previous.files) !== JSON.stringify(inventory(directory)))
      throw new Error("private bundle integrity changed or incomplete");
    if (
      previous.sources !== sha(readFileSync(sourceFile)) ||
      previous.sourceRoot !== (sources ?? null)
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
      previous.sources = sha(readFileSync(sourceFile));
      previous.sourceRoot = sources ?? null;
      previous.files = inventory(directory);
      replacePrivate(
        directory,
        "bundle.json",
        JSON.stringify(previous, null, 2) + "\n",
      );
    }
    return {
      directory,
      project,
      digest: sha(JSON.stringify(previous.files)),
      reused: true,
    };
  }
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
  const authPath = (name) => `/run/umir-auth/secrets/${name}`;
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
  const config = {
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
  write(directory, "auth/config.json", JSON.stringify(config, null, 2) + "\n");
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
    feedback: {
      FEEDBACK_GATEWAY_KEY: keys["gateway-feedback"],
      FEEDBACK_AUTH_DISCOVERY_KEY: keys["feedback-auth"],
    },
  };
  for (const [service, values] of Object.entries(env))
    write(
      directory,
      `env/${service}.env`,
      Object.entries(values)
        .map(([key, value]) => `${key}=${value}\n`)
        .join(""),
    );
  write(directory, "transport/conf/default.conf", transportConfig());
  for (const path of ["auth", "trust", "transport", "health"])
    write(directory, `${path}/.ready`, "completion-private-v1\n");
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
        sources: sha(readFileSync(sourceFile)),
        sourceRoot: sources ?? null,
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
    ]);
    const options = {};
    for (let i = 2; i < process.argv.length; i++) {
      if (process.argv[i] === "--update-images") {
        if (options.updateImages)
          throw new Error("duplicate image update argument");
        options.updateImages = true;
        continue;
      }
      const name = names.get(process.argv[i]);
      const value = process.argv[++i];
      if (name === undefined || value === undefined || name in options)
        throw new Error("invalid preparation argument");
      options[name] = value;
    }
    console.log(JSON.stringify(prepare(options)));
  } catch (error) {
    console.error(
      error instanceof Error ? error.message : "completion preparation failed",
    );
    process.exitCode = 1;
  }
}
