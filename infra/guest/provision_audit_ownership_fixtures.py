#!/usr/bin/env python3
"""Create fixed, empty ownership-rejection fixtures for the TBound audit test.

This one-time helper has no path or identity arguments. It requires root and
uses descriptor-relative, no-follow operations below a fixed trusted directory.
It never runs project code or removes an existing or partial fixture tree.
"""

from __future__ import annotations

import errno
import json
import os
import stat
import sys
from typing import NamedTuple

try:
    import pwd
except ImportError:  # pragma: no cover - the fixed CLI is Linux-only
    pwd = None

TRIAL_ACCOUNT = "tboundadmin"
FIXTURE_ROOT = "/var/lib/tbound/audit-ownership-fixtures"
FIXTURE_ROOT_COMPONENTS = ("var", "lib", "tbound", "audit-ownership-fixtures")
FOREIGN_UID = 65534
FOREIGN_GID = 65534
FOREIGN_DIRECTORY = "foreign-dir"
FILE_PARENT = "file-parent"
FOREIGN_JOURNAL = "journal.jsonl"
INSECURE_MODE_JOURNAL = "owned-insecure-mode.jsonl"

class OpenFlags(NamedTuple):
    read_only: int
    write_only: int
    directory: int
    cloexec: int
    nofollow: int
    create: int
    exclusive: int


def _linux_flags() -> OpenFlags:
    try:
        values = OpenFlags(
            os.O_RDONLY,
            os.O_WRONLY,
            os.O_DIRECTORY,
            os.O_CLOEXEC,
            os.O_NOFOLLOW,
            os.O_CREAT,
            os.O_EXCL,
        )
    except AttributeError as exc:
        raise ProvisionError("required Linux open flags are unavailable") from exc
    if any(not isinstance(value, int) for value in values) or any(value == 0 for value in values[1:]):
        raise ProvisionError("required Linux open flags are unavailable")
    return values


def _dir_flags(flags: OpenFlags) -> int:
    return flags.read_only | flags.directory | flags.cloexec | flags.nofollow


def _file_flags(flags: OpenFlags) -> int:
    return flags.write_only | flags.create | flags.exclusive | flags.cloexec | flags.nofollow


class ProvisionError(RuntimeError):
    """The fixed fixture location or metadata failed a safety check."""


def _metadata(fd: int, ops=os):
    try:
        names = ops.listxattr(fd)
    except (AttributeError, NotImplementedError, OSError, TypeError) as exc:
        raise ProvisionError("cannot verify ACLs and extended attributes") from exc
    if names:
        raise ProvisionError("unexpected ACL or extended attribute")
    try:
        return ops.fstat(fd)
    except OSError as exc:
        raise ProvisionError("cannot inspect an opened fixture entry") from exc


def _trusted_directory(fd: int, label: str, *, owner: int, ops=os):
    info = _metadata(fd, ops)
    if not stat.S_ISDIR(info.st_mode):
        raise ProvisionError(f"{label} is not a directory")
    if info.st_uid != owner:
        raise ProvisionError(f"{label} is not owned by the trusted root owner")
    if stat.S_IMODE(info.st_mode) & 0o022:
        raise ProvisionError(f"{label} is writable by group or other")
    return info


def open_fixture_root(*, ops=os, trusted_uid: int = 0, flags: OpenFlags | None = None) -> int:
    """Open the fixed fixture root without following any path component.

    Only the two dedicated TBound components may be created, as root-owned
    0755 directories. Existing components are verified and never modified.
    """
    flags = _linux_flags() if flags is None else flags
    dir_flags = _dir_flags(flags)
    try:
        current = ops.open("/", dir_flags)
    except OSError as exc:
        raise ProvisionError("cannot open filesystem root") from exc

    try:
        _trusted_directory(current, "/", owner=trusted_uid, ops=ops)
        for index, component in enumerate(FIXTURE_ROOT_COMPONENTS):
            child = None
            try:
                try:
                    child = ops.open(component, dir_flags, dir_fd=current)
                except FileNotFoundError:
                    if component not in ("tbound", "audit-ownership-fixtures"):
                        raise ProvisionError(f"required trusted ancestor {component!r} is missing")
                    ops.mkdir(component, 0o755, dir_fd=current)
                    child = ops.open(component, dir_flags, dir_fd=current)
                    _trusted_directory(child, component, owner=trusted_uid, ops=ops)
                    ops.fchown(child, trusted_uid, 0)
                    ops.fchmod(child, 0o755)
                label = "/" + "/".join(FIXTURE_ROOT_COMPONENTS[: index + 1])
                _trusted_directory(child, label, owner=trusted_uid, ops=ops)
            except (OSError, ProvisionError) as exc:
                if child is not None:
                    ops.close(child)
                if isinstance(exc, ProvisionError):
                    raise
                if exc.errno in (errno.ELOOP, errno.ENOTDIR):
                    raise ProvisionError(f"trusted ancestor {component!r} is a link or non-directory") from exc
                raise ProvisionError(f"cannot open trusted ancestor {component!r}") from exc
            previous = current
            current = child
            ops.close(previous)
        return current
    except BaseException:
        ops.close(current)
        raise


def _fixture_base(fd: int, *, ops=os):
    info = _trusted_directory(fd, FIXTURE_ROOT, owner=0, ops=ops)
    if info.st_gid != 0 or stat.S_IMODE(info.st_mode) != 0o755:
        raise ProvisionError("fixed fixture base must be root:root mode 0755 with no special bits")
    return info

def _require_exact(fd: int, label: str, *, kind: str, uid: int, gid: int, mode: int, size: int | None = None, ops=os):
    info = _metadata(fd, ops)
    type_ok = stat.S_ISDIR(info.st_mode) if kind == "directory" else stat.S_ISREG(info.st_mode)
    if not type_ok:
        raise ProvisionError(f"{label} has the wrong file type")
    if (info.st_uid, info.st_gid) != (uid, gid):
        raise ProvisionError(f"{label} has the wrong owner")
    if stat.S_IMODE(info.st_mode) != mode:
        raise ProvisionError(f"{label} has the wrong permission mode")
    if size is not None and info.st_size != size:
        raise ProvisionError(f"{label} is not empty")
    if kind == "file" and info.st_nlink != 1:
        raise ProvisionError(f"{label} does not have exactly one hard link")
    return info


def _make_directory(parent_fd: int, name: str, *, trusted_uid: int, flags: OpenFlags, ops=os) -> int:
    ops.mkdir(name, 0o700, dir_fd=parent_fd)
    fd = ops.open(name, _dir_flags(flags), dir_fd=parent_fd)
    try:
        info = _metadata(fd, ops)
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != trusted_uid:
            raise ProvisionError(f"new directory {name!r} has unexpected type or owner")
        ops.fchmod(fd, 0o700)
        _require_exact(fd, name, kind="directory", uid=trusted_uid, gid=info.st_gid, mode=0o700, ops=ops)
        return fd
    except BaseException:
        ops.close(fd)
        raise

def _make_empty_file(parent_fd: int, name: str, *, uid: int, gid: int, mode: int, trusted_uid: int, flags: OpenFlags, ops=os) -> int:
    fd = ops.open(name, _file_flags(flags), 0o600, dir_fd=parent_fd)
    try:
        info = _metadata(fd, ops)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != trusted_uid or info.st_size != 0 or info.st_nlink != 1:
            raise ProvisionError(f"new file {name!r} has unexpected type, owner, size, or link count")
        ops.fchown(fd, uid, gid)
        ops.fchmod(fd, mode)
        _require_exact(fd, name, kind="file", uid=uid, gid=gid, mode=mode, size=0, ops=ops)
        return fd
    except BaseException:
        ops.close(fd)
        raise


def _set_directory_owner(fd: int, label: str, *, uid: int, gid: int, mode: int, ops=os) -> None:
    ops.fchown(fd, uid, gid)
    ops.fchmod(fd, mode)
    _require_exact(fd, label, kind="directory", uid=uid, gid=gid, mode=mode, ops=ops)


def validate_trial_identity(uid: int, gid: int) -> None:
    if uid < 1000 or uid in (0, FOREIGN_UID):
        raise ProvisionError("trial account UID must be at least 1000 and distinct from root and nobody")
    if gid <= 0 or gid == FOREIGN_GID:
        raise ProvisionError("trial account primary GID must be non-root and distinct from nobody")


def provision_fixtures(root_fd: int, uid: int, gid: int, *, ops=os, trusted_uid: int = 0, flags: OpenFlags | None = None) -> dict:
    """Create exactly the three fixed entries under a newly created UID directory."""
    flags = _linux_flags() if flags is None else flags
    _fixture_base(root_fd, ops=ops)
    validate_trial_identity(uid, gid)
    uid_name = str(uid)
    # mkdir is exclusive: any preexisting tree, even an empty one, is a hard stop.
    trial_fd = _make_directory(root_fd, uid_name, trusted_uid=trusted_uid, flags=flags, ops=ops)
    foreign_dir_fd = None
    file_parent_fd = None
    foreign_file_fd = None
    insecure_file_fd = None
    try:
        foreign_dir_fd = _make_directory(trial_fd, FOREIGN_DIRECTORY, trusted_uid=trusted_uid, flags=flags, ops=ops)
        file_parent_fd = _make_directory(trial_fd, FILE_PARENT, trusted_uid=trusted_uid, flags=flags, ops=ops)
        foreign_file_fd = _make_empty_file(
            file_parent_fd,
            FOREIGN_JOURNAL,
            uid=FOREIGN_UID,
            gid=gid,
            mode=0o660,
            trusted_uid=trusted_uid,
            flags=flags,
            ops=ops,
        )
        insecure_file_fd = _make_empty_file(
            file_parent_fd,
            INSECURE_MODE_JOURNAL,
            uid=uid,
            gid=gid,
            mode=0o660,
            trusted_uid=trusted_uid,
            flags=flags,
            ops=ops,
        )
        _set_directory_owner(foreign_dir_fd, FOREIGN_DIRECTORY, uid=FOREIGN_UID, gid=FOREIGN_GID, mode=0o755, ops=ops)
        _set_directory_owner(file_parent_fd, FILE_PARENT, uid=uid, gid=gid, mode=0o700, ops=ops)
        # Make the trial-owned UID directory last, after all privileged changes beneath it.
        _set_directory_owner(trial_fd, uid_name, uid=uid, gid=gid, mode=0o700, ops=ops)
        return {
            "status": "created",
            "root": f"{FIXTURE_ROOT}/{uid_name}",
            "trial_uid": uid,
            "trial_gid": gid,
            "foreign_directory": {"path": FOREIGN_DIRECTORY, "uid": FOREIGN_UID, "gid": FOREIGN_GID, "mode": "0755"},
            "foreign_file": {"path": f"{FILE_PARENT}/{FOREIGN_JOURNAL}", "uid": FOREIGN_UID, "gid": gid, "mode": "0660", "size": 0, "nlink": 1},
            "insecure_mode_file": {"path": f"{FILE_PARENT}/{INSECURE_MODE_JOURNAL}", "uid": uid, "gid": gid, "mode": "0660", "size": 0, "nlink": 1},
        }
    finally:
        for fd in (insecure_file_fd, foreign_file_fd, file_parent_fd, foreign_dir_fd, trial_fd):
            if fd is not None:
                ops.close(fd)


def main(arguments: list[str] | None = None) -> int:
    args = sys.argv[1:] if arguments is None else arguments
    if args:
        print("This fixed-path provisioner accepts no arguments.", file=sys.stderr)
        return 2
    if not sys.platform.startswith("linux") or pwd is None:
        print("This provisioner is supported only on Linux.", file=sys.stderr)
        return 2
    if os.geteuid() != 0:
        print("Run this fixed one-time provisioner as root after review.", file=sys.stderr)
        return 2

    try:
        account = pwd.getpwnam(TRIAL_ACCOUNT)
        validate_trial_identity(account.pw_uid, account.pw_gid)
        flags = _linux_flags()
        root_fd = open_fixture_root(flags=flags)
        try:
            result = provision_fixtures(root_fd, account.pw_uid, account.pw_gid, flags=flags)
        finally:
            os.close(root_fd)
    except (KeyError, OSError, ProvisionError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        print("Stop and inspect the fixed fixture path; no cleanup was attempted.", file=sys.stderr)
        return 1

    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())