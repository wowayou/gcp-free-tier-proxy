package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

// Human status consumes the same single read-only status response as JSON.
// Never export credentials, call health run, or manage services from here.
func writeHumanStatus(w io.Writer, v account.Status) error {
	var b strings.Builder
	notice := v.ConnectionNotice()
	fmt.Fprintf(&b, "HY2 用量与账户 · %s（洛杉矶月账本）\n\n", v.Month)
	fmt.Fprintf(&b, "服务端：%s\n%s\n\n", notice.Title, notice.Detail)
	if v.GuardError != "" {
		fmt.Fprintln(&b, "整机保护：余量不可核验（不是 0）")
	} else {
		fmt.Fprintf(&b, "整机保护：剩余 %s GiB · 已发送 %s / %s GiB · 警戒 %s GiB\n",
			account.FormatGiB(v.VMRemaining), account.FormatGiB(v.Guard.Used), account.FormatGiB(v.Guard.Limit), account.FormatGiB(v.Guard.Warn))
	}
	if v.DataError != "" {
		fmt.Fprintln(&b, "账户用量：未知；暂不展示余额、在线数和可分配额度")
	} else {
		fmt.Fprintf(&b, "HY2 记账：%s GiB = 正常采样 %s + 保守补账 %s GiB\n",
			account.FormatGiB(v.Used), account.FormatGiB(v.Sampled()), account.FormatGiB(v.Adjustment))
		fmt.Fprintf(&b, "额度池：已占用 %s / %s GiB · 未分配 %s GiB（不是新增流量授权）\n\n",
			account.FormatGiB(v.AllocatedQuota()), account.FormatGiB(v.QuotaPool), account.FormatGiB(v.UnallocatedQuota()))
		if len(v.Users) == 0 {
			fmt.Fprintln(&b, "尚无账户。按已批准额度在 VM 上创建。")
		} else {
			fmt.Fprintln(&b, "账户余额：")
			for _, u := range v.Users {
				fmt.Fprintf(&b, "%s · %s\n  剩余 %s GiB · 已用 %s / 月额 %s GiB · %d 个在线连接\n",
					u.Name, v.UserNotice(u).Title, account.FormatGiB(u.Remaining), account.FormatGiB(u.Used), account.FormatGiB(u.Quota), u.Online)
				if u.Pending > 0 || u.Failed > 0 || u.Issued > 0 {
					fmt.Fprintf(&b, "  旧连接踢出 %d 待下发 · %d 失败 · %d 已下发/待确认离线\n", u.Pending, u.Failed, u.Issued)
				}
			}
		}
	}
	fmt.Fprintf(&b, "\n最后采集：%s · 证书到期：%s\n", humanTime(v.Meta.LastSample, true), humanTime(v.CertificateExpires, false))
	for _, item := range []struct{ label, message string }{
		{"账本/见证", v.DataError}, {"整机保护", v.GuardError}, {"当前采集", v.StatsError}, {"证书", v.CertificateError},
	} {
		if item.message != "" {
			fmt.Fprintf(&b, "诊断 %s：%q\n", item.label, item.message)
		}
	}
	if v.Meta.Reason != "" {
		fmt.Fprintf(&b, "核验记录：%q\n", v.Meta.Reason)
	}
	if len(v.Gaps) > 0 {
		fmt.Fprintf(&b, "最近计量缺口/核验记录：%d 条（详见 status JSON 或用量页，不重复补账）\n", len(v.Gaps))
	}
	fmt.Fprintln(&b, "\n只读；GiB = 2³⁰ 字节。补账扣个人额度，不等于实际访问或 GCP 账单。")
	fmt.Fprintln(&b, "在线数是连接实例；踢出已下发不等于立即离线。服务端就绪不等于公网访问已验证。")
	_, err := io.WriteString(w, b.String())
	return err
}

func humanTime(n int64, nanos bool) string {
	if n <= 0 {
		return "尚无可信记录"
	}
	t := time.Unix(n, 0)
	if nanos {
		t = time.Unix(0, n)
	}
	return t.UTC().Format(time.RFC3339)
}
