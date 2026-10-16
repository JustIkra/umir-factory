#!/usr/bin/env bash
# Backup and restore of the Auth state with a real Docker daemon and a temporary
# project umir-completion-test-bk<random>. No stand services run: the volume is
# filled by hand and a stand-in container plays Auth. Fails (never skips) when
# Docker is missing.
set -euo pipefail
umask 077

root=$(cd "$(dirname "$0")/../.." && pwd)
project="umir-completion-test-bk$RANDOM$RANDOM"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/umir-completion-backup-test.XXXXXX")
volume="${project}_auth-state"
alpine=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).auxiliary.alpine.ref)' "$root/scripts/completion/images.lock.json")

cleanup() {
  # Only this test's own project: containers and volumes with its name.
  case "$project" in umir-completion-test-bk*) ;; *) echo "refusing to clean $project" >&2; exit 1 ;; esac
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$project")
  if [ -n "$ids" ]; then docker rm -f $ids >/dev/null 2>&1 || true; fi
  docker volume rm "$volume" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
check() { "$@" || fail "$*"; }
sha() { if command -v sha256sum >/dev/null; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }

docker version >/dev/null || fail "Docker is required"
docker image inspect "$alpine" >/dev/null 2>&1 || docker pull -q "$alpine" >/dev/null

bundle="$tmp/umir-completion-bundle"
mkdir -m 700 "$bundle" "$bundle/keys" "$bundle/.generations"
printf '{"binding":{"project":"%s"},"generation":3}\n' "$project" > "$bundle/bundle.json"
printf 'COMPLETION_PRIVATE_DIR=%s\n' "$bundle" > "$bundle/compose.env"
head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$bundle/keys/service-key"
echo old-generation > "$bundle/.generations/old"
key="$tmp/backup.key"
head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$key"
other_key="$tmp/other.key"
head -c 48 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$other_key"

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
  [ "$(grep -c -a -F "$(cat "$bundle/keys/service-key")" "$f" || true)" = 0 ] || fail "plain key in $f"
done
# The old generations are not part of the key archive.
openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -pass "file:$key" -in "$keys_out/bundle.tar.enc" | tar -t | grep -q 'generations' && fail "generations were archived"

# A bad backup key, a bad archive and a foreign project are refused before anything is created.
refused() {
  local label=$1; shift
  if "$@" >/dev/null 2>&1; then fail "$label was accepted"; fi
}
nothing_created() {
  docker volume inspect "$volume" >/dev/null 2>&1 && fail "$1: a volume was created"
  [ ! -e "$bundle" ] || fail "$1: a bundle was created"
  return 0
}
docker rm -f "$project-auth-1" >/dev/null
docker volume rm "$volume" >/dev/null
rm -rf "$bundle"

refused "wrong key" "$root/scripts/completion/restore.sh" --project "$project" --bundle-dir "$bundle" --from "$out" --keys-from "$keys_out" --key-file "$other_key"
nothing_created "wrong key"

cp -R "$out" "$tmp/damaged"; cp -R "$keys_out" "$tmp/damaged-keys"
printf 'X' | dd of="$tmp/damaged/auth-state.tar.enc" bs=1 seek=100 conv=notrunc 2>/dev/null
refused "damaged archive" "$root/scripts/completion/restore.sh" --project "$project" --bundle-dir "$bundle" --from "$tmp/damaged" --keys-from "$keys_out" --key-file "$key"
nothing_created "damaged archive"

cp -R "$out" "$tmp/foreign"
sed -i.bak "s/\"project\": \"$project\"/\"project\": \"umir-completion-test-other\"/" "$tmp/foreign/manifest.json"
refused "foreign project in the manifest" "$root/scripts/completion/restore.sh" --project "$project" --bundle-dir "$bundle" --from "$tmp/foreign" --keys-from "$keys_out" --key-file "$key"
nothing_created "foreign project"

refused "other project name" "$root/scripts/completion/restore.sh" --project umir-completion-test-bkother --bundle-dir "$bundle" --from "$out" --keys-from "$keys_out" --key-file "$key"
nothing_created "other project name"

# The real restore.
"$root/scripts/completion/restore.sh" --project "$project" --bundle-dir "$bundle" --from "$out" --keys-from "$keys_out" --key-file "$key" >/dev/null
got=$(docker run --rm --network none -v "$volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json /data/owner-secrets/key' | sha)
[ "$got" = "$want" ] || fail "restored data differ"
owner=$(docker run --rm --network none -v "$volume:/data:ro" "$alpine" sh -c 'stat -c "%u:%g %a" /data/auth.json')
[ "$owner" = "65532:65532 600" ] || fail "owner or mode changed: $owner"
[ "$(cat "$bundle/keys/service-key")" = "$(openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -pass "file:$key" -in "$keys_out/bundle.tar.enc" | tar -xO ./keys/service-key)" ] || fail "restored key differs"
[ "$(stat -c %a "$bundle" 2>/dev/null || stat -f %Lp "$bundle")" = 700 ] || fail "bundle mode"
[ ! -e "$bundle/.generations" ] || fail "generations restored"

# A second restore into the now non-empty project is refused and changes nothing.
before=$(docker run --rm --network none -v "$volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json' | sha)
refused "restore into a non-empty project" "$root/scripts/completion/restore.sh" --project "$project" --bundle-dir "$tmp/another" --from "$out" --keys-from "$keys_out" --key-file "$key"
[ ! -e "$tmp/another" ] || fail "non-empty project: a bundle was created"
after=$(docker run --rm --network none -v "$volume:/data:ro" "$alpine" sh -c 'cat /data/auth.json' | sha)
[ "$before" = "$after" ] || fail "the volume changed"

# Backup input checks.
refused "backup of another project's bundle" "$root/scripts/completion/backup.sh" --project umir-completion-test-bkother --bundle "$bundle" --key-file "$key" --out "$tmp/o1"
refused "backup with a world-readable key" "$root/scripts/completion/backup.sh" --project "$project" --bundle "$bundle" --key-file <(echo x) --out "$tmp/o2"
printf 'short' > "$tmp/short.key"
refused "backup with a short key" "$root/scripts/completion/backup.sh" --project "$project" --bundle "$bundle" --key-file "$tmp/short.key" --out "$tmp/o3"
refused "backup of a project that is not umir-completion" "$root/scripts/completion/backup.sh" --project other-project --bundle "$bundle" --key-file "$key" --out "$tmp/o4"
[ ! -e "$tmp/o1" ] && [ ! -e "$tmp/o3" ] && [ ! -e "$tmp/o4" ] || fail "an output directory was created for a refused backup"

echo "completion-backup: ok"
