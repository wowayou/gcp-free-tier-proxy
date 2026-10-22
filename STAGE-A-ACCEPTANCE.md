# HY2 账户系统阶段验收记录

更新时间：2026-10-22

状态：**本地阶段验收通过（含交叉核验后的 A2 修复，见第 8 节）；生产 VM 验收未执行。** 本记录用于上线前交叉核验，不代表已经迁移生产。

## 1. 验收范围

本阶段只验证代码、脚本和固定版本官方核心的本地行为，不接触 GCP、真实 VM、真实证书、真实账户或 systemd 生产单元。

通过标准：

- 账户/会话隔离、额度、统计游标、恢复/故障边界有可重复测试。
- 官方 v2.13.0 核心在回环环境中验证多设备、空闲连接、认证竞争和目标账户定向封禁。
- 整机保护覆盖两个服务，开机/启动门禁不能靠重启绕过。
- 迁移工具拒绝猜测，文档顺序不会先启动会争抢 UDP 443 的服务。
- 仓库无真实凭据、无私自扩大计费资源。

## 2. 环境

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
python3 -m unittest tests.test_sync_hysteria_cert     PASS（5，A2 新增；unshare -r）
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
| 证书同步 SAN/有效期/密钥匹配 | PASS（A2） | 一次性假证书：SAN 错误、剩余 <24h、密钥不匹配均拒绝且不替换 `current`；精确与通配 SAN 可发布（A2 前 SAN 检查无效） |
| 旧 sing-box HY2 移除工具 | PASS | 只删确认属于旧入站的规则；歧义引用/无 tag/无 HY2 时拒绝 |
| 旧入口命令阻断 | PASS | 安装账户后端后拒绝 legacy 创建/用户管理 |
| 安装前 guard 覆盖门禁 | PASS（A2） | 精确 unit 名；`sing-box-old hysteria-server-old` 等相似名或单服务列表在任何变更前拒绝（A2 前为子串匹配） |
| 旧用户导入格式 | PASS（A2） | 真实 CLI 解析器接受文档中的 `{"users":[...]}`；裸数组、空列表、未知字段、非 0600 文件拒绝（A2 前文档格式被拒绝） |
| 文档迁移顺序 | PASS（A2） | 先释放 UDP 443，再 `system arm`、启动官方 HY2、激活；上传包保留安装器依赖的目录结构，二进制与 `hy2-account/` 不重名 |

## 5. 本地未覆盖、必须 VM 现场验收

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

本地阶段（阶段 A/B，含 A2 修复）通过，可以进入只读 VM 预检和交叉核验。生产迁移、真实客户端验收和账单确认尚未完成，不能把本记录当作上线批准。

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

A2 仍未覆盖：第 5 节全部 VM 现场项；证书同步和安装器测试在用户命名空间中运行，属主/属组交由 VM 验证。
