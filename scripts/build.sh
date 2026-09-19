#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null || { echo '需要 Go 1.26.5 或更新版本。' >&2; exit 1; }
command -v npm >/dev/null || { echo '构建前端需要 Node.js 22.12+ 或 24+。' >&2; exit 1; }
npm --prefix web ci
npm --prefix web run build
touch internal/webui/dist/.gitkeep
mkdir -p bin
CGO_ENABLED=1 go build -trimpath -o bin/srics ./cmd/srics
echo '已构建 bin/srics；运行 ./start.command 打开本机验证版。'
