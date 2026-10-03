#!/usr/bin/env python3
"""Regression fixtures for bounded Go phase diagnostics."""

import json
from pathlib import Path
import runpy
import subprocess
import unittest
from unittest.mock import patch


_RUNNER = runpy.run_path(str(Path(__file__).with_name("verify-offline.py")))
MAX_DIAGNOSTIC_BYTES = _RUNNER["MAX_DIAGNOSTIC_BYTES"]
bounded_diagnostic_excerpt = _RUNNER["bounded_diagnostic_excerpt"]
summarize_go_phase = _RUNNER["summarize_go_phase"]
summarize_ownership_test_events = _RUNNER["summarize_ownership_test_events"]


class GoPhaseDiagnosticTests(unittest.TestCase):
    def test_excerpt_keeps_head_and_tail_within_utf8_byte_limit(self):
        output = "build-start\n" + "é" * (MAX_DIAGNOSTIC_BYTES * 2) + "\npackage-failure"

        diagnostic = bounded_diagnostic_excerpt(output)
        excerpt = diagnostic["excerpt"]

        self.assertTrue(diagnostic["excerpt_truncated"])
        self.assertEqual(diagnostic["source_bytes"], len(output.encode("utf-8")))
        self.assertEqual(diagnostic["sanitized_bytes"], len(output.encode("utf-8")))
        self.assertGreater(diagnostic["omitted_sanitized_bytes"], 0)
        self.assertLessEqual(len(excerpt.encode("utf-8")), MAX_DIAGNOSTIC_BYTES)
        self.assertTrue(excerpt.startswith("build-start\n"))
        self.assertTrue(excerpt.endswith("\npackage-failure"))

    def test_redacts_url_credentials_and_removes_controls_before_clipping(self):
        short = bounded_diagnostic_excerpt(
            "https://alice:secret@example.test/path\x00bad\r\x1b[31m"
        )
        self.assertEqual(
            short["excerpt"], "https://[REDACTED]@example.test/pathbad[31m"
        )
        self.assertNotIn("alice", short["excerpt"])
        self.assertNotIn("secret", short["excerpt"])

        head_budget = (MAX_DIAGNOSTIC_BYTES - len(
            _RUNNER["TRUNCATED_DIAGNOSTIC_MARKER"].encode("utf-8")
        )) // 2
        boundary = (
            "x" * (head_budget - 8)
            + "https://alice:secret@example.test/path"
            + "y" * MAX_DIAGNOSTIC_BYTES
        )
        clipped = bounded_diagnostic_excerpt(boundary)
        self.assertTrue(clipped["excerpt_truncated"])
        self.assertNotIn("alice", clipped["excerpt"])
        self.assertNotIn("secret", clipped["excerpt"])
        self.assertLessEqual(len(clipped["excerpt"].encode("utf-8")), MAX_DIAGNOSTIC_BYTES)

    def test_ownership_fixture_phase_requires_exactly_one_unskipped_pass(self):
        package = "tbound/supervisor/internal/audit"
        test = "TestOpenRejectsUntrustedOwnership"
        passed = "\n".join([
            json.dumps({"Action": "run", "Package": package, "Test": test}),
            json.dumps({"Action": "pass", "Package": package, "Test": test}),
            json.dumps({"Action": "pass", "Package": package}),
        ]) + "\n"
        report, failures = summarize_ownership_test_events(
            subprocess.CompletedProcess(["go", "test"], 0, passed, "")
        )
        self.assertEqual(failures, [])
        self.assertEqual(report["status"], "PASS")
        self.assertEqual(report["test_run_events"], 1)
        self.assertEqual(report["test_pass_events"], 1)
        self.assertEqual(report["skipped_tests"], [])

        skipped = "\n".join([
            json.dumps({"Action": "run", "Package": package, "Test": test}),
            json.dumps({"Action": "skip", "Package": package, "Test": test}),
            json.dumps({"Action": "pass", "Package": package}),
        ]) + "\n"
        report, failures = summarize_ownership_test_events(
            subprocess.CompletedProcess(["go", "test"], 0, skipped, "")
        )
        self.assertEqual(report["status"], "FAIL")
        self.assertIn("ownership_test_skipped", failures)
        self.assertIn("ownership_test_did_not_pass_exactly_once", failures)

    def test_ownership_fixture_phase_rejects_extra_tests_and_malformed_events(self):
        package = "tbound/supervisor/internal/audit"
        test = "TestOpenRejectsUntrustedOwnership"
        output = "\n".join([
            json.dumps({"Action": "run", "Package": package, "Test": test}),
            json.dumps({"Action": "pass", "Package": package, "Test": test}),
            json.dumps({"Action": "run", "Package": package, "Test": "TestUnexpected"}),
            json.dumps({"Action": "pass", "Package": package}),
            "{not-json",
        ]) + "\n"
        report, failures = summarize_ownership_test_events(
            subprocess.CompletedProcess(["go", "test"], 0, output, "")
        )
        self.assertEqual(report["status"], "FAIL")
        self.assertIn("invalid_go_test_json", failures)
        self.assertIn("ownership_test_not_run_exactly_once", failures)

    def test_normal_and_race_reports_retain_package_results_without_changing_gates(self):
        package = "tbound/supervisor/internal/example"
        stdout = "\n".join([
            json.dumps({"Action": "run", "Package": package, "Test": "TestExample"}),
            json.dumps({"Action": "pass", "Package": package, "Test": "TestExample"}),
            json.dumps({"Action": "pass", "Package": package}),
        ]) + "\n"
        result = subprocess.CompletedProcess(
            ["go", "test"], 1, stdout, "build detail: compilation failed\n"
        )
        expected_failures = ["unexpected_skips", "go_test_failed"]
        for name, command in (
            ("normal", ["go", "test", "-json", "./..."]),
            ("race", ["go", "test", "-race", "-json", "./..."]),
        ):
            with self.subTest(name=name):
                with patch.dict(summarize_go_phase.__globals__, {"run": lambda *_args, **_kwargs: result}):
                    entry, failures = summarize_go_phase(
                        command,
                        Path("/unused"),
                        {},
                        {package},
                        allow_ownership_skip=True,
                    )
                self.assertEqual(failures, expected_failures)
                self.assertEqual(entry["failures"], expected_failures)
                self.assertEqual(entry["status"], "FAIL")
                self.assertEqual(entry["package_results"], {package: "pass"})
                self.assertEqual(entry["diagnostics"]["stdout"]["excerpt"], stdout)
                self.assertEqual(
                    entry["diagnostics"]["stderr"]["excerpt"], result.stderr
                )


if __name__ == "__main__":
    unittest.main()
