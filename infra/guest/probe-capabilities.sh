#!/usr/bin/env bash
# Read-only capability probe. Harmless syscalls run in child processes; no
# container is started, no image is pulled, and cgroup.kill is never written.
set -uo pipefail

if ! command -v python3 >/dev/null 2>&1; then
  printf 'FAIL  probe-runtime: python3 is required\n' >&2
  exit 1
fi
exec python3 - <<'PY'
import ctypes
import errno
import json
import os
import platform
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

results = []

def report(status, name, detail):
    results.append(status)
    print(f'{status:12} {name}: {detail}')

def run_child(code):
    try:
        return subprocess.run(
            [sys.executable, '-c', code], text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=10, check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return exc

print('Read-only guest capability probe')
print(f'Observed: kernel={platform.release()} arch={platform.machine()} uid={os.getuid()} user={pwd.getpwuid(os.getuid()).pw_name}')

# cgroup v2 mount, current cgroup domain type, and cgroup.kill interface.
def unescape_mount(value):
    return re.sub(r'\\([0-7]{3})', lambda match: chr(int(match.group(1), 8)), value)

mountpoint = None
mountroot = None
try:
    for line in Path('/proc/self/mountinfo').read_text().splitlines():
        left, sep, right = line.partition(' - ')
        if sep and right.split()[0] == 'cgroup2':
            fields = left.split()
            if len(fields) >= 5:
                mountroot = unescape_mount(fields[3])
                mountpoint = Path(unescape_mount(fields[4]))
                break
except OSError:
    pass
if mountpoint is None:
    report('FAIL', 'cgroup-v2', 'no cgroup2 mount is visible')
else:
    report('PASS', 'cgroup-v2', f'mounted at {mountpoint}')
    membership = None
    try:
        for line in Path('/proc/self/cgroup').read_text().splitlines():
            if line.startswith('0::'):
                membership = line[3:]
                break
    except OSError:
        pass
    relative = None
    if membership is not None:
        if mountroot == '/' or membership == mountroot:
            relative = membership.lstrip('/') if mountroot == '/' else ''
        elif membership.startswith(mountroot.rstrip('/') + '/'):
            relative = membership[len(mountroot):].lstrip('/')
        elif membership == '/':
            relative = ''
    current = (mountpoint / (relative or '')).resolve(strict=False) if relative is not None else None
    if current is None or not current.exists():
        report('UNVERIFIED', 'cgroup-domain', 'could not resolve this process cgroup from /proc')
        report('UNVERIFIED', 'cgroup.kill-interface', 'no current cgroup path to inspect')
        report('UNVERIFIED', 'cgroup.kill-write', 'read-only probe never writes this destructive control')
    else:
        type_file = current / 'cgroup.type'
        kill_file = current / 'cgroup.kill'
        if type_file.is_file():
            try:
                kind = type_file.read_text().strip()
            except OSError as exc:
                report('UNVERIFIED', 'cgroup-domain', f'cannot read {type_file}: {exc}')
            else:
                if kind == 'domain':
                    report('PASS', 'cgroup-domain', f'{type_file} reports {kind}')
                else:
                    report('FAIL', 'cgroup-domain', f'{type_file} reports {kind}; expected domain')
        else:
            report('UNVERIFIED', 'cgroup-domain', f'{type_file} is not visible (the hierarchy root has no cgroup.type)')
        try:
            mode = kill_file.stat().st_mode
        except FileNotFoundError:
            if current == mountpoint:
                report('UNVERIFIED', 'cgroup.kill-interface', 'current cgroup is the hierarchy root; cgroup.kill exists only on non-root cgroups')
            else:
                report('FAIL', 'cgroup.kill-interface', f'{kill_file} is absent on the current non-root cgroup')
        except OSError as exc:
            report('UNVERIFIED', 'cgroup.kill-interface', f'cannot inspect {kill_file}: {exc}')
        else:
            if stat.S_ISREG(mode):
                report('PASS', 'cgroup.kill-interface', f'write-only kernel control is present at {kill_file}')
            else:
                report('UNVERIFIED', 'cgroup.kill-interface', f'{kill_file} exists but is not a regular control file')
        writable = os.access(kill_file, os.W_OK) if kill_file.exists() else False
        report('UNVERIFIED', 'cgroup.kill-write', f'write permission appears {"available" if writable else "unavailable"}; behavior is untested because writing kills processes')

# pidfd_open on this short-lived probe process exercises the syscall, then closes it.
try:
    if not hasattr(os, 'pidfd_open'):
        report('UNVERIFIED', 'pidfd', 'Python lacks os.pidfd_open; use Python 3.9+ for this syscall probe')
    else:
        fd = os.pidfd_open(os.getpid(), 0)
        os.close(fd)
        report('PASS', 'pidfd', 'pidfd_open(current probe process) succeeded and the descriptor was closed')
except OSError as exc:
    if exc.errno == errno.ENOSYS:
        report('FAIL', 'pidfd', f'kernel returned ENOSYS: {exc}')
    elif exc.errno in (errno.EPERM, errno.EACCES):
        report('UNVERIFIED', 'pidfd', f'policy denied the probe syscall: {exc}')
    else:
        report('FAIL', 'pidfd', f'pidfd_open failed: {exc}')

# Landlock ABI query only asks for the kernel ABI number; no ruleset is created.
landlock_syscall = {'x86_64': 444, 'amd64': 444, 'aarch64': 444, 'arm64': 444, 'riscv64': 444}.get(platform.machine().lower())
if landlock_syscall is None:
    report('UNVERIFIED', 'landlock-abi', f'no syscall number mapping in probe for {platform.machine()}')
else:
    libc = ctypes.CDLL(None, use_errno=True)
    libc.syscall.restype = ctypes.c_long
    value = libc.syscall(landlock_syscall, None, ctypes.c_size_t(0), ctypes.c_uint(1))
    if value > 0:
        report('PASS', 'landlock-abi', f'landlock_create_ruleset VERSION query returned ABI {value}')
    else:
        error = ctypes.get_errno()
        if error in (errno.ENOSYS, errno.EOPNOTSUPP):
            report('FAIL', 'landlock-abi', f'kernel reports Landlock unavailable: {os.strerror(error)}')
        elif error in (errno.EPERM, errno.EACCES):
            report('UNVERIFIED', 'landlock-abi', f'policy denied the ABI query: {os.strerror(error)}')
        else:
            report('FAIL', 'landlock-abi', f'ABI query failed: {os.strerror(error)}')

# Install an allow-all seccomp filter in a disposable child process, then check
# that the kernel marks that child Seccomp: 2. The calling shell is untouched.
seccomp_code = r'''
import ctypes, os, sys
class SockFilter(ctypes.Structure):
    _fields_ = [('code', ctypes.c_ushort), ('jt', ctypes.c_ubyte), ('jf', ctypes.c_ubyte), ('k', ctypes.c_uint32)]
class SockFprog(ctypes.Structure):
    _fields_ = [('len', ctypes.c_ushort), ('filter', ctypes.POINTER(SockFilter))]
libc = ctypes.CDLL(None, use_errno=True)
libc.prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_void_p, ctypes.c_ulong, ctypes.c_ulong]
libc.prctl.restype = ctypes.c_int
if libc.prctl(38, 1, None, 0, 0) != 0:  # PR_SET_NO_NEW_PRIVS
    e = ctypes.get_errno(); print(f'no_new_privs errno={e} {os.strerror(e)}'); sys.exit(20)
filters = (SockFilter * 1)(SockFilter(0x06, 0, 0, 0x7fff0000))  # BPF_RET | SECCOMP_RET_ALLOW
program = SockFprog(1, filters)
if libc.prctl(22, 2, ctypes.cast(ctypes.pointer(program), ctypes.c_void_p), 0, 0) != 0:  # PR_SET_SECCOMP, FILTER
    e = ctypes.get_errno(); print(f'seccomp errno={e} {os.strerror(e)}'); sys.exit(21)
status = open('/proc/self/status', encoding='ascii').read()
mode = next((line.split(':', 1)[1].strip() for line in status.splitlines() if line.startswith('Seccomp:')), '')
print(f'Seccomp:{mode}')
sys.exit(0 if mode == '2' else 22)
'''
seccomp_result = run_child(seccomp_code)
if isinstance(seccomp_result, Exception):
    report('UNVERIFIED', 'seccomp-filter', f'could not run isolated syscall probe: {seccomp_result}')
elif seccomp_result.returncode == 0 and 'Seccomp:2' in seccomp_result.stdout.replace(' ', ''):
    report('PASS', 'seccomp-filter', 'disposable child installed a filter and reported Seccomp: 2')
else:
    detail = (seccomp_result.stdout + seccomp_result.stderr).strip().replace('\n', '; ')
    if 'errno=1 ' in detail or 'errno=13 ' in detail:
        report('UNVERIFIED', 'seccomp-filter', f'outer policy blocked the isolated probe: {detail}')
    else:
        report('FAIL', 'seccomp-filter', f'isolated filter probe failed (exit {seccomp_result.returncode}): {detail or "no detail"}')

# Check subordinate IDs and actually create a user namespace as this non-root user.
account = pwd.getpwuid(os.getuid())
for filename, key in (('/etc/subuid', 'subuid'), ('/etc/subgid', 'subgid')):
    ranges = []
    try:
        for line in Path(filename).read_text().splitlines():
            fields = line.split(':')
            if len(fields) == 3 and fields[0] in (account.pw_name, str(os.getuid())):
                try:
                    ranges.append((int(fields[1]), int(fields[2])))
                except ValueError:
                    pass
    except OSError:
        pass
    sufficient = any(count >= 65536 for _, count in ranges)
    report('PASS' if sufficient else 'FAIL', key, f'{"a range of at least 65536 IDs is configured" if sufficient else "no range of at least 65536 IDs is configured"} for {account.pw_name}')

unshare = shutil.which('unshare')
if os.getuid() == 0:
    report('FAIL', 'user-namespace', 'probe must run as the non-root trial account')
elif not unshare:
    report('FAIL', 'user-namespace', 'unshare utility is missing')
else:
    try:
        result = subprocess.run([unshare, '--user', '--map-root-user', '--fork', '/usr/bin/id', '-u'], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, check=False)
        if result.returncode == 0 and result.stdout.strip() == '0':
            report('PASS', 'user-namespace', 'unshare created a user namespace and mapped this user to namespace uid 0')
        else:
            report('FAIL', 'user-namespace', f'unshare failed (exit {result.returncode}): {(result.stderr or result.stdout).strip()}')
    except (OSError, subprocess.TimeoutExpired) as exc:
        report('FAIL', 'user-namespace', f'unshare probe failed: {exc}')

helpers = [shutil.which('newuidmap'), shutil.which('newgidmap')]
if all(path and os.access(path, os.X_OK) for path in helpers):
    report('UNVERIFIED', 'uidmap-helpers', f'executable helpers found at {helpers[0]} and {helpers[1]}; their mapping behavior is not inferred from presence')
else:
    report('FAIL', 'uidmap-helpers', 'newuidmap and newgidmap executables are both required')

# Podman info checks the runtime selected by configuration. Its mutable storage,
# run, cache and temporary paths are private to this invocation and are removed.
# No image is run or fetched; the account's normal HOME config remains visible.
podman = shutil.which('podman')
if not podman:
    report('FAIL', 'podman-rootless-crun', 'podman executable is missing')
elif os.getuid() == 0:
    report('FAIL', 'podman-rootless-crun', 'run the probe as the non-root trial account')
else:
    temp_root = Path(tempfile.mkdtemp(prefix='tbound-probe-'))
    try:
        for name in ('data', 'cache', 'tmp', 'graphroot', 'runroot'):
            (temp_root / name).mkdir(mode=0o700, parents=True, exist_ok=True)
        env = os.environ.copy()
        env['XDG_DATA_HOME'] = str(temp_root / 'data')
        env['XDG_CACHE_HOME'] = str(temp_root / 'cache')
        env['TMPDIR'] = str(temp_root / 'tmp')
        command = [podman, '--root', str(temp_root / 'graphroot'), '--runroot', str(temp_root / 'runroot'), '--tmpdir', str(temp_root / 'tmp'), 'info', '--format', 'json']
        try:
            result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=30, check=False)
        except (OSError, subprocess.TimeoutExpired) as exc:
            report('UNVERIFIED', 'podman-rootless-crun', f'podman info could not complete: {exc}')
        else:
            if result.returncode != 0:
                report('UNVERIFIED', 'podman-rootless-crun', f'podman info exited {result.returncode}: {result.stderr.strip()}')
            else:
                try:
                    info = json.loads(result.stdout)
                    host = info.get('host', {})
                    rootless = host.get('security', {}).get('rootless')
                    runtime = host.get('ociRuntime', {}).get('name')
                    runtime_path = host.get('ociRuntime', {}).get('path')
                    version = info.get('version', {}).get('version', 'unknown')
                    mappings = host.get('idMappings', {})
                    uid_maps = mappings.get('uidmap', mappings.get('uidMap', []))
                    gid_maps = mappings.get('gidmap', mappings.get('gidMap', []))
                except (json.JSONDecodeError, AttributeError) as exc:
                    report('UNVERIFIED', 'podman-rootless-crun', f'could not parse podman info JSON: {exc}')
                else:
                    print(f'OBSERVED     podman-version: {version}')
                    print(f'OBSERVED     podman-runtime-path: {runtime_path or "unknown"}')
                    runtime_exec_ok = False
                    runtime_exec_detail = 'runtime path is missing or not executable'
                    if runtime == 'crun' and isinstance(runtime_path, str) and os.path.isfile(runtime_path) and os.access(runtime_path, os.X_OK):
                        try:
                            crun_result = subprocess.run([runtime_path, '--version'], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, check=False)
                        except (OSError, subprocess.TimeoutExpired) as exc:
                            runtime_exec_detail = f'crun executable probe failed: {exc}'
                        else:
                            runtime_exec_ok = crun_result.returncode == 0 and 'crun' in (crun_result.stdout + crun_result.stderr).lower()
                            runtime_output = (crun_result.stdout + '\n' + crun_result.stderr).strip()
                            runtime_exec_detail = runtime_output.splitlines()[0] if runtime_output else f'exit {crun_result.returncode}'
                    if rootless is True and runtime == 'crun' and runtime_exec_ok:
                        report('PASS', 'podman-rootless-crun', f'podman info reports rootless=true and crun executed successfully ({runtime_exec_detail})')
                    elif rootless is False or (runtime and runtime != 'crun'):
                        report('FAIL', 'podman-rootless-crun', f'podman info reports rootless={rootless!r}, OCI runtime={runtime!r}')
                    elif runtime == 'crun' and not runtime_exec_ok:
                        report('FAIL', 'podman-rootless-crun', f'Podman selected crun but its executable did not pass: {runtime_exec_detail}')
                    else:
                        report('UNVERIFIED', 'podman-rootless-crun', f'podman info did not report both rootless=true and crun (rootless={rootless!r}, runtime={runtime!r})')
                    def has_subordinate_mapping(entries):
                        mapped = 0
                        for entry in entries if isinstance(entries, list) else []:
                            if not isinstance(entry, dict):
                                continue
                            host_id = entry.get('host_id', entry.get('hostID'))
                            container_id = entry.get('container_id', entry.get('containerID'))
                            size = entry.get('size')
                            try:
                                if int(container_id) != 0 or int(host_id) != os.getuid():
                                    mapped += int(size)
                            except (TypeError, ValueError):
                                continue
                        return mapped >= 65536
                    if rootless is True and has_subordinate_mapping(uid_maps) and has_subordinate_mapping(gid_maps):
                        report('PASS', 'podman-subid-maps', 'podman info reports subordinate UID and GID mappings totaling at least 65536 IDs each')
                    elif rootless is True and uid_maps and gid_maps:
                        report('FAIL', 'podman-subid-maps', 'rootless Podman reported no subordinate UID/GID mappings of at least 65536 IDs')
                    else:
                        report('UNVERIFIED', 'podman-subid-maps', 'Podman did not expose enough live mapping data to verify both subordinate ranges')
    finally:
        shutil.rmtree(temp_root, ignore_errors=True)

# Check exact locally installed language runtimes without package managers or
# network access. Go auto-toolchain fetching is explicitly disabled.
node_path = Path('/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin/node')
if node_path.is_file():
    try:
        result = subprocess.run([str(node_path), '--version'], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, check=False)
        if result.returncode == 0 and result.stdout.strip() == 'v24.21.0':
            report('PASS', 'node-runtime', result.stdout.strip())
        else:
            report('FAIL', 'node-runtime', f'expected v24.21.0; got {(result.stdout + result.stderr).strip()}')
    except (OSError, subprocess.TimeoutExpired) as exc:
        report('FAIL', 'node-runtime', f'could not execute pinned Node binary: {exc}')
else:
    report('FAIL', 'node-runtime', f'{node_path} is missing')
go_path = Path('/opt/tbound/toolchains/go1.27.1/bin/go')
if go_path.is_file():
    try:
        env = os.environ.copy()
        env['GOTOOLCHAIN'] = 'local'
        result = subprocess.run([str(go_path), 'version'], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=10, check=False)
        if result.returncode == 0 and 'go1.27.1 linux/amd64' in result.stdout:
            report('PASS', 'go-runtime', result.stdout.strip())
        else:
            report('FAIL', 'go-runtime', f'expected go1.27.1 linux/amd64; got {(result.stdout + result.stderr).strip()}')
    except (OSError, subprocess.TimeoutExpired) as exc:
        report('FAIL', 'go-runtime', f'could not execute pinned Go binary: {exc}')
else:
    report('FAIL', 'go-runtime', f'{go_path} is missing')

print('\nSummary: ' + ', '.join(f'{status}={results.count(status)}' for status in ('PASS', 'FAIL', 'UNVERIFIED')))
if 'FAIL' in results:
    sys.exit(1)
if 'UNVERIFIED' in results:
    sys.exit(2)
sys.exit(0)
PY
