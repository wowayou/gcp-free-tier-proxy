# AGENTS.md

本文件给之后维护这个目录的 AI/自动化代理使用。目标是让后续维护不会偏离当前收敛方案。

## Project Intent

这是一个 Google Cloud 免费层 sing-box 节点维护包。

当前目标：

- 低成本。
- 低复杂度。
- 明确出站流量边界。
- 主力服务 AI 相关访问。
- 保留可恢复、可审计的脚本和文档。

不要把它扩展成通用全局代理平台、CDN 回源方案、复杂多协议面板，除非用户明确要求并重新评估成本。

## Current Architecture

主力节点：

```text
VLESS Reality Vision
port: 47370/tcp
SNI: apple.com
flow: xtls-rprx-vision
service: sing-box
```

备用节点：

```text
VLESS TCP+TLS
port: 443/tcp, fallback 8443/tcp
certificate: /root/ygkkkca/fullchain.cer
key: /root/ygkkkca/private.key
service: sing-box
```

并行试验入口：

```text
Hysteria 2
port: 443/udp
certificate: /root/ygkkkca/fullchain.cer
service: sing-box
client network: tcp only during the trial
```

统一保护：

```text
gcp-egress-guard
warn: 150 GiB/month
limit: 180 GiB/month
action: stop and mask sing-box
```

## Execution Contexts

严格区分 Cloud Shell、本地 Windows、VM 三类上下文。

Cloud Shell only:

- `gcloud compute ...`
- `create-vm-standard-ipv4.sh`
- `open-vless-reality-port.sh`
- `open-vless-tls-port.sh`
- `open-hysteria2-port.sh`
- `create-budget-alert.sh`
- `audit-leftovers.sh`
- `audit-cost-and-usage.sh`

VM only:

- `install-egress-guard.sh`
- `issue-acme-cloudflare-token.sh`
- `add-vless-tls-inbound.sh`
- `add-hysteria2-inbound.sh`
- `export-vless-links.sh`
- `verify-singbox-runtime.sh`
- `collect-performance-baseline.sh`
- `systemctl`
- `ss`
- `/etc/s-box/sb.json`
- `/etc/sing-box/config.json`

Local Windows:

- Edit files.
- Run static checks.
- Do not assume local `gcloud` is authenticated.
- Use Git Bash at `D:\Dev\Git\bin\bash.exe` for `bash -n`; Windows `bash.exe` may point to WSL and fail if no distro is installed.

## Hard Guardrails

- Do not replace the existing Reality node unless the user explicitly asks.
- Do not remove `gcp-egress-guard`.
- Do not switch traffic to global proxy by default.
- Do not enable Cloudflare orange cloud, Argo, WARP, CDN, Load Balancer, Cloud NAT, or Cloud DNS as part of routine maintenance.
- Do not add UDP-heavy protocols by default.
- Do not commit or print secrets:
  - Cloudflare API Token
  - Global API Key
  - UUIDs from the real server
  - Reality private key
  - Certificate private key
  - Google Cloud credentials
- Do not use destructive GCP commands unless the user explicitly asks and the target resources are listed first.

## Editing Rules

- Keep Markdown in UTF-8.
- Keep shell scripts POSIX-ish Bash with `set -euo pipefail` when appropriate.
- Prefer idempotent scripts:
  - Detect existing firewall rules.
  - Detect existing config entries by tag.
  - Update rather than duplicate.
- For JSON config mutation, use structured parsing, not grep/sed rewrites.
- Before changing sing-box runtime config, make a timestamped backup.
- After changing sing-box config, run:

```bash
sing-box check -c <config-path>
```

If the check fails, restore the backup.

## File Responsibilities

- `README.md`: human entrypoint and quick start.
- `IMPLEMENTATION.md`: operational runbook.
- `CHOOSING-VLESS-NODES.md`: decision guide.
- `HYSTERIA2-EVALUATION.md`: cost gate, diagnosis, A/B acceptance, and rollback guide for Hysteria 2 evaluation.
- `AGENTS.md`: AI maintenance rules.
- `create-vm-standard-ipv4.sh`: VM creation baseline.
- `open-vless-reality-port.sh`: Reality firewall.
- `open-vless-tls-port.sh`: TLS firewall.
- `open-hysteria2-port.sh`: scoped HY2 UDP firewall rule.
- `audit-cost-and-usage.sh`: read-only billing-link, VM cost-property, budget, and month-to-date sent-byte audit.
- `install-egress-guard.sh`: monthly egress stop/mask guard.
- `issue-acme-cloudflare-token.sh`: certificate issuance with Cloudflare token.
- `add-vless-tls-inbound.sh`: append/update VLESS TLS inbound in sing-box config.
- `add-hysteria2-inbound.sh`: append/update HY2 in the existing sing-box process with backup and rollback.
- `export-vless-links.sh`: read-only VM helper that prints VLESS client share links; never persist real output.
- `verify-singbox-runtime.sh`: read-only runtime diagnostics.
- `collect-performance-baseline.sh`: read-only host, process, and network performance snapshot; never print credentials or full configs.
- `sing-box-client-*.json`: client fragments only; never put real secrets in them.

## Validation Checklist

After editing scripts locally:

```powershell
& 'D:\Dev\Git\bin\bash.exe' -n *.sh
```

After editing JSON templates:

```powershell
python -m json.tool sing-box-client-ai-routing.fragment.json
python -m json.tool sing-box-client-vless-tls.fragment.json
python -m json.tool sing-box-client-hysteria2.fragment.json
```

After editing Markdown:

```powershell
Get-Content -Encoding UTF8 -TotalCount 20 README.md
Get-Content -Encoding UTF8 -TotalCount 20 IMPLEMENTATION.md
Get-Content -Encoding UTF8 -TotalCount 20 CHOOSING-VLESS-NODES.md
Get-Content -Encoding UTF8 -TotalCount 20 HYSTERIA2-EVALUATION.md
```

On the VM after runtime changes:

```bash
sudo bash /tmp/verify-singbox-runtime.sh
sudo ss -lntp | grep -E ':(47370|443|8443) '
sudo ss -lunp | grep ':443 '
sudo systemctl status sing-box --no-pager
sudo systemctl status gcp-egress-guard.timer --no-pager
sudo journalctl -u sing-box -n 80 --no-pager
sudo journalctl -t gcp-egress-guard -n 50 --no-pager
```

## Troubleshooting Bias

Use this order before changing architecture:

1. Confirm `sing-box` service is active.
2. Confirm the port is listening.
3. Confirm GCP firewall allows the port.
4. Confirm client fields match the node type.
5. Confirm DNS is gray-cloud and points to VM IPv4.
6. Confirm certificate SAN matches TLS SNI.
7. Confirm actual target site behavior.

Do not treat v2rayN speed test `403 Forbidden` as the only failure signal. It may mean the test URL rejects the exit IP or request pattern while the node itself is fine.

## Cost Bias

Prefer choices that make cost visible and bounded:

- Keep Standard Tier assumptions documented.
- Keep `180 GiB` guard active.
- Avoid unnecessary GCP managed services.
- Avoid leftover static IPs, snapshots, disks, forwarding rules, Cloud Routers, and DNS zones.

When in doubt, run `audit-leftovers.sh` from Cloud Shell before adding new GCP resources.

## Documentation Expectations

Any behavior change should update at least one of:

- `README.md` for quick-start/user-facing changes.
- `IMPLEMENTATION.md` for operational commands.
- `CHOOSING-VLESS-NODES.md` for decision logic.
- `AGENTS.md` for AI maintenance rules.

Keep examples generic. Use placeholders like `rr.example.com`, `YOUR_UUID`, and `203.0.113.10/32`.
