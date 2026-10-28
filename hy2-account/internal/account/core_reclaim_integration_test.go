package account

// Real, checksum-pinned official core: delta (/traffic?clear=1) accounting and
// reconnect capacity release. Loopback fixtures only; skipped without HY2_BIN.
// HY2_BIN=/path/to/verified/hysteria go test ./internal/account -run TestPinnedReclaim -v

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinnedReclaimExactDeltaAccountingAndReconnectRelease(t *testing.T) {
	binary := pinnedCore(t)
	s, cfg, secrets, _ := testStore(t)
	dir := t.TempDir()
	cfg.AuthListen, cfg.PageListen, cfg.StatsURL = freePort(t, false), freePort(t, false), "http://"+freePort(t, false)
	cfg.Certificate, _ = testCertificate(t, dir)
	key := filepath.Join(dir, "test-key.pem")
	// Live cap 3: the old cumulative cap would deny the 4th reconnect.
	cfg.MaxSessionsPerUser, cfg.MaxSessionsTotal = 3, 16
	s.cfg = cfg
	now := time.Now()
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
	server := freePort(t, true)
	config := CoreConfig(cfg, secrets, server, cfg.Certificate, key)
	config["auth"] = map[string]any{"type": "http", "http": map[string]any{"url": "http://" + cfg.AuthListen + "/auth"}}
	config["acl"] = map[string]any{"inline": []string{"direct(all)"}} // Test-only loopback echo.
	path := filepath.Join(dir, "server.json")
	must(t, writeJSON(path, config, 0600))
	core := testProcess(t, binary, "server", path)
	generation := processGeneration(t, core.cmd.Process.Pid, cfg.GenerationFile)
	waitCore(t, func() bool { return c.Collect(ctx, time.Now()) == nil })
	if r := d.Admin(ctx, AdminRequest{Action: "activate"}, time.Now()); !r.OK {
		t.Fatal(r.Error)
	}
	echo := framedEcho(t)

	var wantTx, wantRx int64
	const cycles = 6 // > cap 3 + slack; paced under the 8/minute auth limit.
	seen := map[string]bool{}
	for i := 0; i < cycles; i++ {
		client, addr := coreClient(t, binary, server, cfg.Certificate, strings.Repeat("o", 32), dir, 100+i)
		conn := connectCore(t, addr, echo)
		for j := 0; j < 3; j++ {
			payload := bytes.Repeat([]byte{byte('a' + i)}, 64+j*100)
			must(t, frame(conn, payload))
			wantTx += int64(4 + len(payload))
			wantRx += int64(2 * len(payload))
			// One destructive delta read between frames: totals must still be exact.
			must(t, c.Collect(ctx, time.Now()))
		}
		u := user(t, s, "owner", time.Now())
		if u.Online != 1 {
			t.Fatalf("cycle %d: live connection not observed online: %+v", i, u)
		}
		conn.Close()
		client.stop()
		waitCore(t, func() bool {
			_ = c.Collect(ctx, time.Now())
			return user(t, s, "owner", time.Now()).Online == 0
		})
		var sid string
		must(t, s.db.QueryRow(`SELECT s.id FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE a.name='owner' ORDER BY s.created DESC LIMIT 1`).Scan(&sid))
		if seen[sid] {
			t.Fatal("session ID reused across reconnect")
		}
		seen[sid] = true
		time.Sleep(8 * time.Second) // Stay under the per-account 8 auths/minute limit.
	}
	must(t, c.Collect(ctx, time.Now()))
	u := user(t, s, "owner", time.Now())
	if u.Tx != wantTx || u.Rx != wantRx {
		t.Fatalf("delta accounting not exact: tx=%d/%d rx=%d/%d", u.Tx, wantTx, u.Rx, wantRx)
	}
	if u.ActiveSessions != 0 || u.Sessions != cycles || len(seen) != cycles {
		t.Fatalf("session counters wrong: active=%d cumulative=%d distinct=%d", u.ActiveSessions, u.Sessions, len(seen))
	}
	m, err := s.Meta()
	must(t, err)
	if m.Review || m.Hold {
		t.Fatalf("normal reconnects entered review/hold: %s", m.Reason)
	}
	// The core keeps no per-session counters for closed sessions after the
	// cleared reads: a NON-destructive /traffic read must now be empty.
	r, err := http.NewRequest("GET", cfg.StatsURL+"/traffic", nil)
	must(t, err)
	r.Header.Set("Authorization", secrets.StatsSecret)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(r)
	must(t, err)
	var left map[string]Counter
	must(t, DecodeJSON(resp.Body, &left, MaxStatsBody))
	resp.Body.Close()
	if len(left) != 0 {
		t.Fatalf("core retained counters for closed sessions: %d", len(left))
	}
	if g, err := ReadGeneration(cfg.GenerationFile); err != nil || g != generation {
		t.Fatal("core restarted during reconnect test")
	}
	t.Logf("official core: %d reconnects beyond live cap 3, exact tx=%d rx=%d across %d destructive reads, core counters empty", cycles, wantTx, wantRx, cycles*3)
}
