# VLESS Reality 慢速诊断与 Hysteria 2 评估门禁

更新时间：2026-10-14

## 当前结论

仓库已经提供通过现有 sing-box 并行增加 Hysteria 2 的可回滚脚本。每次实际部署或扩大来源范围前，
仍必须完成账户级账单与当月流量核验；核验通过后才开放 `443/udp` 做小流量实测，未通过时不扩大流量使用。

Hysteria 2 是基于 QUIC（Quick UDP Internet Connections，基于 UDP 的加密传输协议）的代理协议。
它可能改善高往返时延、存在丢包的 TCP 路径，但不是普遍加速器。当前 VM 是共享核心的
`e2-micro`，而 QUIC 在用户态运行，通常比内核 TCP 消耗更多 CPU；如果慢速来自 CPU 抢占、
目标站限速或 Standard Tier 路由，增加 Hysteria 2 可能没有收益，甚至更慢。

在同一 VM 和现有外部 IP 上增加 Hysteria 2 不需要新建 VM、负载均衡器或 Cloud NAT；但它会扩大
UDP 暴露面，并可能因更高吞吐或丢包补偿增加出站字节。Google Cloud 的免费额度不是硬上限，
因此无法承诺严格的零额外费用。

## 第一门禁：计费和流量

在 Cloud Shell 中执行只读检查：

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export VM_NAME="YOUR_VM_NAME"
export ZONE="YOUR_ZONE"
bash gcp-free-tier-proxy/audit-cost-and-usage.sh | tee cost-usage-audit.txt
bash gcp-free-tier-proxy/audit-leftovers.sh | tee leftovers-audit.txt
```

然后在 Google Cloud Console 的 **Billing > Reports** 中使用：

- 时间：本日历月截至当前。
- 过滤：当前项目。
- 分组：SKU。
- 留证：Gross cost、Credits、Net cost，以及所有非零 SKU。

只有以下条件同时满足，才能进入小流量试验：

- 项目账单已启用且没有意外 SKU。
- VM 位于 `us-west1`、机器为 `e2-micro`、磁盘不超过 30 GB `pd-standard`。
- 外部 IPv4 的 `networkTier` 是 `STANDARD`。
- 没有闲置静态 IP、快照、孤儿磁盘、转发规则、Cloud NAT、Cloud Router 或 Cloud DNS zone。
- 本月 VM 发送量低于 `150 GiB`，且 `gcp-egress-guard` 正常运行。
- 已知标准 VM 使用中的外部 IPv4 当前标价为 `$0.005/小时`，每个账户每月只有 1 小时免费；
  720 小时约为 `$3.60/月`（未计税、汇率或合同价格）。现有架构本身不是严格的 0 美元方案。

`compute.googleapis.com/instance/network/sent_bytes_count` 是 VM 所有网络发送字节的近似值，不是
结算用量。它适合作为保守门禁，但最终以 Billing 报表为准。现有熔断器也有边界：如果它不是从
月初持续运行，其状态文件会漏掉安装前或状态丢失期间的流量。

Billing Reports 的费用明细可能延迟超过 24 小时；普通 alerts-only budget 只通知，不会停止
Compute Engine。Google 当前的 spend cap 仅支持少数 API 服务，不包含 Compute Engine，因此不能
用预算代替 VM 内熔断。

## Reality 慢速的排查顺序

先在 VM 上保存两份只读快照：空闲时一份、发生慢速或测速时一份。

```bash
sudo bash /tmp/collect-performance-baseline.sh | tee baseline-utc.txt
```

不要上传原始 sing-box 配置、分享链接、UUID、Reality 私钥或证书私钥。

| 现象 | 更可能的原因 | 判断方法 | 是否值得测 Hysteria 2 |
| --- | --- | --- | --- |
| 高峰期 RTT、抖动和丢包一起上升 | 本地 ISP 到 us-west1 的公网路由拥塞 | 三个时段各测至少 20 次；比较直连与代理 | 值得做受控试验 |
| 丢包出现时 TCP 吞吐明显塌陷 | TCP 队头阻塞和拥塞控制 | 对照丢包、TTFB、吞吐曲线 | 值得做受控试验 |
| VM CPU 高、steal 高或内存紧张 | `e2-micro` 共享核心受限 | 对比空闲/负载快照 | 不优先；QUIC 可能更差 |
| 只有某个 AI 服务慢 | 目标站、出口 IP 或账号侧限制 | 同协议比较多个小请求目标 | 通常不值得 |
| Reality 和 VLESS TLS 同时慢 | 公网路径、VM 或目标站，而非 Reality 特有问题 | 同时段比较现有两个 TCP 入站 | 先修基础路径 |
| UDP 443/自定义 UDP 端口不可达或严重限速 | 客户端网络限制 UDP/QUIC | 临时、限源开放后做握手测试 | 不值得长期部署 |
| 仅大文件慢，交互式 AI 正常 | 带宽或测试目标问题 | 以真实 AI 交互为主，不看单一测速站 | 不扩大流量 |

Standard Tier 主要走公网，而不是依赖 Google 全球骨干把流量送到靠近用户的边缘；跨太平洋公网
路径质量因此是首要假设。Reality 的 TLS 伪装和 Vision 流控本身不应先被当作根因。

## 公平的 A/B 测试

测试必须在同一台 VM、同一客户端、同一网络和同一目标上进行。保持 Reality `47370/tcp`
不动；Hysteria 2 只作为并行候选，试验流量上限为 `2 GiB`。

1. 在低峰、高峰和另一个普通时段各做一轮。
2. 每轮交替执行 Reality、Hysteria 2，各至少 20 个小请求，避免先后顺序偏差。
3. 客户端 Hysteria 2 只转发 TCP，避免把 HTTP/3 套在 Hysteria 的 UDP 转发里。
4. 记录成功率、连接时间、首字节时间（TTFB）、总时间和字节数。
5. 同步记录 RTT、抖动、丢包，以及 VM 的 CPU、内存、发送字节和 sing-box RSS。
6. 不用 `403` 的测速 URL 作为唯一判据；以实际 AI 访问和小请求为主。

建议把每条结果保存为 CSV，至少包含：

```text
timestamp_utc,window,protocol,target,success,connect_ms,ttfb_ms,total_ms,bytes,cpu_pct,rss_mib,loss_pct,rtt_ms
```

为避免隐私泄露，目标字段只写服务类别或主域名，不记录 URL 路径、查询参数、提示词或响应内容。

## 是否保留 Hysteria 2 的验收标准

全部满足才保留：

- 三个时段总体成功率不低于 Reality，差值不超过 1 个百分点。
- 高峰期 P95 TTFB 至少改善 20%，或受控吞吐至少改善 30%。
- 改善在至少两轮中重复出现，而不是单次峰值。
- sing-box 持续 CPU 低于 70%，可用内存保持 128 MiB 以上，无 OOM、重启或明显抖动。
- 试验发送量不超过 2 GiB，本月累计仍低于 150 GiB。
- 不需要端口跳跃、第三方面板、反向代理伪装站或额外 GCP 服务。

任何一项不满足，就关闭 UDP 防火墙并删除 Hysteria 2 入站，继续保留 Reality 主线。

## 通过门禁后的最小部署

执行前先把 `audit-cost-and-usage.sh` 输出的本月已用 GiB 向上取整，修复/安装熔断器。示例中的
`12` 必须替换为真实值：

```bash
sudo INITIAL_USED_GIB=12 LIMIT_GIB=180 START_NEXT_MONTH=false \
  bash /tmp/install-egress-guard.sh
```

在 VM 上加入 HY2。脚本会备份配置、检查 sing-box `1.11+`、证书域名、UDP 占用，执行
`sing-box check`，失败时自动回滚：

```bash
sudo HY2_DOMAIN="rr.example.com" HY2_USER_NAME="friend-1" \
  bash /tmp/add-hysteria2-inbound.sh
```

同时上传 `manage-hysteria2-users.sh` 时，部署脚本会安装持久化管理命令。后续新增使用者无需再次
填写端口、证书或标签，只运行 `sudo sing-box-hy2-user add friend-2`；导入链接写入 root-only
文件，不在终端回显。

然后在 Cloud Shell 开放防火墙；固定出口优先使用 `/32`：

```bash
CONFIRM_COST_AUDIT=yes SOURCE_RANGES="203.0.113.10/32" HY2_PORT=443 \
  bash gcp-free-tier-proxy/open-hysteria2-port.sh
```

- 继续使用现有 `sing-box` 进程；部署前先核实版本支持 Hysteria 2。
- 复用现有受信任证书，不使用 `insecure=true`，校验证书 SNI。
- 使用独立的高强度随机密码，不复用 UUID、Reality key 或 Cloudflare Token。
- 单一固定端口 `443/udp`；它能与已有 `443/tcp` 共存，不使用端口跳跃。相比不知名端口，
  `443/udp` 更容易通过只允许常见端口的网络；它不是隐身措施，安全仍依靠 TLS、强密码与限源。
- 防火墙优先限制到使用者固定公网 CIDR；无法限源时只在试验窗口临时开放。
- 客户端 outbound 设置 `network: tcp`，不通过 Hysteria 2 转发 UDP/HTTP3。
- 首轮不启用 Salamander/Gecko 混淆，不配置 masquerade 反代，不接入 Realm 或管理面板。
- 首轮留空 `up_mbps`/`down_mbps`，使用 BBR 拥塞控制，避免不准确的 Brutal 带宽值造成
  额外丢包补偿和流量放大；只有实测证明需要时才单独评估 Brutal。
- 不盲目修改 UDP buffer、MTU 或进程优先级；先用默认值测量，再一次只改一个变量。
- 保持 `gcp-egress-guard` 保护同一个 `sing-box` 服务。

Hysteria 2 不是天然比 Reality 更隐蔽。sing-box 官方明确提示 UDP 代理的流量特征可能比 TCP
更明显。如果使用环境对协议识别风险敏感，应直接放弃 Hysteria 2；不要为伪装而引入第三方反代，
以免增加访问元数据暴露和维护面。

## 回滚标准

部署动作必须先生成带 UTC 时间戳的 sing-box 配置备份，并执行：

```bash
sing-box check -c CONFIG_PATH
```

检查失败时立即恢复备份，不重启服务。试验失败时按以下顺序回滚：

1. 删除或禁用 Hysteria 2 的 GCP UDP 防火墙规则。
2. 从 sing-box 配置删除 `hysteria2` 入站，或恢复部署前备份。
3. 再次执行 `sing-box check` 后重启 `sing-box`。
4. 验证 `47370/tcp` 仍监听、Reality 实际访问正常、UDP 试验端口不再监听。
5. 验证 `gcp-egress-guard.timer` 仍为 active，并保存回滚后的诊断快照。

Reality 配置、端口、客户端参数和防火墙规则在整个试验中都不得替换或删除。

## 官方依据

- Google Cloud Free Tier：<https://docs.cloud.google.com/free/docs/free-cloud-features>
- Google Cloud VPC 网络价格：<https://cloud.google.com/vpc/network-pricing>
- Google Cloud Billing Reports：<https://docs.cloud.google.com/billing/docs/how-to/reports>
- Google Cloud spend cap 限制：<https://docs.cloud.google.com/billing/docs/how-to/budgets-spend-caps>
- Cloud Monitoring `sent_bytes_count`：<https://docs.cloud.google.com/monitoring/api/metrics_gcp_c>
- Hysteria 2 协议：<https://v2.hysteria.network/docs/developers/Protocol/>
- Hysteria 2 性能说明：<https://v2.hysteria.network/docs/advanced/Performance/>
- Hysteria 2 关于 HTTP/3：<https://v2.hysteria.network/docs/misc/About-HTTP3/>
- sing-box Hysteria 2 入站：<https://sing-box.sagernet.org/configuration/inbound/hysteria2/>
- sing-box Hysteria 2 出站：<https://sing-box.sagernet.org/configuration/outbound/hysteria2/>
