# GCP Free Tier sing-box Proxy Kit

面向 Google Cloud 免费层 VM 的低成本 sing-box 代理维护包。

当前项目的维护口径是：

- `VLESS Reality Vision` 作为主力 AI 节点。
- `VLESS TCP+TLS` 作为可选证书备用节点。
- Cloudflare 只做灰云 DNS，不走小黄云、Argo、WARP、CDN 回源。
- 客户端只把 AI 相关域名走代理，不建议全局流量压到 GCP。
- VM 内用 `gcp-egress-guard` 在约 `180 GiB/月` 时停止并 mask `sing-box`，避免继续消耗出站额度。

## Current Baseline

| 项目 | 当前值 |
| --- | --- |
| Google Cloud project | `YOUR_PROJECT_ID` |
| VM name | `YOUR_VM_NAME` |
| Zone | `YOUR_ZONE` |
| 主力节点 | `VLESS Reality Vision` |
| Reality port | `47370/tcp` |
| Reality SNI | `apple.com` |
| HY2 试验入口 | `443/udp`，与 Reality 并行运行 |
| 备用节点 | `VLESS TCP+TLS` |
| TLS port | 优先 `443/tcp`，冲突时 `8443/tcp` |
| 证书目录 | `/root/ygkkkca` |
| 熔断阈值 | `180 GiB/月` |

## Read First

- [IMPLEMENTATION.md](./IMPLEMENTATION.md)：完整维护手册和执行命令。
- [CHOOSING-VLESS-NODES.md](./CHOOSING-VLESS-NODES.md)：Reality、TLS、端口、DNS、防火墙、分流如何选择。
- [HYSTERIA2-EVALUATION.md](./HYSTERIA2-EVALUATION.md)：Reality 慢速诊断、计费门禁、Hysteria 2 A/B 与回滚标准。
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
| `install-egress-guard.sh` | VM | 安装 180 GiB/月出站流量熔断器 |
| `collect-performance-baseline.sh` | VM | 只读采集慢速诊断所需的主机、进程和网络基线 |
| `issue-acme-cloudflare-token.sh` | VM | 使用 Cloudflare API Token 申请/安装证书 |
| `add-vless-tls-inbound.sh` | VM | 在现有 sing-box 配置里新增 `VLESS TCP+TLS` 入站 |
| `add-hysteria2-inbound.sh` | VM | 备份并校验配置后，在现有 sing-box 增加 HY2 入站 |
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
  gcp-free-tier-proxy/add-vless-tls-inbound.sh \
  gcp-free-tier-proxy/export-vless-links.sh \
  gcp-free-tier-proxy/verify-singbox-runtime.sh \
  "$VM_NAME":/tmp/ \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tunnel-through-iap
```

在 VM 内执行：

```bash
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 SERVICE_NAME=sing-box \
  bash /tmp/install-egress-guard.sh
sudo HY2_DOMAIN="rr.example.com" bash /tmp/add-hysteria2-inbound.sh
sudo bash /tmp/add-vless-tls-inbound.sh
sudo bash /tmp/verify-singbox-runtime.sh
```

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

达到 `150 GiB` 时写 warning；达到 `180 GiB` 时：

```bash
systemctl stop sing-box
systemctl mask sing-box
```

月份按 Google Cloud 计费边界使用 `America/Los_Angeles` 判断。默认不会在新月自动解除熔断；
检查 Billing 后执行 `sudo /usr/local/sbin/gcp-egress-guard --resume` 才恢复服务。计数器或状态
异常时采用 fail-closed（故障时关闭）策略，停止并 mask `sing-box`。

## Hysteria 2 试验入口

`add-hysteria2-inbound.sh` 要求 sing-box `1.11+`，复用现有证书并增加 `443/udp` 入站；
`443/tcp` 与 `443/udp` 可以同时使用。脚本不会设置 Brutal 带宽值，因此按 BBR 路径开始测试，
不会启用详细访问日志。每位使用者用独立用户名运行一次；密码保存在 VM root-only 文件中，
不会打印或写入 Git：

```bash
sudo HY2_DOMAIN="rr.example.com" HY2_USER_NAME="friend-1" \
  bash /tmp/add-hysteria2-inbound.sh
sudo cat /root/hysteria2-credentials/friend-1.json
```

脚本会先备份配置、执行 `sing-box check`、重启并确认 UDP 监听；任一步失败会自动恢复备份。
它还仅对 HY2 入站拒绝访问云元数据域名与私网地址。不要在客户端设置 `insecure: true`。
默认会把 sing-box 全局日志级别从 `info` 收紧为 `warn`，避免长期记录常规连接目标；需要临时
排障时可显式传入 `HY2_REDUCE_LOGS=false` 保留原级别。
当连接地址与证书域名不同时，分别设置 `HY2_SERVER_ADDRESS` 与 `HY2_DOMAIN`。迁移一个从未
对公网开放的旧 HY2 入站时，可显式传入原标签并用 `HY2_REPLACE_USERS=true` 轮换旧凭据；
该选项会使原 HY2 客户端失效，不能用于仍在使用的入口。

## Client Guidance

Reality 主力节点：

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

VM 检查：

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo ss -lunp | grep ':443 '
sudo systemctl status sing-box --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
```

## Safety

- 不要提交 Cloudflare API Token、UUID、Reality private key、证书私钥。
- 不要长期全局走 GCP 节点。
- 不要开启 Cloudflare 小黄云、Argo、WARP 或 CDN 回源，除非重新评估成本和路径。
- 不要把 `403 speed test` 当作唯一故障信号；实际访问和端口握手更重要。
