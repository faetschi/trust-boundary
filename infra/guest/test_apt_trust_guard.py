#!/usr/bin/env python3
"""Regression fixtures for the embedded Ubuntu APT trust preflight."""
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("setup-ubuntu-24.04.sh")
source = SCRIPT.read_text(encoding="utf-8")
marker = "# Inspect effective APT settings without printing the configuration values."
marker_at = source.index(marker)
opener = "python3 - <<'PY'\n"
start = source.index(opener, marker_at) + len(opener)
end = source.index("\nPY", start)
GUARD = compile(source[start:end], f"{SCRIPT}:APT guard", "exec")

STOCK_SAFE_DUMP = """\
Dir::Etc::Trusted "trusted.gpg";
Dir::Etc::TrustedParts "trusted.gpg.d";
Acquire::AllowInsecureRepositories "0";
Acquire::AllowWeakRepositories "no";
Acquire::AllowDowngradeToInsecureRepositories "false";
"""


def run_guard(dump):
    completed = subprocess.CompletedProcess(["apt-config", "dump"], 0, dump, "")
    stdout, stderr = StringIO(), StringIO()
    with patch("subprocess.run", return_value=completed):
        with redirect_stdout(stdout), redirect_stderr(stderr):
            try:
                exec(GUARD, {})
            except SystemExit as exc:
                status = exc.code
            else:
                status = 0
    return status, stdout.getvalue(), stderr.getvalue()


class AptTrustGuardTests(unittest.TestCase):
    def assert_rejected(self, line):
        status, _, stderr = run_guard(STOCK_SAFE_DUMP + line + "\n")
        self.assertEqual(status, 2, stderr)

    def test_stock_keyring_path_and_false_flags_pass_with_defaults_omitted(self):
        status, stdout, stderr = run_guard(STOCK_SAFE_DUMP)
        self.assertEqual(status, 0, stderr)
        self.assertIn("APT effective trust configuration passed.", stdout)

    def test_stock_keyring_path_uses_canonical_name_normalization(self):
        dump = STOCK_SAFE_DUMP.replace(
            'Dir::Etc::Trusted "trusted.gpg";',
            'dIR::eTc::tru_st-ed "trusted.gpg";',
            1,
        )
        status, _, stderr = run_guard(dump)
        self.assertEqual(status, 0, stderr)

    def test_each_true_trust_override_rejects(self):
        for line in (
            'Acquire::AllowInsecureRepositories "yes";',
            'Acquire::AllowWeakRepositories "true";',
            'Acquire::AllowDowngradeToInsecureRepositories "on";',
            'APT::Get::AllowUnauthenticated "1";',
            'Acquire::Trusted "yes";',
        ):
            with self.subTest(line=line):
                self.assert_rejected(line)

    def test_unknown_boolean_value_rejects(self):
        self.assert_rejected('Acquire::AllowWeakRepositories "maybe";')

    def test_disabled_date_checks_reject(self):
        for line in (
            'Acquire::Check-Date "false";',
            'Acquire::Check-Valid-Until "off";',
        ):
            with self.subTest(line=line):
                self.assert_rejected(line)

    def test_malformed_relevant_line_rejects(self):
        self.assert_rejected("Acquire::AllowInsecureRepositories malformed")

    def test_nondefault_keyring_paths_reject(self):
        self.assert_rejected('Dir::Etc::Trusted "/tmp/custom.gpg";')
        self.assert_rejected('Dir::Etc::Trusted "no";')
        self.assert_rejected('Dir::Etc::Trusted "TRUSTED.GPG";')
        self.assert_rejected('Binary::apt::Dir::Etc::Trusted "trusted.gpg";')


if __name__ == "__main__":
    unittest.main()