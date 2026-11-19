"""The committed proc-qual-dev-01 carrier is exactly what build_proc_qual.py writes.
Run from the repository root: TBOUND_RUN=<tbound-run> py -3 -I -m unittest discover -s corpus/sampling/qualification -p "test_*.py"
"""
import filecmp
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
COMMITTED = os.path.join(os.path.dirname(HERE), "carriers", "proc-qual-dev-01")
TBOUND_RUN = os.environ.get("TBOUND_RUN")


@unittest.skipUnless(TBOUND_RUN, "TBOUND_RUN is needed for fixture digests")
class RebuildTest(unittest.TestCase):
    def test_rebuild_matches_the_committed_carrier(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "out")
            subprocess.run([sys.executable, "-I", os.path.join(HERE, "build_proc_qual.py"), "--tbound-run", TBOUND_RUN,
                            "--out", out], check=True, capture_output=True)
            stack = [filecmp.dircmp(COMMITTED, out)]
            while stack:
                d = stack.pop()
                _, mismatch, errors = filecmp.cmpfiles(d.left, d.right, d.common_files, shallow=False)
                self.assertEqual((d.left_only, d.right_only, mismatch, errors), ([], [], [], []), d.left)
                stack.extend(d.subdirs.values())


if __name__ == "__main__":
    unittest.main()
