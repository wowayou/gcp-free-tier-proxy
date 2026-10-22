#!/usr/bin/env python3
"""Regression tests for the structured sing-box HY2 removal helper."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "remove_singbox_hy2", ROOT / "hy2-account/tools/remove_singbox_hy2.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RemoveHY2Tests(unittest.TestCase):
    def test_removes_hy2_and_only_owned_safety_rules(self):
        data = {
            "inbounds": [
                {"type": "vless", "tag": "reality", "listen_port": 47370},
                {"type": "hysteria2", "tag": "hy2-legacy", "listen_port": 443},
            ],
            "route": {
                "rules": [
                    {"inbound": ["hy2-legacy"], "ip_is_private": True, "action": "reject"},
                    {"inbound": ["hy2-legacy"], "domain": ["metadata.google.internal"], "action": "reject"},
                    {"inbound": ["reality"], "domain": ["example.com"], "action": "reject"},
                ]
            },
        }
        result = MODULE.remove_hy2(data)
        self.assertEqual([item["tag"] for item in result["inbounds"]], ["reality"])
        self.assertEqual(result["route"]["rules"], [{"inbound": ["reality"], "domain": ["example.com"], "action": "reject"}])
        # The helper must not mutate its input on a successful path.
        self.assertEqual(len(data["inbounds"]), 2)

    def test_refuses_missing_hy2(self):
        with self.assertRaises(ValueError):
            MODULE.remove_hy2({"inbounds": [{"type": "vless", "tag": "reality"}]})

    def test_refuses_ambiguous_reference_instead_of_guessing(self):
        with self.assertRaises(ValueError):
            MODULE.remove_hy2({
                "inbounds": [{"type": "hysteria2", "tag": "hy2-legacy"}],
                "route": {"rules": [{"inbound": "hy2-legacy", "action": "reject"}]},
            })
        with self.assertRaises(ValueError):
            MODULE.remove_hy2({
                "inbounds": [{"type": "hysteria2", "tag": "hy2-legacy"}],
                "route": {"rules": [{"inbound": ["hy2-legacy", "reality"], "action": "reject"}]},
            })

    def test_refuses_hy2_without_explicit_unique_tag(self):
        with self.assertRaises(ValueError):
            MODULE.remove_hy2({"inbounds": [{"type": "hysteria2"}]})
        with self.assertRaises(ValueError):
            MODULE.remove_hy2({"inbounds": [{"type": "hysteria2", "tag": ""}]})


if __name__ == "__main__":
    unittest.main()
