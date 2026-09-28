#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ "$(uname -s)" == Darwin ]] || { echo '此打包脚本需要 macOS。' >&2; exit 1; }
if [[ "${1:-}" != --skip-build ]]; then ./scripts/build.sh; fi
app="${SRICS_APP_OUTPUT:-$PWD/dist/SRICS Next.app}"
[[ "$app" = /* && "$app" = *.app ]] || { echo 'SRICS_APP_OUTPUT 需要是以 .app 结尾的绝对路径。' >&2; exit 1; }
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/bin/tools" "$app/Contents/Resources/licenses"
cp bin/srics "$app/Contents/Resources/bin/srics"
swiftc -O -parse-as-library -target "$(uname -m)-apple-macosx13.0" desktop/*.swift -o "$app/Contents/MacOS/SRICS Next"
icon_workdir="$(mktemp -d "${TMPDIR:-/tmp}/srics-icon.XXXXXX")"
trap 'rm -rf "$icon_workdir"' EXIT
iconset="$icon_workdir/AppIcon.iconset"
mkdir "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" desktop/Assets/AppIcon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" desktop/Assets/AppIcon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o "$app/Contents/Resources/AppIcon.icns"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>SRICS Next</string>
<key>CFBundleDisplayName</key><string>SRICS Next</string>
<key>CFBundleIdentifier</key><string>com.soraincloud.srics.launcher</string>
<key>CFBundleExecutable</key><string>SRICS Next</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.3.0</string>
<key>CFBundleVersion</key><string>9</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
python3 scripts/bundle-macos-tools.py "$app/Contents/Resources"
# The launcher target alone does not determine compatibility: bundled Go/CGO
# executables and Homebrew libraries may require a newer macOS release.
python3 - "$app" <<'PY'
import pathlib
import plistlib
import re
import subprocess
import sys

app = pathlib.Path(sys.argv[1])
minimum = (13, 0)
files = [app / 'Contents/MacOS/SRICS Next'] + list((app / 'Contents/Resources/bin').rglob('*'))
for file in files:
    if not file.is_file():
        continue
    result = subprocess.run(['/usr/bin/otool', '-l', str(file)], capture_output=True, text=True)
    if result.returncode != 0:
        continue  # Licenses and other non-Mach-O resources.
    command = ''
    for line in result.stdout.splitlines():
        parts = line.strip().split()
        if len(parts) == 2 and parts[0] == 'cmd':
            command = parts[1]
        if len(parts) == 2 and ((command == 'LC_BUILD_VERSION' and parts[0] == 'minos') or
                               (command == 'LC_VERSION_MIN_MACOSX' and parts[0] == 'version')):
            if re.fullmatch(r'\d+(\.\d+){1,2}', parts[1]):
                minimum = max(minimum, tuple(map(int, parts[1].split('.'))))
info_path = app / 'Contents/Info.plist'
with info_path.open('rb') as source:
    info = plistlib.load(source)
info['LSMinimumSystemVersion'] = '.'.join(map(str, minimum))
with info_path.open('wb') as target:
    plistlib.dump(info, target)
print('程序包最低 macOS 版本：' + info['LSMinimumSystemVersion'])
PY
codesign --force --sign - "$app/Contents/Resources/bin/srics"
codesign --force --sign - "$app/Contents/MacOS/SRICS Next"
codesign --force --sign - "$app"
codesign --verify --deep --strict "$app"
echo "已构建：$app"
