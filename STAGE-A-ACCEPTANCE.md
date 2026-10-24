# HY2 账户系统阶段验收记录

更新时间：2026-10-24

状态：**本地修复与复验通过（第 8–10 节）；授权生产服务端切换、真实客户端访问、最终只读核验及临时凭据清理通过（第 11 节），本次单账户迁移收尾完成。** 后续恢复补强本地复验见第 14 节；第 15 节记录配套程序更新、审核终端故障及后续受控恢复：**09:57 UTC 恢复成功、补账已提交，普通交付续接后用户回贴 `FRIEND_READY`，30 GiB 朋友账户与私密文件已准备**。下载/私下发送、升级后客户端访问及真实多账户封禁隔离仍未确认；故障/重启/容量等未覆盖项保留，不签署“全部通过”。

**最新r3收尾（2026-10-24 UTC）：** 用户回贴 `R3_RECOVERED`，服务端认证/统计/核心正常，owner120/朋友30/池150 GiB，新页面已上线；owner当时剩37.857769 GiB。本次审核增量已单次提交，朋友凭据沿用。真实客户端访问仍未回贴，详见第18–19节；不重跑部署或恢复。

## 1. 验收范围

第 1–10 节记录本地阶段：只验证代码、脚本和固定版本官方核心的本地行为，不接触 GCP、真实 VM、真实证书、真实账户或 systemd 生产单元。第 11 节另记之后获授权的生产操作，不把现场结果倒写为本地测试覆盖。

通过标准：

- 账户/会话隔离、额度、统计游标、恢复/故障边界有可重复测试。
- 官方 v2.13.0 核心在回环环境中验证多设备、空闲连接、认证竞争和目标账户定向封禁。
- 整机保护覆盖两个服务，开机/启动门禁不能靠重启绕过。
- 迁移工具拒绝猜测，文档顺序不会先启动会争抢 UDP 443 的服务。
- 仓库无真实凭据、无私自扩大计费资源。

## 2. 环境

下表保留初次阶段验收环境；A3/A4 本地复验环境分别见第 9–10 节。

| 项目 | 值 |
| --- | --- |
| 主机 | Ubuntu 24.04 LTS x86_64（本地 WSL） |
| Go | 1.27.3 |
| 构建 | `CGO_ENABLED=0`，静态链接 |
| 官方核心 | Hysteria v2.13.0，commit `ca8fbd874fd6ca82413948de6dc5924405e103e1` |
| 核心校验和 | `907ba8c9693edb104b20582681fb7dc15639d5b64a9cbb616a7b539190a86691`（linux-amd64） |
| 测试数据 | 临时目录、假证书、假账户；全部数据路径为回环 |
| 工作区注意事项 | 仓库位于 Windows DrvFs，`stat` 可能显示 0777；安装器会显式设置 `0644/0640/0600/0755/0700`。`systemd-analyze verify` 的 world-writable 警告来自挂载属性，不是安装后的权限 |

## 3. 执行结果

```text
cd hy2-account
gofmt -l .                                            无输出
go vet ./...                                         PASS
go test ./...                                        PASS
HY2_BIN=<pinned v2.13.0> go test -race ./...          PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=3 ...   PASS（重复核心隔离测试）
go test -cover ./...                                 internal 68.2%，cmd 5.6%（CLI 由 shell/VM 现场覆盖）
CGO_ENABLED=0 go build -trimpath ...                 PASS（静态 ELF）
/tmp/hy2-account-build version                       hy2-account 1; core v2.13.0 ca8fbd8...

python3 -m unittest tests.test_egress_guard           PASS（20，含 A2 状态发布失败 2 项）
python3 -m unittest tests.test_remove_singbox_hy2     PASS（4）
python3 -m unittest tests.test_sync_hysteria_cert     PASS（17，A3 复验；unshare -r；A2 的 5 项未覆盖全部证书反例）
python3 -m unittest tests.test_install_hy2_account    PASS（2，A2 新增；unshare -r）
HY2_BIN=<pinned v2.13.0> python3 tests/hy2_core_isolation.py  PASS
bash -n *.sh egress-guard/*.sh hy2-account/systemd/*.sh       PASS
JSON 模板校验                                        PASS
Markdown 链接/代码围栏/UTF-8                         PASS
git diff --check                                     PASS
```

官方核心独立可行性脚本输出：

```text
PASS: official v2.13.0; four targeted sessions (multi-device/idle/auth race);
owner original TCP + QUIC connection unchanged; cumulative tx/rx verified;
loopback <1 MiB
```

## 4. 覆盖到的验收点

| 场景 | 结果 | 证据 |
| --- | --- | --- |
| 账户 A 多设备，账户 B 长连接；禁用 A | PASS | 官方核心回环测试：A 4 条会话（含空闲/认证竞争）被处理，B 原 TCP+QUIC 连接 ID 不变 |
| A 达到个人额度 | PASS | 单元测试 + 官方核心额度测试，只封 A，不影响 B |
| 认证与禁用竞争 | PASS | 已签发但未上线的会话也进入待封禁集合 |
| 同一来源 IP 不连带 | PASS | 回环测试中 owner 与 friend 同源，只处理目标账户 |
| `/kick` 超时/部分失败 | PASS | 最多 3 次有限重试，保留禁用和未完成任务，无全服重启兜底 |
| 统计读取与落库失败 | PASS | 累计值 + 游标同事务；注入事务失败后重读不重复计数 |
| 控制程序重启 | PASS | 重开数据库继续累计，不重置月额度 |
| 计数回退/未知会话/核心代次变化 | PASS | 进入 review/hold，不把异常解释为零用量 |
| 恢复旧备份/witness 不一致 | PASS | 认证关闭、页面标注核验；显式补账后才恢复 |
| 跨月、手工禁用、撤销 | PASS | 月账本保留；禁用/撤销不因跨月恢复；姓名/凭据墓碑不可复用 |
| 减额度低于已用量 | PASS | 立即排队封禁目标账户，不抹掉已用量 |
| 整机保护覆盖两个服务 | PASS（A2） | fake systemctl 回归：逐一 stop/mask，单个失败不跳过另一个；状态文件发布失败时两服务均停止并 mask（A2 前会静默少计） |
| VM/服务重启绕过 | PASS（A2） | 开机 generator + `ExecStartPre` 许可门禁；blocked 状态不能被普通启动绕过；状态未持久化时 `--permit` 拒绝（A2 前会放行） |
| 个人踢出失败触发共享停服 | PASS | 个人失败不改变 review/hold；共享统计故障才由独立 health 服务停 HY2 |
| ACL 私网/元数据/映射地址 | PASS | 单元契约 + 官方核心 ACL 测试；公共 IPv4 正向可用 |
| 业务 UDP 禁用/证书校验 | PASS | `disableUDP: true`、`insecure: false`、`sniGuard: strict`、BBR、无 Brutal |
| 证书同步 SAN/有效期/密钥匹配 | PASS（A3，本地） | 暂存副本以 Go/HY2 规则校验；仅 CN、未来生效、复制前/中途换源均不能替换正常 `current`；精确与完整左标签通配 SAN 可发布；故障清理与校验器缺失拒绝已测（A2 结论已由第 9 节补正） |
| 旧 sing-box HY2 移除工具 | PASS | 只删确认属于旧入站的规则；歧义引用/无 tag/无 HY2 时拒绝 |
| 旧入口命令阻断 | PASS | 安装账户后端后拒绝 legacy 创建/用户管理 |
| 安装前 guard 覆盖门禁 | PASS（A2） | 精确 unit 名；`sing-box-old hysteria-server-old` 等相似名或单服务列表在任何变更前拒绝（A2 前为子串匹配） |
| 旧用户导入格式 | PASS（A2） | 真实 CLI 解析器接受文档中的 `{"users":[...]}`；裸数组、空列表、未知字段、非 0600 文件拒绝（A2 前文档格式被拒绝） |
| 文档迁移顺序 | PASS（A2） | 先释放 UDP 443，再 `system arm`、启动官方 HY2、激活；上传包保留安装器依赖的目录结构，二进制与 `hy2-account/` 不重名 |

## 5. 本地未覆盖、必须 VM 现场验收

以下是本地阶段提出的现场门槛；当前已验证的子集及仍未执行项见第 11 节，不能整组视为已通过。

- systemd sandbox 下 `CAP_NET_BIND_SERVICE` 绑定 UDP 443、`ProtectSystem`、`RuntimeDirectory`、`SupplementaryGroups` 的实际行为。
- `hy2-health.timer`、开机 generator、`gcp-egress-guard` 与真实 systemd 的联动和重启表现。预期：VM 重启后 `hy2-health` 会写入 `/var/lib/hy2-health/blocked` 并 mask HY2，直到 `system arm`；这是 fail-closed 设计，不应当作故障。
- guard 状态目录磁盘满/只读时的真实文件系统行为（本地仅用 `mv` 故障注入验证）。
- 真实证书 SAN、续签钩子、证书/私钥权限和 HY2 热加载。
- 真实客户端（多设备、网络切换、空闲连接）下的封禁延迟和重连行为。
- e2-micro 上的实际 RSS/CPU、并发、SQLite/WAL 增长和 128M/256M 限制是否足够。
- GCP 防火墙、Standard Tier 出站计费、共享免费额度、首月额度池和实际账单。
- 维护窗口内的完整迁移、回滚和备份恢复演练。

## 6. 上线前交叉核验清单

建议其他 agent 重点复核：

1. `hy2-account/internal/account/runtime.go` 的 ACL：不要整体拒绝 `::ffff:0:0/96`；确认公共 IPv4、私网、metadata、NAT64、IPv4-compatible 的行为。
2. `/var/lib/hy2-health` 目录必须可被 `hy2-accountd` 遍历（当前 `0755`），latch 文件仍为 root-only `0600`；否则会永久误判共享故障。
3. `install-hy2-account.sh` 必须先确认 guard 的 `SERVICE_NAMES` 同时覆盖 `sing-box` 和 `hysteria-server`，且 timer 已运行。
4. 迁移顺序必须是：安装/账户预置 → 删除旧 sing-box HY2 → `sing-box check` → 启动 sing-box 释放 UDP 443 → `sync-hysteria-cert` → `system arm`（约 45 秒窗口）→ 启动官方 HY2 → 采样 → `system activate`。
5. `system arm` 只清除 health latch 并重置基线；不得被普通 enable/quota 操作调用，也不得解除整机 guard block。
6. `hysteria-server` 的 `BindsTo=hy2-accountd`、`ExecStartPre` health gate、`Wants=hy2-health.timer` 与 guard drop-in 的启动顺序。
7. `hy2-accountd` 对 guard status、generation、certificate、health latch 的路径权限。
8. 旧 `add-hysteria2-inbound.sh` / `manage-hysteria2-users.sh` 在账户后端存在时是否仍能绕过。
9. 页面只读、SSH 隧道、Basic Auth、CSP；不得出现密码、链接、目标或配置。
10. 单账户 `/kick` 失败时是否只保留目标账户任务，绝不调用全局 stop/restart。
11. 文档中的示例额度、`arm` 时序、回滚原则是否与代码一致。
12. 仓库是否仍无真实凭据、数据库、证书私钥或临时产物。

## 7. 结论

本地阶段（阶段 A/B，含 A2、证书 A3 和 legacy A4 收尾）复验通过；这本身不构成上线批准。之后的独立授权生产操作、真实证书/客户端结果和未完成项见第 11 节，仍不能签署“全部通过”。

## 8. 交叉核验与 A2 修复记录（2026-10-22）

第 6 节交叉核验复现了第 3 节全部结果，另用负向测试发现以下问题。每项修复都配有回归测试，并确认该测试在修复前的代码上失败。

| # | 问题 | 影响 | 修复 | 回归 |
| --- | --- | --- | --- | --- |
| 1 | `egress-guard/runtime.sh` 的 `write_state` 以 `\|\| fail_closed` 调用，bash 在此处禁用 `set -e`，`mv` 失败被忽略 | 磁盘满/只读时仍放行启动，用量未持久化；重启后该段永久少计 | 每步显式检查并清理临时文件 | `test_unpublished_state_*`（2） |
| 2 | `sync-hysteria-cert.sh` 依赖 `openssl x509 -checkhost` 的退出码，而它在不匹配时也返回 0 | SAN 错误的证书会被发布并替换 `current`，HY2 握手失败 | 只接受精确的肯定输出；锁路径可用 `SYNC_LOCK_PATH` 覆盖以便测试 | `tests/test_sync_hysteria_cert.py`（5） |
| 3 | `hy2-admin import` 解析裸数组，文档写 `{"users":[...]}` | 迁移窗口中导入步骤必然失败 | CLI 改为文档格式并拒绝空列表 | `cmd/hy2-account` 2 项 |
| 4 | 安装器 guard 覆盖检查为子串匹配 | 相似名误通过（运行时 `ReadGuard` 精确匹配，不致漏保护） | 精确 unit 名 | `tests/test_install_hy2_account.py`（2） |
| 5 | 上传清单缺 `egress-guard/`、`hy2-account/`；`/tmp/hy2-account` 既作二进制又作目录 | 按文档操作安装器失败或路径冲突 | README/IMPLEMENTATION 改为 `/tmp/hy2-release/` 包结构 | 文档人工核对 |

A2 复验（本地离线，`GOPROXY=off`，官方核心 SHA-256 已核对）：

```text
gofmt -l . / go vet ./...                                   PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=1 ./...        PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=3 -run TestPinned PASS
go test -cover ./...（未设 HY2_BIN）                       internal 62.9%，cmd 24.3%
CGO_ENABLED=0 go build -trimpath                            PASS（静态 ELF）
Python 4 个模块                                            PASS（31）
HY2_BIN=<pinned v2.13.0> python3 tests/hy2_core_isolation.py PASS
bash -n（21 个脚本）/ JSON / git diff --check              PASS
```

A2 仍未覆盖：第 5 节全部 VM 现场项；证书同步和安装器测试在用户命名空间中运行，属主/属组交由 VM 验证。另见第 9 节：A2 的证书校验并未覆盖全部负向场景，原证书 PASS 结论已补正。

## 9. 证书残留问题与 A3 修复记录（2026-10-22）

环境表与 A2 的命令输出保留为当时记录；A3 复验实际使用本地 Go 1.26.0（`GOTOOLCHAIN=local`），未下载工具链或依赖。

| # | A2 残留问题 | A3 修复 | 回归证据 |
| --- | --- | --- | --- |
| 1 | OpenSSL 域名检查允许回退 CN；`-checkend` 不检查 `notBefore` | 账户二进制新增离线 `validate-cert`：Go TLS 密钥配对解析、SAN-only `VerifyHostname`、已生效且剩余 >24h | 仅 CN、仅 IP SAN、未来生效、通配边界与时间边界 |
| 2 | 域名/有效期只校验源文件，复制后仅比公钥 | 先复制到 root 可写的版本目录，对暂存证书/私钥一次完整校验，通过后才切换 `current` | 复制前注入错误 SAN、CN-only、未来生效、将到期和密钥不匹配；复制间换源拒绝混合配对；复制后换源不影响已验副本 |
| 3 | 旧 `add-hysteria2-inbound.sh` 只看 `-checkhost` 退出码并允许 CN 回退，错误域名可被静默接受 | A3 改为精确肯定输出和 `DNS:` 字符串存在检查；此修复不完整，A4 已改为结构化 DNS SAN 校验（见第 10 节） | A3 当时仅手动验证精确/完整通配、CN-only、普通错误 SAN、尾点；部分通配和 URI 伪造未覆盖，原结论已补正 |

三个主要反例先在 `4283e06` 的脚本上实际执行，确认仅 CN、未来生效和复制前换成错误 SAN 均返回 0 并替换正常 `current`；修复后全部拒绝，原链接和证书/私钥字节均不变。

修复后把同一组新回归对 `4283e06` 的旧脚本复跑，得到 8 个子场景失败（含仅 CN、未来生效、复制前换源），确认测试确实能捕获原缺陷，而不是只验证新实现。

同时完善发布失败清理：复制失败、PEM 无效、校验器缺失/旧版、切换前重命名失败均保留旧版本并清理未发布目录/链接；若重命名已经提交后才报错，不删除被 `current` 引用的已验证版本。成功轮换仍保留最近三个版本，文件模式保持 `0640`，不重启 HY2。

A3 实际复验（本地离线，`GOPROXY=off`、`GOSUMDB=off`；官方核心使用已有文件且 SHA-256 已核对）：

```text
gofmt -l . / go vet ./...                                   PASS
新增 Go 证书与 CLI 回归（6 项，含 SAN/时间等子场景）        PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=1 ./...        PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=3 -run TestPinned PASS
CGO_ENABLED=0 go build -trimpath                            PASS（静态 ELF）
Python 4 个模块                                            PASS（43，全部执行、无跳过）
  证书发布                                                  17（A2 为 5）
  guard / 旧入口移除 / 安装器门禁                           20 / 4 / 2
新回归在 4283e06 旧脚本上复跑                               FAIL（8 个子场景，符合预期）
HY2_BIN=<pinned v2.13.0> python3 tests/hy2_core_isolation.py PASS
bash -n（21 个脚本）/ JSON（3）/ Markdown 链接与围栏 / git diff --check PASS
```

账户二进制与证书钩子需配套更新；新钩子不对缺失/旧版校验器启用 OpenSSL 回退。真实环境仍需授权维护窗口，A3 未连接 VM、未执行 GCP 操作，也未复验真实证书信任链、属主/属组、续签和热加载；第 5 节现场门槛全部保留。

A3 当时的 legacy 边界与漏检已由第 10 节补正；正式 Go 证书发布路径的上述复验仍有效。`verify-singbox-runtime.sh` 仍只对证书到期告警，属只读诊断而非发布门禁。

## 10. Legacy DNS SAN 漏检与 A4 收尾（2026-10-22）

A3 后独立验收复现两项 legacy 漏检：OpenSSL 会接受 `r*.example.com` 等部分标签通配；`DNS:` 子串判断会把仅 URI 中含 `DNS:rr.example.com` 的证书误当作 DNS SAN，并在 CN 匹配时放行。自动推断的 `sed` 解析也会从 URI 文本中选出伪造域名。第 9 节原第 3 行还多了一列，已统一为四列并补正历史结论。

`add-hysteria2-inbound.sh` 的 `detect_domain` 现通过 Python `cryptography` 结构化读取第一张叶证书的 `SubjectAlternativeName`，只取 `DNSName`：

- 指定域名：只接受传统 ASCII DNS 主机名，大小写不敏感，规范化一个尾点；精确匹配或完整左标签 `*.example.com` 匹配一个非空标签，拒绝部分/多重通配、apex 和跨层匹配。
- 未指定域名：只选择真正的非通配 DNS SAN，跳过 URI、CN、IP、email、无效/通配 DNS 名称，不从文本推断。
- 证书解析失败或依赖缺失时非零退出，不输出可用域名，不自动安装、不回退 OpenSSL 文本/CN 判断；调用点在任何备份、配置/凭据写入或服务重启前停止。

新建 `tests/test_legacy_hy2_certificate.py`（14 项），用固定 Ed25519 假证书直接提取生产函数并执行真实调用形式 `HY2_DOMAIN="$(detect_domain)"`。覆盖两项验收反例、自动推断伪造、SAN 类型混合、部分/多重通配、大小写/尾点、apex/跨层、CN/IP-only、无效输入/PEM、叶证书顺序及依赖缺失。修复前函数快照上的两个验收反例和推断伪造三个测试均实际失败（返回 0 并输出不应接受的域名）；修复后全部通过。

A4 本次实际复验：本地 Go 1.26.0、Python 3.12.3、已有 `cryptography` 41.0.7；无工具链/依赖下载，未连接 VM 或执行 GCP 操作。

```text
gofmt -l . / go vet ./...                                   PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=1 ./...        PASS
HY2_BIN=<pinned v2.13.0> go test -race -count=3 -run TestPinned PASS
CGO_ENABLED=0 go build -trimpath                            PASS（静态 ELF）
Python 5 个模块                                            PASS（57，全部执行、无跳过）
  legacy DNS SAN                                            14（新增）
  证书发布 / guard / 旧入口移除 / 安装器门禁                 17 / 20 / 4 / 2
新回归在 A3 legacy 函数快照上复跑                          FAIL（3 个行为反例，符合预期）
固定种子 DNS/SAN 差分探针（60 组合）                      PASS（legacy 接受项无 Go 拒绝）
HY2_BIN=<pinned v2.13.0> python3 tests/hy2_core_isolation.py PASS
bash -n（21）/ 嵌入 Python / JSON（3）                     PASS
Markdown UTF-8、本地文件链接、围栏、表格列数 / diff check  PASS
```

Legacy 依赖与规则已同步到 README、IMPLEMENTATION、HYSTERIA2-EVALUATION 和 AGENTS。此路径仍只做入口的域名检查，不承担 Go 发布钩子的有效期、私钥配对或可信链验证；ASCII/标签长度等输入约束刻意严于 Go 的历史兼容规则。测试仅覆盖生产函数与调用形式，不执行完整 legacy 部署或真实 systemd。安装账户后端后的 legacy 阻断、正式 Go 证书发布规则及第 5 节 VM 现场门槛全部保留。

## 11. 授权生产切换记录（2026-10-23 UTC）

证据来自用户在 Cloud Shell/VM 执行命令后回贴的输出与真实客户端确认；本地代理没有直接连接生产。授权范围为同一 VM/IP 的官方 HY2 切换，保留 Reality，切换加回退预算 30 分钟，至少预留 5 分钟回退；不新建云资源，不改 DNS/防火墙，不停止/启动 VM，不做测速或付费 API 请求。

候选包基线为 `4283e06`，包含已验收但未提交的 A3/A4 修复，不冒称仅由该提交构建。归档 SHA-256 为 `83ca78e76e885eb81f7c94720c0e4f87744db0d1f485cadf55df9dd04f67a366`；官方核心为上文校验固定的 v2.13.0。VM 上传/解包校验、私密备份与不含旧 HY2 的备用配置均先通过，保留 4 个非 HY2 入站。

| 项目 | 现场结果 | 边界 |
| --- | --- | --- |
| 账单与额度门禁 | 已复核当月费用、VM 成本属性和共享额度；用户接受现有月预算 5 美元下新增实际支出 20%（1 美元）邮件提醒 | 不是硬封顶，也不保证后续流量免费；不启用收费通知集成 |
| 双服务 guard | 升级到保护 `sing-box hysteria-server`，警告/熔断 150/180 GiB；已有整机累计保留并保守向上取整 | 不以 Monitoring 较低值覆盖本机可信累计，不清除 block/mask |
| 账户准备 | 导入唯一 owner 的现有口令，个人额度 90 GiB、池 140 GiB；准备期间认证保持关闭 | 未创建朋友账户，未引入第二个账户来源 |
| 真实证书 | 现有 ACME 证书安装到稳定路径，Go 校验暂存副本后发布；私钥/证书发布模式为 0640；续签安装钩子执行同步成功且不重启 HY2 | 没有重签/强制续签，未验证未来实际续签后的热加载 |
| sing-box 切换 | 删除旧 HY2 入站及其安全规则，结果与预检备用配置一致；`sing-box check` 通过，Reality 47370/tcp 恢复监听 | 其他 4 个入站保留；TLS 备用本次未部署，不声称 443/8443 TCP 可用 |
| HY2 启动门禁 | `arm` 后等到 bootstrap 发布，再启用 health timer、启动官方核心；首次空基线接受，`activate` 成功 | 45 秒窗口内完成；没有旁路门禁、自动重启或清零账本 |
| 首月保守补账 | owner 一次性补记 34 GiB；切换时整机 guard 显示 33.863 GiB，缺口记录 1 条 | 补账包含其他协议整机流量，不是精确个人历史；约剩 56 GiB 个人额度，不是费用保证 |
| 服务端验证 | VM 本机官方客户端代理请求返回 204，UDP 443 属于 hysteria；最终日志 `RESULT=PASS` | 不据此推断公网路径、字节级硬限额或测试流量的实际字节数 |
| 真实客户端 | 用户明确确认原 HY2 客户端能正常访问，地址/SNI/口令无需改动 | 仅确认实际访问，不等于多设备封禁隔离、网络切换或长时间稳定性测试 |
| 备份 | 迁移前私密备份及准备后、切换后数据库备份命令通过 | 未执行备份恢复演练；备份留在 VM，不能公开或作为可直接恢复旧 HY2 的配置 |
| 最终运行核验 | `RUNTIME_HEALTH=PASS`、`RUNTIME_DIAGNOSTIC=PASS`、管理接口仅回环监听、旧 HY2 不存在；150/180 GiB 双服务 guard 正常 | owner 显示已用 34.002 GiB、剩余 55.998 GiB；整机 guard 33.865 GiB，均为当时显示值 |
| 临时凭据清理 | `TEMP_OWNER_IMPORT=REMOVED`、`BACKUP_REMAINDER=PASS`、`FINAL_VERIFY=PASS` | 只删除临时明文导入文件；原清单和其余私密备份保留，不承诺物理擦除 |

计时：2026-10-23 01:22:07 UTC 开始实际切换，01:22:19 UTC 记录完成；含前置检查的脚本总耗时 14 秒，未触发回退。执行由 VM 本地临时 systemd 服务托管，SSH 仅等待并读日志；不能据总耗时推断真实客户端连接的准确中断秒数。

准备期间首次紧接安装器的导入报 `accountd socket unavailable`，脚本即停，旧 HY2 未受影响。随后有限就绪核验通过（包括未发生 accountd 自动重启的检查），再从空账户表续做导入，并未重跑安装器或重复补账。这与启动就绪竞争相符，但未独立复现现场根因；当时安装器尚无就绪等待。后续本地代码补强见第 13 节，不代表重新部署或重跑现场安装器。

收尾结果：**用户回贴 `FINAL_VERIFY=PASS`，本次单账户迁移完成，维护窗口收尾**。只读核验通过后删除了临时 `owner-import.json` 明文文件；未删除数据库、审计、其余迁移备份、证书或 guard 状态，也未重启服务。原 `SHA256SUMS` 留作审计，仍包含已故意删除的导入文件；后续核验剩余备份使用 `SHA256SUMS.after-import-cleanup`，该清单仅排除此文件且已经校验通过。虚拟磁盘/SSD 上删除文件不等于可证明的物理擦除，迁移前备份仍含敏感旧配置，必须保持私密且不可恢复旧无额度 HY2 上线。

仍未执行：真实多账户/多设备封禁隔离、故障/熔断/VM 重启演练、备份恢复、证书真正续签后的热加载、长期资源/容量验证与切换后的结算账单复核。不得为了补齐覆盖而在生产降低额度、禁用 owner、触发故障或重启 VM。

## 12. 迁移后交叉验收与维护手册补全（2026-10-23）

**结论：本次已授权的单账户迁移、实际访问及收尾验收通过；不等于全部生产故障/恢复/容量场景通过。** 本轮只在本地复核代码、已封存候选包与用户回贴的第 11 节证据，不重新连接生产、调用云 API、重启 VM/服务或新增监控自动化。当前运行状态以现场最后一次输出为证，不冒称本轮重新读取过 VM。

本轮重新执行（Go 1.26.0、Python 3.12；工具链/依赖仅用已有缓存，禁止下载）：

| 检查 | 实际结果 | 范围 |
| --- | --- | --- |
| `gofmt -l .` / `go vet ./...` | PASS | 未修改 Go 代码 |
| `go test -race -count=1 -timeout=180s ./...` | PASS | 未设 HY2_BIN 的本地单元/CLI 回归 |
| `HY2_BIN=<verified> go test -race -count=3 -timeout=180s ./internal/account -run TestPinned -v` | PASS（2 个测试各 3 次） | 固定 SHA-256 官方核心、假账户/证书、回环；隔离/额度/认证竞争和生产 ACL |
| Python 五模块回归 | PASS（57，无跳过） | guard 20、移除工具 4、证书发布 17、安装器门禁 2、legacy SAN 14；namespace-root 与 fake systemctl，不接触真实 systemd |
| `HY2_BIN=<verified> python3 tests/hy2_core_isolation.py` | PASS | 多设备/空闲/竞争定向处理，另一账户原连接保持；回环 <1 MiB |
| `CGO_ENABLED=0 go build -trimpath` / version | PASS | 本地静态 linux/amd64 ELF，未上传/替换 VM 二进制 |
| Bash 语法 / 客户端 JSON | PASS（21 / 3） | 本地静态检查，不执行运维命令 |
| 封存归档与当前源码对照 | PASS | 归档 SHA-256 与第 11 节一致；62 个源文件中 55 个逐字节相同，7 个仅为部署/运维文档更新；发行包未改写 |
| 手册恢复 gate 的本地模拟 | PASS（3 场景） | 立即就绪、第 3 次就绪、10 次均失败；仅 fake 命令，失败时不启动任何服务 |
| 手册 Bash 示例 / Markdown / diff | PASS | 42 段 Bash 示例语法；7 份文档 UTF-8/围栏/表格/本地链接与锚点；`git diff --check` |

封存包内 `RELEASE.json` 的 candidate 状态是打包时历史记录，不改写封存包来表示部署；当前结果以第 11 节及包外 `MAINTENANCE-PLAN.json` 为准。本轮（第 12 节的补强前验收）未修改运行时代码，下表当时列出的安装器待补强项之后在第 13 节处理；不能把后续本地测试写成现场安装验收。

验收发现及处置：

| 严重度/项目 | 证据与结论 | 处置 |
| --- | --- | --- |
| 高：误把查询当只读 | `egress-guard/runtime.sh` 的 `--status` 在返回前累计并落盘，失败可 stop/mask；`health run` 可触发独立封锁 | 手册改为直接读状态文件，清晰区分只读 gate 与执行保护的 run |
| 高：恢复时重复补账/绕过保护 | `Store.Arm` 执行 adjustment += n；CLI 后续清 latch/reload 可失败，HTTP 错误也不能证明写入没发生 | 明确增量 byte、一次性执行、错误后核对、不能重填 34 GiB；移除缺少就绪/采样门禁的简化恢复命令链 |
| 高：数据库快照被误作完整恢复 | `Store.Backup` 仅 VACUUM INTO，保留 7 份且不备份 witness；旧库会丢失之后的撤销/额度操作 | 增补密钥/账户审计/独立见证边界，禁止在线覆盖或回退 witness 绕门禁；实际恢复须专项授权验证 |
| 中：诊断假警报与状态误读 | 通用诊断仍有 legacy/TLS 提示，退出码不能代表验收；`healthy` 本身不检查证书 | 增补判读表，结合端口归属、单独 certificate_error 与实际客户端验证，不据提示重建旧入口 |
| 中：续签现场配置被文档泛化 | 现场 reload hook 仅 sync；发行脚本默认另有 try-restart sing-box | 文档区分两者，保留的其他证书协议不得冒称已验证续签联动；不修改现场钩子 |
| 中：安装器没有 socket 就绪门禁（当时） | `Type=simple` + enable --now 后立即返回；现场首次导入失败，稍后有限就绪检查成功 | 当时以文档就绪检查续跑；后续本地限时等待与回归补强见第 13 节。现场启动竞争根因未独立复现，不为此重装已运行节点 |

维护命令的唯一详细入口为 `IMPLEMENTATION.md` 的“日常维护与故障排查”，README 只提供导航和只读起点。维护/恢复流程是代码审阅后的操作指导，不是已在生产执行的演练；第 11 节未执行项全部保留。

## 13. 安装器就绪等待补强（2026-10-23，本地未部署）

用户授权补强后，`install-hy2-account.sh` 在启动 accountd 与打印 Prepared only 之间新增就绪门禁：以 monotonic clock（单调时钟，不受系统校时回拨影响）限定该阶段总预算 20 秒，每个子命令最多 2 秒；只查询 systemctl show 和 hy2-admin status，失败不执行重装、重启、清库或解除保护。

通过条件为服务 active/running、自动重启次数 0、管理接口返回可读初始账户库（hold/review=true，authentication_allowed/core_live/healthy=false，账户为空且无 data_error）；成功响应之后复查服务状态，防止请求期间重启漏检。允许 socket 短暂未就绪并有限重试；错误状态/JSON/服务失败立即停止，挂起请求与持续不可用在预算耗尽后停止。不回显 status 原文、stderr 或凭据，不因为核心尚未启动/证书尚未发布的预期状态要求 healthy=true。

实际本地验证：

- `tests.test_install_hy2_account` 从 2 项增至 14 项。新增 12 项覆盖立即/延迟就绪、持续不可用、挂起请求、服务失败/重启、查询异常/畸形输出、响应期间重启、非法 JSON、非初始关闭状态/非空库、命令缺失、查询耗时计入总预算、shell 失败不报告 Prepared。
- 门禁测试执行生产 Python 代码块，subprocess/时钟为替身；shell 接线测试只执行安装器末段，使用 fake systemctl/python3。**不是完整 root 安装或真实 systemd 启动测试**，没有访问真实 VM 路径。
- 修复前 HEAD 的末段在模拟就绪失败场景仍返回 0 并报告 Prepared；修复后非零退出且不报告 Prepared。该有界实验验证新增门禁能阻止原先的误报，不声称复现了现场 socket 竞争的全部时序。
- 全部五模块 Python 回归 **69 项通过，无跳过**（guard 20、移除 4、证书 17、安装器 14、legacy SAN 14）；无依赖下载。Go/核心代码未修改，本轮不重复宣称新的生产验证。
- 快速恢复文档执行段的早期版本曾以 fake 命令验证 7 条路径；随后的 14 场景模拟记录见下方，但其安全性结论已被后续验收反例推翻，**不是现行恢复流程的验收依据**。现行修复与复验见第 14 节；未执行真实恢复。
- 最终复核：21 个 shell 脚本与手册 Bash 示例语法、7 份 Markdown 的 UTF-8/围栏/表格/本地链接与锚点、`git diff --check` 均通过。

交叉核对（同日，当时的代码核对与模拟记录；补账、时序与限时结论已由第 14 节补正）曾作以下修改：

| 问题 | 证据 | 修正 |
| --- | --- | --- |
| `sudo timeout 60s watch ...` 在交互终端会挂起 | 伪终端实测：无 `--foreground` 时 watch 被停止、到时不退出；加上后正常以 124 退出 | 速查表改为 `timeout --foreground` 并说明原因 |
| 补账文件无可操作取值，普通重启无法遵循 | 当时误把 5 秒采样周期当作最大漏账窗口 | 当时改成 clean=0 / unplanned=每未撤销账户 1 GiB；后续反例证实依据不成立，已撤回，见第 14 节 |
| 启动后固定 `sleep 6` 再 activate 可能早于采样接受 | collector 每 5 秒一拍，`Activate` 要求 arm_until=0 且样本新鲜 | 当时改成 15 次轮询；它不是 15 秒总上限，第 14 节已改为单调截止时间 |
| 重跑可能重复补账 | arm 是 adjustment 累加；失败后照原样重跑会再加一次 | 当时采用成功后改名和 review/hold 预检，仍漏掉见证失败等提交不明状态；第 14 节改为写前持久回执 |
| 整服停机后显示 masked 可能被误判为故障 | prepare-stop 置 review=1，健康 timer 下一拍写锁并 mask | 停机段注明为预期，由 arm 解除，不手动 unmask |

仍成立的核对：初始库 review/hold 默认为 1（安装器就绪门禁与之一致）；账户名正则允许 `friend-1`；当时新增 10 GiB 在 140 GiB 池内；View 返回含撤销账户在内的全部账户，与 Arm 覆盖要求一致；guard 开机 30 秒后才刷新。**撤回当时“arm 同步发布 bootstrap 后 health 不会重新上锁”的判断**：已在途的 health 可持旧状态作停服决定，必须串行化完整决策，见第 14 节。

当时的快速恢复第 1–3 步以 fake 状态机执行文档原文，14 个场景及两处变异检查通过；该模型没有覆盖真实见证提交错误、停机尾差、超过一个周期的统计失败和在途 health 决策，不能据此保证不重复/不漏账。旧模拟结果保留为历史说明，不移用于新版验收；未执行真实 systemd/核心重启恢复。

当前生产仍使用第 11 节的原封存候选包；不为安装器就绪补强而重装现有节点。第 14 节另有运行时代码与恢复助手修改，必须在新的授权窗口配套更新，不能把第 13 节“无需重装”的边界误解为新恢复命令可直接在旧 VM 使用。

## 14. 恢复流程阻断项修复（2026-10-23，本地未部署）

用户授权本地修复后，本轮没有连接 VM、调用云 API、改动保护阈值/定时频率、替换固定官方核心、迁移数据库或重跑安装器。原封存生产候选和第 11 节现场记录未改写。

验收先在临时副本中以真实 Store/Daemon、手册原文预检及受控替身复现五个反例：已提交但见证失败时补账 1 GiB 可被重跑成 2 GiB；成功 prepare-stop 后仍存在未计入尾差；15 秒统计失败仍可能处于允许的新鲜度窗口；撤销账户仍有待踢出会话；在途 health 可把旧停服决定带过一次 arm。另核实 15 次循环不等于 15 秒总预算。

| 修复 | 实现和边界 |
| --- | --- |
| 见证错误显式可见 | `Store.View` 给可读但 witness/fatal 异常的库设置 `data_error`，覆盖 root status 和页面；重开账户库不能隐藏已提交增量 |
| 停止无依据自动补账 | 新增 `recover-hy2.py`（安装名 `hy2-recover`）：全部账户初始为 null，含禁用/撤销；人工填写新增 byte 和依据，显式接受后才执行，不再提供 clean/unplanned 模式 |
| 单次尝试与提交不明 | 执行前绑定审核时的月份、代次、完整账户状态/用量；先落盘 `attempt/`、文件和目录 fsync，再发唯一一次 arm。报错/超时/中断后不删除回执、不重试写操作；空 attempt 也拒绝复用。它不把原始 arm 改成幂等接口 |
| 健康决策与恢复串行 | `interlock.go` 的 root-only `blocked.lock` 从 health 读取前持有到停服核验完成；CLI arm 从停机核验前持有到请求、清 latch 与 reload 完成。竞争最多等 2 秒；锁存储故障时拒绝 arm，health 仍尽力停 HY2；gate 保持只读且不取锁 |
| 真正的等待预算 | 单调时钟限定 gate/baseline 共各 10/15 秒，子请求最多 2 秒并扣除剩余预算；子请求或解析完成已到截止时间也不放行。arm、启动、激活各只尝试一次，不用全服重启兜底 |
| 版本和月份边界 | CLI/status 同时要求 recovery protocol 1，拒绝只换助手、旧 accountd 等混用；arm 可携带 `--expected-month`，月份变化先拒绝写入，启动/最终核验也检查月份 |
| 打包与运维入口 | 新安装器检查并安装助手，上传清单同步；现有 VM 要另行授权配套更新并核实旧 health 进程退出，不为此重装、init 或删除 witness |

实际本地复验（Go 1.26.0 / Python 3.12.3，工具链及依赖仅用已有缓存，禁止下载）：

| 检查 | 结果 | 范围 |
| --- | --- | --- |
| `gofmt -l .` / `go vet ./...` | PASS | Go 修改与新增回归 |
| `HY2_BIN=<checksum-pinned v2.13.0> go test -race -count=1 -timeout=180s ./...` | PASS | 全部 Go 测试及两个官方核心回环测试；真实账本见证错误、月份门禁、停机尾差、撤销残余、私密控制锁、在途 health 串行和 CLI 清锁接线均覆盖 |
| Python 六模块回归 | PASS（100，无跳过） | 原 69 项保留；新增恢复助手 31 项，真实临时文件/锁、fake CLI 与时钟，无真实 systemd |
| 三处局部变异 | 全部被回归拒绝 | 临时副本分别移除 data_error 赋值、绕过 health 控制锁、移除命令截止时间检查，均命中预期行为断言失败，而非编译错误 |
| `CGO_ENABLED=0 go build -trimpath` / version | PASS | 静态 linux/amd64 ELF，版本含 recovery-protocol=1；未上传或替换 VM |
| Shell / 手册示例 / Python / JSON | PASS | 21 个 Shell 脚本、44 段手册 Bash 示例、新 Python 语法、3 个客户端 JSON；`git diff --check` |

恢复助手回归覆盖：待审核 null/显式零、完整撤销账户覆盖、文件权限/重复键/重复目录、双版本门禁、guard/证书/UDP 占用及两个 timer 单独检查、审核后账本漂移、写前 fsync、回执不完整、提交前失败/见证提交后错误/超时/中断、旧代次拒绝、慢请求和处理时间计入总预算、启动/激活失败、最终状态或月份不符，以及重复执行不再提交。Go 侧保留实际 SQLite/见证语义；不把 fake 状态机的通过单独当作账本正确性的证明。

边界仍在：没有真实断电/磁盘恢复或 VM/systemd 重启演练，没有本轮生产升级或公网访问验证；助手不能证明人工填写的补账上界正确，也不能恢复丢失的账户变更或旧备份。原始 `system arm` 仍是加法，必须保留回执、核对已计入增量且不并发手工维护；所有现场动作仍需新的授权窗口。详细命令和停止条件只维护在 `IMPLEMENTATION.md` 的“快速恢复执行段”。

## 15. 恢复协议配套更新与账户交付记录（2026-10-23 UTC）

用户在本轮只读检查及原 HY2 客户端小网页访问成功后，明确请求开始更新。此前被暂停的 `systemctl`/`less` 查询进程已由用户清理；不能复用其旧 PID 再次清理。用户回贴的约 06:15 UTC 状态中，三个服务 active/running、NRestarts=0，认证和核心健康，采样与双服务 guard 新鲜；原 34 GiB adjustment 及一条迁移缺口保留。此记录不是本地代理直接连接 VM 的结果。

本轮限定范围：

- 新候选标识为 `hy2-update-r1-linux-amd64-20261023T062540Z`，包内携带实际工作区源码快照、构建信息和逐文件 SHA-256；基线仍为 `4283e06`，包含未提交修复，不冒称只由该提交构建。
- 只配套更新账户二进制（accountd、root CLI、health 共用）与 `hy2-recover`。与第 11 节封存源码逐字节核对，`store.go`、Go 依赖、证书钩子、guard 和 systemd 模板未变；官方核心保留已校验的 v2.13.0，不随更新包重新部署。
- 不重跑安装器，不 init、不恢复旧库/见证，不改变密码、配额、Reality、DNS/防火墙或定时器；旧封存发行包保持原样。
- 停服前仍须复核本次费用/共享额度门禁、独立管理通道及 Reality 退路，完成新包在 Cloud Shell/VM 的校验、私密本机备份及停机尾差证据准备。开始更新的授权不等于已审核任何补账值；零也不能自动填写，原 34 GiB 不得再次提交。
- 停服和替换按 `IMPLEMENTATION.md` 的“维护停机与受控恢复”及“快速恢复执行段”执行；新 CLI、运行中 accountd 和旧 health 进程退出须分别核实，不能只看磁盘上二进制版本。

本轮实际离线复验：缓存 Go 1.26.0，禁止依赖/工具链下载；`gofmt -l .`、`go vet ./...`、`go test -race -count=1 -timeout=180s ./...` 通过，最后一项显式使用 checksum-pinned 官方 v2.13.0 并覆盖回环集成测试。Python 六模块共 100 项通过、无跳过，只用临时目录、命令替身和用户命名空间，不接触真实 systemd。

**准备阶段后来已有 VM 上传、配套程序更新及审核故障后安全停机证据；截至本节末，受控恢复成功、补账已提交，朋友账户及私密文件准备完成。** 包外 `MAINTENANCE-PLAN.json` 保留准备与失败阶段记录，不将上传/预检冒称为更新成功。最新结果以下方各次用户实际回贴为证，不把第 11 节迁移成功或升级前访问写成升级后验收。

### 合并执行交付（本地准备，未运行生产流程）

用户要求不再逐条回贴命令输出后，另生成一次性 `hy2-update-once.sh`（嵌入 Python 执行器），放在上述候选包旁，原封存归档及账户程序/恢复助手均未修改。本地代理没有可用的原 Cloud Shell/IAP 交互会话，没有尝试本地 `gcloud`、新增 SSH 密钥或直连生产。

- 范围只覆盖已确认的原单 owner、90/140 GiB 额度与 34 GiB 历史 adjustment 基线；不作为通用、多账户或跨窗口升级器。最多两次 SSH、一次 SCP；当日授权、固定目标、已有 SSH 主机记录、包哈希和运行版本均设门禁，未通过即停。
- 只私密备份和原子替换账户二进制/助手；真实库与 witness 离线一致性核验、新进程/协议、旧 health 进程退出、保护及 Reality 不变均在继续前检查。证据/审核/回执不外传；不会因备份存在就自动回滚旧库或旧程序。
- 停机前后采样、累计和网络证据在同一终端展示；金额保持人工审核，不把静默观察或整机差值当作尾差上界。审核接受后仅调用既有恢复助手一次，最后由操作者确认原客户端小请求；失败保留现场，不自动重试写操作。
- 临时 systemd 执行单元限制运行时间并有条件收尾：仅已进入维护且无成功回执时确认/停止 HY2，停止不明也明确报告；不启停 Reality/保护 timer。它不是定时更新或自动恢复服务，真实 systemd、SSH 断线及生产恢复仍待本次现场执行验证。

本次只验证**新编排**：21 项本地检查通过（私密排他回执、原子替换/旧 inode 保留、校验失败不覆盖、全账户人工补账格式/下界/历史 34 GiB 拒绝、prepare-stop 失败仍完成已授权停机、成功/预检失败不误停、停止超时不重试、仅一次恢复调用）。新解包器对现有归档的逐文件校验通过；这些检查只用临时文件和命令替身，没有真实 systemd、网络或凭据，不重跑第 15 节已经通过的 Go/核心/Python 原测试套件。启动文件校验值和交付状态维护在包外计划中；命令与停止边界只维护在 `IMPLEMENTATION.md`。

### 现场预检失败与统计响应修正（2026-10-23，尚未更新运行程序）

证据来自用户 Cloud Shell 回贴，本地代理未连接 VM：第一次因没有活动 Google 账号在 SSH 建连前失败；用户完成原账号登录及项目选择后，受控续接完成传输，随后 VM 执行器报告 `STOP / preflight / unexpected official statistics format`，目录为 `/root/hy2-update-r1-20261023T062540Z`。这一路径位于备份、`maintenance-started.json`、prepare-stop 和程序替换之前；没有执行恢复补账，不能据此声称已完成备份或升级。NumPy 建议只是 IAP 性能提示，不是该失败原因，不安装依赖。

根因是新一次性执行器把 `/dump/streams` 误判为裸数组。已核对固定 v2.13.0 对应源码提交 `ca8fbd874fd6ca82413948de6dc5924405e103e1` 的 `extras/trafficlogger/http.go`，实际 JSON 为 `{"streams": [...]}`；仓库原 `tests/hy2_core_isolation.py` 也正确读取 `api("/dump/streams")["streams"]`。原 21 项编排检查未覆盖这个接口形状；新增回归对原封存执行器复现了相同错误，不能把旧检查通过当作端到端验收。

仅修改包外执行器的响应解析：严格要求外层对象及 `streams` 数组，缺失/null/裸数组/异常响应仍拒绝，非空 streams 或 online 仍阻止维护。新增 `hy2-update-resume-v2.sh` 只传小执行器、复用已校验的 VM 大包，并在新目录创建前核对旧失败阶段、未开始维护/恢复、旧进程退出和安装基线；旧现场和旧启动文件不改写。原账户程序、恢复助手及封存发行包未变。

本次实际验证：包外编排共 **37 项通过**（原 21 项 + 9 项统计响应检查 + 7 项续接门禁检查），全部使用本地临时文件和 HTTP/systemctl 替身；空 streams 正常响应在旧执行器上复现失败、修正版通过，有客户端/业务流及格式异常仍拒绝，输出不带目标/会话/密钥。另通过新启动文件 Git Bash 语法、远端 bootstrap 语法、内嵌载荷哈希往返和非 Cloud Shell 拒绝检查；不重跑原 Go/核心/Python 套件，不下载或启动真实核心。当时尚无修正后 VM 结果；后续回贴见下节，真实 SSH 断线和完整恢复仍不能宣称通过。

### 审核终端故障与安全停止（后续用户回贴）

用户执行 `stats-v2` 后报告 `STOP / charge-review / UnsupportedOperation: inspect private files locally`，目录 `/root/hy2-update-r1-20261023T062540Z-stats-v2`，随后明确输出 `SAFE_STOP=HY2_CONFIRMED_STOPPED; Reality/timers untouched`。按这版执行器路径，已通过备份、停 HY2/accountd、配套程序原子替换、旧进程退出、新 accountd 就绪与 ledger 一致性核验，再进入审核阶段；不再把现场描述为 VM 未改动，也不把这次安全停止当作恢复完成。尚需只读核对实际状态、原审核目录及 attempt 回执，不能仅凭阶段名断言写操作未发生。

已在本地真实伪终端复现：Python `open(tty, "r+")` 抛 `UnsupportedOperation: File or stream is not seekable`；原代码只需要输入，提示由 stdout 输出，改用只读 `r` 即可。原 37 项检查仍未执行真实终端输入，此次补上 4 项真实本地 PTY 检查（接受确认、读取审核 JSON、STOP/空输入拒绝、已过截止时间拒绝）并通过，已部署执行器的原代码在同一确认场景复现报错。未访问生产终端、网络、systemd 或凭据；没有重跑原 Go/核心套件，也没有上传终端修正。

接下来只读核对原审核与账本；**禁止重跑完整更新、重新 prepare 或通过新目录绕过 attempt**。任何已有/不完整 attempt 都先核对是否已补账；窗口过期须重新授权恢复，不能为尽快恢复而猜填 0 或重复 34 GiB。原交付文件保持封存，现场升级为部分完成而非成功结案。

后续用户只读回贴（08:58 UTC）：Reality/accountd active、自动重启 0，HY2 inactive，两个保护 timer active；账户二进制 SHA-256 与候选 `aa04fc…531f331`、助手 `5bdc39…55973f` 匹配，CLI/status protocol 均为 1。原 recovery 快照一致、review.owner=null、attempt 不存在；owner 用量 36539152558 bytes，其中 adjustment 仍为 36507222016 bytes（原 34 GiB）。共享锁及 stopped-core stats_error 是恢复前状态，不通过停用 timer 或清锁解决。随后只读会话汇总：124 个会话、最后认证 08:44:51.216831 UTC，静默前十秒及最后静默后新认证均为 0；全部有 traffic counter，其中 1 个未曾在采样中观察在线，最终 online=0；tx/rx 与静默快照一致。这仍不单独证明尾差为零。

用户不能保证全部客户端退出，并接受在不变架构下保守多记本月个人用量以尽快恢复、之后分享朋友的方向；**具体补账字节尚未审核，朋友账号名/额度也未确定**。下一步仅只读评估全机接收累计的粗上界与剩余额度，适用条件和停止边界维护在 `IMPLEMENTATION.md`，并非已经提交补账。该预览代码本地仅验证语法、金额边界与只读命令检查，以及 7 个临时文件/替身场景（成功、异启动、自定义数据路径、计数回退、接口变化、已有 attempt、账本漂移）；不访问真实 VM、网络、systemd 或凭据，不重跑原 Go/核心测试套件。当时真实上界和恢复结果待用户输出。

后续用户 09:36 UTC 预览回贴：同次启动、原接口及单调快照检查通过，ens4@stop RX=50554583085、lo@preview RX=29455126，候选新增合计 **50584038211 bytes（47.110057 GiB）**；预期 adjustment=87091260227、used=87123190769、owner 剩余 9513573391 bytes（8.860206 GiB），仍在原 90 GiB 内。它是接受前述连续性/路径条件后故意过量的包络，不是实际漏账；预览 ledger_changed/recovery_executed 均 false。用户明确指定一个 30 GiB 的朋友账户，但仍未提交恢复或创建账号。

新增包外收尾批次 `hy2-finish-reviewed.sh`：具体金额、窗口及私密导出由 Cloud Shell Bash 确认（不使用 Python r+）；原审核目录/一次性记录排他，恢复只调用既有助手一次；恢复失败按 recovered 回执决定停止 HY2，不触碰 Reality/timer；服务端核验后单次创建账号并将链接以 SSH/SCP 和 0600 文件私密交付，不打印凭据。新批次实际本地检查：三个真实 Python 代码块共 6 个临时文件/命令替身场景（成功、已有 attempt、账本漂移、已有收尾意图、加账号期间 PID 变化、朋友额度不符），回执门控的失败停止/成功不停止检查，以及真实本地 PTY 的接受/拒绝与整个 Cloud Shell 控制流替身检查（拒绝不访问 VM，接受最多一次 SSH/一次 SCP，凭据不进输出且目标文件 0600）。Git Bash 语法通过，未连接生产、运行真实 systemd 或真实核心，也未重跑原 Go/核心/Python 套件。当时尚无该收尾批次现场结果，后续实际结果如下。

### 恢复成功、外层收尾 hook 错误（09:57 UTC 用户回贴）

用户输出 `REVIEW_ACCEPTED owner +50584038211 bytes`；09:57:51 原助手输出 `RECOVERED auth=true healthy=true core_live=true`，外层停止摘要同时确认 HY2 active/running。临时单元随后记录 `Ignoring unknown escape sequences`，shell 收尾命令报 `Syntax error: end of file unexpected (expecting "then")`，因此外层 `phase=recovery rc=1`；**恢复/补账已经生效，不能因 rc=1 重复提交**。朋友账号创建与交付在失败点之后，没有执行完成证据，下载文件不存在与该控制流一致。原批次永久停用，原 attempt、审计、归档和运行服务均保留；不为清除 failed 外层单元去重启代理。

原本本地检查验证了 shell 的回执分支，却没有覆盖 `systemd-run` 写入 transient unit 后的实际转义/解析；因此测试通过不等于 systemd 集成通过。这是新增编排的缺陷，不是官方核心故障。本轮不再增加收尾 hook 或更新框架，后续只能先只读核验状态与已提交 adjustment，再续接普通账户管理/私密文件交付，失败不重启全服。真实客户端访问、有连接的单账户封禁隔离仍未验收。

本次停机时的账面组成已向用户解释：原保守补记 34 GiB + 本次粗上界 47.110056701 GiB + 正常采样 0.029737635 GiB = 81.139794336 GiB；其中粗上界包含历史和无关流量的过量覆盖，不是实际个人流量或 GCP 出站账单。owner 仍是原 90 GiB 总额度，直接降至 80 会超额；“动态加额度”是设置月总额，不清已用，仍受 140 GiB 池及独立整机保护约束。用户对 80 GiB 的具体意图尚未确认，没有替其修改额度。

用户随后要求分析成熟面板（如 3x-ui）与现有方案的维护成本；这只是评估，不授权安装面板、改变 HY2/Reality 协议、清账或弱化保护。朋友账户未由本地代理直接创建。

### 当日账户交付续接准备与同步

比较面板后，用户决定先完成现有节点与 30 GiB 朋友账户交付，贴出常用命令并同步当日记录；不新增面板、云资源、全局代理或自动更新。续接不再上传/安装执行器、不运行 systemd hook、recover、arm、activate 或服务启停；只检查现有成功回执/已提交增量及健康状态、必要时单次创建账户、私密交付链接。符合授权状态的既有账号/同字节私密 VM 文件可复用，不重复新增、旋转、覆盖不同凭据或清账；错误/超时先查实际状态。owner 暂保留 90 GiB；调月总额支持热更新，不代表已用量清零或无限增额。

本轮新交付代码只在本地验证：实际代码 9 项临时文件/命令替身场景（新建、已有账号、已有相同私密副本、服务不健康、提交不明错误、额度不符、源文件权限不当、PID 改变、账号已耗尽）；确认无恢复/服务变更命令、无凭据内容输出，目标 0600。Cloud Shell 包装另以 fake gcloud 验证旧空目录续接、拒绝覆盖已有文件、最多一次 SSH/一次 SCP；Bash 语法通过。未连接真实 VM、运行真实 systemd/核心或重跑原测试套件。**当时仅为交付准备，现场证据随后回贴如下**；此前混合工作区尚未 commit/push。

### 普通交付续接结果（用户回贴 `FRIEND_READY`）

本次证据为用户贴出的脱敏摘要，本地代理没有重新连接 VM、读取私密文件或验证下载：

| 项目 | 实际回贴结果 | 边界 |
| --- | --- | --- |
| 朋友账户 | 指定账户已创建，月额度 30 GiB | 未据此确认公网客户端实际访问；公开文档使用泛化账户名 |
| owner | 总额度仍为 90 GiB，当时剩余 8.859525 GiB | 包含原历史和恢复保守补账，不是 Google 账单用量 |
| 共享保护 | 三个服务 PID 未变，两个保护 timer active；警戒/熔断 150/180 GiB，额度池 140 GiB | 不等于所有故障、重启或多人隔离场景已通过 |
| 恢复与补账 | `recovery_called=false`、`additional_charge_submitted=false` | 本次只续接账户与文件交付，未重放原恢复 |
| 私密链接 | Cloud Shell 私密文件路径已输出，凭据仅在私密文件，未打印 | 不将真实用户名、路径或文件内容提交 Git；操作者下载后私下发送，尚无下载/发送确认 |
| 客户端/隔离 | 明确 `NOT YET VERIFIED` | 仍需另行授权、限流量的实际访问与有连接封禁隔离验收 |

NumPy 提示只是 IAP 隧道上传性能建议，不是这次交付失败，也不需要为小文件交付新增依赖。

用户随后授权提交/推送同步。本轮同步前实际本地检查：缓存 Go 1.26.0，`gofmt -l .`、`go vet ./...`、未设 `HY2_BIN` 的 `go test -race -count=1 -timeout=180s ./...` 通过；Python 六模块 100 项全部通过、无跳过；Shell/Python/三个客户端 JSON 语法通过。未下载工具链/依赖，未连接生产或改动线上服务；不将本轮检查写成新的真实核心/生产验收。随后已提交并推送 `e57d8bf`，同时同步原先两个本地提交；真实账户名、私密交付路径与凭据未进入 Git。

## 16. 用量页与中文状态摘要改进（本地实现，未部署 VM）

用户要求更直观、更友好，本次仅修改呈现与只读 CLI，不安装通用面板、不开放公网管理、不改变账户额度、数据 schema、认证/踢出、保护阈值或采集频率。现有 VM 继续运行原版本；Git 推送不触发部署，accountd 更新仍需另行授权的维护窗口。

实现：

- 页面改为浅色响应式卡片：服务端状态、整机保护余量、正常采样/保守补账、额度池未分配量，以及每账户优先显示的剩余 GiB。80% 预警、暂停/撤销/超额和踢出未完成不藏在复杂表格中。
- 用 `presentation.go` 共享网页与中文摘要的提示；明确“服务端已就绪”不是实际公网访问或多人隔离已通过，`healthy` 不含证书检查。签发会话容量与在线连接数不混为物理设备数。
- 额度池占用展示对照真实 `allocatedPool` 语义：未撤销账户取 `max(月额, 已用)`，已撤销账户只保留已用；降额/撤销不抹掉消费。未分配量不是新增流量授权或结算额度。
- `status --human` 仅向原 Unix socket 发出一次 `status` 请求，不导出凭据、获取控制锁、执行健康保护或调用 systemctl。原 `status` JSON、精确字节与 `user list` 格式不变；缺失/失败 status 不能误报 Completed。诊断/审核文字的终端控制字符转义输出。
- 账本未知隐藏旧账户余额、日用量与池占用；账本读取失败的页面降级也保留独立 guard 的可信/不可核验状态，不把未知整机余量显示为 0。认证关闭行为保持不变。
- 日趋势/诊断/分享说明可展开；异常时诊断展开，“看诊断”锚点能打开折叠内容。无浏览器脚本、外部字体/CDN/请求，Basic Auth、CSP、no-store 等访问边界保持，自动刷新仍为 15 秒。

本轮实际验证（缓存 Go 1.26.0 / Python 3.12.3 / 已有本地 Chromium 与 Playwright，不下载依赖/工具链）：

| 检查 | 结果 | 范围 |
| --- | --- | --- |
| `gofmt -l .` / `go vet ./...` | PASS | 新增呈现与 CLI 代码格式/静态检查 |
| `go test -race -count=1 -timeout=180s ./...`，未设 HY2_BIN | PASS | 保留原回归；新增 13 个 Go 测试函数及子场景，覆盖 80%/额度/保护/证书/暂停/撤销、账本未知、踢出进度、HTML 转义、原 JSON、单次只读请求、未知响应与输出失败 |
| 额度池对照真实 Store | PASS | 初始、超额、暂停、降额到已用以下、撤销均与真实 SQL 保留消费规则一致 |
| 已校验官方 v2.13.0，`HY2_BIN=<pinned> go test -race ... -run TestPinned -v` | PASS（2） | 本地回环定向封禁/额度/认证竞争，owner 同一 TCP+QUIC 连接保持；生产 ACL 用回环替身验证 |
| `CGO_ENABLED=0 go build -trimpath` / version | PASS | 静态 linux/amd64 ELF，recovery protocol 1；只在本地临时目录构建 |
| Python 六模块 | PASS（100，无跳过） | fake systemctl、临时文件、用户命名空间；不访问生产 |
| 浏览器呈现 | PASS（24 组） | 8 种虚构状态 × 1280/390/320px；无横向溢出/脚本/外部请求，状态提示、明细、锚点与手动刷新；本地截图仅作预览，未提交生成物 |
| 15 秒自动刷新 | PASS | 实际等待 16.5 秒，只有初始和一次刷新页面 GET，无新增 API/外部请求 |
| Shell/Python/客户端 JSON / Markdown / diff | PASS | 语法、UTF-8、围栏、本地文件链接和 78 段 Bash 示例，未执行其运维命令 |

未覆盖：真实 VM 部署、实际 SSH 隧道/页面登录、Safari/手机真机及未来故障/成本变化；不将虚构截图或本地核心测试写成生产验收。操作与更新边界维护在 `IMPLEMENTATION.md`，README 只提供新版入口和未部署说明。

## 17. 新版呈现部署准备与授权停机（2026-10-23 UTC，更新尚未执行）

用户要求部署并确认原 VM、HY2 可中断、30 分钟窗口/至少 5 分钟收尾、费用与共享额度未变化；后续明确无法保证所有客户端退出，要求直接停止 HY2。这授权停止服务，不是关闭 VM，不授权猜补账或扩大额度；Reality、accountd、两个保护 timer 在该停机步骤不动。

本地从干净提交 `bcaece25e092d07091f680e13609bd46b86c5d59` 封存新候选 `hy2-ui-r2-linux-amd64-20261023T122216Z`。只需替换账户二进制，不更换官方核心、恢复助手、证书钩子、配置、systemd 单元或数据库 schema。71 个源码文件的清单 SHA-256 为 `3bef44c14f7ecdb13a2b226dd346201c417ff23f797ed2bf1b92cd36a6cd4c4c`；账户二进制 SHA-256 为 `44f9a99a8e00ef78669946f9b30d483d2abf4f038b834f6524f7a0fecce30444`；归档 SHA-256 为 `d636bd042ea2db19ce3999dde96e598fcae3f08be8c311f1ae1704d46cc92227`。已对提交快照重新执行 gofmt/vet、未设 HY2_BIN 的 race 测试及静态构建/version，逐文件与归档结构校验通过，无下载；没有上传或替换 VM 程序。

一次停机交付由用户在原 Cloud Shell/IAP 通道执行，最多一次 SSH，所有输出/停机证据留 VM 私密目录；最终采样失败也继续唯一一次已授权 stop，不调用恢复、arm、activate、安装器或整体重启。编排以临时目录/fake 命令/用户命名空间验证 8 种场景：正常、采样失败、stop 非零但实际已停、stop 失败、UDP 仍占用、timer 失败、受保护 PID 变化及部分状态探测失败；已有目录拒绝重跑。

**用户回贴已确认：** `SAFE_STOP=HY2_CONFIRMED_STOPPED`、HY2 inactive/MainPID=0、FINAL_SAMPLE_RC=0、STOP_COMMAND_RC=0、Reality/accountd 未变、两个保护 timer active；没有恢复、追加补账或 VM stop。证据来源为用户脱敏摘要，本地代理没有直接连接生产；采样成功不证明零尾差。当前新版二进制与页面仍未部署，HY2 按授权保持停止。

后续顺序调整为先只读核对原停机证据、账本/见证、会话记录和网卡累计，再安排备份/程序替换/恢复，避免更新后才发现核账无法继续。只读助手只查询状态、进程/端口、SQL SELECT/只读事务及本机文件，不调用统计清零/云 API、服务操作或恢复；每个账户新增 byte 仍为 null，网卡窗口差值不单独当作尾差上界。3 个本地测试函数及子场景验证成功、异常拒绝、定制路径/传输条件失败、无字节写入/服务动作；新增交付尚无现场回贴。

原 30 分钟窗口已过，随后用户重新确认续接窗口；写操作仍须扣除检查/交付耗时并保留 5 分钟收尾，不能自动扩展。保留此前全部已提交 adjustment 与恢复回执，特别是原 34 GiB 和 50584038211 bytes 不得再次提交。包外计划保留详细交付/未执行状态，仓库不提交真实目标、账户名、路径或凭据。

### 停机证据助手权限缺陷与修正

用户执行第一个只读助手，Cloud Shell 文件校验通过，VM 返回 `R2_STOP_EVIDENCE_FAILED / runtime_changed=false`；它未执行任何服务/恢复/补账命令，但原失败摘要未指出阶段，不能凭这条输出确定所有现场问题。NumPy 是 IAP 性能建议，不是核验失败原因。

本地已发现并用真实临时文件复现确定缺陷：`install-hy2-account.sh` 将 `/etc/hy2-account/config.json`、`/etc/hysteria/server.json` 发布为 root:对应服务组 `0640`，助手却在第一次配置读取时使用了要求无组权限的 `read_private`（证据文件 `0600` 规则）。因此标准安装的 `0640` 配置必然在第一步被拒绝。原 3 项模型测试替换了配置读取，漏掉了这层权限契约；这是新诊断助手的问题，不是修改 VM 权限或重装后端的理由。

修正版 v2 为配置独立检查 root 所有、指定服务组、精确 `0640`、非链接普通文件；原 root-only `0600` 证据规则、hy2-account 所有 witness 规则保持，不 chmod、不放宽生产 ACL。失败摘要增加固定阶段与代码，系统/JSON/SQL/子进程原始异常文字仍不输出。5 个本地测试函数及子场景通过，配置读取实际使用 `0640` 临时文件，并拒绝错误属主/组、公开/可组写权限、链接/目录；成功、异常不写入、无服务动作与不泄露失败内容仍覆盖。原助手与封存归档保持原样，新 v2 交付语法与内嵌校验往返通过。

后续用户回贴 13:20:39 UTC `R2_STOP_EVIDENCE_READ`：HY2 已停、Reality/accountd/timer active、账本与停机后证据相同；最后采样在 12:47:15.349289 UTC，停机证据窗口 12:47:15.275370–15.419607 UTC，ens4 RX 增量 0、lo RX 增量 2749 bytes。owner 用量 81.142229 GiB、约剩 8.857771 GiB，正常采样 34544302 bytes、历史 adjustment 87091260227；当前代次 28 个会话均有 counter 且曾观察在线。朋友当前代次会话 0、用量 0、30 GiB；创建晚于最近 activate。这是可审核的现场事实，不把小网卡差值、无会话或采样成功自动当作零尾差证明；各账户新增补账仍为 null。`MemoryMax=infinity` 是该只读 systemctl 属性输出，不单独证明现场完整 sandbox/资源配置或新的尾差上界；本轮不更改内存/保护单元。

## 18. 明确授权的 owner 120 / 朋友 30 / 池 150 GiB 调整（2026-10-24 更新通过，HY2 仍停止）

用户要求 owner 月总额 120，拒绝因早期 140 GiB 池而被迫降低朋友额度；已明确解释并撤回“140 不能改”的说法：它是项目早期保守分配，不是 Google 限制。按本项目一贯 GiB 口径，本次目标 **owner 120 GiB、朋友原 30 GiB、显式额度池 150 GiB**；旧用量/补账/凭据完整保留，整机警戒 150/停止 180 GiB 不变。用户批准扩大此账户分配，不授权自动给出新增恢复补账，也不保证 Google 免费/结算流量。

最小实现：配置和 CLI 将先前 140 GiB 校验上限调整为共享的 `MaxQuotaPoolGiB=180`（不是默认分配），安装器显式参数范围同步至 1..180，按十进制安全解析；本次实际池只有 150。Store 分配/消费保留、auth/collector/guard/recovery、schema、官方核心、证书和 systemd 单元均不改。已运行 accountd 的池来自启动配置，修改 JSON 不会热加载；现有节点须在配套更新窗口原子更改池字段与二进制，不重装/init。停核心时个人 quota 修改可能已成功落库但返回 incomplete；用 status 核对后不重复执行。

实际本地验证（缓存 Go 1.26.0/Python 3.12.3，禁止下载）：gofmt、go vet、未设 HY2_BIN 的全量 race 测试通过；2 个 checksum-pinned 官方核心回环测试通过；Python 六模块 **102 项通过、无跳过**（新增安装器范围/十进制与 Go 上限契约）；Shell、Markdown UTF-8/围栏/本地文件链接/Bash 示例、客户端 JSON 和 diff check 通过。新增 Go 覆盖配置150/180边界、LoadConfig 不改密钥、实际 Store 重开后接受120+30=150且保留82GiB合成用量、满池多1 byte拒绝、guard超限/过期/共享锁不能放行，以及停核心时quota提交不明的真实语义。原不受新需求影响的回归不削弱；旧 141 拒绝断言改为新 180 上界 +1 与 181 拒绝。

旧 r2 界面包仍封存，不修改后冒用原哈希；其配置只接受原池，不能用于这次150GiB更新。该准备阶段现场仍为90/30/140、HY2停止，当时尚无新候选应用/恢复证据；后续实际结果见下文与第19节。此前续接时间预算已被本轮设计/验证消耗，下一次执行须按已批准范围重新开始有界窗口并留收尾，不能把扩大账户配额当作无限时维护授权。

本地代码已提交/推送 `851c090ba4bdf3972558253ba07eab659409b16f`，从干净提交封存新候选 `hy2-ui-r3-linux-amd64-20261023T134712Z`，72 个源码文件的清单 SHA-256 为 `22156487aa6b6f37cd439dc9e44090e6f5b77d8203a3a4d451dab4d8625710e4`；二进制为 `4dc5eb701be1500743a7241e79cd920b816b01abf689876293a40bddcbfe73f0`，归档为 `2e5fade86262d4515910c79f4694abf36f8ac4ac7afc65b751d7235f23cd8c6e`。产物为本地缓存 Go 1.26.0 静态 linux/amd64，不含/不替换官方核心或恢复助手，逐文件校验通过。

另提供包外一次性 r3 交付入口，作用仅为私密备份、原子替换账户程序及池字段、启动 accountd、单次设置 owner120并核验新页面；**不调用恢复/arm/activate、不启动 HY2**。Cloud Shell 确认开始30分钟窗口，2次SSH/1次SCP、VM300秒预算，无下载/重试；不使用 transient unit 或 ExecStopPost hook。root 侧与既有恢复助手共用持久 recovery 互斥锁、不 unlink，原账号/健康旧进程自然退出后再继续。普通 SCP 产物权限只在私密上传目录内允许，先复制到 root 私密文件并校验，再解包；不从 user 可改脚本路径执行特权代码。新config保持0640，凭据/用量不改；quota 非零也只读核对是否已成功，不重放。

新编排本地检查：3个测试函数与10种失败场景通过，使用namespace-root临时SQLite/配置/归档与fake命令/页面/时钟；实际原子文件发布证明旧inode不被覆盖、已有暂存/执行目录拒绝重用，完整成功和quota提交后超时语义覆盖。Cloud Shell 包装以真实本地PTY验证接受/拒绝，坏归档/错误上下文不调用gcloud，接受时最多2个fake SSH和1个fake SCP且内嵌程序哈希正确；语法/校验往返通过。没有实际Cloud Shell/VM应用结果，不能把模型通过当作真实systemd/部署成功；现场只有先前安全停机和v2只读核对证据。

### 跨 UTC 日期的入口停止（2026-10-24，未进入 VM）

用户回贴 r3 启动文件 SHA-256 校验通过，随后立即输出“授权日期已变化，请重新评估窗口/费用”。本地只读时钟为 2026-10-24 02:15 UTC，而入口固定授权日期为 2026-10-23；按已校验脚本控制流，此检查位于确认提示、Cloud Shell 控制目录创建、SSH/SCP 和一切 VM 写操作之前，因此本次未上传、替换、重启、调额或补账。它不是部署包损坏，也不是 NumPy/IAP 问题。原候选包、程序哈希与回执保持，不能改系统时钟、删除已有目录或直接去掉日期检查来继续。下一次运行需重新明确 UTC 日期、原目标、30 分钟窗口/5 分钟收尾及费用/共享额度条件；当时尚未获得新日期的窗口确认，不把旧确认自动展期。

随后用户明确确认 2026-10-24 UTC 的同范围续接窗口及费用/共享额度条件。续接仅核对原入口 SHA-256，在内存副本把唯一的日期门禁 `2026-10-23` 改为 `2026-10-24`，再校验整份副本哈希；不重建/改写压缩包、VM 执行器或原脚本，不换控制/上传/VM 工作目录，任何已有回执仍拒绝重跑。原入口哈希 `6f6dab…52d737`、内存续接副本 `bc6996…bf42c`，两者仅一个日期字节不同；VM 执行器保持 `d9c01f…22ea7`，2次SSH/1次SCP、30分钟窗口及5分钟收尾不变，不调用恢复或补账。

本地验证：3种错误日期/哈希/已改副本拒绝，6种隔离包装/真实PTY场景（接受、拒绝、坏压缩包、已有控制目录、错误上下文、shell中日期再次过期）通过；只有接受路径调用2个fake SSH和1个fake SCP，校验内嵌VM程序相同，其他路径不调用gcloud。旧脚本/包/执行器哈希核对不变；没有本轮VM连接或应用结果。无需再次上传11MiB包，现场仍须由原Cloud Shell执行一次并回贴末尾摘要。

### r3 页面/额度应用结果（2026-10-24 UTC 用户回贴）

用户在新日期确认 `APPLY-120-30` 后，入口返回 **`R3_ACCOUNT_READY_HELD`**，Cloud Shell流程正常返回。证据来自用户贴出的脱敏摘要；本地代理没有直接连接VM、读取数据库或凭据。

| 项目 | 现场摘要 | 边界 |
| --- | --- | --- |
| 账户/月池 | owner120 GiB、朋友30 GiB、池150 GiB | 已生效，不再沿用“线上仍90/30/140”的准备期描述 |
| owner余额 | 38.857771 GiB | 当时余额；原历史补账与已用量保留，不是清账后120GiB |
| 新页面 | `page_new_ui_verified=true` | 证明VM本机页面核验，不等于实际SSH隧道/手机浏览器或公网代理访问通过 |
| 保护/备用 | Reality同PID，两个保护timer active，整机150/180 GiB不变 | 不宣称新增故障/重启/多人隔离演练通过 |
| HY2状态 | `HY2_still_stopped=true`、`authentication_still_held=true` | 本批预期结果，不能把页面上线当作代理已恢复 |
| 恢复与补账 | `recovery_execute_called=false`、`additional_charge_submitted=false` | 本次缺口尚未审核；不重放历史补账 |
| 回执 | VM更新目录和Cloud Shell控制目录均保留 | 不删除/重用，不因进程正常返回就再次执行部署或quota |

部署与调额这一切片通过；**当时HY2恢复尚未完成，后续结果见第19节**。当时下一步为基于已保存的停机证据审核每账户尚未入账增量，并在明确授权/有界窗口内使用匹配的单次恢复助手。全局保护、持久锁和原始attempt均保留；没有授权的具体新增字节就不恢复、不猜0或固定1GiB。本文本轮只更新记录和被现场状态改变的文档，未修改运行代码或重跑Go/Python/核心套件。

## 19. r3 单次恢复与收尾（2026-10-24，服务端已恢复，客户端访问待确认）

用户要求尽快恢复后，收敛为一个只调用既有、哈希匹配恢复助手的入口，不再部署、传包、修改额度或重启accountd。它先验证r3 held成功回执及现有账本/证据，再显示本次条件性粗包络：固定direct/TCP无解压/本地造流量路径、停机前后内存/swap/网络/计数未变时，用完整1GiB整机缓冲加2749 bytes窗口RX，owner候选1073744573 bytes；朋友有当前代次从未签发会话且创建晚于激活的证据，候选0。operator必须在终端明确接受具体字节和条件，不能把“尽快恢复”当作已经同意扣除这些字节；不同意或条件未知就停止，所有实际增量在接受前仍未提交。计算与恢复边界仅在 `IMPLEMENTATION.md` 维护，不把该候选写成实际漏账或通用默认值。

已完成本地检查：恢复助手原31项回归通过；2个新纯函数测试及13种边界/条件失败场景通过（只校验数值、条件拒绝和不改变输入，不证明真实主机的物理上界）；4种隔离Cloud Shell/真实PTY包装检查通过，只有合法路径调用1次fake SSH、无SCP，校验内嵌远端程序并保留严格主机验证；错误日期/上下文/已有控制目录均在gcloud前退出。程序语法与内嵌校验通过；没有访问VM、统计API、真实systemd或凭据，也没有重跑Go/核心测试。整个恢复既有arm/activate/锁/回执逻辑未改，异常不重试写操作；任何已有本次审核目录都先核对，绝不换目录绕过。

上述为交付准备时的审核要求。随后用户明确在VM终端输入 `RECOVER-1073744573`，回贴 **`R3_RECOVERED`**，SSH正常结束、Cloud Shell恢复入口正常返回。本地代理未直接连接VM，不把用户摘要冒称为本轮独立读取生产。

| 项目 | 本次现场结果 | 边界/收尾 |
| --- | --- | --- |
| 服务端恢复 | `auth=true / healthy=true / core_live=true` | 认证、统计、核心恢复，不等于公网客户端访问已验收 |
| 单次执行 | `recovery_execute_calls=1` | 原助手/持久回执保持；禁止再次执行此入口/arm |
| 本次新增 | owner1073744573 bytes、朋友0 | 已由操作者接受并提交，不是实测漏账；原34GiB/50584038211 bytes未重放 |
| 账户与余额 | owner120 GiB、朋友30 GiB、池150 GiB；owner剩37.857769 GiB | 本次余额快照，之后正常流量继续计入 |
| 备用与保护 | Reality同PID，timer通过原助手分别检查 | 不改警戒/上限150/180 GiB，不删除锁或停保护 |
| 客户端凭据 | 程序/额度更新前后凭据指纹保持；未执行rotate/重建账户 | 朋友继续原链接/密码，不重新创建或重新发新节点 |
| 客户端访问 | 明确 `NOT YET VERIFIED` | 本人/朋友各做一次小网页访问即可另行补充，不自动测速/调用收费API |
| 现场材料 | VM的本次review/attempt/recovered及Cloud Shell控制目录保留 | 不删除/复用，不恢复旧库/见证，不把临时账号凭据或数据库提交Git |

**本次新版部署、120+30配额调整和服务端恢复收尾完成。** 用户要求同步后，本轮只整理日常命令、更新当前状态与验收记录、提交推送文档；不新增线上命令/资源/自动化，不停止、重启或清理任何运行服务/凭据/备份。真实多账户封禁隔离、VM故障/重启、备份恢复、实际续签热加载和结算账单仍未据此验收；所有未来维护与新增补账须另行授权，不能将本次“收尾”当作下次操作许可。
