"""Unprivileged rejection tests for the cgroup teardown witness parser/guards."""

import hashlib
import os
import signal
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from cgroup_teardown_witness import (
    Refusal,
    SCHEMA,
    WORKER_SOURCE,
    _assert_no_nonroot_write,
    _capture_created_scope_identity,
    _close_and_reap_worker,
    _open_bound_scope,
    _rmdir_bound_scope_entry,
    _validate_boot_id,
    _validate_scope_identity,
    _validate_guest_init_namespace_baseline,
    _validate_namespace_observations,
    read_live_boot_id,
    _write_bound_scope_kill,
    parse_events,
    parse_proc_stat,
    parse_unified_cgroup,
    validate_post_observations,
    validate_profile,
    verify_guest_init_identity,
    verify_guest_init_namespaces,
    validate_witness_timing,
)


class CgroupTeardownWitnessTests(unittest.TestCase):
    @staticmethod
    def _live_process_group_members(process_group: int) -> list[int]:
        members: list[int] = []
        for entry in os.scandir("/proc"):
            if not entry.name.isdecimal():
                continue
            try:
                text = Path(entry.path, "stat").read_text(encoding="ascii")
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                continue
            close = text.rfind(")")
            if close < 0:
                continue
            fields = text[close + 1 :].split()
            # After comm: state, ppid, pgrp, session, ...
            if len(fields) < 4 or fields[0] in {"Z", "X"}:
                continue
            try:
                pgrp, session = int(fields[2]), int(fields[3])
                pid = int(entry.name)
            except ValueError:
                continue
            if pgrp == process_group and session == process_group:
                members.append(pid)
        return sorted(members)

    def _wait_for_process_group_count(self, process_group: int, count: int) -> list[int]:
        deadline = time.monotonic() + 3.0
        members = self._live_process_group_members(process_group)
        while len(members) != count and time.monotonic() < deadline:
            time.sleep(0.01)
            members = self._live_process_group_members(process_group)
        return members

    def _stop_test_worker_group(self, worker: subprocess.Popen[bytes]) -> None:
        if worker.stdin is not None and not worker.stdin.closed:
            worker.stdin.close()
        try:
            # This process group was created solely for the test fixture.
            os.killpg(worker.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        if worker.poll() is None:
            worker.wait(timeout=3.0)
        deadline = time.monotonic() + 3.0
        while self._live_process_group_members(worker.pid) and time.monotonic() < deadline:
            time.sleep(0.01)
        self.assertEqual(self._live_process_group_members(worker.pid), [])

    @unittest.skipUnless(
        sys.platform == "linux" and hasattr(os, "fork") and Path("/proc").is_dir(),
        "the actual fixture worker uses Linux fork and /proc",
    )
    def test_worker_gate_eof_wrong_and_extra_bytes_exit_without_forks(self):
        for label, gate in (("missing/EOF", None), ("wrong", b"X"), ("extra", b"GX")):
            with self.subTest(gate=label), tempfile.TemporaryDirectory() as temp:
                worker = subprocess.Popen(
                    [sys.executable, "-I", "-c", WORKER_SOURCE],
                    cwd=temp,
                    stdin=subprocess.PIPE,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    close_fds=True,
                    start_new_session=True,
                    env={"LANG": "C", "PYTHONDONTWRITEBYTECODE": "1"},
                )
                try:
                    if gate is not None:
                        self.assertIsNotNone(worker.stdin)
                        worker.stdin.write(gate)
                    _close_and_reap_worker(worker, gate_write_attempted=False)
                    self.assertEqual(worker.returncode, 2)
                    self.assertEqual(self._live_process_group_members(worker.pid), [])
                finally:
                    self._stop_test_worker_group(worker)

    @unittest.skipUnless(
        sys.platform == "linux" and hasattr(os, "fork") and Path("/proc").is_dir(),
        "the actual fixture worker uses Linux fork and /proc",
    )
    def test_worker_exact_gate_starts_only_its_test_process_group(self):
        with tempfile.TemporaryDirectory() as temp:
            worker = subprocess.Popen(
                [sys.executable, "-I", "-c", WORKER_SOURCE],
                cwd=temp,
                stdin=subprocess.PIPE,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                close_fds=True,
                start_new_session=True,
                env={"LANG": "C", "PYTHONDONTWRITEBYTECODE": "1"},
            )
            try:
                self.assertIsNotNone(worker.stdin)
                worker.stdin.write(b"G")
                _close_and_reap_worker(worker, gate_write_attempted=True)
                members = self._wait_for_process_group_count(worker.pid, 3)
                self.assertEqual(len(members), 3)
                self.assertIn(worker.pid, members)
            finally:
                self._stop_test_worker_group(worker)

    def test_rejects_missing_or_ambiguous_proc_cgroup_membership(self):
        with self.assertRaises(Refusal):
            parse_unified_cgroup("2:cpu:/user.slice\n")
        with self.assertRaises(Refusal):
            parse_unified_cgroup("0::/one\n0::/two\n")

    def test_rejects_malformed_cgroup_events(self):
        with self.assertRaises(Refusal):
            parse_events("frozen 0\n")
        with self.assertRaises(Refusal):
            parse_events("populated 0\npopulated 1\n")

    def test_proc_stat_parses_comm_with_closing_paren(self):
        fields = ["S", "77"] + ["0"] * 18
        fields[19] = "123456"
        row = parse_proc_stat(f"314 (worker ) name) {' '.join(fields)}")
        self.assertEqual(row["pid"], 314)
        self.assertEqual(row["ppid"], 77)
        self.assertEqual(row["starttime_ticks"], 123456)

    def test_rejects_abi_root_and_runtime_profile_identity_mismatch(self):
        with tempfile.TemporaryDirectory() as temp:
            profile_file = Path(temp) / "runtime-profile.json"
            profile_file.write_text('{"profile":"candidate"}', encoding="utf-8")
            digest = hashlib.sha256(profile_file.read_bytes()).hexdigest()
            profile = {
                "schema": SCHEMA,
                "profile_id": "candidate-guest",
                "kernel_release": "pinned-release",
                "landlock_abi": 8,
                "runtime_profile_id": "candidate-runtime",
                "runtime_profile_path": str(profile_file),
                "runtime_profile_sha256": digest,
                "teardown_deadline_ms": 2500,
                "guest_boot_id": "01234567-89ab-4def-8123-456789abcdef",
                "guest_init_namespaces": {
                    "pid": {"device": 4, "inode": 1001},
                    "user": {"device": 4, "inode": 1002},
                    "mnt": {"device": 4, "inode": 1003},
                    "cgroup": {"device": 4, "inode": 1004},
                },
                "delegated_root": {
                    "path": "/sys/fs/cgroup/delegated",
                    "device": 10,
                    "inode": 20,
                    "mount_id": 30,
                    "owner_uid": 0,
                },
            }
            wrong_abi = dict(profile, landlock_abi=9)
            with self.assertRaises(Refusal):
                validate_profile(wrong_abi, "/sys/fs/cgroup/delegated")
            with self.assertRaises(Refusal):
                validate_profile(profile, "/sys/fs/cgroup/other")
            wrong_digest = dict(profile, runtime_profile_sha256="0" * 64)
            with self.assertRaises(Refusal):
                validate_profile(wrong_digest, "/sys/fs/cgroup/delegated")
            stale_mount = dict(profile, delegated_root={**profile["delegated_root"], "mount_id": 0})
            with self.assertRaises(Refusal):
                validate_profile(stale_mount, "/sys/fs/cgroup/delegated")
            wrong_owner = dict(profile, delegated_root={**profile["delegated_root"], "owner_uid": 1000})
            with self.assertRaisesRegex(Refusal, "UID 0"):
                validate_profile(wrong_owner, "/sys/fs/cgroup/delegated")
            wrong_boot_id = dict(profile, guest_boot_id="not-a-uuid")
            with self.assertRaisesRegex(Refusal, "guest_boot_id"):
                validate_profile(wrong_boot_id, "/sys/fs/cgroup/delegated")
            missing_boot_id = dict(profile)
            del missing_boot_id["guest_boot_id"]
            with self.assertRaisesRegex(Refusal, "keys must be exactly"):
                validate_profile(missing_boot_id, "/sys/fs/cgroup/delegated")
            caller_claim = dict(profile, nonroot_writer_excluded=True)
            with self.assertRaisesRegex(Refusal, "keys must be exactly"):
                validate_profile(caller_claim, "/sys/fs/cgroup/delegated")
            missing_namespace_pin = dict(profile)
            del missing_namespace_pin["guest_init_namespaces"]
            with self.assertRaisesRegex(Refusal, "keys must be exactly"):
                validate_profile(missing_namespace_pin, "/sys/fs/cgroup/delegated")

    def test_boot_id_parser_requires_canonical_uuid_and_exact_proc_contents(self):
        valid = "01234567-89ab-4def-8123-456789abcdef"
        self.assertEqual(_validate_boot_id(valid), valid)
        for invalid in (None, "", "NOT-A-UUID", valid.upper(), "{" + valid + "}", valid.replace("-", "")):
            with self.subTest(invalid=invalid), self.assertRaises(Refusal):
                _validate_boot_id(invalid)

        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "boot_id"
            path.write_bytes((valid + "\n").encode("ascii"))
            self.assertEqual(read_live_boot_id(path), valid)
            for invalid in (
                b"",
                (valid + "\n\n").encode("ascii"),
                (valid + "\r\n").encode("ascii"),
                (valid.upper() + "\n").encode("ascii"),
                b"\xff\n",
            ):
                with self.subTest(contents=invalid):
                    path.write_bytes(invalid)
                    with self.assertRaises(Refusal):
                        read_live_boot_id(path)
            with self.assertRaises(Refusal):
                read_live_boot_id(Path(temp) / "missing")

    def test_stale_boot_id_refuses_before_namespace_checks(self):
        expected = "01234567-89ab-4def-8123-456789abcdef"
        live = "11234567-89ab-4def-8123-456789abcdef"
        namespaces = {
            name: {"device": 4, "inode": index + 1001}
            for index, name in enumerate(("pid", "user", "mnt", "cgroup"))
        }
        with patch("cgroup_teardown_witness.read_live_boot_id", return_value=live), patch(
            "cgroup_teardown_witness.verify_guest_init_namespaces"
        ) as check_namespaces:
            with self.assertRaisesRegex(Refusal, "boot ID differs"):
                verify_guest_init_identity(expected, namespaces)
            check_namespaces.assert_not_called()

        with patch("cgroup_teardown_witness.read_live_boot_id", return_value=expected), patch(
            "cgroup_teardown_witness.verify_guest_init_namespaces", return_value={"pid": "pid:[1001]"}
        ) as check_namespaces:
            observed, namespace_links = verify_guest_init_identity(expected, namespaces)
            self.assertEqual(observed, expected)
            self.assertEqual(namespace_links, {"pid": "pid:[1001]"})
            check_namespaces.assert_called_once_with(namespaces)

    def test_namespace_guard_rejects_nested_pid1_when_baseline_is_guest_init(self):
        baseline = {
            "pid": {"device": 4, "inode": 1001},
            "user": {"device": 4, "inode": 1002},
            "mnt": {"device": 4, "inode": 1003},
            "cgroup": {"device": 4, "inode": 1004},
        }
        expected = _validate_guest_init_namespace_baseline(baseline)
        matching = {name: dict(identity) for name, identity in expected.items()}
        _validate_namespace_observations(expected, matching, matching)

        nested = {name: dict(identity) for name, identity in expected.items()}
        nested["pid"] = {"device": 4, "inode": 2001}
        # A nested PID 1 and the observer can agree with each other while both
        # differ from the independently pinned guest-init namespace identity.
        with self.assertRaisesRegex(Refusal, "observer pid namespace differs"):
            _validate_namespace_observations(expected, nested, nested)

        mismounted_proc = {name: dict(identity) for name, identity in expected.items()}
        mismounted_proc["pid"] = {"device": 4, "inode": 2002}
        with self.assertRaisesRegex(Refusal, "/proc/1 pid namespace differs"):
            _validate_namespace_observations(expected, matching, mismounted_proc)

        with self.assertRaisesRegex(Refusal, "guest_init_namespaces"):
            _validate_guest_init_namespace_baseline({"pid": baseline["pid"]})

    def test_live_namespace_reader_rejects_nested_pid_namespace(self):
        baseline = {
            "pid": {"device": 4, "inode": 1001},
            "user": {"device": 4, "inode": 1002},
            "mnt": {"device": 4, "inode": 1003},
            "cgroup": {"device": 4, "inode": 1004},
        }

        def namespace_inode(path):
            namespace = path.rsplit("/", 1)[1]
            if namespace == "pid" and path.startswith("/proc/"):
                return 2001
            return baseline[namespace]["inode"]

        def fake_readlink(path):
            namespace = path.rsplit("/", 1)[1]
            return f"{namespace}:[{namespace_inode(path)}]"

        def fake_stat(path):
            namespace = path.rsplit("/", 1)[1]
            return SimpleNamespace(st_dev=4, st_ino=namespace_inode(path))

        with patch("cgroup_teardown_witness.os.readlink", side_effect=fake_readlink), patch(
            "cgroup_teardown_witness.os.stat", side_effect=fake_stat
        ):
            # The observer and /proc/1 agree with each other, but both are in
            # a nested PID namespace rather than the pinned guest-init one.
            with self.assertRaisesRegex(Refusal, "observer pid namespace differs"):
                verify_guest_init_namespaces(baseline)

    def test_rejects_escaped_or_reused_scope_identity(self):
        name = ".tbound-proc-witness-" + "a" * 24
        with self.assertRaises(Refusal):
            _validate_scope_identity("../" + name, 1, 8, 1, 9, 1, 9)
        with self.assertRaises(Refusal):
            _validate_scope_identity(name, 1, 8, 2, 9, 2, 9)
        with self.assertRaises(Refusal):
            _validate_scope_identity(name, 1, 8, 1, 9, 1, 10)
        with self.assertRaises(Refusal):
            _validate_scope_identity(name, 1, 9, 1, 9, 1, 9)

    def test_creation_identity_rejects_open_after_name_replacement(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = ".tbound-proc-witness-" + "b" * 24
            original = root / name
            original.mkdir()
            root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
            try:
                captured = _capture_created_scope_identity(root_fd, name)
                backup = root / "original-entry"
                original.rename(backup)
                replacement = root / name
                replacement.mkdir()
                replacement_identity = (replacement.stat().st_dev, replacement.stat().st_ino)
                self.assertNotEqual(captured, replacement_identity)
                with self.assertRaisesRegex(Refusal, "captured immediately after mkdir"):
                    _open_bound_scope(root_fd, name, captured)
                self.assertTrue(replacement.is_dir())
                self.assertTrue(backup.is_dir())
            finally:
                os.close(root_fd)

    def test_bound_removal_refuses_substitution_and_preserves_both_directories(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = ".tbound-proc-witness-" + "c" * 24
            original = root / name
            original.mkdir()
            root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
            scope_fd = os.open(original, os.O_RDONLY | os.O_DIRECTORY)
            try:
                captured = _capture_created_scope_identity(root_fd, name)
                backup = root / "captured-entry"
                original.rename(backup)
                replacement = root / name
                replacement.mkdir()
                with self.assertRaises(Refusal):
                    _rmdir_bound_scope_entry(root_fd, scope_fd, name, captured)
                self.assertTrue(replacement.is_dir())
                self.assertTrue(backup.is_dir())
            finally:
                os.close(scope_fd)
                os.close(root_fd)

    def test_nested_child_insertion_is_detected_and_left_untouched(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = ".tbound-proc-witness-" + "d" * 24
            scope = root / name
            scope.mkdir()
            kill_control = scope / "cgroup.kill"
            kill_control.write_text("", encoding="ascii")
            nested = scope / "concurrent-child"
            nested.mkdir()
            root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
            scope_fd = os.open(scope, os.O_RDONLY | os.O_DIRECTORY)
            try:
                identity = _capture_created_scope_identity(root_fd, name)
                with self.assertRaisesRegex(Refusal, "nested cgroup descendants"):
                    _write_bound_scope_kill(root_fd, scope_fd, name, identity)
                self.assertTrue(nested.is_dir())
                self.assertEqual(kill_control.read_text(encoding="ascii"), "")
            finally:
                os.close(scope_fd)
                os.close(root_fd)

    def test_scope_name_substitution_blocks_fixture_kill_write(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = ".tbound-proc-witness-" + "e" * 24
            original = root / name
            original.mkdir()
            original_kill = original / "cgroup.kill"
            original_kill.write_text("", encoding="ascii")
            root_fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
            scope_fd = os.open(original, os.O_RDONLY | os.O_DIRECTORY)
            try:
                identity = _capture_created_scope_identity(root_fd, name)
                backup = root / "captured-entry"
                original.rename(backup)
                replacement = root / name
                replacement.mkdir()
                replacement_kill = replacement / "cgroup.kill"
                replacement_kill.write_text("", encoding="ascii")
                with self.assertRaisesRegex(Refusal, "anchored child entry"):
                    _write_bound_scope_kill(root_fd, scope_fd, name, identity)
                self.assertEqual(replacement_kill.read_text(encoding="ascii"), "")
                self.assertEqual((backup / "cgroup.kill").read_text(encoding="ascii"), "")
            finally:
                os.close(scope_fd)
                os.close(root_fd)

    def test_group_or_other_write_invalidates_nonroot_exclusion(self):
        with tempfile.TemporaryDirectory() as temp:
            fixture = Path(temp) / "control"
            fixture.write_text("", encoding="ascii")
            os.chmod(fixture, stat.S_IRUSR | stat.S_IWUSR | stat.S_IWGRP)
            with self.assertRaisesRegex(Refusal, "group/other"):
                _assert_no_nonroot_write(fixture.stat(), "fixture control")

    def test_rejects_stale_missing_and_over_deadline_timestamps(self):
        with self.assertRaises(Refusal):
            validate_witness_timing(20, 20, 30, 100)
        with self.assertRaises(Refusal):
            validate_witness_timing(20, 30, 25, 100)
        with self.assertRaises(Refusal):
            validate_witness_timing(20, 30, 30_000_031, 30)
        with self.assertRaises(Refusal):
            validate_witness_timing(20, None, 30, 100)

    def test_rejects_missing_unknown_and_identity_mismatched_witnesses(self):
        scope = "/delegated/.tbound-proc-witness-test"
        pre = [{"pid": 41, "starttime_ticks": 812, "cgroup_path": scope, "pidfd_opened": True}]
        with self.assertRaises(Refusal):
            validate_post_observations(pre, [], scope)
        unknown = [{"pid": 41, "starttime_ticks": 812, "status": "unknown", "pidfd_readable": False}]
        with self.assertRaises(Refusal):
            validate_post_observations(pre, unknown, scope)
        reused = [{"pid": 41, "starttime_ticks": 999, "status": "exited", "pidfd_readable": True}]
        with self.assertRaises(Refusal):
            validate_post_observations(pre, reused, scope)

    def test_rejects_escaped_and_surviving_descendant_witnesses(self):
        scope = "/delegated/.tbound-proc-witness-test"
        pre = [{"pid": 41, "starttime_ticks": 812, "cgroup_path": scope, "pidfd_opened": True}]
        escaped = [{
            "pid": 41,
            "starttime_ticks": 812,
            "status": "escaped",
            "pidfd_readable": False,
            "cgroup_path": "/delegated",
        }]
        with self.assertRaisesRegex(Refusal, "escaped"):
            validate_post_observations(pre, escaped, scope)
        surviving = [{
            "pid": 41,
            "starttime_ticks": 812,
            "status": "surviving",
            "pidfd_readable": False,
            "cgroup_path": scope,
        }]
        with self.assertRaisesRegex(Refusal, "survived"):
            validate_post_observations(pre, surviving, scope)


if __name__ == "__main__":
    unittest.main()
