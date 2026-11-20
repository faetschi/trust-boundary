# shellcheck shell=bash
# Release-archive verification shared by build-deb.sh, build-rpm.sh and make-npm-packages.sh.
# Sourced, never run. The caller owns `set -euo pipefail` and defines die().

# verify_archive_checksum <archive> <SHA256SUMS>: the archive's SHA256SUMS entry matches its bytes.
verify_archive_checksum() {
  local archive="$1" sums="$2" base want got
  base="$(basename "$archive")"
  [ -s "$sums" ] || die "missing or empty $sums; refusing unverified tarball"
  want="$(awk -v a="$base" '$2==a || $2=="./"a {print $1; exit}' "$sums")"
  [ -n "$want" ] || die "SHA256SUMS has no entry for $base"
  if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$archive" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then got="$(shasum -a 256 "$archive" | awk '{print $1}')"
  else die "sha256sum or shasum is required to verify $base"; fi
  [ "$want" = "$got" ] || die "tarball checksum mismatch for $base"
}

# verify_archive_members <archive>: no unsafe member paths, no links or special files. Run before extracting.
verify_archive_members() {
  local archive="$1" member
  while IFS= read -r member; do
    case "$member" in
      ""|/*|../*|*/../*|*/..|*\\*) die "unsafe path in archive $(basename "$archive"): $member" ;;
    esac
  done < <(tar -tzf "$archive")
  tar -tvzf "$archive" | awk 'substr($0,1,1)!="-" && substr($0,1,1)!="d" { bad=1 } END { exit bad ? 1 : 0 }' \
    || die "links or special files are not allowed in $(basename "$archive")"
}

# verify_manifest_tree <dir>: MANIFEST.sha256 of the extracted archive lists exactly its regular files, and each digest holds.
verify_manifest_tree() {
  local root="$1" line member file seen="|"
  [ -f "$root/MANIFEST.sha256" ] || die "archive is missing MANIFEST.sha256"
  while IFS= read -r line; do
    [[ "$line" =~ ^[0-9a-f]{64}\ \ (.+)$ ]] || die "malformed MANIFEST.sha256 record"
    member="${BASH_REMATCH[1]}"
    case "$member" in ./*) member="${member#./}" ;; esac
    [[ "$member" =~ ^[A-Za-z0-9._/-]+$ ]] || die "invalid manifest path '$member'"
    case "$member" in ""|/*|.|..|../*|*/../*|*/..) die "unsafe manifest path '$member'" ;; esac
    case "$seen" in *"|$member|"*) die "duplicate manifest path '$member'" ;; esac
    seen+="$member|"
    [ -f "$root/$member" ] && [ ! -L "$root/$member" ] || die "manifest path is missing, non-regular, or a symlink: $member"
  done < "$root/MANIFEST.sha256"
  while IFS= read -r -d '' file; do
    member="${file#"$root"/}"
    case "$seen" in *"|$member|"*) ;; *) die "unlisted regular file in archive: $member" ;; esac
  done < <(find "$root" -type f ! -name MANIFEST.sha256 -print0)
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$root" && sha256sum --quiet -c MANIFEST.sha256 ) || die "manifest verification failed"
  elif command -v shasum >/dev/null 2>&1; then
    ( cd "$root" && shasum -a 256 -c MANIFEST.sha256 >/dev/null ) || die "manifest verification failed"
  else
    die "sha256sum or shasum is required to verify MANIFEST.sha256"
  fi
}
