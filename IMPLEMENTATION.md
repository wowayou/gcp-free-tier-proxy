# Google Cloud 免费层 sing-box Reality 节点最终维护手册

更新时间：2026-05-30

这份手册是当前项目的最终维护口径：只把 Google Cloud 免费层 VM 当作
**VLESS Reality Vision AI 专用节点** 使用，不做全局代理，不走 Cloudflare 小黄云、
Argo、WARP、CDN 回源。

## 当前维护基线

| 项目 | 当前值 |
| --- | --- |
| Google Cloud project | `YOUR_PROJECT_ID` |
| VM name | `YOUR_VM_NAME` |
| Zone | `YOUR_ZONE` |
| 主力协议 | `VLESS Reality Vision` |
| 主力端口 | `47370/tcp` |
| Reality SNI | `apple.com` |
| VLESS encryption | `none` |
| VLESS flow | `xtls-rprx-vision` |
| 备用证书节点 | `VLESS TCP+TLS`，默认 `443/tcp`，冲突时回退 `8443/tcp` |
| 流量保护 | `gcp-egress-guard`，默认 180 GiB/月熔断 |

继续保持禁用：

- `WARP-plus-Socks5`
- `Argo` / Cloudflare Tunnel
- Cloudflare 小黄云代理
- CDN 回源
- Cloud NAT、Load Balancer、Cloud DNS

当前不维护为主力节点：

- `Vmess-ws`
- `Hysteria-2`
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
- `install-egress-guard.sh`：VM 中执行，安装月度出站流量熔断器。
- `verify-singbox-runtime.sh`：VM 中执行，只读检查 sing-box、端口、熔断器和证书文件。
- `issue-acme-cloudflare-token.sh`：可选工具，仅给普通 TLS/WS/网页服务申请证书；Reality 不依赖它。
- `add-vless-tls-inbound.sh`：VM 中执行，在现有 sing-box 配置中新增 `VLESS TCP+TLS` 入站。
- `open-vless-tls-port.sh`：Cloud Shell 中执行，开放新增 TLS 入站端口。
- `sing-box-client-ai-routing.fragment.json`：客户端 sing-box 分流模板。
- `sing-box-client-vless-tls.fragment.json`：客户端 VLESS TLS 节点模板。
- `CHOOSING-VLESS-NODES.md`：详细说明 Reality、TLS、端口、DNS、防火墙和分流如何选择。

## Cloud Shell 维护命令

先设置固定变量：

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export VM_NAME="YOUR_VM_NAME"
export ZONE="YOUR_ZONE"
export PROXY_PORT="47370"
```

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
  gcp-free-tier-proxy/add-vless-tls-inbound.sh \
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
sudo LIMIT_GIB=180 SERVICE_NAME=sing-box bash /tmp/install-egress-guard.sh
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

手动检查端口：

```bash
sudo ss -lntp | grep 47370
sudo ss -lntp | grep -E ':(443|8443) '
```

手动检查服务：

```bash
sudo systemctl status sing-box --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo journalctl -t gcp-egress-guard -n 50 --no-pager
```

## 客户端配置

v2rayN / 类似客户端手工填写时，VLESS Reality 主力节点应保持：

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

所以 Let’s Encrypt 证书申请成功与否，不影响当前 `47370/tcp reality` 主力节点。
`issue-acme-cloudflare-token.sh` 只保留给普通 TLS/WS/网页服务使用。

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
