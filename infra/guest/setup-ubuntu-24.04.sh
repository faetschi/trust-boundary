#!/usr/bin/env bash
# Controlled online provisioning for an Ubuntu 24.04 (Noble) guest.
# Run once during the provisioning phase, as root, with the target login user as $1.
set -Eeuo pipefail
umask 077

if (( EUID != 0 )); then
  printf 'Run as root: sudo %s <non-root-login-user>\n' "$0" >&2
  exit 1
fi

TARGET_USER="${1:-${TBOUND_USER:-${SUDO_USER:-}}}"
if [[ -z "$TARGET_USER" ]] || ! id "$TARGET_USER" >/dev/null 2>&1; then
  printf 'Provide an existing non-root target user as the first argument.\n' >&2
  exit 2
fi
TARGET_UID=$(id -u "$TARGET_USER")
if [[ "$TARGET_UID" == 0 ]]; then
  printf 'The Podman trial account must be a non-root user.\n' >&2
  exit 2
fi

. /etc/os-release
if [[ "${ID:-}" != ubuntu || "${VERSION_CODENAME:-}" != noble || "${VERSION_ID:-}" != 24.04 ]]; then
  printf 'Expected Ubuntu 24.04 Noble; found ID=%s VERSION_ID=%s CODENAME=%s\n' "${ID:-?}" "${VERSION_ID:-?}" "${VERSION_CODENAME:-?}" >&2
  exit 2
fi
if [[ "$(uname -m)" != x86_64 ]]; then
  printf 'This pinned Go artifact is Linux amd64; found architecture %s.\n' "$(uname -m)" >&2
  exit 2
fi

NODE_VERSION=24.21.0
GO_VERSION=1.27.1
NODE_ARCHIVE="node-v${NODE_VERSION}-linux-x64.tar.xz"
GO_ARCHIVE="go${GO_VERSION}.linux-amd64.tar.gz"
NODE_DEST="/opt/tbound/toolchains/node-v${NODE_VERSION}-linux-x64"
GO_DEST="/opt/tbound/toolchains/go${GO_VERSION}"
if [[ -e "$NODE_DEST" || -L "$NODE_DEST" || -e "$GO_DEST" || -L "$GO_DEST" ]]; then
  printf 'A pinned toolchain destination already exists; inspect it before provisioning. No changes made.\n' >&2
  exit 2
fi

# APT_CONFIG replaces the normal configuration file chain. Reject it before
# examining sources so apt-config and apt-get cannot read a different setup.
if [[ -v APT_CONFIG ]]; then
  printf 'APT_CONFIG is set; clear the custom APT configuration and retry. No changes made.\n' >&2
  exit 2
fi

# Verify that APT's effective source paths resolve to exactly the files this
# script scans. RootDir can prefix even absolute paths, so only the empty value
# or filesystem root is accepted.
python3 - <<'PY'
import os
import re
import subprocess
import sys
from pathlib import Path

def normalize_key(value):
    return '::'.join(re.sub(r'[-_]', '', part.lower()) for part in value.split('::'))

# APT applies Binary::<program> settings to that executable's active tree.
# Reject every path-related override there, not only source-list leaves.
def is_binary_path_override(value):
    parts = normalize_key(value).split('::')
    return len(parts) >= 3 and parts[0] == 'binary' and any(
        part in {'dir', 'rootdir'} for part in parts[2:]
    )

try:
    dump = subprocess.run(['apt-config', 'dump'], check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.DEVNULL).stdout
except Exception:
    print('APT source paths could not be inspected; refusing to continue.', file=sys.stderr)
    sys.exit(2)

settings = {}
path_keys = {
    normalize_key('RootDir'),
    normalize_key('Dir'),
    normalize_key('Dir::Etc'),
    normalize_key('Dir::Etc::sourcelist'),
    normalize_key('Dir::Etc::sourceparts'),
}
malformed_path_setting = False
for line in dump.splitlines():
    stripped = line.strip()
    if not stripped or stripped.startswith('#'):
        continue
    match = re.fullmatch(r'([^\s;]+)\s+"?([^";]*)"?\s*;', stripped)
    if match:
        key = normalize_key(match.group(1))
        if is_binary_path_override(match.group(1)):
            print('Binary-scoped APT path override found; refusing to continue.', file=sys.stderr)
            sys.exit(2)
        if key in path_keys:
            settings[key] = match.group(2)
    else:
        raw_key = stripped.split(None, 1)[0].rstrip(';')
        key = normalize_key(raw_key)
        if is_binary_path_override(raw_key):
            malformed_path_setting = True
        elif key in path_keys:
            malformed_path_setting = True

if malformed_path_setting:
    print('APT source path configuration is malformed; refusing to continue.', file=sys.stderr)
    sys.exit(2)

def setting(name, default):
    return settings.get(normalize_key(name), default)

root_dir = setting('RootDir', '').strip()
if root_dir not in {'', '/'}:
    print('APT RootDir redirects source paths; refusing to continue.', file=sys.stderr)
    sys.exit(2)

dir_raw = setting('Dir', '/')
if dir_raw != '/':
    print('APT Dir changes the source path base; refusing to continue.', file=sys.stderr)
    sys.exit(2)
dir_value = Path('/')
etc_value = Path(setting('Dir::Etc', 'etc/apt'))
etc_path = etc_value if etc_value.is_absolute() else dir_value / etc_value
source_list_value = Path(setting('Dir::Etc::sourcelist', 'sources.list'))
source_parts_value = Path(setting('Dir::Etc::sourceparts', 'sources.list.d'))
source_list = source_list_value if source_list_value.is_absolute() else etc_path / source_list_value
source_parts = source_parts_value if source_parts_value.is_absolute() else etc_path / source_parts_value
expected_list = Path('/etc/apt/sources.list').resolve(strict=False)
expected_parts = Path('/etc/apt/sources.list.d').resolve(strict=False)
if source_list.resolve(strict=False) != expected_list or source_parts.resolve(strict=False) != expected_parts:
    print('APT effective source-list paths differ from the scanned Ubuntu source paths; refusing to continue.', file=sys.stderr)
    sys.exit(2)
print('APT effective source-list and source-parts paths match the scanned locations.')
PY

# Refuse non-Ubuntu sources and every source-level trust override before
# refreshing indexes. APT remains responsible for validating Ubuntu InRelease
# signatures and package hashes.
python3 - <<'PY'
from pathlib import Path
from urllib.parse import urlsplit
import re
import shlex
import sys

files = [Path('/etc/apt/sources.list')]
source_dir = Path('/etc/apt/sources.list.d')
if source_dir.is_dir():
    files += sorted(source_dir.glob('*.list')) + sorted(source_dir.glob('*.sources'))
found = 0
errors = []
truthy = {'yes', 'true', 'on', '1'}
falsey = {'no', 'false', 'off', '0'}
trust_keys = {
    'trusted',
    'allowinsecure', 'allowinsecurerepositories',
    'allowweak', 'allowweakrepositories',
    'allowdowngrade', 'allowdowngradetoinsecure',
    'allowdowngradetoinsecurerepositories',
    'allowunauthenticated',
}
must_remain_enabled = {'checkdate', 'checkvaliduntil'}

def canon(value):
    return re.sub(r'[-_]', '', value.strip().lower())

def check_trust_setting(path, key, value):
    normalized_key = canon(key)
    if normalized_key not in trust_keys | must_remain_enabled:
        return
    normalized = value.strip().lower()
    if normalized_key in must_remain_enabled and normalized in falsey:
        errors.append(f'{path}: APT metadata freshness validation is disabled')
    elif normalized_key in trust_keys and normalized in truthy:
        errors.append(f'{path}: source trust override is enabled')
    elif normalized not in truthy | falsey:
        errors.append(f'{path}: source trust setting is malformed or unverifiable')

def check_uri(path, uri, suites, signed_by):
    global found
    found += 1
    try:
        parsed = urlsplit(uri)
        host = (parsed.hostname or '').lower()
        has_userinfo = parsed.username is not None or parsed.password is not None or '@' in parsed.netloc
    except ValueError:
        errors.append(f'{path}: malformed source URI')
        return
    if has_userinfo:
        errors.append(f'{path}: source URI user information is forbidden')
    if parsed.query or parsed.fragment:
        errors.append(f'{path}: source URI query and fragment components are forbidden')
    if parsed.scheme.lower() not in {'http', 'https'} or not host:
        errors.append(f'{path}: source URI must use HTTP or HTTPS with a host')
    elif host != 'ubuntu.com' and not host.endswith('.ubuntu.com'):
        errors.append(f'{path}: non-Ubuntu repository host')
    if parsed.path.rstrip('/') not in {'', '/ubuntu', '/ubuntu-ports'}:
        errors.append(f'{path}: unexpected Ubuntu archive path')
    if not suites or any(suite not in {'noble', 'noble-updates', 'noble-security', 'noble-backports'} for suite in suites):
        errors.append(f'{path}: expected a supported Noble archive suite')
    if signed_by and signed_by != '/usr/share/keyrings/ubuntu-archive-keyring.gpg':
        errors.append(f'{path}: unexpected Signed-By keyring')

for path in files:
    if not path.is_file():
        continue
    text = path.read_text(errors='replace')
    if path.suffix == '.sources':
        for paragraph in re.split(r'\r?\n[ \t]*\r?\n', text):
            fields = {}
            current_key = None
            for line in paragraph.splitlines():
                if line[:1].isspace() and current_key:
                    fields[current_key][-1] += ' ' + line.strip()
                elif line and ':' in line:
                    key, value = line.split(':', 1)
                    current_key = key.lower()
                    fields.setdefault(current_key, []).append(value.strip())
            for name in fields:
                if canon(name) in trust_keys | must_remain_enabled:
                    if len(fields[name]) != 1:
                        errors.append(f'{path}: duplicate source trust setting is unverifiable')
                    for value in fields[name]:
                        check_trust_setting(path, name, value)
            enabled_values = fields.get('enabled', ['yes'])
            if len(enabled_values) != 1 or enabled_values[0].strip().lower() not in truthy | falsey:
                errors.append(f'{path}: malformed Enabled field')
                continue
            if enabled_values[0].strip().lower() in falsey:
                continue
            uris = ' '.join(fields.get('uris', [])).split()
            suites = ' '.join(fields.get('suites', [])).split()
            signed_values = fields.get('signed-by', [''])
            if len(signed_values) != 1:
                errors.append(f'{path}: duplicate Signed-By field')
                continue
            signed_by = signed_values[0]
            if not uris:
                continue
            for uri in uris:
                check_uri(path, uri, suites, signed_by)
    else:
        for raw in text.splitlines():
            try:
                tokens = shlex.split(raw, comments=True, posix=True)
            except ValueError:
                errors.append(f'{path}: malformed active source line')
                continue
            if not tokens or tokens[0] not in ('deb', 'deb-src'):
                continue
            i = 1
            signed_by = ''
            source_options = []
            if i < len(tokens) and tokens[i].startswith('['):
                option_tokens = []
                while i < len(tokens) and not tokens[i].endswith(']'):
                    option_tokens.append(tokens[i].lstrip('['))
                    i += 1
                if i < len(tokens):
                    option_tokens.append(tokens[i].rstrip(']'))
                for option in option_tokens:
                    if '=' not in option:
                        errors.append(f'{path}: malformed source option')
                        continue
                    key, value = option.split('=', 1)
                    source_options.append((key, value))
                    if canon(key) == 'signedby':
                        signed_by = option.split('=', 1)[1]
                i += 1
            for key, value in source_options:
                check_trust_setting(path, key, value)
            if i + 1 >= len(tokens):
                errors.append(f'{path}: could not parse active source line')
                continue
            check_uri(path, tokens[i], [tokens[i + 1]], signed_by)
if found == 0:
    errors.append('No active Ubuntu APT sources found')
if errors:
    print('\n'.join('APT source check: ' + item for item in errors), file=sys.stderr)
    sys.exit(1)
print(f'Validated {found} active Ubuntu APT source entr(ies); no trust bypass or credential-bearing URI found.')
PY

# Inspect effective APT settings without printing the configuration values.
# Recognize the boolean spellings used by APT and fail closed on unknown values.
python3 - <<'PY'
import re
import subprocess
import sys

keys = {
    'allowinsecure', 'allowinsecurerepositories',
    'allowweak', 'allowweakrepositories',
    'allowdowngrade', 'allowdowngradetoinsecure',
    'allowdowngradetoinsecurerepositories',
    'allowunauthenticated', 'trusted', 'checkdate', 'checkvaliduntil',
}
must_remain_enabled = {'checkdate', 'checkvaliduntil'}
true_values = {'yes', 'true', 'on', '1'}
false_values = {'no', 'false', 'off', '0'}
failed = False

def relevant_parts(name):
    lowered = name.lower()
    split_parts = re.split(r'::|[._-]', lowered)
    scoped_parts = [re.sub(r'[._-]', '', part) for part in lowered.split('::')]
    return split_parts + scoped_parts

try:
    dump = subprocess.run(['apt-config', 'dump'], check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.DEVNULL).stdout
except Exception:
    print('APT effective configuration could not be inspected; refusing to continue.', file=sys.stderr)
    sys.exit(2)

for line in dump.splitlines():
    stripped = line.strip()
    if not stripped or stripped.startswith('#'):
        continue
    match = re.fullmatch(r'([^\s;]+)\s+"?([^";]*)"?\s*;', stripped)
    raw_key = stripped.split(None, 1)[0]
    if not any(re.sub(r'[-_]', '', part) in keys for part in relevant_parts(raw_key)):
        continue
    if not match:
        failed = True
        continue
    key_parts = relevant_parts(match.group(1))
    if not any(re.sub(r'[-_]', '', part) in keys for part in key_parts):
        continue
    raw_value = match.group(2).strip()
    value = raw_value.lower()
    canonical_key = '::'.join(re.sub(r'[._-]', '', part)
                              for part in match.group(1).lower().split('::'))
    if canonical_key == 'dir::etc::trusted':
        # APT's stock trusted keyring filename is a path, not a boolean.
        if raw_value != 'trusted.gpg':
            failed = True
        continue
    key_name = next((re.sub(r'[-_]', '', part) for part in key_parts
                     if re.sub(r'[-_]', '', part) in keys), '')
    if key_name in must_remain_enabled:
        if value in false_values or value not in true_values:
            failed = True
    elif value in true_values or value not in false_values:
        failed = True
if failed:
    print('APT insecure, weak, downgrade, trusted, or unauthenticated setting is enabled or unverifiable; refusing to continue.', file=sys.stderr)
    sys.exit(2)
print('APT effective trust configuration passed.')
PY
if [[ ! -r /usr/share/keyrings/ubuntu-archive-keyring.gpg ]]; then
  printf 'Ubuntu archive keyring is missing.\n' >&2
  exit 2
fi

install -d -m 0755 /var/log
LOG_FILE=/var/log/tbound-guest-provision.log
if [[ -L "$LOG_FILE" || ( -e "$LOG_FILE" && ! -f "$LOG_FILE" ) ]]; then
  printf 'Provisioning log path is not a regular file; refusing to write it.\n' >&2
  exit 2
fi
if [[ ! -e "$LOG_FILE" ]]; then
  (set -o noclobber; : > "$LOG_FILE") || {
    printf 'Could not create the provisioning log securely.\n' >&2
    exit 2
  }
fi
chmod 0600 "$LOG_FILE"
exec > >(tee -a "$LOG_FILE") 2>&1
trap 'rc=$?; printf "Provisioning stopped with exit %s at %s\n" "$rc" "$(date -Is)"; exit "$rc"' ERR

printf '\n=== tbound guest provisioning start: %s ===\n' "$(date -Is)"
printf 'Target account: %s (uid=%s)\n' "$TARGET_USER" "$TARGET_UID"
printf 'OS: %s\nKernel: %s\n' "$(grep '^PRETTY_NAME=' /etc/os-release | cut -d= -f2- | tr -d '"')" "$(uname -r)"
printf 'Architecture: %s\n' "$(uname -m)"

# Suppress raw APT diagnostics: they can contain configured source URLs or
# proxy credentials. Version and repository origin data is recorded below.
run_apt() {
  local action=$1
  shift
  if "$@" >/dev/null 2>&1; then
    printf 'APT %s succeeded.\n' "$action"
  else
    local rc=$?
    printf 'APT %s failed with exit %s; raw diagnostics were suppressed to protect configured credentials.\n' "$action" "$rc" >&2
    return "$rc"
  fi
}
run_apt update apt-get update
APT_PACKAGES=(podman crun uidmap slirp4netns fuse-overlayfs libseccomp2 ca-certificates curl xz-utils gnupg python3 gcc gcc-13 gcc-13-x86-64-linux-gnu)
run_apt install apt-get install -y --no-install-recommends "${APT_PACKAGES[@]}"
run_apt hold apt-mark hold podman crun uidmap slirp4netns fuse-overlayfs libseccomp2 gcc gcc-13 gcc-13-x86-64-linux-gnu

# Rootless Podman needs subordinate UID and GID ranges. Reuse a sufficiently
# large existing range for the target account; otherwise allocate a fresh range
# beyond all configured ranges in both files.
has_subid_range() {
  local file=$1
  [[ -r "$file" ]] && awk -F: -v name="$TARGET_USER" -v uid="$TARGET_UID" '($1 == name || $1 == uid) && $3 ~ /^[0-9]+$/ && $3 >= 65536 { ok=1 } END { exit !ok }' "$file"
}
first_free_subid() {
  local files=()
  [[ -r /etc/subuid ]] && files+=(/etc/subuid)
  [[ -r /etc/subgid ]] && files+=(/etc/subgid)
  if ((${#files[@]} == 0)); then
    printf '100000\n'
  else
    awk -F: 'NF >= 3 && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { end = $2 + $3; if (end > max) max = end } END { start = max + 1; if (start < 100000) start = 100000; print start }' "${files[@]}"
  fi
}
if ! has_subid_range /etc/subuid || ! has_subid_range /etc/subgid; then
  SUBID_START=$(first_free_subid)
  SUBID_END=$((SUBID_START + 65535))
  if (( SUBID_END > 4294967295 )); then
    printf 'No safe 65536-ID subordinate range remains. Configure /etc/subuid and /etc/subgid manually.\n' >&2
    exit 2
  fi
  if ! has_subid_range /etc/subuid; then
    usermod --add-subuids "${SUBID_START}-${SUBID_END}" "$TARGET_USER"
    printf 'Added subuid range %s-%s for %s\n' "$SUBID_START" "$SUBID_END" "$TARGET_USER"
  fi
  if ! has_subid_range /etc/subgid; then
    usermod --add-subgids "${SUBID_START}-${SUBID_END}" "$TARGET_USER"
    printf 'Added subgid range %s-%s for %s\n' "$SUBID_START" "$SUBID_END" "$TARGET_USER"
  fi
fi

WORKDIR=$(mktemp -d /var/tmp/tbound-provision.XXXXXXXX)
cleanup() {
  local work_real
  work_real=$(readlink -f -- "$WORKDIR" 2>/dev/null || true)
  if [[ -n "$work_real" && "$work_real" == /var/tmp/tbound-provision.* ]]; then
    rm -rf -- "$work_real"
  fi
}
trap cleanup EXIT
mkdir -m 0700 "$WORKDIR/gnupg"
export GNUPGHOME="$WORKDIR/gnupg"

NODE_BASE="https://nodejs.org/download/release/v${NODE_VERSION}"
curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$WORKDIR/SHASUMS256.txt" "$NODE_BASE/SHASUMS256.txt"
curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$WORKDIR/SHASUMS256.txt.sig" "$NODE_BASE/SHASUMS256.txt.sig"

# Trust only release-key primary fingerprints published by nodejs/release-keys.
# The imported keys are isolated in this run's temporary GnuPG home.
NODE_KEY_FPS=(
  5BE8A3F6C8A5C01D106C0AD820B1A390B168D356
  DD792F5973C6DE52C432CBDAC77ABFA00DDBF2B7
  CC68F5A3106FF448322E48ED27F5E38D5B0A215F
  890C08DB8579162FEE0DF9DB8BEAB4DFCF555EF4
  C82FA3AE1CBEDC6BE46B9360C43CEC45C17AB93C
  108F52B48DB57BB0CC439B2997B01419BD92F80A
  655F3B5C1FB3FA8D1A0CA6BDE4A7D232B936D2FD
  A363A499291CBBC940DD62E41F10027AF002F8B0
)
for fingerprint in "${NODE_KEY_FPS[@]}"; do
  keyfile="$WORKDIR/${fingerprint}.asc"
  curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$keyfile" "https://github.com/nodejs/release-keys/raw/refs/heads/main/keys/${fingerprint}.asc"
  actual=$(gpg --batch --show-keys --with-colons "$keyfile" | awk -F: '$1 == "fpr" && !seen { print toupper($10); seen=1 }')
  if [[ "$actual" != "$fingerprint" ]]; then
    printf 'Node release key fingerprint mismatch: expected %s, got %s\n' "$fingerprint" "${actual:-none}" >&2
    exit 2
  fi
  gpg --batch --import "$keyfile"
done

gpg --batch --status-fd 1 --verify "$WORKDIR/SHASUMS256.txt.sig" "$WORKDIR/SHASUMS256.txt" > "$WORKDIR/node-signature-status.txt"
grep -q '^\[GNUPG:\] VALIDSIG ' "$WORKDIR/node-signature-status.txt"
grep '^\[GNUPG:\] VALIDSIG ' "$WORKDIR/node-signature-status.txt"
NODE_SHA=$(awk -v file="$NODE_ARCHIVE" '$2 == file { print $1 }' "$WORKDIR/SHASUMS256.txt")
if [[ ! "$NODE_SHA" =~ ^[[:xdigit:]]{64}$ ]]; then
  printf 'No valid SHA-256 entry for %s in the signed Node manifest.\n' "$NODE_ARCHIVE" >&2
  exit 2
fi
curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$WORKDIR/$NODE_ARCHIVE" "$NODE_BASE/$NODE_ARCHIVE"
printf 'Node.js %s archive SHA-256: %s\n' "$NODE_VERSION" "$NODE_SHA"
printf '%s  %s\n' "$NODE_SHA" "$WORKDIR/$NODE_ARCHIVE" | sha256sum --check -

GO_BASE="https://go.dev/dl"
GO_SHA=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
curl --fail --location --proto '=https' --tlsv1.2 --retry 2 -o "$WORKDIR/$GO_ARCHIVE" "$GO_BASE/$GO_ARCHIVE"
printf 'Go %s archive SHA-256: %s\n' "$GO_VERSION" "$GO_SHA"
printf '%s  %s\n' "$GO_SHA" "$WORKDIR/$GO_ARCHIVE" | sha256sum --check -

install -d -m 0755 /opt/tbound/toolchains
tar -xJf "$WORKDIR/$NODE_ARCHIVE" -C "$WORKDIR"
tar -xzf "$WORKDIR/$GO_ARCHIVE" -C "$WORKDIR"
install -d -m 0755 "$NODE_DEST" "$GO_DEST"
cp -a "$WORKDIR/node-v${NODE_VERSION}-linux-x64/." "$NODE_DEST/"
cp -a "$WORKDIR/go/." "$GO_DEST/"

printf '\n=== Installed package versions ===\n'
dpkg-query -W -f='${binary:Package}\t${Version}\t${db:Status-Abbrev}\n' podman crun uidmap slirp4netns fuse-overlayfs libseccomp2 ca-certificates curl xz-utils gnupg python3 gcc gcc-13 gcc-13-x86-64-linux-gnu
printf '\n=== Actual GCC driver package and digest ===\n'
GCC_DRIVER=$(readlink -f /usr/bin/gcc)
dpkg-query -S "$GCC_DRIVER"
dpkg-query -W -f='${binary:Package}\t${Version}\t${db:Status-Abbrev}\n' gcc-13-x86-64-linux-gnu
sha256sum "$GCC_DRIVER"
printf '\n=== Held packages ===\n'
apt-mark showhold
printf '\n=== Full installed package manifest (package and version) ===\n'
dpkg-query -W -f='${db:Status-Abbrev}\t${binary:Package}\t${Version}\n' | awk -F '\t' '$1 ~ /^ii/ { print $2 "\t" $3 }' | sort
printf '\n=== Runtime versions ===\n'
"$NODE_DEST/bin/node" --version
GOTOOLCHAIN=local "$GO_DEST/bin/go" version
podman version
crun --version
gcc --version | head -n 1
gcc-13 --version | head -n 1
printf '\nSubordinate ID mappings for %s:\n' "$TARGET_USER"
getent subuid "$TARGET_USER" || true
getent subgid "$TARGET_USER" || true
printf '\nInstalled artifacts: %s and %s\n' "$NODE_DEST" "$GO_DEST"
printf 'Provisioning log: %s\n' "$LOG_FILE"
printf 'Provisioning complete: %s\n' "$(date -Is)"
printf '\nLog out of %s (or reboot) before the rootless probe so new subordinate IDs take effect.\n' "$TARGET_USER"
printf 'Disconnect the VM virtual NIC at the hypervisor before beginning offline trials.\n'
