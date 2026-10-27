#!/bin/bash
# Unprivileged regressions: extract fixture functions, never run the dispatcher preamble.
set -euo pipefail
[[ $EUID -ne 0 ]] || { echo 'Run these temporary-directory tests without root.' >&2; exit 1; }
python3 -I - "$(dirname -- "$(realpath -- "$0")")/tbound-op" "$@" <<'PY'
import base64
import grp
import json
import os
from pathlib import Path
import pwd
import stat
import subprocess
import sys
import tempfile
import unittest

operator_path = Path(sys.argv.pop(1))
source = operator_path.read_text(encoding="utf-8")
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


class CatalogTests(unittest.TestCase):
    def diagnostics(self, events, entry):
        with tempfile.TemporaryDirectory(prefix="tbound-op-catalog-test-") as tmp:
            base = Path(tmp)
            catalog = base / "catalog.jsonl"
            catalog.write_text("".join(json.dumps(ev) + "\n" for ev in events))
            (base / "audit").mkdir()
            (base / "audit" / "serve-pi-requests.jsonl").touch()
            (base / "cg" / "cells").mkdir(parents=True)
            (base / "cg" / "cells" / "cgroup.procs").touch()
            (base / "work").mkdir()
            helpers = source[source.index("req_records() {"):source.index("GE_OUT=$GA/out")]
            collect = source[source.index("v_collect() {"):source.index("v_diag() {")]
            script = """set -euo pipefail
die() { echo "$*" >&2; exit 1; }
need_argc() { :; }
mkwork() { :; }
put_out() { :; }
flock() { return 1; }
# Only the fixed /proc read is stubbed; catalog, request, and cgroup reads use temp files.
cat() { case ${1:-} in /proc/*) return 0;; *) command cat "$@";; esac; }
CATALOG=$1/catalog.jsonl
AUDIT=$1/audit
CG=$1/cg
WORK=$1/work
SVC_PRIV=(false)
PODMAN=(false)
ARGS=(l4)
""" + helpers + collect + "\n" + entry + "\n"
            return subprocess.run(["bash", "-c", script, "test-local", tmp], capture_output=True, text=True)

    @staticmethod
    def event(kind="pi_workflow_owner_scope_drained", **data):
        return {"event": {"kind": kind, "data": base64.b64encode(json.dumps(data).encode()).decode()}}

    def test_unexposed_cleanup_counts_decoded_boolean_in_both_diagnostics(self):
        events = [self.event(unexposed=True), self.event(unexposed=True),
                  self.event(unexposed=False), self.event(), self.event(unexposed="true"),
                  self.event("other_event", unexposed=True)]
        # A decoy outside the encoded payload must not contribute to the count.
        events[-1]["unexposed"] = True
        for entry, field in (("l4_facts test-request", "catalog_unexposed_true=2"),
                             ("v_collect", "unexposed_true=2")):
            with self.subTest(entry=entry):
                got = self.diagnostics(events, entry)
                self.assertEqual(got.returncode, 0, got.stderr)
                self.assertIn(field, got.stdout)

    def test_no_unexposed_cleanup_counts_zero_in_both_diagnostics(self):
        for events in ([], [self.event(unexposed=False), self.event("other_event", unexposed=True)]):
            for entry, field in (("l4_facts test-request", "catalog_unexposed_true=0"),
                                 ("v_collect", "unexposed_true=0")):
                with self.subTest(events=events, entry=entry):
                    got = self.diagnostics(events, entry)
                    self.assertEqual(got.returncode, 0, got.stderr)
                    self.assertIn(field, got.stdout)

    def test_invalid_cleanup_payload_is_unavailable_in_both_diagnostics(self):
        event = self.event(unexposed=True)
        event["event"]["data"] = "not-base64"
        for entry, field in (("l4_facts test-request", "catalog_unexposed_true=unavailable"),
                             ("v_collect", "unexposed_true=unavailable")):
            with self.subTest(entry=entry):
                got = self.diagnostics([event], entry)
                self.assertEqual(got.returncode, 0, got.stderr)
                self.assertIn(field, got.stdout)


class ManifestInstallTests(unittest.TestCase):
    def test_real_pilot_node_checks_with_dispatcher_arguments(self):
        got = subprocess.run(
            ["go", "test", "./cmd/tbound-profile", "-run", "^TestOperatorCorpusDigestArgumentsRunPilotNodeChecks$", "-count=1"],
            cwd=operator_path.parents[3] / "supervisor", capture_output=True, text=True)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)


    def run_install(self, corrupt=None, extra_args=None, active=False, rollback=False, missing_backup=False, invalid_backup=False, fail_install=None):
        tmp = tempfile.TemporaryDirectory(prefix="tbound-op-manifest-")
        self.addCleanup(tmp.cleanup)
        base = Path(tmp.name)
        for name in ("stage", "work", "etc/corpus", "prev/pilot/corpus", "ga/state"):
            (base / name).mkdir(parents=True)
        import hashlib
        files = ("manifest.json", "receipt.json", "audit.json", "stats.json")
        pins = []
        for name in files:
            raw = (name + "\n").encode()
            pins.append(hashlib.sha256(raw).hexdigest())
            (base / "stage" / name).write_bytes(raw)
            (base / "etc/corpus" / name).write_text("previous " + name)
        if corrupt:
            (base / "stage" / corrupt).write_text("changed")
        tbp = base / "tbp"
        tbp.write_text('''#!/bin/bash
printf '%s\n' "$*" >>"$BASE/calls"
[[ ${REFUSE_ROLLBACK:-} != 1 ]] || { echo "bad backup" >&2; exit 1; }
for file in receipt audit stats; do
 found=0
 for ((i=1; i<=$#; i++)); do
  if [[ ${!i} == --panel-$file ]]; then ((i+=1)); [[ -f ${!i} ]] || exit 1; found=1; fi
 done
 [[ $found == 1 ]] || { echo "missing --panel-$file"; exit 1; }
done
printf 'sha256:%064d\n' 1
''')
        tbp.chmod(0o700)
        staging = source[source.index("stage_hashed() {"):source.index("# -------------------------------------------------------------------------------------------------------- state")]
        helpers = source[source.index("backup_once() {"):source.index("svc_inactive() {")]
        verb = source[source.index("v_manifest_install() {"):source.index("v_svc() {")]
        script = '''set -euo pipefail
BASE=$1; export BASE
shift
WORK=$BASE/work; ETC=$BASE/etc; PREV=$BASE/prev; GA=$BASE/ga; TBP=$BASE/tbp
ARGS=("$@")
die() { echo "$*" >&2; exit 1; }
usage() { echo "$*" >&2; exit 2; }
say() { echo "$*"; }
need_argc() { ((${#ARGS[@]} >= $1 && ${#ARGS[@]} <= $2)) || usage 'wrong number of arguments'; }
need_sha() { [[ $1 =~ ^(sha256:)?[0-9a-f]{64}$ ]] || usage 'bad SHA'; }
norm_sha() { printf 'sha256:%s' "${1#sha256:}"; }
sha() { local h; h=$(sha256sum -- "$1"); printf 'sha256:%s' "${h%% *}"; }
check_tools() { :; }; mkwork() { :; }; prep_ga() { :; }
need_inactive() { ''' + ('die active' if active else ':') + '''; }
stage_copy() { cp -- "$BASE/stage/$2" "$3"; }
install() {
 printf '%s\n' "$*" >>"$BASE/installs"
 if [[ -n ${FAIL_INSTALL:-} && $* == *"$FAIL_INSTALL"* ]]; then return 1; fi
 local a args=()
 while (($#)); do case $1 in -o|-g) shift 2;; *) args+=("$1"); shift;; esac; done
 command install "${args[@]}"
}
''' + staging + helpers + verb + "\nv_manifest_install\n"
        if rollback:
            rollback_verb = source[source.index("v_rollback() {"):source.index("v_manifest_install() {")]
            script += "\nneed_name() { [[ $1 == pilot ]]; }\nst_reset() { :; }\nre_dig='^sha256:[0-9a-f]{64}$'\n" + rollback_verb
            if invalid_backup:
                script += "\nexport REFUSE_ROLLBACK=1\n"
            if fail_install:
                script += "\nexport FAIL_INSTALL=" + fail_install + "\n"
            if missing_backup:
                script += '\nrm -- "$PREV/pilot/corpus/audit.json"\n'
            script += "\nARGS=(pilot corpus)\nv_rollback\n"
        args = extra_args if extra_args is not None else [pins[0], "1".zfill(64), *pins[1:]]
        got = subprocess.run(["bash", "-c", script, "manifest-test", str(base), *args], capture_output=True, text=True)
        return got, base, files

    def test_installs_and_checks_all_three_pinned_panel_files(self):
        got, base, files = self.run_install()
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        calls = (base / "calls").read_text().splitlines()
        self.assertEqual(len(calls), 2)
        for name in files:
            self.assertEqual((base / "etc/corpus" / name).read_bytes(), (base / "stage" / name).read_bytes())
            self.assertEqual((base / "prev/pilot/corpus" / name).read_text(), "previous " + name)
            self.assertEqual(stat.S_IMODE((base / "etc/corpus" / name).stat().st_mode), 0o644)
        self.assertIn("-o root -g root", (base / "installs").read_text())

    def test_corpus_rollback_restores_all_four_backups(self):
        got, base, files = self.run_install(rollback=True)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        for file in files:
            self.assertEqual((base / "etc/corpus" / file).read_text(), "previous " + file)
        self.assertEqual(len((base / "calls").read_text().splitlines()), 3)

    def test_invalid_corpus_backup_preserves_files_and_digest_state(self):
        got, base, files = self.run_install(rollback=True, invalid_backup=True)
        self.assertNotEqual(got.returncode, 0)
        self.assertIn("does not verify", got.stderr)
        for file in files:
            self.assertEqual((base / "etc/corpus" / file).read_bytes(), (base / "stage" / file).read_bytes())
        self.assertTrue((base / "ga/state/corpus-digest").is_file())

    def test_failed_restore_leaves_no_stale_digest_token(self):
        got, base, files = self.run_install(rollback=True, fail_install="audit.json")
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertFalse((base / "ga/state/corpus-digest").exists(), "a half-restored corpus must not keep the digest of the installed manifest")

    def test_incomplete_corpus_backup_refuses_rollback_without_changes(self):
        got, base, files = self.run_install(rollback=True, missing_backup=True)
        self.assertNotEqual(got.returncode, 0)
        self.assertIn("no backup", got.stderr)
        for file in files:
            self.assertEqual((base / "etc/corpus" / file).read_bytes(), (base / "stage" / file).read_bytes())

    def test_changed_panel_files_refused_before_install(self):
        for name in ("receipt.json", "audit.json", "stats.json"):
            with self.subTest(name=name):
                got, base, files = self.run_install(corrupt=name)
                self.assertNotEqual(got.returncode, 0)
                self.assertIn("given SHA", got.stderr)
                for file in files:
                    self.assertEqual((base / "etc/corpus" / file).read_text(), "previous " + file)

    def test_missing_or_malformed_panel_pins_refused(self):
        for args in (["1" * 64, "2" * 64], ["1" * 64, "2" * 64, "bad", "3" * 64, "4" * 64]):
            got, base, files = self.run_install(extra_args=args)
            self.assertEqual(got.returncode, 2, got.stdout + got.stderr)
            self.assertFalse((base / "calls").exists())

    def test_active_service_refused_without_install(self):
        got, base, files = self.run_install(active=True)
        self.assertNotEqual(got.returncode, 0)
        for file in files:
            self.assertEqual((base / "etc/corpus" / file).read_text(), "previous " + file)

class ConfigInstallTests(unittest.TestCase):
    """cfg-install for the corpus config holds it against the policy the panel receipt recorded."""

    digest = "sha256:" + "1" * 64

    def run_cfg(self, config_paths=(), policy="absent", receipt=True):
        import hashlib
        tmp = tempfile.TemporaryDirectory(prefix="tbound-op-cfg-")
        self.addCleanup(tmp.cleanup)
        base = Path(tmp.name)
        for name in ("stage", "work", "etc/corpus", "prev", "ga/state"):
            (base / name).mkdir(parents=True)
        cfg = json.dumps({"schema_version": "tbound-pi-service-config/v4", "corpus_manifest_digest": self.digest,
                          "workflow_bash_output_paths": list(config_paths)}, indent=2).encode()
        (base / "stage" / "serve-pi-service.json").write_bytes(cfg)
        (base / "ga/state/corpus-digest").write_text(self.digest + "\n")
        if receipt:
            doc = {"schema": "tbound-panel-receipt/v3"}
            if policy != "absent":
                doc["public_policy"] = policy
            (base / "etc/corpus/receipt.json").write_text(json.dumps(doc))
        staging = source[source.index("stage_hashed() {"):source.index("# -------------------------------------------------------------------------------------------------------- state")]
        helpers = source[source.index("backup_once() {"):source.index("svc_inactive() {")]
        pycheck = ""
        if "PY_CFGPOLICY" in source:
            pycheck = source[source.index("read -r -d '' PY_CFGPOLICY"):source.index("jget() {")]
        verb = source[source.index("v_cfg_install() {"):source.index("v_reg_measure() {")]
        script = '''set -euo pipefail
BASE=$1; shift
WORK=$BASE/work; ETC=$BASE/etc; PREV=$BASE/prev; GA=$BASE/ga; STATE=$GA/state; CFGTPL=$BASE/tpl
ARGS=("$@")
die() { echo "$*" >&2; exit 1; }
usage() { echo "$*" >&2; exit 2; }
say() { echo "$*"; }
need_argc() { :; }
need_name() { [[ $1 == pilot ]]; }
need_sha() { :; }
norm_sha() { printf 'sha256:%s' "${1#sha256:}"; }
sha() { local h; h=$(sha256sum -- "$1") || return 1; printf 'sha256:%s' "${h%% *}"; }
st_get() { return 0; }; st_put() { :; }; st_reset() { :; }; st_rank() { echo 0; }
check_tools() { :; }; mkwork() { :; }; prep_ga() { :; }; need_inactive() { :; }
stage_copy() { cp -- "$BASE/stage/$2" "$3"; }
cfg_structural() { CFG_EXTRA=$(grep -Eo 'sha256:[0-9a-f]{64}' "$2" | head -1); }
py() { python3 -I -c "$@"; }
install() {
 local args=()
 while (($#)); do case $1 in -o|-g) shift 2;; *) args+=("$1"); shift;; esac; done
 command install "${args[@]}"
}
''' + staging + helpers + pycheck + verb + "\nv_cfg_install\n"
        want = "sha256:" + hashlib.sha256(cfg).hexdigest()
        got = subprocess.run(["bash", "-c", script, "cfg-test", str(base), "pilot", want], capture_output=True, text=True)
        return got, base / "etc/serve-pi-service.json"

    def test_matching_policy_installs_the_config(self):
        for paths in ((), ("out/report.txt",)):
            with self.subTest(paths=paths):
                got, installed = self.run_cfg(config_paths=paths, policy={"bash_output_paths": list(paths), "network_destinations": []})
                self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
                self.assertTrue(installed.is_file())

    def test_a_different_policy_refuses_before_install(self):
        for name, paths, policy in (
                ("bash paths the raters did not see", ("out/report.txt",), {"bash_output_paths": [], "network_destinations": []}),
                ("bash paths the config does not allow", (), {"bash_output_paths": ["out/report.txt"], "network_destinations": []}),
                ("a different order", ("a/", "b/"), {"bash_output_paths": ["b/", "a/"], "network_destinations": []}),
                ("a network destination the cell cannot reach", (), {"bash_output_paths": [], "network_destinations": ["127.0.0.9"]}),
                ("no network destination list", (), {"bash_output_paths": []}),
                ("no policy in the receipt", (), "absent"),
                ("a policy that is not an object", (), ["out/"])):
            with self.subTest(name):
                got, installed = self.run_cfg(config_paths=paths, policy=policy)
                self.assertNotEqual(got.returncode, 0, got.stdout)
                self.assertIn("public policy", got.stderr)
                self.assertFalse(installed.exists())

    def test_missing_installed_receipt_refuses(self):
        got, installed = self.run_cfg(receipt=False)
        self.assertNotEqual(got.returncode, 0, got.stdout)
        self.assertFalse(installed.exists())


class VerifyRetainedTests(unittest.TestCase):
    """verify-retained runs the offline verifier once, only with the service stopped, and exports its one JSON line."""

    runid = "production-run-0123456789ab"
    report = '{"run_id":"production-run-0123456789ab","verified":true,"operations":1,"promoted_bash":0,"denied_bash":1}'

    def run_verb(self, *args, active=False, session_audit=True, tb_exit=0):
        tmp = tempfile.TemporaryDirectory(prefix="tbound-op-verify-retained-")
        self.addCleanup(tmp.cleanup)
        base = Path(tmp.name)
        for name in ("audit", "work", "out"):
            (base / name).mkdir()
        if session_audit:
            (base / "audit" / (self.runid + "-session-audit.jsonl")).touch()
        fake = ["#!/bin/bash", 'printf "%s\\n" "$*" >>"$(dirname "$0")/tb-calls"']
        fake += ["echo 'refused: the settlement is missing' >&2", "exit %d" % tb_exit] if tb_exit else ["echo '%s'" % self.report]
        (base / "tb").write_text("\n".join(fake) + "\n")
        (base / "tb").chmod(0o700)
        lines = source.splitlines()
        grammar = [next(l for l in lines if l.startswith(prefix)) for prefix in ("re_runid=", "need_argc() {", "need_inactive() {")]
        verb = source[source.index("v_verify_retained() {"):source.index("GE_OUT=$GA/out")]
        script = """set -euo pipefail
BASE=$1; shift
VERB=verify-retained
WORK=$BASE/work; AUDIT=$BASE/audit; OUTROOT=$BASE/out; TB=$BASE/tb
ARGS=("$@")
die() { echo "$*" >&2; exit 1; }
usage() { echo "$*" >&2; exit 2; }
say() { echo "$*"; }
check_tools() { :; }
mkwork() { :; }
put_out() { mkdir -p "$OUTROOT/$1"; cp -- "$2" "$OUTROOT/$1/$3"; }
svc_inactive() { return $ACTIVE; }
ACTIVE=%d
""" % (1 if active else 0) + "\n".join(grammar) + "\n" + verb + "\nv_verify_retained\n"
        got = subprocess.run(["bash", "-c", script, "verify-test", str(base), *args], capture_output=True, text=True)
        calls = (base / "tb-calls").read_text().splitlines() if (base / "tb-calls").exists() else []
        exported = base / "out" / "_" / ("verify-retained-" + self.runid + ".json")
        return got, calls, exported

    def test_verified_run_is_exported_and_printed(self):
        got, calls, exported = self.run_verb(self.runid)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertEqual(calls, ["verify-retained --run-id " + self.runid])
        self.assertEqual(exported.read_text(), self.report + "\n")
        self.assertIn(self.report, got.stdout)
        self.assertIn("verify-retained " + self.runid + " ok", got.stdout)

    def test_refused_before_the_verifier_runs(self):
        for name, args, kwargs, code, message in (
                ("bad RUNID", ("short",), {}, 2, "bad RUNID"),
                ("no RUNID", (), {}, 2, "wrong number"),
                ("extra argument", (self.runid, "extra"), {}, 2, "wrong number"),
                ("active service", (self.runid,), {"active": True}, 1, "must be inactive"),
                ("no such run", (self.runid,), {"session_audit": False}, 1, "session-audit.jsonl")):
            with self.subTest(name):
                got, calls, exported = self.run_verb(*args, **kwargs)
                self.assertEqual(got.returncode, code, got.stdout + got.stderr)
                self.assertIn(message, got.stderr)
                self.assertEqual(calls, [])
                self.assertFalse(exported.exists())

    def test_a_refusal_of_the_verifier_exports_nothing(self):
        got, calls, exported = self.run_verb(self.runid, tb_exit=2)
        self.assertEqual(got.returncode, 1, got.stdout + got.stderr)
        self.assertIn("refused", got.stderr)
        self.assertNotIn("verified", got.stdout)
        self.assertEqual(calls, ["verify-retained --run-id " + self.runid])
        self.assertFalse(exported.exists())


unittest.main(verbosity=2)
PY
