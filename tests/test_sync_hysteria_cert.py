#!/usr/bin/env python3
"""sync-hysteria-cert.sh regressions with throwaway certificates.

Runs the real script as namespace-root (`unshare -r`), so no host privilege,
no /etc or /root paths, and no systemd are involved. `install -o/-g` is
replaced by a wrapper that drops ownership flags (the hysteria group does not
exist on a build host); modes are still applied by the real `install`.
"""
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
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="hy2-cert-test-")
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / "bin").mkdir()
        wrapper = self.root / "bin/install"
        wrapper.write_text("""#!/usr/bin/env python3
import os, sys
args, skip = [], False
for a in sys.argv[1:]:
    if skip:
        skip = False
        continue
    if a in ("-o", "-g"):
        skip = True
        continue
    args.append(a)
os.execv("/usr/bin/install", ["/usr/bin/install", *args])
""")
        wrapper.chmod(0o755)
        self.config = self.root / "config.json"
        self.config.write_text(json.dumps({"sni": DOMAIN}))
        self.certs = self.root / "published"

    def cert(self, name, san=f"DNS:{DOMAIN}", days=30):
        cert, key = self.root / f"{name}.pem", self.root / f"{name}.key"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                        "-nodes", "-keyout", str(key), "-out", str(cert), "-days", str(days),
                        "-subj", "/CN=test", "-addext", f"subjectAltName={san}"],
                       check=True, capture_output=True, timeout=10)
        return cert, key

    def sync(self, cert, key):
        env = os.environ | {
            "PATH": f"{self.root / 'bin'}:{os.environ['PATH']}",
            "HY2_ACCOUNT_CONFIG": str(self.config), "CERT_PATH": str(cert), "KEY_PATH": str(key),
            "CERT_ROOT": str(self.certs), "SYNC_LOCK_PATH": str(self.root / "sync.lock"),
        }
        return subprocess.run(["unshare", "-r", "bash", str(SCRIPT)], env=env, capture_output=True, text=True, timeout=30)

    def assert_rejected(self, result, message):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(message, result.stderr)
        self.assertFalse((self.certs / "current").exists(), "invalid certificate was published")

    def test_matching_and_wildcard_certificates_publish_private_copy(self):
        for name, san in [("exact", f"DNS:{DOMAIN}"), ("wildcard", "DNS:*.example.com")]:
            cert, key = self.cert(name, san)
            result = self.sync(cert, key)
            self.assertEqual(result.returncode, 0, result.stderr)
            current = (self.certs / "current").resolve()
            self.assertEqual((current / "fullchain.cer").read_bytes(), cert.read_bytes())
            for f in ["fullchain.cer", "private.key"]:
                self.assertEqual((current / f).stat().st_mode & 0o777, 0o640)

    def test_wrong_san_is_not_published(self):
        cert, key = self.cert("wrong", "DNS:other.example.net")
        self.assert_rejected(self.sync(cert, key), "does not cover")

    def test_wrong_san_does_not_replace_current_version(self):
        good, good_key = self.cert("good")
        self.assertEqual(self.sync(good, good_key).returncode, 0)
        before = os.readlink(self.certs / "current")
        bad, bad_key = self.cert("bad", "DNS:other.example.net")
        self.assertNotEqual(self.sync(bad, bad_key).returncode, 0)
        self.assertEqual(os.readlink(self.certs / "current"), before)

    def test_expiring_certificate_is_not_published(self):
        cert, key = self.cert("expiring", days=1)
        self.assert_rejected(self.sync(cert, key), "expires within 24h")

    def test_key_mismatch_is_not_published(self):
        cert, _ = self.cert("one")
        _, other_key = self.cert("two")
        self.assert_rejected(self.sync(cert, other_key), "mismatch")


if __name__ == "__main__":
    unittest.main()
