#!/usr/bin/env python3
"""sync-hysteria-cert.sh regressions with throwaway certificates.

Runs the real script as namespace-root (`unshare -r`), so no host privilege,
no /etc or /root paths, and no systemd are involved. `install -o/-g` is
replaced by a wrapper that drops ownership flags (the hysteria group does not
exist on a build host); modes are still applied by the real `install`.
Copy, validation and pre-commit failures leave `current` unchanged; cleanup
removes only unreferenced staging paths. If rename committed before an error,
the validated current version must remain readable.

Requires Go 1.26+ and cached dependencies: setUpClass builds the offline
`validate-cert` binary locally once, with downloads disabled.
"""
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "sync-hysteria-cert.sh"
DOMAIN = "rr.example.com"


def unshare_available():
    if not shutil.which("unshare"):
        return False
    r = subprocess.run(["unshare", "-r", "true"], capture_output=True, timeout=5)
    return r.returncode == 0


@unittest.skipUnless(unshare_available(), "unprivileged user namespaces unavailable")
class SyncCertTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # The publication hook uses the installed account binary's Go verifier.
        # Build only locally, once, with downloads/toolchain fallback disabled.
        tmp = tempfile.TemporaryDirectory(prefix="hy2-cert-validator-")
        cls.addClassCleanup(tmp.cleanup)
        cls.account_binary = Path(tmp.name) / "hy2-account"
        env = os.environ | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "0"}
        subprocess.run(["go", "build", "-trimpath", "-o", str(cls.account_binary), "./cmd/hy2-account"],
                       cwd=ROOT / "hy2-account", env=env, check=True, capture_output=True, timeout=180)

    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="hy2-cert-test-")
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / "bin").mkdir()
        wrapper = self.root / "bin/install"
        wrapper.write_text("""#!/usr/bin/env python3
import os, shutil, subprocess, sys
args, skip = [], False
for a in sys.argv[1:]:
    if skip:
        skip = False
        continue
    if a in ("-o", "-g"):
        skip = True
        continue
    args.append(a)
source = args[-2] if len(args) >= 2 else ""
def replace_source():
    shutil.copyfile(os.environ["REPLACEMENT_CERT"], os.environ["CERT_PATH"])
    shutil.copyfile(os.environ["REPLACEMENT_KEY"], os.environ["KEY_PATH"])
    with open(os.environ["REPLACEMENT_MARKER"], "w") as f:
        f.write("replaced")
if os.environ.get("REPLACE_AT") == "before-cert" and source == os.environ["CERT_PATH"]:
    replace_source()
if source == os.environ.get("FAIL_COPY_SOURCE"):
    sys.exit(77)
if os.environ.get("REPLACE_AT") in ("after-cert", "after-key") and source == os.environ[
        "CERT_PATH" if os.environ["REPLACE_AT"] == "after-cert" else "KEY_PATH"]:
    subprocess.run(["/usr/bin/install", *args], check=True)
    replace_source()
    sys.exit(0)
os.execv("/usr/bin/install", ["/usr/bin/install", *args])
""")
        wrapper.chmod(0o755)
        self.config = self.root / "config.json"
        self.config.write_text(json.dumps({"sni": DOMAIN}))
        self.certs = self.root / "published"

    def cert(self, name, san=f"DNS:{DOMAIN}", days=30, cn="test", start_days=-1):
        # openssl ca permits explicit notBefore/notAfter; req -x509 cannot
        # generate the future-dated counterexample on OpenSSL 3.0.
        fixture = self.root / name
        fixture.mkdir()
        cert, key, csr = fixture / "cert.pem", fixture / "key.pem", fixture / "cert.csr"
        (fixture / "index").touch()
        (fixture / "serial").write_text("1000\n")
        config = fixture / "openssl.cnf"
        config.write_text(f"""[req]
distinguished_name = dn
[dn]
[ca]
default_ca = fixture
[fixture]
database = {fixture / 'index'}
new_certs_dir = {fixture}
serial = {fixture / 'serial'}
default_md = sha256
policy = name_policy
[name_policy]
commonName = supplied
[cert_extensions]
""" + (f"subjectAltName = {san}\n" if san is not None else ""))
        subprocess.run(["openssl", "req", "-new", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                        "-nodes", "-keyout", str(key), "-out", str(csr), "-subj", f"/CN={cn}", "-config", str(config)],
                       check=True, capture_output=True, timeout=10)
        now = datetime.now(timezone.utc)
        start = (now + timedelta(days=start_days)).strftime("%Y%m%d%H%M%SZ")
        end = (now + timedelta(days=days)).strftime("%Y%m%d%H%M%SZ")
        subprocess.run(["openssl", "ca", "-batch", "-selfsign", "-notext", "-config", str(config),
                        "-keyfile", str(key), "-in", str(csr), "-out", str(cert),
                        "-startdate", start, "-enddate", end, "-extensions", "cert_extensions"],
                       check=True, capture_output=True, timeout=10)
        return cert, key

    def sync(self, cert, key, extra_env=None):
        env = os.environ | {
            "PATH": f"{self.root / 'bin'}:{os.environ['PATH']}",
            "HY2_ACCOUNT_CONFIG": str(self.config), "CERT_PATH": str(cert), "KEY_PATH": str(key),
            "CERT_ROOT": str(self.certs), "SYNC_LOCK_PATH": str(self.root / "sync.lock"),
            "HY2_ACCOUNT_BINARY": str(self.account_binary),
        } | (extra_env or {})
        return subprocess.run(["unshare", "-r", "bash", str(SCRIPT)], env=env, capture_output=True, text=True, timeout=30)

    def assert_rejected(self, result, message):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(message, result.stderr)
        self.assertFalse(os.path.lexists(self.certs / "current"), "invalid certificate was published")
        if self.certs.exists():
            self.assertEqual(list(self.certs.iterdir()), [], "failed staging copy was left behind")

    def publish_baseline(self):
        self.baseline_cert, self.baseline_key = self.cert("baseline")
        result = self.sync(self.baseline_cert, self.baseline_key)
        self.assertEqual(result.returncode, 0, result.stderr)
        return os.readlink(self.certs / "current")

    def assert_preserved(self, before):
        self.assertEqual(os.readlink(self.certs / "current"), before)
        current = (self.certs / "current").resolve()
        self.assertEqual((current / "fullchain.cer").read_bytes(), self.baseline_cert.read_bytes())
        self.assertEqual((current / "private.key").read_bytes(), self.baseline_key.read_bytes())
        self.assertEqual({p.name for p in self.certs.iterdir()}, {before, "current"},
                         "failed publication left a version or temporary symlink behind")

    def replacement_env(self, cert, key, when="before-cert"):
        marker = self.root / "replaced"
        marker.unlink(missing_ok=True)
        return {"REPLACE_AT": when, "REPLACEMENT_CERT": str(cert), "REPLACEMENT_KEY": str(key),
                "REPLACEMENT_MARKER": str(marker)}

    def test_matching_and_wildcard_certificates_publish_private_copy(self):
        for name, san in [("exact", f"DNS:{DOMAIN}"), ("wildcard", "DNS:*.example.com")]:
            cert, key = self.cert(name, san)
            source_modes = {p: p.stat().st_mode & 0o777 for p in [cert, key]}
            result = self.sync(cert, key)
            self.assertEqual(result.returncode, 0, result.stderr)
            current = (self.certs / "current").resolve()
            self.assertEqual((current / "fullchain.cer").read_bytes(), cert.read_bytes())
            self.assertEqual((current / "private.key").read_bytes(), key.read_bytes())
            self.assertEqual({p: p.stat().st_mode & 0o777 for p in [cert, key]}, source_modes)
            for f in ["fullchain.cer", "private.key"]:
                self.assertEqual((current / f).stat().st_mode & 0o777, 0o640)

    def test_wrong_san_is_not_published(self):
        cert, key = self.cert("wrong", "DNS:other.example.net")
        self.assert_rejected(self.sync(cert, key), "does not cover")

    def test_wrong_san_does_not_replace_current_version(self):
        before = self.publish_baseline()
        bad, bad_key = self.cert("bad", "DNS:other.example.net")
        self.assertNotEqual(self.sync(bad, bad_key).returncode, 0)
        self.assert_preserved(before)

    def test_expiring_certificate_is_not_published(self):
        cert, key = self.cert("expiring", days=1)
        self.assert_rejected(self.sync(cert, key), "expires within 24h")

    def test_key_mismatch_is_not_published(self):
        cert, _ = self.cert("one")
        _, other_key = self.cert("two")
        self.assert_rejected(self.sync(cert, other_key), "mismatch")

    def test_matching_cn_without_dns_san_does_not_replace_current(self):
        before = self.publish_baseline()
        for name, san in [("cn-only", None), ("ip-only", "IP:203.0.113.10")]:
            with self.subTest(name=name):
                cert, key = self.cert(name, san=san, cn=DOMAIN)
                result = self.sync(cert, key)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn("does not cover", result.stderr)
                self.assert_preserved(before)

    def test_future_certificate_does_not_replace_current(self):
        before = self.publish_baseline()
        cert, key = self.cert("future", start_days=1)
        result = self.sync(cert, key)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("not valid yet", result.stderr)
        self.assert_preserved(before)

    def test_wildcard_matching_uses_go_hostname_rules(self):
        before = self.publish_baseline()
        for name, san, domain in [("partial", "DNS:r*.example.com", DOMAIN),
                                  ("apex", "DNS:*.example.com", "example.com"),
                                  ("nested", "DNS:*.example.com", "deep.rr.example.com")]:
            with self.subTest(name=name):
                self.config.write_text(json.dumps({"sni": domain}))
                cert, key = self.cert(name, san=san)
                result = self.sync(cert, key)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn("does not cover", result.stderr)
                self.assert_preserved(before)

    def test_replaced_source_cannot_bypass_staged_validation(self):
        before = self.publish_baseline()
        for name, options, message in [
            ("wrong-san", {"san": "DNS:other.example.net"}, "does not cover"),
            ("cn-only", {"san": None, "cn": DOMAIN}, "does not cover"),
            ("future", {"start_days": 1}, "not valid yet"),
            ("expiring", {"days": 1}, "expires within 24h"),
            ("mismatch", {}, "mismatch"),
        ]:
            with self.subTest(name=name):
                source, source_key = self.cert(f"source-{name}")
                replacement, replacement_key = self.cert(f"replacement-{name}", **options)
                if name == "mismatch":
                    _, replacement_key = self.cert("different-key")
                result = self.sync(source, source_key, self.replacement_env(replacement, replacement_key))
                self.assertTrue((self.root / "replaced").exists(), "source replacement was not injected")
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn(message, result.stderr)
                self.assert_preserved(before)

    def test_source_change_between_copies_cannot_publish_mixed_pair(self):
        before = self.publish_baseline()
        source, source_key = self.cert("source")
        replacement, replacement_key = self.cert("replacement")
        result = self.sync(source, source_key, self.replacement_env(replacement, replacement_key, "after-cert"))
        self.assertTrue((self.root / "replaced").exists(), "source replacement was not injected")
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("mismatch", result.stderr)
        self.assert_preserved(before)

    def test_source_change_after_copy_does_not_affect_valid_staged_pair(self):
        source, source_key = self.cert("source")
        expected_cert, expected_key = source.read_bytes(), source_key.read_bytes()
        replacement, replacement_key = self.cert("replacement", san="DNS:other.example.net")
        result = self.sync(source, source_key, self.replacement_env(replacement, replacement_key, "after-key"))
        self.assertTrue((self.root / "replaced").exists(), "source replacement was not injected")
        self.assertEqual(result.returncode, 0, result.stderr)
        current = (self.certs / "current").resolve()
        self.assertEqual((current / "fullchain.cer").read_bytes(), expected_cert)
        self.assertEqual((current / "private.key").read_bytes(), expected_key)
        self.assertEqual(source.read_bytes(), replacement.read_bytes())

    def test_malformed_pem_keeps_current_and_removes_staged_version(self):
        before = self.publish_baseline()
        for name in ["certificate", "key"]:
            with self.subTest(name=name):
                cert, key = self.cert(f"malformed-{name}")
                (cert if name == "certificate" else key).write_text("not PEM\n")
                self.assertNotEqual(self.sync(cert, key).returncode, 0)
                self.assert_preserved(before)

    def test_copy_failure_keeps_current_and_removes_partial_version(self):
        before = self.publish_baseline()
        cert, key = self.cert("copy-failure")
        result = self.sync(cert, key, {"FAIL_COPY_SOURCE": str(key)})
        self.assertNotEqual(result.returncode, 0)
        self.assert_preserved(before)

    def test_validator_failure_has_no_openssl_fallback(self):
        before = self.publish_baseline()
        cert, key = self.cert("validator-failure")
        failing = self.root / "old-account"
        failing.write_text("#!/bin/sh\necho 'unsupported validate-cert' >&2\nexit 72\n")
        failing.chmod(0o755)
        for binary in [self.root / "missing-account", failing]:
            with self.subTest(binary=binary.name):
                result = self.sync(cert, key, {"HY2_ACCOUNT_BINARY": str(binary)})
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assert_preserved(before)

    def test_rename_failure_keeps_current_and_removes_temporary_symlink(self):
        before = self.publish_baseline()
        cert, key = self.cert("rename-failure")
        failing = self.root / "bin/mv"
        failing.write_text("#!/bin/sh\nexit 74\n")
        failing.chmod(0o755)
        self.assertNotEqual(self.sync(cert, key).returncode, 0)
        self.assert_preserved(before)

    def test_post_commit_error_never_deletes_published_version(self):
        before = self.publish_baseline()
        cert, key = self.cert("commit-error")
        failing = self.root / "bin/mv"
        failing.write_text('#!/bin/sh\n/usr/bin/mv "$@" || exit $?\nexit 74\n')
        failing.chmod(0o755)
        result = self.sync(cert, key)
        self.assertNotEqual(result.returncode, 0)
        current = (self.certs / "current").resolve()
        self.assertNotEqual(current.name, before)
        self.assertEqual((current / "fullchain.cer").read_bytes(), cert.read_bytes())
        self.assertEqual((current / "private.key").read_bytes(), key.read_bytes())
        self.assertEqual({p.name for p in self.certs.iterdir()}, {before, current.name, "current"})

    def test_successful_rotation_retains_three_versions(self):
        for i in range(5):
            cert, key = self.cert(f"rotation-{i}")
            result = self.sync(cert, key)
            self.assertEqual(result.returncode, 0, result.stderr)
        versions = [p for p in self.certs.iterdir() if p.is_dir() and not p.is_symlink()]
        self.assertEqual(len(versions), 3)
        self.assertEqual({p.name for p in self.certs.iterdir()}, {p.name for p in versions} | {"current"})
        current = (self.certs / "current").resolve()
        self.assertIn(current, versions)
        self.assertEqual((current / "fullchain.cer").read_bytes(), cert.read_bytes())


if __name__ == "__main__":
    unittest.main()
