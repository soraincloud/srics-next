#!/bin/zsh
set -eu
cd "${0:A:h}"
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
if [[ ! -x ./bin/srics ]]; then
  echo '首次运行需要构建开发版本。'
  ./scripts/build.sh
fi
echo '启动 SRICS Next 本机资料库。关闭本窗口或按 Ctrl+C 可停止。'
if [[ "$(uname -s)" == Darwin ]]; then
  (sleep 1; open 'http://127.0.0.1:19473') &
fi
exec ./bin/srics serve
