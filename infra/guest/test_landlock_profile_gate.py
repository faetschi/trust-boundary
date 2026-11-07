#!/usr/bin/env python3
"""Offline regressions for the Landlock ABI classifier and host exit gates."""
import ctypes
import errno
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
PROBE = ROOT / "infra" / "guest" / "probe-capabilities.sh"
SHELL_GATE = ROOT / "infra" / "repro" / "landlock-profile-gate.sh"
POWERSHELL_TEST = ROOT / "infra" / "repro" / "test_landlock_profile_gate.ps1"

# Follow the source-extraction convention used by test_apt_trust_guard.py.
source = PROBE.read_text(encoding="utf-8")
start_marker = "# Classify Landlock availability independently from the registered D09 profile."
end_marker = "# Landlock ABI query only asks for the kernel ABI number; no ruleset is created."
start = source.index(start_marker)
end = source.index(end_marker, start)
LANDLOCK_GATE = compile(source[start:end], f"{PROBE}: Landlock classifier", "exec")


class FakeSyscall:
    restype = None

    def __init__(self, value):
        self.value = value

    def __call__(self, *_args):
        return self.value


class FakeLibc:
    def __init__(self, value):
        self.syscall = FakeSyscall(value)


def classify_query(value=None, error=None, unmapped=False):
    namespace = {
        "ctypes": ctypes,
        "errno": errno,
        "os": __import__("os"),
        "platform": __import__("platform"),
    }
    exec(LANDLOCK_GATE, namespace)
    if unmapped:
        return namespace["classify_landlock_abi"]()
    if value is not None and value > 0:
        return namespace["query_landlock_abi"](FakeLibc(value), 444)
    with patch("ctypes.get_errno", return_value=error):
        return namespace["query_landlock_abi"](FakeLibc(-1), 444)


def report_row(status, name, detail):
    return f"{status:12} {name}: {detail}"


def report_for(abi=None, profile_status=None, extra_rows=()):
    rows = []
    if abi is not None:
        rows.append(report_row("PASS", "landlock-abi", f"landlock_create_ruleset VERSION query returned ABI {abi}"))
    if profile_status:
        detail = "registered D09 profile requires exactly Landlock ABI 8"
        if profile_status == "FAIL":
            detail += f"; kernel reports ABI {abi}"
        rows.append(report_row(profile_status, "d09-landlock-profile", detail))
    rows.extend(extra_rows)
    return "\n".join(rows) + "\n"


def run_shell_gate(text, probe_rc, report_kind="file"):
    bash = shutil.which("bash")
    if not bash:
        raise AssertionError("bash is required to execute the production shell classifier")
    with tempfile.TemporaryDirectory(prefix="tbound-landlock-gate-") as temp:
        report_path = Path(temp) / "probe.txt"
        if report_kind == "directory":
            report_path.mkdir()
        elif report_kind != "missing":
            report_path.write_text(text, encoding="utf-8")
        completed = subprocess.run(
            [bash, "-c", 'set -eu; . "$1"; tbound_classify_probe_report "$2" "$3"',
             "landlock-test", str(SHELL_GATE), str(report_path), str(probe_rc)],
            text=True, capture_output=True, check=False, timeout=10,
        )
    if completed.returncode != 0:
        raise AssertionError(f"shell classifier failed: {completed.stderr}")
    return int(completed.stdout.strip())


class LandlockProfileGateTests(unittest.TestCase):
    def assert_rows(self, rows, expected):
        self.assertEqual([(name, status) for name, status, _ in rows], expected)

    def test_query_classifier_accepts_exact_abi_only_for_d09(self):
        cases = {
            4: [("landlock-abi", "PASS"), ("d09-landlock-profile", "FAIL")],
            8: [("landlock-abi", "PASS"), ("d09-landlock-profile", "PASS")],
            9: [("landlock-abi", "PASS"), ("d09-landlock-profile", "FAIL")],
        }
        for abi, expected in cases.items():
            with self.subTest(abi=abi):
                rows = classify_query(value=abi)
                self.assert_rows(rows, expected)

    def test_unavailable_and_denied_queries_keep_distinct_statuses(self):
        for error in (errno.ENOSYS, errno.EOPNOTSUPP):
            with self.subTest(error=error):
                self.assert_rows(
                    classify_query(error=error),
                    [("landlock-abi", "FAIL"), ("d09-landlock-profile", "FAIL")],
                )
        for error in (errno.EPERM, errno.EACCES):
            with self.subTest(error=error):
                self.assert_rows(
                    classify_query(error=error),
                    [("landlock-abi", "UNVERIFIED"), ("d09-landlock-profile", "UNVERIFIED")],
                )

    def test_other_query_error_and_unmapped_architecture_are_not_profile_passes(self):
        self.assert_rows(
            classify_query(error=errno.EIO),
            [("landlock-abi", "FAIL"), ("d09-landlock-profile", "UNVERIFIED")],
        )
        self.assert_rows(
            classify_query(unmapped=True),
            [("landlock-abi", "UNVERIFIED"), ("d09-landlock-profile", "UNVERIFIED")],
        )

    def test_shell_bootstrap_exit_classifier_reads_all_report_rows(self):
        by_design = (
            report_row("UNVERIFIED", "cgroup.kill-write", "not written"),
            report_row("UNVERIFIED", "uidmap-helpers", "behavior not inferred"),
        )
        cases = (
            ("ABI 4", report_for(4, "FAIL", by_design), 1, 3),
            ("ABI 8", report_for(8, "PASS", by_design), 2, 0),
            ("ABI 9 leading zero", report_for("0009", "FAIL"), 1, 3),
            ("mismatch plus unexpected UNVERIFIED",
             report_for(4, "FAIL", by_design + (report_row("UNVERIFIED", "extra-check", "unknown"),)), 1, 1),
            ("mismatch plus another FAIL",
             report_for(4, "FAIL", (report_row("FAIL", "other-check", "failed"),)), 1, 1),
            ("existing FAIL preserved",
             report_for(8, "PASS", (report_row("FAIL", "user-namespace", "failed"),
                                    report_row("UNVERIFIED", "extra-check", "unknown"))), 1, 1),
            ("unavailable is not ABI mismatch",
             "\n".join((report_row("FAIL", "landlock-abi", "unavailable"),
                        report_row("FAIL", "d09-landlock-profile", "unavailable"))) + "\n", 1, 1),
            ("denied query is unexpected UNVERIFIED",
             "\n".join((report_row("UNVERIFIED", "landlock-abi", "denied"),
                        report_row("UNVERIFIED", "d09-landlock-profile", "denied"))) + "\n", 2, 4),
            ("other error preserves FAIL precedence",
             "\n".join((report_row("FAIL", "landlock-abi", "query failed"),
                        report_row("UNVERIFIED", "d09-landlock-profile", "unmeasured"))) + "\n", 1, 1),
        )
        for name, text, probe_rc, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(run_shell_gate(text, probe_rc), expected)

    def test_shell_invalid_or_contradictory_probe_output_is_invocation_error(self):
        valid_pass = report_for(8, "PASS")
        cases = (
            ("missing both gate rows", report_row("PASS", "other-check", "passed"), "0", "file", 2),
            ("missing profile row", report_for(8), "0", "file", 2),
            ("duplicate generic row", valid_pass + report_row("PASS", "landlock-abi", "ABI 8"), "0", "file", 2),
            ("duplicate profile row", valid_pass + report_row("PASS", "d09-landlock-profile", "requires exactly Landlock ABI 8"), "0", "file", 2),
            ("contradictory gate rows", report_for(9, "PASS"), "0", "file", 2),
            ("missing report", None, "0", "missing", 2),
            ("unreadable report path", None, "0", "directory", 2),
            ("rowless report", "", "0", "file", 2),
            ("non-numeric probe exit", valid_pass, "bad", "file", 2),
            ("unsupported probe exit", valid_pass, "3", "file", 2),
            ("non-decimal ABI", report_for("not-a-number", "PASS"), "0", "file", 2),
            ("PASS exit contradicts UNVERIFIED row",
             report_for(8, "PASS", (report_row("UNVERIFIED", "extra-check", "unknown"),)),
             "0", "file", 2),
            ("UNVERIFIED exit contradicts PASS-only rows", valid_pass, "2", "file", 2),
            ("FAIL still takes precedence over invalid probe exit",
             report_for(8, "PASS", (report_row("FAIL", "other-check", "failed"),)),
             "bad", "file", 1),
        )
        for name, text, probe_rc, report_kind, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(
                    run_shell_gate(text, probe_rc, report_kind),
                    expected,
                )

    def test_powershell_report_exit_classifier_cases(self):
        # probe-host.ps1 is Windows host tooling (drive-letter paths, /mnt translation).
        if sys.platform != "win32":
            self.skipTest("Windows host tooling; runs on Windows only")
        powershell = shutil.which("powershell") or shutil.which("pwsh")
        if not powershell:
            self.skipTest("Windows PowerShell or pwsh is unavailable")
        completed = subprocess.run(
            [powershell, "-NoProfile", "-NonInteractive", "-File", str(POWERSHELL_TEST)],
            text=True, capture_output=True, check=False, timeout=20,
        )
        self.assertEqual(completed.returncode, 0, completed.stdout + completed.stderr)
        self.assertIn("PowerShell Landlock profile gate cases passed.", completed.stdout)

        # Exercise the real File wrapper and its cosign row append using only
        # a temporary saved capture; this does not contact a host or read keys.
        with tempfile.TemporaryDirectory(prefix="tbound-landlock-file-wrapper-") as temp:
            capture = Path(temp) / "probe.txt"
            capture.write_text(report_for(8, "PASS"), encoding="utf-8")
            wrapper = subprocess.run(
                [
                    powershell, "-NoProfile", "-NonInteractive", "-File",
                    str(ROOT / "infra" / "repro" / "probe-host.ps1"),
                    "-Target", "File", "-ReportFile", str(capture),
                ],
                text=True, capture_output=True, check=False, timeout=20,
            )
        self.assertEqual(wrapper.returncode, 0, wrapper.stdout + wrapper.stderr)
        self.assertRegex(
            wrapper.stdout,
            r"(?m)^cosign-cli\s+UNVERIFIED\s+not observable from a saved capture",
        )
        self.assertIn("Summary: PASS=2 FAIL=0 UNVERIFIED=1", wrapper.stdout)


if __name__ == "__main__":
    unittest.main()
