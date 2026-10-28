# AGENTS.md

本文件给之后维护这个目录的 AI/自动化代理使用。目标是让后续维护不会偏离当前收敛方案。

## Project Intent

这是一个 Google Cloud 免费层代理节点维护包，目标结构是官方 Hysteria 2 主用、Reality/TLS 备用。

当前目标：

- 低成本。
- 低复杂度。
- 明确出站流量边界。
- 主力服务 AI 相关访问。
- 保留可恢复、可审计的脚本和文档。

不要把它扩展成通用全局代理平台、CDN 回源方案、复杂多协议面板，除非用户明确要求并重新评估成本。

## Current Architecture

> **最新成功状态（2026-10-28 05:26 UTC）：schema2兼容性候选已部署、迁移与恢复成功，05:28只读复核通过。** 用户明确确认本次条件性保守补记及费用/共享额度无异常后，原recovery目录仅执行一次；owner新增1073811632 bytes、朋友0，owner当时剩27.080633 GiB，原历史补账保留。CLI/accountd v2、protocol1/schema2、二进制 `c6b418ef…02208`；认证/统计/核心健康，Reality PID未变，两个timer active，额度120/30/150、guard150/180不变，配置和secrets字节未变。旧累计JSON已恢复语义，活跃限制用独立字段。**不得重跑迁移、部署、arm或本次补账；已有原attempt和recovered回执。** 备份/阶段意图/日志位于 `/root/hy2-session-compat-v2-20261028T051040Z/`，恢复回执仍在 `/root/hy2-reclaim-20261027/recovery/attempt/`。原客户端访问未验证，详见 `STAGE-A-ACCEPTANCE.md` 第25节。下文为本次执行前历史记录，不是待执行清单。

> 部署准备检查点（2026-10-28 05:10–05:15 UTC）：用户授权部署/迁移/恢复及最后Git同步后，费用属性/原账本只读核对已做，VM持久保存 `/root/hy2-session-compat-v2-20261028T051040Z/checkpoint-0001.json`。**尚未部署、迁移、arm或恢复**；候选owner1073811632/朋友0 bytes是未接受的条件性建议，原review仍null。所列预算无Pub/Sub、同账单有2项目且另一项目当前无VM，仍不能证明当月实际费用与历史共享额度。等待一次集中确认，窗口05:40:40 UTC到期、预留5分钟收尾；过期不继续。详见 `STAGE-A-ACCEPTANCE.md` 第24节，不自动写0/重放旧增量，不重跑phase1/phase2。

> 最新只读现场核对（2026-10-28 05:05 UTC，用户明确允许直接连接）：单次本地 Windows gcloud/IAP SSH、严格主机校验通过。HY2 inactive/masked、accountd/Reality active、两个保护 timer active；schema1，witness一致，owner保留512条、朋友0，与原停机前累计记录一致；无pull/delta遗留、原recovery/attempt不存在、审核增量仍全部null。guard健康、整机50.737917/180 GiB，owner剩28.080698 GiB，朋友30 GiB；历史补账未变。**本次只读，没有迁移、部署、补账、arm或启动HY2；新schema2候选仍未发布。** 详细记录见 `STAGE-A-ACCEPTANCE.md` 第23节。用户授权直接管理通道只限此次核对，不代表允许新预算/资源或默认零补账。

> 上次现场/初验记录（2026-10-27 UTC）：上一轮工具回执显示，14:31 已备份并停止 HY2、替换 accountd 为会话回收候选 `50921535…879f208`，accountd active、HY2 inactive/masked，Reality 和两个保护 timer active；尚未恢复或提交本次补账。**本轮独立本地验收未通过，禁止执行已上传的 `/tmp/phase2.sh`、直接 arm/activate 或把该候选当作验收合格版本。** 3 个新增故障/延迟边界回归失败，真实固定核心集成测试未执行；详见 `STAGE-A-ACCEPTANCE.md` 第20节。本轮未连接 VM，这不是当前远端状态的再次确认。保留 `/root/hy2-reclaim-20261027/` 及原备份/审核材料；后续先核对 attempt 和账本，不重跑 phase1，不猜 0/0，不自动回退。下方 r3 为历史成功记录，不代表本次已恢复。
> 兼容性修复（2026-10-28 UTC）：旧 `sessions_generation` 已恢复为累计签发数，独立 `active_sessions` 控制活跃容量；每账户一个事务化汇总行，清理会话不清历史用量。schema 2 须经显式离线 `migrate-sessions --counts-file` 从完整、已审核的 v1 会话记录迁移，正常 serve 不会自动迁移。旧 schema 1 二进制不能直接回退使用该库；不得删表/改 user_version 绕过。CLI/accountd必须配套，协议仍为1。完整含真实核心/race验收通过，详见 `STAGE-A-ACCEPTANCE.md` 第22节。**新版本未部署、未补账、未恢复；最后 VM 记录仍为旧候选停服状态。** `.delta` 不能约束旧程序；升级/回退必须先停核心，新代次恢复仍走原审核助手。预算告警不是封顶，确认可简化但不猜增量。

> 迁移状态（2026-10-23 UTC）：官方 HY2 + 本地账户系统已在授权窗口内完成服务端切换，旧 sing-box HY2 已删除；Reality 保留。官方核心启动、采样、认证激活、VM 本机代理请求及原公网客户端实际访问已通过；**最终只读核验与临时凭据清理通过，本次单账户迁移收尾完成**。后续配套更新与受控恢复已成功，新增补账已提交；用户回贴 `FRIEND_READY`，30 GiB 朋友账户及私密文件准备完成，本次交付未重启服务、未重跑恢复或补账。下载/私下发送、升级后客户端访问及真实多账户封禁隔离仍未确认；故障/重启等不签署全部通过。现场证据与未覆盖项见 `STAGE-A-ACCEPTANCE.md` 第 11、15 节。

> r3 历史状态（2026-10-24 UTC）：用户已在终端接受本次新增字节并回贴 **`R3_RECOVERED / auth=true / healthy=true / core_live=true`**。新版页面、owner120 GiB/朋友30 GiB/池150 GiB已生效，owner当时剩37.857769 GiB；本次仅一次恢复，owner新增保守补账1073744573 bytes、朋友0，原历史与凭据保留。Reality PID未变、保护timer通过助手检查，整机150/180 GiB不变。**不要重跑r3/恢复/arm或重复本次补账，不重启accountd；朋友继续原链接。** 保留review/attempt/recovered回执、审计和私密交付文件；客户端实际访问、真实多人封禁隔离等仍未回贴确认。本次维护服务端收尾，现场证据见 `STAGE-A-ACCEPTANCE.md` 第18–19节。

已保留的本人备用：

```text
VLESS Reality Vision
port: 47370/tcp
SNI: apple.com
flow: xtls-rprx-vision
service: sing-box
```

已切换的 HY2 主用入口（原客户端访问已通过）：

```text
官方 Hysteria 2（固定 v2.13.0）
port: 443/udp
account backend: hy2-accountd
page: 127.0.0.1:18082（SSH 隧道，只读）
admin: root-only /run/hy2-account/admin.sock
```

可选证书备用节点（本次未部署；现场无 443/8443 TCP 监听）：

```text
VLESS TCP+TLS
port: 443/tcp, fallback 8443/tcp
certificate: /root/ygkkkca/fullchain.cer
key: /root/ygkkkca/private.key
service: sing-box
```

旧并行试验入口（已从 sing-box 删除，不得恢复为无额度入口）：

```text
sing-box Hysteria 2
port: 443/udp
```

统一保护：

```text
gcp-egress-guard
warn: 150 GiB/month
limit: 180 GiB/month
services: sing-box hysteria-server
action: stop and mask both
```

独立共享健康联动：

```text
hy2-health.timer
shared statistics/database failure → stop and mask hysteria-server only
```

## Execution Contexts

严格区分 Cloud Shell、本地构建环境、VM 三类上下文。

Cloud Shell only:

- `gcloud compute ...`
- `create-vm-standard-ipv4.sh`
- `open-vless-reality-port.sh`
- `open-vless-tls-port.sh`
- `open-hysteria2-port.sh`
- `create-budget-alert.sh`
- `audit-leftovers.sh`
- `audit-cost-and-usage.sh`

Local build/edit only:

- Edit files and Markdown.
- Build/test the Go account program; do not compile it on the e2-micro.
- Run `download-hysteria-release.sh` only when a bounded, explicit download is acceptable; never auto-download during install.
- Run static checks and fake-systemctl regression tests.
- Do not assume local `gcloud` is authenticated.
- Use Git Bash at `D:\Dev\Git\bin\bash.exe` for `bash -n`; Windows `bash.exe` may point to WSL and fail if no distro is installed.

VM only:

- `install-egress-guard.sh`
- `egress-guard/runtime.sh`, `egress-guard/generator.sh`
- `install-hy2-account.sh`
- `download-hysteria-release.sh` only if local transfer is not possible and the download is explicitly scoped
- `sync-hysteria-cert.sh`
- `hy2-account/tools/remove_singbox_hy2.py`
- `hy2-admin` / `hy2-account` runtime commands
- `recover-hy2.py` / `hy2-recover`（manual reviewed recovery only; matching updated CLI/accountd required）
- `issue-acme-cloudflare-token.sh`
- `add-vless-tls-inbound.sh`
- `add-hysteria2-inbound.sh`（legacy only）
- `manage-hysteria2-users.sh`（legacy only）
- `export-vless-links.sh`
- `verify-singbox-runtime.sh`
- `collect-performance-baseline.sh`
- `systemctl`, `ss`, `/etc/s-box/sb.json`, `/etc/sing-box/config.json`, `/etc/hysteria/server.json`, `/var/lib/hy2-account/`

## Hard Guardrails

- Do not replace the existing Reality node unless the user explicitly asks.
- Do not remove `gcp-egress-guard`.
- Do not deploy or migrate HY2 without a separately authorized maintenance window and a verified billing/shared-allowance audit.
- Do not install the HY2 account backend while leaving `hysteria-server` outside the guard's `SERVICE_NAMES`.
- Do not restore an unquotaed legacy HY2 entry after migration, and do not run both legacy `sing-box-hy2-user` and `hy2-admin` as competing account sources.
- Do not use a full service restart or stop as a fallback for a single-account `/kick` failure; keep the account disabled and retry within the bounded budget.
- For read-only guard diagnostics, read `/run/gcp-egress-guard/status.json`; `gcp-egress-guard --status` persists counters and may stop/mask on failure. `hy2-account health run` is also service-affecting; do not present it as a read-only probe.
- `system arm` charges are additive byte increments, not target totals. A CLI error/timeout does not prove a mutation failed; inspect status/audit before retrying, never duplicate the initial 34 GiB backfill, the later 50584038211-byte charge, or the r3 1073744573-byte charge. Restarting accountd can stop HY2 via BindsTo; recovery needs an authorized window, not an ordinary restart.
- Use `hy2-recover` only with matching recovery-protocol CLI/accountd after a separately authorized paired update; old in-flight health processes must also have exited. The helper persists `attempt/` before arm. Never delete/reuse an attempt or copy old charges to bypass its stop; even an empty attempt means reconcile first.
- Do not infer recovery charges from `clean`/`unplanned`, a nominal 5-second sampling period, successful prepare-stop, or revoked status. Every account starts at null and requires explicitly reviewed additional bytes, including disabled/revoked accounts; zero needs evidence too.
- Keep root `health run` and `system arm` serialized by the same persistent-inode control lock across decision, mutation and latch/reload. Never unlink lock files or stop protection timers to bypass contention; `health gate` must remain read-only and lock-free.
- Keep page and `status --human` presentation read-only; default `status` JSON and exact counters must remain compatible. Unknown ledger/guard data must not become a zero balance or an old green page. Separate sampled usage, conservative adjustments, account-pool allocation and VM egress; do not expose credentials, add browser scripts/CDNs, or imply client access/ban isolation from local readiness.
- Do not increase an account pool or quota without explicit approval. The historical 140 GiB pool is a revisitable conservative allocation, not a GCP limit; code may accept explicit pools up to 180 GiB, but the approved current target is 150 GiB (owner 120, friend 30). Keep the independent 150/180 GiB VM guard unchanged; pool edits require a matched CLI/accountd update and authorized maintenance, never reinstall/init or erase usage.
- Do not switch traffic to global proxy by default.
- Do not enable Cloudflare orange cloud, Argo, WARP, CDN, Load Balancer, Cloud NAT, or Cloud DNS as part of routine maintenance.
- Do not add UDP-heavy protocols by default.
- Do not commit or print secrets:
  - Cloudflare API Token
  - Global API Key
  - UUIDs from the real server
  - Reality private key
  - Certificate private key
  - Google Cloud credentials
  - HY2 account passwords, page password, auth/stats keys, database contents
- Do not use destructive GCP commands unless the user explicitly asks and the target resources are listed first.

## Editing Rules

- Keep Markdown in UTF-8.
- Keep shell scripts POSIX-ish Bash with `set -euo pipefail` when appropriate.
- Prefer idempotent scripts:
  - Detect existing firewall rules.
  - Detect existing config entries by tag.
  - Update rather than duplicate.
- For JSON config mutation, use structured parsing, not grep/sed rewrites.
- Before changing sing-box runtime config, make a timestamped backup.
- After changing sing-box config, run:

```bash
sing-box check -c <config-path>
```

If the check fails, restore the last verified safe configuration. During/after HY2 migration, that must be the checked non-HY2 fallback, never a pre-migration backup that would revive unquotaed legacy HY2.
- For the HY2 account program, keep tests deterministic and use only loopback fixtures; never point tests at production, real certificates, real passwords, or systemd.
- Run `gofmt` and `go vet` after Go changes. A real-core test must use the checksum-pinned official binary and must not download or start anything during `go test` unless `HY2_BIN` is explicitly supplied.
- Certificate publication must validate the staged pair with Go/HY2 SAN-only hostname rules, not CN fallback or source-only checks; require notBefore <= now and >24h remaining before switching `current`.
- The legacy `add-hysteria2-inbound.sh` domain check must parse typed leaf DNS SANs with Python cryptography and use exact/full-left-label wildcard matching. Do not use OpenSSL text, `DNS:` substring, partial wildcards or CN fallback. Missing dependencies fail closed; never auto-install them.
- Do not weaken the guard, auth, quota, witness, or ACL regression tests to make a change pass.

## File Responsibilities

- `README.md`: human entrypoint and quick start.
- `IMPLEMENTATION.md`: operational runbook, including HY2 account build/preflight/migration/rollback and the canonical daily-maintenance/troubleshooting/recovery procedures. Keep commands and safety boundaries here; README links to them.
- `CHOOSING-VLESS-NODES.md`: decision guide (HY2 primary, Reality/TLS backup).
- `HYSTERIA2-EVALUATION.md`: historical cost gate, diagnosis, A/B record, and rollback guide; legacy deployment paths are marked.
- `HYSTERIA2-ACCOUNT-PLAN.md`: account/isolation design plus implementation status and validation record.
- `STAGE-A-ACCEPTANCE.md`: local stage acceptance record, exclusions, pre-launch checklist, authorized production evidence (§11), and post-migration local acceptance/review findings (§12).
- `AGENTS.md`: AI maintenance rules.
- `create-vm-standard-ipv4.sh`: VM creation baseline.
- `open-vless-reality-port.sh`: Reality firewall.
- `open-vless-tls-port.sh`: TLS firewall.
- `open-hysteria2-port.sh`: scoped HY2 UDP firewall rule.
- `audit-cost-and-usage.sh`: read-only billing-link, VM cost-property, budget, and month-to-date sent-byte audit.
- `install-egress-guard.sh`: monthly egress stop/mask guard for an explicit service list.
- `egress-guard/runtime.sh`: runtime accounting/stop/mask/permit/resume logic.
- `egress-guard/generator.sh`: early boot/daemon-reload mask generator.
- `download-hysteria-release.sh`: bounded pinned official core download and checksum verification.
- `install-hy2-account.sh`: preparation-only installer for HY2/accountd; never performs migration. Its post-start readiness gate must bound requests/time, confirm stable accountd + readable held initial status, and never restart/reinstall on failure.
- `recover-hy2.py`: manual reviewed recovery (`hy2-recover`), private one-attempt receipts, bounded read-only polling; never invent charges or retry uncertain writes.
- `sync-hysteria-cert.sh`: root-only validated certificate publication for HY2.
- `hy2-account/`: Go controller, page, systemd templates, migration tool, tests.
- `tests/`: Python guard regressions and standalone pinned-core feasibility gate.
- `issue-acme-cloudflare-token.sh`: certificate issuance with Cloudflare token and HY2 sync hook.
- `add-vless-tls-inbound.sh`: append/update VLESS TLS inbound in sing-box config.
- `add-hysteria2-inbound.sh`: **legacy** old sing-box HY2 inlet; blocked after account backend installation.
- `manage-hysteria2-users.sh`: **legacy** old sing-box HY2 user management; blocked after account backend installation.
- `export-vless-links.sh`: read-only VM helper that prints VLESS client share links; never persist real output.
- `verify-singbox-runtime.sh`: read-only runtime diagnostics for sing-box, HY2, accountd, guard and certificates.
- `collect-performance-baseline.sh`: read-only host, process, and network performance snapshot; never print credentials or full configs.
- `sing-box-client-*.json`: client fragments only; never put real secrets in them.

## Validation Checklist

After editing scripts locally:

```powershell
& 'D:\Dev\Git\bin\bash.exe' -n *.sh
```

After editing JSON templates:

```powershell
python -m json.tool sing-box-client-ai-routing.fragment.json
python -m json.tool sing-box-client-vless-tls.fragment.json
python -m json.tool sing-box-client-hysteria2.fragment.json
```

After editing Markdown:

```powershell
Get-Content -Encoding UTF8 -TotalCount 20 README.md
Get-Content -Encoding UTF8 -TotalCount 20 IMPLEMENTATION.md
Get-Content -Encoding UTF8 -TotalCount 20 CHOOSING-VLESS-NODES.md
Get-Content -Encoding UTF8 -TotalCount 20 HYSTERIA2-EVALUATION.md
Get-Content -Encoding UTF8 -TotalCount 20 HYSTERIA2-ACCOUNT-PLAN.md
```

After editing the Go account program:

```bash
cd hy2-account
gofmt -l .
go vet ./...
go test ./...
# Optional real-core gate; requires explicit HY2_BIN and is loopback-only
HY2_BIN=/path/to/verified/hysteria go test -race ./internal/account -run TestPinned -v
```

After editing the Python guard tests or standalone core gate:

```bash
python3 -m unittest -v tests.test_egress_guard
python3 -m unittest -v tests.test_remove_singbox_hy2
# Recovery fixtures: temporary files/locks and fake commands/clock only.
python3 -m unittest -v tests.test_recover_hy2
# Namespace-root only (unshare -r); throwaway certificates, fake systemctl
# Certificate tests build the local Go verifier once; Go 1.26+ and cached
# dependencies are required (GOPROXY=off / GOTOOLCHAIN=local; no downloads).
python3 -m unittest -v tests.test_sync_hysteria_cert tests.test_install_hy2_account
# Legacy leaf/SAN checks only; no namespace/root/Go/systemd needed.
# Requires explicitly provisioned Python cryptography; never downloads it.
python3 -m unittest -v tests.test_legacy_hy2_certificate
HY2_BIN=/path/to/verified/hysteria python3 tests/hy2_core_isolation.py
```

On the VM after runtime changes:

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo ss -lunp | grep ':443 '
sudo systemctl status sing-box --no-pager
sudo systemctl status hysteria-server --no-pager
sudo systemctl status hy2-accountd --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo systemctl status hy2-health.timer --no-pager
sudo hy2-admin status
sudo journalctl -u sing-box -n 80 --no-pager
sudo journalctl -u hysteria-server -n 80 --no-pager
sudo journalctl -u hy2-accountd -n 80 --no-pager
sudo journalctl -t gcp-egress-guard -n 50 --no-pager
```

## Troubleshooting Bias

Use this order before changing architecture:

1. Confirm the relevant service (`sing-box` for Reality/TLS, `hysteria-server` for HY2) is active and not masked.
2. Confirm the expected port is listening and owned by the expected process.
3. Confirm GCP firewall allows the port and source range.
4. Confirm the client fields and credentials match the node type/account.
5. Confirm DNS is gray-cloud and points to VM IPv4; confirm certificate SAN for TLS/HY2.
6. For HY2 accounts, check `hy2-admin status` for auth gate, statistics freshness, shared-health latch, and ban task state before changing config.
7. Confirm actual target site behavior.

Do not restart the whole service for a single-account quota/ban issue. Do not treat v2rayN speed test `403 Forbidden` as the only failure signal. It may mean the test URL rejects the exit IP or request pattern while the node itself is fine.

## Cost Bias

Prefer choices that make cost visible and bounded:

- Keep Standard Tier assumptions documented.
- Keep `180 GiB` guard active.
- Avoid unnecessary GCP managed services.
- Avoid leftover static IPs, snapshots, disks, forwarding rules, Cloud Routers, and DNS zones.

When in doubt, run `audit-leftovers.sh` from Cloud Shell before adding new GCP resources.

## Documentation Expectations

Any behavior change should update at least one of:

- `README.md` for quick-start/user-facing changes.
- `IMPLEMENTATION.md` for operational commands and migration/rollback steps.
- `CHOOSING-VLESS-NODES.md` for decision logic.
- `HYSTERIA2-ACCOUNT-PLAN.md` for account/isolation design or implementation-status changes.
- `HYSTERIA2-EVALUATION.md` for historical cost/A/B evidence; do not rewrite it as if the new architecture were already deployed.
- `AGENTS.md` for AI maintenance rules.

Keep examples generic. Use placeholders like `rr.example.com`, `YOUR_UUID`, and `203.0.113.10/32`.
