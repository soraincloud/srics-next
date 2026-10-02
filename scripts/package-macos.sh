#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ "$(uname -s)" == Darwin ]] || { echo '此打包脚本需要 macOS。' >&2; exit 1; }
if [[ "${1:-}" != --skip-build ]]; then ./scripts/build.sh; fi
python3 - <<'PY'
import json
import pathlib
import re
import subprocess
import sys

declared = json.loads(pathlib.Path('internal/buildinfo/release.json').read_text())
if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', declared.get('version', '')) or type(declared.get('build')) is not int or declared['build'] < 1:
    sys.exit('release.json 版本或构建号无效。')
try:
    actual = json.loads(subprocess.check_output(['bin/srics', 'version', '--json']))
except (subprocess.CalledProcessError, json.JSONDecodeError, OSError):
    sys.exit('无法读取服务版本，请先运行 scripts/build.sh。')
if any(actual.get(key) != declared[key] for key in ('version', 'build')):
    sys.exit('服务版本与 release.json 不一致，请重新构建后打包。')
print('准备打包：' + actual['label'])
PY
app="${SRICS_APP_OUTPUT:-$PWD/dist/SRICS Next.app}"
[[ "$app" = /* && "$app" = *.app ]] || { echo 'SRICS_APP_OUTPUT 需要是以 .app 结尾的绝对路径。' >&2; exit 1; }
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources/bin/tools" "$app/Contents/Resources/licenses"
cp bin/srics "$app/Contents/Resources/bin/srics"
python3 scripts/prepare-open-source.py --verify
mkdir -p "$app/Contents/Resources/licenses/SRICS-Next"
cp internal/webui/dist/legal/source.tar.gz internal/webui/dist/legal/source-info.json "$app/Contents/Resources/"
cp internal/webui/dist/legal/*.txt "$app/Contents/Resources/licenses/SRICS-Next/"
swiftc -O -parse-as-library -target "$(uname -m)-apple-macosx13.0" desktop/*.swift -o "$app/Contents/MacOS/SRICS Next"
icon_workdir="$(mktemp -d "${TMPDIR:-/tmp}/srics-icon.XXXXXX")"
trap 'rm -rf "$icon_workdir"' EXIT
# Compile the layered Icon Composer source, retaining Liquid Glass materials and
# system appearance variants. actool also creates the flattened ICNS fallback.
xcrun actool desktop/Assets/AppIcon.icon \
  --compile "$icon_workdir" --output-format human-readable-text \
  --notices --warnings --errors --output-partial-info-plist "$icon_workdir/icon-info.plist" \
  --app-icon AppIcon --include-all-app-icons --enable-on-demand-resources NO \
  --development-region en --target-device mac --minimum-deployment-target 13.0 --platform macosx
cp "$icon_workdir/Assets.car" "$icon_workdir/AppIcon.icns" "$app/Contents/Resources/"
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
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
python3 scripts/bundle-macos-tools.py "$app/Contents/Resources"
# The launcher target alone does not determine compatibility: bundled Go/CGO
# executables and Homebrew libraries may require a newer macOS release.
python3 - "$app" "$icon_workdir/icon-info.plist" <<'PY'
import pathlib
import json
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
with pathlib.Path(sys.argv[2]).open('rb') as source:
    info.update(plistlib.load(source))
info['LSMinimumSystemVersion'] = '.'.join(map(str, minimum))
release = json.loads(subprocess.check_output([str(app / 'Contents/Resources/bin/srics'), 'version', '--json']))
info['CFBundleShortVersionString'] = release['version'].split('-')[0]
info['CFBundleVersion'] = str(release['build'])
info['CFBundleGetInfoString'] = 'SRICS Next ' + release['label']
info['SRICSVersion'] = release['version']
info['SRICSCommit'] = release['commit']
info['SRICSBuildTime'] = release['builtAt']
(app / 'Contents/Resources/release.json').write_text(json.dumps(release, ensure_ascii=False, indent=2) + '\n')
with info_path.open('wb') as target:
    plistlib.dump(info, target)
print('程序包最低 macOS 版本：' + info['LSMinimumSystemVersion'])
PY
codesign --force --sign - "$app/Contents/Resources/bin/srics"
codesign --force --sign - "$app/Contents/MacOS/SRICS Next"
codesign --force --sign - "$app"
codesign --verify --deep --strict "$app"
echo "已构建：$app"
