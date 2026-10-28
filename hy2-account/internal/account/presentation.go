package account

import "fmt"

// Notice is presentation only. It never grants authentication, changes an
// account, or treats a successful local check as proof of client access.
type Notice struct {
	Tone   string
	Title  string
	Detail string
}

// FormatGiB keeps the overview readable without rounding tiny usage to zero.
// Exact byte counts remain available in the unchanged status JSON.
func FormatGiB(n int64) string {
	if n > 0 && n < (GiB+99)/100 {
		return "<0.01"
	}
	return fmt.Sprintf("%.2f", float64(n)/float64(GiB))
}

func (v Status) ConnectionNotice() Notice {
	switch {
	case v.DataError != "":
		return Notice{"danger", "用量未知，需要核验", "账户账本或恢复见证异常，认证应保持关闭。先在 VM 本机核验，不要重装、清库或重复补账。"}
	case v.Guard.Blocked || (v.Guard.Limit > 0 && v.Guard.Used >= v.Guard.Limit):
		return Notice{"danger", "整机保护不放行", "整机已锁定或达到月度出站上限，HY2 与 Reality 都受其约束。先核实账单和累计，不要解除保护试错。"}
	case v.GuardError != "":
		return Notice{"danger", "整机保护状态不可核验", "保护状态缺失、过期或异常；未知余量不会显示成零。先检查 VM 上的保护状态，不要提高上限或绕过门禁。"}
	case v.HealthBlocked:
		return Notice{"danger", "HY2 共享保护已锁定", "统计或共享组件故障留下了保护锁。仅 HY2 受此锁影响；先核对原因与已入账用量，再在授权窗口恢复。"}
	case v.Meta.Review:
		return Notice{"danger", "用量待审核，暂不接入", "计量状态需要人工核验。保留账本与恢复回执，不以重启、清零或重复补账来恢复连接。"}
	case v.CertificateError != "":
		return Notice{"danger", "证书异常，接入待核验", "认证门禁可能仍开启，但证书状态异常；不能只凭 healthy 字段确认客户端可用。请检查下方证书诊断。"}
	case !v.CoreLive:
		return Notice{"danger", "HY2 核心未确认运行", "未确认官方核心进程标识存活。先核对运行状态和停机原因，不直接重启或重新安装。"}
	case !v.Healthy:
		return Notice{"danger", "统计或运行状态不可放行", "最近可信采样、核心代次或共享保护未满足安全门槛。先看运行诊断，不能把旧用量当作实时余额。"}
	case v.Meta.Hold || !v.AuthAllowed:
		return Notice{"warning", "HY2 暂不接受新连接", "认证门禁关闭，可能处于计划停机或受控恢复阶段。先核对 VM 状态，不重复启动或补账。"}
	case v.StatsError != "":
		return Notice{"warning", "采集有异常，留意最新采样", "上次可信采样仍在安全窗口，但当前采集出现异常，用量可能滞后。请看运行诊断；这不表示所有连接已停止。"}
	case v.Guard.Warn > 0 && v.Guard.Used >= v.Guard.Warn:
		return Notice{"warning", "整机出站已到警戒线", "尚未到停止上限。请控制总流量并核对账单账户内其他资源的共享额度；个人有余额也不能绕过整机保护。"}
	default:
		return Notice{"success", "服务端已就绪", "统计新鲜、认证开启。账户仍受个人额度、连接容量和速率限制；公网访问需要客户端另行确认。"}
	}
}

func (v Status) UserNotice(u UserView) Notice {
	switch {
	case u.Revoked:
		return Notice{"danger", "已永久撤销", "不能启用或重建来恢复额度；旧连接的踢出进度仍需确认。"}
	case u.Disabled:
		return Notice{"warning", "已暂停", "不接受该账户的新认证；旧连接是否已关闭，以踢出进度和后续采样为准。"}
	case u.Quota <= 0:
		return Notice{"danger", "账户额度不可核验", "未取得明确且大于零的月额度，不能据此判断账户可接入。"}
	case u.Exhausted || u.Used >= u.Quota:
		return Notice{"danger", "本月额度已用完", "新认证被拒绝，旧连接按账户封禁流程处理；不重启整个代理。"}
	case v.DataError != "" || v.GuardError != "" || v.Guard.Blocked || (v.Guard.Limit > 0 && v.Guard.Used >= v.Guard.Limit) || v.HealthBlocked || v.Meta.Review || v.Meta.Hold || !v.Healthy || !v.CoreLive || !v.AuthAllowed:
		return Notice{"warning", "暂不能接入", "账户自身未暂停，但共享门禁或运行状态不可放行；请先处理顶部提示。"}
	case v.CertificateError != "":
		return Notice{"warning", "接入待核验", "账户有余额且未暂停，但证书异常；需先确认客户端能够正常校验证书。"}
	case u.ActiveSessionCap > 0 && u.ActiveSessions >= u.ActiveSessionCap:
		return Notice{"warning", "新连接容量已满", "活跃连接（含刚认证的连接）达到个人上限，不因此踢出现有连接；离线且持续无活动的普通会话会自动回收，仍在线的空闲连接不会因此删除。这不是物理设备数限制。"}
	case u.Percent >= 80:
		return Notice{"warning", "余额偏低", "已使用至少 80% 的月额度；有余额不代表整机还有同等免费出站额度。"}
	default:
		return Notice{"success", "已启用", "有余额且未暂停；实际认证仍受共享保护、连接容量和速率限制。"}
	}
}

func (v Status) Sampled() int64   { return v.Tx + v.Rx }
func (u UserView) Sampled() int64 { return u.Tx + u.Rx }

// Match allocatedPool: retired usage and usage above a lowered quota remain
// reserved. Summing configured quotas alone would overstate reusable capacity.
func (v Status) AllocatedQuota() int64 {
	var total int64
	for _, u := range v.Users {
		reserved := u.Used
		if !u.Revoked {
			reserved = max(u.Quota, u.Used)
		}
		total += reserved
	}
	return total
}

func (v Status) UnallocatedQuota() int64 {
	return max(0, v.QuotaPool-v.AllocatedQuota())
}
