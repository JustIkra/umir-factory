// Tests of the private-init script. The script is read from
// compose.completion.yaml (through `docker compose config`, so `$$` is already
// resolved, exactly what the container receives) and executed:
//   - on the host with `sh` against temporary /bootstrap and /targets
//     directories, where PATH shims log every changing command, replace chown
//     (the host user cannot give files to 65532) and can stop the script at the
//     N-th changing command, which simulates a crash;
//   - in the pinned alpine image (busybox) with bind mounts, as a compatibility
//     check. Docker is required: without it these tests fail.
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  chmodSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { prepare } from "../../../scripts/completion/prepare.mjs";
import {
  digest,
  fixture,
  readReady,
  serviceDirectory,
  targets as names,
  treeSha,
} from "./support.mjs";

const factory = new URL("../../../", import.meta.url).pathname;

let specCache;
function spec(t) {
  if (specCache === undefined) {
    const { options } = fixture(t);
    const result = prepare(options);
    const model = JSON.parse(
      execFileSync(
        "docker",
        [
          "compose",
          "--env-file",
          join(result.directory, "compose.env"),
          "-f",
          join(factory, "compose.completion.yaml"),
          "config",
          "--format",
          "json",
        ],
        { cwd: factory, encoding: "utf8", timeout: 20000 },
      ),
    );
    const service = model.services["private-init"];
    assert.deepEqual(service.command.slice(0, 2), ["sh", "-ec"]);
    specCache = {
      // `config` prints the command as Compose input, with "$" written as "$$";
      // the container receives the single "$".
      script: service.command[2].replaceAll("$$", "$"),
      image: service.image,
      service,
    };
  }
  return specCache;
}

function temporary(t, prefix) {
  const path = realpathSync(mkdtempSync(join(tmpdir(), prefix)));
  t.after(() => rmSync(path, { recursive: true, force: true }));
  return path;
}

// A prepared private bundle (generation 1). `rotate` makes the next generation.
function bundle(t) {
  const { options } = fixture(t);
  const result = prepare(options);
  return {
    directory: result.directory,
    rotate() {
      return prepare({ ...options, rotateLeaf: true });
    },
    options,
  };
}

function emptyTargets(t) {
  const root = temporary(t, "umir-private-init-targets-");
  for (const name of names) mkdirSync(join(root, name));
  return root;
}

// ---- Running the script ----------------------------------------------------

const realTool = (name) =>
  execFileSync("sh", ["-c", `command -v ${name}`], { encoding: "utf8" }).trim();
const mutating = ["mv", "cp", "rm", "rmdir", "mkdir", "chmod", "chown", "sync"];

// Shims go first on PATH. Every changing command is appended to the log. With
// `stopAt` = N the N-th changing command and all later ones fail without doing
// anything: the power went out there.
function host(t, { stopAt } = {}) {
  const dir = temporary(t, "umir-private-init-shims-");
  const log = join(dir, "log");
  const counter = join(dir, "count");
  writeFileSync(log, "");
  writeFileSync(counter, "0");
  const tool = (name, body) => {
    writeFileSync(join(dir, name), `#!/bin/sh\n${body}`, { mode: 0o755 });
    chmodSync(join(dir, name), 0o755);
  };
  for (const name of mutating) {
    const real = name === "chown" ? "true" : realTool(name);
    const exec = name === "chown" ? "exit 0" : `exec ${real} "$@"`;
    tool(
      name,
      `printf '%s\\n' "${name} $*" >> '${log}'
n=$(cat '${counter}'); n=$((n + 1)); echo $n > '${counter}'
${stopAt === undefined ? "" : `if [ "$n" -ge ${stopAt} ]; then echo "injected stop at ${name}" >&2; exit 1; fi`}
${exec}
`,
    );
  }
  if (spawnSync("sh", ["-c", "command -v sha256sum"]).status !== 0)
    tool("sha256sum", `exec shasum -a 256 "$@"\n`);
  return {
    kind: "host",
    log: () =>
      readFileSync(log, "utf8")
        .split("\n")
        .filter(Boolean),
    calls: () => Number(readFileSync(counter, "utf8")),
    run(script, { bootstrap, targets }) {
      assert.ok(script.includes("/bootstrap") && script.includes("/targets"));
      const text = script
        .split("/bootstrap")
        .join(bootstrap)
        .split("/targets")
        .join(targets);
      const result = spawnSync("sh", ["-ec", text], {
        encoding: "utf8",
        env: { ...process.env, PATH: `${dir}:${process.env.PATH}` },
      });
      return { status: result.status, stderr: result.stderr + result.stdout };
    },
  };
}

function docker(t) {
  const { image } = spec(t);
  return {
    kind: "docker",
    run(script, { bootstrap, targets }) {
      const result = spawnSync(
        "docker",
        [
          "run",
          "--rm",
          "--network",
          "none",
          "--user",
          "0:0",
          "-v",
          `${bootstrap}:/bootstrap:ro`,
          "-v",
          `${targets}:/targets`,
          image,
          "sh",
          "-ec",
          script,
        ],
        { encoding: "utf8", timeout: 120000 },
      );
      assert.notEqual(result.status, null, "docker run did not finish");
      return { status: result.status, stderr: result.stderr + result.stdout };
    },
    stat(targets) {
      return execFileSync(
        "docker",
        [
          "run",
          "--rm",
          "--network",
          "none",
          "-v",
          `${targets}:/targets:ro`,
          image,
          "sh",
          "-c",
          "find /targets -exec stat -c '%u:%g %a %n' {} +",
        ],
        { encoding: "utf8", timeout: 120000 },
      )
        .split("\n")
        .filter(Boolean);
    },
  };
}

const apply = (t, runner, { bootstrap, targets }) =>
  runner.run(spec(t).script, { bootstrap, targets });

// ---- Observing volumes ------------------------------------------------------

// Every entry (including service directories) with kind, mode and content hash.
function dump(root, prefix = "") {
  const out = {};
  for (const name of readdirSync(join(root, prefix)).sort()) {
    const path = prefix === "" ? name : `${prefix}/${name}`;
    const stat = lstatSync(join(root, path));
    if (stat.isDirectory()) {
      out[path] = `dir ${stat.mode & 0o777}`;
      Object.assign(out, dump(root, path));
    } else {
      out[path] = `file ${stat.mode & 0o777} ${digest(join(root, path))}`;
    }
  }
  return out;
}
function volumeFiles(root, prefix = "") {
  return readdirSync(join(root, prefix)).flatMap((name) => {
    const path = prefix === "" ? name : `${prefix}/${name}`;
    if (serviceDirectory.test(path)) return [];
    return lstatSync(join(root, path)).isDirectory()
      ? volumeFiles(root, path)
      : [path];
  });
}
const content = (root) =>
  Object.fromEntries(
    volumeFiles(root)
      .sort()
      .map((path) => [path, readFileSync(join(root, path)).toString("base64")]),
  );

// Volume equals generation `generation` of the bundle: same files, same marker.
function assertCurrent(directory, targets, generation, label = "") {
  for (const name of names) {
    const volume = join(targets, name);
    const source = join(directory, name);
    assert.deepEqual(content(volume), content(source), `${label} ${name} files`);
    const ready = readReady(volume);
    assert.equal(ready?.generation, generation, `${label} ${name} generation`);
    assert.equal(ready.tree, treeSha(volume), `${label} ${name} marker tree`);
    assert.equal(ready.tree, readReady(source).tree);
  }
}
// With a marker the volume is consistent: its files are exactly the marker's tree.
function assertConsistentIfReady(targets, label) {
  for (const name of names) {
    const volume = join(targets, name);
    if (!existsSync(join(volume, ".ready"))) continue;
    const ready = readReady(volume);
    assert.notEqual(ready, null, `${label} ${name} marker readable`);
    assert.equal(treeSha(volume), ready.tree, `${label} ${name} mixed content`);
  }
}
function assertNoLeftovers(targets) {
  for (const name of names)
    assert.deepEqual(
      readdirSync(join(targets, name)).filter((entry) =>
        /^\.(next-|previous\.tmp)/.test(entry),
      ),
      [],
      `${name} leftovers`,
    );
}

// ---- Tests -------------------------------------------------------------------

for (const kind of ["host", "docker"]) {
  const runner = (t) => (kind === "host" ? host(t) : docker(t));

  test(`${kind}: an empty volume is copied, owned by 65532, with the marker last`, (t) => {
    const b = bundle(t);
    const targets = emptyTargets(t);
    const r = runner(t);
    const result = apply(t, r, { bootstrap: b.directory, targets });
    assert.equal(result.status, 0, result.stderr);
    assertCurrent(b.directory, targets, 1);
    for (const name of names) {
      const ready = readFileSync(join(targets, name, ".ready"), "utf8");
      assert.equal(
        ready,
        readFileSync(join(b.directory, name, ".ready"), "utf8"),
      );
      assert.match(
        ready,
        /^completion-private-v2 generation=1\ntree-sha256=[a-f0-9]{64}\n$/,
      );
    }
    if (kind === "host") {
      const log = r.log();
      for (const name of names) {
        const target = join(targets, name);
        assert.ok(
          log.some((line) => line.startsWith("chown") && line.includes("65532:65532")),
          "chown 65532:65532",
        );
        const moves = log
          .map((line, index) => [line, index])
          .filter(([line]) => line.startsWith("mv ") && line.includes(`${target}/`));
        const ready = moves.filter(([line]) => line.endsWith(`${target}/.ready`));
        assert.equal(ready.length, 1, `${name} .ready moved once`);
        assert.equal(
          ready[0][1],
          Math.max(...moves.map(([, index]) => index)),
          `${name} .ready is the last move`,
        );
      }
      const chowns = log.filter((line) => line.startsWith("chown"));
      assert.ok(chowns.length > 0);
      assert.ok(
        chowns.every((line) => /65532:65532/.test(line)),
        chowns.join("\n"),
      );
      for (const name of names) {
        const volume = join(targets, name);
        for (const path of volumeFiles(volume).concat([".ready"]))
          assert.equal(lstatSync(join(volume, path)).mode & 0o777, 0o600, path);
        for (const path of Object.keys(dump(volume)).filter(
          (p) => lstatSync(join(volume, p)).isDirectory(),
        ))
          assert.equal(lstatSync(join(volume, path)).mode & 0o777, 0o700, path);
        assert.equal(lstatSync(volume).mode & 0o777, 0o700);
      }
    } else {
      const lines = r.stat(targets);
      assert.ok(lines.length > names.length * 3, lines.join("\n"));
      for (const line of lines) {
        const [owner, mode, path] = line.split(" ");
        // The mount point /targets itself belongs to whoever created it.
        if (path === "/targets") continue;
        assert.equal(owner, "65532:65532", line);
        assert.equal(
          mode,
          lines.some((other) => other.split(" ")[2].startsWith(`${path}/`)) ||
            ["/targets", ...names.map((n) => `/targets/${n}`)].includes(path)
            ? "700"
            : "600",
          line,
        );
      }
    }
  });

  test(`${kind}: the same generation with the same digest changes nothing`, (t) => {
    const b = bundle(t);
    const targets = emptyTargets(t);
    assert.equal(
      apply(t, runner(t), { bootstrap: b.directory, targets }).status,
      0,
    );
    const before = dump(targets);
    const second = runner(t);
    const result = apply(t, second, { bootstrap: b.directory, targets });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(dump(targets), before);
    if (kind === "host") assert.deepEqual(second.log(), []);
  });

  test(`${kind}: a newer generation is applied and the previous one is kept`, (t) => {
    const b = bundle(t);
    const targets = emptyTargets(t);
    assert.equal(
      apply(t, runner(t), { bootstrap: b.directory, targets }).status,
      0,
    );
    const first = Object.fromEntries(
      names.map((name) => [name, content(join(targets, name))]),
    );
    const firstReady = Object.fromEntries(
      names.map((name) => [name, readReady(join(targets, name))]),
    );
    assert.equal(b.rotate().generation, 2);
    const result = apply(t, runner(t), { bootstrap: b.directory, targets });
    assert.equal(result.status, 0, result.stderr);
    assertCurrent(b.directory, targets, 2);
    assertNoLeftovers(targets);
    for (const name of names) {
      const previous = join(targets, name, ".previous");
      assert.deepEqual(content(previous), first[name], `${name} previous files`);
      assert.deepEqual(readReady(previous), firstReady[name]);
    }
    // The leaf rotation changed exactly the auth and transport targets.
    assert.notEqual(
      readReady(join(targets, "auth")).tree,
      firstReady.auth.tree,
    );
    // Running it again is a no-op.
    const again = dump(targets);
    assert.equal(
      apply(t, runner(t), { bootstrap: b.directory, targets }).status,
      0,
    );
    assert.deepEqual(dump(targets), again);
  });

  test(`${kind}: an older generation or another digest at the same number is refused`, (t) => {
    const b = bundle(t);
    const targets = emptyTargets(t);
    const old = temporary(t, "umir-private-init-old-");
    assert.equal(
      apply(t, runner(t), { bootstrap: b.directory, targets }).status,
      0,
    );
    cpSync(b.directory, join(old, "gen1"), { recursive: true });
    b.rotate();
    assert.equal(
      apply(t, runner(t), { bootstrap: b.directory, targets }).status,
      0,
    );
    const before = dump(targets);
    const older = apply(t, runner(t), {
      bootstrap: join(old, "gen1"),
      targets,
    });
    assert.notEqual(older.status, 0);
    assert.match(older.stderr, /older/i);
    assert.deepEqual(dump(targets), before, "older generation left the volume");

    // Same number, different content: another independently prepared bundle.
    const other = bundle(t);
    const sameNumber = apply(t, runner(t), {
      bootstrap: other.directory,
      targets: (() => {
        const fresh = emptyTargets(t);
        assert.equal(
          apply(t, runner(t), { bootstrap: join(old, "gen1"), targets: fresh })
            .status,
          0,
        );
        return fresh;
      })(),
    });
    assert.notEqual(sameNumber.status, 0);
    assert.match(sameNumber.stderr, /different content|same generation/i);
  });
}

test("an interrupted first copy is completed, also over foreign files", (t) => {
  const b = bundle(t);
  // Half of the files are there and there is no marker.
  const partial = emptyTargets(t);
  for (const name of names) {
    // The marker is written last, so a half-copied volume never has it.
    const files = volumeFiles(join(b.directory, name)).sort().filter((path) => path !== ".ready");
    for (const path of files.slice(0, Math.ceil(files.length / 2))) {
      mkdirSync(join(partial, name, path, ".."), { recursive: true });
      cpSync(join(b.directory, name, path), join(partial, name, path));
    }
  }
  let result = apply(t, host(t), { bootstrap: b.directory, targets: partial });
  assert.equal(result.status, 0, result.stderr);
  assertCurrent(b.directory, partial, 1, "partial");

  // Files that are not part of the bundle and no marker: the volume is built up
  // the same way and the strangers are removed (the tree must equal the bundle).
  const foreign = emptyTargets(t);
  for (const name of names) {
    writeFileSync(join(foreign, name, "stranger.txt"), "x");
    mkdirSync(join(foreign, name, "sub", "deeper"), { recursive: true });
    writeFileSync(join(foreign, name, "sub", "deeper", "file with space"), "y");
  }
  result = apply(t, host(t), { bootstrap: b.directory, targets: foreign });
  assert.equal(result.status, 0, result.stderr);
  assertCurrent(b.directory, foreign, 1, "foreign");
  for (const name of names)
    assert.ok(!existsSync(join(foreign, name, "stranger.txt")));
});

// Stops the script at every changing command in turn. After each stop the volume
// must be consistent whenever a marker is present, and the next normal run must
// finish the job.
function sweep(t, { seed, bootstrap, generation }) {
  const probe = host(t);
  const trial = temporary(t, "umir-private-init-sweep-");
  cpSync(seed, join(trial, "t"), { recursive: true });
  assert.equal(
    apply(t, probe, { bootstrap, targets: join(trial, "t") }).status,
    0,
  );
  const total = probe.calls();
  assert.ok(total > 20, `${total} changing commands`);
  let stopped = 0;
  for (let at = 1; at <= total; at++) {
    const targets = join(trial, `s${at}`);
    cpSync(seed, targets, { recursive: true });
    const crash = host(t, { stopAt: at });
    const first = apply(t, crash, { bootstrap, targets });
    assert.notEqual(first.status, 0, `stop at ${at} must fail the run`);
    stopped++;
    assertConsistentIfReady(targets, `stop at ${at}`);
    const second = apply(t, host(t), { bootstrap, targets });
    assert.equal(second.status, 0, `stop at ${at}: ${second.stderr}`);
    assertCurrent(bootstrap, targets, generation, `stop at ${at}`);
    assertNoLeftovers(targets);
    rmSync(targets, { recursive: true, force: true });
  }
  assert.equal(stopped, total);
}

test("a first copy stopped at any command is finished by the next run", (t) => {
  const b = bundle(t);
  sweep(t, { seed: emptyTargets(t), bootstrap: b.directory, generation: 1 });
});

test("a generation change stopped at any command is finished without mixing generations", (t) => {
  const b = bundle(t);
  const targets = emptyTargets(t);
  assert.equal(
    apply(t, host(t), { bootstrap: b.directory, targets }).status,
    0,
  );
  b.rotate();
  sweep(t, { seed: targets, bootstrap: b.directory, generation: 2 });
});

test("a change stopped midway is finished by busybox too", (t) => {
  const b = bundle(t);
  const targets = emptyTargets(t);
  assert.equal(
    apply(t, host(t), { bootstrap: b.directory, targets }).status,
    0,
  );
  b.rotate();
  const crash = host(t, { stopAt: 25 });
  assert.notEqual(
    apply(t, crash, { bootstrap: b.directory, targets }).status,
    0,
  );
  assertConsistentIfReady(targets, "after the stop");
  const result = apply(t, docker(t), { bootstrap: b.directory, targets });
  assert.equal(result.status, 0, result.stderr);
  assertCurrent(b.directory, targets, 2);
  assertNoLeftovers(targets);
});

test("a volume with the old marker counts as generation 0", (t) => {
  const b = bundle(t);
  const legacy = (directory) => {
    for (const name of names)
      writeFileSync(join(directory, name, ".ready"), "completion-private-v1\n");
  };
  const targets = emptyTargets(t);
  assert.equal(
    apply(t, host(t), { bootstrap: b.directory, targets }).status,
    0,
  );
  legacy(targets);
  const before = Object.fromEntries(
    names.map((name) => [name, content(join(targets, name))]),
  );

  // An old bundle (generation 0) over an equal old volume: nothing changes.
  const oldBundle = temporary(t, "umir-private-init-legacy-");
  cpSync(b.directory, oldBundle, { recursive: true });
  legacy(oldBundle);
  const same = dump(targets);
  assert.equal(
    apply(t, host(t), { bootstrap: oldBundle, targets }).status,
    0,
  );
  assert.deepEqual(dump(targets), same);

  // An old bundle whose files differ from the old volume: refused.
  writeFileSync(join(oldBundle, "trust", "ca.crt"), "tampered\n");
  const refused = apply(t, host(t), { bootstrap: oldBundle, targets });
  assert.notEqual(refused.status, 0);
  assert.deepEqual(dump(targets), same);

  // A generation 1 bundle replaces the old volume and keeps it as previous.
  const upgrade = apply(t, host(t), { bootstrap: b.directory, targets });
  assert.equal(upgrade.status, 0, upgrade.stderr);
  assertCurrent(b.directory, targets, 1);
  for (const name of names) {
    const previous = join(targets, name, ".previous");
    assert.deepEqual(content(previous), before[name]);
    assert.equal(
      readFileSync(join(previous, ".ready"), "utf8"),
      "completion-private-v1\n",
    );
  }

  // The old marker never replaces a newer one.
  const back = apply(t, host(t), { bootstrap: oldBundle, targets });
  assert.notEqual(back.status, 0);
});

test("the executed script is the one in compose.completion.yaml and is plain POSIX sh", (t) => {
  const { script, service } = spec(t);
  const yaml = readFileSync(join(factory, "compose.completion.yaml"), "utf8");
  // Compose resolves "$$" to "$": the raw file must carry no unescaped "$".
  const raw = yaml
    .split("\n")
    .slice(yaml.split("\n").findIndex((line) => line.includes("- |")) + 1)
    .join("\n")
    .split("\n  transport:")[0];
  assert.ok(!/(^|[^$])\$[A-Za-z_({]/.test(raw), "unescaped dollar in the script");
  assert.equal(raw.replace(/\$\$/g, "$").replace(/^ {8}/gm, "").trim(), script.trim());
  const probe = host(t);
  const bootstrap = bundle(t).directory;
  const targets = emptyTargets(t);
  assert.equal(probe.run(script, { bootstrap, targets }).status, 0);
  assert.equal(service.network_mode, "none");
  assert.equal(service.user, "0:0");
  for (const bashism of [
    /\[\[/,
    /<<</,
    /<\(/,
    /\blocal\b/,
    /\bfunction\b/,
    /[ =(]\$'/,
    /==/,
    /\$\{[^}]*(\/\/|:\d|,,|\^\^)/,
    /\barray\b|\bdeclare\b|\bmapfile\b|\bsource\b/,
    /\bls -A\b|diff -rq/,
  ])
    assert.ok(!bashism.test(script), `bashism ${bashism}`);
});

test("a bundle from before the keys target is applied without it", (t) => {
  const b = bundle(t);
  rmSync(join(b.directory, "keys"), { recursive: true });
  const targets = emptyTargets(t);
  const result = apply(t, host(t), { bootstrap: b.directory, targets });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /keys: not in the bundle, skipped/);
  for (const name of names.filter((n) => n !== "keys")) {
    assert.equal(readReady(join(targets, name))?.generation, 1, name);
  }
  assert.equal(existsSync(join(targets, "keys", ".ready")), false);
});
