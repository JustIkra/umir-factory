#!/usr/bin/env bash
# Restores a backup made by backup.sh into an EMPTY project.
#
#   restore.sh --project umir-completion-<name> --bundle-dir <new dir>
#              --from <backup dir> [--keys-from <dir>] --key-file <file>
#
# It refuses when the project already has the volume <project>_auth-state, any
# container, or when --bundle-dir exists. Before anything is created it checks the
# manifest, the SHA-256 of both encrypted archives, decrypts them into a private
# temporary directory and compares the SHA-256 of the plain archives; a wrong
# key or a damaged archive stops it with nothing written. Then it creates the
# volume with its original owners and modes and the private bundle directory
# (mode 0700) at the path it had when it was made. Start the stand afterwards with the usual `docker compose up`.
set -euo pipefail
umask 077

die() { echo "restore.sh: $*" >&2; exit 1; }

project= bundle_dir= from= keys_from= key_file=
while [ $# -gt 0 ]; do
  case "$1" in
    --project) project=${2:-}; shift 2 ;;
    --bundle-dir) bundle_dir=${2:-}; shift 2 ;;
    --from) from=${2:-}; shift 2 ;;
    --keys-from) keys_from=${2:-}; shift 2 ;;
    --key-file) key_file=${2:-}; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done
[ -n "$project" ] && [ -n "$bundle_dir" ] && [ -n "$from" ] && [ -n "$key_file" ] || die "usage: restore.sh --project P --bundle-dir NEWDIR --from DIR [--keys-from DIR] --key-file FILE"
[[ "$project" =~ ^umir-completion-[a-z0-9-]{1,40}$ ]] || die "the project must be umir-completion-<name>"
keys_from=${keys_from:-$from}
[[ "$bundle_dir" = /* && "$from" = /* && "$keys_from" = /* && "$key_file" = /* ]] || die "paths must be absolute"

root=$(cd "$(dirname "$0")/../.." && pwd)
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
file_sha() { sha256 < "$1"; }

[ -f "$key_file" ] && [ ! -L "$key_file" ] || die "the key file must be a regular file"
mode=$(stat -c %a "$key_file" 2>/dev/null || stat -f %Lp "$key_file")
[ "$mode" = 600 ] || [ "$mode" = 400 ] || die "the key file must have mode 0600 or 0400"

volume="${project}_auth-state"
[ ! -e "$bundle_dir" ] || die "$bundle_dir exists: restore only into an empty project"
if docker volume inspect "$volume" >/dev/null 2>&1; then die "volume $volume exists: restore only into an empty project"; fi
[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$project")" ] || die "the project $project has containers: restore only into an empty project"

# The manifest and the encrypted archives, before any decryption.
[ -f "$from/manifest.json" ] || die "no manifest.json in $from"
read_manifest() { node -e 'const m=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));const v=process.argv[2].split(".").reduce((o,k)=>o?.[k],m);if(v===undefined)process.exit(3);process.stdout.write(String(v))' "$from/manifest.json" "$1"; }
[ "$(read_manifest format)" = 1 ] || die "unknown manifest format"
[ "$(read_manifest project)" = "$project" ] || die "the backup belongs to another project"
data_file=$from/$(read_manifest data.file)
keys_file=$keys_from/$(read_manifest keys.file)
case "$(read_manifest data.file)$(read_manifest keys.file)" in */*|*..*) die "unsafe file name in the manifest" ;; esac
[ -f "$data_file" ] && [ -f "$keys_file" ] || die "an archive named in the manifest is missing"
[ "$(file_sha "$data_file")" = "$(read_manifest data.encryptedSha256)" ] || die "the data archive does not match the manifest (damaged or replaced)"
[ "$(file_sha "$keys_file")" = "$(read_manifest keys.encryptedSha256)" ] || die "the key archive does not match the manifest (damaged or replaced)"

alpine=$(node -e 'process.stdout.write(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).auxiliary.alpine.ref)' "$root/scripts/completion/images.lock.json")
case "$alpine" in *@sha256:*) ;; *) die "the locked Alpine image has no digest" ;; esac

work=$(mktemp -d "${TMPDIR:-/tmp}/umir-completion-restore.XXXXXX")
created_volume=false
created_dir=false
cleanup() {
  status=$?
  rm -rf "$work"
  if [ "$status" -ne 0 ]; then
    if [ "$created_volume" = true ]; then docker volume rm "$volume" >/dev/null 2>&1 || true; fi
    if [ "$created_dir" = true ]; then rm -rf "$bundle_dir"; fi
  fi
  exit "$status"
}
trap cleanup EXIT

decrypt() { openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -pass "file:$key_file" -in "$1" -out "$2" 2>/dev/null; }
decrypt "$data_file" "$work/auth-state.tar" || die "cannot decrypt the data archive (wrong key?)"
decrypt "$keys_file" "$work/bundle.tar" || die "cannot decrypt the key archive (wrong key?)"
[ "$(file_sha "$work/auth-state.tar")" = "$(read_manifest data.plainSha256)" ] || die "the decrypted data archive does not match the manifest"
[ "$(file_sha "$work/bundle.tar")" = "$(read_manifest keys.plainSha256)" ] || die "the decrypted key archive does not match the manifest"

docker volume create --label "com.docker.compose.project=$project" --label com.docker.compose.volume=auth-state "$volume" >/dev/null
created_volume=true
docker run --rm --network none -v "$volume:/data" -v "$work:/in:ro" "$alpine" tar -xpf /in/auth-state.tar -C /data

mkdir -m 700 "$bundle_dir"
created_dir=true
tar -xpf "$work/bundle.tar" -C "$bundle_dir"
chmod 700 "$bundle_dir"
# compose.env and the integrity list of the bundle name the directory: it must be
# restored where it was (prepare.mjs refuses a bundle that changed).
recorded=$(sed -n 's/^COMPLETION_PRIVATE_DIR=//p' "$bundle_dir/compose.env")
[ "$recorded" = "$bundle_dir" ] || die "the bundle was made in $recorded; restore it to that path"

echo "restore.sh: restored $volume and $bundle_dir (backup of $(read_manifest createdAt))"
