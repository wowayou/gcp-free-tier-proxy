#!/usr/bin/env python3
"""Egress runtime regression tests with fake counters/systemctl, never host units."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
GIB = 2**30


class GuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="egress-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for p in ["state", "run", "bin", "masks", "sys/eth0/statistics", "early"]:
            (self.root / p).mkdir(parents=True, exist_ok=True)
        self.config = self.root / "config"
        self.config.write_text('LIMIT_GIB=180\nWARN_GIB=150\nSERVICE_NAMES="sing-box hysteria-server"\nINTERFACE=eth0\nBILLING_TIMEZONE=America/Los_Angeles\nMASK_ON_LIMIT=true\nSTART_NEXT_MONTH=false\n')
        (self.root / "boot").write_text("boot-a\n")
        (self.root / "sys/eth0/statistics/tx_bytes").write_text("100\n")
        self.env = os.environ | {
            "PATH": str(self.root / "bin") + ":" + os.environ["PATH"],
            "GUARD_CONFIG_FILE": str(self.config),
            "GUARD_STATE_DIR": str(self.root / "state"),
            "GUARD_STATUS_FILE": str(self.root / "run/status.json"),
            "GUARD_SYS_NET": str(self.root / "sys"),
            "GUARD_BOOT_ID_FILE": str(self.root / "boot"),
            "GUARD_MASK_DIR": str(self.root / "masks"),
            "TEST_ROOT": str(self.root),
            "TEST_MONTH": "2026-10", "TEST_NOW": "1792584000",
        }
        self.script("date", '#!/usr/bin/env python3\nimport os,sys\nprint(os.environ["TEST_MONTH"] if sys.argv[-1]=="+%Y-%m" else os.environ["TEST_NOW"])\n')
        self.script("logger", "#!/bin/sh\nexit 0\n")
        self.script("systemctl", '''#!/usr/bin/env python3
import os,sys,json,subprocess
from pathlib import Path
r=Path(os.environ["TEST_ROOT"])
a=sys.argv[1:]
with (r/"calls").open("a") as f: f.write(json.dumps(a)+"\\n")
s=next((v.removesuffix(".service") for v in a if v.endswith(".service")), "")
def masked():
    return any((r/d/(s+".service")).is_symlink() for d in ("masks", "early"))
if a[0]=="stop":
    if os.environ.get("FAIL_STOP")==s: raise SystemExit(1)
    (r/(s+".active")).unlink(missing_ok=True)
elif a[0]=="show":
    if "--property=ActiveState" in a: print("active" if (r/(s+".active")).exists() else "inactive")
    elif "--property=LoadState" in a: print("masked" if masked() and os.environ.get("FAIL_MASK")!=s else "loaded")
elif a[0]=="daemon-reload":
    if os.environ.get("FAIL_RELOAD")=="yes": raise SystemExit(1)
    for p in (r/"early").iterdir(): p.unlink()
    root=Path(os.environ["RUNTIME"]).resolve().parents[1]
    subprocess.run(["bash",str(root/"egress-guard/generator.sh"),str(r/"normal"),str(r/"early"),str(r/"late")],check=True,env=os.environ,timeout=5)
    if os.environ.get("HY2_HEALTH_BLOCKED_FILE"):
        subprocess.run(["bash",str(root/"hy2-account/systemd/hy2-health-generator.sh"),str(r/"normal"),str(r/"early"),str(r/"late")],check=True,env=os.environ,timeout=5)
elif a[0]=="start":
    if masked() or os.environ.get("FAIL_START")==s: raise SystemExit(1)
    if os.environ.get("NESTED_GATE")=="yes":
        proc=subprocess.run(["bash",os.environ["RUNTIME"],"--permit",s],env=os.environ,timeout=12)
        if proc.returncode: raise SystemExit(proc.returncode)
    (r/(s+".active")).write_text("true")
''')
        self.state()

    def script(self, name, code):
        path = self.root / "bin" / name
        path.write_text(code)
        path.chmod(0o755)

    def state(self, used=0, blocked=False, month="2026-10", tx=100, boot="boot-a", legacy=False):
        text = f"state_month={month}\nlast_tx={tx}\nused_bytes={used}\nblocked_by_guard={str(blocked).lower()}\nwarning_logged=false\n"
        if not legacy:
            text += f"last_boot_id={boot}\nlast_interface=eth0\nupdated_epoch=1792583900\n"
        (self.root / "state/state").write_text(text)

    def run_guard(self, *args, success=True, extra=None):
        env = self.env | (extra or {})
        env["RUNTIME"] = str(ROOT / "egress-guard/runtime.sh")
        p = subprocess.run(["bash", env["RUNTIME"], *args], env=env, capture_output=True, text=True, timeout=30)
        if success:
            self.assertEqual(p.returncode, 0, p.stderr)
        else:
            self.assertNotEqual(p.returncode, 0, p.stderr)
        return p

    def calls(self):
        p = self.root / "calls"
        return [json.loads(x) for x in p.read_text().splitlines()] if p.exists() else []

    def value(self, key):
        return dict(x.split("=", 1) for x in (self.root / "state/state").read_text().splitlines())[key]

    def test_cumulative_upgrade_and_no_reset(self):
        self.state(used=12*GIB, legacy=True)
        (self.root / "sys/eth0/statistics/tx_bytes").write_text("300\n")
        self.run_guard()
        self.assertEqual(int(self.value("used_bytes")), 12*GIB+300)
        self.run_guard()
        self.assertEqual(int(self.value("used_bytes")), 12*GIB+300)
        self.assertEqual(self.value("last_boot_id"), "boot-a")

    def test_threshold_stops_masks_every_service_even_partial_failure(self):
        self.state(used=180*GIB)
        for s in ["sing-box", "hysteria-server"]: (self.root / (s+".active")).write_text("true")
        self.run_guard(success=False, extra={"FAIL_STOP": "sing-box", "FAIL_MASK": "sing-box"})
        calls = self.calls()
        self.assertIn(["stop", "hysteria-server.service"], calls)
        self.assertIn(["show", "hysteria-server.service", "--property=LoadState", "--value"], calls)
        self.assertTrue((self.root / "early/hysteria-server.service").is_symlink())
        self.assertEqual(self.value("blocked_by_guard"), "true")
        self.assertTrue((self.root / "state/blocked").exists())
        self.assertFalse(json.loads((self.root / "run/status.json").read_text())["healthy"])

    def test_startup_gate_never_stops_activating_units(self):
        self.state(used=180*GIB)
        self.run_guard("--permit", "hysteria-server", success=False)
        self.assertEqual(self.calls(), [])
        self.run_guard("--permit", "sing-box", success=False)

    def test_month_keeps_global_block_and_counts_boundary_delta(self):
        self.state(used=179*GIB, blocked=True)
        (self.root / "state/blocked").write_text("limit")
        self.env["TEST_MONTH"] = "2026-11"
        (self.root / "sys/eth0/statistics/tx_bytes").write_text("150\n")
        self.run_guard()
        self.assertEqual(self.value("state_month"), "2026-11")
        self.assertEqual(int(self.value("used_bytes")), 50)
        self.assertEqual(self.value("blocked_by_guard"), "true")
        self.run_guard("--permit", "hysteria-server", success=False)

    def test_boot_identity_not_counter_magnitude(self):
        self.state(used=20*GIB, tx=100)
        (self.root / "boot").write_text("boot-b")
        (self.root / "sys/eth0/statistics/tx_bytes").write_text("500\n")
        self.run_guard()
        self.assertEqual(int(self.value("used_bytes")), 20*GIB+500)

    def test_same_boot_counter_decrease_fails_closed(self):
        (self.root / "sys/eth0/statistics/tx_bytes").write_text("50\n")
        self.run_guard(success=False)
        self.assertTrue((self.root / "state/blocked").exists())

    def test_missing_state_and_malformed_state_never_baseline_zero(self):
        for data in [None, "bad=value\n", "state_month=2026-10\nlast_tx=99\nused_bytes=1\n"]:
            path = self.root / "state/state"
            if data is None: path.unlink(missing_ok=True)
            else: path.write_text(data)
            self.run_guard(success=False)
            self.assertTrue((self.root / "state/blocked").exists())

    def test_resume_releases_lock_before_nested_legacy_start_gate(self):
        self.config.write_text('LIMIT_GIB=180\nWARN_GIB=150\nSERVICE_NAMES="sing-box"\nINTERFACE=eth0\nBILLING_TIMEZONE=America/Los_Angeles\nMASK_ON_LIMIT=true\nSTART_NEXT_MONTH=false\n')
        self.state(used=20*GIB, blocked=True)
        (self.root / "state/blocked").write_text("prior limit")
        self.run_guard("--resume", extra={"NESTED_GATE": "yes"})
        self.assertEqual(self.value("blocked_by_guard"), "false")
        self.assertFalse((self.root / "state/blocked").exists())
        self.assertTrue((self.root / "sing-box.active").exists())

    def test_early_generator_masks_on_reboot_including_etc_units(self):
        self.state(blocked=True)
        p = subprocess.run(["bash", str(ROOT / "egress-guard/generator.sh"), str(self.root / "normal"), str(self.root / "early"), str(self.root / "late")], env=self.env, timeout=5)
        self.assertEqual(p.returncode, 0)
        for s in ["sing-box", "hysteria-server"]:
            self.assertEqual(os.readlink(self.root / "early" / (s+".service")), "/dev/null")

    def test_legacy_service_name_and_unsafe_configuration(self):
        self.config.write_text("LIMIT_GIB=180\nWARN_GIB=150\nSERVICE_NAME=sing-box\n")
        self.script("ip", "#!/bin/sh\necho 'default via 192.0.2.1 dev eth0'\n")
        self.run_guard()
        self.assertEqual(json.loads((self.root / "run/status.json").read_text())["services"], ["sing-box"])
        self.config.write_text('SERVICE_NAMES="sing-box; echo secret"\n')
        self.run_guard(success=False)
        self.assertEqual(json.loads((self.root / "run/status.json").read_text())["services"], ["sing-box", "hysteria-server"])
    def test_hy2_global_resume_requires_no_start_then_separate_review(self):
        self.state(used=20*GIB, blocked=True)
        (self.root / "state/blocked").write_text("prior limit")
        self.run_guard("--resume", success=False)
        self.assertTrue((self.root / "state/blocked").exists())
        self.run_guard("--resume", "--no-start")
        self.assertEqual(self.value("blocked_by_guard"), "false")
        self.assertFalse((self.root / "state/blocked").exists())
        self.assertFalse(any(a[0]=="start" for a in self.calls()))
        for s in ["sing-box", "hysteria-server"]:
            self.run_guard("--permit", s)

    def test_no_start_refuses_live_core_and_never_erases_operator_mask(self):
        self.state(used=20*GIB, blocked=True)
        (self.root / "state/blocked").write_text("prior limit")
        (self.root / "hysteria-server.active").write_text("true")
        self.run_guard("--resume", "--no-start", success=False)
        self.assertTrue((self.root / "state/blocked").exists())
        (self.root / "hysteria-server.active").unlink()
        operator=self.root / "masks/hysteria-server.service"
        operator.symlink_to("/dev/null")
        self.run_guard("--resume", "--no-start")
        self.assertTrue(operator.is_symlink())

    def test_global_resume_does_not_clear_independent_health_latch(self):
        self.state(used=20*GIB, blocked=True)
        (self.root / "state/blocked").write_text("prior limit")
        health=self.root / "health-blocked"
        health.write_text("statistics gap")
        self.run_guard("--resume", "--no-start", extra={"HY2_HEALTH_BLOCKED_FILE":str(health)})
        self.assertTrue(health.exists())
        self.assertTrue((self.root / "early/hysteria-server.service").is_symlink())
        self.assertFalse((self.root / "early/sing-box.service").exists())

    def test_failed_latch_persistence_still_attempts_both_stops(self):
        (self.root / "state/blocked").mkdir()
        (self.root / "state/state").write_text("corrupt=true\n")
        self.run_guard(success=False)
        for s in ["sing-box", "hysteria-server"]:
            self.assertIn(["stop",s+".service"],self.calls())
            self.assertTrue((self.root / "early" / (s+".service")).is_symlink())

    def test_bad_or_missing_config_always_protects_both_defaults(self):
        for data in [None, 'SERVICE_NAME=sing-box\nUNKNOWN=1\n', 'SERVICE_NAME=sing-box\nSERVICE_NAME=sing-box\n', 'LIMIT_GIB=181\nWARN_GIB=150\n']:
            if data is None: self.config.unlink(missing_ok=True)
            else: self.config.write_text(data)
            self.run_guard(success=False)
            status=json.loads((self.root / "run/status.json").read_text())
            self.assertEqual(status["services"],["sing-box","hysteria-server"])
            for s in status["services"]: self.assertIn(["stop",s+".service"],self.calls())

    def test_bad_permit_flag_cannot_fool_boot_generator(self):
        (self.root / "state/state").write_text("blocked_by_guard=false\n")
        subprocess.run(["bash",str(ROOT / "egress-guard/generator.sh"),str(self.root / "normal"),str(self.root / "early"),str(self.root / "late")],check=True,env=self.env,timeout=5)
        for s in ["sing-box","hysteria-server"]:
            self.assertTrue((self.root / "early" / (s+".service")).is_symlink())

    def test_auto_resume_never_allows_hy2(self):
        self.config.write_text(self.config.read_text().replace('START_NEXT_MONTH=false','START_NEXT_MONTH=true'))
        self.run_guard(success=False)
        self.assertFalse(any(a[0]=="start" for a in self.calls()))

    def test_arguments_rejected_before_any_state_change(self):
        before=(self.root / "state/state").read_bytes()
        for args in [("--permit",),("--status","unused"),("--resume","unknown"),("--permit","sing-box","unused")]:
            self.run_guard(*args,success=False)
        self.assertEqual(before,(self.root / "state/state").read_bytes())
        self.assertEqual(self.calls(),[])


if __name__ == "__main__":
    unittest.main()
