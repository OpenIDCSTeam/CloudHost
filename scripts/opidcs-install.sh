#!/usr/bin/env bash
# ============================================================================
# opidcs 一键安装（Linux / macOS）
#
#   curl -fsSL https://raw.githubusercontent.com/OpenIDCSTeam/CloudHost/main/scripts/opidcs-install.sh | sh
#   curl -fsSL .../opidcs-install.sh | sh -s -- --version v1.0.0 --dir /usr/local/bin
#   curl -fsSL .../opidcs-install.sh | sh -s -- --file ./opidcs-linux-amd64
# ============================================================================
set -euo pipefail

REPO="OpenIDCSTeam/CloudHost"
VERSION=""
DIR="${HOME}/.local/bin"
FILE=""

usage() {
  cat <<EOF
用法: opidcs-install.sh [选项]
  --version, -v <tag>   指定版本，如 v1.0.0（默认最新）
  --dir <path>          安装目录（默认 ~/.local/bin）
  --file, -f <path>     使用本地二进制（离线）
  --help, -h            显示帮助
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version|-v) VERSION="${2:?缺少版本号}"; shift 2 ;;
    --dir)        DIR="${2:?缺少目录}"; shift 2 ;;
    --file|-f)    FILE="${2:?缺少文件}"; shift 2 ;;
    --help|-h)    usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage; exit 1 ;;
  esac
done

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux) os="linux" ;;
  darwin) os="darwin" ;;
  *) echo "不支持的系统: $os" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

asset="opidcs-${os}-${arch}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if [[ -n "${FILE}" ]]; then
  echo "==> 使用本地文件 ${FILE}"
  cp "${FILE}" "$tmp"
else
  if [[ -z "${VERSION}" ]]; then
    echo "==> 查询最新版本"
    VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | cut -d'"' -f4)"
  fi
  if [[ -z "${VERSION}" ]]; then
    echo "无法确定版本，请用 --version 指定" >&2
    exit 1
  fi
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  echo "==> 下载 ${url}"
  curl -fL --retry 3 -o "$tmp" "$url"
fi

mkdir -p "${DIR}"
install -m 0755 "$tmp" "${DIR}/opidcs"

echo "==> 已安装到 ${DIR}/opidcs"
"${DIR}/opidcs" version || true

# shell 补全
if command -v opidcs >/dev/null 2>&1; then
  case "${SHELL:-}" in
    */zsh)
      mkdir -p "${HOME}/.zsh/completions"
      opidcs completion zsh > "${HOME}/.zsh/completions/_opidcs" 2>/dev/null || true
      ;;
    */bash)
      mkdir -p "${HOME}/.local/share/bash-completion/completions"
      opidcs completion bash > "${HOME}/.local/share/bash-completion/completions/opidcs" 2>/dev/null || true
      ;;
  esac
fi

case ":${PATH}:" in
  *":${DIR}:"*) ;;
  *) echo "提示: 请把 ${DIR} 加入 PATH，例如：echo 'export PATH=\"${DIR}:\$PATH\"' >> ~/.bashrc" ;;
esac
