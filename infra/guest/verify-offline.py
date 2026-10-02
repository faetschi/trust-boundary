#!/usr/bin/env python3
"""Run the pinned T-bound verification gates inside the prepared Linux guest."""

from __future__ import annotations

import hashlib
import json
import os
import platform
import re
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any


NODE_VERSION = "v24.21.0"
GO_VERSION = "go1.27.1"
PI_VERSION = "0.87.1"
NODE_BIN = Path("/opt/tbound/toolchains/node-v24.21.0-linux-x64/bin")
GO_BIN = Path("/opt/tbound/toolchains/go1.27.1/bin")
EXPECTED_OWNERSHIP_SKIP = "TestOpenRejectsUntrustedOwnership"
SCHEMA = "tbound.guest-verification/v1"
OWNERSHIP_PACKAGE = "tbound/supervisor/internal/audit"
GCC_DRIVER_PACKAGE = "gcc-13-x86-64-linux-gnu"


class GateError(Exception):
    def __init__(self, check: str, detail: str):
        super().__init__(detail)
        self.check = check
        self.detail = detail


def run(
    command: list[str],
    *,
    cwd: Path | None = None,
    env: dict[str, str] | None = None,
    timeout: int = 900,
) -> subprocess.CompletedProcess[str]:
    try:
        return subprocess.run(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise GateError("runner", f"could not complete {Path(command[0]).name}: {type(exc).__name__}") from None


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def parse_installed_package_manifest(manifest: str) -> list[dict[str, str]]:
    installed_packages = []
    for line in manifest.splitlines():
        fields = line.split("\t")
        # The second status character is the current state, even when the desired action is hold.
        if len(fields) == 3 and len(fields[0]) >= 2 and fields[0][1] == "i":
            installed_packages.append({"package": fields[1], "version": fields[2]})
    installed_packages.sort(key=lambda row: row["package"])
    return installed_packages


def tree_sha256(root: Path, excluded: set[str]) -> str:
    digest = hashlib.sha256()
    entries = sorted(root.rglob("*"), key=lambda path: path.relative_to(root).as_posix())
    for path in entries:
        relative_path = path.relative_to(root)
        if excluded.intersection(relative_path.parts):
            continue
        relative = relative_path.as_posix().encode("utf-8")
        if path.is_symlink():
            kind = b"L"
            content = os.readlink(path).encode("utf-8")
        elif path.is_dir():
            kind = b"D"
            content = b""
        elif path.is_file():
            kind = b"F"
            content = bytes.fromhex(sha256_file(path))
        else:
            raise GateError("source_hash", "tree contains a non-regular filesystem entry")
        digest.update(len(relative).to_bytes(8, "big"))
        digest.update(relative)
        digest.update(kind)
        digest.update(stat.S_IMODE(path.lstat().st_mode).to_bytes(4, "big"))
        digest.update(len(content).to_bytes(8, "big"))
        digest.update(content)
    return digest.hexdigest()


def assert_inside(path: Path, parent: Path, check: str) -> None:
    try:
        path.resolve(strict=True).relative_to(parent.resolve(strict=True))
    except (OSError, ValueError):
        raise GateError(check, "source path is missing or escapes the checkout") from None


def assert_executable_inside(path: Path, root: Path, check: str) -> None:
    try:
        path.resolve(strict=True).relative_to(root.resolve(strict=True))
    except (OSError, ValueError):
        raise GateError(check, "tool executable is missing or escapes its pinned toolchain") from None


def reject_source_symlinks(root: Path, check: str) -> None:
    for current, directory_names, file_names in os.walk(root, topdown=True, followlinks=False):
        directory = Path(current)
        if directory == root:
            directory_names[:] = [name for name in directory_names if name not in {"artifacts", "node_modules"}]
        for name in list(directory_names):
            path = directory / name
            if path.is_symlink():
                raise GateError(check, "unexpected symlink in source tree")
        for name in file_names:
            path = directory / name
            if path.is_symlink():
                raise GateError(check, "unexpected symlink in source tree")


def validate_dependency_tree(root: Path) -> None:
    if root.is_symlink() or not root.is_dir():
        raise GateError("adapter_dependencies", "adapter node_modules is missing or is a symlink")
    resolved_root = root.resolve(strict=True)
    for current, directory_names, file_names in os.walk(root, topdown=True, followlinks=False):
        for name in directory_names:
            path = Path(current) / name
            if path.is_symlink():
                raise GateError("adapter_dependencies", "symlinked package directory found in staged node_modules")
        for name in file_names:
            path = Path(current) / name
            if not path.is_symlink():
                continue
            target = os.readlink(path)
            if os.path.isabs(target):
                raise GateError("adapter_dependencies", "absolute symlink found in node_modules")
            try:
                path.resolve(strict=True).relative_to(resolved_root)
            except (OSError, ValueError):
                raise GateError("adapter_dependencies", "node_modules symlink escapes its dependency tree") from None


def trusted_ancestry(path: Path, expected_leaf_uid: int, check: str, private_leaf: bool) -> None:
    current = path
    components: list[Path] = []
    while True:
        components.append(current)
        if current.parent == current:
            break
        current = current.parent
    for index, component in enumerate(reversed(components)):
        try:
            info = component.lstat()
        except OSError:
            raise GateError(check, "private HOME ancestry is unavailable") from None
        if not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode):
            raise GateError(check, "private HOME ancestry contains a non-directory or symlink")
        if info.st_uid not in {0, expected_leaf_uid} or stat.S_IMODE(info.st_mode) & 0o022:
            raise GateError(check, "private HOME ancestry has an untrusted owner or writable mode")
        if private_leaf and index == len(components) - 1:
            if info.st_uid != expected_leaf_uid or stat.S_IMODE(info.st_mode) != 0o700:
                raise GateError(check, "private HOME must be owned by the trial user and have mode 0700")


def inspect_guest_network() -> tuple[dict[str, Any], list[str]]:
    snapshot: dict[str, Any] = {
        "guest_non_loopback_links": [],
        "ipv4_default_route_present": None,
        "ipv6_default_route_present": None,
    }
    failures: list[str] = []
    ip = shutil.which("ip", path="/usr/sbin:/usr/bin:/sbin:/bin")
    if not ip:
        failures.append("iproute2_missing")
    else:
        for family, flag, field in (
            ("ipv4", "-4", "ipv4_default_route_present"),
            ("ipv6", "-6", "ipv6_default_route_present"),
        ):
            result = run([ip, flag, "route", "show", "table", "all"], timeout=10)
            if result.returncode != 0:
                failures.append(f"{family}_route_unreadable")
                continue
            routes = [line for line in result.stdout.splitlines() if re.search(r"(^|\s)default($|\s)", line)]
            snapshot[field] = bool(routes)
            if routes:
                failures.append(f"{family}_default_route_present")

    network_root = Path("/sys/class/net")
    try:
        interfaces = sorted(path for path in network_root.iterdir() if path.name != "lo")
    except OSError:
        failures.append("guest_links_unreadable")
        interfaces = []
    if not interfaces:
        failures.append("no_guest_link_to_verify")
    for interface in interfaces:
        item: dict[str, Any] = {"name": interface.name, "carrier": None}
        snapshot["guest_non_loopback_links"].append(item)
        try:
            carrier_text = (interface / "carrier").read_text(encoding="ascii").strip()
        except OSError:
            failures.append(f"{interface.name}_carrier_unreadable")
            continue
        if carrier_text not in {"0", "1"}:
            failures.append(f"{interface.name}_carrier_unknown")
            continue
        item["carrier"] = int(carrier_text)
        if item["carrier"] != 0:
            failures.append(f"{interface.name}_carrier_present")
    return snapshot, failures


def parse_go_events(output: str) -> dict[str, Any]:
    tests: dict[tuple[str, str], dict[str, int]] = {}
    package_actions: dict[str, str] = {}
    malformed = 0
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            malformed += 1
            continue
        package = event.get("Package")
        action = event.get("Action")
        test_name = event.get("Test")
        if test_name:
            identity = (package or "", test_name)
            counts = tests.setdefault(identity, {"run": 0, "pass": 0, "fail": 0, "skip": 0})
            if action in counts:
                counts[action] += 1
        elif package and action in {"pass", "fail", "skip"}:
            package_actions[package] = action
    run_tests = [
        {"package": package, "test": name}
        for (package, name), counts in sorted(tests.items())
        if counts["run"]
    ]
    skipped_tests = [
        {"package": package, "test": name}
        for (package, name), counts in sorted(tests.items())
        if counts["skip"]
    ]
    skip_event_counts = [
        {"package": package, "test": name, "count": counts["skip"]}
        for (package, name), counts in sorted(tests.items())
        if counts["skip"]
    ]
    failed_tests = [
        {"package": package, "test": name}
        for (package, name), counts in sorted(tests.items())
        if counts["fail"]
    ]
    return {
        "test_run_events": sum(item["run"] for item in tests.values()),
        "run_tests": run_tests,
        "test_pass_events": sum(item["pass"] for item in tests.values()),
        "test_fail_events": sum(item["fail"] for item in tests.values()),
        "skipped_tests": skipped_tests,
        "skip_event_counts": skip_event_counts,
        "failed_tests": failed_tests,
        "package_results": package_actions,
        "malformed_events": malformed,
    }


def summarize_go_phase(
    command: list[str],
    cwd: Path,
    env: dict[str, str],
    expected_packages: set[str],
    *,
    allow_ownership_skip: bool,
) -> tuple[dict[str, Any], list[str]]:
    result = run(command, cwd=cwd, env=env)
    events = parse_go_events(result.stdout)
    failures: list[str] = []
    skips = events["skipped_tests"]
    expected_skip = {"package": "tbound/supervisor/internal/audit", "test": EXPECTED_OWNERSHIP_SKIP}
    allowed = [expected_skip] if allow_ownership_skip else []
    unexpected_skips = [item for item in skips if item not in allowed]
    expected_skip_events = sum(item["count"] for item in events["skip_event_counts"]
                               if item["package"] == expected_skip["package"] and item["test"] == expected_skip["test"])
    if allow_ownership_skip and (skips.count(expected_skip) != 1 or expected_skip_events != 1):
        unexpected_skips.append({"package": "", "test": f"expected-one-{EXPECTED_OWNERSHIP_SKIP}"})
    unexpected_skips.sort(key=lambda item: (item["package"], item["test"]))
    if unexpected_skips:
        failures.append("unexpected_skips")
    if events["malformed_events"]:
        failures.append("invalid_go_test_json")
    if result.returncode != 0:
        failures.append("go_test_failed")
    if events["test_fail_events"]:
        failures.append("failed_tests")
    if events["test_run_events"] == 0:
        failures.append("no_tests_executed")
    observed_packages = events["package_results"]
    if set(observed_packages) != expected_packages:
        failures.append("package_coverage_mismatch")
    if any(action != "pass" for action in observed_packages.values()):
        failures.append("failed_packages")
    entry = {
        "status": "PASS" if not failures else "FAIL",
        "exit_code": result.returncode,
        "packages_expected": len(expected_packages),
        "packages_reported": len(observed_packages),
        "test_run_events": events["test_run_events"],
        "test_pass_events": events["test_pass_events"],
        "test_fail_events": events["test_fail_events"],
        "failed_tests": events["failed_tests"],
        "skipped_tests": skips,
        "skip_event_counts": events["skip_event_counts"],
        "deferred_known_skip": expected_skip if allow_ownership_skip and expected_skip in skips else None,
        "packages": sorted(observed_packages),
        "unexpected_skips": unexpected_skips,
        "failures": failures,
    }
    return entry, failures


def extract_adapter_report(output: str) -> dict[str, Any] | None:
    decoder = json.JSONDecoder()
    for match in re.finditer(r"(?m)^\s*\{", output):
        try:
            parsed, _ = decoder.raw_decode(output[match.start() :])
        except json.JSONDecodeError:
            continue
        if isinstance(parsed, dict) and "pi_package" in parsed and "provider_stream_attempts" in parsed:
            return parsed
    return None


def privileged_ownership_main(arguments: list[str]) -> int:
    """Run only the ownership integration test with a root-owned temporary HOME."""
    result: dict[str, Any] = {
        "schema": "tbound.guest-ownership-test/v1",
        "status": "FAIL",
        "test": EXPECTED_OWNERSHIP_SKIP,
        "package": OWNERSHIP_PACKAGE,
        "failures": [],
        "cleanup": {"status": "NOT_RUN", "leftover_path": None},
    }
    root_temp: Path | None = None
    try:
        if os.geteuid() != 0:
            raise GateError("privileged_test", "private helper must run as root")
        if len(arguments) != 3:
            raise GateError("privileged_test", "invalid fixed helper arguments")
        staged_supervisor = Path(arguments[0]).resolve(strict=True)
        module_cache = Path(arguments[1]).resolve(strict=True)
        try:
            trial_uid = int(arguments[2])
        except ValueError:
            raise GateError("privileged_test", "invalid trial uid") from None
        if trial_uid <= 0:
            raise GateError("privileged_test", "invalid trial uid")
        if staged_supervisor.name != "supervisor" or not module_cache.is_dir():
            raise GateError("privileged_test", "staged source or module cache is missing")
        if not (staged_supervisor / "go.mod").is_file():
            raise GateError("privileged_test", "staged Go module is missing")
        root_home = Path("/root")
        trusted_ancestry(root_home, 0, "privileged_test", private_leaf=False)
        home_info = root_home.lstat()
        if home_info.st_uid != 0 or stat.S_IMODE(home_info.st_mode) & 0o077:
            raise GateError("privileged_test", "root HOME must be root-owned and private")

        root_temp = Path(tempfile.mkdtemp(prefix=".tbound-guest-audit-", dir=root_home))
        if root_temp.lstat().st_uid != 0 or stat.S_IMODE(root_temp.lstat().st_mode) != 0o700:
            raise GateError("privileged_test", "root temporary directory is not private")
        root_home_private = root_temp / "home"
        root_tmp = root_temp / "tmp"
        go_cache = root_temp / "gocache"
        for directory in (root_home_private, root_tmp, go_cache):
            directory.mkdir(mode=0o700)

        go = GO_BIN / "go"
        if not go.is_file():
            raise GateError("privileged_test", "pinned Go executable is missing")
        env = {
            "HOME": str(root_home_private),
            "TMPDIR": str(root_tmp),
            "GOCACHE": str(go_cache),
            "GOMODCACHE": str(module_cache),
            "PATH": f"{GO_BIN}:/usr/bin:/bin",
            "GOTOOLCHAIN": "local",
            "GOENV": "off",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOWORK": "off",
            "GOFLAGS": "",
            "CGO_ENABLED": "1",
            "LANG": "C.UTF-8",
        }
        command = [
            str(go), "test", "-json", "-count=1", "-run",
            "^TestOpenRejectsUntrustedOwnership$", "./internal/audit",
        ]
        completed = run(command, cwd=staged_supervisor, env=env)
        events = parse_go_events(completed.stdout)
        expected = {"package": OWNERSHIP_PACKAGE, "test": EXPECTED_OWNERSHIP_SKIP}
        failures: list[str] = []
        if completed.returncode != 0:
            failures.append("go_test_failed")
        if events["malformed_events"]:
            failures.append("invalid_go_test_json")
        if events["skipped_tests"]:
            failures.append("test_skipped")
        if events["test_fail_events"] or events["failed_tests"]:
            failures.append("test_failed")
        if expected not in events["run_tests"]:
            failures.append("ownership_test_not_run")
        if events["package_results"] != {OWNERSHIP_PACKAGE: "pass"}:
            failures.append("package_did_not_pass")
        result.update({
            "exit_code": completed.returncode,
            "test_run_events": events["test_run_events"],
            "test_pass_events": events["test_pass_events"],
            "test_fail_events": events["test_fail_events"],
            "skipped_tests": events["skipped_tests"],
            "skip_event_counts": events["skip_event_counts"],
            "package_results": events["package_results"],
            "failures": failures,
        })
    except GateError as exc:
        result["failures"].append(f"{exc.check}:{exc.detail}")
    except Exception as exc:
        result["failures"].append(f"helper:{type(exc).__name__}")
    finally:
        if root_temp is not None:
            try:
                shutil.rmtree(root_temp)
                if root_temp.exists():
                    raise OSError("temporary directory remains after removal")
                result["cleanup"] = {"status": "PASS", "leftover_path": None}
            except OSError as exc:
                result["cleanup"] = {"status": "FAIL", "leftover_path": str(root_temp)}
                result["failures"].append(f"cleanup_failed:{type(exc).__name__}")
    if result["cleanup"]["status"] == "NOT_RUN":
        result["cleanup"] = {"status": "FAIL", "leftover_path": None}
        result["failures"].append("cleanup_not_run")
    result["status"] = "PASS" if not result["failures"] else "FAIL"
    print(json.dumps(result, sort_keys=True, separators=(",", ":")))
    return 0 if result["status"] == "PASS" else 1


def main() -> int:
    report: dict[str, Any] = {
        "schema": SCHEMA,
        "status": "FAIL",
        "checks": {},
        "failures": [],
        "cleanup": {"status": "NOT_RUN", "leftover_paths": []},
        "claims": {
            "live_provider_exchange": False,
            "live_effect_execution": False,
            "containment_established": False,
            "g1_durability_established": False,
        },
    }
    scratch: Path | None = None
    safe_home: Path | None = None
    try:
        if sys.argv[1:] != ["--host-adapter-disconnected"]:
            raise GateError("offline_prerequisite", "verify the guest NIC is disconnected in the hypervisor, then pass --host-adapter-disconnected")
        if platform.system() != "Linux":
            raise GateError("guest", "runner only operates in a Linux guest")
        if os.geteuid() == 0:
            raise GateError("guest", "invoke as the non-root trial account")
        if platform.machine().lower() not in {"x86_64", "amd64"}:
            raise GateError("guest", "Ubuntu amd64 guest is required")
        release = platform.freedesktop_os_release()
        if release.get("ID") != "ubuntu" or release.get("VERSION_CODENAME") != "noble" or release.get("VERSION_ID") != "24.04":
            raise GateError("guest", "Ubuntu 24.04 Noble is required")

        raw_home = Path(os.environ.get("HOME", ""))
        if not raw_home.is_absolute():
            raise GateError("home", "HOME must be an absolute path")
        user_home = raw_home.absolute()
        trusted_ancestry(user_home, os.geteuid(), "home", private_leaf=False)
        network_snapshot, network_failures = inspect_guest_network()
        report["guest"] = {
            "distribution": "Ubuntu 24.04 Noble",
            "architecture": "amd64",
            "kernel": platform.release(),
            "python": platform.python_version(),
            "uid": os.geteuid(),
            "offline_prerequisite": {
                "host_adapter_disconnected_attested": True,
                **network_snapshot,
            },
        }
        if network_failures:
            raise GateError("network", ",".join(network_failures))

        script_path = Path(__file__)
        if script_path.is_symlink():
            raise GateError("source", "runner script must not be a symlink")
        runner_path = script_path.resolve(strict=True)
        tbound_root = runner_path.parents[2]
        supervisor_source = tbound_root / "supervisor"
        adapter_source = tbound_root / "adapter"
        for source in (supervisor_source, adapter_source):
            assert_inside(source, tbound_root, "source")
            if source.is_symlink():
                raise GateError("source", "source directory must not be a symlink")
            reject_source_symlinks(source, "source")

        go = GO_BIN / "go"
        node = NODE_BIN / "node"
        npm = NODE_BIN / "npm"
        gcc = Path("/usr/bin/gcc")
        if not go.is_file() or not node.is_file() or not npm.is_file():
            raise GateError("toolchains", "pinned Go, Node.js, or bundled npm executable is missing")
        assert_executable_inside(go, GO_BIN.parent, "toolchains")
        assert_executable_inside(node, NODE_BIN.parent, "toolchains")
        assert_executable_inside(npm, NODE_BIN.parent, "toolchains")
        node_result = run([str(node), "--version"], env={"PATH": f"{NODE_BIN}:/usr/bin:/bin"}, timeout=10)
        go_result = run([str(go), "version"], env={"GOTOOLCHAIN": "local", "GOENV": "off"}, timeout=10)
        npm_result = run(
            [str(npm), "--version"],
            env={
                "PATH": f"{NODE_BIN}:/usr/bin:/bin",
                "HOME": str(user_home),
                "npm_config_userconfig": "/dev/null",
                "npm_config_offline": "true",
                "npm_config_cache": str(user_home / ".cache" / "tbound-npm-cache"),
            },
            timeout=10,
        )
        if node_result.returncode or node_result.stdout.strip() != NODE_VERSION:
            raise GateError("toolchains", "pinned Node.js version check failed")
        if go_result.returncode or not go_result.stdout.startswith(f"go version {GO_VERSION} linux/amd64"):
            raise GateError("toolchains", "pinned Go version check failed")
        if npm_result.returncode or not npm_result.stdout.strip():
            raise GateError("toolchains", "bundled npm version check failed")
        go_env_result = run(
            [str(go), "env", "GOOS", "GOARCH", "CGO_ENABLED"],
            env={"GOTOOLCHAIN": "local", "GOENV": "off", "HOME": str(user_home), "PATH": "/usr/bin:/bin"},
            timeout=10,
        )
        if go_env_result.returncode or go_env_result.stdout.splitlines() != ["linux", "amd64", "1"]:
            raise GateError("toolchains", "Go must report linux/amd64 with cgo enabled")
        gcc_resolved = gcc.resolve(strict=True)
        gcc_owner = run(["/usr/bin/dpkg-query", "-S", str(gcc_resolved)], env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}, timeout=10)
        gcc_package = run(
            ["/usr/bin/dpkg-query", "-W", "-f=${Version}", GCC_DRIVER_PACKAGE],
            env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}, timeout=10,
        )
        gcc_holds = run(["/usr/bin/apt-mark", "showhold"], env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}, timeout=10)
        gcc_result = run([str(gcc_resolved), "--version"], env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}, timeout=10)
        owner_lines = gcc_owner.stdout.splitlines()
        owner_prefix = owner_lines[0].split(": ", 1)[0] if owner_lines and ": " in owner_lines[0] else ""
        owner_path = owner_lines[0].split(": ", 1)[1] if owner_lines and ": " in owner_lines[0] else ""
        accepted_owners = {GCC_DRIVER_PACKAGE, f"{GCC_DRIVER_PACKAGE}:amd64"}
        if (gcc_result.returncode or gcc_owner.returncode
                or owner_prefix not in accepted_owners or owner_path != str(gcc_resolved)
                or gcc_package.returncode or not gcc_package.stdout.strip()
                or gcc_holds.returncode or not (accepted_owners & set(gcc_holds.stdout.splitlines()))):
            raise GateError("toolchains", "held GCC backend ownership, version, or binary verification failed")
        package_manifest = run(
            ["/usr/bin/dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${binary:Package}\t${Version}\n"],
            env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}, timeout=30,
        )
        if package_manifest.returncode:
            raise GateError("toolchains", "installed package manifest could not be captured")
        installed_packages = parse_installed_package_manifest(package_manifest.stdout)
        canonical_manifest = "".join(f'{row["package"]}\t{row["version"]}\n' for row in installed_packages)
        gcc_manifest_entry = next(
            (row for row in installed_packages if row["package"] in accepted_owners), None
        )
        if gcc_manifest_entry is None or gcc_manifest_entry["version"] != gcc_package.stdout.strip():
            raise GateError("toolchains", "GCC backend is missing from the installed package manifest")
        report["toolchains"] = {
            "go": go_result.stdout.strip(),
            "go_sha256": sha256_file(go),
            "node": node_result.stdout.strip(),
            "node_sha256": sha256_file(node),
            "npm": npm_result.stdout.strip(),
            "npm_sha256": sha256_file(npm),
            "gcc": gcc_result.stdout.splitlines()[0],
            "gcc_driver_path": str(gcc_resolved),
            "gcc_driver_owner": owner_prefix,
            "gcc_driver_package_version": gcc_package.stdout.strip(),
            "gcc_driver_package_held": True,
            "gcc_driver_sha256": sha256_file(gcc_resolved),
            "installed_package_count": len(installed_packages),
            "installed_package_manifest_sha256": hashlib.sha256(canonical_manifest.encode()).hexdigest(),
            "installed_packages": installed_packages,
        }

        sudo = shutil.which("sudo", path="/usr/bin:/bin")
        if not sudo:
            raise GateError("ownership_test", "sudo is required for the isolated root-only ownership test")
        sudo_check = run([sudo, "-n", "-v"], timeout=10)
        if sudo_check.returncode:
            raise GateError("ownership_test", "run `sudo -v` before disconnecting, then retry")
        root_home = Path("/root")
        try:
            root_info = root_home.lstat()
        except OSError:
            raise GateError("ownership_test", "root HOME is unavailable") from None
        if not stat.S_ISDIR(root_info.st_mode) or root_info.st_uid != 0 or stat.S_IMODE(root_info.st_mode) & 0o077:
            raise GateError("ownership_test", "root HOME must be a root-owned private directory")
        trusted_ancestry(root_home, 0, "ownership_test", private_leaf=False)

        scratch = Path(tempfile.mkdtemp(prefix="tbound-offline-verify-", dir="/tmp"))
        scratch_info = scratch.lstat()
        if scratch_info.st_uid != os.geteuid() or stat.S_IMODE(scratch_info.st_mode) != 0o700:
            raise GateError("staging", "temporary staging directory must be trial-user-owned mode 0700")
        safe_home = Path(tempfile.mkdtemp(prefix=".tbound-verification-home-", dir=user_home))
        trusted_ancestry(safe_home, os.geteuid(), "home", private_leaf=True)

        staged_supervisor = scratch / "supervisor"
        staged_adapter = scratch / "adapter"
        stage_ignored = shutil.ignore_patterns("artifacts", "node_modules", ".cache", ".npm")
        shutil.copytree(supervisor_source, staged_supervisor, symlinks=False, ignore=stage_ignored)
        shutil.copytree(adapter_source, staged_adapter, symlinks=False, ignore=stage_ignored)
        assert_inside(staged_supervisor, scratch, "staging")
        assert_inside(staged_adapter, scratch, "staging")

        module_cache_result = run(
            [str(go), "env", "GOMODCACHE"],
            env={"GOTOOLCHAIN": "local", "GOENV": "off", "HOME": str(user_home)},
            timeout=10,
        )
        if module_cache_result.returncode:
            raise GateError("go_modules", "could not locate the prepared Go module cache")
        module_cache = Path(module_cache_result.stdout.strip()).resolve(strict=True)
        if not module_cache.is_dir():
            raise GateError("go_modules", "prepared Go module cache is missing")
        npm_cache = user_home / ".cache" / "tbound-npm-cache"
        try:
            npm_cache_resolved = npm_cache.resolve(strict=True)
            npm_cache_resolved.relative_to(user_home.resolve(strict=True))
            for cache_dir in (user_home / ".cache", npm_cache):
                cache_info = cache_dir.lstat()
                if (not stat.S_ISDIR(cache_info.st_mode) or stat.S_ISLNK(cache_info.st_mode)
                        or cache_info.st_uid != os.geteuid() or stat.S_IMODE(cache_info.st_mode) & 0o022):
                    raise ValueError
        except (OSError, ValueError):
            raise GateError("adapter_dependencies", "prepared npm cache must exist under HOME and be trial-user-owned") from None

        user_env = {
            "PATH": f"{GO_BIN}:{NODE_BIN}:/usr/bin:/bin",
            "HOME": str(safe_home),
            "TMPDIR": str(scratch),
            "LANG": "C.UTF-8",
            "GOTOOLCHAIN": "local",
            "GOENV": "off",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOMODCACHE": str(module_cache),
            "GOCACHE": str(scratch / "go-cache"),
            "GOWORK": "off",
            "GOFLAGS": "",
            "CGO_ENABLED": "1",
            "npm_config_offline": "true",
            "npm_config_audit": "false",
            "npm_config_fund": "false",
            "npm_config_update_notifier": "false",
            "npm_config_cache": str(npm_cache_resolved),
            "npm_config_logs_dir": str(scratch / "npm-logs"),
            "npm_config_userconfig": "/dev/null",
        }
        graph = run([str(go), "list", "-m", "all"], cwd=staged_supervisor, env=user_env)
        if graph.returncode:
            raise GateError("go_modules", "offline Go module graph resolution failed; provision go.mod dependencies first")
        modules = [line.strip() for line in graph.stdout.splitlines() if line.strip()]
        if not modules:
            raise GateError("go_modules", "Go module graph was empty")
        verify = run([str(go), "mod", "verify"], cwd=staged_supervisor, env=user_env)
        if verify.returncode or "all modules verified" not in verify.stdout.lower():
            raise GateError("go_modules", "offline Go module cache verification failed")
        package_list = run([str(go), "list", "./..."], cwd=staged_supervisor, env=user_env)
        if package_list.returncode:
            raise GateError("go_modules", "offline Go package enumeration failed")
        expected_packages = {line.strip() for line in package_list.stdout.splitlines() if line.strip()}
        if not expected_packages:
            raise GateError("go_modules", "no supervisor packages were enumerated")

        report["inputs"] = {
            "supervisor_source_sha256": tree_sha256(staged_supervisor, {"artifacts", "node_modules", ".cache", ".npm"}),
            "go_mod_sha256": sha256_file(staged_supervisor / "go.mod"),
            "go_sum_sha256": sha256_file(staged_supervisor / "go.sum"),
            "go_module_count": len(modules),
            "go_module_graph_sha256": hashlib.sha256(("\n".join(modules) + "\n").encode()).hexdigest(),
            "adapter_source_sha256": tree_sha256(staged_adapter, {"artifacts", "node_modules", ".cache", ".npm"}),
            "adapter_package_json_sha256": sha256_file(staged_adapter / "package.json"),
            "adapter_package_lock_sha256": sha256_file(staged_adapter / "package-lock.json"),
            "adapter_profile_sha256": sha256_file(staged_adapter / "profile.json"),
            "guest_runner_sha256": sha256_file(runner_path),
            "guest_setup_sha256": sha256_file(runner_path.parent / "setup-ubuntu-24.04.sh"),
        }
        report["checks"]["go_modules"] = {"status": "PASS", "verified": "all cached modules", "packages": len(expected_packages)}

        sudo_check = run([sudo, "-n", "-v"], timeout=10)
        if sudo_check.returncode:
            raise GateError("ownership_test", "sudo authorization expired; run `sudo -v` and retry")
        root_command = [
            sudo, "-n", "env", "-i", "HOME=/root", "PATH=/usr/bin:/bin", "LANG=C.UTF-8",
            "/usr/bin/python3", "-I", str(runner_path), "--_privileged-ownership-test",
            str(staged_supervisor), str(module_cache), str(os.geteuid()),
        ]
        root_result = run(root_command, timeout=900)
        try:
            ownership = json.loads(root_result.stdout)
        except json.JSONDecodeError:
            ownership = {"status": "FAIL", "failures": ["invalid_helper_report"]}
        ownership_failures = list(ownership.get("failures", []))
        if root_result.returncode != 0 and not ownership_failures:
            ownership_failures.append("privileged_helper_failed")
        if ownership.get("schema") != "tbound.guest-ownership-test/v1" or ownership.get("status") != "PASS":
            ownership_failures.append("privileged_ownership_test_failed")
        ownership["sudo_noninteractive"] = True
        ownership["test"] = EXPECTED_OWNERSHIP_SKIP
        ownership["failure_count"] = len(ownership_failures)
        ownership["status"] = "PASS" if not ownership_failures else "FAIL"
        ownership["failures"] = ownership_failures
        report["checks"]["privileged_ownership_test"] = ownership
        if ownership_failures:
            report["failures"].extend(f"privileged_ownership_test:{item}" for item in ownership_failures)

        full_command = [str(go), "test", "-json", "-count=1", "./..."]
        regular, regular_failures = summarize_go_phase(
            full_command,
            staged_supervisor,
            user_env,
            expected_packages,
            allow_ownership_skip=True,
        )
        report["checks"]["go_tests"] = regular
        if regular_failures:
            report["failures"].extend(f"go_tests:{item}" for item in regular_failures)

        race_command = [str(go), "test", "-race", "-json", "-count=1", "./..."]
        race, race_failures = summarize_go_phase(
            race_command,
            staged_supervisor,
            user_env,
            expected_packages,
            allow_ownership_skip=True,
        )
        report["checks"]["go_race"] = race
        if race_failures:
            report["failures"].extend(f"go_race:{item}" for item in race_failures)

        npm_ci_command = [
            str(npm), "ci", "--offline", "--ignore-scripts", "--no-audit", "--no-fund",
        ]
        npm_ci_result = run(npm_ci_command, cwd=staged_adapter, env=user_env)
        adapter_dependency_failures: list[str] = []
        if npm_ci_result.returncode:
            adapter_dependency_failures.append("offline_npm_ci_failed")
            report["checks"]["adapter_dependencies"] = {
                "status": "FAIL", "exit_code": npm_ci_result.returncode,
                "method": "npm ci --offline --ignore-scripts --no-audit --no-fund",
                "failures": adapter_dependency_failures,
            }
            report["failures"].extend(f"adapter_dependencies:{item}" for item in adapter_dependency_failures)
        else:
            try:
                validate_dependency_tree(staged_adapter / "node_modules")
                assert_inside(staged_adapter / "node_modules", scratch, "adapter_dependencies")
                dependency_hash = tree_sha256(staged_adapter / "node_modules", set())
            except GateError as exc:
                adapter_dependency_failures.append(exc.detail)
                dependency_hash = None
            report["checks"]["adapter_dependencies"] = {
                "status": "PASS" if not adapter_dependency_failures else "FAIL",
                "exit_code": npm_ci_result.returncode,
                "method": "npm ci --offline --ignore-scripts --no-audit --no-fund",
                "node_modules_sha256": dependency_hash,
                "failures": adapter_dependency_failures,
            }
            if adapter_dependency_failures:
                report["failures"].extend(f"adapter_dependencies:{item}" for item in adapter_dependency_failures)
        if report["checks"]["adapter_dependencies"]["status"] != "PASS":
            report["checks"]["node_adapter"] = {"status": "NOT_RUN", "reason": "adapter dependency installation did not pass"}
            adapter_deps_ready = False
        else:
            adapter_deps_ready = True
        report["inputs"]["adapter_node_modules_sha256"] = report["checks"]["adapter_dependencies"].get("node_modules_sha256")

        adapter_command = [
            str(npm),
            "--offline",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
            "--silent",
            "run",
            "check",
        ]
        if adapter_deps_ready:
            adapter_result = run(adapter_command, cwd=staged_adapter, env=user_env)
            adapter_report = extract_adapter_report(adapter_result.stdout)
            adapter_failures: list[str] = []
            pi = (adapter_report or {}).get("pi_package", {})
            if adapter_result.returncode:
                adapter_failures.append("npm_check_failed")
            if not adapter_report:
                adapter_failures.append("adapter_report_missing")
            if pi.get("installed_version") != PI_VERSION or pi.get("locked_version") != PI_VERSION:
                adapter_failures.append("pi_version_mismatch")
            if (adapter_report or {}).get("provider_stream_attempts") != 0:
                adapter_failures.append("provider_stream_attempt_recorded")
            report["checks"]["node_adapter"] = {
                "status": "PASS" if not adapter_failures else "FAIL",
                "exit_code": adapter_result.returncode,
                "check": "npm run check (offline; lifecycle scripts disabled)",
                "pi_package": PI_VERSION if pi.get("installed_version") == PI_VERSION else None,
                "provider_stream_attempts": (adapter_report or {}).get("provider_stream_attempts"),
                "failures": adapter_failures,
            }
            if adapter_failures:
                report["failures"].extend(f"node_adapter:{item}" for item in adapter_failures)

        if report["failures"]:
            report["status"] = "FAIL"
        else:
            report["status"] = "PASS"
    except GateError as exc:
        report["failures"].append(f"{exc.check}:{exc.detail}")
    except Exception as exc:  # Keep stdout machine-readable even on unexpected local errors.
        report["failures"].append(f"runner:{type(exc).__name__}")
    finally:
        cleanup_errors: list[str] = []
        if safe_home is not None:
            try:
                shutil.rmtree(safe_home)
                if safe_home.exists():
                    raise OSError("temporary HOME remains after removal")
            except OSError as exc:
                cleanup_errors.append(f"{safe_home}:{type(exc).__name__}")
        if scratch is not None:
            try:
                shutil.rmtree(scratch)
                if scratch.exists():
                    raise OSError("staging directory remains after removal")
            except OSError as exc:
                cleanup_errors.append(f"{scratch}:{type(exc).__name__}")
        report["cleanup"] = {
            "status": "FAIL" if cleanup_errors else "PASS",
            "leftover_paths": [entry.split(":", 1)[0] for entry in cleanup_errors],
        }
        if cleanup_errors:
            report["failures"].extend(f"cleanup_failed:{entry}" for entry in cleanup_errors)

    if report["failures"]:
        report["status"] = "FAIL"
    print(json.dumps(report, sort_keys=True, separators=(",", ":")))
    return 0 if report["status"] == "PASS" else 1


if __name__ == "__main__":
    if len(sys.argv) >= 2 and sys.argv[1] == "--_privileged-ownership-test":
        sys.exit(privileged_ownership_main(sys.argv[2:]))
    sys.exit(main())
