#!/usr/bin/env python3
"""Regression tests for safe Go module graph failure diagnostics."""

from pathlib import Path
import runpy
import subprocess
import unittest


go_module_graph_failure_diagnostic = runpy.run_path(
    str(Path(__file__).with_name("verify-offline.py"))
)["go_module_graph_failure_diagnostic"]


class GoModuleGraphDiagnosticTests(unittest.TestCase):
    def test_nonzero_result_has_structured_diagnostic(self):
        result = subprocess.CompletedProcess(
            ["go", "list", "-m", "all"], 17, "", "go: module graph unavailable"
        )

        self.assertEqual(
            go_module_graph_failure_diagnostic(result),
            {
                "command": "go list -m all",
                "exit_code": 17,
                "stderr_excerpt": "go: module graph unavailable",
                "truncated": False,
            },
        )

    def test_empty_stderr_is_an_empty_untruncated_excerpt(self):
        result = subprocess.CompletedProcess(["go"], 1, "", "")

        diagnostic = go_module_graph_failure_diagnostic(result)

        self.assertEqual(diagnostic["stderr_excerpt"], "")
        self.assertFalse(diagnostic["truncated"])

    def test_utf8_excerpt_stays_within_byte_limit(self):
        result = subprocess.CompletedProcess(["go"], 1, "", "a" + "é" * 1024)

        diagnostic = go_module_graph_failure_diagnostic(result)

        self.assertTrue(diagnostic["truncated"])
        self.assertLessEqual(len(diagnostic["stderr_excerpt"].encode("utf-8")), 2048)
        self.assertTrue(diagnostic["stderr_excerpt"].endswith("é"))

    def test_url_userinfo_is_redacted(self):
        result = subprocess.CompletedProcess(
            ["go"], 1, "", "go: https://alice:secret@example.test/path: denied"
        )

        excerpt = go_module_graph_failure_diagnostic(result)["stderr_excerpt"]

        self.assertIn("https://[REDACTED]@example.test/path", excerpt)
        self.assertNotIn("alice", excerpt)
        self.assertNotIn("secret", excerpt)

    def test_unsafe_control_characters_are_removed(self):
        result = subprocess.CompletedProcess(["go"], 1, "", "go:\x00bad\r\x1b[31m\x7f\x85")

        excerpt = go_module_graph_failure_diagnostic(result)["stderr_excerpt"]

        self.assertEqual(excerpt, "go:bad[31m")
        self.assertFalse(any(ord(character) < 32 and character not in "\t\n" for character in excerpt))
        self.assertNotIn("\x7f", excerpt)
        self.assertNotIn("\x85", excerpt)


if __name__ == "__main__":
    unittest.main()