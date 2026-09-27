#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p reports
npm --prefix web audit --json > reports/security-npm.json
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -show verbose ./... > reports/security-srics.txt
python3 scripts/build-restic.py --audit
echo '依赖安全检查通过；详细结果位于 reports/security-*（不构成零漏洞保证）。'
