#!/usr/bin/env bash
# Regression test for held-package detection in the setup scripts.
#
# The guarded provisioner holds runtime and build packages, so dpkg reports
# "hi" (held, installed) rather than "ii". A second bootstrap run must treat
# held packages as installed and report only packages that are truly absent.
# Runs the bootstrap's real package loop and the installed-package manifest
# command from setup-ubuntu-24.04.sh against a dpkg-query stub.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$ROOT/bootstrap-linux.sh"
SETUP="$ROOT/../guest/setup-ubuntu-24.04.sh"
[[ -r "$BOOTSTRAP" && -r "$SETUP" ]] || { echo "setup scripts not readable" >&2; exit 2; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tbound-pkg-status.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
# Only the stubs below are on PATH while the extracted code runs, so `command -v` is deterministic.
for tool in grep awk sort; do ln -s "$(command -v "$tool")" "$WORK/bin/$tool"; done
for cmd in podman crun newuidmap slirp4netns fuse-overlayfs curl python3 make; do
  printf '#!/bin/bash\nexit 0\n' > "$WORK/bin/$cmd"
  chmod +x "$WORK/bin/$cmd"
done
cat > "$WORK/bin/dpkg-query" <<'SH'
#!/bin/bash
# Stub over $DPKG_STATUS_FILE ("<package> <status>" per line). With package names it
# prints the named status and exits 1 when unknown. With no names (the manifest
# form) it prints "<status><TAB><package><TAB><version>" for every row.
names=()
for arg in "$@"; do
  [[ "$arg" == -* ]] || names+=("$arg")
done
if ((${#names[@]} == 0)); then
  while read -r name status; do
    printf '%s\t%s\t1.0\n' "$status" "$name"
  done < "$DPKG_STATUS_FILE"
  exit 0
fi
while read -r name status; do
  if [[ "$name" == "${names[${#names[@]}-1]}" ]]; then
    printf '%s' "$status"
    exit 0
  fi
done < "$DPKG_STATUS_FILE"
exit 1
SH
chmod +x "$WORK/bin/dpkg-query"

# Extract the code under test from the real scripts (not copies of it).
LOOP_TEXT=$(sed -n '/^  MISSING_PKGS=()$/,/^  done$/p' "$BOOTSTRAP")
PACKAGE_INSTALLED_TEXT=$(sed -n '/^package_installed()/,/^}/p' "$BOOTSTRAP")
MANIFEST_CMD=$(grep -A1 'Full installed package manifest' "$SETUP" | tail -n 1)
[[ -n "$LOOP_TEXT" ]] || { echo "could not find the package loop in $BOOTSTRAP" >&2; exit 2; }
[[ "$MANIFEST_CMD" == *dpkg-query* && "$MANIFEST_CMD" == *awk* ]] || { echo "could not find the manifest command in $SETUP" >&2; exit 2; }

# Runs the bootstrap's package loop over the given dpkg status rows and prints MISSING_PKGS.
missing_after_loop() {
  printf '%s\n' "$1" > "$WORK/dpkg-status"
  (
    export DPKG_STATUS_FILE="$WORK/dpkg-status"
    PATH="$WORK/bin"
    eval "$PACKAGE_INSTALLED_TEXT"
    eval "$LOOP_TEXT"
    printf '%s\n' "${MISSING_PKGS[*]:-}"
  )
}

# rows_with <package> <status>: every dpkg-checked package installed, except the named one.
# Status "absent" omits that package's row.
rows_with() {
  local pkg
  for pkg in libseccomp2 ca-certificates xz-utils gnupg; do
    if [[ "$pkg" == "$1" ]]; then
      [[ "$2" == absent ]] || printf '%s %s\n' "$pkg" "$2"
    else
      printf '%s ii\n' "$pkg"
    fi
  done
}

pass=0
fail=0
expect_missing() {  # expect_missing <label> <status rows> <want: space-separated names or none>
  local label=$1 rows=$2 want=$3 got
  got=$(missing_after_loop "$rows")
  [[ -n "$got" ]] || got=none
  if [[ "$got" == "$want" ]]; then
    printf 'PASS  %s: missing=%s\n' "$label" "$got"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s: missing=%s (want %s)\n' "$label" "$got" "$want" >&2
    fail=$((fail + 1))
  fi
}

expect_missing "all packages installed" "$(rows_with '' '')" none
expect_missing "held package (hi) counts as installed" "$(rows_with libseccomp2 hi)" none
expect_missing "unpacked package (un) is missing" "$(rows_with libseccomp2 un)" libseccomp2
expect_missing "config-files package (rc) is missing" "$(rows_with gnupg rc)" gnupg
expect_missing "absent package is missing" "$(rows_with xz-utils absent)" xz-utils

# The installed-package manifest must list held packages too, not only "ii" rows.
printf 'podman ii\ngcc hi\nold rc\n' > "$WORK/dpkg-status"
manifest=$( export DPKG_STATUS_FILE="$WORK/dpkg-status"; PATH="$WORK/bin"; eval "$MANIFEST_CMD" )
if [[ "$manifest" == *$'podman\t'* && "$manifest" == *$'gcc\t'* && "$manifest" != *old* ]]; then
  printf 'PASS  manifest lists ii and hi rows, not rc rows\n'
  pass=$((pass + 1))
else
  printf 'FAIL  manifest lists ii and hi rows, not rc rows: got [%s] (want podman and gcc, no old)\n' "$manifest" >&2
  fail=$((fail + 1))
fi

# make is a command check in the bootstrap loop; with no make on PATH it must be reported.
rm -f "$WORK/bin/make"
expect_missing "make command absent is reported" "$(rows_with '' '')" make
printf '#!/bin/bash\nexit 0\n' > "$WORK/bin/make"
chmod +x "$WORK/bin/make"

# The provisioner must install make and hold it with the other build packages.
declare -a APT_PACKAGES=()
eval "$(grep '^APT_PACKAGES=' "$SETUP")"
hold_list=$(grep '^run_apt hold ' "$SETUP")
hold_words=${hold_list#run_apt hold apt-mark hold }
if [[ " ${APT_PACKAGES[*]} " == *" make "* ]]; then
  printf 'PASS  make is in the provisioner apt install list\n'
  pass=$((pass + 1))
else
  printf 'FAIL  make is in the provisioner apt install list: [%s]\n' "${APT_PACKAGES[*]}" >&2
  fail=$((fail + 1))
fi
if [[ " $hold_words " == *" make "* ]]; then
  printf 'PASS  make is in the provisioner apt-mark hold list\n'
  pass=$((pass + 1))
else
  printf 'FAIL  make is in the provisioner apt-mark hold list: [%s]\n' "$hold_words" >&2
  fail=$((fail + 1))
fi

printf '\npackage-status checks: %d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
