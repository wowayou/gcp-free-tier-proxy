# VLESS / HY2 节点选择指南

更新时间：2026-10-22

这份文档用于回答一个核心问题：在当前 Google Cloud 免费层 VM 里，什么时候用 HY2，什么时候用
`VLESS Reality Vision` 或 `VLESS TCP+TLS`，以及端口、DNS、防火墙、客户端分流应该怎么选。

## 结论先行

代码实现已确定的新目标是：

| 用途 | 推荐选择 | 原因 |
| --- | --- | --- |
| 主用受限入口 | 官方 `Hysteria 2` 独立服务，`443/udp` | 有独立账户、个人月额度和定向封禁；单人封禁不重启全服 |
| 本人备用 | `VLESS Reality Vision`，`47370/tcp` | 不依赖域名证书，继续留在 sing-box |
| 备用证书节点 | `VLESS TCP+TLS`，优先 `443/tcp` | 真实证书，客户端配置直观，适合作第二备用 |
| 普通网页/下载/视频 | 不走这个 GCP 节点 | 避免烧 Standard Tier 出站额度，也降低出口异常和风控概率 |
| Cloudflare 小黄云/CDN/Argo/WARP | 不使用 | 当前目标是低成本、低复杂度、可控出站路径 |

一句话：**HY2 做受限主用，Reality/TLS 做本人备用；AI 域名走代理，其他流量默认不要压到 GCP。**

> 当前生产仍未迁移；旧 sing-box 内置 HY2 只是历史试验入口。迁移完成前，以 `IMPLEMENTATION.md` 的当前基线为准。

## 三类节点怎么选

### 选择 HY2 作为主用

使用官方 `Hysteria 2` 独立服务，当你满足这些条件：

- 需要给本人和朋友分别设置账户和月额度。
- 希望单人超额/禁用时只影响该人，不重启代理。
- 能接受 UDP 443 的暴露面，且客户端网络不封锁 QUIC/UDP。
- 愿意维护本地 SQLite、证书同步和健康联动。

HY2 主用不代表全局代理，也不替代整机熔断。个人额度只是软限额，整机 180 GiB 保护仍然保留。

### 选择 Reality 作为备用

继续保留 `VLESS Reality Vision`，当你满足这些条件：

- 现在这个节点已经能测出延迟，实际网站访问也能跑。
- 你主要访问 AI 服务，想保留一个不依赖域名证书的备用入口。
- 你能正确保存客户端的 Reality `PublicKey`、`ShortId`、`SNI`。

Reality 节点的关键点：

```text
port: 47370
network: tcp
tls: reality
sni: apple.com
flow: xtls-rprx-vision
encryption: none
```

Reality 不读取：

```text
/root/ygkkkca/fullchain.cer
/root/ygkkkca/cert.crt
/root/ygkkkca/private.key
```

所以不要用“证书有没有生效”来判断 Reality 是否正常。Reality 的验收重点是：

- `sing-box` 服务正常。
- `47370/tcp` 正在监听。
- Google Cloud 防火墙开放了 `47370/tcp`。
- 客户端 Reality 参数匹配服务端。
- 实际 AI 网站可访问。

### 选择 TLS 作为第二备用

新增 `VLESS TCP+TLS`，当你满足这些条件：

- 你已经有自己的域名和 Let’s Encrypt 证书。
- 你想要一个使用真实证书的备用节点。
- 你的客户端或网络环境对普通 TLS 更友好。
- 你愿意维护证书续签和 DNS 解析。

TLS 节点的关键点：

```text
port: 443
network: tcp
tls: tls
sni: 你的证书域名
flow: 留空
encryption: none
```

TLS 节点会读取：

```text
/root/ygkkkca/fullchain.cer
/root/ygkkkca/private.key
```

如果 `fullchain.cer` 不存在，脚本会回退使用：

```text
/root/ygkkkca/cert.crt
```

TLS 节点适合作第二备用；Reality 本身也保留为备用，不把稳定路径推倒重来。新增 TLS 的收益是多一个入口，而不是替代 HY2 的账户和额度边界。

## TCP+TLS、WS+TLS、Nginx 怎么选

### 默认选 TCP+TLS

当前方案选择 `VLESS TCP+TLS`，不加 Nginx，不加 WebSocket。

优点：

- 组件最少，只有 `sing-box` 一个服务。
- 证书直接由 sing-box 读取。
- 出问题时排查路径短。
- 熔断器停止 `sing-box` 后，Reality 和 TLS 会一起停；HY2 由同一熔断器单独停止。

缺点：

- 它不是一个真正的网站入口。
- 浏览器直接访问域名时，不会得到完整网页体验。

对你当前目标来说，这个取舍是合理的：你要的是稳定代理入口，不是建站。

### 什么时候才选 WS+TLS+Nginx

只有在这些条件成立时才考虑：

- 你确实要同一个 `443` 同时承载网站和代理。
- 你需要 Nginx 返回正常网页，代理只挂在某个 WebSocket path 下。
- 你愿意维护 Nginx 配置、反代、证书 reload、path、header 等额外变量。

它的代价是：

- 组件更多。
- 故障面更大。
- WebSocket 有额外封装。
- 排查时要同时看 Nginx 和 sing-box。

所以当前不默认选它。

### 什么时候选自定义端口 TLS

如果 `443` 已被其他服务占用，又不想动那个服务，就选 `8443`。

优点：

- 不打断已有服务。
- 防火墙和客户端配置都直观。

缺点：

- 非标准 HTTPS 端口。
- 某些网络环境可能对非标准端口更敏感。

当前脚本的规则是：优先 `443`，如果 `443` 被非 sing-box 服务占用，就回退 `8443`。

## 端口怎么选

| 端口 | 用途 | 建议 |
| --- | --- | --- |
| `443/udp` | 官方 HY2 主用入口 | 固定端口，不做端口跳跃；账户凭据和证书必须正确 |
| `47370/tcp` | Reality 备用节点 | 保持不动 |
| `443/tcp` | TLS 证书备用节点 | 优先使用 |
| `8443/tcp` | TLS 回退端口 | 仅在 `443` 冲突时使用 |
| `80/tcp` | ACME HTTP challenge 或网站 | 当前不需要，DNS-01 签证书不依赖它 |

不要为了“看起来统一”把 Reality 改到 `443`，也不要把 HY2 和 TLS 的 UDP/TCP 端口混为一谈。

## DNS 和 Cloudflare 怎么选

当前推荐：

```text
Cloudflare DNS: 灰云
A 记录: 指向 VM IPv4
AAAA 记录: 没有 IPv6 就不要配
SNI: 证书里的真实域名
```

不要开启：

```text
Cloudflare 小黄云
Argo Tunnel
WARP
CDN 回源
```

原因：

- 小黄云会让客户端连接 Cloudflare IP，不是直接连你的 VM。
- 回源和隧道会增加路径复杂度。
- 成本和流量统计不如直连清楚。
- 当前目标是低成本、低复杂度、可预测维护。

如果证书是泛域名，例如：

```text
example.com
*.example.com
```

客户端 SNI 应填写实际使用的域名，例如：

```text
rr.example.com
```

不要填写：

```text
*.example.com
```

## 防火墙怎么选

当前执行口径是：

1. 先开放公网，快速确认节点能用。
2. 跑通后，如果你有稳定出口 IP，再收紧到自己的 `/32` 或常用 CIDR。

三种选择对比：

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| 只允许本人 IP | 最不容易被扫，暴露面最小 | 换网络后要更新规则 |
| 开放公网 | 客户端最省事 | 更容易被扫描，完全依赖 UUID 和协议本身 |
| 先公网后收紧 | 验证最快，后续还能降低暴露 | 需要记得收紧 |

你当前选择的是“先公网后收紧”，这是比较务实的路线。

收紧示例：

```bash
SOURCE_RANGES="203.0.113.10/32" TLS_PORT=443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

如果回退到了 `8443`：

```bash
SOURCE_RANGES="203.0.113.10/32" TLS_PORT=8443 bash gcp-free-tier-proxy/open-vless-tls-port.sh
```

## 客户端分流怎么选

不要全局走 GCP 节点。

推荐：

```text
AI 网站完整域名组 -> ai-proxy
国内网站 -> direct
普通国外网站 -> 普通代理或 direct
视频/下载/系统更新 -> 不走 GCP 免费层节点
```

AI 相关域名至少包括：

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

原因：

- GCP Standard Tier 出站额度有限。
- 同一个 AI 账号最好保持出口一致。
- 视频和下载很容易把 180 GiB 熔断打满。
- 全局代理会让问题排查变复杂。

## 费用和 180 GiB 熔断怎么影响选择

`gcp-egress-guard` 看的是 VM 默认网卡的 `tx_bytes`，也就是这台 VM 发出去的流量。

它不会区分：

```text
HY2 流量
Reality 流量
TLS 流量
系统更新
SSH 回包
其他进程出站
```

只要是默认网卡出站，都会计入本月累计。

达到阈值时：

```text
150 GiB: 写 warning 日志
180 GiB: stop + mask sing-box 和 hysteria-server
```

Reality 和 TLS 在同一个 `sing-box` 服务里，HY2 是独立服务；安装账户后端时必须在
`SERVICE_NAMES` 中同时列出两个服务，避免只停 sing-box 而让 HY2 继续跑。开机 generator 和
启动门禁也要同时覆盖两个服务。

## 推荐决策树

按这个顺序判断：

```text
1. 需要给多人分配独立账户和月额度？
   -> 使用官方 HY2 + hy2-accountd；按维护窗口迁移。

2. 只是本人需要一个不依赖证书的备用入口？
   -> 保留 Reality 47370，别动主用 HY2。

3. 想多一个使用真实证书的备用入口？
   -> 新增 VLESS TCP+TLS。

4. 443/tcp 空闲？
   -> TLS 用 443。

5. 443/tcp 被已有服务占用？
   -> TLS 用 8443，不强行抢占；443/udp 仍可给 HY2。

6. 有稳定公网出口 IP？
   -> 验证后把防火墙收紧到你的 IP。

7. 没有稳定出口 IP？
   -> 可以公网开放，但必须使用独立强凭据、个人额度和整机熔断。

8. 想让域名同时像网站一样访问？
   -> 再考虑 Nginx + WS+TLS；当前不是默认方案。
```

## 最终建议

迁移完成后的目标结构：

```text
HY2 443/udp: 主用受限入口，独立账户和月额度
Reality 47370/tcp: 本人备用
TLS 443/8443/tcp: 第二备用证书节点
hy2-accountd: 账户、统计、定向封禁、只读页
gcp-egress-guard: 同时保护 hysteria-server 和 sing-box
Cloudflare: 灰云 DNS
客户端: AI 分流，不全局
```

迁移前的生产结构仍是 Reality 主力 + 旧 HY2 试验入口；不要把目标结构当成已经部署。这套组合的
目标不是“最花哨”，而是低成本、路径短、个人可限额、故障边界清楚、能长期维护。
