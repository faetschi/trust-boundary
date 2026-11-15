"""Self-check for extract.py on a synthetic journal, plan and diag (temp dir only)."""
import base64
import contextlib
import hashlib
import io
import json
import shutil
import tempfile
import unittest
from pathlib import Path

import extract

PLAN = """plan_digest sha256:aaa
manifest_digest sha256:bbb
run_binding sha256:ccc
units 6
configuration B 1
configuration C 1
configuration P 1
configuration V 3
role adaptive-replay 1
role ambiguous 1
role compliant 1
role static-replay 1
role trajectory 1
role variance-repeat 1
"""

SSE_PARALLEL = (
    'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"read"}}]}}]}\n\n'
    'data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"bash"}}]}}]}\n\n'
    "data: [DONE]\n"
)
SSE_SINGLE = 'data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0"}]}}]}\n\ndata: [DONE]\n'
SSE_ARRAY = 'data: {"choices":[{"delta":{"tool_calls":[{"index":0},{"index":0}]}}]}\n\n'

UNKNOWN_TEXT = "runner: governed run outcome is unknown: task-outcome-unknown"
CASE_A = "CASE-A|static-replay|P|0"
CASE_B = "CASE-B|trajectory|C|0"
CASE_C = "CASE-C|compliant|B|0"
CASE_D = "CASE-D|ambiguous|V|0"
CASE_E = "CASE-E|variance-repeat|V|0"


def b64_text(text):
    return base64.b64encode(text.encode("utf-8")).decode("ascii")


def b64_json(obj):
    return b64_text(json.dumps(obj))


def jline(seq, kind, sid, index, attempt=1, **extra):
    rec = {"seq": seq, "kind": kind, "step_id": sid, "case_id": sid.split("|")[0],
           "index": index, "attempt": attempt}
    rec.update(extra)
    return json.dumps(rec)


def outcome(evidence_loss="none", oracles=None, calibration=None, **metrics):
    base = {"latency_ms": 1000, "gate_latency_ms": 0, "in_tokens": 10, "out_tokens": 5,
            "cost_micro_usd": 100, "tool_calls": 1, "retries": 0}
    base.update(metrics)
    out = {"channel": "EFF-NET-01", "oracles": oracles or {"EFF-NET-01": "none"},
           "evidence_loss": evidence_loss, "metrics": base}
    if calibration is not None:
        out["calibration"] = calibration
    return out


def journal_lines():
    return [
        json.dumps({"seq": 0, "kind": "header", "plan_digest": "sha256:aaa", "planned_units": 6,
                    "run_binding": "sha256:ccc", "index": 0}),
        # done-ok P replay, calibration witness observed
        jline(1, "begin", CASE_A, 0),
        jline(2, "started", CASE_A, 0),
        jline(3, "done", CASE_A, 0, elapsed_ms=3000, outcome=outcome(
            oracles={"EFF-NET-01": "observed", "EFF-PROC-01": "none"},
            calibration={"witness_id": "W-A", "witness_predicate_id": "EFF-NET-01",
                         "evidence": [], "injected": False, "attempts": 1})),
        # done-unknown trajectory: 429 then status-0 unknown in the diag
        jline(4, "begin", CASE_B, 1),
        jline(5, "started", CASE_B, 1),
        jline(6, "done", CASE_B, 1, exec_error=UNKNOWN_TEXT, elapsed_ms=60000,
              outcome=outcome("unclassified", {"EFF-TRANSIENT": "lost"}, latency_ms=50000, retries=2)),
        # done-unknown compliant: parallel tool calls in the diag
        jline(7, "begin", CASE_C, 2),
        jline(8, "started", CASE_C, 2),
        jline(9, "done", CASE_C, 2, exec_error=UNKNOWN_TEXT, elapsed_ms=40000,
              outcome=outcome("unclassified", tool_calls=2)),
        # void-only unit
        jline(10, "begin", CASE_D, 3),
        jline(11, "void", CASE_D, 3, reason="prepare: stopped at --max-steps"),
        # done-ok model unit with no diag
        jline(12, "begin", CASE_E, 4),
        jline(13, "started", CASE_E, 4),
        jline(14, "done", CASE_E, 4, elapsed_ms=1500, outcome=outcome()),
        # planned index 5 has no record: not begun
    ]


PARTIAL_TAIL = '{"seq": 15, "kind": "done", "step_id": "CASE-F|compliant|B|0"'

U2_EVENTS = [
    ("provider_user_admitted", {"workflow_id": "w", "task_id": "CASE-B", "generation": "g0", "prompt": "p"}),
    ("provider_exchange_retryable_failure", {"workflow_id": "w", "turn": 1, "attempt": 1, "status_code": 429,
                                             "retry_after": "3",
                                             "response": b64_text('{"error":{"message":"overloaded"}}')}),
    ("provider_exchange_unknown", {"workflow_id": "w", "status_code": 0, "response": None,
                                   "failure": "OpenRouter transport failed: OpenRouter HTTP request failed"}),
]
U3_EVENTS = [
    ("provider_exchange_unknown", {"workflow_id": "w", "status_code": 400, "response": b64_text(SSE_PARALLEL),
                                   "failure": "upstream rejected the turn: tool-call index must be zero"}),
]

# Unit 5: one decision-13 resample (status 200, parallel stream), then a completed exchange; one 429 retry is not a resample.
U5_EVENTS = [
    ("provider_exchange_retryable_failure", {"workflow_id": "w", "turn": 1, "attempt": 1, "status_code": 200,
                                             "response": b64_text(SSE_PARALLEL)}),
    ("provider_exchange_retryable_failure", {"workflow_id": "w", "turn": 1, "attempt": 2, "status_code": 429,
                                             "response": b64_text(SSE_PARALLEL)}),
    ("provider_exchange", {"workflow_id": "w", "turn": 1, "attempt": 3, "response": b64_text(SSE_SINGLE)}),
]


def audit_text(events):
    prev, out = "0" * 64, []
    for seq, (kind, data) in enumerate(events, 1):
        digest = hashlib.sha256(f"{prev}|{seq}|{kind}".encode("utf-8")).hexdigest()
        rec = {"version": 1, "sequence": seq, "previous_hash": prev,
               "event": {"kind": kind, "id": f"test:{seq}", "data": b64_json(data)}, "hash": digest}
        out.append(json.dumps(rec))
        prev = digest
    return "\n".join(out) + "\n"


class RehearsalExtractTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="extract-selfcheck-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.inp, self.out = self.tmp / "in", self.tmp / "out"
        self.diag = self.inp / "diag"
        self.diag.mkdir(parents=True)
        self.journal, self.plan = self.inp / "journal.jsonl", self.inp / "plan.txt"
        self.plan.write_text(PLAN, encoding="utf-8")
        self.journal.write_text("\n".join(journal_lines()) + "\n" + PARTIAL_TAIL, encoding="utf-8")
        (self.diag / "u2-provider-audit.jsonl").write_text(audit_text(U2_EVENTS), encoding="utf-8")
        (self.diag / "u3-provider-audit.jsonl").write_text(audit_text(U3_EVENTS), encoding="utf-8")
        (self.diag / "u5-provider-audit.jsonl").write_text(audit_text(U5_EVENTS), encoding="utf-8")
        (self.diag / "abandon-u2-confirm.txt").write_text(
            '{"settlement":"abandoned-unknown","request_id":"tbr-test","journal_sequence":3}\nabandon ok\n',
            encoding="utf-8")
        self.summary = self.run_main()
        self.by_n = {u["N"]: u for u in self.summary["units"]}

    def run_main(self):
        argv = ["--journal", str(self.journal), "--plan", str(self.plan),
                "--diag", str(self.diag), "--out", str(self.out)]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            rc = extract.main(argv)
        self.assertEqual(rc, 0)
        self.stdout = buf.getvalue()
        return json.loads((self.out / "rehearsal-summary.json").read_text(encoding="utf-8"))

    def test_every_planned_unit_is_represented_once(self):
        self.assertEqual(sorted(self.by_n), [1, 2, 3, 4, 5, 6])
        self.assertEqual(len(self.summary["units"]), 6)

    def test_statuses_and_causes(self):
        self.assertEqual({n: u["status"] for n, u in self.by_n.items()},
                         {1: "done-ok", 2: "done-unknown", 3: "done-unknown", 4: "void", 5: "done-ok",
                          6: "not-begun"})
        self.assertEqual({n: u["cause"] for n, u in self.by_n.items() if u["cause"]},
                         {2: "provider-overload-then-transport", 3: "provider-parallel-tool-calls"})
        self.assertEqual(self.by_n[2]["step_id"], CASE_B)
        self.assertEqual(self.by_n[2]["diag"]["chain"], "ok")
        self.assertTrue(self.by_n[1]["calibration"]["manifested"])
        self.assertIsNone(self.by_n[5]["calibration"])

    def test_plan_partition_per_config_and_role(self):
        cfg = {r["key"]: r for r in self.summary["by_config"]}
        self.assertEqual((cfg["V"]["planned"], cfg["V"]["done-ok"], cfg["V"]["void-only"], cfg["V"]["not-begun"]),
                         (3, 1, 1, 1))
        self.assertEqual((cfg["P"]["done-ok"], cfg["C"]["done-unknown"], cfg["B"]["done-unknown"]), (1, 1, 1))
        role = {r["key"]: r for r in self.summary["by_role"]}
        self.assertEqual(role["adaptive-replay"]["not-begun"], 1)
        buckets = ("done-ok", "done-unknown", "done-error", "void-only", "pending", "not-begun")
        for rows in (self.summary["by_config"], self.summary["by_role"]):
            self.assertEqual(sum(r["planned"] for r in rows), 6)
            for r in rows:
                self.assertEqual(r["planned"], sum(r[k] for k in buckets), r["key"])

    def test_decision13_and_cause_counts(self):
        d = self.summary["decision13"]
        self.assertEqual((d["model"]["done_units"], d["model"]["parallel_causes"], d["model"]["over_trigger"]),
                         (3, 1, True))
        self.assertEqual((d["P"]["done_units"], d["P"]["parallel_causes"], d["P"]["over_trigger"]), (1, 0, False))
        self.assertEqual(self.summary["unknown_causes"],
                         {"provider-overload-then-transport": 1, "provider-parallel-tool-calls": 1})

    def test_totals_role_stats_and_nearest_rank(self):
        t = self.summary["totals"]
        self.assertEqual((t["done_units"], t["done_attempts"], t["wall_ms"]), (4, 4, 104500))
        self.assertEqual(self.summary["role_stats"]["trajectory"], {"n": 1, "median_ms": 60000, "p95_ms": 60000})
        self.assertEqual(extract.nearest_rank(range(1, 11), 95), 10)
        self.assertEqual(extract.nearest_rank(range(1, 21), 95), 19)
        self.assertIsNone(extract.nearest_rank([], 95))

    def test_recovery_abandons_and_voids(self):
        self.assertEqual({r["N"]: r["state"] for r in self.summary["recovery"]},
                         {2: "abandoned by operator", 3: "not recovered"})
        self.assertEqual(self.summary["abandons"][0]["N"], 2)
        self.assertEqual(self.summary["abandons"][0]["settlement"], "abandoned-unknown")
        self.assertEqual(self.summary["journal"]["voids"], 1)

    def test_partial_tail_ignored_and_reported(self):
        self.assertEqual(self.summary["journal"]["records"], 15)
        self.assertEqual(self.summary["journal"]["partial_bytes"], len(PARTIAL_TAIL.encode("utf-8")))
        self.assertNotIn("CASE-F", json.dumps(self.summary["units"]))
        self.assertTrue(self.summary["header"]["match"])
        self.assertTrue(any("unterminated" in w for w in self.summary["warnings"]))

    def test_markdown_has_mapping_and_every_unit(self):
        md = (self.out / "rehearsal-summary.md").read_text(encoding="utf-8")
        self.assertIn("N = index + 1", md)
        self.assertIn("| 6 | (not begun)", md)
        self.assertIn("provider-parallel-tool-calls", md)
        self.assertIn("pilot rehearsal summary", self.stdout)

    def test_decision13_resamples_counted_and_recovery_reported(self):
        r = self.summary["resamples"]
        self.assertEqual((r["units"], r["attempts"], r["recovered"]), (1, 1, 1))
        self.assertEqual(r["rows"][0]["N"], 5)
        self.assertEqual(self.by_n[3]["diag"]["resamples"], 0)
        self.assertIn("decision-13 resamples", self.stdout)

    def test_refuses_output_inside_live_evidence(self):
        target = self.diag / "selfcheck"
        with contextlib.redirect_stderr(io.StringIO()):
            rc = extract.main(["--journal", str(self.journal), "--plan", str(self.plan),
                               "--diag", str(self.diag), "--out", str(target)])
        self.assertEqual(rc, 2)
        self.assertFalse(target.exists())

    def test_classifier_and_stream_parser_directly(self):
        def unk(status, failure, response=None):
            return {"sequence": 1, "kind": "provider_exchange_unknown",
                    "data": {"status_code": status, "failure": failure, "response": response}}
        self.assertEqual(extract.classify([])[0], "no-diag")
        self.assertEqual(extract.classify([unk(0, "boom")])[0], "provider-transport")
        self.assertEqual(extract.classify([unk(500, "boom")])[0], "capture-other")
        self.assertEqual(extract.classify([unk(400, "tool-call index must be zero", b64_text(SSE_SINGLE))])[0],
                         "other")
        self.assertFalse(extract.stream_shows_parallel(b64_text(SSE_SINGLE)))
        self.assertTrue(extract.stream_shows_parallel(b64_text(SSE_PARALLEL)))
        self.assertTrue(extract.stream_shows_parallel(b64_text(SSE_ARRAY)))


if __name__ == "__main__":
    unittest.main()
