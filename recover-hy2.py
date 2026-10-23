#!/usr/bin/env python3
"""Manual, one-attempt HY2 recovery. No cloud requests, downloads or retries of writes.

Only the standard installed layout is supported. All account charges require an
explicit human review; five-second sampling is NOT a bound on missing traffic.
"""
import argparse
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time

ADMIN = "/usr/local/sbin/hy2-admin"
CORE = "/usr/local/lib/hy2-account/hy2-account"
CONFIG = "/etc/hy2-account/config.json"
PROTOCOL = 1
GIB = 1 << 30


class Stop(RuntimeError):
    pass


def decode(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise Stop("duplicate JSON key; review the input")
            result[key] = value
        return result
    try:
        return json.loads(data, object_pairs_hook=unique)
    except (ValueError, UnicodeDecodeError):
        raise Stop("invalid JSON; inspect local state without sharing its contents") from None


def private_dir(path):
    if not path.is_absolute() or path.resolve() != path:
        raise Stop("workspace must be an absolute path without symlinks")
    st = path.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o077:
        raise Stop("workspace and its parent must be owned private directories (0700)")


def read_private(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        st = os.fstat(stream.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o077:
            raise Stop("review files must be owned private regular files (0600)")
        data = stream.read(65537)
        if len(data) > 65536:
            raise Stop("review file exceeds size limit")
        return decode(data)


def write_private(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


@contextmanager
def recovery_lock(path):
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.geteuid() or st.st_mode & 0o077:
            raise Stop("recovery lock must be an owned private regular file")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Stop("another recovery is running; do not run concurrent attempts") from None
        yield
    finally:
        os.close(fd)  # Never unlink a lock inode while another process may wait.


def snapshot(status):
    """Bind a review to its month, core generation and complete account ledger."""
    if not isinstance(status, dict) or type(status.get("recovery_protocol")) is not int or status["recovery_protocol"] != PROTOCOL:
        raise Stop("accountd recovery protocol mismatch; authorized paired update required")
    if status.get("data_error"):
        raise Stop("database/witness error; quick recovery is forbidden")
    if status.get("vm_error") != "":
        raise Stop("guard error; do not resume or bypass the global guard here")
    if any(type(status.get(k)) is not bool for k in ("core_live", "healthy", "authentication_allowed")):
        raise Stop("invalid status flags")
    month = status.get("month")
    accounting = status.get("accounting")
    users = status.get("users")
    if (not isinstance(month, str) or not re.fullmatch(r"[0-9]{4}-(0[1-9]|1[0-2])", month)
            or not isinstance(accounting, dict) or not isinstance(users, list) or not 1 <= len(users) <= 32):
        raise Stop("invalid accounting status or no existing accounts")
    for key in ("review", "hold"):
        if type(accounting.get(key)) is not bool:
            raise Stop("invalid accounting flags")
    for key in ("last_sample", "arm_until"):
        if type(accounting.get(key)) is not int or accounting[key] < 0:
            raise Stop("invalid accounting timestamps")
    if not isinstance(accounting.get("generation"), str):
        raise Stop("invalid core generation")
    accounts = {}
    for user in users:
        if not isinstance(user, dict):
            raise Stop("invalid account status")
        name = user.get("name")
        if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z0-9._-]{1,64}", name) or name in (".", "..") or name in accounts:
            raise Stop("invalid or duplicate account name")
        fields = ("quota_bytes", "used_bytes", "conservative_adjustment", "tx", "rx")
        if any(type(user.get(k)) is not int or user[k] < 0 for k in fields):
            raise Stop("invalid account usage")
        if any(type(user.get(k)) is not bool for k in ("disabled", "revoked")):
            raise Stop("invalid account flags")
        accounts[name] = {k: user[k] for k in (*fields, "disabled", "revoked")}
    return {"month": month,
            "accounting": {k: accounting[k] for k in ("generation", "last_sample", "review", "hold", "arm_until")},
            "accounts": accounts}


def reviewed_charges(review, before):
    if not isinstance(review, dict) or set(review) != {"month", "reason", "charges"} or review["month"] != before["month"]:
        raise Stop("review format/month changed; prepare a separately reviewed attempt")
    reason, charges = review["reason"], review["charges"]
    if not isinstance(reason, str) or not 8 <= len(reason.strip().encode("utf-8")) <= 200:
        raise Stop("write an 8..200-byte reason documenting the gap and charge evidence")
    if not isinstance(charges, dict) or set(charges) != set(before["accounts"]):
        raise Stop("charges must explicitly cover every account, including disabled/revoked")
    if any(type(n) is not int or not 0 <= n <= 180 * GIB for n in charges.values()):
        raise Stop("replace every null with a reviewed nonnegative byte integer; no automatic 0/1 GiB")
    return charges, reason.strip()


class Recovery:
    def __init__(self, config, *, run=subprocess.run, clock=time.monotonic, sleep=time.sleep, output=print):
        if (not isinstance(config, dict) or any(not isinstance(config.get(k), str) or not config[k]
                for k in ("sni", "certificate", "health_blocked_file"))):
            raise Stop("invalid installed recovery configuration")
        self.run, self.clock, self.sleep, self.output = run, clock, sleep, output
        self.sni = config["sni"]
        self.cert = Path(config["certificate"])
        self.blocked = Path(config["health_blocked_file"])
        if not self.cert.is_absolute() or not self.blocked.is_absolute():
            raise Stop("invalid installed recovery configuration")
        self.key = self.cert.with_name("private.key")

    def command(self, args, deadline, limit=2):
        remaining = deadline - self.clock()
        if remaining <= 0:
            raise Stop("phase deadline exceeded")
        try:
            result = self.run(args, capture_output=True, timeout=min(limit, remaining))
        except subprocess.TimeoutExpired:
            result = None
        except OSError:
            raise Stop("required local command unavailable") from None
        if self.clock() >= deadline:
            raise Stop("phase deadline exceeded; a timed-out write may already have committed")
        return result

    def checked(self, args, deadline, label, limit=2):
        result = self.command(args, deadline, limit)
        if result is None or result.returncode != 0:
            # Never echo subprocess output: it can contain private state/errors.
            raise Stop(label + " failed/timed out; inspect status and local logs")
        return result.stdout

    def status(self, deadline, retry=False):
        result = self.command([ADMIN, "status"], deadline)
        if result is None or result.returncode != 0:
            if retry:
                return None
            raise Stop("accountd status unavailable")
        status = decode(result.stdout)
        snapshot(status)  # Also rejects witness/guard errors during polling.
        return status

    def timers(self, deadline):
        # is-active with multiple units succeeds if ANY is active. Require
        # each protection timer separately; do not start or enable new timers.
        for unit in ("gcp-egress-guard.timer", "hy2-health.timer"):
            self.checked(["systemctl", "is-active", "--quiet", unit], deadline, unit)

    def preflight(self, deadline):
        version = self.checked([CORE, "version"], deadline, "account CLI version")
        if f"recovery-protocol={PROTOCOL}".encode() not in version.split():
            raise Stop("account CLI recovery protocol mismatch; do not use the old VM binary")
        status = self.status(deadline)
        if status["core_live"] or status["authentication_allowed"]:
            raise Stop("HY2 must already be stopped, not active or authenticating")
        active = self.checked(["systemctl", "show", "hysteria-server.service", "--property=ActiveState", "--value"], deadline, "stopped-core check")
        if active.strip() not in (b"inactive", b"failed"):
            raise Stop("HY2 is not confirmed inactive/failed")
        if self.checked(["ss", "-H", "-lun", "sport = :443"], deadline, "UDP port check").strip():
            raise Stop("UDP 443 is occupied; do not start or restore another HY2 entry")
        self.timers(deadline)
        if status.get("certificate_error") != "":
            raise Stop("certificate error; keep HY2 stopped")
        self.checked([CORE, "validate-cert", str(self.cert), str(self.key), self.sni], deadline, "certificate validation")
        return status

    def prepare(self, directory):
        private_dir(directory.parent)
        status = self.preflight(self.clock() + 20)
        before = snapshot(status)
        directory.mkdir(mode=0o700)  # Exclusive; never overwrite a prior review.
        write_private(directory / "before.json", before)
        write_private(directory / "review.json", {"month": before["month"], "reason": "", "charges": {name: None for name in before["accounts"]}})
        sync_dir(directory)
        sync_dir(directory.parent)
        self.output(f"REVIEW_REQUIRED {directory / 'review.json'}; every account starts at null, including revoked; no ledger change")

    def wait(self, seconds, probe, label):
        deadline = self.clock() + seconds
        while self.clock() < deadline:
            ready = probe(deadline)
            if self.clock() >= deadline:
                break
            if ready:
                return
            remaining = deadline - self.clock()
            if remaining > 0:
                self.sleep(min(1, remaining))
        raise Stop(label + " deadline exceeded")

    def execute(self, directory, accept):
        if not accept:
            raise Stop("explicit --accept-reviewed-charges is required")
        private_dir(directory.parent)
        private_dir(directory)
        attempt = directory / "attempt"
        # This lock covers distinct workspaces too; a second recovery must
        # re-read state AFTER the first one finishes, not reuse its old preflight.
        with recovery_lock(str(self.blocked) + ".recovery.lock"):
            if os.path.lexists(attempt):
                raise Stop("attempt already recorded; NEVER reuse/delete it or replay its charges; reconcile status/audit first")
            before = read_private(directory / "before.json")
            review = read_private(directory / "review.json")
            current = snapshot(self.preflight(self.clock() + 20))
            if before != current:
                raise Stop("month/accounting/accounts changed since review; reconcile before making a new plan")
            charges, reason = reviewed_charges(review, before)
            for name, n in charges.items():
                self.output(f"REVIEWED {name} +{n} bytes ({n / GIB:.6f} GiB)")
            # Record intent DURABLY BEFORE sending arm. Even a failed command,
            # killed SSH session, or post-commit witness failure cannot make
            # this workspace eligible for another automatic attempt.
            attempt.mkdir(mode=0o700)
            write_private(attempt / "charges.json", charges)
            write_private(attempt / "review.json", review)
            sync_dir(attempt)
            sync_dir(directory)
            try:
                self.checked([ADMIN, "system", "arm", "--charges-file", str(attempt / "charges.json"),
                              "--reason", reason, "--accept-gap", "--expected-month", before["month"]],
                             self.clock() + 30, "arm (commit may already have succeeded)", limit=30)
                write_private(attempt / "arm-returned-ok.json", {"arm_returned_ok": True})
                sync_dir(attempt)

                def gate(deadline):
                    result = self.command([CORE, "health", "gate"], deadline)
                    return result is not None and result.returncode == 0
                self.wait(10, gate, "startup gate")
                self.checked(["systemctl", "start", "hysteria-server.service"], self.clock() + 20, "core start", limit=20)

                def baseline(deadline):
                    s = self.status(deadline, retry=True)
                    if s is None:
                        return False
                    a = s["accounting"]
                    if s["month"] != before["month"]:
                        raise Stop("billing month changed during recovery; review before activation")
                    return (s["core_live"] and s["healthy"] and not s["authentication_allowed"]
                            and not a["review"] and a["hold"] and a["arm_until"] == 0
                            and a["generation"] != before["accounting"]["generation"]
                            and s.get("stats_error") == "" and s.get("certificate_error") == "")
                self.wait(15, baseline, "fresh baseline")
                self.checked([ADMIN, "system", "activate"], self.clock() + 17, "activate", limit=17)
                deadline = self.clock() + 10
                final = self.status(deadline)
                self.timers(deadline)
                if (final["month"] != before["month"]
                        or not all(final[k] for k in ("authentication_allowed", "healthy", "core_live"))
                        or final.get("stats_error") != "" or final.get("certificate_error") != ""):
                    raise Stop("recovery not confirmed effective")
                write_private(attempt / "recovered.json", {"recovered": True, "month": final["month"]})
                sync_dir(attempt)
            except (Stop, OSError) as exc:
                raise Stop(f"{exc}; attempt retained at {attempt}; inspect status/audit, never replay these charges") from None
        self.output("RECOVERED auth=true healthy=true core_live=true; verify one small client request")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("prepare").add_argument("directory", type=Path)
    execute = sub.add_parser("execute")
    execute.add_argument("directory", type=Path)
    execute.add_argument("--accept-reviewed-charges", action="store_true")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise Stop("VM root-only manual recovery; no operation performed")
    with open(CONFIG, "rb") as stream:
        data = stream.read(16385)
    if len(data) > 16384:
        raise Stop("installed config exceeds size limit")
    recovery = Recovery(decode(data))
    if args.command == "prepare":
        recovery.prepare(args.directory)
    else:
        recovery.execute(args.directory, args.accept_reviewed_charges)


if __name__ == "__main__":
    try:
        main()
    except (Stop, OSError, KeyboardInterrupt) as error:
        print(f"STOP: {error}; preserve all review/attempt files and inspect status before retrying", file=sys.stderr)
        sys.exit(1)
