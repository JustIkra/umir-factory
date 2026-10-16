#!/usr/bin/env bash
# Encrypted backup of the Auth state of a completion stand.
#
#   backup.sh --project umir-completion-<name> --bundle <private bundle dir>
#             --key-file <file> --out <new dir> [--keys-out <new dir>]
#
# Makes two encrypted archives that are stored apart from each other:
#   auth-state.tar.enc  the Docker volume <project>_auth-state (auth.json and
#                       owner-secrets together), written to --out
#   bundle.tar.enc      the private bundle directory without .next (generation
#                       history is retained so a restored package can roll back),
#                       written to --keys-out (default: --out)
# plus manifest.json (in --out) and receipt.json (in --out), which
# `prepare.mjs --update-images --backup-receipt` accepts for 24 hours.
#
# Only Auth is stopped (not `down`), for the time of the copy, and started again
# if it was running. The key file stays outside the archives; its content is the
# passphrase of `openssl enc -aes-256-cbc -pbkdf2` (OpenSSL is on every VM and
# in the Alpine image, there is nothing else to install). Nothing here prints
# secrets.
set -euo pipefail
umask 077

die() { echo "backup.sh: $*" >&2; exit 1; }

project= bundle= key_file= out= keys_out=
while [ $# -gt 0 ]; do
  case "$1" in
    --project) project=${2:-}; shift 2 ;;
    --bundle) bundle=${2:-}; shift 2 ;;
    --key-file) key_file=${2:-}; shift 2 ;;
    --out) out=${2:-}; shift 2 ;;
    --keys-out) keys_out=${2:-}; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done
[ -n "$project" ] && [ -n "$bundle" ] && [ -n "$key_file" ] && [ -n "$out" ] || die "usage: backup.sh --project P --bundle DIR --key-file FILE --out DIR [--keys-out DIR]"
[[ "$project" =~ ^umir-completion-[a-z0-9-]{1,40}$ ]] || die "the project must be umir-completion-<name>"
case "$bundle$key_file$out$keys_out" in
  *$'\n'*) die "invalid path" ;;
esac
[[ "$bundle" = /* && "$key_file" = /* && "$out" = /* ]] || die "paths must be absolute"
keys_out=${keys_out:-$out}
[[ "$keys_out" = /* ]] || die "paths must be absolute"

root=$(cd "$(dirname "$0")/../.." && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
file_sha() { sha256 < "$1"; }

# The key file: a regular file, private, long enough to be a key.
[ -f "$key_file" ] && [ ! -L "$key_file" ] || die "the key file must be a regular file"
mode=$(stat -c %a "$key_file" 2>/dev/null || stat -f %Lp "$key_file")
[ "$mode" = 600 ] || [ "$mode" = 400 ] || die "the key file must have mode 0600 or 0400"
[ "$(wc -c < "$key_file")" -ge 32 ] || die "the key file must hold at least 32 bytes"

[ -d "$bundle" ] && [ ! -L "$bundle" ] && [ -f "$bundle/bundle.json" ] || die "not a private bundle: $bundle"
bundle_project=$(node -e 'const b=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(String(b.binding&&b.binding.project));process.stderr.write("")' "$bundle/bundle.json")
[ "$bundle_project" = "$project" ] || die "the bundle belongs to another project"
generation=$(node -e 'const b=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(String(b.generation??0))' "$bundle/bundle.json")

alpine=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).auxiliary.alpine.ref)' "$root/scripts/completion/images.lock.json")
case "$alpine" in *@sha256:*) ;; *) die "the locked Alpine image has no digest" ;; esac

volume="${project}_auth-state"
docker volume inspect "$volume" >/dev/null 2>&1 || die "no volume $volume"
for dir in "$out" "$keys_out"; do
  if [ -e "$dir" ]; then
    [ -d "$dir" ] && [ -z "$(ls -A "$dir")" ] || die "$dir must be a new or empty directory"
  fi
  mkdir -p "$dir"
  chmod 700 "$dir"
done

work=$(mktemp -d "${TMPDIR:-/tmp}/umir-completion-backup.XXXXXX")
created_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
auth_was_running=false
auth_ids=
cleanup() {
  status=$?
  rm -rf "$work"
  if [ "$auth_was_running" = true ]; then
    # shellcheck disable=SC2086
    docker start $auth_ids >/dev/null 2>&1 || echo "backup.sh: could not start Auth again: run docker compose -p $project start auth" >&2
  fi
  if [ "$status" -ne 0 ]; then
    rm -f "$out/auth-state.tar.enc" "$keys_out/bundle.tar.enc" "$out/manifest.json" "$out/receipt.json"
  fi
  exit "$status"
}
trap cleanup EXIT

encrypt() { openssl enc -aes-256-cbc -pbkdf2 -iter 600000 -salt -pass "file:$key_file" -out "$1"; }

# Auth of this project only (Compose labels), running now.
auth_ids=$(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=auth --filter status=running)
if [ -n "$auth_ids" ]; then
  auth_was_running=true
  # shellcheck disable=SC2086
  docker stop $auth_ids >/dev/null
fi

# Data: the volume, read-only, through a container without network. The plain
# archives exist only in the private temporary directory and are removed on exit.
docker run --rm --network none -v "$volume:/data:ro" "$alpine" tar -C /data -cf - . > "$work/auth-state.tar"
file_sha "$work/auth-state.tar" > "$work/data.plain.sha"
encrypt "$out/auth-state.tar.enc" < "$work/auth-state.tar"
rm -f "$work/auth-state.tar"

# Keys: the bundle directory.
tar -C "$bundle" --exclude=./.next -cf "$work/bundle.tar" .
file_sha "$work/bundle.tar" > "$work/keys.plain.sha"
encrypt "$keys_out/bundle.tar.enc" < "$work/bundle.tar"
rm -f "$work/bundle.tar"

# Auth goes back up as soon as the copy is done.
if [ "$auth_was_running" = true ]; then
  # shellcheck disable=SC2086
  docker start $auth_ids >/dev/null
  auth_was_running=false
  auth_state_at_copy=running
else
  auth_state_at_copy=stopped
fi

chmod 600 "$out/auth-state.tar.enc" "$keys_out/bundle.tar.enc"
node - "$out" "$keys_out" "$project" "$created_at" "$generation" "$auth_state_at_copy" \
  "$(cat "$work/data.plain.sha")" "$(file_sha "$out/auth-state.tar.enc")" \
  "$(cat "$work/keys.plain.sha")" "$(file_sha "$keys_out/bundle.tar.enc")" <<'NODE'
const fs = require("fs");
const crypto = require("crypto");
const [out, keysOut, project, createdAt, generation, authState, dataPlain, dataEnc, keysPlain, keysEnc] = process.argv.slice(2);
const manifest = {
  format: 1,
  project,
  createdAt,
  authStateAtCopy: authState,
  bundleGeneration: Number(generation),
  cipher: "aes-256-cbc pbkdf2 iter=600000",
  data: { file: "auth-state.tar.enc", plainSha256: dataPlain, encryptedSha256: dataEnc },
  keys: { file: "bundle.tar.enc", plainSha256: keysPlain, encryptedSha256: keysEnc },
};
const manifestText = JSON.stringify(manifest, null, 2) + "\n";
fs.writeFileSync(`${out}/manifest.json`, manifestText, { mode: 0o600 });
const receipt = {
  format: 1,
  project,
  createdAt,
  manifestSha256: crypto.createHash("sha256").update(manifestText).digest("hex"),
  manifest: `${out}/manifest.json`,
};
fs.writeFileSync(`${out}/receipt.json`, JSON.stringify(receipt, null, 2) + "\n", { mode: 0o600 });
NODE

echo "backup.sh: wrote $out/manifest.json and $out/receipt.json (data in $out, keys in $keys_out)"
