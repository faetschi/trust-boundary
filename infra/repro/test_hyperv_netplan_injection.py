#!/usr/bin/env python3
"""Offline regression for the Netplan block embedded in the Hyper-V injector."""
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
POWERSHELL = ROOT / "infra" / "repro" / "bootstrap-hyperv-windows.ps1"

# Extract the actual shell block from the PowerShell here-string, following
# the source-extraction pattern used by test_apt_trust_guard.py.
source = POWERSHELL.read_text(encoding="utf-8-sig")
here_start = source.index("$injectScript = @'\n") + len("$injectScript = @'\n")
here_end = source.index("\n'@", here_start)
injector = source[here_start:here_end]

block_start = injector.index(
    'if [[ "$MODE" == "golden" ]]; then\n'
    "  # A cloned golden disk must become an independent machine: netplan,"
)
block_end = injector.index(
    '  if [[ -z "$KEEP_HOST_KEYS" ]]; then',
    block_start,
)
NETPLAN_BLOCK = injector[block_start:block_end]

BASH = shutil.which("bash")
NETPLAN = shutil.which("netplan")


class HyperVNetplanInjectionTests(unittest.TestCase):
    def setUp(self):
        if not BASH:
            self.fail("bash is required; run this test inside the Ubuntu WSL distro")

    def run_block(self, root):
        script = r'''
set -Eeuo pipefail
MNT="$TBOUND_TEST_ROOT"
MODE=golden
die() { printf 'INJECTION ERROR: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
''' + NETPLAN_BLOCK + "\nfi\n"
        env = {
            "PATH": os.environ.get("PATH", "/usr/sbin:/usr/bin:/sbin:/bin"),
            "LC_ALL": "C",
            "TMPDIR": str(root / "tmp"),
            "TBOUND_TEST_ROOT": str(root),
        }
        return subprocess.run(
            [BASH, "-c", script],
            env=env,
            text=True,
            capture_output=True,
            timeout=10,
            check=False,
        )

    def run_netplan(self, root, *args):
        env = {
            "PATH": os.environ.get("PATH", "/usr/sbin:/usr/bin:/sbin:/bin"),
            "LC_ALL": "C",
            "TMPDIR": str(root / "tmp"),
        }
        return subprocess.run(
            [NETPLAN, *args, "--root-dir", str(root)],
            env=env,
            text=True,
            capture_output=True,
            timeout=20,
            check=False,
        )

    def make_root(self, base):
        root = base / "guest"
        (root / "etc" / "netplan").mkdir(parents=True)
        (root / "tmp").mkdir()
        return root

    def test_replaces_all_installer_yaml_and_generates_hyperv_dhcp(self):
        if not NETPLAN:
            self.fail("netplan is required; run this test inside the Ubuntu WSL distro")
        with tempfile.TemporaryDirectory(prefix="tbound-netplan-") as temp:
            root = self.make_root(Path(temp))
            netplan_dir = root / "etc" / "netplan"
            mac_bound = netplan_dir / "00-installer-config.yaml"
            static_other_name = netplan_dir / "50-custom-static.yml"
            mac_bound_text = """\
network:
  version: 2
  ethernets:
    ens160:
      match:
        macaddress: "00:11:22:33:44:55"
      addresses: [192.0.2.10/24]
"""
            static_text = """\
network:
  version: 2
  ethernets:
    enp3s0:
      addresses: [198.51.100.20/24]
"""
            mac_bound.write_text(mac_bound_text, encoding="utf-8")
            static_other_name.write_text(static_text, encoding="utf-8")

            result = self.run_block(root)
            self.assertEqual(result.returncode, 0, result.stderr)

            self.assertFalse(mac_bound.exists())
            self.assertFalse(static_other_name.exists())
            self.assertEqual(
                mac_bound.with_name(mac_bound.name + ".installer-orig").read_text(
                    encoding="utf-8"
                ),
                mac_bound_text,
            )
            self.assertEqual(
                static_other_name.with_name(
                    static_other_name.name + ".installer-orig"
                ).read_text(encoding="utf-8"),
                static_text,
            )

            active_files = sorted(
                path.name
                for pattern in ("*.yaml", "*.yml")
                for path in netplan_dir.glob(pattern)
            )
            self.assertEqual(active_files, ["99-tbound-dhcp.yaml"])
            managed = netplan_dir / "99-tbound-dhcp.yaml"
            self.assertEqual(stat.S_IMODE(managed.stat().st_mode), 0o600)

            effective = self.run_netplan(root, "get")
            self.assertEqual(effective.returncode, 0, effective.stderr)
            self.assertRegex(effective.stdout, r"(?m)^\s*renderer:\s*networkd\s*$")
            self.assertRegex(effective.stdout, r'(?m)^\s*driver:\s*"?hv_netvsc"?\s*$')
            self.assertRegex(effective.stdout, r"(?m)^\s*dhcp4:\s*true\s*$")

            generated = self.run_netplan(root, "generate")
            self.assertEqual(generated.returncode, 0, generated.stderr)

    def test_existing_installer_backup_refuses_without_overwriting(self):
        with tempfile.TemporaryDirectory(prefix="tbound-netplan-") as temp:
            root = self.make_root(Path(temp))
            netplan_dir = root / "etc" / "netplan"
            original = netplan_dir / "00-installer-config.yaml"
            backup = netplan_dir / "00-installer-config.yaml.installer-orig"
            original_text = "installer config\n"
            backup_text = "pre-existing backup\n"
            original.write_text(original_text, encoding="utf-8")
            backup.write_text(backup_text, encoding="utf-8")

            result = self.run_block(root)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Netplan backup already exists", result.stderr)
            self.assertEqual(original.read_text(encoding="utf-8"), original_text)
            self.assertEqual(backup.read_text(encoding="utf-8"), backup_text)
            self.assertFalse((netplan_dir / "99-tbound-dhcp.yaml").exists())

    def test_dangling_managed_symlinks_are_moved_without_writing_targets(self):
        for target_kind in ("absolute", "relative"):
            with self.subTest(target_kind=target_kind):
                with tempfile.TemporaryDirectory(prefix="tbound-netplan-") as temp:
                    base = Path(temp)
                    root = self.make_root(base)
                    netplan_dir = root / "etc" / "netplan"
                    managed = netplan_dir / "99-tbound-dhcp.yaml"
                    target = base / f"outside-{target_kind}-target"
                    link_target = (
                        str(target)
                        if target_kind == "absolute"
                        else os.path.relpath(target, netplan_dir)
                    )
                    os.symlink(link_target, managed)

                    result = self.run_block(root)
                    self.assertEqual(result.returncode, 0, result.stderr)

                    backup = netplan_dir / "99-tbound-dhcp.yaml.installer-orig"
                    self.assertTrue(os.path.islink(backup))
                    self.assertEqual(os.readlink(backup), link_target)
                    self.assertFalse(os.path.islink(managed))
                    self.assertTrue(stat.S_ISREG(managed.lstat().st_mode))
                    self.assertEqual(stat.S_IMODE(managed.stat().st_mode), 0o600)
                    self.assertFalse(os.path.lexists(str(target)))

    def test_dangling_backup_symlink_collision_refuses_without_writes(self):
        with tempfile.TemporaryDirectory(prefix="tbound-netplan-") as temp:
            base = Path(temp)
            root = self.make_root(base)
            netplan_dir = root / "etc" / "netplan"
            original = netplan_dir / "00-installer-config.yaml"
            backup = netplan_dir / "00-installer-config.yaml.installer-orig"
            original_text = "installer config\n"
            outside_target = base / "outside-backup-target"
            original.write_text(original_text, encoding="utf-8")
            os.symlink(str(outside_target), backup)

            result = self.run_block(root)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Netplan backup already exists", result.stderr)
            self.assertEqual(original.read_text(encoding="utf-8"), original_text)
            self.assertTrue(os.path.islink(backup))
            self.assertEqual(os.readlink(backup), str(outside_target))
            self.assertFalse(os.path.lexists(str(outside_target)))
            self.assertFalse(
                os.path.lexists(str(netplan_dir / "99-tbound-dhcp.yaml"))
            )

    def test_symlinked_netplan_parent_refuses_without_writing_target(self):
        with tempfile.TemporaryDirectory(prefix="tbound-netplan-") as temp:
            base = Path(temp)
            root = self.make_root(base)
            netplan_dir = root / "etc" / "netplan"
            outside_dir = base / "outside-netplan"
            outside_dir.mkdir()
            netplan_dir.rmdir()
            os.symlink(str(outside_dir), netplan_dir, target_is_directory=True)

            result = self.run_block(root)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(
                "Symlinked Netplan parent directory is not accepted",
                result.stderr,
            )
            self.assertTrue(netplan_dir.is_symlink())
            self.assertEqual(os.readlink(netplan_dir), str(outside_dir))
            self.assertFalse(
                os.path.lexists(str(outside_dir / "99-tbound-dhcp.yaml"))
            )


if __name__ == "__main__":
    unittest.main()
