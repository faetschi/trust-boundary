from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import prepare_official_pi_runtime as runtime  # noqa: E402


PI_NAME = runtime.PI_PACKAGE
PI_VERSION = runtime.PI_VERSION


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, sort_keys=True) + "\n", encoding="utf-8")


def make_package(path: Path, name: str, version: str, dependencies: dict[str, str] | None = None) -> dict[str, object]:
    manifest: dict[str, object] = {"name": name, "version": version}
    if dependencies:
        manifest["dependencies"] = dependencies
    write_json(path / "package.json", manifest)
    return manifest


def make_staging_fixture(root: Path) -> dict[str, Path]:
    package_root = root / "installed" / "node_modules" / "@earendil-works" / "pi-coding-agent"
    package_root.mkdir(parents=True)
    entry = package_root / "dist" / "bundle" / "cli.js"
    entry.parent.mkdir(parents=True)
    entry.write_text("// fixture Pi CLI entry\n", encoding="utf-8")

    root_manifest = make_package(package_root, PI_NAME, PI_VERSION, {"fixture-dep": "^1.0.0"})
    dep_root = package_root / "node_modules" / "fixture-dep"
    make_package(dep_root, "fixture-dep", "1.2.3")
    lock = {
        "name": PI_NAME,
        "version": PI_VERSION,
        "lockfileVersion": 3,
        "packages": {
            "": {"name": PI_NAME, "version": PI_VERSION, "dependencies": {"fixture-dep": "^1.0.0"}},
            "node_modules/fixture-dep": {"version": "1.2.3"},
        },
    }
    lock_path = package_root / "npm-shrinkwrap.json"
    write_json(lock_path, lock)

    extension_dir = root / "extension" / "src"
    extension_dir.mkdir(parents=True)
    for name in runtime.EXTENSION_FILES:
        (extension_dir / name).write_text(f"// {name}\n", encoding="utf-8")
    extension_package = root / "extension" / "package.json"
    write_json(extension_package, {"type": "module"})

    node_path = root / "node"
    node_path.write_text("fixture Node executable\n", encoding="utf-8")
    output_parent = root / "output"
    output_parent.mkdir()
    return {
        "package_root": package_root,
        "entry": entry,
        "lock_path": lock_path,
        "extension_dir": extension_dir,
        "extension_package": extension_package,
        "node": node_path,
        "output_parent": output_parent,
    }


def make_args(fixture: dict[str, Path], output_name: str = "runtime") -> object:
    from argparse import Namespace

    return Namespace(
        pi=fixture["entry"],
        node=fixture["node"],
        out=fixture["output_parent"] / output_name,
        extension_dir=fixture["extension_dir"],
        extension_package=fixture["extension_package"],
        pi_version=PI_VERSION,
        node_version=runtime.NODE_VERSION,
        max_files=100,
        max_bytes=1_000_000,
    )


class DependencyLockTests(unittest.TestCase):
    def test_installed_version_must_match_direct_shrinkwrap_entry(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            package_root = Path(temporary) / "pi"
            package_root.mkdir()
            manifest = make_package(package_root, PI_NAME, PI_VERSION, {"wrongdep": "^1.0.0"})
            make_package(package_root / "node_modules" / "wrongdep", "wrongdep", "99.0.0")
            lock = {"packages": {"node_modules/wrongdep": {"version": "1.0.0"}}}
            with self.assertRaisesRegex(runtime.PreparationError, "version does not match.*99.0.0.*1.0.0"):
                runtime.validate_dependency_closure(package_root, manifest, lock)

    def test_nested_dependency_uses_its_resolved_lock_location(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            package_root = Path(temporary) / "pi"
            package_root.mkdir()
            manifest = make_package(package_root, PI_NAME, PI_VERSION, {"foo": "^1.0.0"})
            foo = package_root / "node_modules" / "foo"
            make_package(foo, "foo", "1.3.0", {"bar": "^1.0.0"})
            make_package(package_root / "node_modules" / "bar", "bar", "2.0.0")
            make_package(foo / "node_modules" / "bar", "bar", "1.4.0")
            lock = {
                "packages": {
                    "node_modules/foo": {"version": "1.3.0"},
                    "node_modules/bar": {"version": "2.0.0"},
                    "node_modules/foo/node_modules/bar": {"version": "1.4.0"},
                }
            }
            runtime.validate_dependency_closure(package_root, manifest, lock)

            lock["packages"]["node_modules/foo/node_modules/bar"]["version"] = "9.9.9"
            with self.assertRaisesRegex(runtime.PreparationError, "node_modules/foo/node_modules/bar.*1.4.0.*9.9.9"):
                runtime.validate_dependency_closure(package_root, manifest, lock)

            del lock["packages"]["node_modules/foo/node_modules/bar"]
            with self.assertRaisesRegex(runtime.PreparationError, "missing from Pi npm-shrinkwrap"):
                runtime.validate_dependency_closure(package_root, manifest, lock)

    def test_missing_dependency_and_sibling_hoist_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_root = root / "installed" / "node_modules" / "@earendil-works" / "pi-coding-agent"
            package_root.mkdir(parents=True)
            missing = make_package(package_root, PI_NAME, PI_VERSION, {"missing": "1.0.0"})
            with self.assertRaisesRegex(runtime.PreparationError, "missing dependency 'missing'"):
                runtime.validate_dependency_closure(package_root, missing, {"packages": {}})

            external = package_root.parents[2] / "node_modules" / "external"
            make_package(external, "external", "1.0.0")
            root_manifest = make_package(package_root, PI_NAME, PI_VERSION, {"external": "1.0.0"})
            with self.assertRaisesRegex(runtime.PreparationError, "sibling-hoisted outside"):
                runtime.validate_dependency_closure(package_root, root_manifest, {"packages": {}})


class StagingControlTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform.startswith("linux"), "the staging command publishes only on Linux")
    def test_lock_digest_comes_from_verified_staged_bytes_after_source_changes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = make_staging_fixture(root)
            args = make_args(fixture)
            original_scan = runtime.scan_tree
            package_scans = 0

            def scan_then_mutate(path: Path, **kwargs: object) -> list[dict[str, object]]:
                nonlocal package_scans
                entries = original_scan(path, **kwargs)
                if path.resolve() == fixture["package_root"].resolve():
                    package_scans += 1
                    if package_scans == 2:
                        changed = json.loads(fixture["lock_path"].read_text(encoding="utf-8"))
                        changed["review_test_mutation"] = True
                        write_json(fixture["lock_path"], changed)
                return entries

            def version_check(_node: Path, _entry: Path, _home: Path, *, node_only: bool = False) -> str:
                return runtime.NODE_VERSION if node_only else PI_VERSION

            with (
                mock.patch.object(runtime.os, "geteuid", return_value=1000, create=True),
                mock.patch.object(runtime, "scan_tree", side_effect=scan_then_mutate),
                mock.patch.object(runtime, "run_version_check", side_effect=version_check),
            ):
                runtime.prepare(args)

            output = args.out
            staged_lock = output / "cli" / "node_modules" / "@earendil-works" / "pi-coding-agent" / "npm-shrinkwrap.json"
            staged_digest = hashlib.sha256(staged_lock.read_bytes()).hexdigest()
            source_digest = hashlib.sha256(fixture["lock_path"].read_bytes()).hexdigest()
            manifest = json.loads((output / "closure-manifest.json").read_text(encoding="utf-8"))
            lock_manifest_path = "cli/node_modules/@earendil-works/pi-coding-agent/npm-shrinkwrap.json"
            recorded_entry = next(entry for entry in manifest["entries"] if entry["path"] == lock_manifest_path)
            self.assertNotEqual(staged_digest, source_digest)
            self.assertEqual(manifest["pi"]["dependency_lock_sha256"], staged_digest)
            self.assertEqual(manifest["pi"]["dependency_lock_size"], staged_lock.stat().st_size)
            self.assertEqual(recorded_entry["sha256"], staged_digest)
            self.assertEqual(recorded_entry["size"], staged_lock.stat().st_size)

    @unittest.skipUnless(sys.platform.startswith("linux"), "the staging command is Linux-only")
    def test_existing_output_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            fixture = make_staging_fixture(root)
            output = fixture["output_parent"] / "already-there"
            output.mkdir()
            args = make_args(fixture, "already-there")
            with mock.patch.object(runtime.os, "geteuid", return_value=1000, create=True):
                with self.assertRaisesRegex(runtime.PreparationError, "output already exists; refusing to overwrite"):
                    runtime.prepare(args)

    @unittest.skipUnless(sys.platform.startswith("linux"), "symlink behavior is verified in Linux CI")
    def test_scan_rejects_symlink_escape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_root = root / "pi"
            package_root.mkdir()
            external = root / "outside.txt"
            external.write_text("outside\n", encoding="utf-8")
            (package_root / "escape.txt").symlink_to(external)
            with self.assertRaisesRegex(runtime.PreparationError, "symlink escapes official Pi package"):
                runtime.scan_tree(package_root, max_files=10, max_bytes=1024)

    def test_file_symlinks_are_materialized_as_regular_files(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_root = root / "pi"
            bin_dir = package_root / "node_modules" / ".bin"
            bin_dir.mkdir(parents=True)
            link_names = (
                "anthropic-ai-sdk", "esbuild", "jiti", "marked",
                "node-which", "pi-ai", "semver", "yaml",
            )
            links: list[Path] = []
            try:
                for index, name in enumerate(link_names):
                    target = package_root / "node_modules" / f"tool-{index}" / "bin" / "cli.js"
                    target.parent.mkdir(parents=True)
                    target.write_text(f"#!/usr/bin/env node\n// {name}\n", encoding="utf-8")
                    link = bin_dir / name
                    link.symlink_to(os.path.relpath(target, link.parent), target_is_directory=False)
                    links.append(link)
            except (OSError, NotImplementedError) as exc:
                self.skipTest(f"symlink creation is unavailable: {exc}")

            entries = runtime.scan_tree(package_root, max_files=100, max_bytes=100_000)
            source_links = [entry for entry in entries if entry["kind"] == "symlink"]
            self.assertEqual(len(source_links), len(link_names))
            self.assertTrue(all({"sha256", "size", "mode", "target"} <= entry.keys() for entry in source_links))

            staged_package = root / "staged"
            staged_entries = runtime.copy_package_tree(package_root, staged_package, entries)
            staged_links = [entry for entry in staged_entries if Path(entry["path"]).name in link_names]
            self.assertEqual(len(staged_links), len(link_names))
            self.assertTrue(all(entry["kind"] == "file" for entry in staged_links))
            for source_link in links:
                staged_link = staged_package / source_link.relative_to(package_root)
                self.assertTrue(stat.S_ISREG(staged_link.lstat().st_mode), staged_link)
                self.assertFalse(staged_link.is_symlink(), staged_link)
                self.assertEqual(staged_link.read_bytes(), source_link.resolve(strict=True).read_bytes())

    def test_directory_symlink_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package_root = root / "pi"
            target = package_root / "internal"
            target.mkdir(parents=True)
            try:
                (package_root / "directory-link").symlink_to(target, target_is_directory=True)
            except (OSError, NotImplementedError) as exc:
                self.skipTest(f"symlink creation is unavailable: {exc}")
            with self.assertRaisesRegex(runtime.PreparationError, "target is not a regular file"):
                runtime.scan_tree(package_root, max_files=10, max_bytes=1024)

    def test_copy_rejects_source_bytes_that_do_not_match_the_scanned_identity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source.txt"
            source.write_bytes(b"changed after scan")
            destination = root / "stage" / "source.txt"
            expected = {
                "sha256": hashlib.sha256(b"original bytes").hexdigest(),
                "size": len(b"original bytes"),
                "mode": 0o644,
            }
            with self.assertRaisesRegex(runtime.PreparationError, "source file changed while it was copied"):
                runtime.copy_regular_file(source, destination, expected)

class ExtensionFileListTests(unittest.TestCase):
    def test_all_four_runtime_file_lists_agree(self) -> None:
        repo = Path(__file__).resolve().parent.parent
        names = lambda text: set(re.findall(r"[\w-]+\.ts", text))  # noqa: E731
        build = (repo / "scripts/build-release.sh").read_text(encoding="utf-8")
        go = (repo / "supervisor/internal/piruntime/development_linux.go").read_text(encoding="utf-8")
        ts = (repo / "adapter/src/tbound-cli-extension.test.ts").read_text(encoding="utf-8")
        sources = {
            "build-release.sh": names(re.search(r"for runtime_source in ([^;]*);", build).group(1)),
            "development_linux.go": names(re.search(r"\{([^{}]*tbound-cli-extension\.ts[^{}]*)\}", go).group(1)),
            "tbound-cli-extension.test.ts": names(re.search(r"for \(const name of \[([^\]]*)\]", ts).group(1)),
        }
        expected = set(runtime.EXTENSION_FILES)
        self.assertEqual(len(expected), 5)
        for label, found in sources.items():
            self.assertEqual(found, expected, label)


if __name__ == "__main__":
    unittest.main()
