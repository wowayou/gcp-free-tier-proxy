#!/usr/bin/env python3
"""Installer guard gates and bounded post-start readiness.

Guard tests use namespace-root and stop before installation. Readiness tests
execute the production Python block with a fake clock/subprocesses; tail
wiring tests use only fake systemctl/python3. No real systemd or VM paths.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

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


class InstallerQuotaPoolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = (ROOT / "install-hy2-account.sh").read_text()
        start = source.index('QUOTA_POOL_GIB="${QUOTA_POOL_GIB:')
        end = source.index('\nfor c in python3 sha256sum', start)
        cls.block = source[start:end]

    def test_explicit_pool_range_and_decimal_parsing(self):
        for value, expected in (("1", "1"), ("140", "140"), ("150", "150"), ("180", "180"), ("090", "90")):
            with self.subTest(value=value):
                r = subprocess.run(["bash", "-euo", "pipefail", "-c", self.block + '\nprintf "%s\\n" "$QUOTA_POOL_GIB"'],
                                   env=os.environ | {"QUOTA_POOL_GIB": value}, capture_output=True, text=True, timeout=5)
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertEqual(r.stdout.strip(), expected)
        for value in ("0", "000", "181", "1800", "-1", "1.5", "NaN", "", "$(id)", " 150"):
            with self.subTest(value=value):
                r = subprocess.run(["bash", "-euo", "pipefail", "-c", self.block],
                                   env=os.environ | {"QUOTA_POOL_GIB": value}, capture_output=True, text=True, timeout=5)
                self.assertNotEqual(r.returncode, 0)

    def test_installer_ceiling_matches_go_configuration(self):
        code = (ROOT / "hy2-account/internal/account/config.go").read_text()
        self.assertIn("const MaxQuotaPoolGiB int64 = 180", code)
        self.assertIn("10#$QUOTA_POOL_GIB<=180", self.block)


class InstallerReadinessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = (ROOT / "install-hy2-account.sh").read_text()
        cls.code = compile(source.split("<<'PY_READY'\n", 1)[1].split("\nPY_READY\n", 1)[0],
                           "installer-readiness", "exec")
        cls.tail = source[source.index("systemctl enable --now hy2-accountd.service\n"):]

    def probe(self, *, delay=0, hung=False, status=None, payload=None, service=None,
              service_error=None, restart_after_probe=False, missing_admin=False,
              query_cost=0):
        clock = [0.0]
        calls = []
        probes = [0]
        normal = {"accounting": {"hold": True, "review": True},
                  "authentication_allowed": False, "core_live": False,
                  "healthy": False, "users": None}
        if payload is None:
            payload = json.dumps(normal if status is None else status).encode()

        def run(args, *, capture_output, timeout):
            self.assertTrue(capture_output)
            self.assertGreater(timeout, 0)
            self.assertLessEqual(timeout, 2)
            self.assertLessEqual(timeout, 20 - clock[0])
            calls.append(args)
            if args[0] == "systemctl":
                self.assertEqual(args, ["systemctl", "show", "hy2-accountd.service", "--no-pager",
                                        "--property=ActiveState,SubState,NRestarts"])
                if service_error == "timeout":
                    raise subprocess.TimeoutExpired(args, timeout)
                if service_error == "missing":
                    raise FileNotFoundError("sensitive fixture must not be printed")
                if service_error == "nonzero":
                    return subprocess.CompletedProcess(args, 1, b"sensitive fixture", b"private error")
                if service_error in ("bad_utf8", "bad_property"):
                    data = b"\xff" if service_error == "bad_utf8" else b"sensitive fixture"
                    return subprocess.CompletedProcess(args, 0, data, b"")
                data = service or b"ActiveState=active\nSubState=running\nNRestarts=0\n"
                if restart_after_probe and probes[0]:
                    data = b"ActiveState=active\nSubState=running\nNRestarts=1\n"
                clock[0] += min(query_cost, timeout)
                return subprocess.CompletedProcess(args, 0, data, b"")
            self.assertEqual(args, ["/usr/local/sbin/hy2-admin", "status"])
            probes[0] += 1
            if missing_admin:
                raise FileNotFoundError("sensitive fixture must not be printed")
            if hung:
                clock[0] += timeout
                raise subprocess.TimeoutExpired(args, timeout)
            if probes[0] <= delay:
                return subprocess.CompletedProcess(args, 1, b"sensitive fixture", b"private socket error")
            return subprocess.CompletedProcess(args, 0, payload, b"")

        out, err = io.StringIO(), io.StringIO()
        failure = None
        with mock.patch("subprocess.run", side_effect=run), \
             mock.patch("time.monotonic", side_effect=lambda: clock[0]), \
             mock.patch("time.sleep", side_effect=lambda n: clock.__setitem__(0, clock[0] + n)), \
             contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                exec(self.code, {"__name__": "__main__"})
            except SystemExit as exc:
                failure = str(exc)
        self.assertNotIn("sensitive fixture", out.getvalue() + err.getvalue() + (failure or ""))
        self.assertEqual(out.getvalue(), "", "status/credentials must not be printed")
        self.assertEqual(err.getvalue(), "")
        self.assertLessEqual(clock[0], 20)
        return failure, probes[0], calls, clock[0]

    def test_ready_immediately_requires_post_probe_service_check(self):
        failure, probes, calls, elapsed = self.probe()
        self.assertIsNone(failure)
        self.assertEqual(probes, 1)
        self.assertEqual([x[0] for x in calls], ["systemctl", "/usr/local/sbin/hy2-admin", "systemctl"])
        self.assertEqual(elapsed, 0)

    def test_delayed_socket_eventually_passes(self):
        failure, probes, _, elapsed = self.probe(delay=3)
        self.assertIsNone(failure)
        self.assertEqual(probes, 4)
        self.assertEqual(elapsed, 3)

    def test_unavailable_socket_stops_at_whole_phase_deadline(self):
        failure, probes, _, elapsed = self.probe(delay=100)
        self.assertIn("deadline exceeded", failure)
        self.assertEqual(probes, 20)
        self.assertEqual(elapsed, 20)

    def test_hung_requests_are_bounded_by_remaining_time(self):
        failure, probes, _, elapsed = self.probe(hung=True)
        self.assertIn("deadline exceeded", failure)
        self.assertEqual(probes, 7)
        self.assertEqual(elapsed, 20)

    def test_service_failure_or_restarts_fail_without_socket_retry(self):
        for state in [b"ActiveState=failed\nSubState=failed\nNRestarts=0\n",
                      b"ActiveState=activating\nSubState=auto-restart\nNRestarts=1\n",
                      b"ActiveState=active\nSubState=running\nNRestarts=1\n",
                      b"ActiveState=active\nSubState=running\n"]:
            with self.subTest(state=state):
                failure, probes, _, _ = self.probe(service=state)
                self.assertIn("not stably running", failure)
                self.assertEqual(probes, 0)

    def test_service_query_failures_do_not_leak_output(self):
        for mode in ("timeout", "missing", "nonzero", "bad_utf8", "bad_property"):
            with self.subTest(mode=mode):
                failure, probes, _, _ = self.probe(service_error=mode)
                self.assertIn("cannot read service state", failure)
                self.assertEqual(probes, 0)

    def test_restart_during_successful_probe_is_rejected(self):
        failure, probes, _, _ = self.probe(restart_after_probe=True)
        self.assertIn("not stably running", failure)
        self.assertEqual(probes, 1)

    def test_malformed_status_fails_immediately(self):
        for payload in (b"not json sensitive fixture", b"\xff", b"[]", b"null"):
            with self.subTest(payload=payload):
                failure, probes, _, _ = self.probe(payload=payload)
                self.assertIn("invalid status", failure)
                self.assertEqual(probes, 1)

    def test_status_must_be_held_empty_and_readable(self):
        normal = {"accounting": {"hold": True, "review": True},
                  "authentication_allowed": False, "core_live": False,
                  "healthy": False, "users": []}
        failure, _, _, _ = self.probe(status=normal)
        self.assertIsNone(failure)
        cases = [{}, normal | {"accounting": {"hold": False, "review": True}},
                 normal | {"accounting": {"hold": True, "review": False}},
                 normal | {"authentication_allowed": True}, normal | {"core_live": True},
                 normal | {"healthy": True}, normal | {"users": [{"name": "fixture"}]},
                 normal | {"data_error": "private failure"},
                 normal | {"authentication_allowed": 0}]
        for status in cases:
            with self.subTest(status=status):
                failure, probes, _, _ = self.probe(status=status)
                self.assertIn("held, empty, readable", failure)
                self.assertEqual(probes, 1)

    def test_missing_admin_stops_immediately(self):
        failure, probes, _, _ = self.probe(missing_admin=True)
        self.assertIn("admin command unavailable", failure)
        self.assertEqual(probes, 1)

    def test_service_query_time_counts_towards_deadline(self):
        failure, probes, _, elapsed = self.probe(delay=100, query_cost=2)
        self.assertIn("deadline exceeded", failure)
        self.assertLess(probes, 20)
        self.assertEqual(elapsed, 20)

    def test_installer_tail_never_reports_prepared_after_failed_gate(self):
        # Exercise the shell integration too, but NEVER the mutating install body.
        # python3 is a stub here; the real embedded code is covered above.
        with tempfile.TemporaryDirectory(prefix="hy2-readiness-tail-") as d:
            root = Path(d)
            calls = root / "calls"
            for name, body in {
                "systemctl": '#!/bin/sh\nprintf "%s\\n" "$*" >>"$CALLS"\nexit 0\n',
                "python3": '#!/bin/sh\nwhile IFS= read -r line; do :; done\nexit "$PROBE_EXIT"\n',
            }.items():
                f = root / name
                f.write_text(body)
                f.chmod(0o700)
            for code in (0, 1):
                calls.unlink(missing_ok=True)
                env = os.environ | {"PATH": f"{root}:/usr/bin:/bin", "CALLS": str(calls),
                                    "PROBE_EXIT": str(code)}
                r = subprocess.run(["bash", "-euo", "pipefail", "-c", self.tail],
                                   capture_output=True, text=True, env=env, timeout=5)
                self.assertEqual(calls.read_text().splitlines(), ["enable --now hy2-accountd.service"])
                if code == 0:
                    self.assertEqual(r.returncode, 0, r.stderr)
                    self.assertIn("Prepared only", r.stdout)
                else:
                    self.assertNotEqual(r.returncode, 0)
                    self.assertNotIn("Prepared only", r.stdout)
                    self.assertIn("Do not rerun the installer", r.stderr)


if __name__ == "__main__":
    unittest.main()
