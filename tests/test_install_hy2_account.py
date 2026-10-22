#!/usr/bin/env python3
"""Pre-flight gate of install-hy2-account.sh, as namespace-root only.

Every case stops before any install/useradd/systemd mutation: either at the
guard-coverage check or at the fake `systemctl is-active` timer check, which
is the first command after it and always reports inactive here.
"""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
COVERAGE_ERROR = "does not cover both sing-box and hysteria-server"
NEXT_GATE = "gcp-egress-guard.timer is not active"


def unshare_available():
    if not shutil.which("unshare"):
        return False
    return subprocess.run(["unshare", "-r", "true"], capture_output=True, timeout=5).returncode == 0


@unittest.skipUnless(unshare_available(), "unprivileged user namespaces unavailable")
class InstallerGuardCoverageTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="hy2-install-test-")
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / "bin").mkdir()
        self.calls = self.root / "calls"
        fake = self.root / "bin/systemctl"
        fake.write_text(f"#!/bin/sh\necho \"$*\" >> '{self.calls}'\n[ \"$1\" = is-active ] || exit 99\nexit 3\n")
        fake.chmod(0o755)

    def run_installer(self, service_line):
        config = self.root / "guard"
        config.write_text(f"LIMIT_GIB=180\nWARN_GIB=150\n{service_line}\n")
        env = os.environ | {"PATH": f"{self.root / 'bin'}:{os.environ['PATH']}",
                            "CONFIRM_LOCAL_INSTALL": "yes", "GUARD_CONFIG": str(config)}
        return subprocess.run(["unshare", "-r", "bash", str(ROOT / "install-hy2-account.sh")],
                              env=env, capture_output=True, text=True, timeout=20)

    def test_lookalike_or_partial_service_lists_are_rejected(self):
        for line in ['SERVICE_NAMES="sing-box-old hysteria-server-old"', 'SERVICE_NAMES="sing-box"',
                     'SERVICE_NAMES="hysteria-server"', 'SERVICE_NAME=sing-box', 'SERVICE_NAMES="xsing-box hysteria-serverx"']:
            r = self.run_installer(line)
            self.assertNotEqual(r.returncode, 0)
            self.assertIn(COVERAGE_ERROR, r.stderr, line)
        self.assertFalse(self.calls.exists(), "installer continued past a failed coverage check")

    def test_exact_coverage_passes_to_next_gate_only(self):
        for line in ['SERVICE_NAMES="sing-box hysteria-server"', 'SERVICE_NAMES="hysteria-server.service sing-box.service"']:
            r = self.run_installer(line)
            self.assertNotEqual(r.returncode, 0)
            self.assertNotIn(COVERAGE_ERROR, r.stderr, line)
            self.assertIn(NEXT_GATE, r.stderr, line)
        self.assertEqual(self.calls.read_text().split("\n")[0], "is-active --quiet gcp-egress-guard.timer")


if __name__ == "__main__":
    unittest.main()
