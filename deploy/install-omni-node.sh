#!/usr/bin/env bash
set -Eeuo pipefail

# Omni Proxy 节点固定安装器。版本写死，避免生产节点误装滚动版。
readonly XUI_VERSION="v3.7.2"
readonly INSTALL_URL="https://raw.githubusercontent.com/SynexIM/3x-ui/${XUI_VERSION}/install.sh"

export XUI_NONINTERACTIVE=1
export XUI_PANEL_PORT="${XUI_PANEL_PORT:-2053}"
export XUI_SSL_MODE="${XUI_SSL_MODE:-none}"
export XUI_ENABLE_FAIL2BAN="${XUI_ENABLE_FAIL2BAN:-true}"

echo "Installing SynexIM/3x-ui ${XUI_VERSION} for Omni Proxy..."
curl --fail --silent --show-error --location "${INSTALL_URL}" | bash -s -- "${XUI_VERSION}"

echo
echo "Installation complete. Read the generated panel credentials with:"
echo "  sudo cat /etc/x-ui/install-result.env"
