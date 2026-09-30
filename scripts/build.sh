#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
command -v go >/dev/null || { echo '需要 Go 1.27.1 或支持自动下载该工具链的 Go。' >&2; exit 1; }
command -v npm >/dev/null || { echo '构建前端需要 Node.js 22.12+ 或 24+。' >&2; exit 1; }
npm --prefix web ci
npm --prefix web run build
touch internal/webui/dist/.gitkeep
mkdir -p bin
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
CGO_ENABLED=1 go build -trimpath -ldflags "-X github.com/soraincloud/srics-next/internal/buildinfo.BuiltAt=$build_time" -o bin/srics ./cmd/srics
python3 scripts/build-restic.py
echo '已构建 bin/srics；运行 ./start.command 打开本机资料库。'
