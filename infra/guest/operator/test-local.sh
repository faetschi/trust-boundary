#!/bin/bash
# Unprivileged regressions: extract fixture functions, never run the dispatcher preamble.
set -euo pipefail
[[ $EUID -ne 0 ]] || { echo 'Run these temporary-directory tests without root.' >&2; exit 1; }
python3 -I - "$(dirname -- "$(realpath -- "$0")")/tbound-op" "$@" <<'PY'
import grp
import os
from pathlib import Path
import pwd
import stat
import subprocess
import sys
import tempfile
import unittest

source = Path(sys.argv.pop(1)).read_text(encoding="utf-8")
fixture = source[source.index("live_listing() {"):source.index("declare -A TASK_OF=")]
# Keep production permission checks, substituting only the test user's identity.
fixture = fixture.replace("995", str(os.getuid())).replace("986", str(os.getgid()))
fixture = fixture.replace("tbound-pi:tbound-pi", pwd.getpwuid(os.getuid()).pw_name + ":" + grp.getgrgid(os.getgid()).gr_name)
smoke = "TBound smoke fixture. This file is harmless: it only checks that the read tool works.\n"
release = "version=2.4.0\nstatus=draft\n"


def snapshot(root):
    result = {}
    for p in [root, *sorted(root.rglob("*"))]:
        s = p.lstat()
        result[str(p.relative_to(root))] = (
            s.st_uid, s.st_gid, s.st_mode, s.st_mtime_ns,
            p.read_bytes() if p.is_file() and not p.is_symlink() else None,
        )
    return result


class FixtureTests(unittest.TestCase):
    def setUp(self):
        self.new_fixture()

    def new_fixture(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="tbound-op-test-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.mount = self.base / "live"
        self.mount.mkdir(mode=0o700)
        self.live = self.mount / "root"
        self.live.mkdir(mode=0o700)
        self.target = self.base / "target"
        self.target.mkdir(mode=0o750)
        (self.target / "release.txt").write_text("untouched\n")
        (self.target / "sentinel").write_text("untouched\n")

    def run_fixture(self, verb="g1", entry="v_seed_live"):
        script = """set -euo pipefail
umask 077
die() { echo "$*" >&2; exit 1; }
say() { echo "$*"; }
need_argc() { :; }
check_tools() { :; }
mkwork() { :; }
prep_ga() { :; }
need_inactive() { :; }
findmnt() { printf '%s\\n' "$LIVE_MOUNT"; }
SVC_PRIV=()
LIVE_MOUNT=$1
LIVE=$LIVE_MOUNT/root
ARGS=($2)
""" + fixture + "\nlive_report() { :; }\n" + entry + "\n"
        return subprocess.run(["bash", "-c", script, "test-local", str(self.mount), verb],
                              capture_output=True, text=True)

    def test_directory_symlinks_refused_without_target_changes(self):
        for verb in ("smoke", "g1"):
            for name in ("lost+found", "src", "out"):
                with self.subTest(verb=verb, name=name):
                    self.new_fixture()
                    link = self.live / name
                    link.symlink_to(self.target, target_is_directory=True)
                    before = snapshot(self.target)
                    live_before = snapshot(self.live)
                    got = self.run_fixture(verb)
                    self.assertNotEqual(got.returncode, 0, got.stdout)
                    self.assertEqual(snapshot(self.target), before, got.stderr)
                    self.assertEqual(snapshot(self.live), live_before, got.stderr)
                    self.assertTrue(link.is_symlink(), got.stderr)
                    link.unlink()

    def test_dangling_directory_symlinks_refused(self):
        for name in ("lost+found", "src", "out"):
            with self.subTest(name=name):
                self.new_fixture()
                link = self.live / name
                link.symlink_to(self.target / "missing", target_is_directory=True)
                got = self.run_fixture()
                self.assertNotEqual(got.returncode, 0, got.stdout)
                self.assertTrue(link.is_symlink(), got.stderr)
                self.assertFalse((self.target / "missing").exists())
                link.unlink()

    def test_live_root_symlink_refused(self):
        self.live.rmdir()
        self.live.symlink_to(self.target, target_is_directory=True)
        before = snapshot(self.target)
        got = self.run_fixture()
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertEqual(snapshot(self.target), before, got.stderr)

    def test_live_mount_symlink_refused(self):
        self.live.rmdir()
        self.mount.rmdir()
        self.mount.symlink_to(self.target, target_is_directory=True)
        before = snapshot(self.target)
        got = self.run_fixture()
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertEqual(snapshot(self.target), before, got.stderr)

    def test_nested_symlink_refused(self):
        (self.live / "out").mkdir(mode=0o755)
        (self.live / "out" / "nested").symlink_to(self.target, target_is_directory=True)
        before = snapshot(self.target)
        got = self.run_fixture("smoke")
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertEqual(snapshot(self.target), before, got.stderr)

    def test_reg_measure_fence_symlink_refused_before_changes(self):
        (self.live / "lost+found").symlink_to(self.target, target_is_directory=True)
        before = snapshot(self.target)
        got = self.run_fixture(entry="live_dir")
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertEqual(snapshot(self.target), before, got.stderr)

    def test_existing_directory_modes_are_refused_without_repair(self):
        for name in (".", "lost+found", "src", "out"):
            with self.subTest(name=name):
                self.new_fixture()
                path = self.live / name
                if name != ".":
                    path.mkdir(mode=0o700)
                path.chmod(0o750)
                before = snapshot(self.live)
                got = self.run_fixture()
                self.assertNotEqual(got.returncode, 0, got.stdout)
                self.assertEqual(snapshot(self.live), before, got.stderr)
                if name == ".":
                    path.chmod(0o700)
                else:
                    path.rmdir()

    def test_fixture_file_symlinks_refused(self):
        for verb, relative in (("g1", "src/release.txt"), ("smoke", "SMOKE-README.txt")):
            with self.subTest(verb=verb):
                self.new_fixture()
                (self.live / "src").mkdir(mode=0o755)
                link = self.live / relative
                link.symlink_to(self.target / "release.txt")
                before = snapshot(self.target)
                got = self.run_fixture(verb)
                self.assertNotEqual(got.returncode, 0, got.stdout)
                self.assertEqual(snapshot(self.target), before, got.stderr)
                self.assertTrue(link.is_symlink(), got.stderr)
                link.unlink()

    def test_missing_root_and_fixture_transitions(self):
        self.live.rmdir()
        for verb, expected in (("smoke", smoke), ("g1", release), ("g1", release), ("smoke", smoke)):
            with self.subTest(verb=verb):
                got = self.run_fixture(verb)
                self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
                file = self.live / ("SMOKE-README.txt" if verb == "smoke" else "src/release.txt")
                self.assertEqual(file.read_text(), expected)
                self.assertEqual(stat.S_IMODE(file.stat().st_mode), 0o644)
                self.assertEqual(stat.S_IMODE(self.live.stat().st_mode), 0o700)
                self.assertEqual(stat.S_IMODE((self.live / "lost+found").stat().st_mode), 0o755)
                for p in self.live.rglob("*"):
                    self.assertEqual((p.stat().st_uid, p.stat().st_gid), (os.getuid(), os.getgid()))


unittest.main(verbosity=2)
PY
