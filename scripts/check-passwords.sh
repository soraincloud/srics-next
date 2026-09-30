#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
test_binary="$(mktemp "${TMPDIR:-/tmp}/srics-password-check.XXXXXX")"
trap 'rm -f "$test_binary"' EXIT
swiftc desktop/PasswordSettings.swift desktop/tests/PasswordSettingsChecks.swift -o "$test_binary"
"$test_binary"
