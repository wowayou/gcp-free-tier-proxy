package account

// Real, checksum-pinned official core integration. All data paths terminate on
// loopback fixtures. No VM, cloud API, public target or systemd action is used.
// HY2_BIN=/path/to/verified/hysteria go test -race ./internal/account -run TestPinned -v

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func pinnedCore(t *testing.T) string {
	t.Helper()
	path := os.Getenv("HY2_BIN")
	if path == "" {
		t.Skip("HY2_BIN not supplied: real-core gate NOT run")
	}
	path, err := filepath.Abs(path)
	must(t, err)
	b, err := os.ReadFile(path)
	must(t, err)
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	if sum != "907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691" && sum != "a68a61a84452ca250ce0368202521965ca9cc9d801a404f1dc9008ac6cf677a7" {
		t.Fatal("core is not the checksum-pinned official release")
	}
	version, err := exec.Command(path, "version").Output()
	must(t, err)
	if !strings.Contains(string(version), CoreVersion) || !strings.Contains(string(version), CoreCommit) || !strings.Contains(string(version), "BuildType:\trelease") {
		t.Fatal("unexpected core version/build")
	}
	return path
}

func freePort(t *testing.T, udp bool) string {
	t.Helper()
	if udp {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		must(t, err)
		defer c.Close()
		return c.LocalAddr().String()
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	defer l.Close()
	return l.Addr().String()
}

func testCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	must(t, err)
	now := time.Now()
	c := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "rr.example.com"}, DNSNames: []string{"rr.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	must(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	must(t, err)
	certPath, keyPath := filepath.Join(dir, "test-cert.pem"), filepath.Join(dir, "test-key.pem")
	must(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	must(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600))
	return certPath, keyPath
}

type coreProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func testProcess(t *testing.T, binary, mode, path string) *coreProcess {
	t.Helper()
	cmd := exec.Command(binary, "--disable-update-check", "--log-level", "error", mode, "-c", path)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	must(t, cmd.Start())
	p := &coreProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	t.Cleanup(p.stop)
	return p
}
func (p *coreProcess) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}
func processGeneration(t *testing.T, pid int, path string) string {
	t.Helper()
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	must(t, err)
	ticks, err := ProcessTicks(pid)
	must(t, err)
	id, err := randomToken(24)
	must(t, err)
	must(t, writeJSON(path, ProcessGeneration{ID: id, PID: pid, StartTicks: ticks, BootID: strings.TrimSpace(string(boot))}, 0640))
	g, err := ReadGeneration(path)
	must(t, err)
	return g
}
func waitCore(t *testing.T, fn func() bool) {
	t.Helper()
	end := time.Now().Add(9 * time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("bounded local-core wait expired")
}

func coreClient(t *testing.T, binary, server, certificate, password, dir string, index int) (*coreProcess, string) {
	t.Helper()
	addr := freePort(t, false)
	path := filepath.Join(dir, fmt.Sprintf("client-%d.json", index))
	must(t, writeJSON(path, map[string]any{"server": server, "auth": password, "tls": map[string]any{"sni": "rr.example.com", "ca": certificate}, "socks5": map[string]any{"listen": addr}}, 0600))
	return testProcess(t, binary, "client", path), addr
}
func socksDial(addr, target string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			c.Close()
		}
	}()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	header := make([]byte, 2)
	if _, err := io.ReadFull(c, header); err != nil || !bytes.Equal(header, []byte{5, 0}) {
		return nil, errors.New("SOCKS negotiation failed")
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil {
		return nil, err
	}
	r := []byte{5, 1, 0}
	ip := net.ParseIP(host)
	if v4 := ip.To4(); v4 != nil {
		r = append(append(r, 1), v4...)
	} else if ip != nil {
		r = append(append(r, 4), ip.To16()...)
	} else {
		if len(host) > 253 {
			return nil, errors.New("test hostname too long")
		}
		r = append(append(r, 3, byte(len(host))), []byte(host)...)
	}
	r = append(r, byte(p>>8), byte(p))
	if _, err := c.Write(r); err != nil {
		return nil, err
	}
	header = make([]byte, 4)
	if _, err := io.ReadFull(c, header); err != nil {
		return nil, err
	}
	if header[1] != 0 {
		return nil, errors.New("HY2 forwarding rejected")
	}
	n := 0
	switch header[3] {
	case 1:
		n = 6
	case 4:
		n = 18
	default:
		return nil, errors.New("invalid SOCKS reply")
	}
	if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
		return nil, err
	}
	ok = true
	return c, nil
}
func connectCore(t *testing.T, addr, target string) net.Conn {
	t.Helper()
	var conn net.Conn
	waitCore(t, func() bool { var err error; conn, err = socksDial(addr, target); return err == nil })
	t.Cleanup(func() { conn.Close() })
	return conn
}

func framedEcho(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					var size [4]byte
					if _, err := io.ReadFull(c, size[:]); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(size[:])
					if n == 0 || n > 8192 {
						return
					}
					b := make([]byte, n)
					if _, err := io.ReadFull(c, b); err != nil {
						return
					}
					if _, err := c.Write(bytes.Repeat(b, 2)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().String()
}
func frame(conn net.Conn, b []byte) error {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(b)))
	if _, err := conn.Write(append(header[:], b...)); err != nil {
		return err
	}
	got := make([]byte, len(b)*2)
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if !bytes.Equal(got, bytes.Repeat(b, 2)) {
		return errors.New("original TCP stream payload corrupted")
	}
	return nil
}
func streamConnection(t *testing.T, cfg Config, secret, sid string) uint32 {
	t.Helper()
	r, err := http.NewRequest("GET", cfg.StatsURL+"/dump/streams", nil)
	must(t, err)
	r.Header.Set("Authorization", secret)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(r)
	must(t, err)
	defer resp.Body.Close()
	var data struct {
		Streams []struct {
			Auth       string `json:"auth"`
			Connection uint32 `json:"connection"`
		} `json:"streams"`
	}
	// Dump contains target fields; only this test decodes it, never the daemon/page.
	must(t, json.NewDecoder(io.LimitReader(resp.Body, MaxStatsBody)).Decode(&data))
	for _, s := range data.Streams {
		if s.Auth == sid {
			return s.Connection
		}
	}
	t.Fatal("original core stream no longer exists")
	return 0
}

func TestPinnedCoreAccountIsolationQuotaAndAuthRace(t *testing.T) {
	binary := pinnedCore(t)
	s, cfg, secrets, _ := testStore(t)
	dir := t.TempDir()
	cfg.AuthListen, cfg.PageListen, cfg.StatsURL = freePort(t, false), freePort(t, false), "http://"+freePort(t, false)
	cfg.Certificate, _ = testCertificate(t, dir)
	key := filepath.Join(dir, "test-key.pem")
	s.cfg = cfg
	now := time.Now()
	must(t, s.ChangeUser("quota", "friend", "", GiB, now))
	must(t, s.Hold("explicit isolated core test baseline", now))
	must(t, s.Arm(map[string]int64{"owner": 0, "friend": 0}, "test-only initial baseline", true, now))
	healthyGuard(t, cfg, now, false)
	c := &Collector{Store: s, API: NewHTTPAPI(cfg.StatsURL, secrets.StatsSecret), Generation: func() (string, error) { return ReadGeneration(cfg.GenerationFile) }, Clock: time.Now}
	d := NewDaemon(s, c, cfg, secrets)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon cleanup timed out")
		}
	})

	// Delay exactly one already-durable auth response to prove the controller
	// queues sessions that have not reached the core /online map yet.
	var holdNext atomic.Bool
	signed, release := make(chan string, 1), make(chan struct{})
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		d.AuthHandler(rec, r)
		var reply struct {
			OK bool   `json:"ok"`
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &reply)
		if reply.OK && holdNext.CompareAndSwap(true, false) {
			signed <- reply.ID
			select {
			case <-release:
			case <-r.Context().Done():
				return
			case <-time.After(8 * time.Second):
				return
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(auth.Close)
	server := freePort(t, true)
	config := CoreConfig(cfg, secrets, server, cfg.Certificate, key)
	config["auth"] = map[string]any{"type": "http", "http": map[string]any{"url": auth.URL + "/auth"}}
	config["acl"] = map[string]any{"inline": []string{"direct(all)"}} // Test-only echo exception; production ACL tested separately below.
	path := filepath.Join(dir, "server.json")
	must(t, writeJSON(path, config, 0600))
	core := testProcess(t, binary, "server", path)
	generation := processGeneration(t, core.cmd.Process.Pid, cfg.GenerationFile)
	waitCore(t, func() bool { return c.Collect(ctx, time.Now()) == nil })
	if result := d.Admin(ctx, AdminRequest{Action: "activate"}, time.Now()); !result.OK {
		t.Fatal(result.Error)
	}
	echo := framedEcho(t)
	ownerProcess, ownerAddr := coreClient(t, binary, server, cfg.Certificate, strings.Repeat("o", 32), dir, 0)
	owner := connectCore(t, ownerAddr, echo)
	must(t, frame(owner, []byte("original-owner-stream")))
	var ownerSID string
	must(t, s.db.QueryRow(`SELECT s.id FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE a.name='owner'`).Scan(&ownerSID))
	connection := streamConnection(t, cfg, secrets.StatsSecret, ownerSID)

	// One writer exclusively owns this same socket, continuously throughout
	// both bans. No reconnection/replacement socket can hide a disruption.
	ownerStop := make(chan struct{})
	ownerDone := make(chan error, 1)
	var rounds atomic.Int64
	go func() {
		for i := 0; i < 1500; i++ { // Hard cap: <600 KiB of owner payload, at most 30 s.
			select {
			case <-ownerStop:
				ownerDone <- nil
				return
			default:
			}
			if err := frame(owner, bytes.Repeat([]byte{byte(i)}, 128)); err != nil {
				ownerDone <- err
				return
			}
			rounds.Add(1)
			time.Sleep(20 * time.Millisecond)
		}
		ownerDone <- errors.New("local transfer cap reached before test finished")
	}()
	defer func() {
		close(ownerStop)
		if err := <-ownerDone; err != nil {
			t.Error(err)
		}
	}()

	var friends []net.Conn
	for i := 1; i <= 3; i++ {
		_, addr := coreClient(t, binary, server, cfg.Certificate, strings.Repeat("f", 32), dir, i)
		conn := connectCore(t, addr, echo)
		friends = append(friends, conn)
	}
	for _, conn := range friends[:2] {
		must(t, frame(conn, bytes.Repeat([]byte("f"), 32)))
	}
	must(t, c.Collect(ctx, time.Now()))
	holdNext.Store(true)
	_, raceAddr := coreClient(t, binary, server, cfg.Certificate, strings.Repeat("f", 32), dir, 4)
	var raceSID string
	select {
	case raceSID = <-signed:
	case <-time.After(8 * time.Second):
		t.Fatal("auth race fixture not signed")
	}
	before := rounds.Load()
	result := d.Admin(ctx, AdminRequest{Action: "disable", Name: "friend"}, time.Now())
	if !result.OK || user(t, s, "friend", time.Now()).Issued != 4 {
		t.Fatalf("all signed sessions not issued a ban: %+v", result)
	}
	if user(t, s, "friend", time.Now()).Offline != 0 {
		t.Fatal("HTTP 200 falsely reported idle clients offline")
	}
	close(release)
	race := connectCore(t, raceAddr, echo)
	friends = append(friends, race)
	must(t, c.Collect(ctx, time.Now())) // Observe all four before triggering delayed close.
	for _, conn := range friends {
		if frame(conn, []byte("consume-own-kick")) == nil {
			t.Fatal("target original stream survived")
		}
	}
	waitCore(t, func() bool { _ = c.Collect(ctx, time.Now()); return user(t, s, "friend", time.Now()).Offline == 4 })
	if raceSID == ownerSID || rounds.Load() <= before {
		t.Fatal("owner did not continue transferring throughout target ban")
	}
	if _, err := s.Authenticate(strings.Repeat("f", 32), generation, time.Now()); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled account reauthenticated")
	}

	if r := d.Admin(ctx, AdminRequest{Action: "enable", Name: "friend"}, time.Now()); !r.OK {
		t.Fatal(r.Error)
	}
	var quotaConns []net.Conn
	for i := 5; i <= 6; i++ {
		_, addr := coreClient(t, binary, server, cfg.Certificate, strings.Repeat("f", 32), dir, i)
		conn := connectCore(t, addr, echo)
		quotaConns = append(quotaConns, conn)
		must(t, frame(conn, bytes.Repeat([]byte("q"), 128)))
	}
	must(t, c.Collect(ctx, time.Now()))
	used := user(t, s, "friend", time.Now()).Used
	if r := d.Admin(ctx, AdminRequest{Action: "quota", Name: "friend", Quota: used + 500}, time.Now()); !r.OK {
		t.Fatal(r.Error)
	}
	for _, conn := range quotaConns {
		must(t, frame(conn, bytes.Repeat([]byte("q"), 256)))
	}
	waitCore(t, func() bool { u := user(t, s, "friend", time.Now()); return u.Exhausted && u.Issued == 2 }) // Real 5 s sampler invokes /kick.
	for _, conn := range quotaConns {
		if frame(conn, []byte("quota-kick")) == nil {
			t.Fatal("exhausted account still forwards")
		}
	}
	if streamConnection(t, cfg, secrets.StatsSecret, ownerSID) != connection {
		t.Fatal("owner QUIC connection was rebuilt")
	}
	if gen, err := ReadGeneration(cfg.GenerationFile); err != nil || gen != generation {
		t.Fatal("core restarted for a personal operation")
	}
	if _, err := ProcessTicks(ownerProcess.cmd.Process.Pid); err != nil {
		t.Fatal("owner client exited/rebuilt")
	}
	var ownerSessions int
	must(t, s.db.QueryRow(`SELECT COUNT(*) FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE a.name='owner'`).Scan(&ownerSessions))
	if ownerSessions != 1 {
		t.Fatal("client auto reconnect masked an interruption")
	}
	must(t, c.Collect(ctx, time.Now()))
	u := user(t, s, "owner", time.Now())
	if u.Rx <= u.Tx || u.Tx <= 0 || u.Pending != 0 || u.Disabled {
		t.Fatal("asymmetric upload/download accounting or isolation failed")
	}
	t.Log("official core: 4 targeted sessions including idle/auth race; 2 quota sessions; original owner's TCP + QUIC unchanged; sampled asymmetric payload accounting")
}

// Tiny deterministic UDP DNS fixture; only answers this test's questions and
// never forwards a query to an external resolver.
func fixtureDNS(t *testing.T, records map[string][]net.IP) string {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			if len(q) < 17 {
				continue
			}
			pos := 12
			var labels []string
			for pos < len(q) && q[pos] != 0 {
				size := int(q[pos])
				pos++
				if size > 63 || pos+size > len(q) {
					pos = len(q)
					break
				}
				labels = append(labels, string(q[pos:pos+size]))
				pos += size
			}
			if pos+5 > len(q) {
				continue
			}
			pos++
			typ := binary.BigEndian.Uint16(q[pos : pos+2])
			end := pos + 4
			reply := append([]byte(nil), q[:end]...)
			reply[2], reply[3] = 0x81, 0x80
			for i := 6; i < 12; i++ {
				reply[i] = 0
			}
			count := uint16(0)
			for _, ip := range records[strings.ToLower(strings.Join(labels, "."))] {
				var data []byte
				if typ == 1 {
					data = ip.To4()
				} else if typ == 28 && ip.To4() == nil {
					data = ip.To16()
				}
				if data == nil {
					continue
				}
				reply = append(reply, 0xc0, 0x0c, byte(typ>>8), byte(typ), 0, 1, 0, 0, 0, 30, 0, byte(len(data)))
				reply = append(reply, data...)
				count++
			}
			binary.BigEndian.PutUint16(reply[6:8], count)
			_, _ = c.WriteTo(reply, addr)
		}
	}()
	return c.LocalAddr().String()
}

func TestPinnedProductionACLRejectsPrivateMetadataAndResolvedAddresses(t *testing.T) {
	binary := pinnedCore(t)
	_, cfg, secrets, _ := testStore(t)
	dir := t.TempDir()
	certificate, key := testCertificate(t, dir)
	cfg.AuthListen, cfg.StatsURL = freePort(t, false), "http://"+freePort(t, false)
	// Auth fixture is independent of accounts; this slice verifies the exact
	// production ACL against the official engine, not management behavior.
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid, _ := randomToken(24)
		jsonResponse(w, 200, map[string]any{"ok": true, "id": sid})
	}))
	defer auth.Close()
	var attempted atomic.Int64
	spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempted.Add(1)
		if r.Method != "CONNECT" {
			w.WriteHeader(405)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		_, _ = io.Copy(conn, conn)
	}))
	defer spy.Close()
	dns := fixtureDNS(t, map[string][]net.IP{
		"private.test": {net.ParseIP("10.1.2.3")}, "private6.test": {net.ParseIP("fd00::1234")},
		"mixed.test":               {net.ParseIP("203.0.113.10"), net.ParseIP("fd00::1234")},
		"metadata.google.internal": {net.ParseIP("203.0.113.10")}, "metadata.goog": {net.ParseIP("203.0.113.10")},
	})
	server := freePort(t, true)
	config := CoreConfig(cfg, secrets, server, certificate, key)
	config["auth"] = map[string]any{"type": "http", "http": map[string]any{"url": auth.URL + "/auth"}}
	config["resolver"] = map[string]any{"type": "udp", "udp": map[string]any{"addr": dns, "timeout": "1s"}}
	// Override ONLY the direct outbound with a loopback CONNECT spy. Thus even
	// a failing ACL never contacts metadata, private hosts or public targets.
	config["outbounds"] = []any{map[string]any{"name": "direct", "type": "http", "http": map[string]any{"url": spy.URL}}}
	path := filepath.Join(dir, "acl-server.json")
	must(t, writeJSON(path, config, 0600))
	testProcess(t, binary, "server", path)
	api := NewHTTPAPI(cfg.StatsURL, secrets.StatsSecret)
	waitCore(t, func() bool { _, err := api.Read(context.Background()); return err == nil })
	_, addr := coreClient(t, binary, server, certificate, strings.Repeat("t", 32), dir, 0)
	positive := connectCore(t, addr, "203.0.113.10:12345")
	_, err := positive.Write([]byte("loopback-spy-positive"))
	must(t, err)
	b := make([]byte, 21)
	_, err = io.ReadFull(positive, b)
	must(t, err)
	if attempted.Load() != 1 {
		t.Fatal("positive control did not reach loopback spy")
	}
	for _, target := range []string{"127.0.0.1:18082", "10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80", "169.254.169.254:80", "100.64.0.1:80", "[::1]:18083", "[fd00::1]:80", "[fe80::1]:80", "[::ffff:10.1.2.3]:80", "[::ffff:127.0.0.1]:80", "[::ffff:169.254.169.254]:80", "[::ffff:192.168.1.1]:80", "[::ffff:100.64.0.1]:80", "[64:ff9b::a01:203]:80", "private.test:80", "private6.test:80", "mixed.test:80", "metadata.google.internal:80", "metadata.goog:80"} {
		conn, err := socksDial(addr, target)
		if err == nil {
			conn.Close()
			t.Fatalf("protected target allowed: %s", target)
		}
		if attempted.Load() != 1 {
			t.Fatalf("ACL leaked protected target to outbound: %s", target)
		}
	}
	t.Log("exact production ACL: IPv4/IPv6/private/metadata/literal/resolved/mixed-family rejection; external forwarding physically replaced by loopback spy")
}
