#!/usr/bin/env python3
"""Regression fixtures for dpkg's installed package manifest parsing."""

from pathlib import Path
import runpy
import unittest


parse_installed_package_manifest = runpy.run_path(
    str(Path(__file__).with_name("verify-offline.py"))
)["parse_installed_package_manifest"]


class InstalledPackageManifestTests(unittest.TestCase):
    def test_keeps_held_installed_packages_and_excludes_other_states(self):
        manifest = """\
ii \tzeta\t1.0
hi \tgcc-13-x86-64-linux-gnu\t13.3.0-6ubuntu2~24.04.1
ii \talpha\t2.0
rc \tconfig-only\t3.0
un \tuninstalled\t4.0
iF \thalf-configured\t5.0
i\tshort-status\t6.0
malformed-row
ii \tmissing-version
\tblank-status\t7.0
"""

        self.assertEqual(
            parse_installed_package_manifest(manifest),
            [
                {"package": "alpha", "version": "2.0"},
                {
                    "package": "gcc-13-x86-64-linux-gnu",
                    "version": "13.3.0-6ubuntu2~24.04.1",
                },
                {"package": "zeta", "version": "1.0"},
            ],
        )


if __name__ == "__main__":
    unittest.main()
