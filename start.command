#!/bin/zsh
set -eu
cd "${0:A:h}"
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
if [[ ! -d './dist/SRICS Next.app' ]]; then
  ./scripts/package-macos.sh
fi
open './dist/SRICS Next.app'
