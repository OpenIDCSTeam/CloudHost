#!/bin/bash
# HostAgent-Go 构建脚本 (Linux/macOS)
# 用法: ./build.sh [linux|windows|darwin]

BINARY="hostagent"
MAIN="./cmd/server"

TARGET=${1:-linux}

case $TARGET in
  linux)
    GOOS=linux GOARCH=amd64 OUTPUT="${BINARY}-linux-amd64"
    ;;
  windows)
    GOOS=windows GOARCH=amd64 OUTPUT="${BINARY}-windows-amd64.exe"
    ;;
  darwin)
    GOOS=darwin GOARCH=arm64 OUTPUT="${BINARY}-darwin-arm64"
    ;;
  *)
    echo "用法: $0 [linux|windows|darwin]"
    exit 1
    ;;
esac

echo "正在构建 ${GOOS}/${GOARCH}..."
go mod tidy
# SQLite 驱动为纯 Go 实现，关闭 CGO 可免装 C 工具链并支持交叉编译
CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -v -o $OUTPUT $MAIN
echo "构建完成: $OUTPUT"

OPIDCS_OUTPUT="opidcs-${GOOS}-${GOARCH}"
if [ "$GOOS" = "windows" ]; then
  OPIDCS_OUTPUT="${OPIDCS_OUTPUT}.exe"
fi
CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -trimpath -ldflags "-s -w" -o "$OPIDCS_OUTPUT" ./cmd/opidcs
echo "构建完成: $OPIDCS_OUTPUT"
