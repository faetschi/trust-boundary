#!/usr/bin/env python3
"""Summarize one `go test -json` stream from a CI Go suite.

The go test step decides pass or fail; this script only reports. It writes a timing
document with no test Output, appends a markdown summary to the step summary, and
prints the Output of failed tests and packages to the job log. A malformed event
exits 1 with the line and field named, as the evidence reader in
supervisor/cmd/tbound-run does: nothing is guessed.
"""

from __future__ import annotations

import argparse
from collections import Counter
from dataclasses import dataclass
import json
from pathlib import Path
import sys

TIMING_DOCUMENT_SCHEMA_VERSION = 1
SLOWEST_PACKAGE_ROW_COUNT = 10
SLOWEST_TEST_ROW_COUNT = 20
# shortcut: the step summary lists 200 failed results, upgrade when a run needs every failure there
# (the timing artifact keeps all results, so nothing is lost).
FAILED_ROW_LIMIT = 200
RESULT_ACTIONS = ("pass", "fail", "skip")


class GoTestJsonError(ValueError):
    """A stream line or event that cannot be summarized. The message names the line and the field."""


@dataclass(frozen=True)
class GoTestResult:
    package: str
    test: str | None  # None is the package-level result
    action: str  # pass, fail or skip
    elapsed_seconds: float


@dataclass(frozen=True)
class SuiteSummary:
    results: list[GoTestResult]
    failure_output: str
    markdown: str
    timing_document: dict[str, object]


def iterate_events(go_test_json_path: Path):
    """Yield (line_number, event) for each non-blank line after checking the fields every event needs."""
    with go_test_json_path.open(encoding="utf-8") as stream:
        for line_number, raw_line in enumerate(stream, start=1):
            if not raw_line.strip():
                continue
            try:
                event = json.loads(raw_line)
            except json.JSONDecodeError as error:
                raise GoTestJsonError(f"line {line_number} is not a JSON test event: {error.msg}") from error
            if not isinstance(event, dict):
                raise GoTestJsonError(f"line {line_number} is not a JSON object")
            if event.get("Action") in ("build-output", "build-fail"):
                if not isinstance(event.get("ImportPath"), str) or not event["ImportPath"]:
                    raise GoTestJsonError(f"line {line_number} has no ImportPath")
                if event["Action"] == "build-output" and not isinstance(event.get("Output"), str):
                    raise GoTestJsonError(f"line {line_number} is a build-output event with no Output")
                yield line_number, event
                continue
            for required_field in ("Action", "Package"):
                if not isinstance(event.get(required_field), str) or not event[required_field]:
                    raise GoTestJsonError(f"line {line_number} has no {required_field}")
            if "Test" in event and not isinstance(event["Test"], str):
                raise GoTestJsonError(f"line {line_number} has a Test that is not a string")
            if event["Action"] == "output" and not isinstance(event.get("Output"), str):
                raise GoTestJsonError(f"line {line_number} is an output event with no Output")
            yield line_number, event


def collect_results(go_test_json_path: Path) -> list[GoTestResult]:
    results: list[GoTestResult] = []
    event_count = 0
    for line_number, event in iterate_events(go_test_json_path):
        event_count += 1
        action = event["Action"]
        if action not in RESULT_ACTIONS:
            continue
        elapsed = event.get("Elapsed")
        if isinstance(elapsed, bool) or not isinstance(elapsed, (int, float)):
            raise GoTestJsonError(f"line {line_number} is a {action} result with no numeric Elapsed")
        results.append(GoTestResult(event["Package"], event.get("Test") or None, action, float(elapsed)))
    if event_count == 0:
        raise GoTestJsonError("no test events")
    return results


def collect_failure_output(
    go_test_json_path: Path,
    failed_packages: set[str],
) -> str:
    """Keep all failed-package output: late crashes can be attributed to passed tests."""
    output_chunks: list[str] = []
    for _, event in iterate_events(go_test_json_path):
        if event["Action"] == "build-output":
            output_chunks.append(event["Output"])
            continue
        if event["Action"] != "output" or event["Package"] not in failed_packages:
            continue
        output_chunks.append(event["Output"])
    return "".join(output_chunks)


def summarize_go_test_json(go_test_json_path: Path, suite_name: str) -> SuiteSummary:
    results = collect_results(go_test_json_path)
    failed_packages = {result.package for result in results if result.action == "fail"}
    return SuiteSummary(
        results=results,
        failure_output=collect_failure_output(go_test_json_path, failed_packages),
        markdown=render_markdown(suite_name, results),
        timing_document=build_timing_document(suite_name, results),
    )


def render_markdown(suite_name: str, results: list[GoTestResult]) -> str:
    package_results = [result for result in results if result.test is None]
    test_results = [result for result in results if result.test is not None]
    lines = [
        f"## Go {suite_name} suite timing",
        "",
        f"Packages: {describe_counts(package_results)}. Tests and subtests: {describe_counts(test_results)}.",
        "",
        f"### Slowest packages (top {SLOWEST_PACKAGE_ROW_COUNT})",
        "",
        *markdown_table(
            ["Rank", "Package", "Result", "Elapsed"],
            [
                [str(rank), result.package, result.action, format_elapsed_for_humans(result.elapsed_seconds)]
                for rank, result in enumerate(slowest_first(package_results)[:SLOWEST_PACKAGE_ROW_COUNT], start=1)
            ],
        ),
        "",
        f"### Slowest tests and subtests (top {SLOWEST_TEST_ROW_COUNT})",
        "",
        *markdown_table(
            ["Rank", "Test", "Package", "Result", "Elapsed"],
            [
                [
                    str(rank),
                    result.test or "",
                    result.package,
                    result.action,
                    format_elapsed_for_humans(result.elapsed_seconds),
                ]
                for rank, result in enumerate(slowest_first(test_results)[:SLOWEST_TEST_ROW_COUNT], start=1)
            ],
        ),
        "",
    ]
    failed_results = sorted(
        (result for result in results if result.action == "fail"),
        key=lambda result: (result.package, result.test or ""),
    )
    if failed_results:
        lines += [
            "### Failed tests and packages",
            "",
            *markdown_table(
                ["Package", "Test", "Elapsed"],
                [
                    [result.package, result.test or "(package)", format_elapsed_for_humans(result.elapsed_seconds)]
                    for result in failed_results[:FAILED_ROW_LIMIT]
                ],
            ),
        ]
        omitted_failed_count = len(failed_results) - FAILED_ROW_LIMIT
        if omitted_failed_count > 0:
            lines += ["", f"{omitted_failed_count} more failed results are in the timing artifact."]
        lines.append("")
    return "\n".join(lines) + "\n"


def describe_counts(results: list[GoTestResult]) -> str:
    counts = Counter(result.action for result in results)
    return f"{len(results)} ({counts['pass']} pass, {counts['fail']} fail, {counts['skip']} skip)"


def slowest_first(results: list[GoTestResult]) -> list[GoTestResult]:
    return sorted(results, key=lambda result: (-result.elapsed_seconds, result.package, result.test or ""))


def format_elapsed_for_humans(elapsed_seconds: float) -> str:
    if elapsed_seconds < 60:
        return f"{elapsed_seconds:.2f}s"
    minutes, seconds = divmod(elapsed_seconds, 60)
    return f"{int(minutes)}m {seconds:05.2f}s"


def markdown_table(headers: list[str], rows: list[list[str]]) -> list[str]:
    def render_row(cells: list[str]) -> str:
        return "| " + " | ".join(markdown_cell(cell) for cell in cells) + " |"

    return [render_row(headers), render_row(["---"] * len(headers)), *(render_row(row) for row in rows)]


def markdown_cell(text: str) -> str:
    # Test names come from test code: keep the cell on one line and keep a pipe from ending it.
    return " ".join(text.split()).replace("|", "\\|")


def build_timing_document(suite_name: str, results: list[GoTestResult]) -> dict[str, object]:
    """Names, results and seconds only: test Output never enters the timing document."""
    package_results = sorted(
        (result for result in results if result.test is None),
        key=lambda result: result.package,
    )
    test_results = sorted(
        (result for result in results if result.test is not None),
        key=lambda result: (result.package, result.test or ""),
    )
    return {
        "schema_version": TIMING_DOCUMENT_SCHEMA_VERSION,
        "suite": suite_name,
        "packages": [
            {"package": result.package, "result": result.action, "elapsed_seconds": result.elapsed_seconds}
            for result in package_results
        ],
        "tests": [
            {
                "package": result.package,
                "test": result.test,
                "result": result.action,
                "elapsed_seconds": result.elapsed_seconds,
            }
            for result in test_results
        ],
    }


def main(argument_values: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Summarize one go test -json stream from a CI Go suite.")
    parser.add_argument("--suite", required=True, help="Suite name for the headings, such as unit or race.")
    parser.add_argument("--go-test-json", required=True, type=Path, help="The go test -json stream to summarize.")
    parser.add_argument("--timing-json", required=True, type=Path, help="Where to write the timing document.")
    parser.add_argument(
        "--step-summary",
        type=Path,
        help="Append the markdown summary here, such as $GITHUB_STEP_SUMMARY.",
    )
    arguments = parser.parse_args(argument_values)
    try:
        summary = summarize_go_test_json(arguments.go_test_json, arguments.suite)
    except (GoTestJsonError, OSError) as error:
        print(f"ci_go_test_timing: {arguments.suite}: {error}", file=sys.stderr)
        return 1
    sys.stdout.write(summary.markdown)
    if summary.failure_output:
        sys.stdout.write("\nOutput of failed tests and packages (job log only; never uploaded):\n")
        sys.stdout.write(summary.failure_output)
    arguments.timing_json.parent.mkdir(parents=True, exist_ok=True)
    arguments.timing_json.write_text(json.dumps(summary.timing_document, indent=2) + "\n", encoding="utf-8")
    if arguments.step_summary is not None:
        with arguments.step_summary.open("a", encoding="utf-8") as step_summary_stream:
            step_summary_stream.write(summary.markdown)
    return 0


if __name__ == "__main__":
    sys.exit(main())
