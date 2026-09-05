# Cloud deployment (unattended install)

Tooling to ship the 3x-ui panel via unattended install, with **per-instance
credentials generated on first boot** (never `admin/admin`, never a shared
session secret). Works on amd64 and arm64.

| Path | What it is | Use when |
| --- | --- | --- |
| [`cloud-init/`](cloud-init/) | Generic cloud-init user-data (unattended `install.sh`) | Any cloud, no image build |
| [`marketplace/hetzner/`](marketplace/hetzner/) | Hetzner Cloud notes | Hetzner deployments |
| [`test/`](test/) | Container smoke test | Verifying the install path |

## How it works

`install.sh` runs unattended when `XUI_NONINTERACTIVE=1` or stdin is not a TTY.
Each instance installs and configures itself with random credentials. See
[`cloud-init/README.md`](cloud-init/README.md).

## Unattended install knobs

`install.sh` reads these env vars in non-interactive mode (all optional; unset ⇒
secure random / default):

`XUI_USERNAME`, `XUI_PASSWORD`, `XUI_PANEL_PORT`, `XUI_WEB_BASE_PATH`,
`XUI_SSL_MODE` (`none`|`ip`|`domain`, default `none`), `XUI_DOMAIN`,
`XUI_ACME_EMAIL`, `XUI_ACME_HTTP_PORT` (ACME HTTP-01 listener port, default `80`),
`XUI_SSL_IPV6` (optional IPv6 address to add to an `ip`-mode cert),
`XUI_SERVER_IP` (fallback IP for the displayed access URL when auto-detection fails),
`XUI_DB_TYPE` (`sqlite`|`postgres`), `XUI_DB_DSN`.

The resulting credentials are written to `/etc/x-ui/install-result.env` (mode 600).

## Omni Proxy 固定版本一键安装

在 Ubuntu 服务器上以 root 执行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/SynexIM/3x-ui/v3.7.2/deploy/install-omni-node.sh)
```

该入口固定安装 `v3.7.2`，默认使用 HTTP、面板端口 `2053`，并为管理账号、
密码、Web 路径和 API token 生成独立随机值。安装后执行
`sudo cat /etc/x-ui/install-result.env` 查看需要录入控制面的信息。
