"""Offline regressions for read-only current-scope cgroup inventory."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SOURCE = Path(os.environ.get(
    "CHECK_GOVERNED_HOST_SOURCE", Path(__file__).with_name("check-governed-host.sh")
)).read_text()


class CgroupInventoryTests(unittest.TestCase):
    def inventory(self, *, controls=True, kind="domain", rel=None, scope_name="user.scope"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            hierarchy = root / "cgroup2"
            scope = hierarchy / scope_name
            scope.mkdir(parents=True)
            # A healthy hierarchy does not establish delegation to this scope.
            (hierarchy / "cgroup.controllers").write_text("cpu memory pids\n")
            (hierarchy / "cgroup.events").write_text("populated 1\n")
            (scope / "cgroup.controllers").write_text("\n")
            (scope / "cgroup.type").write_text(kind + "\n")
            (scope / "cgroup.events").write_text("populated 0\n")
            if controls:
                for name in ("cgroup.kill", "memory.max", "pids.max"):
                    (scope / name).write_text("max\n")
            proc = root / "membership"
            mounts = root / "mountinfo"
            proc.write_text("0::" + ("/" + scope_name if rel is None else rel) + "\n")
            mounts.write_text(f"1 0 0:1 / {hierarchy} rw - cgroup2 cgroup rw\n")
            # Isolate the inventory block; substitute procfs paths only in this
            # private test copy. The installed script has no fixture overrides.
            start = SOURCE.index("# Current-scope cgroup") if "# Current-scope cgroup" in SOURCE else SOURCE.index("# cgroup v2")
            block = SOURCE[start:SOURCE.index("# Containment + image tooling.")]
            block = block.replace("/sys/fs/cgroup", str(hierarchy))
            block = block.replace("/proc/self/cgroup", str(proc))
            block = block.replace("/proc/self/mountinfo", str(mounts))
            functions = SOURCE[SOURCE.index("pass=0;"):SOURCE.index('echo "tbound governed-host')]
            run = subprocess.run(["bash", "-c", "set -uo pipefail\n" + functions + block],
                                 capture_output=True, text=True, check=True)
            # Reading the inventory must never write kill/limit controls.
            if controls:
                self.assertEqual((scope / "cgroup.kill").read_text(), "max\n")
            return run.stdout

    def test_uses_current_scope_without_requiring_root_kill(self):
        output = self.inventory()
        self.assertIn("user.scope", output)
        self.assertIn("available in current scope: none", output)
        self.assertNotIn("not present at root", output)
        self.assertIn("[WARN] cgroup-delegation", output)
        self.assertIn("[WARN] cgroup-reuse", output)

    def test_root_controllers_do_not_supply_missing_scope_controls(self):
        output = self.inventory(controls=False)
        for name in ("cgroup.kill", "memory.max", "pids.max"):
            self.assertIn("[WARN] " + name, output)
        self.assertNotIn("[PASS] cgroup-delegation", output)

    def test_threaded_scope_is_not_a_domain(self):
        output = self.inventory(kind="threaded")
        self.assertIn("[FAIL] cgroup-type", output)
        self.assertIn("threaded (need domain)", output)

    def test_membership_preserves_colons_in_scope_name(self):
        output = self.inventory(scope_name="user.scope:leaf")
        self.assertIn("user.scope:leaf", output)
        self.assertIn("[PASS] cgroup-type", output)
        self.assertNotIn("unreadable", output)

    def test_unresolved_membership_is_not_success(self):
        for rel in ("", "/../other"):
            with self.subTest(rel=rel):
                output = self.inventory(rel=rel)
                self.assertIn("[WARN] cgroup-scope", output)
                self.assertNotIn("[PASS] cgroup-scope", output)


if __name__ == "__main__":
    unittest.main()
