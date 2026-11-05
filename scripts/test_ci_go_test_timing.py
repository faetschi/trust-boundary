from __future__ import annotations

import json
import contextlib
import io
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import ci_go_test_timing as timing  # noqa: E402


def write_go_test_stream(test_case: unittest.TestCase, lines: list[object]) -> Path:
    """Write each event as one JSON line. A str item is written unchanged, to model a broken line."""
    directory = tempfile.TemporaryDirectory()
    test_case.addCleanup(directory.cleanup)
    stream_path = Path(directory.name) / "go-test.jsonl"
    stream_path.write_text(
        "".join((line if isinstance(line, str) else json.dumps(line)) + "\n" for line in lines),
        encoding="utf-8",
    )
    return stream_path


class GoTestTimingSummaryTest(unittest.TestCase):
    def test_artifact_write_failure_does_not_hide_test_diagnostics(self) -> None:
        stream_path = write_go_test_stream(self, [
            {"Action": "output", "Package": "example.com/a", "Output": "failure diagnostic\n"},
            {"Action": "fail", "Package": "example.com/a", "Elapsed": 1},
        ])
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(OSError):
            timing.main(["--suite", "unit", "--go-test-json", str(stream_path),
                         "--timing-json", str(stream_path.parent)])
        self.assertIn("failure diagnostic", output.getvalue())

    def test_late_diagnostic_attributed_to_a_passed_test_remains_in_the_job_log(self) -> None:
        stream_path = write_go_test_stream(self, [
            {"Action": "pass", "Package": "example.com/a", "Test": "TestFinished", "Elapsed": 0},
            {"Action": "output", "Package": "example.com/a", "Test": "TestFinished", "Output": "late race diagnostic\n"},
            {"Action": "fail", "Package": "example.com/a", "Elapsed": 1},
        ])
        self.assertIn("late race diagnostic", timing.summarize_go_test_json(stream_path, "race").failure_output)

    def test_timeout_output_without_a_test_result_remains_in_the_job_log(self) -> None:
        stream_path = write_go_test_stream(self, [
            {"Action": "run", "Package": "example.com/a", "Test": "TestHung"},
            {"Action": "output", "Package": "example.com/a", "Test": "TestHung", "Output": "panic: test timed out\n"},
            {"Action": "fail", "Package": "example.com/a", "Elapsed": 1},
        ])
        summary = timing.summarize_go_test_json(stream_path, "unit")
        self.assertIn("test timed out", summary.failure_output)
        self.assertNotIn("Output", json.dumps(summary.timing_document))

    def test_compiler_errors_without_a_package_field_remain_in_the_job_log(self) -> None:
        stream_path = write_go_test_stream(self, [
            {"Action": "build-output", "ImportPath": "example.com/dependency", "Output": "undefined: missing\n"},
            {"Action": "build-fail", "ImportPath": "example.com/dependency"},
            {"Action": "fail", "Package": "example.com/target", "FailedBuild": "example.com/dependency", "Elapsed": 0},
        ])
        summary = timing.summarize_go_test_json(stream_path, "unit")
        self.assertIn("undefined: missing", summary.failure_output)
        self.assertEqual(summary.timing_document["packages"][0]["result"], "fail")
        self.assertNotIn("Output", json.dumps(summary.timing_document))

    def test_failed_package_output_stays_in_the_log_and_out_of_timing_artifacts(self) -> None:
        stream_path = write_go_test_stream(
            self,
            [
                {"Action": "output", "Package": "example.com/a", "Test": "TestFine", "Output": "fine noise\n"},
                {"Action": "pass", "Package": "example.com/a", "Test": "TestFine", "Elapsed": 0.5},
                {"Action": "output", "Package": "example.com/a", "Test": "TestBoom", "Output": "boom: want 1, got 2\n"},
                {"Action": "fail", "Package": "example.com/a", "Test": "TestBoom", "Elapsed": 1.25},
                {"Action": "output", "Package": "example.com/a", "Output": "FAIL\texample.com/a\t1.3s\n"},
                {"Action": "fail", "Package": "example.com/a", "Elapsed": 1.3},
            ],
        )

        summary = timing.summarize_go_test_json(stream_path, "unit")

        self.assertIn(("example.com/a", "TestBoom", "fail"), [(r.package, r.test, r.action) for r in summary.results])
        self.assertIn("| example.com/a | TestBoom | 1.25s |", summary.markdown)
        self.assertIn("boom: want 1, got 2", summary.failure_output)
        self.assertIn("FAIL\texample.com/a\t1.3s", summary.failure_output)
        self.assertIn("fine noise", summary.failure_output)
        self.assertNotIn("Output", json.dumps(summary.timing_document))
        for raw_output in ("boom: want 1, got 2", "fine noise"):
            self.assertNotIn(raw_output, json.dumps(summary.timing_document))
            self.assertNotIn(raw_output, summary.markdown)

    def test_events_missing_required_fields_fail_closed_and_name_the_problem(self) -> None:
        cases = {
            "Elapsed": [{"Action": "pass", "Package": "example.com/a", "Test": "TestX"}],
            "Action": [{"Package": "example.com/a", "Test": "TestX", "Elapsed": 0.1}],
            "Package": [{"Action": "pass", "Test": "TestX", "Elapsed": 0.1}],
            "JSON test event": ["this is not json"],
            "no test events": [],
        }
        for expected_message, lines in cases.items():
            with self.subTest(expected_message=expected_message):
                stream_path = write_go_test_stream(self, lines)
                with self.assertRaisesRegex(timing.GoTestJsonError, expected_message):
                    timing.summarize_go_test_json(stream_path, "unit")

    def test_slowest_rows_come_first_and_a_package_without_tests_counts_as_skip(self) -> None:
        stream_path = write_go_test_stream(
            self,
            [
                {"Action": "pass", "Package": "example.com/fast", "Elapsed": 2.0},
                {
                    "Action": "output",
                    "Package": "example.com/empty",
                    "Output": "?   \texample.com/empty\t[no test files]\n",
                },
                {"Action": "skip", "Package": "example.com/empty", "Elapsed": 0},
                {"Action": "pass", "Package": "example.com/slow", "Elapsed": 9.0},
            ],
        )

        summary = timing.summarize_go_test_json(stream_path, "race")

        self.assertIn(("example.com/empty", None, "skip"), [(r.package, r.test, r.action) for r in summary.results])
        self.assertIn("| 1 | example.com/slow | pass | 9.00s |", summary.markdown)
        self.assertLess(
            summary.markdown.index("example.com/slow | pass"),
            summary.markdown.index("example.com/fast | pass"),
        )
        self.assertEqual(
            [entry["package"] for entry in summary.timing_document["packages"]],
            ["example.com/empty", "example.com/fast", "example.com/slow"],
        )
        self.assertEqual(summary.failure_output, "")


if __name__ == "__main__":
    unittest.main()
