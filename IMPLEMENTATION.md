# Google Cloud 免费层代理节点最终维护手册

更新时间：2026-10-22

这份手册是当前项目的维护口径：Google Cloud 免费层 VM 用于受限代理，不做全局代理，不走
Cloudflare 小黄云、Argo、WARP、CDN 回源。

> **当前状态：** 官方 HY2 主用 + 本地账户系统的代码已经实现，并通过本地单元测试与固定版本
> 官方核心的回环集成测试；**尚未在真实 VM 上部署或迁移**。迁移前生产仍是 Reality 主力，
> 旧 sing-box HY2 只是试验入口。本文的 “HY2 主用账户系统（实现已就绪，迁移需授权）” 一节给出迁移步骤，但每一步都需要明确授权和维护窗口。

## 当前维护基线

| 项目 | 当前值 |
| --- | --- |
| Google Cloud project | `YOUR_PROJECT_ID` |
| VM name | `YOUR_VM_NAME` |
| Zone | `YOUR_ZONE` |
| 迁移前生产主力 | `VLESS Reality Vision` |
| Reality 端口 | `47370/tcp` |
| Reality SNI | `apple.com` |
| VLESS encryption | `none` |
| VLESS flow | `xtls-rprx-vision` |
| HY2 目标主用 | 官方 Hysteria 2 独立服务，`443/udp` |
| 备用证书节点 | `VLESS TCP+TLS`，默认 `443/tcp`，冲突时回退 `8443/tcp` |
| 账户控制 | `hy2-accountd` + SQLite + root-only `hy2-admin` + 只读用量页 |
| 流量保护 | `gcp-egress-guard`，默认 180 GiB/月，覆盖 `sing-box` 与 `hysteria-server` |

继续保持禁用：

- `WARP-plus-Socks5`
- `Argo` / Cloudflare Tunnel
- Cloudflare 小黄云代理
- CDN 回源
- Cloud NAT、Load Balancer、Cloud DNS

当前不维护为主力节点：

- `Vmess-ws`
- 旧 sing-box 内置 HY2 入站（迁移后删除或保持停用；新架构使用官方独立 HY2 + 账户系统）
- `TUIC-v5`
- `Anytls`

这些协议可以留在服务器上，但不要开放对应的 Google Cloud 防火墙端口。

## 成本边界

- 免费 VM 区域只选 `us-west1`、`us-central1`、`us-east1`。
- 机器类型只用 `e2-micro`。
- 启动盘只用 `pd-standard`，大小不超过 `30GB`。
- IPv4 + Standard Tier 方案不是 0 美元：外部 IPv4 本身会产生基础费用。
- Standard Tier 200 GiB/月不要贴满使用，本项目用 `180 GiB` 做 VM 内软熔断。
- 预算提醒只是通知，不是硬上限；真正防止流量继续跑的是 `install-egress-guard.sh`。

## 文件说明

- `create-vm-standard-ipv4.sh`：创建推荐 VM，默认代理端口为 `47370`。
- `open-vless-reality-port.sh`：Cloud Shell 中执行，给 VM 加标签并开放 `tcp:47370`。
- `create-budget-alert.sh`：Cloud Shell 中执行，创建项目级预算提醒。
- `audit-leftovers.sh`：Cloud Shell 中执行，只读检查可能继续计费的残留资源。
- `audit-cost-and-usage.sh`：Cloud Shell 中执行，只读检查账单关联、VM 成本属性、预算和当月发送量。
- `install-egress-guard.sh`：VM 中执行，安装月度出站流量熔断器；可同时保护 `sing-box` 和 `hysteria-server`。
- `egress-guard/runtime.sh`、`egress-guard/generator.sh`：熔断器的运行逻辑和开机 mask generator；不要单独手工修改已安装副本。
- `download-hysteria-release.sh`：本地或 VM 中单次下载并校验固定版本官方 HY2 核心；安装器自身不联网下载。
- `install-hy2-account.sh`：VM 中执行，准备官方 HY2、账户控制、SQLite 和 systemd；不迁移、不启动公网 HY2。
- `sync-hysteria-cert.sh`：VM 中由 root 运行，校验并原子发布 HY2 证书；ACME 续签钩子调用。
- `hy2-account/`：Go 账户控制程序、只读页面、systemd 模板、sing-box 旧 HY2 移除工具和测试。
- `tests/`：本地 egress guard 回归测试与官方核心可行性脚本。
- `add-hysteria2-inbound.sh`：**legacy**，旧 sing-box HY2 入站；安装账户后端后拒绝创建。
- `manage-hysteria2-users.sh`：**legacy**，旧 sing-box HY2 用户管理；安装账户后端后拒绝运行。
- `verify-singbox-runtime.sh`：VM 中执行，只读检查 sing-box、HY2、账户控制、端口、熔断器和证书文件。
- `collect-performance-baseline.sh`：VM 中执行，只读采集慢速诊断的主机、进程和网络基线。
- `issue-acme-cloudflare-token.sh`：可选工具，仅给普通 TLS/WS/网页服务申请证书；Reality 不依赖它。
- `add-vless-tls-inbound.sh`：VM 中执行，在现有 sing-box 配置中新增 `VLESS TCP+TLS` 入站。
- `export-vless-links.sh`：VM 中执行，只读导出 `vless://` 分享链接和本地订阅内容。
- `open-vless-tls-port.sh`：Cloud Shell 中执行，开放新增 TLS 入站端口。
- `open-hysteria2-port.sh`：Cloud Shell 中执行，按显式来源范围开放 HY2 UDP 端口。
- `sing-box-client-ai-routing.fragment.json`：客户端 sing-box 分流模板。
- `sing-box-client-vless-tls.fragment.json`：客户端 VLESS TLS 节点模板。
- `sing-box-client-hysteria2.fragment.json`：客户端 HY2 模板，不允许跳过证书验证。
- `CHOOSING-VLESS-NODES.md`：详细说明 Reality、TLS、端口、DNS、防火墙和分流如何选择。
- `HYSTERIA2-EVALUATION.md`：Reality 慢速诊断和 Hysteria 2 的计费门禁、A/B、验收与回滚方案。

## Cloud Shell 维护命令

先设置固定变量：

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export VM_NAME="YOUR_VM_NAME"
export ZONE="YOUR_ZONE"
export PROXY_PORT="47370"
```

新建 VM 时优先通过 IAP 管理 SSH：

```bash
SSH_SOURCE_RANGE="35.235.240.0/20" bash gcp-free-tier-proxy/create-vm-standard-ipv4.sh
```

脚本对 VM 的 `ssh-restricted` 标签创建优先级 `1000` 的可信来源 allow 和优先级 `1100` 的
其余来源 deny；后者会覆盖 default VPC 中优先级更低的公网 SSH allow，但不会更改其他 VM。
现有 VM 先用 `audit-leftovers.sh` 核实规则并验证 IAP 登录，不自动删除项目级规则。

开放 Reality 端口：

```bash
bash gcp-free-tier-proxy/open-vless-reality-port.sh
```

开放新增 VLESS TLS 端口，默认先公网跑通：

```bash
TLS_PORT=443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

如果 VM 内脚本提示 `443` 被占用并回退到 `8443`，则改用：

```bash
TLS_PORT=8443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

HY2 使用独立的 UDP socket，因此可以与 `443/tcp` 共存。下面是**旧 sing-box 试验入口**的说明；
新架构请使用官方独立 HY2 和 `hy2-admin`，不要同时维护两个账户真相。必须先运行
`audit-cost-and-usage.sh` 并人工核对 Billing Reports，再按朋友的固定出口 IP 限源：

```bash
CONFIRM_COST_AUDIT=yes SOURCE_RANGES="203.0.113.10/32" \
  HY2_PORT=443 bash gcp-free-tier-proxy/open-hysteria2-port.sh
```

如果客户端出口地址动态变化，只能临时用 `SOURCE_RANGES="0.0.0.0/0"`；这不会产生新的
付费云资源，但会扩大被扫描面，应以独立强密码和试验结束后关闭规则控制风险。

跑通后建议收紧来源 IP，例如：

```bash
SOURCE_RANGES="203.0.113.10/32" TLS_PORT=443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

进入 VM：

```bash
gcloud compute ssh "$VM_NAME" \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tunnel-through-iap
```

传维护脚本到 VM，避免复制粘贴时截断：

```bash
gcloud compute scp \
  gcp-free-tier-proxy/install-egress-guard.sh \
  gcp-free-tier-proxy/add-hysteria2-inbound.sh \
  gcp-free-tier-proxy/manage-hysteria2-users.sh \
  gcp-free-tier-proxy/add-vless-tls-inbound.sh \
  gcp-free-tier-proxy/export-vless-links.sh \
  gcp-free-tier-proxy/verify-singbox-runtime.sh \
  "$VM_NAME":/tmp/ \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tunnel-through-iap
```

检查残留计费资源：

```bash
bash gcp-free-tier-proxy/audit-leftovers.sh
```

重点看：

- `Snapshots`：不需要就删除。
- `External IP addresses`：`RESERVED` 且 `USERS` 为空的静态 IP 仍会计费。
- `Persistent disks`：孤儿磁盘可能继续计费。
- `Machine images`、`Forwarding rules`、`Cloud Routers`、`Cloud DNS zones`：本方案不需要。

## VM 内维护命令

进入 VM 后切 root：

```bash
sudo -i
```

安装流量熔断器：

```bash
# 12 只是示例；必须替换成审计结果向上取整后的本月已用 GiB。
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAME=sing-box \
  bash /tmp/install-egress-guard.sh
```

首次安装或状态文件丢失时不允许隐式从零开始。熔断器按 `America/Los_Angeles` 月份计数，
状态/网卡计数器异常时停止并 mask sing-box；跨月默认保持停用，核对 Billing 后手工恢复：

显式提供 `INITIAL_USED_GIB` 会备份旧 `state` 并重建基线，用于修复旧版月中安装的漏计；
已有可信状态、只是重复安装时可以省略该变量。

```bash
sudo /usr/local/sbin/gcp-egress-guard --resume
```

运行只读诊断：

```bash
sudo bash /tmp/verify-singbox-runtime.sh
```

新增 VLESS TCP+TLS 入站：

```bash
sudo bash /tmp/add-vless-tls-inbound.sh
```

如果要明确指定域名或端口：

```bash
sudo TLS_DOMAIN="rr.example.com" TLS_PORT=443 bash /tmp/add-vless-tls-inbound.sh
```

在同一 sing-box 中新增 HY2 入站（**legacy**；要求 sing-box `1.11+`、证书覆盖该域名）：

```bash
sudo HY2_DOMAIN="rr.example.com" HY2_USER_NAME="friend-1" \
  bash /tmp/add-hysteria2-inbound.sh
```

当 `manage-hysteria2-users.sh` 与部署脚本位于同一目录时，会自动安装为
`/usr/local/sbin/sing-box-hy2-user`。之后不要重复填写端口、标签和证书路径；每位朋友使用独立名称：

```bash
sudo sing-box-hy2-user add friend-2
sudo sing-box-hy2-user list
sudo sing-box-hy2-user export friend-2
sudo sing-box-hy2-user remove friend-2
```

`add` 自动识别现有唯一 HY2 入站并保留其他用户；重复添加同名用户只重新导出，不轮换密码。
`remove` 需要交互确认且拒绝删除最后一个用户。每次实际配置变更都会备份、运行
`sing-box check`、重启、检查 UDP 监听并在失败时回滚。新密码不会输出到终端，而是写入：

```text
/root/hysteria2-credentials/<user>.json
/root/hysteria2-credentials/<user>.txt
```

JSON 是 sing-box 出站片段，TXT 是客户端导入链接，权限均为 `0600`。脚本不会配置
`up_mbps`/`down_mbps`，首轮使用 BBR，不启用 Brutal。
默认还会把全局日志级别设为 `warn`，不长期记录常规连接目标；排障期间才使用
`HY2_REDUCE_LOGS=false`。

如果客户端连接 IP、但证书校验使用域名，可分别设置：

```bash
sudo HY2_SERVER_ADDRESS="203.0.113.10" HY2_DOMAIN="rr.example.com" \
  bash /tmp/add-hysteria2-inbound.sh
```

只有确认旧 HY2 从未开放且允许旧客户端失效时，才使用
`HY2_TAG="existing-tag" HY2_REPLACE_USERS=true` 迁移并轮换旧凭据。

手动检查端口：

```bash
sudo ss -lntp | grep 47370
sudo ss -lntp | grep -E ':(443|8443) '
sudo ss -lunp | grep ':443 '
```

手动检查服务：

```bash
sudo systemctl status sing-box --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo journalctl -t gcp-egress-guard -n 50 --no-pager
```

## HY2 主用账户系统（实现已就绪，迁移需授权）

这一节对应 `HYSTERIA2-ACCOUNT-PLAN.md` 的代码实现。它不会自动执行迁移；安装脚本只做本地准备，
不删除 sing-box 配置、不占用 `443/udp`、不启动公网 HY2。

### 组件与边界

```text
客户端 UDP 443
      │
      ├─ hysteria-server（官方固定版本，独立进程）── HTTP 认证/统计 ──┐
      │                                                              │
      └─ sing-box：Reality 47370/tcp、VLESS TLS 443/8443 tcp（备用）  │
                                                                     ▼
                                              hy2-accountd（本机回环监听）
                                                ├─ SQLite 账户/月额度/会话游标
                                                ├─ 只读用量页（SSH 隧道）
                                                └─ root-only Unix socket ← hy2-admin

gcp-egress-guard ── 180 GiB/月时停止并 mask sing-box + hysteria-server
hy2-health.timer ── 共享统计/数据库故障时独立停止并 mask hysteria-server
```

关键边界：

- 账户名只是标签；封禁按稳定 `account_id` 和每条连接独立 `session_id` 执行，不按用户名或来源 IP 连带封禁。
- 新增、改额度、暂停、恢复、撤销、换密码都不重启代理；只有迁移、核心升级或共享故障才可能停止服务。
- 个人额度是 HY2 的软限额（5 秒采样），不等于 Google 结算流量；整机熔断和共享组件故障仍可能影响所有人。
- `owner` 也必须有显式额度，不创建隐藏的无限账户。第一版个人额度只覆盖 HY2；Reality/TLS 只给本人保留，不能把备用凭据发给朋友绕过额度。

### 本地构建和测试

不要在 e2-micro 上编译 Go 程序或前端。使用 Go 1.26+（当前验证使用 1.27.3），在本地 Linux/WSL：

```bash
cd hy2-account
go vet ./...
go test ./...

# 可选但推荐：固定版本官方核心，仅回环、假证书、假账户
bash ../download-hysteria-release.sh amd64 /tmp/hysteria
HY2_BIN=/tmp/hysteria go test -race ./internal/account -run TestPinned -v

CGO_ENABLED=0 go build -trimpath -o /tmp/hy2-account ./cmd/hy2-account
/tmp/hy2-account version
```

产物是静态二进制；SQLite 使用纯 Go 驱动，不要求在 VM 上安装 C 编译器或 SQLite 开发包。

### 迁移前预检（阶段 C）

先确认以下事实，不要根据旧用户名字猜用途：

- VM 的 IP、灰云域名、当前 sing-box 版本、unit 启动参数、配置路径和证书续签方式。
- `443/udp` 是否已被旧 sing-box HY2 占用；旧 HY2 用户和真实使用者的对应关系。
- 本月已用出站、账单、Standard Tier 共享免费额度，以及是否还有其他资源占用。
- `gcp-egress-guard` 当前状态、`INITIAL_USED_GIB` 的审计值、是否已有静态 IP/快照/孤儿磁盘。
- 证书 SAN 是否覆盖 `HY2_DOMAIN`；私钥是否可读但不是全局可读。

只读诊断可先运行：

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo sed -n '1,20p' /var/lib/gcp-egress-guard/state || true
```

不要为了预检重启 sing-box、停 VM、清空生产 state、降低生产额度，或创建真实朋友账户。

### 准备安装（阶段 D 第 1-2 步）

先让全局保护覆盖两个服务。示例中的 `12` 必须替换为审计后向上取整的本月已用 GiB：

```bash
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAMES="sing-box hysteria-server" \
  bash /tmp/install-egress-guard.sh
```

然后准备账户后端。安装器要求显式确认、已构建的静态二进制、已校验的官方核心和明确的额度池，
并且会拒绝在 `gcp-egress-guard.timer` 未运行或 `SERVICE_NAMES` 未同时覆盖 `sing-box` 与
`hysteria-server` 时继续：

```bash
sudo CONFIRM_LOCAL_INSTALL=yes \
  ACCOUNT_BINARY=/tmp/hy2-account \
  HYSTERIA_BINARY=/tmp/hysteria \
  HY2_DOMAIN="rr.example.com" \
  QUOTA_POOL_GIB=140 \
  bash /tmp/install-hy2-account.sh
```

参数说明：

- `QUOTA_POOL_GIB` 必须 `>0` 且 `<=140`；月中上线、其他 VM 用量或账单异常时还要下调。
- `HY2_SERVER_ADDRESS` 可选；默认等于 `HY2_DOMAIN`，需要 IP 连接配域名 SNI 时再分开设置。
- 默认回环端口是 auth `18081`、page `18082`、stats `18083`；安装器会检查本机 TCP 冲突。
- 安装器会创建 `hysteria`/`hy2-account` 系统用户、私密目录、`/etc/hy2-account/config.json`、`/etc/hysteria/server.json` 和四个 systemd 单元。
- 安装器**只启动 `hy2-accountd`**，认证保持关闭；不会启动 HY2，也不会启用健康 timer 和 HY2 服务。

安装完成后，先添加至少一个 `owner` 账户和明确额度（示例）：

```bash
sudo hy2-admin user add owner --quota-gib 80
sudo hy2-admin user add friend-1 --quota-gib 20
sudo hy2-admin user list
```

`add` 只输出私密文件路径，密码和链接不会回显。交付朋友前确认文件权限为 `0600`，并通过可信私密渠道发送。

如果要从旧 sing-box HY2 迁移已有用户，先停止 HY2，再准备一个 root-only 映射文件（不要把真实密码贴进终端历史或聊天）：

```json
{
  "users": [
    {"name": "friend-1", "password": "EXISTING_STRONG_PASSWORD", "quota_bytes": 21474836480}
  ]
}
```

```bash
sudo chmod 0600 /root/hy2-import.json
sudo hy2-admin import /root/hy2-import.json
```

导入不会猜测旧用户名对应哪个人，也不会自动把旧共享用量归到某个账户；归属和保守补账必须人工确认。旧密码若少于 24 字节或无法确认归属，应改用 `user add` 生成新凭据并让本人重新导入链接。

### 释放旧 UDP 443（维护窗口内先做）

官方 HY2 和旧 sing-box HY2 不能同时绑定 `443/udp`。必须先释放旧入口，再启动新服务；顺序不能反。

1. 先记录旧 sing-box 配置路径和时间戳备份，并确认已通知旧 HY2 使用者。
2. 停止 sing-box，删除旧 HY2 入站，校验后再启动，只保留 Reality/TLS：

```bash
CONFIG=/etc/s-box/sb.json   # 替换为实际路径
sudo cp -a "$CONFIG" "${CONFIG}.bak.$(date -u +%Y%m%d-%H%M%S)"
sudo systemctl stop sing-box
sudo python3 /tmp/hy2-account/tools/remove_singbox_hy2.py "$CONFIG"
sudo sing-box check -c "$CONFIG"
sudo systemctl start sing-box
```

该工具只删除它确认属于旧 HY2 的安全规则；发现其他路由仍引用旧 tag 时会拒绝写入，要求人工复核。`sing-box check` 或删除工具失败时立即恢复备份并 `systemctl start sing-box`，不要启动新 HY2。

3. 确认 UDP 443 已释放，Reality/TLS 已恢复：

```bash
sudo ss -lunp | grep ':443 ' || echo 'UDP 443 is free'
sudo ss -lntp | grep -E ':(47370|443|8443) '
```

这一步会短暂中断 Reality/TLS，是迁移的一部分。不要用旧客户端自动重连掩盖迁移窗口。

### 证书、基线与首次启动

官方核心不接受未经核验的空基线启动。`system arm` 需要：

1. 官方 `hysteria-server` 处于 inactive/failed（旧 sing-box HY2 不能占用 UDP 443）；
2. 整机保护健康且同时覆盖两个服务；
3. 一个 root-only 的逐账户保守补账 JSON，显式覆盖每个账户（包括禁用/撤销账户，零也必须写）。

先校验并发布现有证书。默认读取 `/root/ygkkkca/fullchain.cer` 与 `private.key`：

```bash
sudo /usr/local/sbin/sync-hysteria-cert
```

创建示例基线文件（权限 `0600`；这里假设是全新安装且没有旧的 HY2 个人历史）：

```bash
sudo install -m 0600 /dev/null /root/hy2-arm.json
sudo tee /root/hy2-arm.json >/dev/null <<'JSON'
{"owner": 0, "friend-1": 0}
JSON
sudo hy2-admin system arm \
  --charges-file /root/hy2-arm.json \
  --reason "first verified HY2 baseline; historical per-user usage unavailable" \
  --accept-gap
```

`--accept-gap` 不是“忽略缺口”的普通开关；它会记录审计并在页面显示个人统计起始时间。若已有旧 HY2 用量但无法按人归属，必须按保守估计填写，而不是填零。

随后启动健康 timer 和官方 HY2，等待一次采样后激活认证：

```bash
sudo systemctl enable --now hy2-health.timer
sudo systemctl start hysteria-server.service
# 等待 5-10 秒，确认账号页/状态显示 core_live 且采样新鲜
sudo hy2-admin status
sudo hy2-admin system activate
```

如果 `activate` 报 “fresh verified baseline required”，先检查 `hy2-admin status` 中的 `core_live`、`last_sample`、`guard` 和 `stats_error`；不要反复重启核心。若 `hysteria-server` 因多次启动失败进入 start-limit，先查看日志，必要时只执行 `systemctl reset-failed hysteria-server` 清除计数，再重新走 `system arm`/启动/激活；不要把 `reset-failed` 当成绕过基线检查。

### 切换后的验证

确认官方 HY2 已监听 UDP 443，然后执行：

```bash
sudo ss -lunp | grep ':443 '
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo systemctl status hysteria-server hy2-accountd --no-pager
sudo systemctl status gcp-egress-guard.timer hy2-health.timer --no-pager
sudo bash /tmp/verify-singbox-runtime.sh
sudo hy2-admin status
```

用真实客户端验证：朋友至少两台设备、你的长连接；封禁朋友后确认其新认证被拒绝、存量会话按状态下发，**你的同一条连接不能中断**。不要用客户端自动重连掩盖中断。

### 日常账户操作

```bash
sudo hy2-admin user add friend-2 --quota-gib 20
sudo hy2-admin user quota friend-2 --gib 30
sudo hy2-admin user disable friend-2
sudo hy2-admin user enable friend-2
sudo hy2-admin user retry-ban friend-2
sudo hy2-admin user revoke friend-2
sudo hy2-admin user export friend-2
sudo hy2-admin system backup
```

规则：

- `quota` 增加额度不会清空已用量，也不会自动复活已被封禁的旧会话；客户端重新认证后会使用新额度。降低到已用量以下会触发该账户封禁。
- `disable` 是手工暂停，`revoke` 是永久墓碑；两者都不跨月自动恢复。
- 只有“仅因个人额度耗尽”的账户可以在新月、全局健康且未熔断时重新认证；手工暂停/撤销不会自动恢复。
- `retry-ban` 只重置目标账户的有限重试预算，不重启服务、不清除禁用状态。
- `system arm`、`system activate`、`system prepare-stop` 是维护操作，不是普通用户操作；不要用它们给个人解封。

### 用量页

页面只监听 `127.0.0.1:18082`，需要 Basic Auth，只读，不展示密码、链接、目标站或原始配置：

```bash
ssh -N -L 18082:127.0.0.1:18082 USER@VM
# 浏览器打开 http://127.0.0.1:18082
# 首次凭据：/root/hy2-credentials/page-login.txt
```

页面同时展示整机出站、HY2 合计、每账户上传/下载/合计/剩余、在线连接、封禁任务状态、统计新鲜度、证书到期和缺口。在线数是连接实例，不是物理设备数。

### 共享故障、总量熔断和回滚

| 情况 | 行为 |
| --- | --- |
| 单人超额/禁用/撤销 | 只拒绝该人新认证并封该人的会话；不重启全服 |
| 单人 `/kick` 超时 | 显示未完成，最多 3 次有限重试；用 `retry-ban` 人工重试，禁止全服重启兜底 |
| 统计持续过期、数据库/控制服务故障 | `hy2-health.timer` 独立停止并 mask HY2；页面本身失败不触发停服 |
| 整机达到 180 GiB 或状态不可信 | `gcp-egress-guard` 停止并 mask 两个服务 |
| 核心异常重启/计数回退/恢复旧备份 | 保持认证关闭并记录缺口，显式核验/补账后才可恢复 |

整机熔断后的恢复顺序：

```bash
sudo /usr/local/sbin/gcp-egress-guard --resume --no-start
# 核对账户/月账本、证书和共享健康状态，然后重新走基线
sudo hy2-admin system arm --charges-file /root/hy2-arm.json --reason "post-guard review" --accept-gap
sudo systemctl start hysteria-server
sudo hy2-admin system activate
```

`--resume` 只清整机 block，不会替个人账户解封，也不会解除 `hy2-health` 的共享故障锁。

回滚原则：

- 停止 `hysteria-server`，保留 SQLite、审计、禁用状态和整机累计；不要删除数据库来“重置”。
- 默认回到已经验证的 Reality/TLS 备用状态；HY2 保持关闭。
- **不要自动恢复旧共享 HY2 入口**，也不要覆盖旧配置来绕过个人额度；若必须恢复，先明确授权并核对所有账户的禁用/额度状态。
- 回滚后再次检查两个受保护服务、mask、启动门禁和证书钩子。

### 当前限制

- 尚未在真实 VM、真实证书和真实客户端上完成本手册的迁移验收；本地验证不能替代 VM 现场验证。
- VM 重启后 HY2 不会自动恢复：内存统计已丢失，`health gate` 和持久状态会保持关闭，必须重新核对 guard/账本后再执行 `system arm`、启动核心和 `system activate`。这是有意的 fail-closed 行为，不是自动重连。
- 个人额度是采样软限额，存在 5 秒采样、`/kick` 在途和连接关闭延迟；不是字节级硬截断。
- `HY2` 个人流量不等于 Google 结算流量；整机熔断是保守的 VM 发送字节近似值，也不是财务硬上限。
- 第一版不做速度限制、Brutal、端口跳跃、公网管理页、朋友自助后台、自动核心升级或外部监控。

## 客户端配置

### 查看分享链接和订阅内容

本项目没有部署订阅面板，也不建议为了订阅链接额外暴露一个 HTTP 服务。实际维护时使用
`export-vless-links.sh` 在 VM 本机只读解析 sing-box 配置，打印可导入客户端的分享链接。

Reality 节点需要客户端 public key。服务端配置里通常只有 private key，所以需要从初次部署输出或
原客户端配置中取回 `Reality public key`：

```bash
sudo REALITY_PUBLIC_KEY="YOUR_REALITY_PUBLIC_KEY" \
  REALITY_ADDRESS="YOUR_GRAY_CLOUD_DOMAIN_OR_VM_IP" \
  bash /tmp/export-vless-links.sh
```

如果只导出 VLESS TCP+TLS 备用节点：

```bash
sudo TLS_ADDRESS="rr.example.com" bash /tmp/export-vless-links.sh
```

输出分两段：

- `VLESS share links`：直接复制 `vless://...` 导入 v2rayN / 类似客户端。
- `Base64 subscription content`：订阅内容本体，不是可远程访问的订阅 URL。

不要把输出里的 UUID、Reality public key、short id 发到聊天、Issue 或 Git 提交中。

v2rayN / 类似客户端手工填写时，VLESS Reality 备用节点应保持：

| 字段 | 值 |
| --- | --- |
| 地址 | VM IPv4 或灰云域名 |
| 端口 | `47370` |
| 用户 ID | sing-box-yg 输出的 UUID |
| 流控 | `xtls-rprx-vision` |
| 加密方式 | `none` |
| 传输协议 | `tcp` |
| 伪装类型 | `none` |
| host/path | 留空 |
| TLS | `reality` |
| SNI | `apple.com` |
| Fingerprint | `chrome` |
| PublicKey | sing-box-yg 输出的 Reality public key |
| ShortId | sing-box-yg 输出的 short id |
| Mux / multiplex | 不启用 |

`encryption=none` 是正确的。VLESS 协议层不额外加密，真正的安全层是
Reality + Vision。

HY2 主用入口的手工字段：

| 字段 | 值 |
| --- | --- |
| 类型 | `hysteria2` |
| 地址 | 灰云域名或 VM IPv4 |
| 端口 | `443` |
| 密码 | 由 `hy2-admin` 导出，不在文档或聊天中出现 |
| SNI | 证书域名 |
| 证书校验 | 开启（`insecure: false`） |
| 网络 | `tcp`（底层仍走 UDP 443） |
| up/down | 留空，不使用 Brutal |

每个账户使用自己的链接，不要把备用 Reality/TLS 凭据发给朋友绕过个人额度。

客户端只把 AI 域名走 `ai-proxy`，其他流量默认 `direct`。不要把系统全局流量、视频、
下载、系统更新都压到这个 Google Cloud 节点上。

新增 VLESS TLS 节点在 v2rayN 中手工填写时：

| 字段 | 值 |
| --- | --- |
| 地址 | 证书对应的灰云域名 |
| 端口 | `443`；如果脚本回退则用 `8443` |
| 用户 ID | `add-vless-tls-inbound.sh` 输出的新 UUID |
| 流控 | 留空 |
| 加密方式 | `none` |
| 传输协议 | `tcp` |
| TLS | `tls` |
| SNI | 证书域名 |
| Fingerprint | `chrome` |
| PublicKey / ShortId | 留空 |
| Mux / multiplex | 不启用 |

## 测速 403 的判断

v2rayN 速度测试显示：

```text
The remote server returned an error: (403) Forbidden
```

通常不等于节点坏了。只要延迟测试能返回，比如 `164ms`，说明端口和握手大概率已经通。
403 多半是测速目标 URL 拒绝了代理出口、数据中心 IP 或测速请求头。

验收优先级：

1. 客户端能测出延迟。
2. VM 上 `ss -lntp | grep 47370` 显示监听。
3. Google Cloud 防火墙开放 `tcp:47370`。
4. 浏览器或 AI 客户端实际访问目标 AI 服务可用。

不要把“速度测试 403”当作唯一故障结论。

## 证书说明

VLESS Reality 不使用 `/root/ygkkkca/cert.crt` 和 `/root/ygkkkca/private.key`。
Reality 使用的是：

- 服务端 Reality private key
- 客户端 Reality public key
- short id
- SNI `apple.com`

所以 Let’s Encrypt 证书申请成功与否，不影响 Reality 备用节点。安装账户后端后，
`issue-acme-cloudflare-token.sh` 的默认续签动作会先调用 root-only 的
`/usr/local/sbin/sync-hysteria-cert`，校验 SAN、有效期和证书/私钥匹配后原子发布到：

```text
/etc/hysteria/certs/current/fullchain.cer
/etc/hysteria/certs/current/private.key
```

官方 HY2 每次 TLS 握手读取当前证书，因此证书轮换不需要重启 HY2；sing-box 的 VLESS TLS 仍会
通过 `systemctl try-restart sing-box` 读取更新后的证书。不要放宽 `/root` 或私钥权限，也不要把
真实私钥复制进仓库。

新增的 VLESS TLS 节点会读取：

- `/root/ygkkkca/fullchain.cer`，不存在时回退 `/root/ygkkkca/cert.crt`
- `/root/ygkkkca/private.key`

证书域名应使用 Cloudflare 灰云 DNS 直接指向 VM IPv4。不要为这个方案开启小黄云、
Argo、WARP 或 CDN 回源。

## 官方来源

- Google Cloud 免费层：<https://docs.cloud.google.com/free/docs/free-cloud-features>
- VPC 网络价格与外部 IP 价格：<https://cloud.google.com/vpc/network-pricing>
- 静态外部 IP：<https://cloud.google.com/compute/docs/ip-addresses/configure-static-external-ip-address>
- Persistent Disk / Snapshot 价格：<https://cloud.google.com/compute/disks-image-pricing>
- 预算提醒不会形成硬上限：<https://docs.cloud.google.com/billing/docs/how-to/disable-billing-with-notifications>
