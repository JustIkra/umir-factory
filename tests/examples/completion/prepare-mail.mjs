import { randomBytes } from "node:crypto";
import { execFileSync } from "node:child_process";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  realpathSync,
  writeFileSync,
} from "node:fs";
import { join, resolve } from "node:path";
const destination = resolve(process.argv[2] ?? "");
if (!destination.includes("/umir-completion-") || existsSync(destination))
  throw new Error(
    "a fresh isolated umir-completion-* fixture directory is required",
  );
mkdirSync(destination, { recursive: true, mode: 0o700 });
const directory = realpathSync(destination);
const write = (name, body) =>
  writeFileSync(join(directory, name), body, { mode: 0o600, flag: "wx" });
const username = randomBytes(12).toString("hex");
const password = randomBytes(24).toString("hex");
write("smtp-user", username + "\n");
write("smtp-password", password + "\n");
write("smtp-auth", `${username}:${password}\n`);
write(
  "smtp.cnf",
  "[req]\ndistinguished_name=dn\nx509_extensions=extensions\nprompt=no\n[dn]\nCN=demo-smtp\n[extensions]\nsubjectAltName=DNS:demo-smtp\nbasicConstraints=critical,CA:TRUE\nkeyUsage=digitalSignature,keyCertSign\nextendedKeyUsage=serverAuth\n",
);
execFileSync(
  "openssl",
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
    "30",
    "-config",
    "smtp.cnf",
    "-keyout",
    "smtp.key",
    "-out",
    "smtp.crt",
  ],
  { cwd: directory, stdio: "ignore", timeout: 10000 },
);
chmodSync(join(directory, "smtp.key"), 0o600);
chmodSync(join(directory, "smtp.crt"), 0o600);
write(
  "inputs.json",
  JSON.stringify(
    {
      initialUser: {
        userID: "11111111-1111-4111-8111-111111111111",
        login: "demo.operator",
        displayName: "Демо оператор",
        credentialVersion: "1",
      },
      smtp: {
        Host: "demo-smtp",
        Port: 1025,
        TLSMode: "starttls",
        From: "auth@umir.invalid",
        To: "inbox@umir.invalid",
      },
      smtpUsernameFile: join(directory, "smtp-user"),
      smtpPasswordFile: join(directory, "smtp-password"),
      smtpCAFile: join(directory, "smtp.crt"),
      registrationMail: {
        OperatorName: "Демонстрационный оператор",
        OperatorAddress: "Изолированный тестовый стенд",
        SenderName: "Ровный ход",
        PrivacyURL: "",
      },
    },
    null,
    2,
  ) + "\n",
);
console.log(
  JSON.stringify({ directory, inputs: join(directory, "inputs.json") }),
);
