#!/usr/bin/env bash
# ============================================================================
# opidcs 一键安装（Linux / macOS）
#
#   稳定版：
#     curl -fsSL https://raw.githubusercontent.com/OpenIDCSTeam/CloudHost/main/scripts/opidcs-install.sh | sh
#   测试版（beta 通道，随 main 自动构建）：
#     curl -fsSL .../opidcs-install.sh | sh -s -- --channel beta
#   指定版本 / 目录 / 离线包：
#     curl -fsSL .../opidcs-install.sh | sh -s -- --version 1.2.3 --dir /usr/local/bin
#     curl -fsSL .../opidcs-install.sh | sh -s -- --file ./opidcs-linux-amd64
# ============================================================================
set -euo pipefail

REPO="${OPIDCS_REPO:-OpenIDCSTeam/CloudHost}"
DOWNLOAD_BASE="${OPIDCS_DOWNLOAD_BASE:-}"
CHANNEL="stable"
VERSION=""
DIR="${HOME}/.local/bin"
FILE=""

usage() {
  cat <<EOF
用法: opidcs-install.sh [选项]
  --channel, -c <stable|beta>  发布通道（默认 stable）
  --version, -v <tag>          指定版本，如 1.2.3 或 1.2.3-beta.1（默认最新）
  --dir <path>                 安装目录（默认 ~/.local/bin）
  --file, -f <path>            使用本地二进制（离线）
  --help, -h                   显示帮助

环境变量：
  OPIDCS_REPO            GitHub 仓库，默认 OpenIDCSTeam/CloudHost
  OPIDCS_DOWNLOAD_BASE   自定义下载前缀（镜像），形如 https://mirror.example.com/opidcs
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --channel|-c) CHANNEL="${2:?缺少通道}"; shift 2 ;;
    --version|-v) VERSION="${2:?缺少版本号}"; shift 2 ;;
    --dir)        DIR="${2:?缺少目录}"; shift 2 ;;
    --file|-f)    FILE="${2:?缺少文件}"; shift 2 ;;
    --help|-h)    usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage; exit 1 ;;
  esac
done

case "${CHANNEL}" in
  stable|beta) ;;
  *) echo "通道必须是 stable 或 beta，当前: ${CHANNEL}" >&2; exit 1 ;;
esac

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  linux|darwin) ;;
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
  tag=""
  if [[ -n "${VERSION}" ]]; then
    version="${VERSION#v}"
    tag="v${version}"
  elif [[ "${CHANNEL}" == "beta" ]]; then
    tag="beta"
  else
    echo "==> 查询最新稳定版"
    tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
      | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | cut -d'"' -f4)"
  fi
  if [[ -z "${tag}" ]]; then
    echo "无法确定版本，请用 --version 指定" >&2
    exit 1
  fi

  if [[ -n "${DOWNLOAD_BASE}" ]]; then
    url="${DOWNLOAD_BASE%/}/${tag}/${asset}"
  else
    url="https://github.com/${REPO}/releases/download/${tag}/${asset}"
  fi
  echo "==> 通道 ${CHANNEL}，下载 ${url}"
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
