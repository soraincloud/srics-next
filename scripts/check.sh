#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
./scripts/build.sh
npm --prefix web test
go vet ./...
go test -race ./...
go test -tags integration ./internal/verification ./internal/library ./internal/server -count=1
mkdir -p reports
report="reports/m0-$(date +%Y%m%d-%H%M%S).json"
./bin/srics verify --report "$report"
echo "验证报告：$report"
