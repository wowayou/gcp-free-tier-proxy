package account

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"
)

//go:embed page.html
var pageFiles embed.FS

var pageTemplate = template.Must(template.New("page.html").Funcs(template.FuncMap{
	"gib":     func(n int64) string { return fmt.Sprintf("%.3f", float64(n)/float64(GiB)) },
	"seconds": func(n int64) string { return displayTime(n, false) },
	"nanos":   func(n int64) string { return displayTime(n, true) },
}).ParseFS(pageFiles, "page.html"))

func displayTime(n int64, nanos bool) string {
	if n <= 0 {
		return "尚无可信记录"
	}
	t := time.Unix(n, 0)
	if nanos {
		t = time.Unix(0, n)
	}
	return t.In(billingLocation).Format("2006-01-02 15:04:05 MST")
}

type UserView struct {
	Name       string  `json:"name"`
	Quota      int64   `json:"quota_bytes"`
	Tx         int64   `json:"tx"`
	Rx         int64   `json:"rx"`
	Adjustment int64   `json:"conservative_adjustment"`
	Used       int64   `json:"used_bytes"`
	Remaining  int64   `json:"remaining_bytes"`
	Percent    float64 `json:"percent"`
	Disabled   bool    `json:"disabled"`
	Revoked    bool    `json:"revoked"`
	Exhausted  bool    `json:"exhausted"`
	Online     int     `json:"online"`
	Pending    int     `json:"ban_pending"`
	Failed     int     `json:"ban_failed"`
	Issued     int     `json:"ban_issued_waiting"`
	Offline    int     `json:"ban_offline"`
	Sessions   int     `json:"sessions_generation"`
	SessionCap int     `json:"session_cap_generation"`
	Status     string  `json:"status"`
}

type DayView struct {
	Day   string `json:"day"`
	Tx    int64  `json:"tx"`
	Rx    int64  `json:"rx"`
	Total int64  `json:"total"`
}
type Status struct {
	RecoveryProtocol   int         `json:"recovery_protocol"`
	Month              string      `json:"month"`
	Meta               Meta        `json:"accounting"`
	Users              []UserView  `json:"users"`
	Days               []DayView   `json:"days"`
	DayMax             int64       `json:"day_max"`
	Tx                 int64       `json:"tx"`
	Rx                 int64       `json:"rx"`
	Adjustment         int64       `json:"conservative_adjustment"`
	Used               int64       `json:"used_bytes"`
	VMRemaining        int64       `json:"vm_remaining_bytes"`
	QuotaPool          int64       `json:"quota_pool_bytes"`
	Online             int         `json:"online"`
	Guard              GuardStatus `json:"vm"`
	GuardError         string      `json:"vm_error"`
	StatsError         string      `json:"stats_error"`
	CertificateExpires int64       `json:"certificate_expires"`
	CertificateError   string      `json:"certificate_error"`
	Healthy            bool        `json:"healthy"`
	AuthAllowed        bool        `json:"authentication_allowed"`
	CoreLive           bool        `json:"core_live"`
	HealthBlocked      bool        `json:"shared_health_blocked"`
	DataError          string      `json:"data_error,omitempty"`
	Gaps               []string    `json:"accounting_gaps"`
}

func (s *Store) View(now time.Time) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v Status
	v.Month, _ = billingTime(now)
	m, err := readMeta(s.db)
	if err != nil {
		return v, err
	}
	if s.fatal != nil {
		m.Review = true
		m.Reason = s.fatal.Error()
		// A readable SQLite file is not proof of a valid durable witness. The
		// root status API must expose this too, not only the page fallback.
		v.DataError = s.fatal.Error()
	}
	v.Meta = m
	rows, err := s.db.Query(`SELECT a.name,a.quota,a.disabled,a.revoked,COALESCE(m.tx,0),COALESCE(m.rx,0),COALESCE(m.adjustment,0),COALESCE(m.exhausted,0),
 (SELECT COALESCE(SUM(online),0) FROM sessions WHERE account_id=a.id AND generation=?),
 (SELECT COUNT(*) FROM sessions WHERE account_id=a.id AND generation=? AND kick_state='pending'),
 (SELECT COUNT(*) FROM sessions WHERE account_id=a.id AND generation=? AND kick_state='failed'),
 (SELECT COUNT(*) FROM sessions WHERE account_id=a.id AND generation=? AND kick_state='issued'),
 (SELECT COUNT(*) FROM sessions WHERE account_id=a.id AND generation=? AND kick_state='offline'),
 (SELECT COUNT(*) FROM sessions WHERE account_id=a.id AND generation=?)
 FROM accounts a LEFT JOIN months m ON m.account_id=a.id AND m.month=? ORDER BY a.created`, m.Generation, m.Generation, m.Generation, m.Generation, m.Generation, m.Generation, v.Month)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var u UserView
		if err := rows.Scan(&u.Name, &u.Quota, &u.Disabled, &u.Revoked, &u.Tx, &u.Rx, &u.Adjustment, &u.Exhausted, &u.Online, &u.Pending, &u.Failed, &u.Issued, &u.Offline, &u.Sessions); err != nil {
			rows.Close()
			return v, err
		}
		u.Used = u.Tx + u.Rx + u.Adjustment
		u.Remaining = u.Quota - u.Used
		if u.Remaining < 0 {
			u.Remaining = 0
		}
		u.Percent = 100 * float64(u.Used) / float64(u.Quota)
		if u.Percent > 100 {
			u.Percent = 100
		}
		u.Exhausted = u.Used >= u.Quota
		u.Status = "正常"
		if u.Percent >= 80 {
			u.Status = "额度预警"
		}
		if u.Exhausted {
			u.Status = "本月额度耗尽"
		}
		if u.Disabled {
			u.Status = "手工暂停"
		}
		if u.Revoked {
			u.Status = "已撤销"
		}
		if u.Pending > 0 || u.Failed > 0 {
			u.Status += " · 存量封禁未完成"
		} else if u.Issued > 0 {
			u.Status += " · 封禁已下发/等待离线"
		}
		u.SessionCap = s.cfg.MaxSessionsPerUser
		if u.Sessions >= u.SessionCap {
			u.Status += " · 本代次会话容量耗尽/拒绝新连接"
		} else if u.Sessions*5 >= u.SessionCap*4 {
			u.Status += " · 会话容量预警"
		}
		v.Users = append(v.Users, u)
		v.Tx += u.Tx
		v.Rx += u.Rx
		v.Adjustment += u.Adjustment
		v.Online += u.Online
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return v, err
	}
	rows.Close()
	v.Used = v.Tx + v.Rx + v.Adjustment
	v.QuotaPool = s.cfg.QuotaPoolBytes
	rows, err = s.db.Query(`SELECT day,SUM(tx),SUM(rx) FROM days GROUP BY day ORDER BY day DESC LIMIT 14`)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var d DayView
		if err := rows.Scan(&d.Day, &d.Tx, &d.Rx); err != nil {
			rows.Close()
			return v, err
		}
		d.Total = d.Tx + d.Rx
		if d.Total > v.DayMax {
			v.DayMax = d.Total
		}
		v.Days = append(v.Days, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return v, err
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT reason FROM gaps ORDER BY id DESC LIMIT 10`)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			rows.Close()
			return v, err
		}
		v.Gaps = append(v.Gaps, reason)
	}
	err = rows.Err()
	rows.Close()
	return v, err
}

type Health struct {
	Updated    int64  `json:"updated"`
	LastSample int64  `json:"last_sample"`
	Healthy    bool   `json:"healthy"`
	Bootstrap  bool   `json:"bootstrap"`
	ArmUntil   int64  `json:"arm_until"`
	Review     bool   `json:"review"`
	Hold       bool   `json:"hold"`
	Reason     string `json:"reason"`
	MAC        string `json:"mac"`
}

func healthMAC(h Health, key []byte) string {
	h.MAC = ""
	b, _ := json.Marshal(h)
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}
func VerifyHealth(path string, key []byte, now time.Time) (Health, error) {
	var h Health
	f, err := os.Open(path)
	if err != nil {
		return h, err
	}
	defer f.Close()
	if err := DecodeJSON(f, &h, 4096); err != nil {
		return h, err
	}
	if !hmac.Equal([]byte(h.MAC), []byte(healthMAC(h, key))) || h.Updated > now.Unix()+5 || now.Unix()-h.Updated > 15 {
		return h, errors.New("accountd health witness stale/invalid")
	}
	if h.Healthy && (h.Review || h.LastSample <= 0 || h.LastSample > now.UnixNano() || now.UnixNano()-h.LastSample > 30*int64(time.Second)) {
		return h, errors.New("accountd sample is stale/inconsistent")
	}
	if h.Bootstrap && (h.Review || !h.Hold || h.ArmUntil < now.Unix()) {
		return h, errors.New("armed bootstrap expired/inconsistent")
	}
	if !h.Healthy && !h.Bootstrap {
		return h, errors.New("accountd is not healthy")
	}
	return h, nil
}

type Daemon struct {
	Store      *Store
	Collector  *Collector
	Config     Config
	Secrets    Secrets
	authSlots  chan struct{}
	pageSlots  chan struct{}
	adminSlots chan struct{}
	adminMu    sync.Mutex // Root mutations/export follow-ups cannot rename out from under a ban.
	publishMu  sync.Mutex
	rateMu     sync.Mutex
	rateStart  time.Time
	rateCount  int
	pageMu     sync.RWMutex
	page       []byte
	statusJSON []byte
	listeners  []net.Listener
}

func NewDaemon(s *Store, c *Collector, cfg Config, secrets Secrets) *Daemon {
	return &Daemon{Store: s, Collector: c, Config: cfg, Secrets: secrets, authSlots: make(chan struct{}, 8), pageSlots: make(chan struct{}, 4), adminSlots: make(chan struct{}, 2)}
}
func (d *Daemon) allowed(now time.Time) bool {
	d.rateMu.Lock()
	defer d.rateMu.Unlock()
	if d.rateStart.IsZero() || now.Sub(d.rateStart) >= time.Minute {
		d.rateStart = now
		d.rateCount = 0
	}
	if d.rateCount >= 60 {
		return false
	}
	d.rateCount++
	return true
}
func slot(ch chan struct{}) bool {
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}
func closeSlot(ch chan struct{}) { <-ch }
func jsonResponse(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func (d *Daemon) AuthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/auth" || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if !slot(d.authSlots) {
		jsonResponse(w, 503, map[string]any{"ok": false})
		return
	}
	defer closeSlot(d.authSlots)
	now := time.Now()
	if !d.allowed(now) {
		jsonResponse(w, 429, map[string]any{"ok": false})
		return
	}
	var req struct {
		Addr string `json:"addr"`
		Auth string `json:"auth"`
		Tx   uint64 `json:"tx"`
	}
	if err := DecodeJSON(r.Body, &req, MaxBody); err != nil {
		jsonResponse(w, 400, map[string]any{"ok": false})
		return
	}
	if _, err := ReadGuard(d.Config.GuardStatusFile, now); err != nil || sharedBlocked(d.Config.HealthBlockedFile) {
		jsonResponse(w, 200, map[string]any{"ok": false})
		return
	}
	gen, err := d.Collector.Generation()
	if err != nil {
		jsonResponse(w, 200, map[string]any{"ok": false})
		return
	}
	sid, err := d.Store.Authenticate(req.Auth, gen, now)
	if err != nil {
		jsonResponse(w, 200, map[string]any{"ok": false})
		return
	}
	// No allow cache. Disabled and session insertion share the same transaction
	// lock; even if this HTTP response is delayed, the durable sid can be kicked.
	jsonResponse(w, 200, map[string]any{"ok": true, "id": sid})
}

func (d *Daemon) PageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || (r.URL.Path != "/" && r.URL.Path != "/api/status") || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if !slot(d.pageSlots) {
		http.Error(w, "busy", 503)
		return
	}
	defer closeSlot(d.pageSlots)
	user, pw, ok := r.BasicAuth()
	expected := sha256.Sum256([]byte(d.Secrets.PagePassword))
	actual := sha256.Sum256([]byte(pw))
	if !ok || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 || user != d.Secrets.PageUser {
		w.Header().Set("WWW-Authenticate", `Basic realm="HY2 usage", charset="UTF-8"`)
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	d.pageMu.RLock()
	defer d.pageMu.RUnlock()
	if r.URL.Path == "/api/status" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(d.statusJSON)
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(d.page)
	}
}

func (d *Daemon) Status(now time.Time) (Status, error) {
	v, err := d.Store.View(now)
	v.RecoveryProtocol = RecoveryProtocol
	if err != nil {
		return v, err
	}
	g, e := ReadGuard(d.Config.GuardStatusFile, now)
	v.Guard = g
	v.VMRemaining = g.Limit - g.Used
	if v.VMRemaining < 0 {
		v.VMRemaining = 0
	}
	if e != nil {
		v.GuardError = e.Error()
	}
	v.StatsError = d.Collector.Failure()
	cert, e := os.ReadFile(d.Config.Certificate)
	if e != nil {
		v.CertificateError = "certificate unreadable"
	} else {
		p, _ := pem.Decode(cert)
		if p == nil {
			v.CertificateError = "certificate invalid"
		} else if c, e := x509.ParseCertificate(p.Bytes); e != nil {
			v.CertificateError = "certificate invalid"
		} else {
			v.CertificateExpires = c.NotAfter.Unix()
			if c.VerifyHostname(d.Config.SNI) != nil || !now.Before(c.NotAfter) || now.Before(c.NotBefore) {
				v.CertificateError = "certificate expired/not valid for SNI"
			}
		}
	}
	gen, genErr := d.Collector.Generation()
	v.CoreLive = genErr == nil
	v.HealthBlocked = sharedBlocked(d.Config.HealthBlockedFile)
	v.Healthy = eFresh(v.Meta, now) && v.GuardError == "" && genErr == nil && gen == v.Meta.Generation && !v.HealthBlocked
	v.AuthAllowed = v.Healthy && !v.Meta.Hold
	return v, nil
}

func sharedBlocked(path string) bool {
	_, err := os.Lstat(path)
	return !os.IsNotExist(err) // An unreadable latch cannot grant permission.
}
func eFresh(m Meta, now time.Time) bool {
	return !m.Review && m.LastSample > 0 && now.UnixNano()-m.LastSample <= 30*int64(time.Second) && m.LastSample <= now.UnixNano()
}

func (d *Daemon) publish(now time.Time) error {
	d.publishMu.Lock()
	defer d.publishMu.Unlock()
	m, metaErr := d.Store.Meta()
	g, guardErr := ReadGuard(d.Config.GuardStatusFile, now)
	gen, genErr := d.Collector.Generation()
	h := Health{Updated: now.Unix(), LastSample: m.LastSample, ArmUntil: m.ArmUntil, Review: m.Review, Hold: m.Hold, Reason: m.Reason}
	h.Bootstrap = metaErr == nil && guardErr == nil && !m.Review && m.Hold && m.ArmUntil >= now.Unix()
	h.Healthy = metaErr == nil && guardErr == nil && genErr == nil && gen == m.Generation && eFresh(m, now) && !sharedBlocked(d.Config.HealthBlockedFile)
	if guardErr != nil {
		h.Reason = guardErr.Error()
	}
	if metaErr != nil {
		h.Reason = "database unavailable or restore witness invalid"
	}
	if !h.Healthy && !h.Bootstrap && h.Reason == "" {
		h.Reason = "statistics stale or core unavailable; independent health service must stop HY2"
	}
	h.MAC = healthMAC(h, d.Store.authKey)
	if err := writeJSON(d.Config.HealthFile, h, 0600); err != nil {
		return err
	}
	v, err := d.Status(now)
	if err != nil {
		// Do not leave an old green page in the cache after storage failure.
		// Rendering remains outside the service-health decision.
		month, _ := billingTime(now)
		v = Status{Month: month, Meta: m, Guard: g, DataError: "账户账本不可读；用量未知，不能据旧数字放行", Healthy: false}
		v.Meta.Review = true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var buffer bytes.Buffer
	if err := pageTemplate.Execute(&buffer, v); err != nil {
		return nil
	}
	d.pageMu.Lock()
	d.page = buffer.Bytes()
	d.statusJSON = b
	d.pageMu.Unlock()
	return nil
}

func (d *Daemon) Run(ctx context.Context) error {
	if st, err := os.Lstat(d.Config.AdminSocket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("admin socket path is not a socket")
		}
		if conn, err := net.DialTimeout("unix", d.Config.AdminSocket, 300*time.Millisecond); err == nil {
			conn.Close()
			return errors.New("admin socket is already live")
		}
		if err := os.Remove(d.Config.AdminSocket); err != nil {
			return err
		}
	}
	auth, err := net.Listen("tcp", d.Config.AuthListen)
	if err != nil {
		return err
	}
	page, err := net.Listen("tcp", d.Config.PageListen)
	if err != nil {
		auth.Close()
		return err
	}
	admin, err := net.Listen("unix", d.Config.AdminSocket)
	if err != nil {
		auth.Close()
		page.Close()
		return err
	}
	if err := os.Chmod(d.Config.AdminSocket, 0600); err != nil {
		admin.Close()
		auth.Close()
		page.Close()
		return err
	}
	d.listeners = []net.Listener{auth, page, admin}
	defer func() {
		for _, l := range d.listeners {
			l.Close()
		}
		os.Remove(d.Config.AdminSocket)
	}()
	errorsCh := make(chan error, 3)
	for i, handler := range []http.Handler{http.HandlerFunc(d.AuthHandler), http.HandlerFunc(d.PageHandler), http.HandlerFunc(d.AdminHandler)} {
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
		if i == 2 {
			srv.ConnContext = adminPeer
		}
		go func(l net.Listener) {
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				errorsCh <- err
			}
		}(d.listeners[i])
		defer srv.Close()
	}
	_ = d.Collector.Tick(ctx, time.Now())
	if err := d.publish(time.Now()); err != nil {
		return err
	}
	// No periodic cloud calls, parallel collectors, backups or retry storms.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errorsCh:
			return err
		case <-ticker.C:
			_ = d.Collector.Tick(ctx, time.Now())
			if err := d.publish(time.Now()); err != nil {
				return err
			}
		}
	}
}

// Unit-test access stays in package account. Runtime mutates only over this
// socket, with SO_PEERCRED requiring UID 0 even if Unix path permissions change.
type peerKey struct{}

func adminPeer(ctx context.Context, conn net.Conn) context.Context {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return context.WithValue(ctx, peerKey{}, false)
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return context.WithValue(ctx, peerKey{}, false)
	}
	allowed := false
	if err := raw.Control(func(fd uintptr) {
		cred, e := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		allowed = e == nil && cred.Uid == 0
	}); err != nil {
		allowed = false
	}
	return context.WithValue(ctx, peerKey{}, allowed)
}
