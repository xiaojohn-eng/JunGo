#!/bin/bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
agent_binary="${JUNGO_BINARY:-$project_root/dist/jungo}"
build_configuration="${JUNGO_SWIFT_CONFIGURATION:-release}"
sign_identity="${JUNGO_SIGN_IDENTITY:--}"
dist_directory="$project_root/dist"
output_bundle="$dist_directory/军哥互联.app"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Mac 应用必须在 macOS 上构建。" >&2
  exit 1
fi
if ! command -v swift >/dev/null 2>&1; then
  echo "找不到 Swift。请安装 Apple Command Line Tools 后重试。" >&2
  exit 1
fi
if [[ ! -f "$agent_binary" || ! -x "$agent_binary" ]]; then
  echo "缺少可执行的 Mac 后台程序：$agent_binary" >&2
  echo "请先构建 dist/jungo，或通过 JUNGO_BINARY 指定 Darwin 版本的 jungo。" >&2
  exit 1
fi
binary_format="$(file -b "$agent_binary")"
case "$binary_format" in
  *Mach-O*) ;;
  *) echo "后台程序不是 Mac Mach-O 可执行文件：$agent_binary" >&2; exit 1 ;;
esac
agent_architectures="$(lipo -archs "$agent_binary")"
case " $agent_architectures " in
  *" $(uname -m) "*) ;;
  *) echo "后台架构（$agent_architectures）与当前 Mac（$(uname -m)）不匹配。" >&2; exit 1 ;;
esac

# Keep source locations in the released executable independent of the builder's
# account and checkout path. Both debug records and #filePath literals need maps.
swift_build_args=(
  --package-path "$project_root/macos"
  --configuration "$build_configuration"
  -Xswiftc -debug-prefix-map -Xswiftc "$project_root=/src/jungo"
  -Xswiftc -file-prefix-map -Xswiftc "$project_root=/src/jungo"
  -Xswiftc -debug-prefix-map -Xswiftc "$HOME=/src/builder"
  -Xswiftc -file-prefix-map -Xswiftc "$HOME=/src/builder"
)
swift build "${swift_build_args[@]}"
swift_binary_directory="$(swift build "${swift_build_args[@]}" --show-bin-path)"
swift_binary="$swift_binary_directory/JunGoMenu"
if [[ ! -x "$swift_binary" ]]; then
  echo "Swift 构建没有生成 JunGoMenu 可执行文件。" >&2
  exit 1
fi

mkdir -p "$dist_directory"
package_work="$(mktemp -d "${TMPDIR:-/tmp}/jungo-macos.XXXXXX")"
trap 'rm -rf "$package_work"' EXIT
# Build/sign outside File Provider directories: their background metadata writes
# can otherwise race codesign even immediately after xattr cleanup.
app_bundle="$package_work/军哥互联.app"
mkdir -p "$app_bundle/Contents/MacOS" "$app_bundle/Contents/Resources"
install -m 755 "$swift_binary" "$app_bundle/Contents/MacOS/JunGoMenu"
install -m 755 "$agent_binary" "$app_bundle/Contents/MacOS/jungo"
# ld records the original object-file paths as debug symbols even when Swift
# remaps source paths. Remove those symbols before signing the release bundle.
strip -S "$app_bundle/Contents/MacOS/JunGoMenu"
python3 - "$app_bundle/Contents/MacOS/JunGoMenu" "$app_bundle/Contents/MacOS/jungo" "$project_root" "$HOME" <<'PY'
from pathlib import Path
import sys

for executable in sys.argv[1:3]:
    data = Path(executable).read_bytes()
    if any(path.encode() in data for path in sys.argv[3:]):
        raise SystemExit('Release executable contains the builder home or checkout path')
PY
install -m 644 "$project_root/THIRD_PARTY_NOTICES.md" "$app_bundle/Contents/Resources/THIRD_PARTY_NOTICES.md"
COPYFILE_DISABLE=1 cp -R "$project_root/licenses" "$app_bundle/Contents/Resources/licenses"
chmod -R u+rwX "$app_bundle/Contents/Resources/licenses"
if ! cmp -s "$agent_binary" "$app_bundle/Contents/MacOS/jungo"; then
  echo "后台程序在复制期间发生变化，请等待后台构建完成后重新打包。" >&2
  exit 1
fi
shasum -a 256 "$app_bundle/Contents/MacOS/jungo" | awk '{print $1}' > "$app_bundle/Contents/Resources/backend-source.sha256"
icon_directory="$project_root/macos/.build/AppIcon.iconset"
swift "$project_root/macos/Tools/GenerateIcon.swift" "$icon_directory"
iconutil -c icns "$icon_directory" -o "$app_bundle/Contents/Resources/AppIcon.icns"

cat > "$app_bundle/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key><string>zh_CN</string>
    <key>CFBundleDisplayName</key><string>军哥互联</string>
    <key>CFBundleName</key><string>军哥互联</string>
    <key>CFBundleExecutable</key><string>JunGoMenu</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>CFBundleIdentifier</key><string>com.junge.connect.mac</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>0.2.3</string>
    <key>CFBundleVersion</key><string>5</string>
    <key>LSMinimumSystemVersion</key><string>13.0</string>
    <key>LSUIElement</key><true/>
    <key>LSMultipleInstancesProhibited</key><true/>
    <key>LSApplicationCategoryType</key><string>public.app-category.utilities</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSPrincipalClass</key><string>NSApplication</string>
    <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
    <key>NSLocalNetworkUsageDescription</key><string>连接你配对的设备，访问服务并传输共享文件。</string>
    <key>NSDocumentsFolderUsageDescription</key><string>访问你明确选择共享的文稿目录。</string>
    <key>NSDownloadsFolderUsageDescription</key><string>访问你明确选择共享的下载目录。</string>
    <key>NSDesktopFolderUsageDescription</key><string>访问你明确选择共享的桌面目录。</string>
</dict>
</plist>
PLIST

plutil -lint "$app_bundle/Contents/Info.plist"
# Finder/File Provider metadata can be added when building under Documents.
# Clean only this generated bundle; resource forks are not valid signing inputs.
xattr -cr "$app_bundle"
if [[ "$sign_identity" == "-" ]]; then
  codesign --force --sign - --timestamp=none "$app_bundle/Contents/MacOS/jungo"
  codesign --force --sign - --timestamp=none "$app_bundle"
else
  codesign --force --sign "$sign_identity" --options runtime --timestamp "$app_bundle/Contents/MacOS/jungo"
  codesign --force --sign "$sign_identity" --options runtime --timestamp "$app_bundle"
fi
codesign --verify --deep --strict --verbose=2 "$app_bundle"

if [[ "${JUNGO_CREATE_DMG:-1}" == "1" ]]; then
  dmg_staging="$package_work/dmg"
  mkdir -p "$dmg_staging"
  COPYFILE_DISABLE=1 cp -R "$app_bundle" "$dmg_staging/军哥互联.app"
  xattr -cr "$dmg_staging/军哥互联.app"
  codesign --verify --deep --strict "$dmg_staging/军哥互联.app"
  ln -s /Applications "$dmg_staging/Applications"
  dmg_path="$dist_directory/军哥互联-0.2.3-preview-$(uname -m).dmg"
  hdiutil create -volname "军哥互联" -srcfolder "$dmg_staging" -ov -format UDZO "$dmg_path"
  echo "安装镜像：$dmg_path"
fi

# A File Provider may reattach FinderInfo even after an immediate verification
# passes. Preserve the signed bundle in immutable containers instead of leaving
# a loose app whose signature can be damaged asynchronously by the output folder.
rm -rf "$output_bundle"
zip_path="$dist_directory/军哥互联.app.zip"
ditto -c -k --keepParent --norsrc --noextattr "$app_bundle" "$zip_path"
echo "应用归档：$zip_path"
echo "已签名应用保存在 ZIP/DMG 中，请从安装镜像拖入应用程序。"
echo "构建完成。尚未启动应用、安装登录项或修改后台服务。"
if [[ "$sign_identity" == "-" ]]; then
  echo "当前为本机 ad-hoc 签名构建，尚未经过 Apple 公证。"
fi
