#!/usr/bin/env python3
"""Recovery helper regressions: real private files/flock, fake CLI/clock only.

Never invoke a VM path, real systemd, core, cloud API or production account.
The Go recovery tests separately exercise the real ledger and health interlock.
"""
import copy
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("recover_hy2", ROOT / "recover-hy2.py")
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)
GIB = 1 << 30


def initial_status():
    users = []
    for name, revoked, disabled in (("owner", False, False), ("friend-1", False, True), ("retired", True, True)):
        users.append({"name": name, "quota_bytes": 90 * GIB if name == "owner" else 10 * GIB,
                      "used_bytes": 34 * GIB if name == "owner" else 0,
                      "conservative_adjustment": 34 * GIB if name == "owner" else 0,
                      "tx": 0, "rx": 0, "disabled": disabled, "revoked": revoked})
    return {"recovery_protocol": 1, "month": "2026-10", "users": users,
            "accounting": {"generation": "old-test-generation", "last_sample": 1000000000,
                           "arm_until": 0, "review": True, "hold": True},
            "core_live": False, "healthy": False, "authentication_allowed": False,
            "vm_error": "", "stats_error": "core identity unavailable", "certificate_error": ""}


class Fixture:
    def __init__(self, root):
        self.root, self.clock = root, 0.0
        self.calls, self.output = [], []
        self.state = initial_status()
        self.phase = "stopped"
        self.arm_count = self.start_count = self.activate_count = 0
        self.mode, self.fail = "", ""
        self.gate_delay = self.baseline_delay = self.baseline_probes = 0
        self.gate_cost = self.baseline_cost = 0
        self.started_at = None
        self.protocol = b"recovery-protocol=1"
        self.on_arm = None
        self.helper = recovery.Recovery({"sni": "rr.example.com", "certificate": str(root / "fullchain.cer"),
                                         "health_blocked_file": str(root / "blocked")},
                                        run=self.run, clock=lambda: self.clock, sleep=self.sleep, output=self.output.append)
        self.directory = root / "reviewed"

    def sleep(self, n):
        self.clock += n

    def usage(self, name="owner"):
        return next(u["used_bytes"] for u in self.state["users"] if u["name"] == name)

    def run(self, args, *, capture_output, timeout):
        assert capture_output and timeout > 0
        self.calls.append((list(args), timeout, self.clock))
        code, data = 0, b""
        if args == [recovery.CORE, "version"]:
            data = b"hy2-account 1; core test; " + self.protocol + b"\n"
        elif args[:2] == [recovery.CORE, "validate-cert"]:
            code = int(self.fail == "certificate")
        elif args == [recovery.ADMIN, "status"]:
            if self.phase == "started":
                self.clock += min(self.baseline_cost, timeout)
                if self.baseline_cost >= timeout:
                    raise subprocess.TimeoutExpired(args, timeout)
                self.baseline_probes += 1
                if self.mode != "no-baseline" and self.baseline_probes > self.baseline_delay:
                    self.state.update(healthy=True, stats_error="")
                    self.state["accounting"].update(arm_until=0, last_sample=2000000000,
                                                     generation="old-test-generation" if self.mode == "old-generation" else "new-test-generation")
            if self.fail == "status":
                code, data = 1, b"private fixture output"
            else:
                data = json.dumps(self.state).encode()
        elif args[:2] == ["systemctl", "show"]:
            data = b"active\n" if self.fail == "active" else b"inactive\n"
        elif args[:3] == ["ss", "-H", "-lun"]:
            data = b"test occupied UDP port\n" if self.fail == "port" else b""
        elif args[:3] == ["systemctl", "is-active", "--quiet"]:
            assert len(args) == 4, "a combined is-active can accept only one active timer"
            code = int(self.fail == args[-1])
        elif args[:3] == [recovery.ADMIN, "system", "arm"]:
            self.arm_count += 1
            charges_path = Path(args[args.index("--charges-file") + 1])
            assert charges_path.parent.name == "attempt" and charges_path.parent.is_dir()
            assert (charges_path.parent / "review.json").is_file()
            if self.on_arm:
                self.on_arm(charges_path)
            if self.mode == "arm-uncommitted":
                return subprocess.CompletedProcess(args, 1, b"", b"private fixture error")
            expected_month = args[args.index("--expected-month") + 1]
            if self.mode == "month-at-arm":
                self.state["month"] = "2026-11"
            if expected_month != self.state["month"]:
                return subprocess.CompletedProcess(args, 1, b"", b"month changed")
            for name, n in recovery.read_private(charges_path).items():
                u = next(u for u in self.state["users"] if u["name"] == name)
                u["used_bytes"] += n
                u["conservative_adjustment"] += n
            self.state["accounting"].update(review=False, hold=True, arm_until=45, last_sample=0)
            if self.mode == "arm-witness-error":
                self.state["data_error"] = "test committed but witness failed"
                self.state["accounting"]["review"] = True
                return subprocess.CompletedProcess(args, 1, b"", b"private fixture error")
            if self.mode == "arm-timeout":
                self.clock += timeout
                raise subprocess.TimeoutExpired(args, timeout)
            if self.mode == "arm-interrupted":
                raise KeyboardInterrupt()
        elif args == [recovery.CORE, "health", "gate"]:
            self.clock += min(self.gate_cost, timeout)
            if self.gate_cost >= timeout:
                raise subprocess.TimeoutExpired(args, timeout)
            code = int(self.mode == "no-gate" or self.gate_delay > 0)
            self.gate_delay -= 1
        elif args == ["systemctl", "start", "hysteria-server.service"]:
            self.start_count += 1
            if self.fail == "start":
                code = 1
            else:
                self.phase = "started"
                self.started_at = self.clock
                self.state["core_live"] = True
        elif args == [recovery.ADMIN, "system", "activate"]:
            self.activate_count += 1
            if self.fail == "activate":
                code = 1
            else:
                self.phase = "active"
                self.state["accounting"]["hold"] = False
                self.state["authentication_allowed"] = True
                if self.mode == "bad-final":
                    self.state["stats_error"] = "test new statistics failure"
                if self.mode == "month-after-activate":
                    self.state["month"] = "2026-11"
        else:
            raise AssertionError(f"unexpected command (no real fallback allowed): {args}")
        return subprocess.CompletedProcess(args, code, data, b"private fixture error" if code else b"")

    def prepare(self):
        self.helper.prepare(self.directory)

    def approve(self, charges=None, reason="Reviewed synthetic gap evidence; not production usage"):
        review = recovery.read_private(self.directory / "review.json")
        review["charges"] = charges if charges is not None else {"owner": GIB, "friend-1": 0, "retired": 50}
        review["reason"] = reason
        (self.directory / "review.json").write_text(json.dumps(review))

    def execute(self):
        self.helper.execute(self.directory, True)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="hy2-recovery-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.f = Fixture(self.root)

    def prepared(self):
        self.f.prepare()
        self.f.approve()

    def assert_no_write(self):
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (0, 0, 0))
        self.assertFalse((self.f.directory / "attempt").exists())

    def test_prepare_never_infers_zero_or_one_gib_for_any_account(self):
        self.f.prepare()
        review = recovery.read_private(self.f.directory / "review.json")
        self.assertEqual(review["charges"], {"owner": None, "friend-1": None, "retired": None})
        self.assertEqual(review["reason"], "")
        self.assert_no_write()
        self.assertEqual(self.f.usage(), 34 * GIB)
        for p in self.f.directory.iterdir():
            self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.f.directory.stat().st_mode), 0o700)

    def test_prepare_refuses_existing_workspace_without_overwriting_review(self):
        self.prepared()
        before = (self.f.directory / "review.json").read_bytes()
        with self.assertRaises(FileExistsError):
            self.f.prepare()
        self.assertEqual((self.f.directory / "review.json").read_bytes(), before)
        self.assert_no_write()

    def test_execute_requires_explicit_review_acknowledgement(self):
        self.prepared()
        with self.assertRaisesRegex(recovery.Stop, "accept-reviewed-charges"):
            self.f.helper.execute(self.f.directory, False)
        self.assert_no_write()

    def test_unreviewed_invalid_or_missing_account_values_are_rejected(self):
        self.prepared()
        good = {"owner": GIB, "friend-1": 0, "retired": 50}
        invalid = [good | {"owner": n} for n in (None, True, -1, 1.5, "1", 181 * GIB)]
        invalid += [{"owner": GIB, "friend-1": 0}, good | {"unknown": 0}]
        for charges in invalid:
            with self.subTest(charges=charges):
                self.f.approve(charges)
                with self.assertRaises(recovery.Stop):
                    self.f.execute()
                self.assert_no_write()

    def test_review_requires_evidence_reason_even_for_explicit_zero(self):
        self.prepared()
        for reason in ("", "   ", "short", "a" * 201):
            self.f.approve({"owner": 0, "friend-1": 0, "retired": 0}, reason)
            with self.assertRaisesRegex(recovery.Stop, "reason"):
                self.f.execute()
            self.assert_no_write()
        self.f.approve({"owner": 0, "friend-1": 0, "retired": 0})
        self.f.execute()
        self.assertEqual(self.f.usage(), 34 * GIB)

    def test_success_and_replay_never_charge_twice(self):
        self.prepared()
        self.f.execute()
        self.assertTrue(self.f.output[-1].startswith("RECOVERED "))
        self.assertEqual(self.f.usage(), 35 * GIB)
        self.assertEqual(self.f.usage("retired"), 50)
        self.assertTrue(self.f.state["users"][-1]["revoked"])
        self.assertTrue((self.f.directory / "attempt/recovered.json").exists())
        calls = len(self.f.calls)
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(len(self.f.calls), calls)
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (1, 1, 1))
        self.assertEqual(self.f.usage(), 35 * GIB)

    def test_arm_error_before_commit_is_still_not_automatically_retried(self):
        self.prepared()
        self.f.mode = "arm-uncommitted"
        with self.assertRaises(recovery.Stop):
            self.f.execute()
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(self.f.arm_count, 1)
        self.assertEqual(self.f.usage(), 34 * GIB)
        self.assertEqual(self.f.start_count, 0)

    def test_committed_witness_error_cannot_replay_through_review_true(self):
        self.prepared()
        self.f.mode = "arm-witness-error"
        with self.assertRaises(recovery.Stop):
            self.f.execute()
        self.assertTrue(self.f.state["accounting"]["review"])
        self.assertEqual(self.f.usage(), 35 * GIB)
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        with self.assertRaisesRegex(recovery.Stop, "database/witness"):
            self.f.helper.prepare(self.root / "another")
        self.assertEqual(self.f.arm_count, 1)
        self.assertFalse((self.f.directory / "attempt/arm-returned-ok.json").exists())

    def test_arm_timeout_preserves_uncertain_receipt_and_never_retries(self):
        self.prepared()
        self.f.mode = "arm-timeout"
        with self.assertRaisesRegex(recovery.Stop, "committed"):
            self.f.execute()
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(self.f.arm_count, 1)
        self.assertEqual(self.f.usage(), 35 * GIB)
        self.assertEqual(self.f.start_count, 0)

    def test_interruption_after_commit_leaves_attempt_and_releases_lock(self):
        self.prepared()
        self.f.mode = "arm-interrupted"
        with self.assertRaises(KeyboardInterrupt):
            self.f.execute()
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(self.f.arm_count, 1)
        self.assertEqual(self.f.usage(), 35 * GIB)

    def test_intent_is_fsynced_before_first_mutating_command(self):
        self.prepared()
        synced = []
        sync = recovery.sync_dir
        def record(path):
            sync(path)
            synced.append(path)
        def at_arm(path):
            self.assertIn(path.parent, synced)
            self.assertIn(self.f.directory, synced)
            for p in path.parent.iterdir():
                self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o600)
        self.f.on_arm = at_arm
        with mock.patch.object(recovery, "sync_dir", side_effect=record):
            self.f.execute()

    def test_intent_persistence_failure_never_sends_arm(self):
        self.prepared()
        with mock.patch.object(recovery, "sync_dir", side_effect=OSError("test fsync failure")):
            with self.assertRaises(OSError):
                self.f.execute()
        self.assertEqual(self.f.arm_count, 0)
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(self.f.arm_count, 0)

    def test_incomplete_attempt_after_crash_is_never_reused(self):
        self.prepared()
        (self.f.directory / "attempt").mkdir(mode=0o700)
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual(self.f.arm_count, 0)

    def test_preflight_failures_stop_before_intent_or_mutations(self):
        self.prepared()
        for failure in ("active", "port", "certificate", "status", "gcp-egress-guard.timer", "hy2-health.timer"):
            with self.subTest(failure=failure):
                self.f.fail = failure
                with self.assertRaises(recovery.Stop) as result:
                    self.f.execute()
                self.assertNotIn("private fixture", str(result.exception))
                self.assert_no_write()
        self.f.fail = ""
        for field, value in (("core_live", True), ("data_error", "witness failed"), ("vm_error", "guard failed"),
                             ("certificate_error", "wrong certificate"), ("authentication_allowed", True)):
            with self.subTest(field=field):
                original = copy.deepcopy(self.f.state)
                self.f.state[field] = value
                with self.assertRaises(recovery.Stop):
                    self.f.execute()
                self.assert_no_write()
                self.f.state = original

    def test_old_cli_or_daemon_cannot_use_new_recovery(self):
        self.prepared()
        self.f.protocol = b"old CLI"
        with self.assertRaisesRegex(recovery.Stop, "CLI recovery protocol"):
            self.f.execute()
        self.f.protocol = b"recovery-protocol=1"
        for value in (None, 0, True, "1"):
            self.f.state["recovery_protocol"] = value
            with self.assertRaisesRegex(recovery.Stop, "accountd recovery protocol"):
                self.f.execute()
        self.assert_no_write()

    def test_ledger_month_account_or_flags_changed_after_review_are_rejected(self):
        self.prepared()
        original = copy.deepcopy(self.f.state)
        mutations = [lambda s: s.update(month="2026-11"),
                     lambda s: s["accounting"].update(hold=False),
                     lambda s: s["users"][0].update(conservative_adjustment=35 * GIB, used_bytes=35 * GIB),
                     lambda s: s["users"][1].update(disabled=False),
                     lambda s: s["users"][1].update(name="renamed")]
        for mutate in mutations:
            self.f.state = copy.deepcopy(original)
            mutate(self.f.state)
            with self.assertRaisesRegex(recovery.Stop, "changed since review"):
                self.f.execute()
            self.assert_no_write()

    def test_month_change_at_arm_is_explicitly_guarded(self):
        self.prepared()
        self.f.mode = "month-at-arm"
        with self.assertRaises(recovery.Stop):
            self.f.execute()
        self.assertEqual(self.f.usage(), 34 * GIB)
        self.assertEqual(self.f.start_count, 0)

    def test_gate_deadline_counts_request_time_and_never_starts_core(self):
        self.prepared()
        self.f.mode, self.f.gate_cost = "no-gate", 2
        start = self.f.clock
        with self.assertRaisesRegex(recovery.Stop, "deadline"):
            self.f.execute()
        self.assertLessEqual(self.f.clock - start, 10)
        calls = [(timeout, at) for args, timeout, at in self.f.calls if args == [recovery.CORE, "health", "gate"]]
        self.assertGreater(len(calls), 0)
        self.assertTrue(all(timeout <= min(2, start + 10 - at) for timeout, at in calls))
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (1, 0, 0))

    def test_baseline_deadline_is_fifteen_seconds_not_fifteen_slow_calls(self):
        self.prepared()
        self.f.mode, self.f.baseline_cost = "no-baseline", 2
        with self.assertRaisesRegex(recovery.Stop, "deadline"):
            self.f.execute()
        self.assertLessEqual(self.f.clock - self.f.started_at, 15)
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (1, 1, 0))

    def test_delayed_new_baseline_activates_once(self):
        self.prepared()
        self.f.gate_delay, self.f.baseline_delay = 2, 3
        self.f.execute()
        self.assertEqual(self.f.baseline_probes, 4)
        self.assertEqual(self.f.activate_count, 1)
        self.assertTrue(self.f.output[-1].startswith("RECOVERED "))

    def test_old_generation_is_not_accepted_as_new_baseline(self):
        self.prepared()
        self.f.mode = "old-generation"
        with self.assertRaisesRegex(recovery.Stop, "deadline"):
            self.f.execute()
        self.assertEqual(self.f.activate_count, 0)

    def test_activate_failure_is_single_attempt_with_no_restart_fallback(self):
        self.prepared()
        self.f.fail = "activate"
        with self.assertRaisesRegex(recovery.Stop, "activate"):
            self.f.execute()
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (1, 1, 1))
        self.assertFalse(any(line.startswith("RECOVERED ") for line in self.f.output))
        with self.assertRaises(recovery.Stop):
            self.f.execute()
        self.assertEqual(self.f.activate_count, 1)

    def test_final_error_or_month_change_is_not_reported_recovered(self):
        for mode in ("bad-final", "month-after-activate"):
            with self.subTest(mode=mode):
                root = self.root / mode
                root.mkdir(mode=0o700)
                self.f = Fixture(root)
                self.prepared()
                self.f.mode = mode
                with self.assertRaisesRegex(recovery.Stop, "not confirmed"):
                    self.f.execute()
                self.assertFalse((self.f.directory / "attempt/recovered.json").exists())
                self.assertFalse(any(line.startswith("RECOVERED ") for line in self.f.output))

    def test_global_lock_serializes_different_workspaces(self):
        self.prepared()
        with recovery.recovery_lock(str(self.f.helper.blocked) + ".recovery.lock"):
            with self.assertRaisesRegex(recovery.Stop, "another recovery"):
                self.f.execute()
        self.assert_no_write()

    def test_symlink_or_public_review_files_are_rejected(self):
        self.prepared()
        path = self.f.directory / "review.json"
        path.chmod(0o644)
        with self.assertRaisesRegex(recovery.Stop, "private regular"):
            self.f.execute()
        path.chmod(0o600)
        private = path.with_name("kept.json")
        path.rename(private)
        path.symlink_to(private)
        with self.assertRaises(OSError):
            self.f.execute()
        self.assert_no_write()

    def test_duplicate_json_keys_are_rejected_instead_of_last_value_wins(self):
        with self.assertRaisesRegex(recovery.Stop, "duplicate"):
            recovery.decode('{"charges":{"owner":1,"owner":0}}')

    def test_pre_intent_failure_can_retry_after_correction_without_double_charge(self):
        self.prepared()
        self.f.fail = "certificate"
        with self.assertRaises(recovery.Stop):
            self.f.execute()
        self.assert_no_write()
        self.f.fail = ""
        self.f.execute()
        self.assertEqual(self.f.arm_count, 1)
        self.assertEqual(self.f.usage(), 35 * GIB)

    def test_start_failure_retains_receipt_without_restart_or_arm_replay(self):
        self.prepared()
        self.f.fail = "start"
        with self.assertRaisesRegex(recovery.Stop, "core start"):
            self.f.execute()
        self.assertTrue((self.f.directory / "attempt/arm-returned-ok.json").exists())
        with self.assertRaisesRegex(recovery.Stop, "attempt already recorded"):
            self.f.execute()
        self.assertEqual((self.f.arm_count, self.f.start_count, self.f.activate_count), (1, 1, 0))
        self.assertEqual(self.f.usage(), 35 * GIB)

    def test_successful_process_response_at_deadline_is_not_accepted(self):
        def late_success(args, *, capture_output, timeout):
            self.f.clock += timeout
            return subprocess.CompletedProcess(args, 0, b"", b"")
        self.f.helper.run = late_success
        with self.assertRaisesRegex(recovery.Stop, "deadline"):
            self.f.helper.command([recovery.CORE, "health", "gate"], self.f.clock + 1)
        self.assertEqual(self.f.start_count, 0)

    def test_probe_processing_time_also_counts_towards_phase_deadline(self):
        def late_ready(deadline):
            self.f.clock = deadline
            return True
        with self.assertRaisesRegex(recovery.Stop, "deadline"):
            self.f.helper.wait(15, late_ready, "baseline")
        self.assertEqual(self.f.clock, 15)

    def test_cli_has_no_clean_or_unplanned_autocharging_mode(self):
        for mode in ("clean", "unplanned"):
            result = subprocess.run(["python3", str(ROOT / "recover-hy2.py"), mode], capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(b"invalid choice", result.stderr)
        # --help must work without root/config and without invoking any service.
        result = subprocess.run(["python3", str(ROOT / "recover-hy2.py"), "--help"], capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0)
        self.assertIn(b"prepare", result.stdout)


if __name__ == "__main__":
    unittest.main()
