# GCP Free Tier Proxy Kit

面向 Google Cloud 免费层 VM 的低成本代理维护包。

当前项目有两层状态，不能混为一谈：

- **生产迁移前基线**：`VLESS Reality Vision` 仍是主力；旧 sing-box HY2 仅是一次性试验入口。
- **已实现、待授权的 HY2 主用账户系统**：官方 Hysteria 2（HY2）独立服务、SQLite 账户/额度、只读用量页和定向封禁。代码已通过本地单元测试与固定版本官方核心的回环集成测试，但**尚未在真实 VM 上部署或迁移**。

HY2 主用的目标不是全局代理，而是把 HY2 作为个人和朋友的受限入口：每个账户有显式月额度，封禁一个账户不重启代理、不影响其他账户的既有连接；Reality/TLS 继续留在 sing-box 作为本人备用。迁移会短暂中断现有连接，必须在明确维护窗口内执行。

当前项目的维护口径是：

- `VLESS Reality Vision` 作为本人备用节点；迁移后 HY2 作为主用入口。
- `VLESS TCP+TLS` 作为可选证书备用节点。
- Cloudflare 只做灰云 DNS，不走小黄云、Argo、WARP、CDN 回源。
- 客户端只把 AI 相关域名走代理，不建议全局流量压到 GCP。
- VM 内用 `gcp-egress-guard` 在约 `180 GiB/月` 时停止并 mask `sing-box` 与 `hysteria-server`，避免继续消耗出站额度。

## Current Baseline

| 项目 | 当前值 |
| --- | --- |
| Google Cloud project | `YOUR_PROJECT_ID` |
| VM name | `YOUR_VM_NAME` |
| Zone | `YOUR_ZONE` |
| 迁移前生产主力 | `VLESS Reality Vision`，`47370/tcp` |
| Reality SNI | `apple.com` |
| 迁移前 HY2 试验入口 | sing-box `443/udp`（legacy；完成迁移后必须删除或保持停用） |
| HY2 目标入口 | 官方 Hysteria 2 独立服务，`443/udp`，每账户独立凭据 |
| 备用节点 | `VLESS TCP+TLS` |
| TLS port | 优先 `443/tcp`，冲突时 `8443/tcp` |
| 证书目录 | `/root/ygkkkca` |
| HY2 发布证书 | `/etc/hysteria/certs/current/`（由 root-only 同步钩子发布） |
| 熔断阈值 | `180 GiB/月`；覆盖 `sing-box` 与 `hysteria-server` |
| 账户控制 | `hy2-accountd` + SQLite + root-only `hy2-admin` + SSH 隧道只读页 |

## Read First

- [IMPLEMENTATION.md](./IMPLEMENTATION.md)：完整维护手册和执行命令，包含 HY2 账户系统的构建、预检、迁移和回滚。
- [CHOOSING-VLESS-NODES.md](./CHOOSING-VLESS-NODES.md)：Reality、TLS、HY2、端口、DNS、防火墙、分流如何选择。
- [HYSTERIA2-EVALUATION.md](./HYSTERIA2-EVALUATION.md)：历史 Reality 慢速诊断、计费门禁、HY2 A/B 与回滚记录；新架构以账户方案为准。
- [HYSTERIA2-ACCOUNT-PLAN.md](./HYSTERIA2-ACCOUNT-PLAN.md)：HY2 主用、独立账户配额和用量页方案；已实现代码，待授权 VM 迁移。
- [STAGE-A-ACCEPTANCE.md](./STAGE-A-ACCEPTANCE.md)：本地阶段验收记录、未覆盖项和上线前交叉核验清单。
- [AGENTS.md](./AGENTS.md)：给后续 AI/维护者的项目边界和执行规则。

## Files

| 文件 | 运行位置 | 作用 |
| --- | --- | --- |
| `create-vm-standard-ipv4.sh` | Cloud Shell | 创建推荐的 GCP 免费层 VM 和基础防火墙 |
| `open-vless-reality-port.sh` | Cloud Shell | 给 VM 加标签并开放 `47370/tcp` |
| `open-vless-tls-port.sh` | Cloud Shell | 开放 TLS 节点端口，默认 `443/tcp` |
| `open-hysteria2-port.sh` | Cloud Shell | 按显式来源范围开放 HY2，默认 `443/udp` |
| `create-budget-alert.sh` | Cloud Shell | 创建项目级预算提醒 |
| `audit-leftovers.sh` | Cloud Shell | 只读检查可能继续计费的残留资源 |
| `audit-cost-and-usage.sh` | Cloud Shell | 只读核实计费关联、VM 成本属性、预算和当月发送量 |
| `install-egress-guard.sh` | VM | 安装月度出站流量熔断器；可同时保护 `sing-box` 与 `hysteria-server` |
| `egress-guard/runtime.sh` | VM | 熔断器实际运行逻辑；由安装脚本安装到 `/usr/local/sbin` |
| `egress-guard/generator.sh` | VM | 开机/daemon-reload 时按持久状态 mask 受保护服务 |
| `download-hysteria-release.sh` | 本地或 VM | 单次下载并校验固定版本官方 HY2 核心；安装器本身不下载 |
| `install-hy2-account.sh` | VM | 准备官方 HY2、账户控制、SQLite 与 systemd；不迁移、不启动公网 HY2 |
| `sync-hysteria-cert.sh` | VM | root-only 证书校验与原子发布；由 ACME 续签钩子调用 |
| `hy2-account/` | 本地构建、VM 运行 | Go 账户控制程序、只读页面、systemd 模板、sing-box HY2 移除工具和回归测试 |
| `tests/` | 本地 | egress guard 回归测试与固定核心可行性脚本 |
| `collect-performance-baseline.sh` | VM | 只读采集慢速诊断所需的主机、进程和网络基线 |
| `issue-acme-cloudflare-token.sh` | VM | 使用 Cloudflare API Token 申请/安装证书，并在已安装 HY2 时调用证书同步钩子 |
| `add-vless-tls-inbound.sh` | VM | 在现有 sing-box 配置里新增 `VLESS TCP+TLS` 入站 |
| `add-hysteria2-inbound.sh` | VM | **legacy**：旧 sing-box HY2 入站；安装账户后端后拒绝再次创建 |
| `manage-hysteria2-users.sh` | VM | **legacy**：旧 sing-box HY2 用户管理；安装账户后端后拒绝运行 |
| `export-vless-links.sh` | VM | 只读导出 `vless://` 分享链接和本地订阅内容 |
| `verify-singbox-runtime.sh` | VM | 只读诊断 sing-box、端口、证书、熔断器 |
| `sing-box-client-ai-routing.fragment.json` | 本地客户端 | AI 分流模板 |
| `sing-box-client-vless-tls.fragment.json` | 本地客户端 | VLESS TLS 节点模板 |
| `sing-box-client-hysteria2.fragment.json` | 本地客户端 | HY2 客户端模板，强制校验证书 |

## Quick Start

在 Cloud Shell 设置固定变量：

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export VM_NAME="YOUR_VM_NAME"
export ZONE="YOUR_ZONE"
```

新建 VM 时优先把 SSH 来源设为 IAP（Identity-Aware Proxy，身份感知代理）的 TCP 转发网段：

```bash
SSH_SOURCE_RANGE="35.235.240.0/20" bash gcp-free-tier-proxy/create-vm-standard-ipv4.sh
```

创建脚本会对该 VM 的 SSH 标签增加“可信来源 allow + 其余来源 deny”，因此 default VPC 中的
`default-allow-ssh 0.0.0.0/0` 不会重新放开这台 VM。必须先确认 IAP IAM 权限可用；已有 VM
先运行 `audit-leftovers.sh`，不要未经验证就删除项目级默认规则。

开放 Reality 主力端口：

```bash
bash gcp-free-tier-proxy/open-vless-reality-port.sh
```

开放 TLS 备用端口：

```bash
TLS_PORT=443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

开放 HY2 前必须先运行只读核验，并在 Cloud Billing > Reports 检查本月至今、按 SKU 分组的
Gross cost、Credits 与 Net cost：

```bash
bash gcp-free-tier-proxy/audit-cost-and-usage.sh
bash gcp-free-tier-proxy/audit-leftovers.sh
```

仅在没有意外 SKU、发送量低于警戒线且熔断器健康时开放。能固定客户端出口时优先填 `/32`；
动态网络确实无法限源时才用 `0.0.0.0/0`：

```bash
CONFIRM_COST_AUDIT=yes SOURCE_RANGES="203.0.113.10/32" \
  HY2_PORT=443 bash gcp-free-tier-proxy/open-hysteria2-port.sh
```

如果 VM 脚本回退到 `8443`：

```bash
TLS_PORT=8443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

进入 VM：

```bash
gcloud compute ssh "$VM_NAME" \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tunnel-through-iap
```

把 VM 脚本传进去：

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

在 VM 内执行**迁移前基线**（如果尚未安装账户后端）：

```bash
# 12 只是示例；必须先用 audit-cost-and-usage.sh 核实本月已用并向上取整。
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAME=sing-box \
  bash /tmp/install-egress-guard.sh
sudo HY2_DOMAIN="rr.example.com" bash /tmp/add-hysteria2-inbound.sh
sudo bash /tmp/add-vless-tls-inbound.sh
sudo bash /tmp/verify-singbox-runtime.sh
```

> 安装 `install-hy2-account.sh` 后，`add-hysteria2-inbound.sh` 和 `manage-hysteria2-users.sh` 会拒绝运行，避免重建不受个人额度保护的旧入口。

## HY2 主用账户系统（已实现，待授权迁移）

完整命令、迁移顺序和回滚见 [IMPLEMENTATION.md](./IMPLEMENTATION.md#hy2-主用账户系统实现已就绪迁移需授权)。先本地构建并测试，不要在 e2-micro 上编译：

```bash
cd hy2-account
go test ./...
# 可选：固定版本官方核心的回环隔离测试，不会接触 VM、公网目标或 systemd
bash ../download-hysteria-release.sh amd64 /tmp/hysteria
HY2_BIN=/tmp/hysteria go test -race ./internal/account -run TestPinned -v
CGO_ENABLED=0 go build -trimpath -o /tmp/hy2-account ./cmd/hy2-account
```

把 `install-hy2-account.sh`、`sync-hysteria-cert.sh`、`hy2-account/systemd/`、`hy2-account/tools/`、构建好的二进制和已校验的官方 HY2 二进制传到 VM。迁移前必须：

1. 用 `audit-cost-and-usage.sh` 核实本月已用、账单和共享免费额度。
2. 用 `INITIAL_USED_GIB` 重建/确认熔断器基线，并显式传入 `SERVICE_NAMES="sing-box hysteria-server"`。
3. 在维护窗口内执行 `install-hy2-account.sh`，导入账户；先不要启动官方 HY2。
4. 先运行 `sync-hysteria-cert` 发布证书，再删除 sing-box 中的旧 HY2 入站并 `sing-box check`，释放 `443/udp`。随后立即执行 `hy2-admin system arm`、启动 `hysteria-server`、等待采样并 `hy2-admin system activate`。`system arm` 的启动窗口只有约 45 秒，不要提前太久执行。
5. 验证单人封禁不重启服务、Reality/TLS 仍可用、整机熔断覆盖两个服务。

未完成上述验证前，不要把 UDP 443 的公开入口从旧 sing-box 切换到新服务。

## 查看客户端分享链接

当前项目不运行订阅面板，也不额外暴露 HTTP 订阅服务。推荐在 VM 上只读导出
`vless://` 分享链接，然后导入 v2rayN / sing-box 客户端。

Reality 服务端配置通常只保存 private key，不保存客户端需要的 public key。查看 Reality
分享链接时，把初次部署输出里的 Reality public key 通过环境变量传入：

```bash
sudo REALITY_PUBLIC_KEY="YOUR_REALITY_PUBLIC_KEY" \
  REALITY_ADDRESS="YOUR_GRAY_CLOUD_DOMAIN_OR_VM_IP" \
  bash /tmp/export-vless-links.sh
```

如果只看 TLS 备用节点，通常不需要传 public key：

```bash
sudo TLS_ADDRESS="rr.example.com" bash /tmp/export-vless-links.sh
```

脚本会打印：

- `vless://...` 分享链接。
- base64 后的本地订阅内容。

这些内容包含 UUID 等客户端凭据，只在自己的终端里查看，不要贴进聊天、Issue 或提交记录。

## Certificate Notes

Reality 节点不使用 Let’s Encrypt 证书。

TLS 节点使用：

```text
/root/ygkkkca/fullchain.cer
/root/ygkkkca/private.key
```

如果 `fullchain.cer` 不存在，TLS 脚本会回退到：

```text
/root/ygkkkca/cert.crt
```

证书域名必须能通过 Cloudflare 灰云 DNS 解析到 VM IPv4。不要把这个域名开小黄云。

安装 HY2 账户后端后，`issue-acme-cloudflare-token.sh` 会先调用 root-only 的
`/usr/local/sbin/sync-hysteria-cert`，校验 SAN、有效期和证书/私钥匹配，再原子发布到：

```text
/etc/hysteria/certs/current/fullchain.cer
/etc/hysteria/certs/current/private.key
```

官方 HY2 每次 TLS 握手读取当前证书，不需要为证书续签重启 HY2。不要放宽 `/root` 或私钥权限。

## Egress Guard

`install-egress-guard.sh` 会安装：

```text
/usr/local/sbin/gcp-egress-guard
/etc/systemd/system/gcp-egress-guard.service
/etc/systemd/system/gcp-egress-guard.timer
/var/lib/gcp-egress-guard/state
```

首次安装必须显式传入由 `audit-cost-and-usage.sh` 核实并向上取整的当月已用量
`INITIAL_USED_GIB`，避免月中安装或重装时错误地从零计数。之后它每分钟读取默认网卡：

显式传入该值也表示“重新建立基线”：若旧状态存在，安装器会先把它备份为带 UTC 时间戳的
`state.bak.*` 再写入新基线。日常重复安装且不需要重建基线时可省略该变量；状态意外丢失后
运行时不会自动归零，而会关闭代理并要求重新审计。

```text
/sys/class/net/<iface>/statistics/tx_bytes
```

达到 `150 GiB` 时写 warning；达到 `180 GiB` 时逐一停止并 mask 受保护服务。安装账户后端后应使用：

```bash
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAMES="sing-box hysteria-server" \
  bash /tmp/install-egress-guard.sh
```

保护逻辑会逐一执行 `systemctl stop <unit>`，再通过 generator 在开机和 `daemon-reload` 时 mask；某个服务停止失败不会跳过另一个。

月份按 Google Cloud 计费边界使用 `America/Los_Angeles` 判断。默认不会在新月自动解除熔断；检查 Billing 后执行 `sudo /usr/local/sbin/gcp-egress-guard --resume` 才恢复服务。若列表包含 HY2，必须使用 `--resume --no-start` 并单独走账户核验/启动流程。计数器或状态异常时采用 fail-closed（故障时关闭）策略。

## HY2 账户系统

新架构使用官方 `hysteria-server` 独立进程，账户由本机 `hy2-accountd` 管理；sing-box 只保留 Reality/TLS。每个账户有独立密码和月额度，账户名只是显示标签，实际封禁按稳定账户和每条连接独立的会话 ID 执行。

常用命令（root）：

```bash
sudo hy2-admin status
sudo hy2-admin user add friend-1 --quota-gib 20
sudo hy2-admin user list
sudo hy2-admin user quota friend-1 --gib 30
sudo hy2-admin user disable friend-1
sudo hy2-admin user enable friend-1
sudo hy2-admin user retry-ban friend-1
sudo hy2-admin user revoke friend-1
sudo hy2-admin user export friend-1
```

`add`/`export` 只把节点链接和客户端片段写入 `/root/hy2-credentials/`，不回显密码；通过可信私密渠道交付。`revoke` 是永久墓碑，不能通过删除再新增恢复额度。

只读用量页经 SSH 隧道访问，不在公网开放：

```bash
ssh -N -L 18082:127.0.0.1:18082 USER@VM
# 浏览器打开 http://127.0.0.1:18082
# 用户名/密码见 VM 上 /root/hy2-credentials/page-login.txt
```

页面的“已下发”表示 `/kick` 标记已写入，不代表所有空闲设备立即离线；封禁接口失败会显示未完成并保留有限重试，不会重启代理。个人额度是每 5 秒采样的软限额，不是结算级硬截断。VM 重启后 HY2 不会自动恢复，需要重新核验并执行 `system arm`/启动/激活。

## 旧 sing-box HY2 入口（legacy）

> 这是一次性试验入口，不是新账户系统的一部分。安装账户后端后，`add-hysteria2-inbound.sh` 和 `manage-hysteria2-users.sh` 会拒绝运行；迁移完成后应删除该入站并保持停用，不能用它绕过个人额度。

`add-hysteria2-inbound.sh` 要求 sing-box `1.11+`，复用现有证书并增加 `443/udp` 入站；
`443/tcp` 与 `443/udp` 可以同时使用。脚本不会设置 Brutal 带宽值，因此按 BBR 路径开始测试，
不会启用详细访问日志。每位使用者用独立用户名运行一次；密码保存在 VM root-only 文件中，
不会打印或写入 Git：

```bash
sudo HY2_DOMAIN="rr.example.com" HY2_USER_NAME="friend-1" \
  bash /tmp/add-hysteria2-inbound.sh
```

脚本会先备份配置、执行 `sing-box check`、重启并确认 UDP 监听；任一步失败会自动恢复备份。
当用户管理脚本与它位于同一目录时，还会自动安装 root-only 管理命令。之后新增第二个人只需：

```bash
sudo sing-box-hy2-user add friend-2
sudo sing-box-hy2-user list
```

命令自动识别当前唯一的 HY2 入站、端口、证书和 TLS 域名，保留已有用户，为新用户生成独立
强密码，并创建两个权限为 `0600` 的文件：

```text
/root/hysteria2-credentials/friend-2.json
/root/hysteria2-credentials/friend-2.txt
```

`.txt` 是可导入客户端的 `hysteria2://` 链接。命令只报告文件路径，不把密码或链接打印到终端。
重新导出和撤销用户分别使用：

```bash
sudo sing-box-hy2-user export friend-2
sudo sing-box-hy2-user remove friend-2
```

撤销前会交互确认，并拒绝删除最后一个用户。每次配置变更仍会备份、校验、重启和检查 UDP
监听；失败自动回滚。若配置中有多个 HY2 入站，才需要显式传入 `HY2_TAG`。

它还仅对 HY2 入站拒绝访问云元数据域名与私网地址。不要在客户端设置 `insecure: true`。
默认会把 sing-box 全局日志级别从 `info` 收紧为 `warn`，避免长期记录常规连接目标；需要临时
排障时可显式传入 `HY2_REDUCE_LOGS=false` 保留原级别。
当连接地址与证书域名不同时，分别设置 `HY2_SERVER_ADDRESS` 与 `HY2_DOMAIN`。迁移一个从未
对公网开放的旧 HY2 入站时，可显式传入原标签并用 `HY2_REPLACE_USERS=true` 轮换旧凭据；
该选项会使原 HY2 客户端失效，不能用于仍在使用的入口。

## Client Guidance

HY2 主用入口（每账户使用自己导出的凭据）：

```text
type: hysteria2
server: 灰云域名或 VM IPv4
port: 443
sni: 证书域名
insecure: false
network: tcp            # 客户端只转发 TCP 业务；底层仍是 UDP 443
up/down: 留空           # 不用 Brutal 带宽提示
```

Reality 备用节点：

```text
protocol: vless
port: 47370
tls: reality
sni: apple.com
flow: xtls-rprx-vision
encryption: none
```

TLS 备用节点：

```text
protocol: vless
port: 443 或 8443
tls: tls
sni: 证书域名
flow: 留空
encryption: none
```

客户端推荐只把 AI 域名走代理：

```text
openai.com
chatgpt.com
oaistatic.com
oaiusercontent.com
anthropic.com
claude.ai
generativelanguage.googleapis.com
gemini.google.com
aistudio.google.com
cursor.sh
anysphere.co
```

## Validation

本地检查：

```powershell
& 'D:\Dev\Git\bin\bash.exe' -n *.sh
python -m json.tool sing-box-client-ai-routing.fragment.json
python -m json.tool sing-box-client-vless-tls.fragment.json
python -m json.tool sing-box-client-hysteria2.fragment.json
```

本地 Go 检查（不要先在 VM 上编译）：

```bash
cd hy2-account
go vet ./...
go test ./...
# 可选：固定版本官方核心回环集成测试
HY2_BIN=/path/to/verified/hysteria go test -race ./internal/account -run TestPinned -v
```

本地 Python 回归（不接触 systemd/VM）：

```bash
python3 -m unittest -q tests.test_egress_guard tests.test_remove_singbox_hy2
HY2_BIN=/path/to/verified/hysteria python3 tests/hy2_core_isolation.py
```

VM 检查：

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo ss -lunp | grep ':443 '
sudo systemctl status sing-box --no-pager
sudo systemctl status hysteria-server --no-pager
sudo systemctl status hy2-accountd --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo hy2-admin status
```

## Safety

- 不要提交 Cloudflare API Token、UUID、Reality private key、证书私钥、HY2 账户密码、页面密码、认证/统计密钥或 SQLite 数据库。
- 不要把旧 sing-box HY2 和新的官方 HY2 账户后端同时作为两套账户真相。
- 不要长期全局走 GCP 节点。
- 不要开启 Cloudflare 小黄云、Argo、WARP 或 CDN 回源，除非重新评估成本和路径。
- 不要把 `403 speed test` 当作唯一故障信号；实际访问和端口握手更重要。
