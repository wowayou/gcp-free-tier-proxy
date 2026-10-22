#!/usr/bin/env python3
"""Structured sing-box HY2 removal, isolated from deployment orchestration.

No secrets are printed. Refuse ambiguous references instead of rewriting other
protocol rules. A timestamped backup is made by the maintenance-window wrapper.
"""
import argparse
import copy
import json
import os
from pathlib import Path
import stat
import tempfile


def remove_hy2(data):
    original = copy.deepcopy(data)
    # Work on a copy so a rejected or successful call never mutates the
    # caller's config object (the CLI passes the parsed document once).
    data = copy.deepcopy(data)
    hy2 = [v for v in data.get("inbounds", []) if v.get("type") == "hysteria2"]
    if not hy2:
        raise ValueError("no legacy Hysteria 2 inbound; refuse ambiguous migration")
    tags = {v.get("tag") for v in hy2}
    if None in tags or "" in tags or len(tags) != len(hy2):
        raise ValueError("HY2 tags must be unique and explicit")
    data["inbounds"] = [v for v in data["inbounds"] if v.get("type") != "hysteria2"]
    rules = data.get("route", {}).get("rules", [])
    kept = []
    for rule in rules:
        inbound = rule.get("inbound")
        if isinstance(inbound, list) and set(inbound) <= tags and inbound:
            # Only remove known legacy safety rules, not arbitrary user routing.
            keys = set(rule)
            private = rule.get("ip_is_private") is True and keys == {"inbound", "ip_is_private", "action"}
            metadata = rule.get("domain") == ["metadata.google.internal"] and keys == {"inbound", "domain", "action"}
            if rule.get("action") == "reject" and (private or metadata):
                continue
        kept.append(rule)
    if "route" in data and "rules" in data["route"]:
        data["route"]["rules"] = kept

    def references(value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "inbound" and (isinstance(item, str) and item in tags or isinstance(item, list) and any(x in tags for x in item)):
                    return True
                if references(item):
                    return True
        elif isinstance(value, list):
            return any(references(x) for x in value)
        return False

    if references(data):
        raise ValueError("legacy HY2 tag still referenced by non-owned routing; review manually")
    preserved = [v for v in original["inbounds"] if v.get("type") != "hysteria2"]
    if data["inbounds"] != preserved:
        raise ValueError("non-HY2 inbounds changed")
    return data


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("config")
    args = parser.parse_args()
    path = Path(args.config).resolve(strict=True)
    metadata = path.stat()
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    try:
        result = remove_hy2(data)
    except ValueError as exc:
        raise SystemExit(str(exc))
    fd, tmp = tempfile.mkstemp(prefix=".hy2-migrate-", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(result, f, indent=2, ensure_ascii=False)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, stat.S_IMODE(metadata.st_mode))
        os.chown(tmp, metadata.st_uid, metadata.st_gid)
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


if __name__ == "__main__":
    main()
