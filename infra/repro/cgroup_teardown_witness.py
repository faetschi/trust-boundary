#!/usr/bin/env python3
"""Run a fixed cgroup.kill component probe on one fresh child of a delegated root.

This is an operator-run guest-host helper, not a provider command surface.  It
accepts no worker command and only writes cgroup.kill through the descriptor of
the unique child it created during this invocation.
"""

from __future__ import annotations

import argparse
import ctypes
import errno
import hashlib
import json
import os
import platform
import re
import select
import secrets
import stat
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any


SCHEMA = "tbound-host-proc-cgroup-profile/v4"
NAMESPACE_NAMES = ("pid", "user", "mnt", "cgroup")
BOOT_ID_PATH = Path("/proc/sys/kernel/random/boot_id")
WORKER_SOURCE = (
    "import os,time\n"
    "gate=bytearray()\n"
    "while len(gate)<2:\n"
    " chunk=os.read(0,2-len(gate))\n"
    " if not chunk: break\n"
    " gate.extend(chunk)\n"
    "if bytes(gate)!=b'G': raise SystemExit(2)\n"
    "for _ in range(2):\n"
    " p=os.fork()\n"
    " if p==0:\n"
    "  time.sleep(120)\n"
    "  os._exit(0)\n"
    "time.sleep(120)\n"
)
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


class Refusal(RuntimeError):
    pass


def _validate_boot_id(value: Any) -> str:
    if not isinstance(value, str):
        raise Refusal("guest_boot_id must be a canonical lowercase UUID")
    try:
        canonical = str(uuid.UUID(value))
    except (ValueError, AttributeError) as exc:
        raise Refusal("guest_boot_id must be a canonical lowercase UUID") from exc
    if value != canonical:
        raise Refusal("guest_boot_id must be a canonical lowercase UUID")
    return canonical


def read_live_boot_id(path: Path = BOOT_ID_PATH) -> str:
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise Refusal(f"cannot read live kernel boot ID: {exc}") from exc
    if raw.endswith(b"\n"):
        raw = raw[:-1]
    try:
        value = raw.decode("ascii")
    except UnicodeDecodeError as exc:
        raise Refusal("live kernel boot ID is not ASCII") from exc
    return _validate_boot_id(value)


def _validate_guest_init_namespace_baseline(value: Any) -> dict[str, dict[str, int]]:
    if not isinstance(value, dict) or set(value) != set(NAMESPACE_NAMES):
        raise Refusal(
            "guest_init_namespaces must pin device/inode identities for exactly "
            f"{list(NAMESPACE_NAMES)}"
        )
    result: dict[str, dict[str, int]] = {}
    for namespace in NAMESPACE_NAMES:
        identity = value[namespace]
        if not isinstance(identity, dict) or set(identity) != {"device", "inode"}:
            raise Refusal(f"guest_init_namespaces.{namespace} must contain exactly device and inode")
        if type(identity["device"]) is not int or identity["device"] < 0:
            raise Refusal(f"guest_init_namespaces.{namespace}.device must be a nonnegative integer")
        if type(identity["inode"]) is not int or identity["inode"] < 1:
            raise Refusal(f"guest_init_namespaces.{namespace}.inode must be a positive integer")
        result[namespace] = {"device": identity["device"], "inode": identity["inode"]}
    return result


def _read_namespace_identity(path: str, namespace: str) -> tuple[dict[str, int], str]:
    try:
        target = os.readlink(path)
        details = os.stat(path)
    except OSError as exc:
        raise Refusal(f"cannot inspect {path}: {exc}") from exc
    match = re.fullmatch(rf"{re.escape(namespace)}:\[(\d+)\]", target)
    if match is None or int(match.group(1)) != details.st_ino:
        raise Refusal(f"{path} is not a stable {namespace} namespace identity")
    return {"device": details.st_dev, "inode": details.st_ino}, target


def _validate_namespace_observations(
    expected: dict[str, dict[str, int]],
    observer: dict[str, dict[str, int]],
    init: dict[str, dict[str, int]],
) -> None:
    for namespace in NAMESPACE_NAMES:
        if observer.get(namespace) != expected[namespace]:
            raise Refusal(f"observer {namespace} namespace differs from the trusted guest-init baseline")
        if init.get(namespace) != expected[namespace]:
            raise Refusal(f"/proc/1 {namespace} namespace differs from the trusted guest-init baseline")


def verify_guest_init_namespaces(value: Any) -> dict[str, str]:
    """Require live /proc namespace identities to equal the trusted boot baseline."""
    expected = _validate_guest_init_namespace_baseline(value)
    observer_links: dict[str, str] = {}
    observer_ids: dict[str, dict[str, int]] = {}
    init_ids: dict[str, dict[str, int]] = {}
    for namespace in NAMESPACE_NAMES:
        observer, observer_link = _read_namespace_identity(f"/proc/self/ns/{namespace}", namespace)
        init, _ = _read_namespace_identity(f"/proc/1/ns/{namespace}", namespace)
        observer_ids[namespace] = observer
        init_ids[namespace] = init
        observer_links[namespace] = observer_link
    _validate_namespace_observations(expected, observer_ids, init_ids)
    return observer_links


def verify_guest_init_identity(boot_id: Any, namespaces: Any) -> tuple[str, dict[str, str]]:
    expected_boot_id = _validate_boot_id(boot_id)
    observed_boot_id = read_live_boot_id()
    if observed_boot_id != expected_boot_id:
        raise Refusal("running guest boot ID differs from the trusted profile baseline")
    return observed_boot_id, verify_guest_init_namespaces(namespaces)


def _strict_json(data: bytes) -> dict[str, Any]:
    def pairs(items: list[tuple[str, Any]]) -> dict[str, Any]:
        result: dict[str, Any] = {}
        for key, value in items:
            if key in result:
                raise Refusal(f"duplicate JSON key: {key}")
            result[key] = value
        return result

    try:
        value = json.loads(data, object_pairs_hook=pairs)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise Refusal(f"invalid profile JSON: {exc}") from exc
    if not isinstance(value, dict):
        raise Refusal("profile JSON must be an object")
    return value


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    fd = -1
    try:
        fd = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
        file_stat = os.fstat(fd)
        if not stat.S_ISREG(file_stat.st_mode):
            raise Refusal(f"identity file is not a regular file: {path}")
        with os.fdopen(fd, "rb") as stream:
            fd = -1
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as exc:
        raise Refusal(f"cannot read identity file {path}: {exc}") from exc
    finally:
        if fd >= 0:
            os.close(fd)
    return digest.hexdigest()


def read_identity_document(path: Path) -> tuple[bytes, str]:
    if not path.is_absolute():
        raise Refusal("profile manifest path must be absolute")
    try:
        if Path(os.path.realpath(path, strict=True)) != path:
            raise Refusal("profile manifest path must not contain symlinks or non-canonical components")
        fd = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
    except OSError as exc:
        raise Refusal(f"cannot open profile manifest: {exc}") from exc
    try:
        file_stat = os.fstat(fd)
        if not stat.S_ISREG(file_stat.st_mode) or file_stat.st_size > 1024 * 1024:
            raise Refusal("profile manifest must be a regular file no larger than 1 MiB")
        with os.fdopen(fd, "rb") as stream:
            fd = -1
            data = stream.read(1024 * 1024 + 1)
        if len(data) > 1024 * 1024:
            raise Refusal("profile manifest exceeds 1 MiB")
        return data, hashlib.sha256(data).hexdigest()
    finally:
        if fd >= 0:
            os.close(fd)


def parse_unified_cgroup(data: str) -> str:
    rows = [line.split(":", 2) for line in data.splitlines() if line]
    unified = [row[2] for row in rows if len(row) == 3 and row[0] == "0" and row[1] == ""]
    if len(unified) != 1 or not unified[0].startswith("/"):
        raise Refusal("/proc entry has no unique unified cgroup membership")
    return unified[0]


def parse_events(data: str) -> int:
    values: dict[str, int] = {}
    for line in data.splitlines():
        parts = line.split()
        if len(parts) != 2 or parts[0] in values or parts[1] not in {"0", "1"}:
            raise Refusal("malformed cgroup.events")
        values[parts[0]] = int(parts[1])
    if "populated" not in values:
        raise Refusal("cgroup.events omitted populated")
    return values["populated"]


def parse_proc_stat(data: str) -> dict[str, int | str]:
    # comm is parenthesized and may itself contain spaces or closing parens.
    left = data.find("(")
    right = data.rfind(")")
    if left <= 0 or right <= left or right + 2 > len(data):
        raise Refusal("malformed /proc/<pid>/stat")
    try:
        pid = int(data[:left].strip())
        fields = data[right + 2 :].split()
        if len(fields) < 20:
            raise ValueError("short stat record")
        return {
            "pid": pid,
            "state": fields[0],
            "ppid": int(fields[1]),
            "starttime_ticks": int(fields[19]),
        }
    except ValueError as exc:
        raise Refusal(f"malformed /proc/<pid>/stat fields: {exc}") from exc


def validate_profile(profile: dict[str, Any], root_argument: str) -> tuple[Path, str, dict[str, int]]:
    required = {
        "schema",
        "profile_id",
        "kernel_release",
        "landlock_abi",
        "runtime_profile_id",
        "runtime_profile_path",
        "runtime_profile_sha256",
        "teardown_deadline_ms",
        "guest_boot_id",
        "delegated_root",
        "guest_init_namespaces",
    }
    if set(profile) != required:
        raise Refusal(f"profile keys must be exactly {sorted(required)}")
    if profile["schema"] != SCHEMA:
        raise Refusal(f"profile schema must be {SCHEMA}")
    _validate_boot_id(profile["guest_boot_id"])
    _validate_guest_init_namespace_baseline(profile["guest_init_namespaces"])
    for key in ("profile_id", "runtime_profile_id", "kernel_release"):
        if not isinstance(profile[key], str) or not profile[key].strip():
            raise Refusal(f"profile {key} must be a non-empty string")
    if type(profile["landlock_abi"]) is not int or profile["landlock_abi"] != 8:
        raise Refusal("profile must pin the exact required Landlock ABI 8")
    deadline = profile["teardown_deadline_ms"]
    if type(deadline) is not int or not 1 <= deadline <= 60_000:
        raise Refusal("registered teardown_deadline_ms must be an integer from 1 to 60000")
    runtime_digest = profile["runtime_profile_sha256"]
    if not isinstance(runtime_digest, str) or not SHA256_RE.fullmatch(runtime_digest):
        raise Refusal("runtime_profile_sha256 must be a lowercase SHA-256 digest")
    if not isinstance(profile["runtime_profile_path"], str):
        raise Refusal("runtime_profile_path must be an absolute path string")
    runtime_path = Path(profile["runtime_profile_path"])
    if not runtime_path.is_absolute():
        raise Refusal("runtime_profile_path must be absolute")
    try:
        if Path(os.path.realpath(runtime_path, strict=True)) != runtime_path:
            raise Refusal("runtime_profile_path must not contain symlinks or non-canonical components")
    except OSError as exc:
        raise Refusal(f"runtime profile path cannot be resolved: {exc}") from exc
    if sha256_file(runtime_path) != runtime_digest:
        raise Refusal("runtime profile digest does not match its pinned identity")

    root = profile["delegated_root"]
    root_keys = {"path", "device", "inode", "mount_id", "owner_uid"}
    if not isinstance(root, dict) or set(root) != root_keys:
        raise Refusal(f"delegated_root keys must be exactly {sorted(root_keys)}")
    if not isinstance(root["path"], str):
        raise Refusal("delegated_root.path must be an absolute path string")
    root_path = Path(root["path"])
    if not root_path.is_absolute() or str(root_path) != root_argument:
        raise Refusal("selected delegated root must exactly match the profile path")
    for key in ("device", "inode", "mount_id", "owner_uid"):
        minimum = 0 if key == "device" else 1
        if type(root[key]) is not int or root[key] < minimum:
            raise Refusal(f"delegated_root.{key} must be an integer no smaller than {minimum}")
    if root["owner_uid"] != 0:
        raise Refusal("protected observer root must be owned by trusted host UID 0")
    if not isinstance(profile["kernel_release"], str):
        raise Refusal("kernel_release must be a string")
    return runtime_path, runtime_digest, root


def parse_cgroup2_mount() -> tuple[int, Path, str]:
    try:
        lines = Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise Refusal(f"cannot read host /proc/self/mountinfo: {exc}") from exc
    mounts: list[tuple[int, Path, str]] = []
    for line in lines:
        left, separator, right = line.partition(" - ")
        if not separator:
            continue
        before, after = left.split(), right.split()
        if len(before) < 5 or not after or after[0] != "cgroup2":
            continue
        unescape = lambda value: re.sub(
            r"\\([0-7]{3})", lambda match: chr(int(match.group(1), 8)), value
        )
        try:
            mounts.append((int(before[0]), Path(unescape(before[4])), unescape(before[3])))
        except ValueError as exc:
            raise Refusal("invalid cgroup2 mount identity in mountinfo") from exc
    if len(mounts) != 1:
        raise Refusal(f"expected one visible cgroup2 mount, found {len(mounts)}")
    return mounts[0]


def _assert_no_nonroot_write(st: os.stat_result, label: str, require_owner_write: bool = False) -> None:
    mode = stat.S_IMODE(st.st_mode)
    if mode & 0o022:
        raise Refusal(f"{label} is writable by group/other; non-root writer exclusion is not established")
    if require_owner_write and mode & 0o300 != 0o300:
        raise Refusal(f"{label} lacks owner write/search permission for the trusted root observer")


def _assert_protected_directory(st: os.stat_result, label: str) -> None:
    if not stat.S_ISDIR(st.st_mode):
        raise Refusal(f"{label} is not a directory")
    if st.st_uid != 0:
        raise Refusal(f"{label} is not owned by trusted host UID 0")
    _assert_no_nonroot_write(st, label, require_owner_write=True)


def _open_protected_path(path: Path) -> tuple[int, list[dict[str, Any]]]:
    if not path.is_absolute() or Path(os.path.normpath(path)) != path:
        raise Refusal("protected observer root path must be canonical and absolute")
    current_fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)
    current_path = Path("/")
    rows: list[dict[str, Any]] = []
    try:
        root_stat = os.fstat(current_fd)
        _assert_protected_directory(root_stat, "/")
        rows.append({"path": "/", "device": root_stat.st_dev, "inode": root_stat.st_ino, "uid": root_stat.st_uid, "mode": format(stat.S_IMODE(root_stat.st_mode), "04o")})
        for component in path.parts[1:]:
            entry_stat = os.stat(component, dir_fd=current_fd, follow_symlinks=False)
            _assert_protected_directory(entry_stat, str(current_path / component))
            child_fd = os.open(
                component,
                os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
                dir_fd=current_fd,
            )
            try:
                held_stat = os.fstat(child_fd)
                if (entry_stat.st_dev, entry_stat.st_ino) != (held_stat.st_dev, held_stat.st_ino):
                    raise Refusal(f"protected path component changed while opening: {current_path / component}")
            except Exception:
                os.close(child_fd)
                raise
            os.close(current_fd)
            current_fd = child_fd
            current_path = current_path / component
            rows.append({"path": str(current_path), "device": held_stat.st_dev, "inode": held_stat.st_ino, "uid": held_stat.st_uid, "mode": format(stat.S_IMODE(held_stat.st_mode), "04o")})
        final_fd = current_fd
        current_fd = -1
        return final_fd, rows
    finally:
        if current_fd >= 0:
            os.close(current_fd)


def _protected_path_snapshot(path: Path, expected_final: os.stat_result) -> list[dict[str, Any]]:
    final_fd, rows = _open_protected_path(path)
    try:
        final_stat = os.fstat(final_fd)
        if (final_stat.st_dev, final_stat.st_ino) != (expected_final.st_dev, expected_final.st_ino):
            raise Refusal("protected observer root path no longer resolves to its held directory identity")
        return rows
    finally:
        os.close(final_fd)


def _cgroup_ancestor_control_identities(mountpoint: Path, root_path: Path) -> list[dict[str, Any]]:
    try:
        relative = root_path.relative_to(mountpoint)
    except ValueError as exc:
        raise Refusal("protected observer root is outside the visible cgroup2 mount") from exc
    current_fd, _ = _open_protected_path(mountpoint)
    rows: list[dict[str, Any]] = []
    current_path = mountpoint
    try:
        for index, component in enumerate(("", *relative.parts)):
            if component:
                entry = os.stat(component, dir_fd=current_fd, follow_symlinks=False)
                _assert_protected_directory(entry, str(current_path / component))
                child_fd = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=current_fd)
                held = os.fstat(child_fd)
                if (entry.st_dev, entry.st_ino) != (held.st_dev, held.st_ino):
                    os.close(child_fd)
                    raise Refusal(f"cgroup ancestor changed while opening: {current_path / component}")
                os.close(current_fd)
                current_fd = child_fd
                current_path = current_path / component
            controls: list[dict[str, Any]] = []
            for name in ("cgroup.procs", "cgroup.subtree_control", "cgroup.events"):
                controls.append(_cgroup_control_identity(current_fd, name, require_owner_write=False))
            try:
                controls.append(_cgroup_control_identity(current_fd, "cgroup.kill", require_owner_write=False))
            except FileNotFoundError:
                if index != 0:
                    raise Refusal(f"non-root cgroup ancestor omitted cgroup.kill: {current_path}")
            held = os.fstat(current_fd)
            rows.append({"path": str(current_path), "device": held.st_dev, "inode": held.st_ino, "controls": controls})
        return rows
    finally:
        os.close(current_fd)


def _cgroup_child_names(directory_fd: int) -> list[str]:
    children: list[str] = []
    for name in os.listdir(directory_fd):
        entry = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
        if stat.S_ISDIR(entry.st_mode):
            children.append(name)
        elif not stat.S_ISREG(entry.st_mode):
            raise Refusal(f"cgroup directory contains an unexpected non-control entry: {name}")
    return sorted(children)


def _require_descendant_free(directory_fd: int, label: str) -> None:
    children = _cgroup_child_names(directory_fd)
    if children:
        raise Refusal(f"{label} contains nested cgroup descendants: {children}")


def _cgroup_control_identity(directory_fd: int, name: str, require_owner_write: bool) -> dict[str, int | str]:
    st = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    if not stat.S_ISREG(st.st_mode):
        raise Refusal(f"cgroup control {name} is not a regular control file")
    if st.st_uid != 0:
        raise Refusal(f"cgroup control {name} is not owned by trusted host UID 0")
    _assert_no_nonroot_write(st, f"cgroup control {name}")
    if require_owner_write and not (stat.S_IMODE(st.st_mode) & 0o200):
        raise Refusal(f"trusted root observer cannot write cgroup control {name}")
    return {"name": name, "device": st.st_dev, "inode": st.st_ino, "uid": st.st_uid, "mode": format(stat.S_IMODE(st.st_mode), "04o")}


def _verify_scope_permissions(scope_fd: int) -> list[dict[str, int | str]]:
    _assert_protected_directory(os.fstat(scope_fd), "fresh disposable scope")
    return [
        _cgroup_control_identity(scope_fd, "cgroup.procs", require_owner_write=True),
        _cgroup_control_identity(scope_fd, "cgroup.kill", require_owner_write=True),
        _cgroup_control_identity(scope_fd, "cgroup.events", require_owner_write=False),
    ]


def _verify_writer_exclusion(
    root_fd: int,
    root_path: Path,
    expected_root: dict[str, int],
    expected_mount: tuple[int, Path, str],
    expected_children: list[str],
    require_empty: bool,
) -> dict[str, Any]:
    if os.geteuid() != 0:
        raise Refusal("observer must run as trusted host root for the protected observer subtree")
    root_stat = os.fstat(root_fd)
    _assert_protected_directory(root_stat, "selected protected observer root")
    if (root_stat.st_dev, root_stat.st_ino, root_stat.st_uid) != (
        expected_root["device"], expected_root["inode"], expected_root["owner_uid"]
    ):
        raise Refusal("held protected observer root differs from the pinned profile identity")
    if expected_root["mount_id"] != expected_mount[0]:
        raise Refusal("pinned protected observer root mount ID differs from the visible cgroup2 mount")
    path_rows = _protected_path_snapshot(root_path, root_stat)
    if parse_cgroup2_mount() != expected_mount:
        raise Refusal("visible cgroup2 mount identity changed during the observer run")
    cgroup_ancestors = _cgroup_ancestor_control_identities(expected_mount[1], root_path)
    if not cgroup_ancestors or (cgroup_ancestors[-1]["device"], cgroup_ancestors[-1]["inode"]) != (root_stat.st_dev, root_stat.st_ino):
        raise Refusal("protected cgroup ancestry no longer resolves to the held observer root")
    controls = [
        _cgroup_control_identity(root_fd, "cgroup.procs", require_owner_write=True),
        _cgroup_control_identity(root_fd, "cgroup.kill", require_owner_write=True),
        _cgroup_control_identity(root_fd, "cgroup.events", require_owner_write=False),
    ]
    if _scope_members(root_fd):
        raise Refusal("protected observer root contains direct processes outside the disposable child")
    observed_children = _cgroup_child_names(root_fd)
    if observed_children != sorted(expected_children):
        raise Refusal(f"protected observer root child set changed: observed {observed_children}, expected {sorted(expected_children)}")
    if require_empty and parse_events(_read_at(root_fd, "cgroup.events")) != 0:
        raise Refusal("protected observer root was not empty at preflight")
    return {
        "status": "verified-from-root-owned-path-and-control-permissions",
        "trusted_writer_uid": 0,
        "nonroot_writer_excluded": True,
        "root_is_trusted_tcb": True,
        "caller_assertion_used": False,
        "protected_path_ancestry": path_rows,
        "protected_cgroup_ancestors": cgroup_ancestors,
        "protected_root_controls": controls,
        "expected_direct_children": sorted(expected_children),
    }


def landlock_abi() -> int:
    if sys.platform != "linux" or platform.machine() != "x86_64":
        raise Refusal("exact ABI probe is implemented only for Linux x86_64")
    libc = ctypes.CDLL(None, use_errno=True)
    libc.syscall.restype = ctypes.c_long
    ctypes.set_errno(0)
    # landlock_create_ruleset(2), x86_64 syscall number, VERSION query flag.
    result = libc.syscall(444, None, ctypes.c_size_t(0), ctypes.c_uint(1))
    if result < 0:
        number = ctypes.get_errno()
        raise Refusal(f"Landlock ABI version query failed: errno={number} ({os.strerror(number)})")
    return int(result)


def _read_at(directory_fd: int, name: str, limit: int = 64 * 1024) -> str:
    fd = os.open(name, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=directory_fd)
    with os.fdopen(fd, "rb") as stream:
        data = stream.read(limit + 1)
    if len(data) > limit:
        raise Refusal(f"cgroup control {name} exceeds read limit")
    return data.decode("ascii", errors="strict").strip()


def verify_root(profile: dict[str, Any], root_argument: str) -> tuple[int, Path, tuple[int, Path, str], dict[str, int], str, str, dict[str, Any]]:
    runtime_path, runtime_digest, expected_root = validate_profile(profile, root_argument)
    root_path = Path(root_argument)
    if os.geteuid() != 0:
        raise Refusal("run as trusted host root with a pre-provisioned protected observer subtree")
    try:
        resolved = Path(os.path.realpath(root_path, strict=True))
    except OSError as exc:
        raise Refusal(f"protected observer root cannot be resolved: {exc}") from exc
    if resolved != root_path:
        raise Refusal("protected observer root path contains a symlink or non-canonical component")
    mount_info = parse_cgroup2_mount()
    mount_id, mountpoint, mount_root = mount_info
    try:
        relative = root_path.relative_to(mountpoint)
    except ValueError as exc:
        raise Refusal("protected observer root is outside the visible cgroup2 mount") from exc
    if mount_root != "/":
        raise Refusal("cgroup2 mount must expose the hierarchy root for exact init-namespace /proc path mapping")
    if not relative.parts and mount_root == "/":
        raise Refusal("selected protected observer root resolves to the cgroup-v2 hierarchy root")
    root_fd = os.open(root_path, os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW)
    try:
        root_stat = os.fstat(root_fd)
        if (root_stat.st_dev, root_stat.st_ino, mount_id, root_stat.st_uid) != (
            expected_root["device"],
            expected_root["inode"],
            expected_root["mount_id"],
            0,
        ):
            raise Refusal("protected observer root device/inode/mount/owner differs from pinned profile")
        _assert_protected_directory(root_stat, "selected protected observer root")
        root_type = _read_at(root_fd, "cgroup.type")
        _read_at(root_fd, "cgroup.controllers")
        _read_at(root_fd, "cgroup.subtree_control")
        populated = parse_events(_read_at(root_fd, "cgroup.events"))
        if _scope_members(root_fd):
            raise Refusal("protected observer root must contain no direct processes")
        _require_descendant_free(root_fd, "protected observer root")
    except (OSError, UnicodeError, Refusal) as exc:
        os.close(root_fd)
        raise Refusal(f"protected cgroup-v2 observer root lacks required controls: {exc}") from exc
    if root_type != "domain":
        os.close(root_fd)
        raise Refusal(f"protected observer root type is {root_type!r}, expected domain")
    if populated != 0:
        os.close(root_fd)
        raise Refusal("protected observer root must be empty before the disposable probe")
    expected_proc_prefix = "/" + relative.as_posix().strip("/")
    try:
        writer_evidence = _verify_writer_exclusion(root_fd, root_path, expected_root, mount_info, [], require_empty=True)
    except Exception:
        os.close(root_fd)
        raise
    return root_fd, root_path, mount_info, expected_root, expected_proc_prefix, runtime_digest, writer_evidence


def _proc_record(pid: int) -> dict[str, Any]:
    base = Path("/proc") / str(pid)
    stat_record = parse_proc_stat((base / "stat").read_text(encoding="ascii"))
    if stat_record["pid"] != pid:
        raise Refusal(f"/proc/{pid}/stat reported a mismatched PID identity")
    cgroup_path = parse_unified_cgroup((base / "cgroup").read_text(encoding="ascii"))
    status = (base / "status").read_text(encoding="ascii")
    uid_row = next((line for line in status.splitlines() if line.startswith("Uid:")), None)
    if uid_row is None:
        raise Refusal(f"/proc/{pid}/status omitted Uid")
    try:
        uids = [int(value) for value in uid_row.split()[1:5]]
    except ValueError as exc:
        raise Refusal(f"/proc/{pid}/status has malformed Uid") from exc
    if len(uids) != 4:
        raise Refusal(f"/proc/{pid}/status has malformed Uid")
    stat_record.update({"cgroup_path": cgroup_path, "uid": uids[0]})
    return stat_record


def _scope_members(scope_fd: int) -> list[int]:
    text = _read_at(scope_fd, "cgroup.procs")
    try:
        values = [int(row) for row in text.splitlines() if row]
    except ValueError as exc:
        raise Refusal("cgroup.procs contains a non-PID value") from exc
    if len(values) != len(set(values)) or any(pid <= 0 for pid in values):
        raise Refusal("cgroup.procs contains duplicate or invalid PIDs")
    return sorted(values)


def _scope_cgroup_path(prefix: str, scope_name: str) -> str:
    return prefix.rstrip("/") + "/" + scope_name


def _validate_scope_identity(
    name: str,
    root_device: int,
    root_inode: int,
    scope_device: int,
    scope_inode: int,
    reopened_device: int,
    reopened_inode: int,
    expected_identity: tuple[int, int] | None = None,
) -> None:
    if not re.fullmatch(r"\.tbound-proc-witness-[0-9a-f]{24}", name):
        raise Refusal("disposable scope name is not the fixed helper-owned direct-child form")
    if scope_device != root_device or scope_inode == root_inode or (reopened_device, reopened_inode) != (scope_device, scope_inode):
        raise Refusal("held disposable-scope identity no longer matches its anchored child entry")
    if expected_identity is not None and (scope_device, scope_inode) != expected_identity:
        raise Refusal("disposable scope entry differs from the identity captured immediately after mkdir")


def _capture_created_scope_identity(root_fd: int, scope_name: str) -> tuple[int, int]:
    root_stat = os.fstat(root_fd)
    entry_stat = os.stat(scope_name, dir_fd=root_fd, follow_symlinks=False)
    _validate_scope_identity(
        scope_name,
        root_stat.st_dev,
        root_stat.st_ino,
        entry_stat.st_dev,
        entry_stat.st_ino,
        entry_stat.st_dev,
        entry_stat.st_ino,
    )
    return entry_stat.st_dev, entry_stat.st_ino


def _open_bound_scope(root_fd: int, scope_name: str, expected_identity: tuple[int, int]) -> int:
    root_stat = os.fstat(root_fd)
    before = os.stat(scope_name, dir_fd=root_fd, follow_symlinks=False)
    _validate_scope_identity(
        scope_name,
        root_stat.st_dev,
        root_stat.st_ino,
        before.st_dev,
        before.st_ino,
        before.st_dev,
        before.st_ino,
        expected_identity,
    )
    scope_fd = os.open(
        scope_name,
        os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
        dir_fd=root_fd,
    )
    try:
        held_stat = os.fstat(scope_fd)
        after = os.stat(scope_name, dir_fd=root_fd, follow_symlinks=False)
        _validate_scope_identity(
            scope_name,
            root_stat.st_dev,
            root_stat.st_ino,
            held_stat.st_dev,
            held_stat.st_ino,
            after.st_dev,
            after.st_ino,
            expected_identity,
        )
        return scope_fd
    except Exception:
        os.close(scope_fd)
        raise


def _verify_scope_anchor(
    root_fd: int,
    scope_fd: int,
    scope_name: str,
    expected_identity: tuple[int, int],
) -> os.stat_result:
    root_stat = os.fstat(root_fd)
    held_stat = os.fstat(scope_fd)
    entry_before = os.stat(scope_name, dir_fd=root_fd, follow_symlinks=False)
    _validate_scope_identity(
        scope_name,
        root_stat.st_dev,
        root_stat.st_ino,
        entry_before.st_dev,
        entry_before.st_ino,
        held_stat.st_dev,
        held_stat.st_ino,
        expected_identity,
    )
    reopened_fd = os.open(
        scope_name,
        os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC | os.O_NOFOLLOW,
        dir_fd=root_fd,
    )
    try:
        reopened_stat = os.fstat(reopened_fd)
        entry_after = os.stat(scope_name, dir_fd=root_fd, follow_symlinks=False)
        _validate_scope_identity(
            scope_name,
            root_stat.st_dev,
            root_stat.st_ino,
            held_stat.st_dev,
            held_stat.st_ino,
            entry_after.st_dev,
            entry_after.st_ino,
            expected_identity,
        )
        if (reopened_stat.st_dev, reopened_stat.st_ino) != expected_identity:
            raise Refusal("reopened disposable scope differs from the captured child identity")
    finally:
        os.close(reopened_fd)
    return held_stat


def _remove_empty_scope(
    root_fd: int,
    scope_fd: int,
    scope_name: str,
    expected_identity: tuple[int, int],
    root_path: Path,
    expected_root: dict[str, int],
    expected_mount: tuple[int, Path, str],
) -> None:
    _verify_scope_permissions(scope_fd)
    _verify_scope_anchor(root_fd, scope_fd, scope_name, expected_identity)
    _require_descendant_free(scope_fd, "disposable scope before removal")
    if _scope_members(scope_fd) or parse_events(_read_at(scope_fd, "cgroup.events")) != 0:
        raise Refusal("disposable scope is not empty; refusing name-based removal")
    _verify_writer_exclusion(root_fd, root_path, expected_root, expected_mount, [scope_name], require_empty=False)
    _rmdir_bound_scope_entry(root_fd, scope_fd, scope_name, expected_identity)


def _rmdir_bound_scope_entry(
    root_fd: int,
    scope_fd: int,
    scope_name: str,
    expected_identity: tuple[int, int],
) -> None:
    """Remove only the still-anchored directory captured after its creation."""
    _verify_scope_anchor(root_fd, scope_fd, scope_name, expected_identity)
    os.rmdir(scope_name, dir_fd=root_fd)


def validate_witness_timing(snapshot_ns: int, kill_ns: int, after_ns: int, deadline_ms: int) -> None:
    if any(type(value) is not int or value <= 0 for value in (snapshot_ns, kill_ns, after_ns)):
        raise Refusal("witness timestamps are missing, unknown, or invalid")
    if not snapshot_ns < kill_ns < after_ns:
        raise Refusal("witness timestamps are stale or out of observation order")
    if after_ns - kill_ns > deadline_ms * 1_000_000:
        raise Refusal("witness timestamps exceed the registered teardown deadline")


def _open_pidfd(pid: int) -> int:
    if not hasattr(os, "pidfd_open"):
        raise Refusal("Python lacks os.pidfd_open; use Python 3.9 or newer")
    try:
        return os.pidfd_open(pid, 0)
    except OSError as exc:
        raise Refusal(f"pidfd_open({pid}) failed: {exc}") from exc


def _close_and_reap_worker(
    worker: subprocess.Popen[bytes] | None,
    gate_write_attempted: bool,
) -> None:
    if worker is None:
        return
    if worker.stdin is not None and not worker.stdin.closed:
        try:
            worker.stdin.close()
        except (BrokenPipeError, OSError):
            pass
    if worker.poll() is None:
        try:
            worker.wait(timeout=0.2 if gate_write_attempted else 2.0)
        except subprocess.TimeoutExpired:
            if not gate_write_attempted:
                # The exact worker cannot fork until the gate, so only this
                # startup child may be terminated and reaped on this path.
                try:
                    worker.kill()
                except ProcessLookupError:
                    pass
                worker.wait(timeout=2.0)


def _await_process_set(scope_fd: int, scope_path: str, worker_pid: int, worker_uid: int, deadline: float) -> list[dict[str, Any]]:
    last_problem = "fixture descendants were not observed"
    while time.monotonic() < deadline:
        pids = _scope_members(scope_fd)
        try:
            records = [_proc_record(pid) for pid in pids]
        except (OSError, Refusal) as exc:
            last_problem = f"host /proc snapshot incomplete: {exc}"
            time.sleep(0.025)
            continue
        if not records:
            time.sleep(0.025)
            continue
        if any(row["cgroup_path"] != scope_path for row in records):
            raise Refusal("a cgroup.procs member has mismatched /proc cgroup membership")
        by_pid = {row["pid"]: row for row in records}
        if len(by_pid) != len(records) or worker_pid not in by_pid:
            raise Refusal("host process snapshot has duplicate identity or omitted worker")
        children = [row for row in records if row["ppid"] == worker_pid]
        if len(records) == 3 and len(children) == 2 and by_pid[worker_pid]["ppid"] != os.getpid():
            # Reparenting is not expected while the fixed worker is blocked in sleep.
            raise Refusal("fixed worker parent identity changed before teardown")
        if len(records) == 3 and len(children) == 2:
            for row in records:
                if row["uid"] != worker_uid:
                    raise Refusal("observed fixture process UID differs from the pinned unprivileged fixture UID")
            return sorted(records, key=lambda row: int(row["pid"]))
        if len(records) > 3:
            raise Refusal("unexpected process appeared in the disposable scope")
        time.sleep(0.025)
    raise Refusal(last_problem)


def _pidfd_readable(fd: int) -> bool:
    poller = select.poll()
    poller.register(fd, select.POLLIN | select.POLLHUP | select.POLLERR)
    return bool(poller.poll(0))


def _post_observation(pre: list[dict[str, Any]], pidfds: dict[int, int], scope_path: str) -> list[dict[str, Any]]:
    observed: list[dict[str, Any]] = []
    for before in pre:
        pid = int(before["pid"])
        readable = _pidfd_readable(pidfds[pid])
        try:
            now = _proc_record(pid)
        except FileNotFoundError:
            now = None
        except (OSError, Refusal) as exc:
            observed.append({"pid": pid, "starttime_ticks": before["starttime_ticks"], "status": "unknown", "detail": str(exc), "pidfd_readable": readable})
            continue
        if readable:
            if now is not None and now["starttime_ticks"] == before["starttime_ticks"]:
                if now["state"] in {"Z", "X"}:
                    observed.append({
                        "pid": pid,
                        "starttime_ticks": before["starttime_ticks"],
                        "status": "exited",
                        "pidfd_readable": True,
                        "proc_after": "zombie-or-dead",
                        "proc_after_state": now["state"],
                    })
                else:
                    observed.append({"pid": pid, "starttime_ticks": before["starttime_ticks"], "status": "unknown", "detail": "pidfd readable but /proc identity is not dead/zombie", "pidfd_readable": True})
            else:
                observed.append({
                    "pid": pid,
                    "starttime_ticks": before["starttime_ticks"],
                    "status": "exited",
                    "pidfd_readable": True,
                    "proc_after": "absent" if now is None else "pid-reused",
                    "proc_after_starttime_ticks": None if now is None else now["starttime_ticks"],
                })
        elif now is None:
            observed.append({"pid": pid, "starttime_ticks": before["starttime_ticks"], "status": "unknown", "detail": "pidfd not readable while /proc entry is absent", "pidfd_readable": False})
        elif now["starttime_ticks"] != before["starttime_ticks"]:
            observed.append({"pid": pid, "starttime_ticks": before["starttime_ticks"], "status": "unknown", "detail": "PID reused before original pidfd became readable", "pidfd_readable": False})
        elif now["cgroup_path"] != scope_path:
            observed.append({"pid": pid, "status": "escaped", "pidfd_readable": False, "cgroup_path": now["cgroup_path"], "starttime_ticks": before["starttime_ticks"]})
        else:
            observed.append({"pid": pid, "starttime_ticks": before["starttime_ticks"], "status": "surviving", "pidfd_readable": False, "cgroup_path": now["cgroup_path"]})
    return observed


def validate_post_observations(pre: list[dict[str, Any]], post: list[dict[str, Any]], scope_path: str) -> None:
    if not pre or len(post) != len(pre):
        raise Refusal("missing or incomplete host /proc+pidfd witness rows")
    if any(row.get("cgroup_path") != scope_path or row.get("pidfd_opened") is not True for row in pre):
        raise Refusal("pre-teardown witness has missing or mismatched scope/pidfd identity")
    pre_ids = {(int(row["pid"]), int(row["starttime_ticks"])) for row in pre}
    post_ids = [int(row.get("pid", -1)) for row in post]
    if len(pre_ids) != len(pre) or len(set(post_ids)) != len(post_ids) or set(post_ids) != {pid for pid, _ in pre_ids}:
        raise Refusal("host witness PID identities do not match the pre-teardown set")
    expected_starts = {pid: start for pid, start in pre_ids}
    for row in post:
        if row.get("starttime_ticks") != expected_starts.get(int(row["pid"])):
            raise Refusal(f"post-teardown PID identity mismatch for PID {row.get('pid')}")
        if row.get("status") != "exited" or row.get("pidfd_readable") is not True:
            if row.get("status") == "escaped":
                raise Refusal(f"observed descendant escaped to {row.get('cgroup_path')}")
            if row.get("status") == "surviving":
                raise Refusal(f"descendant survived in {scope_path}")
            raise Refusal(f"descendant witness is unknown for PID {row.get('pid')}")


def _write_kill(scope_fd: int) -> None:
    fd = os.open("cgroup.kill", os.O_WRONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=scope_fd)
    try:
        if os.write(fd, b"1\n") != 2:
            raise Refusal("short write to freshly created child cgroup.kill")
    finally:
        os.close(fd)


def _write_bound_scope_kill(
    root_fd: int,
    scope_fd: int,
    scope_name: str,
    expected_identity: tuple[int, int],
) -> None:
    """Final descriptor/name and recursive-scope guard immediately before write."""
    _verify_scope_anchor(root_fd, scope_fd, scope_name, expected_identity)
    _require_descendant_free(scope_fd, "disposable scope at cgroup.kill write")
    _write_kill(scope_fd)


def run_probe(profile_path: Path, root_argument: str, confirmed: bool) -> dict[str, Any]:
    if sys.platform != "linux":
        raise Refusal("this helper must run on the trusted Linux guest host")
    if not confirmed:
        raise Refusal("pass --confirm-disposable-cgroup-kill to authorize the fixed child-scope fixture")
    profile_bytes, profile_manifest_digest = read_identity_document(profile_path)
    profile = _strict_json(profile_bytes)
    validate_profile(profile, root_argument)
    observed_boot_id, namespace_ids = verify_guest_init_identity(
        profile["guest_boot_id"], profile["guest_init_namespaces"]
    )
    root_fd, root_path, mount_info, root_identity, cgroup_prefix, runtime_digest, initial_writer_evidence = verify_root(profile, root_argument)
    scope_fd = -1
    scope_name: str | None = None
    scope_path: str | None = None
    scope_identity: tuple[int, int] | None = None
    scope_stat: os.stat_result | None = None
    worker: subprocess.Popen[bytes] | None = None
    worker_gate_write_attempted = False
    pidfds: dict[int, int] = {}
    try:
        actual_release = platform.uname().release
        actual_abi = landlock_abi()
        if profile["kernel_release"] != actual_release:
            raise Refusal("running kernel release differs from the pinned profile")
        if actual_abi != 8:
            raise Refusal(f"live Landlock ABI is {actual_abi}, exact ABI 8 is required")
        if sys.version_info < (3, 9):
            raise Refusal("Python 3.9 or newer is required for pidfd_open and explicit unprivileged worker credentials")
        if not hasattr(os, "pidfd_open"):
            raise Refusal("Python lacks os.pidfd_open")
        if sha256_file(profile_path) != profile_manifest_digest or sha256_file(Path(profile["runtime_profile_path"])) != runtime_digest:
            raise Refusal("candidate profile or runtime-profile identity changed during preflight")
        # The selected root is pre-provisioned and protected.  Its root-only
        # ownership/modes exclude every non-root writer; root is the trusted
        # host TCB.  Create one random child, capture the entry identity before
        # opening it, then bind all later operations to that identity.
        scope_name = ".tbound-proc-witness-" + secrets.token_hex(12)
        scope_path = _scope_cgroup_path(cgroup_prefix, scope_name)
        _verify_writer_exclusion(root_fd, root_path, root_identity, mount_info, [], require_empty=True)
        os.mkdir(scope_name, 0o700, dir_fd=root_fd)
        try:
            scope_identity = _capture_created_scope_identity(root_fd, scope_name)
            scope_fd = _open_bound_scope(root_fd, scope_name, scope_identity)
        except (OSError, Refusal) as exc:
            raise Refusal(
                f"could not bind newly created child; path={scope_path} "
                f"creation_identity={scope_identity}; leaving it untouched for inspection: {exc}"
            ) from exc
        scope_stat = os.fstat(scope_fd)
        try:
            _verify_scope_permissions(scope_fd)
            _verify_scope_anchor(root_fd, scope_fd, scope_name, scope_identity)
            if (scope_stat.st_dev, scope_stat.st_ino) != scope_identity:
                raise Refusal("held fresh scope differs from its creation-time directory identity")
            if _read_at(scope_fd, "cgroup.type") != "domain":
                raise Refusal("fresh disposable child is not a non-threaded domain cgroup")
            _require_descendant_free(scope_fd, "fresh disposable child before worker creation")
            if _scope_members(scope_fd) or parse_events(_read_at(scope_fd, "cgroup.events")) != 0:
                raise Refusal("fresh disposable child is not empty")

            # UID 65534 is deliberately unprivileged.  It cannot create cgroup
            # descendants or migrate processes through the root-owned controls;
            # the root observer remains the only non-TCB writer.
            worker_uid = 65534
            worker = subprocess.Popen(
                [sys.executable, "-I", "-c", WORKER_SOURCE],
                stdin=subprocess.PIPE,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                close_fds=True,
                user=worker_uid,
                group=worker_uid,
                extra_groups=[],
                env={"LANG": "C", "PYTHONDONTWRITEBYTECODE": "1"},
            )
            try:
                # The worker blocks on stdin until after it has been moved into this fresh scope.
                fd = os.open("cgroup.procs", os.O_WRONLY | os.O_CLOEXEC | os.O_NOFOLLOW, dir_fd=scope_fd)
                try:
                    if os.write(fd, f"{worker.pid}\n".encode("ascii")) <= 0:
                        raise Refusal("failed to place fixed worker in its disposable child")
                finally:
                    os.close(fd)
                if parse_unified_cgroup(Path(f"/proc/{worker.pid}/cgroup").read_text(encoding="ascii")) != scope_path:
                    raise Refusal("host /proc did not confirm exact worker membership in the new scope")
                # Once this write is attempted the worker may have received its
                # gate, so failure cleanup must leave its exact scope for review.
                worker_gate_write_attempted = True
                worker.stdin.write(b"G")
                worker.stdin.flush()
                worker.stdin.close()

                ready_deadline = time.monotonic() + 10.0
                pre = _await_process_set(scope_fd, scope_path, worker.pid, worker_uid, ready_deadline)
                for row in pre:
                    pid = int(row["pid"])
                    pidfd = _open_pidfd(pid)
                    pidfds[pid] = pidfd
                    recheck = _proc_record(pid)
                    if recheck["starttime_ticks"] != row["starttime_ticks"] or recheck["cgroup_path"] != scope_path:
                        raise Refusal(f"PID {pid} changed identity or scope while opening pidfd")
                    row["pidfd_opened"] = True
                if _scope_members(scope_fd) != sorted(int(row["pid"]) for row in pre):
                    raise Refusal("cgroup.procs PID set changed before teardown snapshot completed")
                if parse_events(_read_at(scope_fd, "cgroup.events")) != 1:
                    raise Refusal("pre-teardown cgroup.events did not report populated 1")
                if sha256_file(profile_path) != profile_manifest_digest or sha256_file(Path(profile["runtime_profile_path"])) != runtime_digest:
                    raise Refusal("candidate profile or runtime-profile identity changed before teardown")

                writer_evidence = _verify_writer_exclusion(
                    root_fd, root_path, root_identity, mount_info, [scope_name], require_empty=False
                )
                scope_permissions = _verify_scope_permissions(scope_fd)
                _verify_scope_anchor(root_fd, scope_fd, scope_name, scope_identity)
                _require_descendant_free(scope_fd, "disposable scope immediately before cgroup.kill")
                expected_pids = sorted(int(row["pid"]) for row in pre)
                if _scope_members(scope_fd) != expected_pids:
                    raise Refusal("cgroup.procs changed at the destructive kill gate")
                if parse_events(_read_at(scope_fd, "cgroup.events")) != 1:
                    raise Refusal("cgroup.events changed at the destructive kill gate")

                snapshot_ns = time.monotonic_ns()
                # Repeat the independently derived writer and identity checks at
                # the final gate. No caller flag can assert these conditions.
                writer_evidence = _verify_writer_exclusion(
                    root_fd, root_path, root_identity, mount_info, [scope_name], require_empty=False
                )
                scope_permissions = _verify_scope_permissions(scope_fd)
                _verify_scope_anchor(root_fd, scope_fd, scope_name, scope_identity)
                _require_descendant_free(scope_fd, "disposable scope at cgroup.kill")
                if _scope_members(scope_fd) != expected_pids:
                    raise Refusal("cgroup.procs changed at the final cgroup.kill gate")
                observed_boot_id, namespace_ids = verify_guest_init_identity(
                    profile["guest_boot_id"], profile["guest_init_namespaces"]
                )
                kill_started_ns = time.monotonic_ns()
                _write_bound_scope_kill(root_fd, scope_fd, scope_name, scope_identity)
                deadline = time.monotonic() + profile["teardown_deadline_ms"] / 1000.0
                populated = 1
                while time.monotonic() <= deadline:
                    populated = parse_events(_read_at(scope_fd, "cgroup.events"))
                    if populated == 0 and all(_pidfd_readable(fd) for fd in pidfds.values()):
                        break
                    time.sleep(0.01)
                post = _post_observation(pre, pidfds, scope_path)
                validate_post_observations(pre, post, scope_path)
                if populated != 0 or _scope_members(scope_fd):
                    raise Refusal("cgroup subtree did not reach populated 0 with no direct members")
                _require_descendant_free(scope_fd, "disposable scope after teardown")
                after_ns = time.monotonic_ns()
                validate_witness_timing(snapshot_ns, kill_started_ns, after_ns, profile["teardown_deadline_ms"])
                if sha256_file(profile_path) != profile_manifest_digest or sha256_file(Path(profile["runtime_profile_path"])) != runtime_digest:
                    raise Refusal("candidate profile or runtime-profile identity changed during teardown")
                evidence = {
                    "schema": "tbound-host-proc-cgroup-evidence/v1",
                    "evidence_class": "component-only:cgroup.kill+host-proc+pidfd",
                    "qualification_status": "not-established",
                    "eff_proc_01": "component-observation-only",
                    "eff_proc_02": "not-tested",
                    "eff_proc_03": "not-tested",
                    "g1": False,
                    "h1": "unexecuted",
                    "production_qualification": "open",
                    "profile_id": profile["profile_id"],
                    "profile_manifest_sha256": profile_manifest_digest,
                    "runtime_profile_id": profile["runtime_profile_id"],
                    "runtime_profile_sha256": runtime_digest,
                    "kernel_release": actual_release,
                    "landlock_abi": actual_abi,
                    "guest_boot_id": observed_boot_id,
                    "delegated_root": {**root_identity, "path": str(root_path), "mount_id": mount_info[0]},
                    "scope": {"path": scope_path, "device": scope_stat.st_dev, "inode": scope_stat.st_ino},
                    "observer_namespaces": namespace_ids,
                    "writer_exclusion_initial": initial_writer_evidence,
                    "writer_exclusion_at_kill_gate": writer_evidence,
                    "scope_control_permissions_at_kill_gate": scope_permissions,
                    "teardown_deadline_ms": profile["teardown_deadline_ms"],
                    "pre_teardown_monotonic_ns": snapshot_ns,
                    "kill_started_monotonic_ns": kill_started_ns,
                    "after_monotonic_ns": after_ns,
                    "cgroup_events_populated_after": populated,
                    "pre_teardown_host_proc": pre,
                    "post_teardown_host_proc_pidfd": post,
                    "scope_removed": False,
                    "signing_verified": False,
                    "protocol_freeze": "pending",
                }
                if worker.poll() is None:
                    raise Refusal("worker leader did not exit after its pidfd reported exit")
                _remove_empty_scope(root_fd, scope_fd, scope_name, scope_identity, root_path, root_identity, mount_info)
                evidence["scope_removed"] = True
                return evidence
            except Exception as exc:
                # Do not issue a second kill on an incomplete observation. Preserve the exact
                # child identity for operator review if it remains populated.
                try:
                    remaining = parse_events(_read_at(scope_fd, "cgroup.events"))
                except (OSError, Refusal):
                    remaining = "unknown"
                raise Refusal(
                    f"{exc}; disposable child path={scope_path} "
                    f"device={None if scope_stat is None else scope_stat.st_dev} "
                    f"inode={None if scope_stat is None else scope_stat.st_ino} "
                    f"creation_identity={scope_identity} populated={remaining}; inspect before cleanup"
                ) from exc
            finally:
                for fd in pidfds.values():
                    os.close(fd)
                _close_and_reap_worker(worker, worker_gate_write_attempted)
        finally:
            # Remove only if the newly created child is observed empty. Otherwise leave
            # its exact path for operator inspection; do not sweep the delegated root.
            if scope_fd >= 0:
                try:
                    if (
                        scope_name is not None
                        and scope_identity is not None
                        and parse_events(_read_at(scope_fd, "cgroup.events")) == 0
                        and not _scope_members(scope_fd)
                        and not _cgroup_child_names(scope_fd)
                    ):
                        _remove_empty_scope(
                            root_fd, scope_fd, scope_name, scope_identity, root_path, root_identity, mount_info
                        )
                except (OSError, Refusal):
                    pass
                os.close(scope_fd)
    finally:
        os.close(root_fd)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile-manifest", required=True, help="pinned, operator-maintained candidate guest profile JSON")
    parser.add_argument("--delegated-root", required=True, help="exact absolute delegated cgroup-v2 domain path from that profile")
    parser.add_argument("--confirm-disposable-cgroup-kill", action="store_true", help="authorize killing only this invocation's fixed child-scope fixture")
    args = parser.parse_args()
    try:
        evidence = run_probe(Path(args.profile_manifest), args.delegated_root, args.confirm_disposable_cgroup_kill)
    except (OSError, Refusal, subprocess.SubprocessError) as exc:
        print(json.dumps({
            "schema": "tbound-host-proc-cgroup-evidence/v1",
            "evidence_class": "component-only",
            "qualification_status": "not-established",
            "result": "refused-or-incomplete",
            "error": str(exc),
            "g1": False,
            "h1": "unexecuted",
            "production_qualification": "open",
            "signing_verified": False,
            "protocol_freeze": "pending",
        }, sort_keys=True))
        return 2
    print(json.dumps(evidence, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
