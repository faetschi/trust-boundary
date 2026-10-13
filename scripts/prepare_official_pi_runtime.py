#!/usr/bin/env python3
"""Stage the installed official Pi CLI package and TBound RPC extension.

This is a bounded, unprivileged copy operation. It never installs packages,
downloads artifacts, signs a profile, or changes the production launch gate.
"""

from __future__ import annotations

import argparse
import ctypes
import errno
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
from typing import Any


PI_PACKAGE = "@earendil-works/pi-coding-agent"
PI_VERSION = "0.87.1"
NODE_VERSION = "v24.21.0"
PI_ENTRY_RELATIVE = Path("dist/bundle/cli.js")
CLI_ENTRY_BUNDLE_PATH = "cli/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js"
EXTENSION_ENTRY_BUNDLE_PATH = "extension/src/tbound-cli-extension.ts"
EXTENSION_FILES = (
    "tbound-cli-extension.ts",
    "broker-provider.ts",
    "proxy-tools.ts",
    "ipc-transport.ts",
)
DEFAULT_MAX_FILES = 100_000
DEFAULT_MAX_BYTES = 1_073_741_824
HASH_CHUNK_BYTES = 1024 * 1024


class PreparationError(RuntimeError):
    pass


def fail(message: str) -> None:
    raise PreparationError(message)


def within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def sha256_file(path: Path, *, nofollow: bool = False) -> tuple[str, int]:
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0)
    if nofollow:
        flags |= getattr(os, "O_NOFOLLOW", 0)
    fd = os.open(path, flags)
    digest = hashlib.sha256()
    size = 0
    try:
        metadata = os.fstat(fd)
        if not stat.S_ISREG(metadata.st_mode):
            fail(f"expected a regular file: {path}")
        while True:
            chunk = os.read(fd, HASH_CHUNK_BYTES)
            if not chunk:
                break
            size += len(chunk)
            digest.update(chunk)
    finally:
        os.close(fd)
    return digest.hexdigest(), size


def safe_relative(path: Path) -> str:
    value = path.as_posix()
    if path.is_absolute() or value in ("", ".") or ".." in path.parts:
        fail(f"unsafe relative path in runtime closure: {value!r}")
    return value


def resolve_package_symlink(path: Path, root: Path, root_device: int) -> tuple[Path, str]:
    try:
        resolved = path.resolve(strict=True)
    except (OSError, RuntimeError) as exc:
        fail(f"cannot resolve runtime symlink {path}: {exc}")
    if not within(resolved, root):
        fail(f"runtime symlink escapes official Pi package: {path} -> {resolved}")
    metadata = resolved.lstat()
    if metadata.st_dev != root_device:
        fail(f"runtime symlink crosses a filesystem boundary: {path}")
    if not stat.S_ISREG(metadata.st_mode):
        fail(f"runtime symlink target is not a regular file: {path}")
    target = os.path.relpath(resolved, path.parent)
    return resolved, Path(target).as_posix()


def canonical_symlink_target(path: Path, root: Path, root_device: int) -> str:
    return resolve_package_symlink(path, root, root_device)[1]


def scan_tree(root: Path, *, max_files: int, max_bytes: int) -> list[dict[str, Any]]:
    """Hash regular files and directories; identify safe file links for materialization."""
    root_metadata = root.stat()
    root_device = root_metadata.st_dev
    entries: list[dict[str, Any]] = []
    total_bytes = 0

    for directory, dirnames, filenames in os.walk(root, topdown=True, followlinks=False):
        base = Path(directory)
        kept_dirs: list[str] = []
        for name in sorted(dirnames):
            path = base / name
            relative = safe_relative(path.relative_to(root))
            metadata = path.lstat()
            if stat.S_ISLNK(metadata.st_mode):
                target = canonical_symlink_target(path, root, root_device)
                entries.append({"path": relative, "kind": "symlink", "target": target})
            elif stat.S_ISDIR(metadata.st_mode):
                if metadata.st_dev != root_device:
                    fail(f"nested mount/filesystem in official Pi package: {path}")
                entries.append({"path": relative, "kind": "directory"})
                kept_dirs.append(name)
            else:
                fail(f"unexpected non-directory object in official Pi package: {path}")
        dirnames[:] = kept_dirs

        for name in sorted(filenames):
            path = base / name
            relative = safe_relative(path.relative_to(root))
            metadata = path.lstat()
            if stat.S_ISLNK(metadata.st_mode):
                resolved, target = resolve_package_symlink(path, root, root_device)
                target_metadata = resolved.lstat()
                digest, size = sha256_file(resolved, nofollow=True)
                total_bytes += size
                if total_bytes > max_bytes:
                    fail(f"official Pi package exceeds the {max_bytes}-byte closure limit")
                entries.append({
                    "path": relative,
                    "kind": "symlink",
                    "target": target,
                    "sha256": digest,
                    "size": size,
                    "mode": 0o755 if target_metadata.st_mode & 0o111 else 0o644,
                })
                continue
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_dev != root_device:
                fail(f"unexpected non-regular object in official Pi package: {path}")
            digest, size = sha256_file(path, nofollow=True)
            total_bytes += size
            if total_bytes > max_bytes:
                fail(f"official Pi package exceeds the {max_bytes}-byte closure limit")
            entries.append({
                "path": relative,
                "kind": "file",
                "sha256": digest,
                "size": size,
                "mode": 0o755 if metadata.st_mode & 0o111 else 0o644,
            })
            if len(entries) > max_files:
                fail(f"official Pi package exceeds the {max_files}-entry closure limit")

    entries.sort(key=lambda item: item["path"])
    if len(entries) > max_files:
        fail(f"official Pi package exceeds the {max_files}-entry closure limit")
    return entries


def read_package_manifest(path: Path, expected_name: str, expected_version: str) -> dict[str, Any]:
    try:
        metadata = path.lstat()
    except OSError as exc:
        fail(f"cannot inspect package manifest {path}: {exc}")
    if not stat.S_ISREG(metadata.st_mode):
        fail(f"package manifest is not a regular file: {path}")
    try:
        raw = path.read_bytes()
        if len(raw) > 4 * 1024 * 1024:
            fail(f"package manifest is too large: {path}")
        value = json.loads(raw)
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"cannot parse package manifest {path}: {exc}")
    if not isinstance(value, dict) or value.get("name") != expected_name or value.get("version") != expected_version:
        fail(f"unexpected package identity/version in {path}; expected {expected_name}@{expected_version}")
    return value


def resolve_dependency(issuer: Path, name: str, package_root: Path) -> tuple[Path, Path] | None:
    if not name or name.startswith("/") or "\\" in name or any(part in ("", ".", "..") for part in name.split("/")):
        fail(f"unsafe npm dependency name: {name!r}")
    cursor = issuer
    while True:
        candidate = cursor / "node_modules" / Path(*name.split("/"))
        if candidate.exists() or candidate.is_symlink():
            try:
                resolved = candidate.resolve(strict=True)
            except (OSError, RuntimeError) as exc:
                fail(f"cannot resolve installed dependency {name!r} from {issuer}: {exc}")
            if not within(resolved, package_root):
                fail(f"installed dependency {name!r} is sibling-hoisted outside the Pi package closure")
            if not resolved.is_dir():
                fail(f"installed dependency {name!r} is not a package directory")
            return candidate, resolved
        parent = cursor.parent
        if parent == cursor:
            return None
        cursor = parent


def dependency_lock_entry(
    package_root: Path,
    package_locations: dict[str, Any],
    candidate: Path,
    resolved: Path,
    name: str,
) -> tuple[dict[str, Any], str]:
    """Find the lock entry for this exact installed location, following npm links."""
    location = safe_relative(candidate.relative_to(package_root))
    entry = package_locations.get(location)
    if not isinstance(entry, dict):
        fail(f"installed dependency {name!r} at {location!r} is missing from Pi npm-shrinkwrap")

    if entry.get("link") is not True:
        if candidate.is_symlink():
            fail(f"installed dependency {name!r} is a symlink not recorded as a shrinkwrap link")
        return entry, location

    if not candidate.is_symlink():
        fail(f"installed dependency {name!r} is marked as a shrinkwrap link but is not a symlink")
    visited = {location}
    while entry.get("link") is True:
        target = entry.get("resolved")
        if (
            not isinstance(target, str)
            or not target
            or target.startswith("/")
            or "\\" in target
            or any(part in ("", ".", "..") for part in target.split("/"))
        ):
            fail(f"installed dependency {name!r} has an unsafe shrinkwrap link target")
        if target in visited:
            fail(f"installed dependency {name!r} has a cyclic shrinkwrap link")
        visited.add(target)
        try:
            target_path = (package_root / Path(*target.split("/"))).resolve(strict=True)
        except (OSError, RuntimeError) as exc:
            fail(f"cannot resolve shrinkwrap link for installed dependency {name!r}: {exc}")
        if not within(target_path, package_root) or target_path != resolved:
            fail(f"installed dependency {name!r} does not match its shrinkwrap link target")
        entry = package_locations.get(target)
        if not isinstance(entry, dict):
            fail(f"installed dependency {name!r} link target {target!r} is missing from Pi npm-shrinkwrap")
        location = target
    return entry, location


def validate_dependency_closure(package_root: Path, pi_manifest: dict[str, Any], lock: dict[str, Any]) -> None:
    """Require every installed runtime dependency to match its exact lock location."""
    package_locations = lock.get("packages")
    if not isinstance(package_locations, dict):
        fail("Pi npm-shrinkwrap is missing its package-location map")
    queue: list[tuple[Path, dict[str, Any]]] = [(package_root, pi_manifest)]
    seen: set[Path] = set()
    while queue:
        issuer, manifest = queue.pop()
        issuer = issuer.resolve(strict=True)
        if issuer in seen:
            continue
        seen.add(issuer)

        dependencies = manifest.get("dependencies", {})
        optional = manifest.get("optionalDependencies", {})
        peers = manifest.get("peerDependencies", {})
        peer_meta = manifest.get("peerDependenciesMeta", {})
        for section in (dependencies, optional, peers):
            if section is not None and not isinstance(section, dict):
                fail(f"malformed dependency metadata in {issuer / 'package.json'}")

        required: dict[str, str] = {}
        required.update(dependencies or {})
        required.update(optional or {})
        required.update(peers or {})
        for name in sorted(required):
            optional_peer = bool((peer_meta or {}).get(name, {}).get("optional", False))
            resolution = resolve_dependency(issuer, name, package_root)
            if resolution is None:
                if name in (optional or {}) or optional_peer:
                    continue
                fail(f"installed Pi closure is missing dependency {name!r} declared by {issuer}")
            candidate, resolved = resolution
            manifest_path = resolved / "package.json"
            try:
                child = json.loads(manifest_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                fail(f"cannot read installed dependency manifest {manifest_path}: {exc}")
            if not isinstance(child, dict) or child.get("name") != name:
                fail(f"installed dependency identity does not match {name!r}: {manifest_path}")
            lock_entry, lock_location = dependency_lock_entry(
                package_root, package_locations, candidate, resolved, name
            )
            installed_version = child.get("version")
            locked_version = lock_entry.get("version")
            if (
                not isinstance(installed_version, str)
                or not isinstance(locked_version, str)
                or installed_version != locked_version
            ):
                fail(
                    f"installed dependency version does not match Pi npm-shrinkwrap at {lock_location!r}: "
                    f"installed {installed_version!r}, lock {locked_version!r}"
                )
            queue.append((resolved, child))


def canonical_entry(pi_path: Path) -> tuple[Path, Path, dict[str, Any]]:
    try:
        entry = pi_path.expanduser().resolve(strict=True)
    except OSError as exc:
        fail(f"cannot resolve installed Pi CLI path {pi_path}: {exc}")
    if not entry.is_file() or entry.name != "cli.js" or entry.relative_to(entry.parents[2]) != PI_ENTRY_RELATIVE:
        fail("Pi path must resolve to the official dist/bundle/cli.js entry (wrappers are refused)")
    package_root = entry.parents[2].resolve(strict=True)
    if package_root.parent.name != "@earendil-works" or package_root.name != "pi-coding-agent" or package_root.parent.parent.name != "node_modules":
        fail("canonical Pi CLI entry is outside the expected official npm package layout")
    manifest = read_package_manifest(package_root / "package.json", PI_PACKAGE, PI_VERSION)
    lock_path = package_root / "npm-shrinkwrap.json"
    try:
        lock = json.loads(lock_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"official Pi package does not contain a readable npm-shrinkwrap.json: {exc}")
    if not isinstance(lock, dict) or lock.get("name") != PI_PACKAGE or lock.get("version") != PI_VERSION:
        fail("Pi npm-shrinkwrap identity/version does not match the installed CLI")
    if lock.get("lockfileVersion") not in (2, 3):
        fail("Pi npm-shrinkwrap has an unsupported lockfile version")
    validate_dependency_closure(package_root, manifest, lock)
    return entry, package_root, manifest


def make_clean_environment(home: Path, node_path: Path) -> dict[str, str]:
    return {
        "HOME": str(home),
        "TMPDIR": str(home),
        "PATH": str(node_path.parent),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PI_OFFLINE": "1",
        "NO_COLOR": "1",
    }


def run_version_check(node_path: Path, entry: Path, temp_home: Path, *, node_only: bool = False) -> str:
    command = [str(node_path), "--version"] if node_only else [str(node_path), str(entry), "--version"]
    try:
        result = subprocess.run(
            command,
            cwd=temp_home,
            env=make_clean_environment(temp_home, node_path),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=15,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        fail(f"version check could not complete for {command[1]}: {exc}")
    if result.returncode != 0:
        stderr = result.stderr[:2048].decode("utf-8", "replace").strip()
        fail(f"version check failed for {command[1]} (exit {result.returncode}): {stderr}")
    stdout = result.stdout[:4097].decode("utf-8", "replace").strip()
    if len(result.stdout) > 4096:
        fail("version check output exceeded 4096 bytes")
    return stdout


def extension_sources(extension_dir: Path, extension_package: Path) -> list[tuple[str, Path]]:
    sources: list[tuple[str, Path]] = []
    for name in EXTENSION_FILES:
        source = extension_dir / name
        try:
            metadata = source.lstat()
        except OSError as exc:
            fail(f"missing extension source {source}: {exc}")
        if not stat.S_ISREG(metadata.st_mode):
            fail(f"extension source must be a regular, non-symlink file: {source}")
        sources.append((f"extension/src/{name}", source))
    try:
        package_metadata = extension_package.lstat()
        package_json = json.loads(extension_package.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        fail(f"cannot read runtime extension package metadata {extension_package}: {exc}")
    if not stat.S_ISREG(package_metadata.st_mode) or not isinstance(package_json, dict) or package_json.get("type") != "module":
        fail("runtime extension package metadata must be a regular package.json with type=module")
    sources.append(("extension/package.json", extension_package))
    return sources


def copy_regular_file(source: Path, destination: Path, expected: dict[str, Any]) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    digest = hashlib.sha256()
    size = 0
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        source_fd = os.open(source, flags)
    except OSError as exc:
        fail(f"cannot safely open staged source file {source}: {exc}")
    try:
        metadata = os.fstat(source_fd)
        if not stat.S_ISREG(metadata.st_mode):
            fail(f"source changed to a non-regular file during staging: {source}")
        with os.fdopen(source_fd, "rb", closefd=False) as input_file:
            with destination.open("xb") as output_file:
                while True:
                    chunk = input_file.read(HASH_CHUNK_BYTES)
                    if not chunk:
                        break
                    digest.update(chunk)
                    size += len(chunk)
                    output_file.write(chunk)
        os.chmod(destination, int(expected.get("mode", 0o644)))
        os.utime(destination, (0, 0), follow_symlinks=False)
    finally:
        os.close(source_fd)
    if digest.hexdigest() != expected.get("sha256") or size != expected.get("size"):
        fail(f"source file changed while it was copied: {source}")


def copy_package_tree(package_root: Path, staged_package: Path, entries: list[dict[str, Any]]) -> list[dict[str, Any]]:
    output_entries: list[dict[str, Any]] = []
    staged_package.mkdir(parents=True, mode=0o755)
    for entry in entries:
        relative = Path(entry["path"])
        source = package_root / relative
        destination = staged_package / relative
        if entry["kind"] == "directory":
            destination.mkdir(parents=True, exist_ok=True, mode=0o755)
            continue
        if entry["kind"] == "file":
            copy_regular_file(source, destination, entry)
            output_entries.append({"path": f"cli/node_modules/@earendil-works/pi-coding-agent/{entry['path']}", **{key: entry[key] for key in ("kind", "sha256", "size", "mode")}})
            continue
        if entry["kind"] == "symlink":
            # The immutable production verifier rejects links, so copy the
            # approved in-package file target into this path as a regular file.
            resolved, current_target = resolve_package_symlink(source, package_root, package_root.stat().st_dev)
            if current_target != entry["target"]:
                fail(f"source symlink changed while it was copied: {source}")
            copy_regular_file(resolved, destination, entry)
            output_entries.append({
                "path": f"cli/node_modules/@earendil-works/pi-coding-agent/{entry['path']}",
                "kind": "file",
                "sha256": entry["sha256"],
                "size": entry["size"],
                "mode": entry["mode"],
            })
    return output_entries


def stage_extensions(sources: list[tuple[str, Path]]) -> list[dict[str, Any]]:
    output: list[dict[str, Any]] = []
    for relative, source in sources:
        digest, size = sha256_file(source, nofollow=True)
        mode = 0o755 if source.stat().st_mode & 0o111 else 0o644
        output.append({"path": relative, "kind": "file", "sha256": digest, "size": size, "mode": mode})
    return output


def copy_extension_sources(sources: list[tuple[str, Path]], stage_root: Path, manifest_entries: list[dict[str, Any]]) -> None:
    expected = {entry["path"]: entry for entry in manifest_entries}
    for relative, source in sources:
        copy_regular_file(source, stage_root / relative, expected[relative])


def verified_staged_lock_identity(staged_package: Path, package_entries: list[dict[str, Any]]) -> tuple[str, int]:
    """Hash the staged shrinkwrap and bind it to its verified closure entry."""
    manifest_path = "cli/node_modules/@earendil-works/pi-coding-agent/npm-shrinkwrap.json"
    matches = [entry for entry in package_entries if entry.get("path") == manifest_path]
    if len(matches) != 1 or matches[0].get("kind") != "file":
        fail("verified package entries do not contain exactly one regular Pi npm-shrinkwrap")
    digest, size = sha256_file(staged_package / "npm-shrinkwrap.json", nofollow=True)
    if digest != matches[0].get("sha256") or size != matches[0].get("size"):
        fail("staged Pi npm-shrinkwrap differs from its verified closure entry")
    return digest, size


def canonical_json(value: Any) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")


def atomic_rename_noreplace(source: Path, destination: Path) -> None:
    """Linux renameat2(RENAME_NOREPLACE), with no unsafe fallback."""
    try:
        renameat2 = ctypes.CDLL(None, use_errno=True).renameat2
    except AttributeError:
        fail("libc renameat2 is unavailable; cannot publish staged output without overwrite risk")
    renameat2.argtypes = (ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint)
    renameat2.restype = ctypes.c_int
    at_fdcwd = -100
    rename_noreplace = 1
    result = renameat2(at_fdcwd, os.fsencode(source), at_fdcwd, os.fsencode(destination), rename_noreplace)
    if result != 0:
        code = ctypes.get_errno()
        if code == errno.EEXIST:
            fail(f"output already exists; refusing to overwrite: {destination}")
        raise OSError(code, os.strerror(code), str(destination))


def prepare(args: argparse.Namespace) -> dict[str, Any]:
    if not sys.platform.startswith("linux"):
        fail("official Pi closure preparation is Linux-only")
    if os.geteuid() == 0:
        fail("run closure preparation as the unprivileged CLI owner; root-owned installation is a separate reviewed step")
    if args.max_files <= 0 or args.max_bytes <= 0:
        fail("closure limits must be positive")

    node_path = args.node.expanduser().resolve(strict=True)
    if not node_path.is_file():
        fail(f"explicit Node path is not a regular file: {node_path}")
    entry, package_root, pi_manifest = canonical_entry(args.pi)
    if not (package_root / "npm-shrinkwrap.json").is_file():
        fail("the official Pi package must include its upstream npm-shrinkwrap.json")

    output_parent = args.out.expanduser().parent.resolve(strict=True)
    output_path = output_parent / args.out.name
    if os.path.lexists(output_path):
        fail(f"output already exists; refusing to overwrite: {output_path}")
    if within(output_path, package_root) or within(package_root, output_path):
        fail("output path must be outside the installed Pi package directory")

    extension_dir = args.extension_dir.expanduser().resolve(strict=True)
    extension_package = args.extension_package.expanduser().resolve(strict=True)
    if within(output_path, extension_dir) or within(extension_dir, output_path):
        fail("output path must be outside the extension source directory")
    ext_sources = extension_sources(extension_dir, extension_package)

    node_version_expected = args.node_version
    pi_version_expected = args.pi_version
    if node_version_expected != NODE_VERSION or pi_version_expected != PI_VERSION:
        fail(f"this preparation schema is pinned to Node {NODE_VERSION} and Pi {PI_VERSION}")

    temp_parent = output_parent
    temp_dir = Path(tempfile.mkdtemp(prefix=f".{output_path.name}.prepare-", dir=temp_parent))
    os.chmod(temp_dir, 0o700)
    final_output = False
    try:
        with tempfile.TemporaryDirectory(prefix="tbound-pi-version-", dir=os.environ.get("TMPDIR") or None) as version_home_name:
            version_home = Path(version_home_name)
            os.chmod(version_home, 0o700)
            node_version = run_version_check(node_path, entry, version_home, node_only=True)
            if node_version != node_version_expected:
                fail(f"Node version mismatch: expected {node_version_expected}, found {node_version}")
            source_cli_version = run_version_check(node_path, entry, version_home)
            if source_cli_version != pi_version_expected:
                fail(f"official Pi CLI version mismatch: expected {pi_version_expected}, found {source_cli_version!r}")

        package_before = scan_tree(package_root, max_files=args.max_files, max_bytes=args.max_bytes)
        extension_before = stage_extensions(ext_sources)
        staged_package = temp_dir / "cli/node_modules/@earendil-works/pi-coding-agent"
        package_entries = copy_package_tree(package_root, staged_package, package_before)
        copy_extension_sources(ext_sources, temp_dir, extension_before)

        package_after = scan_tree(package_root, max_files=args.max_files, max_bytes=args.max_bytes)
        extension_after = stage_extensions(ext_sources)
        if package_before != package_after:
            fail("installed official Pi package changed during closure preparation")
        if extension_before != extension_after:
            fail("TBound extension sources changed during closure preparation")

        manifest_entries = sorted(package_entries + extension_before, key=lambda item: item["path"])
        if len(manifest_entries) > args.max_files:
            fail(f"runtime closure exceeds the {args.max_files}-entry limit")
        total_bytes = sum(int(item.get("size", 0)) for item in manifest_entries)
        if total_bytes > args.max_bytes:
            fail(f"runtime closure exceeds the {args.max_bytes}-byte limit")

        lock_digest, lock_size = verified_staged_lock_identity(staged_package, package_entries)
        manifest = {
            "schema_version": "tbound-official-pi-runtime-closure/v1",
            "pi": {
                "package": PI_PACKAGE,
                "version": PI_VERSION,
                "cli_entry": CLI_ENTRY_BUNDLE_PATH,
                "dependency_lock": "cli/node_modules/@earendil-works/pi-coding-agent/npm-shrinkwrap.json",
                "dependency_lock_sha256": lock_digest,
                "dependency_lock_size": lock_size,
            },
            "node_version_checked": NODE_VERSION,
            "extension": {
                "entry": EXTENSION_ENTRY_BUNDLE_PATH,
                "package_metadata": "extension/package.json",
            },
            "entries": manifest_entries,
            "limits": {"max_entries": args.max_files, "max_bytes": args.max_bytes},
            "production_requirements": [
                "prepared output is user-owned and unsigned",
                "operator must separately verify and install the complete bundle root-owned and read-only at /var/lib/tbound/runtime",
                "operator must separately freeze the runtime bundle digest in the signed host profile",
                "production launch remains fail-closed until profile, host, source, containment and settlement qualification pass",
            ],
        }
        manifest_path = temp_dir / "closure-manifest.json"
        manifest_bytes = canonical_json(manifest)
        manifest_path.write_bytes(manifest_bytes)
        os.chmod(manifest_path, 0o644)
        os.utime(manifest_path, (0, 0))
        manifest_digest = hashlib.sha256(manifest_bytes).hexdigest()
        sidecar = temp_dir / "closure-manifest.sha256"
        sidecar.write_text(f"{manifest_digest}  closure-manifest.json\n", encoding="ascii")
        os.chmod(sidecar, 0o644)
        os.utime(sidecar, (0, 0))

        # Verify the staged CLI from the staged location with the explicit Node
        # binary and a fresh, empty private HOME before publishing the output.
        staged_cli = temp_dir / CLI_ENTRY_BUNDLE_PATH
        with tempfile.TemporaryDirectory(prefix="tbound-pi-staged-check-", dir=os.environ.get("TMPDIR") or None) as check_home_name:
            check_home = Path(check_home_name)
            os.chmod(check_home, 0o700)
            staged_version = run_version_check(node_path, staged_cli, check_home)
        if staged_version != PI_VERSION:
            fail(f"staged official Pi CLI version mismatch: expected {PI_VERSION}, found {staged_version!r}")

        atomic_rename_noreplace(temp_dir, output_path)
        final_output = True
        return {
            "status": "prepared-user-owned-unsealed-not-admitted",
            "output": str(output_path),
            "canonical_pi_entry": str(entry),
            "canonical_pi_package_root": str(package_root),
            "canonical_node": str(node_path),
            "node_version": node_version,
            "pi_version": source_cli_version,
            "manifest": str(output_path / "closure-manifest.json"),
            "manifest_sha256": manifest_digest,
            "files": len(manifest_entries),
            "bytes": total_bytes,
        }
    finally:
        if not final_output:
            shutil.rmtree(temp_dir, ignore_errors=True)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    script_root = Path(__file__).resolve().parent.parent
    adapter = script_root / "adapter"
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pi", type=Path, required=True, help="installed Pi shim; it must resolve to the official bundled CLI entry")
    parser.add_argument("--node", type=Path, required=True, help="explicit Node executable, checked before and after staging")
    parser.add_argument("--out", type=Path, required=True, help="new output directory; existing destinations are refused")
    parser.add_argument("--extension-dir", type=Path, default=adapter / "src", help="TBound extension source directory")
    parser.add_argument("--extension-package", type=Path, default=adapter / "pi-runtime-package.json", help="runtime extension package.json (type=module)")
    parser.add_argument("--pi-version", default=PI_VERSION)
    parser.add_argument("--node-version", default=NODE_VERSION)
    parser.add_argument("--max-files", type=int, default=DEFAULT_MAX_FILES)
    parser.add_argument("--max-bytes", type=int, default=DEFAULT_MAX_BYTES)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    try:
        receipt = prepare(parse_args(argv))
    except (PreparationError, OSError) as exc:
        print(f"prepare-official-pi-runtime: error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(receipt, ensure_ascii=False, sort_keys=True, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
