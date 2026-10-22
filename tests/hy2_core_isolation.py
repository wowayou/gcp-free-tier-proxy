#!/usr/bin/env python3
"""Pinned official-core feasibility gate. Loopback only; no VM/systemd changes.

HY2_BIN=/path/to/verified/hysteria python3 tests/hy2_core_isolation.py
The full accountd integration test additionally exercises the durable controller.
"""
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

VERSION = "v2.13.0"
COMMIT = "ca8fbd874fd6ca82413948de6dc5924405e103e1"
SHA256_AMD64 = "907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691"
SHA256_ARM64 = "a68a61a84452ca250ce0368202521965ca9cc9d801a404f1dc9008ac6cf677a7"


def port(udp=False):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM if udp else socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_for(fn, seconds=8):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            value = fn()
            if value:
                return value
        except (OSError, urllib.error.URLError, ValueError):
            pass
        time.sleep(0.05)
    raise AssertionError("bounded wait expired")


def recv_exact(s, n):
    data = b""
    while len(data) < n:
        chunk = s.recv(n - len(data))
        if not chunk:
            raise OSError("original stream closed")
        data += chunk
    return data


def socks_connect(addr, target):
    s = socket.create_connection(addr, timeout=2)
    s.sendall(b"\x05\x01\x00")
    assert recv_exact(s, 2) == b"\x05\x00"
    s.sendall(b"\x05\x01\x00\x01" + socket.inet_aton(target[0]) + target[1].to_bytes(2, "big"))
    header = recv_exact(s, 4)
    if header[1] != 0:
        s.close()
        raise OSError("HY2 forwarding rejected")
    assert header[3] in (1, 4)
    recv_exact(s, (4 if header[3] == 1 else 16) + 2)
    return s


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            data = self.request.recv(8192)
            if not data:
                return
            self.request.sendall(data)


class EchoServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


class AuthState:
    def __init__(self):
        self.lock = threading.Lock()
        self.sessions = {}
        self.disabled = set()
        self.passwords = {"owner": secrets.token_hex(32), "friend": secrets.token_hex(32)}
        self.hold = False
        self.signed = threading.Event()
        self.release = threading.Event()

    def authenticate(self, password):
        with self.lock:
            account = next((a for a, p in self.passwords.items() if secrets.compare_digest(p, password)), None)
            if not account or account in self.disabled:
                return {"ok": False, "id": ""}
            sid = secrets.token_hex(24)
            self.sessions[sid] = account
            hold = self.hold and account == "friend"
        if hold:
            self.signed.set()
            assert self.release.wait(5)
        return {"ok": True, "id": sid}


def main():
    binary = Path(os.environ["HY2_BIN"]).resolve()
    version = subprocess.check_output([str(binary), "version"], text=True)
    assert VERSION in version and COMMIT in version and "BuildType:\trelease" in version
    if "Architecture:\tamd64" in version:
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == SHA256_AMD64
    elif "Architecture:\tarm64" in version:
        assert hashlib.sha256(binary.read_bytes()).hexdigest() == SHA256_ARM64
    else:
        raise AssertionError("unexpected core architecture")
    state = AuthState()

    class Auth(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            n = int(self.headers.get("Content-Length", "0"))
            assert 0 < n <= 4096
            reply = state.authenticate(json.loads(self.rfile.read(n))["auth"])
            body = json.dumps(reply).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_):
            pass

    auth = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Auth)
    echo = EchoServer(("127.0.0.1", 0), Echo)
    for srv in (auth, echo):
        threading.Thread(target=srv.serve_forever, daemon=True).start()
    processes, sockets = [], []
    secret = secrets.token_hex(32)
    stats_port, server_port = port(), port(udp=True)

    def api(path, data=None):
        req = urllib.request.Request(f"http://127.0.0.1:{stats_port}{path}",
                                     data=None if data is None else json.dumps(data).encode(),
                                     headers={"Authorization": secret, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=2) as r:
            body = r.read(4 * 1024 * 1024)
            return json.loads(body) if body else None

    with tempfile.TemporaryDirectory(prefix="hy2-isolation-") as tmp:
        tmp = Path(tmp)
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                        "-nodes", "-keyout", str(tmp / "key.pem"), "-out", str(tmp / "cert.pem"),
                        "-days", "1", "-subj", "/CN=rr.example.com", "-addext", "subjectAltName=DNS:rr.example.com"],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        server = {"listen": f"127.0.0.1:{server_port}",
                  "tls": {"cert": str(tmp / "cert.pem"), "key": str(tmp / "key.pem"), "sniGuard": "strict"},
                  "auth": {"type": "http", "http": {"url": f"http://127.0.0.1:{auth.server_port}/auth"}},
                  "trafficStats": {"listen": f"127.0.0.1:{stats_port}", "secret": secret},
                  "disableUDP": True, "ignoreClientBandwidth": True,
                  "congestion": {"type": "bbr", "bbrProfile": "standard"},
                  "quic": {"maxIdleTimeout": "60s"}}
        (tmp / "server.json").write_text(json.dumps(server))
        log = (tmp / "core.log").open("w")
        try:
            core = subprocess.Popen([str(binary), "--disable-update-check", "--log-level", "warn", "server", "-c", str(tmp / "server.json")],
                                    stdout=log, stderr=log)
            processes.append(core)
            wait_for(lambda: api("/online") == {})
            core_start = Path(f"/proc/{core.pid}/stat").read_text().split()[21]

            def client(account):
                local_port = port()
                cfg = {"server": f"127.0.0.1:{server_port}", "auth": state.passwords[account],
                       "tls": {"sni": "rr.example.com", "ca": str(tmp / "cert.pem")},
                       "socks5": {"listen": f"127.0.0.1:{local_port}"}}
                path = tmp / f"client-{len(processes)}.json"
                path.write_text(json.dumps(cfg))
                p = subprocess.Popen([str(binary), "--disable-update-check", "--log-level", "warn", "client", "-c", str(path)],
                                     stdout=log, stderr=log)
                processes.append(p)
                return p, local_port

            def connect(p):
                return wait_for(lambda: socks_connect(("127.0.0.1", p), echo.server_address))

            owner_client, owner_port = client("owner")
            owner = connect(owner_port)
            sockets.append(owner)
            owner.sendall(b"original-owner-stream")
            assert recv_exact(owner, 21) == b"original-owner-stream"
            friends = []
            for _ in range(3):
                _, p = client("friend")
                s = connect(p)
                friends.append(s)
                sockets.append(s)
            for s in friends[:2]:
                s.sendall(b"friend-data")
                assert recv_exact(s, 11) == b"friend-data"
            owner_sid = next(s for s, a in state.sessions.items() if a == "owner")
            streams = wait_for(lambda: api("/dump/streams")["streams"])
            owner_conn = next(s["connection"] for s in streams if s["auth"] == owner_sid)

            # Signed session not yet returned to the core must also receive a kick.
            state.hold = True
            _, race_port = client("friend")
            assert state.signed.wait(5)
            with state.lock:
                state.disabled.add("friend")
                friend_ids = [s for s, a in state.sessions.items() if a == "friend"]
            assert len(friend_ids) == 4
            api("/kick", friend_ids)
            state.release.set()
            race = connect(race_port)
            sockets.append(race)
            assert any(s in api("/online") for s in friend_ids), "idle must not be reported offline immediately"

            for s in friends + [race]:
                s.sendall(b"trigger-kick")
                try:
                    recv_exact(s, 12)
                except OSError:
                    pass
                else:
                    raise AssertionError("target original stream survived kick")
            wait_for(lambda: all(s not in api("/online") for s in friend_ids))

            # No new SOCKS/TCP stream and no reconnect loop is allowed to mask an interruption.
            for i in range(30):
                payload = (f"same-stream-{i:04d}:".encode() + b"x" * 4096)
                owner.sendall(payload)
                assert recv_exact(owner, len(payload)) == payload
                time.sleep(0.02)
            after = api("/dump/streams")["streams"]
            assert next(s["connection"] for s in after if s["auth"] == owner_sid) == owner_conn
            assert [s for s, a in state.sessions.items() if a == "owner"] == [owner_sid]
            assert core.poll() is None and owner_client.poll() is None
            assert Path(f"/proc/{core.pid}/stat").read_text().split()[21] == core_start
            assert not state.authenticate(state.passwords["friend"])["ok"]
            traffic = api("/traffic")
            assert traffic[owner_sid]["tx"] == traffic[owner_sid]["rx"] > 120000
            print("PASS: official v2.13.0; four targeted sessions (multi-device/idle/auth race); owner original TCP + QUIC connection unchanged; cumulative tx/rx verified; loopback <1 MiB")
        finally:
            state.release.set()
            for s in sockets:
                s.close()
            for p in reversed(processes):
                if p.poll() is None:
                    p.terminate()
                try:
                    p.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    p.kill()
                    p.wait(timeout=3)
            log.close()
            auth.shutdown()
            echo.shutdown()


if __name__ == "__main__":
    main()
