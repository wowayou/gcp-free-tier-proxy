package account

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Synthetic UI fixture: no production values, passwords or service processes.
func presentationFixture() Status {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	return Status{
		RecoveryProtocol: RecoveryProtocol, Month: "2026-10",
		Meta: Meta{Started: now.Add(-time.Hour).Unix(), LastSample: now.UnixNano()},
		Users: []UserView{
			{Name: "owner", Quota: 90 * GiB, Tx: GiB / 4, Rx: 3 * GiB / 4, Adjustment: 80 * GiB,
				Used: 81 * GiB, Remaining: 9 * GiB, Percent: 90, Online: 1, Sessions: 2, SessionCap: 64, Status: "额度预警"},
			{Name: "friend-1", Quota: 30 * GiB, Remaining: 30 * GiB, SessionCap: 64, Status: "正常"},
		},
		Days: []DayView{{Day: "2026-10-23", Tx: GiB / 4, Rx: 3 * GiB / 4, Total: GiB}}, DayMax: GiB,
		Tx: GiB / 4, Rx: 3 * GiB / 4, Adjustment: 80 * GiB, Used: 81 * GiB, QuotaPool: 140 * GiB, Online: 1,
		Guard:       GuardStatus{Month: "2026-10", Used: 34 * GiB, Limit: 180 * GiB, Warn: 150 * GiB, Updated: now.Unix(), Healthy: true},
		VMRemaining: 146 * GiB, CertificateExpires: now.Add(30 * 24 * time.Hour).Unix(),
		Healthy: true, CoreLive: true, AuthAllowed: true,
	}
}

func renderPresentation(t *testing.T, v Status) string {
	t.Helper()
	var b bytes.Buffer
	must(t, pageTemplate.Execute(&b, v))
	return b.String()
}

func TestReadableGiBDoesNotRoundPositiveTinyUsageToZero(t *testing.T) {
	threshold := (GiB + 99) / 100
	for _, tc := range []struct {
		bytes int64
		want  string
	}{{0, "0.00"}, {1, "<0.01"}, {threshold - 1, "<0.01"}, {threshold, "0.01"}, {GiB / 4, "0.25"}, {30 * GiB, "30.00"}} {
		if got := FormatGiB(tc.bytes); got != tc.want {
			t.Fatalf("%d bytes: %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestConnectionNoticeSeparatesReadyWarningsAndSharedFailures(t *testing.T) {
	for _, tc := range []struct {
		name, tone, title string
		change            func(*Status)
	}{
		{"ready", "success", "服务端已就绪", func(*Status) {}},
		{"below warning", "success", "服务端已就绪", func(v *Status) { v.Guard.Used = v.Guard.Warn - 1 }},
		{"at warning", "warning", "整机出站已到警戒线", func(v *Status) { v.Guard.Used = v.Guard.Warn }},
		{"warning disabled", "success", "服务端已就绪", func(v *Status) { v.Guard.Warn = 0 }},
		{"transient stats failure", "warning", "采集有异常，留意最新采样", func(v *Status) { v.StatsError = "test timeout" }},
		{"stale stats", "danger", "统计或运行状态不可放行", func(v *Status) { v.Healthy = false; v.StatsError = "test timeout" }},
		{"auth gate", "warning", "HY2 暂不接受新连接", func(v *Status) { v.AuthAllowed = false }},
		{"maintenance", "warning", "HY2 暂不接受新连接", func(v *Status) { v.Meta.Hold = true }},
		{"core unavailable", "danger", "HY2 核心未确认运行", func(v *Status) { v.CoreLive = false }},
		{"accounting review", "danger", "用量待审核，暂不接入", func(v *Status) { v.Meta.Review = true }},
		{"certificate with healthy true", "danger", "证书异常，接入待核验", func(v *Status) { v.CertificateError = "test expired certificate" }},
		{"shared lock", "danger", "HY2 共享保护已锁定", func(v *Status) { v.HealthBlocked = true }},
		{"guard missing", "danger", "整机保护状态不可核验", func(v *Status) { v.GuardError = "test missing guard" }},
		{"guard locked", "danger", "整机保护不放行", func(v *Status) { v.Guard.Blocked = true; v.GuardError = "test blocked guard" }},
		{"guard limit", "danger", "整机保护不放行", func(v *Status) { v.Guard.Used = v.Guard.Limit }},
		{"witness takes precedence", "danger", "用量未知，需要核验", func(v *Status) { v.DataError = "test witness error"; v.Guard.Blocked = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := presentationFixture()
			tc.change(&v)
			got := v.ConnectionNotice()
			if got.Tone != tc.tone || got.Title != tc.title || got.Detail == "" {
				t.Fatalf("wrong notice: %+v", got)
			}
			body := renderPresentation(t, v)
			if !strings.Contains(body, "<h2>"+tc.title+"</h2>") {
				t.Fatal("page omitted the primary status")
			}
			if tc.tone != "success" && strings.Contains(body, "<h2>服务端已就绪</h2>") {
				t.Fatal("page showed a green ready banner despite the warning/fault")
			}
		})
	}
}

func TestUserNoticeDoesNotConfuseAccountEnabledWithClientAccess(t *testing.T) {
	for _, tc := range []struct {
		name, title string
		change      func(*Status, *UserView)
	}{
		{"enabled", "已启用", func(*Status, *UserView) {}},
		{"below 80 percent", "已启用", func(_ *Status, u *UserView) { u.Percent = 79.99 }},
		{"at 80 percent", "余额偏低", func(_ *Status, u *UserView) { u.Percent = 80 }},
		{"exact quota", "本月额度已用完", func(_ *Status, u *UserView) { u.Used = u.Quota }},
		{"exhausted flag", "本月额度已用完", func(_ *Status, u *UserView) { u.Exhausted = true }},
		{"paused", "已暂停", func(_ *Status, u *UserView) { u.Disabled = true }},
		{"revoked", "已永久撤销", func(_ *Status, u *UserView) { u.Disabled = true; u.Revoked = true }},
		{"unknown quota", "账户额度不可核验", func(_ *Status, u *UserView) { u.Quota = 0 }},
		{"connection capacity", "新连接容量已满", func(_ *Status, u *UserView) { u.Sessions = u.SessionCap }},
		{"old-session ban after rotation", "已启用", func(_ *Status, u *UserView) { u.Pending = 2; u.Failed = 1 }},
		{"certificate", "接入待核验", func(v *Status, _ *UserView) { v.CertificateError = "test expired" }},
		{"guard", "暂不能接入", func(v *Status, _ *UserView) { v.GuardError = "test stale" }},
		{"guard lock", "暂不能接入", func(v *Status, _ *UserView) { v.Guard.Blocked = true }},
		{"guard limit", "暂不能接入", func(v *Status, _ *UserView) { v.Guard.Used = v.Guard.Limit }},
		{"shared lock", "暂不能接入", func(v *Status, _ *UserView) { v.HealthBlocked = true }},
		{"review", "暂不能接入", func(v *Status, _ *UserView) { v.Meta.Review = true }},
		{"hold", "暂不能接入", func(v *Status, _ *UserView) { v.Meta.Hold = true }},
		{"auth gate", "暂不能接入", func(v *Status, _ *UserView) { v.AuthAllowed = false }},
		{"core", "暂不能接入", func(v *Status, _ *UserView) { v.CoreLive = false }},
		{"stale statistics", "暂不能接入", func(v *Status, _ *UserView) { v.Healthy = false }},
		{"untrusted ledger", "暂不能接入", func(v *Status, _ *UserView) { v.DataError = "test witness error" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := presentationFixture()
			u := v.Users[1]
			tc.change(&v, &u)
			got := v.UserNotice(u)
			if got.Title != tc.title || got.Detail == "" {
				t.Fatalf("wrong account notice: %+v", got)
			}
		})
	}
}

func TestPresentationQuotaPoolMatchesRealReservationAfterAccountChanges(t *testing.T) {
	s, _, _, now := testStore(t)
	owner, friend := session(t, s, "owner", now), session(t, s, "friend", now)
	check := func() {
		t.Helper()
		v, err := s.View(now)
		must(t, err)
		tx, err := s.db.Begin()
		must(t, err)
		want, err := allocatedPool(tx, v.Month, "")
		must(t, err)
		must(t, tx.Rollback())
		if v.AllocatedQuota() != want || v.UnallocatedQuota() != max(0, v.QuotaPool-want) {
			t.Fatalf("presentation understated reserved consumption: got %d, want %d", v.AllocatedQuota(), want)
		}
	}
	check()
	must(t, s.Apply("generation-1", Snapshot{Traffic: map[string]Counter{owner: {Tx: 200, Rx: 300}, friend: {Tx: 600, Rx: 900}}, Online: map[string]int{}}, now))
	check() // Used above quota stays reserved.
	must(t, s.ChangeUser("disable", "owner", "", 0, now))
	check() // Pausing does not release its quota.
	must(t, s.ChangeUser("quota", "owner", "", 400, now))
	check() // Lowering below usage must not make consumed bytes reusable.
	must(t, s.ChangeUser("revoke", "friend", "", 0, now))
	check() // Retired consumption, not its former quota, remains reserved.
	v := Status{QuotaPool: GiB, Users: []UserView{{Quota: 2 * GiB}}}
	if v.UnallocatedQuota() != 0 {
		t.Fatal("over-allocation should not display negative spare capacity")
	}
}

func TestPageOverviewSeparatesRemainingSampledAndConservativeUsage(t *testing.T) {
	v := presentationFixture()
	body := renderPresentation(t, v)
	for _, text := range []string{"流量与账户", "整机保护余量", "146.00", "HY2 本月记账", "81.00", "正常采样", "80.00", "GiB 未分配", "20.00", "账户余额", "9.00", "余额偏低", "friend-1", "30.00", "已启用", "不等于 GCP 出站账单", "单账户异常时，不重启整个代理"} {
		if !strings.Contains(body, text) {
			t.Fatalf("overview missing %q", text)
		}
	}
	v.Users[1].Used, v.Users[1].Tx = 1, 1
	body = renderPresentation(t, v)
	if !strings.Contains(body, "&lt;0.01") {
		t.Fatal("tiny positive usage lost or not HTML-escaped")
	}
}

func TestPageUnknownUsageHidesOldAccountsDaysAndPool(t *testing.T) {
	v := presentationFixture()
	v.DataError = "test unreadable ledger"
	v.Users[0].Name, v.Users[0].Remaining = "STALE_ACCOUNT_DO_NOT_SHOW", 9876*GiB
	v.Used, v.Adjustment = 5432*GiB, 4321*GiB
	v.Days[0].Day = "STALE_DAY_DO_NOT_SHOW"
	body := renderPresentation(t, v)
	for _, text := range []string{"STALE_ACCOUNT_DO_NOT_SHOW", "STALE_DAY_DO_NOT_SHOW", "9876.00", "5432.00", "4321.00", "<article"} {
		if strings.Contains(body, text) {
			t.Fatalf("unknown page retained stale data: %q", text)
		}
	}
	if !strings.Contains(body, "用量未知") || !strings.Contains(body, "暂不展示余额和在线数") || !strings.Contains(body, "test unreadable ledger") {
		t.Fatal("unknown page did not explain the failure")
	}
}

func TestPageBanStatesEmptyAccountsAndUntrustedText(t *testing.T) {
	v := presentationFixture()
	v.Users[1].Disabled, v.Users[1].Pending, v.Users[1].Failed = true, 2, 1
	body := renderPresentation(t, v)
	if !strings.Contains(body, "已暂停") || !strings.Contains(body, "旧连接踢出未完成：2 待处理 · 1 失败") {
		t.Fatal("pause/ban failure was hidden")
	}
	v.Users[1].Pending, v.Users[1].Failed, v.Users[1].Issued = 0, 0, 3
	body = renderPresentation(t, v)
	if !strings.Contains(body, "3 条踢出标记已下发，仍待确认连接关闭") {
		t.Fatal("issued ban falsely implied immediate offline")
	}
	v.Users, v.Days = nil, nil
	body = renderPresentation(t, v)
	if !strings.Contains(body, "尚无账户") || !strings.Contains(body, "暂无可信日采样") {
		t.Fatal("empty states lost")
	}
	injected := `<script>alert("untrusted")</script>`
	v.Meta.Reason, v.Gaps = injected, []string{injected}
	body = renderPresentation(t, v)
	if strings.Contains(body, injected) || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("admin-controlled reason escaped html/template protection")
	}
}

func TestPageStorageFailurePreservesIndependentGuardTrustAndReadOnlyHeaders(t *testing.T) {
	for _, missingGuard := range []bool{false, true} {
		t.Run(map[bool]string{false: "readable guard", true: "missing guard"}[missingGuard], func(t *testing.T) {
			s, cfg, secrets, now := testStore(t)
			healthyGuard(t, cfg, now, false)
			c := &Collector{Store: s, API: &fakeAPI{}, Generation: func() (string, error) { return "generation-1", nil }}
			d := NewDaemon(s, c, cfg, secrets)
			must(t, d.publish(now))
			must(t, s.Close())
			if missingGuard {
				must(t, os.Remove(cfg.GuardStatusFile))
			}
			must(t, d.publish(now.Add(time.Second)))
			get := func(path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", path, nil)
				r.SetBasicAuth(secrets.PageUser, secrets.PagePassword)
				w := httptest.NewRecorder()
				d.PageHandler(w, r)
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				return w
			}
			var v Status
			must(t, json.Unmarshal(get("/api/status").Body.Bytes(), &v))
			if v.Healthy || v.AuthAllowed || v.DataError == "" || v.RecoveryProtocol != RecoveryProtocol || (v.GuardError != "") != missingGuard {
				t.Fatal("fallback lost storage/independent guard trust")
			}
			w := get("/")
			body := w.Body.String()
			if !strings.Contains(body, "用量未知") || strings.Contains(body, "<article") || (missingGuard && strings.Contains(body, `class="number">0.00`)) {
				t.Fatal("page displayed stale accounts or a fictitious zero")
			}
			if !missingGuard && (v.VMRemaining != 180*GiB || !strings.Contains(body, "180.00")) {
				t.Fatal("independently trustworthy guard remainder was lost")
			}
			for key, want := range map[string]string{
				"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff",
				"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'",
			} {
				if w.Header().Get(key) != want {
					t.Fatalf("read-only security header changed: %s", key)
				}
			}
			for _, secret := range []string{secrets.PagePassword, secrets.AuthKey, secrets.CredentialKey, secrets.StatsSecret, "hysteria2://"} {
				if strings.Contains(body, secret) {
					t.Fatal("page exposed a credential")
				}
			}
		})
	}
}
