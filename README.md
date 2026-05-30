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
| 备用节点 | `VLESS TCP+TLS` |
| TLS port | 优先 `443/tcp`，冲突时 `8443/tcp` |
| 证书目录 | `/root/ygkkkca` |
| 熔断阈值 | `180 GiB/月` |

## Read First

- [IMPLEMENTATION.md](./IMPLEMENTATION.md)：完整维护手册和执行命令。
- [CHOOSING-VLESS-NODES.md](./CHOOSING-VLESS-NODES.md)：Reality、TLS、端口、DNS、防火墙、分流如何选择。
- [AGENTS.md](./AGENTS.md)：给后续 AI/维护者的项目边界和执行规则。

## Files

| 文件 | 运行位置 | 作用 |
| --- | --- | --- |
| `create-vm-standard-ipv4.sh` | Cloud Shell | 创建推荐的 GCP 免费层 VM 和基础防火墙 |
| `open-vless-reality-port.sh` | Cloud Shell | 给 VM 加标签并开放 `47370/tcp` |
| `open-vless-tls-port.sh` | Cloud Shell | 开放 TLS 节点端口，默认 `443/tcp` |
| `create-budget-alert.sh` | Cloud Shell | 创建项目级预算提醒 |
| `audit-leftovers.sh` | Cloud Shell | 只读检查可能继续计费的残留资源 |
| `install-egress-guard.sh` | VM | 安装 180 GiB/月出站流量熔断器 |
| `issue-acme-cloudflare-token.sh` | VM | 使用 Cloudflare API Token 申请/安装证书 |
| `add-vless-tls-inbound.sh` | VM | 在现有 sing-box 配置里新增 `VLESS TCP+TLS` 入站 |
| `verify-singbox-runtime.sh` | VM | 只读诊断 sing-box、端口、证书、熔断器 |
| `sing-box-client-ai-routing.fragment.json` | 本地客户端 | AI 分流模板 |
| `sing-box-client-vless-tls.fragment.json` | 本地客户端 | VLESS TLS 节点模板 |

## Quick Start

在 Cloud Shell 设置固定变量：

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export VM_NAME="YOUR_VM_NAME"
export ZONE="YOUR_ZONE"
```

开放 Reality 主力端口：

```bash
bash gcp-free-tier-proxy/open-vless-reality-port.sh
```

开放 TLS 备用端口：

```bash
TLS_PORT=443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
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
  gcp-free-tier-proxy/add-vless-tls-inbound.sh \
  gcp-free-tier-proxy/verify-singbox-runtime.sh \
  "$VM_NAME":/tmp/ \
  --zone "$ZONE" \
  --project "$PROJECT_ID" \
  --tunnel-through-iap
```

在 VM 内执行：

```bash
sudo LIMIT_GIB=180 SERVICE_NAME=sing-box bash /tmp/install-egress-guard.sh
sudo bash /tmp/add-vless-tls-inbound.sh
sudo bash /tmp/verify-singbox-runtime.sh
```

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

它每 5 分钟读取默认网卡：

```text
/sys/class/net/<iface>/statistics/tx_bytes
```

达到 `150 GiB` 时写 warning；达到 `180 GiB` 时：

```bash
systemctl stop sing-box
systemctl mask sing-box
```

下个月 UTC 月份变化后会重置计数，并尝试重新启动 `sing-box`。

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
```

VM 检查：

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo systemctl status sing-box --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
```

## Safety

- 不要提交 Cloudflare API Token、UUID、Reality private key、证书私钥。
- 不要长期全局走 GCP 节点。
- 不要开启 Cloudflare 小黄云、Argo、WARP 或 CDN 回源，除非重新评估成本和路径。
- 不要把 `403 speed test` 当作唯一故障信号；实际访问和端口握手更重要。
