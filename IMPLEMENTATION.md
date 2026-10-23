# Google Cloud 免费层代理节点最终维护手册

更新时间：2026-10-23

这份手册是当前项目的维护口径：Google Cloud 免费层 VM 用于受限代理，不做全局代理，不走
Cloudflare 小黄云、Argo、WARP、CDN 回源。

> **当前状态（2026-10-23 UTC）：** 官方 HY2 v2.13.0 + 本地账户系统已完成授权服务端切换，
> 启动、采样、认证激活和 VM 本机代理请求通过；旧 sing-box HY2 已删除，Reality 保留。
> **原公网客户端实际访问、最终只读核验与临时凭据清理均通过，本次单账户迁移收尾完成**；详细证据与未演练项见 `STAGE-A-ACCEPTANCE.md` 第 11 节。
> 下文迁移命令供经授权的安装/维护使用，不要在已迁移 VM 上从头重跑；任何新切换仍需明确授权和维护窗口。

## 当前维护基线

| 项目 | 当前值 |
| --- | --- |
| Google Cloud project | `YOUR_PROJECT_ID` |
| VM name | `YOUR_VM_NAME` |
| Zone | `YOUR_ZONE` |
| 本人备用 | `VLESS Reality Vision`（已保留） |
| Reality 端口 | `47370/tcp` |
| Reality SNI | `apple.com` |
| VLESS encryption | `none` |
| VLESS flow | `xtls-rprx-vision` |
| HY2 主用 | 官方 Hysteria 2 v2.13.0 独立服务，`443/udp`；原客户端访问已通过 |
| 可选备用证书节点 | `VLESS TCP+TLS`，默认 `443/tcp`，冲突时回退 `8443/tcp`（本次未部署） |
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

传维护脚本到 VM，避免复制粘贴时截断（`install-egress-guard.sh` 依赖同级的 `egress-guard/` 目录，必须一起上传）：

```bash
gcloud compute scp --recurse \
  gcp-free-tier-proxy/install-egress-guard.sh \
  gcp-free-tier-proxy/egress-guard \
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

在同一 sing-box 中新增 HY2 入站（**legacy**；要求 sing-box `1.11+`、证书 DNS SAN 覆盖该域名）：

此旧脚本要求 Python `cryptography`，结构化读取叶证书的 DNS SAN；只接受精确域名或
完整左标签通配，不接受 CN 回退、部分通配或 URI 文本伪装的 `DNS:`。
未设置 `HY2_DOMAIN` 时只推断真实的非通配 DNS SAN。使用前可离线确认依赖：

```bash
python3 -c 'from cryptography import x509'
```

缺失时明确报错，在任何配置备份/写入、凭据创建或服务重启前停止；脚本不会安装依赖。
确需使用此历史路径时，包安装另行按维护权限执行，不要以安装依赖为由恢复迁移后已停用的旧入口。

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

不要在 e2-micro 上编译 Go 程序或前端。使用 Go 1.26+（证书 A3 离线复验使用 1.26.0），在本地 Linux/WSL：

```bash
cd hy2-account
go vet ./...
go test ./...

# 可选但推荐：固定版本官方核心，仅回环、假证书、假账户
bash ../download-hysteria-release.sh amd64 /tmp/hysteria
HY2_BIN=/tmp/hysteria go test -race ./internal/account -run TestPinned -v

CGO_ENABLED=0 go build -trimpath -o /tmp/hy2-account.bin ./cmd/hy2-account
/tmp/hy2-account.bin version
```

产物是静态二进制；SQLite 使用纯 Go 驱动，不要求在 VM 上安装 C 编译器或 SQLite 开发包。

### 上线执行门禁（先只读预检，后授权窗口）

本地验收通过只表示可以准备候选包和进入阶段 C，**不是生产切换批准**。首次上线按以下顺序逐项确认，不执行“一键全部运行”：

| 门禁 | 必须确认的证据 | 未满足时 |
| --- | --- | --- |
| 目标与通道 | `PROJECT_ID`、`VM_NAME`、`ZONE`、`HY2_DOMAIN`、VM 架构、可用的 SSH/IAP 管理通道 | 不上传、不安装，不猜默认目标 |
| 账单与共享额度 | 当月 SKU 费用/抵扣、Standard Tier、计费账户下其他项目/VM 的共享额度占用、可信的当月发送量 | 不以预算提醒或审计脚本退出 0 当作通过；空数据、权限错误、意外费用均暂停 |
| 现场状态与配额 | Reality/TLS 可用、guard 健康、证书/SAN/续签方式、端口归属、旧账户映射；明确首月额度池与个人额度 | 不把示例 `12`、`140`、`80` 或补账 `0` 当作实际值 |
| 授权窗口与停止条件 | 明确日期/时区/时长、允许的 Reality/TLS 中断、回滚截止时间、现场测试流量上限 | 未授权不停止/重启服务、不修改防火墙、不启动公网 HY2 |

只读预检也应限定到已确认的目标、单轮执行；错误或信息不全即停，不自动启用 API、安装软件、扩大资源或重试收费请求。按 Cloud Shell/VM 上下文分别运行已有诊断工具，不从本地假定 `gcloud` 已认证。审计结果不能证明当前计费规则时，先核对官方定价和 Billing，再决定是否继续。

候选包应同时携带源码快照、构建参数、官方核心固定校验值和逐文件 SHA-256 清单。工作区尚有未提交修复时，不能用 `git archive HEAD` 代替当前快照，也不能把候选包冒称为仅包含 `HEAD`；记录基线提交和源码摘要。上传/解包后先核对包摘要及 `SHA256SUMS`，任一不匹配都停止。

维护窗口内先准备证书和现有续签钩子，再切换 UDP 443。不要为上线例行重签/强制续签证书，不要运行 legacy 建入口脚本；新架构不需要 legacy 的 Python `cryptography` 依赖。任何现场门禁失败，保持 HY2 关闭，按下文回滚原则保留 Reality/TLS、账户审计和整机累计，不恢复无个人额度的旧 HY2。

### 上传迁移包

`install-hy2-account.sh` 从自身所在目录读取 `hy2-account/systemd/`、`sync-hysteria-cert.sh` 和 `recover-hy2.py`，迁移步骤还要用到
`hy2-account/tools/`。因此把它们及 guard、诊断脚本按原目录结构放进独立的暂存目录，二进制用 `.bin` 等文件名，避免与
`hy2-account/` 目录重名。下面的上传命令仅在目标与上传范围已确认后，于 **Cloud Shell** 执行；先把本地二进制和已验收源码/候选包上传到 Cloud Shell，不假定旧 checkout 已含未提交修复：

```bash
gcloud compute ssh "$VM_NAME" --zone "$ZONE" --project "$PROJECT_ID" --tunnel-through-iap \
  --command 'install -d -m 0700 /tmp/hy2-release'
gcloud compute scp --recurse \
  gcp-free-tier-proxy/install-hy2-account.sh \
  gcp-free-tier-proxy/sync-hysteria-cert.sh \
  gcp-free-tier-proxy/recover-hy2.py \
  gcp-free-tier-proxy/install-egress-guard.sh \
  gcp-free-tier-proxy/egress-guard \
  gcp-free-tier-proxy/verify-singbox-runtime.sh \
  gcp-free-tier-proxy/collect-performance-baseline.sh \
  gcp-free-tier-proxy/hy2-account \
  /tmp/hy2-account.bin \
  /tmp/hysteria \
  "$VM_NAME":/tmp/hy2-release/ \
  --zone "$ZONE" --project "$PROJECT_ID" --tunnel-through-iap
```

安装器会在 VM 上重新校验官方核心的 SHA-256；不要在 VM 上下载或编译。若使用完整候选归档，解包后也应得到
`/tmp/hy2-release/`，先在该目录运行 `sha256sum -c SHA256SUMS`；不要将新脚本与旧二进制混用。

### 迁移前预检（阶段 C）

先确认以下事实，不要根据旧用户名字猜用途：

- VM 的 IP、灰云域名、当前 sing-box 版本、unit 启动参数、配置路径和证书续签方式。
- `443/udp` 是否已被旧 sing-box HY2 占用；旧 HY2 用户和真实使用者的对应关系。
- 本月已用出站、账单、Standard Tier 共享免费额度，以及是否还有其他资源占用。
- `gcp-egress-guard` 当前状态、`INITIAL_USED_GIB` 的审计值、是否已有静态 IP/快照/孤儿磁盘。
- 证书 SAN 是否覆盖 `HY2_DOMAIN`（不能仅依赖 CN），已生效且剩余有效期大于 24 小时；私钥是否可读但不是全局可读。

只读诊断可先运行：

```bash
sudo bash /tmp/hy2-release/verify-singbox-runtime.sh
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo sed -n '1,20p' /var/lib/gcp-egress-guard/state || true
```

不要为了预检重启 sing-box、停 VM、清空生产 state、降低生产额度，或创建真实朋友账户。

### 准备安装（阶段 D 第 1-2 步）

先让全局保护覆盖两个服务。示例中的 `12` 必须替换为审计后向上取整的本月已用 GiB：

```bash
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAMES="sing-box hysteria-server" \
  bash /tmp/hy2-release/install-egress-guard.sh
```

然后准备账户后端。安装器要求显式确认、已构建的静态二进制、已校验的官方核心和明确的额度池，
并且会拒绝在 `gcp-egress-guard.timer` 未运行或 `SERVICE_NAMES` 未同时覆盖 `sing-box` 与
`hysteria-server` 时继续：

```bash
sudo CONFIRM_LOCAL_INSTALL=yes \
  ACCOUNT_BINARY=/tmp/hy2-release/hy2-account.bin \
  HYSTERIA_BINARY=/tmp/hy2-release/hysteria \
  HY2_DOMAIN="rr.example.com" \
  QUOTA_POOL_GIB=140 \
  bash /tmp/hy2-release/install-hy2-account.sh
```

参数说明：

- `QUOTA_POOL_GIB` 必须 `>0` 且 `<=140`；月中上线、其他 VM 用量或账单异常时还要下调。
- `HY2_SERVER_ADDRESS` 可选；默认等于 `HY2_DOMAIN`，需要 IP 连接配域名 SNI 时再分开设置。
- 默认回环端口是 auth `18081`、page `18082`、stats `18083`；安装器会检查本机 TCP 冲突。
- 安装器会创建 `hysteria`/`hy2-account` 系统用户、私密目录、`/etc/hy2-account/config.json`、`/etc/hysteria/server.json` 和四个 systemd 单元。
- 安装器**只启动 `hy2-accountd`**，认证保持关闭；不会启动 HY2，也不会启用健康 timer 和 HY2 服务。
- `systemctl is-active` 不等于管理 socket 就绪。首次现场安装曾立即报 `accountd socket unavailable`；稍后续跑通过，与启动就绪竞争相符，现场根因未独立复现。**当前仓库安装器已补强**：启动后的就绪阶段总预算 20 秒，每次 systemctl/status 请求最多 2 秒，确认服务 active/running、NRestarts=0、管理接口可读、空账户库且认证处于初始 hold/review 才报告准备完成；成功响应后再次检查服务状态。失败不回显原始响应、不重装/重启/清库，保留现场待核验。此补强未部署到已运行 VM，无需为此重新安装。

安装完成、管理 socket 就绪后，先添加至少一个 `owner` 账户和明确额度（示例）：

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
sudo python3 /tmp/hy2-release/hy2-account/tools/remove_singbox_hy2.py "$CONFIG"
sudo sing-box check -c "$CONFIG"
sudo systemctl start sing-box
```

该工具只删除它确认属于旧 HY2 的安全规则；发现其他路由仍引用旧 tag 时会拒绝写入，要求人工复核。停服前须准备并校验不含 legacy HY2 的备用配置；删除或校验失败时停止迁移，只用该安全备用配置恢复 sing-box，不启动新 HY2，也不把含旧 HY2 的备份恢复上线。

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

`arm` 只有约 45 秒启动窗口。先在有限等待内确认 `/run/hy2-account/health.json` 已发布新鲜的 `bootstrap=true`（账户服务约每 5 秒发布），再启动健康 timer 和官方 HY2，等待一次采样后激活认证；不能把命令返回直接当作健康见证已更新。超时即停，不反复 `arm` 或重复补账：

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
sudo bash /tmp/hy2-release/verify-singbox-runtime.sh
sudo hy2-admin status
```

用真实客户端验证：朋友至少两台设备、你的长连接；封禁朋友后确认其新认证被拒绝、存量会话按状态下发，**你的同一条连接不能中断**。不要用客户端自动重连掩盖中断。

### 日常账户操作

**VM SSH** 执行。先运行 `sudo hy2-admin status` 和 `sudo hy2-admin user list`，再选择一个确实需要的操作；下表不是顺序执行清单。改变额度、封禁、撤销、换密码前须确认目标和授权，不能拿真实 owner 做验收故障注入。

| 目的 | 单条命令（示例账户） | 影响 |
| --- | --- | --- |
| 新增 | `sudo hy2-admin user add friend-2 --quota-gib 20` | 占用已批准的额度池，生成私密凭据文件 |
| 修改额度 | `sudo hy2-admin user quota friend-2 --gib 30` | 改总额度，不是增加 30 GiB；不清空已用量 |
| 暂停 | `sudo hy2-admin user disable friend-2` | 拒绝新认证，并排队处理该账户既有会话 |
| 恢复 | `sudo hy2-admin user enable friend-2` | 仅在全局健康、未超额且未撤销时允许；旧会话不会复活 |
| 重试踢出 | `sudo hy2-admin user retry-ban friend-2` | 人工批准一轮有限重试，不取消禁用 |
| 永久撤销 | `sudo hy2-admin user revoke friend-2` | 不可通过 enable/重建同名账户恢复 |
| 换密码 | `sudo hy2-admin user rotate friend-2` | 旧凭据失效、旧会话排队踢出；须私密交付新文件 |
| 导出当前凭据 | `sudo hy2-admin user export friend-2` | 写 `/root/hy2-credentials/`，不回显密码；不是换密码 |
| 数据库备份 | `sudo hy2-admin system backup` | 创建本机私密一致性快照，保留最近 7 份；不是完整灾备 |

命令超时/返回非零不总等于“未写入”：账户修改可能已落库而踢出或凭据导出失败。先查 `status/list`；`rotate` 后若仅导出失败，核实后用 `export`，不要再次 rotate。新建失败也先确认账户是否已存在，不直接重建。

规则：

- `quota` 增加额度不会清空已用量，也不会自动复活已被封禁的旧会话；客户端重新认证后会使用新额度。降低到已用量以下会触发该账户封禁。
- `disable` 是手工暂停，`revoke` 是永久墓碑；两者都不跨月自动恢复。
- 只有“仅因个人额度耗尽”的账户可以在新月、全局健康且未熔断时重新认证；手工暂停/撤销不会自动恢复。
- `retry-ban` 只重置目标账户的有限重试预算，不重启服务、不清除禁用状态。
- `system arm`、`system activate`、`system prepare-stop` 是维护操作，不是普通用户操作；不要用它们给个人解封。

### 常用命令速查

以下均为 **VM SSH**，按需选用，不要整段顺序执行。`friend-1` 为示例；新增、停用或改额度先确认目标。

| 操作 | 命令 |
| --- | --- |
| 新建 10 GiB 账户 | `sudo hy2-admin user add friend-1 --quota-gib 10` |
| 查看全局健康（原始 JSON） | `sudo hy2-admin status` |
| 中文余额与状态摘要（新版 CLI） | `sudo hy2-admin status --human` |
| 查看各账户用量 | `sudo hy2-admin user list` |
| 观察 60 秒，每 5 秒刷新 | `sudo timeout --foreground 60s watch -n 5 hy2-admin user list` |
| 最近 50 行 HY2/账户日志 | `sudo journalctl -u hysteria-server -u hy2-accountd -n 50 --no-pager` |
| 停用一个账户 | `sudo hy2-admin user disable friend-1` |
| 恢复一个未撤销、未超额账户 | `sudo hy2-admin user enable friend-1` |

`status --human` 是本次新增的易读格式，**仓库已实现，现有 VM 尚未部署新版 CLI/页面**；旧程序继续使用 `status`。它仅向 accountd 发出一次原有 `status` 请求，用 GiB 和中文显示服务端状态、个人余额、正常采样/补账及踢出提醒，不导出凭据、不获取控制锁、不执行 `health run` 或服务操作。默认 `status` 仍是原始 JSON，精确字节与自动化解析不变。

新建成功只显示私密文件路径，分享链接在 `/root/hy2-credentials/friend-1.txt`，通过私密渠道交付，不贴聊天。`user list` 的 used/quota 单位是字节（90 GiB = 96636764160）。`--foreground` 不能省：缺它时 `watch` 在交互终端会被挂起、到时也不退出（已在伪终端复现）。到时退出 124 是正常停止观察，不是代理故障。日志先脱敏。

**停止整个 HY2（会中断所有 HY2 用户，保留 Reality；仅用于已授权停机）：**

```bash
sudo hy2-admin system prepare-stop
# 即使上条采样失败，仍执行已批准的停机；保留错误并记录缺口，不据此填零。
sudo systemctl stop hysteria-server
sudo systemctl --no-pager show hysteria-server -p ActiveState -p LoadState
```

这是逐条维护操作，不要用 `&&` 让最终采样失败阻止已批准的停机。prepare-stop 后健康检查会写共享锁并 mask HY2，所以最后一条可能显示 `LoadState=masked`，这是**预期结果**，不是精确 5 秒的完成保证；恢复时由 `arm` 解除，不要手动 unmask。不要停止 accountd、guard 或 health timer 来“停用账户”。重启后使用下文 [快速恢复执行段](#快速恢复执行段)，不是 `systemctl restart hysteria-server`。prepare-stop 成功也不能据此认定停机尾差为零。

### 用量页

页面只监听 `127.0.0.1:18082`，需要 Basic Auth（HTTP 基本认证，仅在 SSH 隧道中使用），只读，不展示密码、链接、目标站或原始配置。下面在**本地客户端终端**执行，使用已验证的 SSH 管理路径/配置别名；没有可用路径时先核对现有 IAP 通道，不新增密钥、修改元数据或放宽防火墙：

```bash
ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18082:127.0.0.1:18082 YOUR_EXISTING_SSH_ALIAS
# 同一台本地电脑的浏览器打开 http://127.0.0.1:18082
# 首次凭据仅在 VM 的私密管理会话读取：/root/hy2-credentials/page-login.txt
```

**新版呈现（仓库已实现，现有 VM 页面尚未更新）：**

- 顶部直接提示服务端是否就绪、认证关闭、共享保护/证书/采集是否异常；“就绪”不是公网客户端访问或多人隔离验收通过。
- 三张总览卡分清整机保护余量、HY2 本月记账（正常采样＋保守补账）、额度池未分配量。额度池按 `未撤销账户 max(月额度, 已用) + 已撤销账户已用` 计算占用，降额/撤销不抹掉消费；未分配量不是新增流量授权或 Google 免费额度。
- 每账户优先显示**本月剩余**，用状态标签与进度条表达已启用、余额偏低（≥80%）、暂停、撤销、超额及新连接容量不足。上传/下载、签发会话和踢出明细可展开；待下发/失败/已下发待离线会在卡片直接提醒。
- 日趋势、运行诊断和使用/私密分享说明折叠显示；“看诊断”可直接展开，异常时诊断默认展开。`DataError` 时隐藏账户、日统计和额度池旧数值；guard 未知时不显示伪零余额。
- 概览用 GiB 保留两位小数，小于 0.01 的正值显示 `<0.01`；明细保留三位，精确字节以原始 `status` JSON 为准。页面无 JavaScript（浏览器脚本）、外部字体/CDN 或额外网络请求，刷新仍为每 15 秒一次页面 GET。

页面还显示统计新鲜度、证书到期和缺口。在线数是连接实例，不是物理设备数；会话容量是本次核心运行累计签发数量，包含已离线会话，不是同时在线设备限额。隧道需保持运行；Cloud Shell 的 `localhost` 不是本地电脑的 `localhost`，不要把在 Cloud Shell 建立的端口转发误认为本地已可访问。页面密码不是节点密码；不把凭据放 URL、命令参数、聊天或截图，不给朋友管理员页面凭据。

页面内嵌在账户二进制中；Git 同步不会改变 VM 页面。正式更新仍需独立授权的维护窗口与配套检查，不能只为了换界面重跑安装器、`init`、恢复补账或直接重启 accountd（可能通过 BindsTo 停 HY2）。

### 共享故障、总量熔断和回滚

| 情况 | 行为 |
| --- | --- |
| 单人超额/禁用/撤销 | 只拒绝该人新认证并封该人的会话；不重启全服 |
| 单人 `/kick` 超时 | 显示未完成，最多 3 次有限重试；用 `retry-ban` 人工重试，禁止全服重启兜底 |
| 统计持续过期、数据库/控制服务故障 | `hy2-health.timer` 独立停止并 mask HY2；页面本身失败不触发停服 |
| 整机达到 180 GiB 或状态不可信 | `gcp-egress-guard` 停止并 mask 两个服务 |
| 核心异常重启/计数回退/恢复旧备份 | 保持认证关闭并记录缺口，显式核验/补账后才可恢复 |

整机熔断不是普通故障重启场景：先核对 Billing、当前月份/累计及故障原因，并确认两个代理都已停。经授权才可在 **VM SSH** 执行一次 `sudo /usr/local/sbin/gcp-egress-guard --resume --no-start`；达到本月 180 GiB 时不能通过 resume 绕过，更不能改低累计或提高上限。

`--resume --no-start` 只清 guard 自己的封锁，不启动任何代理、不替个人解封、不解除独立 health 锁或操作者的 mask。之后按下文“维护停机与受控恢复”分别恢复 Reality 和 HY2；仅 HY2 health 锁时，不要顺手执行全局 resume。

回滚原则：

- 停止 `hysteria-server`，保留 SQLite、审计、禁用状态和整机累计；不要删除数据库来“重置”。
- 默认回到已经验证的 Reality/TLS 备用状态；HY2 保持关闭。
- **不得恢复无个人额度的旧共享 HY2 入口**，也不能覆盖旧配置来绕过禁用/额度状态。此次迁移回退只允许保留已验的非 HY2 sing-box 配置；新架构变更必须另行评估授权。
- 回滚后再次检查两个受保护服务、mask、启动门禁和证书钩子。

### 当前限制

- 授权服务端切换、真实证书首次发布、原公网客户端实际访问和最终只读核验已通过，临时明文口令文件已删除，本次单账户迁移收尾完成。实际续签后的热加载、共享故障/重启/恢复演练和容量验证仍未执行，不能把本机请求成功当作全部现场验收；见 `STAGE-A-ACCEPTANCE.md` 第 11 节。
- VM 重启后 HY2 不会自动恢复：内存统计已丢失，`health gate` 和持久状态会保持关闭，必须重新核对 guard/账本后再执行 `system arm`、启动核心和 `system activate`。这是有意的 fail-closed 行为，不是自动重连。重启后 `hy2-health.timer` 会因认证处于 hold 而写入 `/var/lib/hy2-health/blocked` 并 mask HY2，这是**预期结果**，不是需要排查的故障；`system arm` 会清除该锁。
- 个人额度是采样软限额，存在 5 秒采样、`/kick` 在途和连接关闭延迟；不是字节级硬截断。
- `HY2` 个人流量不等于 Google 结算流量；整机熔断是保守的 VM 发送字节近似值，也不是财务硬上限。
- 第一版不做速度限制、Brutal、端口跳跃、公网管理页、朋友自助后台、自动核心升级或外部监控。

## 日常维护与故障排查

本节是迁移后的运维入口，首次安装命令不是修复命令。**默认只读、按需执行，不新增 cron/timer、云 API 轮询或收费监控。** 本次验收范围及现场未覆盖项见 `STAGE-A-ACCEPTANCE.md` 第 11–12 节。

### 日常检查与判读

| 时机 | 检查 | 停止条件/边界 |
| --- | --- | --- |
| 使用异常或维护前后 | VM SSH：下方只读快照；本地客户端一次小网页验证 | 先区分单账户与共享故障，不测速、不反复重连 |
| 每周人工查看/证书接近到期 | VM SSH：证书日期、cron 状态、磁盘/内存、最近错误 | 7 天内到期尽早排查；发布要求 >24h，不等到到期才处理 |
| 月初或 guard 告警 | Cloud Shell/Billing：按已授权范围单轮费用与共享额度复核 | 按 `America/Los_Angeles` 切月；预算邮件不是硬封顶，不自动 resume |
| 改账户/配置前及重要维护后 | VM SSH：一次数据库快照，核对可恢复材料 | 不自动创建云快照/存储；不扩大额度池或重装后端 |

**VM SSH，只读快照**（无需输出私钥、密码或完整配置）：

```bash
sudo hy2-admin status
sudo hy2-admin user list
sudo systemctl --no-pager show sing-box hysteria-server hy2-accountd \
  -p ActiveState -p SubState -p LoadState -p NRestarts -p Result -p MemoryCurrent
sudo systemctl list-timers gcp-egress-guard.timer hy2-health.timer --all --no-pager
sudo ss -H -lunp 'sport = :443'
sudo ss -H -lntp 'sport = :47370'
sudo ss -H -lntp '( sport = :18081 or sport = :18082 or sport = :18083 )'
sudo python3 -m json.tool /run/gcp-egress-guard/status.json
sudo openssl x509 -in /etc/hysteria/certs/current/fullchain.cer -noout -dates
sudo systemctl is-active cron
free -h
df -h / /var/lib/hy2-account
df -i / /var/lib/hy2-account
```

- 正常：`healthy`、`core_live`、`authentication_allowed` 为 true；`accounting.review/hold` 和 `shared_health_blocked` 为 false；`vm_error/stats_error/certificate_error/data_error` 为空。**healthy 不包含证书错误检查**，不能只看一个绿色字段。
- `accounting.last_sample` 是 Unix 纳秒；距当前应不超过 30 秒。guard 的 `updated` 是 Unix 秒，距当前不超过 180 秒；月份正确、覆盖两个服务、未 blocked、累计小于 limit 才可用。文件缺失/过期是未知，不是零用量。
- 443/udp 属于 hysteria，47370/tcp 属于 sing-box；18081/18082/18083 只能在 `127.0.0.1`，管理 socket 权限为 0600。TLS 备用未部署，443/8443 **TCP** 不监听不是本次迁移故障。
- owner 的 `used_bytes` 包含 `conservative_adjustment`；本次首月一次补记 34 GiB，已记录 1 条历史缺口。缺口不会因健康恢复消失；新出现的缺口才需结合原因调查。HY2 统计为双向业务量加补账，guard 是整机发送量，两者不应强行相等。
- `ban_pending/ban_failed/ban_issued_waiting` 分别是待处理、重试失败、已下发待确认。`/kick` 下发不是所有空闲会话已经断开的证明；查看后续采样，不通过全服重启“清状态”。
- **不要把 `gcp-egress-guard --status` 当只读命令**：它会更新累计/状态，异常或超限时会触发保护。`hy2-account health run` 同样可能停止并 mask HY2，不能拿来随意探测。
- `verify-singbox-runtime.sh` 可辅助只读诊断，但其退出码不是验收结论，必须看各项输出。迁移后通用段可能仍提示 `hysteria2 inbound not found`、TLS 未部署；应结合后文官方 HY2/无 legacy 检查判读，不能据此重建旧入口。

### 有限日志与故障分流

**VM SSH，只读，最多取最近 15 分钟各 60 行**；先自行审阅、脱敏后再分享。不要开启 `set -x` 或 dump secrets/server 配置，错误日志也不能无条件保证无敏感数据：

```bash
sudo journalctl -u hysteria-server -u hy2-accountd -u hy2-health \
  --since '15 min ago' -n 60 --no-pager
sudo journalctl -u sing-box --since '15 min ago' -n 60 --no-pager
sudo journalctl -t gcp-egress-guard --since '15 min ago' -n 60 --no-pager
```

| 现象 | 优先核对 | 处理边界 |
| --- | --- | --- |
| 仅某个账户失败 | `users` 中 disabled/revoked/exhausted、剩余量、ban 状态；该人客户端凭据/SNI | 用账户操作处理；撤销不可恢复。额度池拒绝时核对已消耗用量保留，不删账户腾额度 |
| 改额度/禁用返回 ban incomplete | 修改可能已落库；查看 pending/failed/issued 状态 | 保持禁用；内置最多 3 次有限尝试，原因排除后才批准一次 retry-ban；不无限循环 |
| 全部 HY2 失败，Reality 正常 | hysteria 是否 active/masked、core_live、stats_error、health latch、证书 | 保留 Reality，不停止 sing-box；共享故障恢复走受控流程 |
| 两个代理都失败 | guard blocked、月累计、状态文件新鲜度、服务/端口 | 先审计成本和累计；不能 unmask/resume/重装清零试错 |
| 服务 active 但客户端失败 | 端口归属、GCP 已有防火墙来源/标签、当前 IPv4、灰云 DNS、SNI/证书、客户端网络 UDP | Cloud Shell 仅按授权只读核对已知规则，不扩大来源范围；仅 TCP 可用时改用已有 Reality |
| 只有某目标 403 或测速 403 | 同节点普通小网页是否可用，目标对出口 IP 的限制 | 不是服务故障的充分证据，不改架构、不大流量测速 |
| `accountd socket unavailable` | ActiveState/SubState/NRestarts、socket 路径/权限、journal、磁盘/内存 | 最多一次有限就绪等待（如 20 秒）；仍失败则停止；不能据该消息断言写操作没生效，更不能重装 |
| 页面 401/打不开，但 HY2 可用 | 本地 SSH 隧道、18082 监听、页面专用密码 | 页面故障不需要重启 accountd；不要开放公网页面或把节点密码当页面密码 |
| `database/witness mismatch` / integrity check failed | 数据库与独立 witness（恢复见证，用于发现回退）不一致、文件权限/磁盘、是否换过备份 | 保持 HY2 关闭，保留原始材料；禁止改 revision、删 witness、init 数据库来强行放行 |
| `unplanned core generation` / 计数回退 | 核心是否异常重启、进程代次、最新采样/缺口 | 不能当作零用量；查明原因并保守补账后再 arm |
| 内存高、磁盘/WAL 增长、OOM（内存耗尽） | `MemoryCurrent`、journal、df、CPU，是否长连接/会话接近上限 | WAL 是 SQLite 预写日志，不能在线删除；不自动升级 VM/改资源限制或清库 |

补充性能采样时可在 **VM SSH** 运行已核验包中的 `collect-performance-baseline.sh` 一次；它默认侧重 sing-box，HY2/accountd 资源应以上述 systemctl 属性补充。不要为排查开启大流量并发压测。

### 证书与备份维护

**VM SSH**：只读到期检查见上文。现场沿用原 ACME cron；登记的 reload hook **只调用 `sync-hysteria-cert`，不重启 sing-box**。这不同于 `issue-acme-cloudflare-token.sh` 的通用默认钩子；不要为“补齐”而重新申请证书或重跑发行脚本。实际续签后的热加载仍需届时验证。

若 ACME 已成功安装新证书、只是 HY2 发布失败，核对源路径/权限/SAN/时间及匹配的校验器后，经授权仅重试一次 **VM SSH**：`sudo /usr/local/sbin/sync-hysteria-cert`。它会写证书版本目录和 current 链接，但不重启 HY2；失败保留已发布版本，不能关闭客户端证书校验、放宽私钥权限或绕过校验器。`validate-cert` 只做离线域名/有效期/密钥校验，**不证明客户端信任链**；后者仍靠开启证书验证的真实客户端确认。

备份与恢复规则：

1. **VM SSH**：`sudo hy2-admin system backup` 只做 SQLite 一致性快照（`VACUUM INTO`），位于 `/var/lib/hy2-account/backups/`，0600，最多保留 7 份；它不是自动定时任务，不包含 secrets、配置、证书或独立 witness。不得频繁执行刷掉有价值的旧快照。
2. 数据库包含可解密凭据和用量，必须与 `/etc/hy2-account/secrets.json` 等恢复必需材料一并按私密备份管理，但**不得把旧 witness 与旧库一起回退来伪装最新状态**。不要在线 `cp accounts.sqlite` 冒充一致性备份，也不要删 `-wal/-shm`。
3. 导出到异机/新存储前另行确认授权、加密、位置和费用；本次只有 VM 本机备份，不能保证抵抗整盘丢失。保留账户最新禁用/撤销/额度变更证据；旧库可能缺失这些状态，不能仅补一点流量就恢复认证。
4. 恢复属于独立维护任务：先批准窗口、停 HY2，再按需停 accountd，保留当前损坏/待核验材料，离线验证候选副本、兼容版本和对应密钥，核对期间全部账户变更及缺口。**本仓库没有通用一键 restore 命令**，本次也未做真实恢复演练；材料不足则保持关闭，不猜测 SQL 修账。
5. 本次迁移备份的原 `SHA256SUMS` 含已删除的 `owner-import.json`，应留作审计；核验余项使用 `SHA256SUMS.after-import-cleanup`。不要为了原清单全绿而重新生成明文导入文件，也不能把含 legacy HY2 的旧配置恢复上线。

### 维护停机与受控恢复

**本节不是日常巡检，也不是自动修复脚本。所有写操作仅在已授权的 VM SSH 维护窗口执行，先有独立管理通道与 Reality 退路。** 保留 guard 和 health timer，不把停用保护/手动 unmask 当作恢复步骤。

**计划停机/升级：** 先保存状态和一次数据库快照，再执行 `sudo hy2-admin system prepare-stop`，随后立即 `sudo systemctl stop hysteria-server`，确认已停。prepare-stop 先关闭新认证并采最终样本，**不保证既有连接到停机之间零尾差**，独立健康检查也可能接着停服；失败时仍按已授权停机处理并记录缺口，不能假定“命令失败等于未改变状态”。只有确需改账户程序时才停 accountd；`hysteria-server` 对 accountd 的 `BindsTo` 依赖意味着重启 accountd 可能连带停止 HY2。不得用 `systemctl restart hysteria-server` 代替新代次核验。

升级固定核心/账户程序：本地离线构建测试、校验配套产物，批准窗口内备份并替换；不在 VM 下载/编译，不重跑安装器、不 init。更换官方核心版本或数据库格式必须重新验收；当前只批准固定 v2.13.0。VM stop/start 可能改变临时公网 IP，本次维护不允许用这种方式排障。

**最新收尾状态（用户回贴）：** 09:57 UTC `hy2-recover` 已输出 `RECOVERED auth=true healthy=true core_live=true`，HY2 ActiveState=active；50584038211 bytes 新增补账已经提交。随后临时单元的 shell `ExecStopPost` 因 systemd 转义/序列化问题报语法错误，导致外层批次非零退出，**不是恢复没有提交**。`hy2-finish-reviewed.sh` 已停用，原 attempt、审核记录和日志保留；不要重跑、不要再调用 recover/arm。之后普通账户/交付续接回贴 `FRIEND_READY`：指定的 30 GiB 朋友账户已创建，Cloud Shell 私密链接文件已准备；三个服务 PID 未变、两个保护 timer active，本次没有恢复或追加补账。文件下载后私下发送，不贴内容；尚无下载/发送、升级后客户端访问或有连接封禁隔离确认。owner 仍为原 90 GiB、当时剩余约 8.86 GiB，总池 140 GiB；80 GiB 意图未明确，不能在账面已用约 81.14 GiB 时直接下调。

普通交付不使用新的 systemd 收尾 hook：只读确认原 recovered/charges 回执及当前健康状态，账号不存在时仅调用一次 `user add`；符合已授权额度/启用状态的现有账户复用其 root-only 私密凭据，不重复创建、旋转或改额度。只将同字节凭据交付到已授权的 VM/Cloud Shell 0700 目录和 0600 文件；已有不同内容或非私密文件即停，不覆盖。操作前后核对三个服务 PID 与两个保护 timer，任何错误先查实际状态，不重启代理、不调用恢复。成熟面板评估未授权安装、迁移、放宽保护或改账；本次仍不新增面板、云资源、全局代理或自动更新。

**此前审核阶段故障（历史记录）：** `stats-v2` 用户回贴显示配套更新后停在 `charge-review / UnsupportedOperation`，收尾确认 `SAFE_STOP=HY2_CONFIRMED_STOPPED`。这是执行器以 Python `r+` 打开不可 seek 的终端造成的输入故障，不是账户核心报错。**下面两版整段更新入口均不应再次执行**；现应只读核对当前程序/账本、原 `recovery/before.json` 与 `review.json`、是否存在 `attempt/` 及审核回执。即使错误发生在审核阶段，也先核对回执，不凭阶段名排除提交。没有 attempt 且账本未漂移时才可在有效授权窗口内继续原审核目录；不得重新 prepare、换目录绕过已提交增量或重新替换程序。窗口已到期则保持 HY2 停止，续接需要重新授权。终端修正只在本地验证通过，尚未作为生产续接执行。

**本次 r1 配套更新的合并执行交付（以下为已执行步骤的历史记录，不是当前重跑指令）：** 已封存候选包旁另提供一次性执行文件，不是常驻更新器，也不修改原归档。**原 `hy2-update-once.sh` 已因新执行器误解析统计格式而停在预检，不再直接重跑。** 当前续接文件为 `hy2-update-resume-v2.sh`：使用原 Cloud Shell、已验证密钥/主机记录，最多两次 SSH、一次小文件 SCP，只传修正后的执行器，复用已上传 VM 的约 10.3 MiB 原包；不下载、不新增云资源，不重跑原有 Go/核心/Python 测试套件。

续接在进入新工作目录前检查旧结果确实是 `preflight / unexpected official statistics format`、旧执行器及包哈希匹配、旧进程已退出、原账户二进制未替换、恢复助手未安装，且旧目录没有维护标记或恢复目录。任一不符即停；**绝不通过新建目录绕过已有维护/补账回执**。旧失败目录保持原样，仅在这个已核实未开始维护的场景使用新的 `-stats-v2` 工作目录。须先退出所有 HY2 客户端，保留独立 Cloud Shell 与 Reality 退路，且本次费用/共享额度确认仍有效。上传新启动文件到 Cloud Shell HOME 并按交付的 SHA-256 校验后，只启动一次：

```bash
bash "$HOME/hy2-update-resume-v2.sh"
```

执行器先私密备份、保存用量证据，再按本节停止 HY2/accountd，原子替换配套程序；核验旧进程退出、新 accountd 就绪和双协议标识后，调用现有助手生成全为 null 的审核文件。**终端内仍必须人工填写逐账户新增 byte、理由和覆盖整个缺口的证据，并确认 `ACCEPT`**；不默认填零，拒绝重复原 34 GiB。安静的在线/业务流快照、整机网卡差值、prepare-stop 成功均不单独证明零尾差；没有足够依据就输入 `STOP`，不能因为脚本接受了 JSON 就宣称金额有证据。

审核后只调用一次 `hy2-recover execute`，保留其写前回执；成功后在同一会话做一次原客户端小请求并确认，最后只分享脱敏摘要。VM 临时 systemd 单元主执行上限 24 分钟，停止及收尾各至多 30 秒，为 30 分钟窗口保留至少 5 分钟退路；不增加定时任务。进入维护后若失败、超时或断线且未留下成功回执，`ExecStopPost`（服务结束后的收尾命令）只检查/必要时停止 HY2；停机也无法确认时明确报 `SAFE_STOP=UNCONFIRMED`，不能保证故障系统一定完成了停止。Reality 和两个保护 timer 从不作为兜底被启停。Cloud Shell、VM 单次目录及恢复回执均保留，**不要直接重跑、删除目录或照搬补账**。交付校验和新编排的本地验证见 `STAGE-A-ACCEPTANCE.md` 第 15 节及包外 `MAINTENANCE-PLAN.json`；这不是生产更新成功记录。

**重启/共享故障后的恢复：**

1. 核对时间/月份、日志、故障原因及最新账本；官方 HY2 必须为 inactive/failed，`ss` 确认 443/udp 未被其他进程占用，不得同时存在旧入口。accountd 必须可读 status（如因维护已停，只在授权下启动一次并有限等待 socket 就绪），不能带着 integrity/密钥/未知恢复问题继续。
2. guard 必须健康、新鲜、覆盖两服务且额度未满。若只是 HY2 health 锁，保留它直到 `arm`；若是全局 guard 锁，按前文成本审计及 **两个代理均停机** 的条件，仅执行一次 `--resume --no-start`。确认锁来源，不删除文件或操作者 mask。
3. 若 Reality 因全局保护被停，在 guard 已许可、非 HY2 sing-box 配置检查通过后，单独 `sudo systemctl start sing-box` 并确认 47370/tcp；保持官方 HY2 关闭直到下一步。未部署的 TLS 不能当作可用退路。
4. 按下文生成私密审核目录，核对本次缺口后填写逐账户**新增 byte 整数**（不是 GiB、不是目标月总量），包括禁用和撤销账户。`arm` 会在当前洛杉矶计费月执行 `adjustment += n`；已记的 34 GiB **不得再填一次**。没有通用的 `clean=0` 或 `unplanned=1 GiB`：最终采样后仍可能有尾差，5 秒采样也不是最大漏账时长。证据不足、恢复旧备份、跨月缺口、长时间未采样或缺失账户变更时，退出快速流程制定专项方案。
5. 仅在完整人工审核后执行恢复工具一次。它先持久保存 `attempt/` 回执，再调用 `arm`；无论错误、超时或中断，同一审核目录都不能再次提交。原始 `system arm` 仍是累加接口，**不是幂等接口**；没有成功输出不证明未写入，必须先核对 `status`、已提交 adjustment、gaps 和本地审计。
6. 新版 root CLI 的 `arm` 与 `health run` 使用同一控制锁，覆盖旧健康状态读取、停服、补账请求、清 latch 和 daemon-reload；不靠停用 timer 消除竞争。恢复工具只重试只读就绪检查，按总截止时间等待启动许可和新代次基线，最多各 10/15 秒；启动与 activate 各只调用一次。
7. 最后确认 authentication_allowed/core_live/healthy 为 true，guard/stats/certificate/data 错误为空、两个保护 timer 运行，原客户端小请求可用。保留本次审核目录与回执直至维护结案；不删除回执后重用原补账，也不清审计。

恢复失败的退路始终是保留已验证的 Reality（以 guard 许可为前提）。先核实核心和认证的实际状态，不能把非零返回当作已经停服；需要中止本轮时，在已授权窗口确保官方 HY2 停止。不恢复旧无额度 HY2，不删除数据库“重新开始”。新月份会切换账本，但不会自动解除手工禁用/撤销或持久全局封锁；正常健康运行的账户无需每月 arm。

### 快速恢复执行段

> **现有 VM 已配套更新到 recovery protocol 1；09:57 UTC 恢复助手已报告成功，新增补账已提交。** 外层收尾 hook 转义报错不能作为重跑依据；后续普通交付已回贴 `FRIEND_READY`，30 GiB 朋友账户与私密文件准备完成。当前没有待重跑的更新、prepare、recover 或 arm。下文仅供其他已授权停机使用，不是现有运行节点的待执行清单；accountd 重启可能通过 BindsTo 停 HY2。

**适用范围：VM 重启或计划停机后 HY2 未运行，Reality/guard 正常，且本次缺口已经人工核清。不适用于**全局熔断、数据库/witness 错误、跨月或长时间未采样、账户变更丢失。三步均在已授权的 **VM SSH** 窗口执行；开机后至少等 1 分钟，不能把仍未刷新的 guard 状态当零用量。正常运行时不要执行。

助手来自仓库 `recover-hy2.py`，新安装器将其安装为 `/usr/local/sbin/hy2-recover`；现有节点的配套更新需要单独安排，不重新安装/初始化账户库。它只支持本项目标准安装路径，不增加定时任务、云请求、下载或自动故障恢复。

**第 1 步，只读确认版本和状态：**

```bash
sudo /usr/local/lib/hy2-account/hy2-account version
sudo hy2-admin status
```

CLI 版本必须包含 `recovery-protocol=1`，正在运行的 accountd 状态必须包含 `recovery_protocol: 1`。缺少任一项就停止，不能绕过版本检查。预期 `core_live=false`、`authentication_allowed=false`、`vm_error=''`，且没有 `data_error`。新版即使 SQLite 可读，也会把独立见证错误明确报告为 `data_error`；`review=true` 本身不能区分正常维护与见证故障。版本字段也不能代替升级窗口中的旧 health 进程退出核验。

**第 2 步，生成并人工填写审核文件（不改账本、不启停服务）：**

```bash
sudo hy2-recover prepare /root/hy2-recovery-01
sudo nano /root/hy2-recovery-01/review.json
```

`/root/hy2-recovery-01` 是本次新目录的示例名；父目录和审核目录要求 root 所有、0700，文件为 0600。目录已存在时拒绝覆盖，不要为重试删除旧目录。`before.json` 保存当时月份、代次、采样时间、账户状态和用量；**不要编辑它**。`review.json` 的 `charges` 从实时完整账户表生成，每个值都是 `null`，包括禁用和撤销账户。

审核要求：

- 将每个 `null` 替换为本次**尚未入账的新增 byte 整数**，并填写 `reason`（8–200 个 UTF-8 字节），说明缺口区间、依据及为何这些值足够；不要填写密码、密钥或节点链接，审核文件不外传。1 GiB = 1073741824 bytes，这只是单位换算，**不是默认补账量**。
- 从最后可信采样到确认旧核心停止之间核对缺口，包括采样失败、请求耗时、停机尾差及期间账户变化。5 秒是正常采样周期，代码容忍的统计过期窗口可更长；未经证实的 e2-micro 吞吐假设不是硬速率上界。没有可核实上界或历史证据时停止，不猜固定补账。
- `prepare-stop` 成功后既有连接仍可能产生尾差；`revoked=true` 也不证明旧会话已全部关闭（踢出可能未完成）。**只有能证实无尚未入账用量，或此前补账已完整计入时，才可明确填 0**。
- 填写的是增量，不是当前总量或额度；不要重复迁移时的 34 GiB，也不要重填前一次失败恢复中已经提交的增量。恢复旧库、见证错误或缺失账户变更需要专项核验，不由此助手修复。

以下是本次保守补账的审核过程记录；具体值已于 09:57 UTC 确认并提交，**不得再次提交**。用户此前接受“用可能达到几十 GiB 的保守多记换取恢复”的方向，随后另行确认具体字节数。可先只读评估一次极粗上界：固定官方 direct/TCP 核心无自定义传输/请求 hook 或数据解压扩张、同一启动及网络命名空间、原 ens4/lo 接口和计数连续成立时，停机确认时 ens4 累计接收量加当前 lo 累计接收量，覆盖上传从客户端接收和下载从目标接收的业务字节。它含大量历史及无关流量，**不是实际漏账或结算用量**；不减原 34 GiB，也不默认给出 0/1 GiB。只读预览须显示新增 byte、原 adjustment、新 adjustment、补后 owner 剩余额度与是否仍在原配额内，具体数值经审核后才可写原 `review.json`。同次启动、原 ifindex、单调快照只能检查常规连续性，不能证明不存在未记录的特权计数重置/接口重建；若发生过此类变更或数据路径覆盖不成立，停止这一方案，不把方向授权当作依据已成立。该评估不会改账本、保护或云资源。后续预览已回贴：新增候选 50584038211 bytes（47.110057 GiB），owner 补后约剩 8.860206 GiB，这在预览时未提交，后来已确认并提交；用户另指定了一个 30 GiB 朋友账户（两账户预留 90+30=120 GiB，仍在原 140 GiB 池内）。包外 `hy2-finish-reviewed.sh` 原设计如下，但已因收尾 hook 的实际执行错误而停用、只留作审计，不再执行：在 Cloud Shell 以 Bash 输入明确确认具体金额、30 分钟窗口及经 SSH/SCP 到 Cloud Shell 0600 私密文件的交付授权，保留原审核目录并写私密审核记录，再调用现有恢复助手一次。恢复由临时 systemd 单元托管，主执行最多 150 秒，失败且无 recovered 回执才停止 HY2；不重新安装或 prepare。恢复核验通过后才单次创建朋友账号，账号/交付失败不重启或停止整体服务，错误后先查是否已落库，不重建或重试补账。最终只输出脱敏服务端摘要和文件路径，真实客户端访问及有连接的封禁隔离仍须另验；不自动放宽防火墙，也不宣称全部生产场景通过。交付文件与校验值见包外计划，旧更新入口仍不得重跑。

工具会在执行前重新核对月份、代次和所有账户的用量/额度/禁用/撤销状态；与 `before.json` 不一致即停。不要并发执行其他账户修改、原始 `system arm` 或另一套维护命令。

**第 3 步，明确接受已审核增量后执行一次：**

```bash
sudo hy2-recover execute /root/hy2-recovery-01 --accept-reviewed-charges
```

执行边界：

- 先检查两个保护 timer 分别为 active、核心 inactive/failed、UDP 443 空闲、guard/数据库正常及证书有效；不自动启动 timer、解除全局熔断或手动 unmask。
- 在发出唯一一次 `arm` **之前**，先将审核副本和补账写入 `attempt/` 并同步文件/目录到磁盘。该目录存在即拒绝再次提交，不能依赖成功后改名或 review/hold 推断是否已计入。`arm` 带 `--expected-month`，月份变化会在账户端写入前拒绝。
- 等待只读 gate 的总预算为 10 秒；启动后的新代次基线总预算为 15 秒。均使用单调时钟，每次只读调用最多 2 秒并扣除剩余预算，不是 10/15 次慢调用。预检总预算 20 秒；arm、启动、activate 单次命令预算分别为 30/20/17 秒，超时不重试写操作，服务端动作仍可能已经生效。
- root 侧共享锁为 `/var/lib/hy2-health/blocked.lock`，恢复助手互斥锁为同目录的 `blocked.recovery.lock`，均为 0600。不要删除锁文件、停止保护 timer 或反复 reset-failed 来试错。健康检查在获取控制锁后才读状态，不能把旧停服决定带过一次成功 arm。

成功最后一行为 `RECOVERED ...`，再用客户端打开一个小网页确认，不测速。`attempt/arm-returned-ok.json` 只证明 arm 返回成功；`attempt/recovered.json` 记录本轮最终本机核验通过，均不等于未来状态或公网访问保证。

**出现 STOP、超时或 SSH 中断时：**

| 现场状态 | 处理 |
| --- | --- |
| 没有 `attempt/`，停在预检/审核阶段 | 助手尚未调用 arm；修正原因并确认账本未被其他操作改变后，可用原审核目录重试执行。审核目录本身已存在时不要重跑 prepare 覆盖它 |
| 有 `attempt/`，无 arm 成功回执（包括空/不完整 attempt） | 可能未发送、未提交或已提交，不能凭错误判定；保留全部文件，先只读核对 status 的 adjustment、gaps 和本地审计。不得删除 attempt、复制旧 charges 或改回旧 clean/unplanned 命令重跑 |
| 有 arm 成功回执，但停在 gate/启动/基线/激活 | 原补账已计入；先查原因、核心实际状态及认证是否已激活，不直接重启、重补或重复 activate。若确需新一轮恢复，重新确认核心已停，并为**仅尚未计入的新增缺口**建立全新审核目录；全部已计入且无新增尾差时才可显式审核为 0 |
| 任意 `data_error` / witness 错误 / 全局 guard 故障 | 退出快速恢复，保持 HY2 关闭，按专项核验处理；不能删见证、清库、重装、回退旧无额度入口或顺手解除全局保护 |

`system arm` 本身仍是累加接口，单次提交保障依赖保留审核目录/回执并遵守人工审核边界；它不是财务硬限额或通用数据库恢复器。失败退路仍是 guard 许可下已验证的 Reality。普通重启保留临时公网 IP；控制台 stop/start VM 可能换 IP，本流程不执行该操作。

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
`/usr/local/sbin/sync-hysteria-cert`，先复制到 root 可写、`hysteria` 组只读的版本目录，再对
**暂存副本**统一检查域名、生效/到期时间和密钥配对，通过后才原子切换 `current`：

```text
/etc/hysteria/certs/current/fullchain.cer
/etc/hysteria/certs/current/private.key
```

钩子调用已安装的 `/usr/local/lib/hy2-account/hy2-account validate-cert <cert> <key> <hostname>`，
使用 Go 的 `crypto/tls.LoadX509KeyPair` 和 `crypto/x509.VerifyHostname`：必须由叶证书的 SAN
覆盖配置域名，不回退旧式 CN；只接受精确域名或完整左侧标签通配，不接受 `r*.example.com`。
证书必须已经生效、剩余有效期**大于 24 小时**，且私钥与叶证书匹配。校验入口仅离线读文件，
不加载账户配置、密钥配置或数据库，不联网。复制、校验或 `current` 切换前失败会保留旧版本并清理
未发布副本/临时链接；若重命名已提交后才报错，则保留被 `current` 引用的已验证版本，避免悬空链接。
源文件在复制后变化不影响已校验的暂存副本。发布成功后保留最近三个版本。

更新此钩子时必须配套更新本地构建的账户二进制（已安装环境仍需授权维护窗口），不能只替换脚本。
账户二进制缺失或不支持 `validate-cert` 时拒绝发布，不回退 OpenSSL 检查；安装器不会在 VM 编译或下载。

官方 HY2 每次 TLS 握手读取当前证书，因此证书轮换不需要重启 HY2。`issue-acme-cloudflare-token.sh` 的通用默认钩子还会执行 `systemctl try-restart sing-box`；**本次现场登记的是仅同步 HY2 的钩子，没有这个重启步骤，且未部署 VLESS TLS**。保留的其他 sing-box 证书协议如需更新，须另行确认路径与热加载/重启方式及中断授权，不冒称已具备续签联动。不要放宽 `/root` 或私钥权限，也不要把真实私钥复制进仓库。

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
