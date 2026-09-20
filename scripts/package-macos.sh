#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ "$(uname -s)" == Darwin ]] || { echo '此打包脚本需要 macOS。' >&2; exit 1; }
if [[ "${1:-}" != --skip-build ]]; then ./scripts/build.sh; fi
app="$PWD/dist/SRICS Next.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/bin/tools" "$app/Contents/Resources/licenses"
cp bin/srics "$app/Contents/Resources/bin/srics"
swiftc -O -parse-as-library -target "$(uname -m)-apple-macosx13.0" desktop/*.swift -o "$app/Contents/MacOS/SRICS Next"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>SRICS Next</string>
<key>CFBundleDisplayName</key><string>SRICS Next</string>
<key>CFBundleIdentifier</key><string>com.soraincloud.srics.launcher</string>
<key>CFBundleExecutable</key><string>SRICS Next</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.3.0</string>
<key>CFBundleVersion</key><string>3</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
python3 scripts/bundle-macos-tools.py "$app/Contents/Resources"
codesign --force --sign - "$app/Contents/Resources/bin/srics"
codesign --force --sign - "$app/Contents/MacOS/SRICS Next"
codesign --force --sign - "$app"
codesign --verify --deep --strict "$app"
echo "已构建：$app"
