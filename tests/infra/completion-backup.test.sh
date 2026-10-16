#!/usr/bin/env bash
# Backup and restore of the Auth state with a real Docker daemon and a temporary
# project umir-completion-test-bk<random>. No stand services run: the volume is
# filled by hand and a stand-in container plays Auth. Fails (never skips) when
# Docker is missing.
set -euo pipefail
umask 077

root=$(cd "$(dirname "$0")/../.." && pwd)
project="umir-completion-test-bk$RANDOM$RANDOM"
restore_project="${project}-restore"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/umir-completion-backup-test.XXXXXX")
tmp=$(cd "$tmp" && pwd -P)
volume="${project}_auth-state"
restore_volume="${restore_project}_auth-state"
alpine=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).auxiliary.alpine.ref)' "$root/scripts/completion/images.lock.json")

cleanup() {
  # Only this test's own project: containers and volumes with its name.
  case "$project" in umir-completion-test-bk*) ;; *) echo "refusing to clean $project" >&2; exit 1 ;; esac
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
  if [ -n "$ids" ]; then docker rm -f $ids >/dev/null 2>&1 || true; fi
  docker volume rm "$volume" "$restore_volume" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
check() { "$@" || fail "$*"; }
sha() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }

docker version >/dev/null || fail "Docker is required"
docker image inspect "$alpine" >/dev/null 2>&1 || docker pull -q "$alpine" >/dev/null

bundle="$tmp/umir-completion-bundle"
restored_bundle="$tmp/umir-completion-restored-bundle"
mail="$tmp/mail"
node "$root/tests/examples/completion/prepare-mail.mjs" "$mail" >/dev/null
cp "$mail/inputs.json" "$tmp/base-inputs.json"
chmod 600 "$tmp/base-inputs.json"
prepare=(--directory "$bundle" --inputs "$mail/inputs.json" --project "$project" --origin https://demo.invalid --cookie-mode secure)
node "$root/scripts/completion/prepare.mjs" "${prepare[@]}" >/dev/null
node "$root/scripts/completion/prepare.mjs" "${prepare[@]}" --rotate-leaf >/dev/null
node - "$mail/inputs.json" <<'NODE'
const fs=require("fs");const path=process.argv[2];
const input=JSON.parse(fs.readFileSync(path,"utf8"));
input.registration={enabled:true,mailLimitPer10Minutes:10,organizationsPerUser:10,organizationCreationsPerHour:10,wrongCodesPerAddressPerHour:10};
fs.writeFileSync(path,JSON.stringify(input,null,2)+"\n",{mode:0o600});
NODE
node "$root/scripts/completion/prepare.mjs" "${prepare[@]}" --reconfigure >/dev/null
key="$tmp/backup.key"
head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$key"
other_key="$tmp/other.key"
head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$other_key"
source_bundle_sha=$(tar -C "$bundle" -cf - . | sha)
source_password_sha=$(sha < "$bundle/auth/secrets/password")
source_server_key_sha=$(sha < "$bundle/auth/secrets/server.key")
source_feedback_key_sha=$(sha < "$bundle/keys/feedback-auth")
key_pattern="$tmp/private-key-pattern"
cp "$bundle/auth/secrets/password" "$key_pattern"
chmod 600 "$key_pattern"

# The sentinel: random text in auth.json inside the volume, owned by 65532.
sentinel=$(head -c 40 /dev/urandom | od -An -tx1 | tr -d ' \n')
docker volume create "$volume" >/dev/null
docker run --rm --network none -v "$volume:/data" "$alpine" sh -c "mkdir -p /data/owner-secrets && printf '%s' '$sentinel' > /data/auth.json && printf 'second' > /data/owner-secrets/key && chown -R 65532:65532 /data && chmod 600 /data/auth.json"
want=$(docker run --rm --network none -v "$volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json /data/owner-secrets/key' | sha)

# A stand-in for Auth, so that the "stop only Auth and start it again" part is exercised.
docker run -d --name "$project-auth-1" --label "com.docker.compose.project=$project" --label com.docker.compose.service=auth --network none "$alpine" sleep 600 >/dev/null

out="$tmp/backup-data"
keys_out="$tmp/backup-keys"
"$root/scripts/completion/backup.sh" --project "$project" --bundle "$bundle" --key-file "$key" --out "$out" --keys-out "$keys_out" >/dev/null
check test -f "$out/manifest.json"
check test -f "$out/receipt.json"
check test -f "$out/auth-state.tar.enc"
check test -f "$keys_out/bundle.tar.enc"
check test ! -e "$out/bundle.tar.enc"
[ "$(docker inspect -f '{{.State.Running}}' "$project-auth-1")" = true ] || fail "Auth was not started again"
node -e '
const fs=require("fs");const [o,p]=process.argv.slice(1);
const m=JSON.parse(fs.readFileSync(o+"/manifest.json","utf8"));
const r=JSON.parse(fs.readFileSync(o+"/receipt.json","utf8"));
const ok=m.project===p&&m.format===1&&m.authStateAtCopy==="running"&&m.bundleGeneration===3&&/^[0-9a-f]{64}$/.test(m.data.plainSha256)&&m.data.plainSha256!==m.data.encryptedSha256&&m.keys.plainSha256!==m.data.plainSha256&&r.project===p&&/^[0-9a-f]{64}$/.test(r.manifestSha256)&&!isNaN(Date.parse(m.createdAt));
process.exit(ok?0:1)' "$out" "$project" || { cat "$out/manifest.json" "$out/receipt.json" >&2; fail "manifest or receipt"; }
[ "$(sha < "$out/manifest.json")" = "$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).manifestSha256)' "$out/receipt.json")" ] || fail "receipt does not match the manifest"
# The archive hides the sentinel and the bundle key.
for f in "$out/auth-state.tar.enc" "$keys_out/bundle.tar.enc"; do
  [ "$(grep -c -a -F "$sentinel" "$f" || true)" = 0 ] || fail "plain sentinel in $f"
  [ "$(grep -c -a -F -f "$key_pattern" "$f" || true)" = 0 ] || fail "plain key in $f"
done
# Rollback history stays in the encrypted key archive.
openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -pass "file:$key" -in "$keys_out/bundle.tar.enc" | tar -t | grep -F '.generations/2/meta.json' >/dev/null || fail "the previous generation metadata was not archived"

# A bad backup key, a bad archive and a foreign project are refused before anything is created.
refused() {
  local label=$1; shift
  if "$@" >/dev/null 2>&1; then fail "$label was accepted"; fi
}
nothing_created() {
  docker volume inspect "$restore_volume" >/dev/null 2>&1 && fail "$1: a volume was created"
  [ ! -e "$restored_bundle" ] || fail "$1: a bundle was created"
  return 0
}
docker rm -f "$project-auth-1" >/dev/null
docker volume rm "$volume" >/dev/null

refused "wrong key" "$root/scripts/completion/restore.sh" --project "$restore_project" --bundle-dir "$restored_bundle" --from "$out" --keys-from "$keys_out" --key-file "$other_key"
nothing_created "wrong key"

cp -R "$out" "$tmp/damaged"; cp -R "$keys_out" "$tmp/damaged-keys"
printf 'X' | dd of="$tmp/damaged/auth-state.tar.enc" bs=1 seek=100 conv=notrunc 2>/dev/null
refused "damaged archive" "$root/scripts/completion/restore.sh" --project "$restore_project" --bundle-dir "$restored_bundle" --from "$tmp/damaged" --keys-from "$keys_out" --key-file "$key"
nothing_created "damaged archive"

cp -R "$out" "$tmp/foreign"
sed -i.bak "s/\"project\": \"$project\"/\"project\": \"umir-completion-test-other\"/" "$tmp/foreign/manifest.json"
refused "foreign project in the manifest" "$root/scripts/completion/restore.sh" --project "$restore_project" --bundle-dir "$restored_bundle" --from "$tmp/foreign" --keys-from "$keys_out" --key-file "$key"
nothing_created "foreign project"

# Restore into a different project and package directory. The original package
# stays in place so its byte-level snapshot can be checked after restoration.
"$root/scripts/completion/restore.sh" --project "$restore_project" --bundle-dir "$restored_bundle" --from "$out" --keys-from "$keys_out" --key-file "$key" >/dev/null
got=$(docker run --rm --network none -v "$restore_volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json /data/owner-secrets/key' | sha)
[ "$got" = "$want" ] || fail "restored data differ"
owner=$(docker run --rm --network none -v "$restore_volume:/data:ro" "$alpine" sh -c 'stat -c "%u:%g %a" /data/auth.json')
[ "$owner" = "65532:65532 600" ] || fail "owner or mode changed: $owner"
[ "$(sha < "$restored_bundle/auth/secrets/password")" = "$source_password_sha" ] || fail "Auth password changed"
[ "$(sha < "$restored_bundle/auth/secrets/server.key")" = "$source_server_key_sha" ] || fail "server key changed"
[ "$(sha < "$restored_bundle/keys/feedback-auth")" = "$source_feedback_key_sha" ] || fail "Feedback key changed"
[ "$(stat -c %a "$restored_bundle" 2>/dev/null || stat -f %Lp "$restored_bundle")" = 700 ] || fail "bundle mode"
node - "$bundle" "$restored_bundle" "$restore_project" <<'NODE'
const fs=require("fs");const path=require("path");const [source,destination,project]=process.argv.slice(2);
const read=(root,file)=>fs.readFileSync(path.join(root,file),"utf8");
const fail=message=>{console.error(message);process.exit(1);};
const sourceState=JSON.parse(read(source,"bundle.json"));
const restoredState=JSON.parse(read(destination,"bundle.json"));
const env=read(destination,"compose.env");
if(restoredState.binding.project!==project||!env.includes(`COMPOSE_PROJECT_NAME=${project}\n`)||!env.includes(`COMPLETION_PRIVATE_DIR=${destination}\n`))fail("active package binding does not match destination");
if(JSON.stringify(sourceState.previous.trees)!==JSON.stringify(restoredState.previous.trees))fail("active generation tree digests changed during restore");
for(const target of ["auth","transport","trust","health","keys"])
  if(read(source,`${target}/.ready`)!==read(destination,`${target}/.ready`))fail(`active ${target} tree marker changed during restore`);
const generations=fs.readdirSync(path.join(destination,".generations")).sort();
if(!generations.includes("2"))fail("previous generation 2 is missing after restore");
for(const generation of generations) {
  const relative=`.generations/${generation}/meta.json`;
  const sourceMeta=JSON.parse(read(source,relative));
  const meta=JSON.parse(read(destination,relative));
  if(meta.binding.project!==project)fail("history generation project binding mismatch");
  meta.binding.project=sourceMeta.binding.project;
  if(JSON.stringify(meta)!==JSON.stringify(sourceMeta))fail("history metadata changed beyond its project binding");
  for(const target of ["auth","transport","trust","health","keys"])
    if(read(source,`.generations/${generation}/${target}/.ready`)!==read(destination,`.generations/${generation}/${target}/.ready`))fail(`history ${target} tree marker changed during restore`);
}
NODE
[ "$(tar -C "$bundle" -cf - . | sha)" = "$source_bundle_sha" ] || fail "source bundle changed during restore"

# Roll back the restored package, then repeat preparation and rotation under the
# destination project. The history must keep its target tree digests and binding.
node "$root/scripts/completion/prepare.mjs" --directory "$restored_bundle" --inputs "$mail/inputs.json" --project "$restore_project" --origin https://demo.invalid --cookie-mode secure --rollback >/dev/null
node - "$restored_bundle" "$restore_project" <<'NODE'
const fs=require("fs");const [bundle,project]=process.argv.slice(2);
const state=JSON.parse(fs.readFileSync(`${bundle}/bundle.json`,`utf8`));
const env=fs.readFileSync(`${bundle}/compose.env`,`utf8`);
if(state.binding.project!==project||!env.includes(`COMPOSE_PROJECT_NAME=${project}\n`))process.exit(1);
NODE
cp "$tmp/base-inputs.json" "$mail/inputs.json"
chmod 600 "$mail/inputs.json"
restore_prepare=(--directory "$restored_bundle" --inputs "$mail/inputs.json" --project "$restore_project" --origin https://demo.invalid --cookie-mode secure)
node "$root/scripts/completion/prepare.mjs" "${restore_prepare[@]}" >/dev/null
node "$root/scripts/completion/prepare.mjs" "${restore_prepare[@]}" --rotate-leaf >/dev/null
node "$root/scripts/completion/prepare.mjs" "${restore_prepare[@]}" >/dev/null
[ "$(tar -C "$bundle" -cf - . | sha)" = "$source_bundle_sha" ] || fail "rollback or rotation changed the source bundle"

# A second restore into the now non-empty project is refused and changes nothing.
before=$(docker run --rm --network none -v "$restore_volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json' | sha)
refused "restore into a non-empty project" "$root/scripts/completion/restore.sh" --project "$restore_project" --bundle-dir "$tmp/another" --from "$out" --keys-from "$keys_out" --key-file "$key"
[ ! -e "$tmp/another" ] || fail "non-empty project: a bundle was created"
after=$(docker run --rm --network none -v "$restore_volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json' | sha)
[ "$before" = "$after" ] || fail "the volume changed"

# Backup input checks.
refused "backup of another project's bundle" "$root/scripts/completion/backup.sh" --project umir-completion-test-bkother --bundle "$bundle" --key-file "$key" --out "$tmp/o1"
refused "backup with a world-readable key" "$root/scripts/completion/backup.sh" --project "$project" --bundle "$bundle" --key-file <(echo x) --out "$tmp/o2"
printf 'short' > "$tmp/short.key"
refused "backup with a short key" "$root/scripts/completion/backup.sh" --project "$project" --bundle "$bundle" --key-file "$tmp/short.key" --out "$tmp/o3"
refused "backup of a project that is not umir-completion" "$root/scripts/completion/backup.sh" --project other-project --bundle "$bundle" --key-file "$key" --out "$tmp/o4"
[ ! -e "$tmp/o1" ] && [ ! -e "$tmp/o3" ] && [ ! -e "$tmp/o4" ] || fail "an output directory was created for a refused backup"

echo "completion-backup: ok"
