#!/usr/bin/env python3
"""Legacy HY2 DNS SAN regressions, with deterministic throwaway certificates.

Extract and call only the production detect_domain function: no root, host
config paths, guard changes, systemd, network, Go build or package downloads.
Python cryptography is an explicit local/VM dependency; missing it is an error,
not a skipped test or an OpenSSL text/CN fallback.
"""
from datetime import datetime
import ipaddress
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import unittest

from cryptography import x509
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.x509.oid import NameOID

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "add-hysteria2-inbound.sh"
DOMAIN = "rr.example.com"


def production_function():
    functions = re.findall(r"^detect_domain\(\) \{\n.*?^\}", SCRIPT.read_text(encoding="utf-8"), re.M | re.S)
    if len(functions) != 1:
        raise AssertionError("expected exactly one production detect_domain function")
    return functions[0]


class LegacyHY2CertificateTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="hy2-legacy-cert-")
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.function = production_function()

    def cert(self, name, sans=None, cn=DOMAIN):
        # Fixed Ed25519 key and dates: only local parser fixtures, never trusted
        # certificates or credentials for any real server.
        key = ed25519.Ed25519PrivateKey.from_private_bytes(bytes(range(32)))
        subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, cn)])
        builder = (x509.CertificateBuilder().subject_name(subject).issuer_name(subject)
                   .public_key(key.public_key()).serial_number(1)
                   .not_valid_before(datetime(2020, 1, 1)).not_valid_after(datetime(2040, 1, 1)))
        if sans is not None:
            builder = builder.add_extension(x509.SubjectAlternativeName(sans), critical=False)
        path = self.root / f"{name}.pem"
        path.write_bytes(builder.sign(key, algorithm=None).public_bytes(serialization.Encoding.PEM))
        return path

    def detect(self, cert, domain=DOMAIN, extra_env=None):
        env = os.environ | {"CERT_PATH": str(cert), "HY2_DOMAIN": domain or ""} | (extra_env or {})
        # Exercise the real call-site's assignment under set -e as well, so a
        # rejected certificate cannot reach the next configuration step.
        shell = "set -euo pipefail\n" + self.function + '\nHY2_DOMAIN="$(detect_domain)"\nprintf "%s\\n" "$HY2_DOMAIN"\n'
        return subprocess.run(["bash", "-c", shell], cwd=self.root, env=env,
                              capture_output=True, text=True, timeout=10)

    def assert_rejected(self, result, message):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(result.stdout, "", "rejected certificate emitted a usable hostname")
        self.assertIn(message, result.stderr)

    def test_baseline_partial_wildcard_counterexample_is_rejected(self):
        cert = self.cert("baseline-partial", [x509.DNSName("r*.example.com")])
        self.assert_rejected(self.detect(cert), "does not cover")

    def test_baseline_uri_counterexample_is_rejected(self):
        cert = self.cert("baseline-uri", [x509.UniformResourceIdentifier("https://example.net/DNS:rr.example.com")])
        self.assert_rejected(self.detect(cert), "no DNS SAN")

    def test_exact_multiple_sans_case_and_trailing_dot(self):
        for name, sans, domain in [
            ("exact", [x509.DNSName(DOMAIN)], DOMAIN),
            ("case", [x509.DNSName("RR.EXAMPLE.COM")], "Rr.Example.Com"),
            ("trailing-dot", [x509.DNSName(DOMAIN)], "RR.EXAMPLE.COM."),
            ("multiple", [x509.DNSName("other.example.net"), x509.DNSName(DOMAIN)], DOMAIN),
        ]:
            with self.subTest(name=name):
                result = self.detect(self.cert(name, sans), domain)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, DOMAIN + "\n")

    def test_full_wildcard_matches_exactly_one_label(self):
        cert = self.cert("wildcard", [x509.DNSName("*.example.com")])
        for domain, accepted in [(DOMAIN, True), ("RR.EXAMPLE.COM.", True),
                                 ("example.com", False), ("deep.rr.example.com", False),
                                 ("rr.example.net", False)]:
            with self.subTest(domain=domain):
                result = self.detect(cert, domain)
                if accepted:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, DOMAIN + "\n")
                else:
                    self.assert_rejected(result, "does not cover")

    def test_partial_and_multiple_wildcards_are_rejected(self):
        for i, pattern in enumerate(["r*.example.com", "*r.example.com", "r*r.example.com",
                                     "**.example.com", "*.*.com", "rr.*.com", "*.exa*ple.com", "*"]):
            with self.subTest(pattern=pattern):
                self.assert_rejected(self.detect(self.cert(f"partial-{i}", [x509.DNSName(pattern)])),
                                     "does not cover")

    def test_matching_cn_and_ip_sans_are_not_dns_sans(self):
        for name, sans in [("cn-only", None), ("empty-san", []),
                           ("ip-only", [x509.IPAddress(ipaddress.ip_address("203.0.113.10"))]),
                           ("email-only", [x509.RFC822Name("rr@example.com")])]:
            with self.subTest(name=name):
                cert = self.cert(name, sans)
                for domain in [DOMAIN, None]:
                    self.assert_rejected(self.detect(cert, domain), "no DNS SAN")

    def test_dns_san_type_cannot_be_spoofed_by_uri_text(self):
        for i, uri in enumerate(["https://example.net/DNS:rr.example.com",
                                 "https://example.net/other,DNS:rr.example.com"]):
            with self.subTest(uri=uri):
                cert = self.cert(f"uri-{i}", [x509.UniformResourceIdentifier(uri)])
                for domain in [DOMAIN, None]:
                    self.assert_rejected(self.detect(cert, domain), "no DNS SAN")

    def test_wrong_dns_san_cannot_be_masked_by_other_san_types(self):
        cert = self.cert("mixed", [x509.DNSName("other.example.net"),
                                   x509.UniformResourceIdentifier("https://example.net/DNS:rr.example.com")])
        self.assert_rejected(self.detect(cert), "does not cover")
        inferred = self.detect(cert, None)
        self.assertEqual(inferred.returncode, 0, inferred.stderr)
        self.assertEqual(inferred.stdout, "other.example.net\n")

    def test_inference_uses_first_concrete_dns_san_only(self):
        cert = self.cert("infer", [x509.UniformResourceIdentifier("https://example.net/DNS:spoof.example.com"),
                                   x509.DNSName("*.example.com"), x509.DNSName("r*.example.com"),
                                   x509.DNSName("RR.EXAMPLE.COM"), x509.DNSName("other.example.net")])
        result = self.detect(cert, None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, DOMAIN + "\n")

    def test_inference_rejects_wildcard_only_and_invalid_dns_names(self):
        for i, pattern in enumerate(["*.example.com", "r*.example.com", "*", "a..example.com",
                                     "-rr.example.com", "rr-.example.com", "203.0.113.10", ".",
                                     "a" * 64 + ".example.com"]):
            with self.subTest(pattern=pattern):
                self.assert_rejected(self.detect(self.cert(f"no-concrete-{i}", [x509.DNSName(pattern)]), None),
                                     "Cannot infer a concrete hostname")

    def test_invalid_explicit_hostnames_fail_closed(self):
        cert = self.cert("valid", [x509.DNSName(DOMAIN)])
        for domain in ["*.example.com", "r*.example.com", "rr.example.com\n", "a..example.com",
                       "-rr.example.com", "rr-.example.com", "203.0.113.10", "[::1]", ".",
                       "rr.example.com..", "rr.example.com/path", "中文.example.com",
                       "Kr.example.com", "a" * 64 + ".example.com", ("a." * 127) + "com"]:
            with self.subTest(domain=domain):
                self.assert_rejected(self.detect(cert, domain), "concrete ASCII DNS hostname")

    def test_malformed_or_missing_certificate_fails_closed(self):
        invalid = self.root / "invalid.pem"
        invalid.write_text("not PEM\n")
        for cert in [invalid, self.root / "missing.pem"]:
            with self.subTest(cert=cert.name):
                self.assert_rejected(self.detect(cert), "Cannot read certificate")

    def test_checks_leaf_not_later_chain_certificate(self):
        wrong = self.cert("wrong-leaf", [x509.DNSName("other.example.net")])
        good = self.cert("good-leaf", [x509.DNSName(DOMAIN)])
        chain = self.root / "chain.pem"
        chain.write_bytes(wrong.read_bytes() + good.read_bytes())
        self.assert_rejected(self.detect(chain), "does not cover")
        chain.write_bytes(good.read_bytes() + wrong.read_bytes())
        result = self.detect(chain)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, DOMAIN + "\n")

    def test_missing_cryptography_has_no_text_or_cn_fallback(self):
        cert = self.cert("dependency", [x509.DNSName(DOMAIN)])
        bindir = self.root / "bin"
        bindir.mkdir()
        shim = bindir / "python3"
        # -S excludes site-packages, simulating a VM lacking the explicit
        # dependency without uninstalling anything or changing the host.
        shim.write_text(f"#!/bin/sh\nexec {shlex.quote(sys.executable)} -S \"$@\"\n")
        shim.chmod(0o755)
        result = self.detect(cert, extra_env={"PATH": f"{bindir}:{os.environ['PATH']}", "PYTHONPATH": ""})
        self.assert_rejected(result, "cryptography is required")


if __name__ == "__main__":
    unittest.main()
