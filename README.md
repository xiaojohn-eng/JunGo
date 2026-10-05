# 军哥互联 JunGo

面向个人设备的 Android 代理、私有组网、设备聊天和文件传输客户端，配套 macOS 菜单栏程序、Linux 设备服务、自托管控制与加密数据中继。

当前为 **0.2.3-preview 预览版**，面向自己的多台设备，不提供公共社交、群聊或多用户账号。源码以 GPL-3.0-only 开源，第三方组件保留原许可证。

本公开仓库由经过检查的源码快照建立，不包含开发环境历史、实机截图、个人配置、订阅、密钥或部署日志。安全说明见 [SECURITY.md](SECURITY.md) 和 [发布扫描说明](docs/publication-security.md)，测试与限制见 [验证说明](docs/validation.md)。

## 项目结构

| 目录 | 内容 |
| --- | --- |
| `android/` | Kotlin / Compose、唯一 VpnService、SAF 文件选择、前台传输服务 |
| `macos/` | SwiftUI 菜单栏、连接与配对、共享目录、登录后启动 |
| `internal/control/`、`internal/relay/` | 一次性配对、设备撤销、状态同步、加密帧中继 |
| `internal/mesh/` | WireGuard userspace 网络、直连探测、自托管 STUN、中继切换 |
| `internal/proxycore/` | mihomo 集成、私网优先路由、应用 UID 绕过 |
| `internal/files/`、`internal/engine/` | 授权文件接口、TLS 指纹、任务持久化、校验与恢复 |
| `internal/chat/` | 自己的设备会话、修订号去重、本地持久记录 |
| `cmd/jungo/`、`mobile/` | 桌面/Linux CLI 和 Android Go JNI 接口 |
| `deploy/` | Linux systemd 服务模板 |

Android 12+，支持折叠屏内外屏、窗口缩放和大字体。布局根据实际窗口与折叠特征切换。Mac 端是菜单栏程序，不包含 iPhone 或 iPad 客户端。

## 下载与安装

在 [GitHub Releases](https://github.com/xiaojohn-eng/JunGo/releases) 下载与你的设备匹配的预览包：Android 12+ / arm64 的 APK、Apple Silicon Mac 的 DMG 或 ZIP、Linux amd64 / arm64 的压缩包。安装步骤、服务器部署、配对和平台限制见 [下载与部署指南](docs/download-install.md)。包内包含对应程序和运行所需资源；源码归档与校验清单在同一 Release 中。首次使用仍需自己的控制服务器与各设备独立的一次性配对码。

## 构建

需要 Python 3.12 或更新版本运行 bootstrap（归档安全解压使用 `filter` 参数）。已验证的其他工具链：Go 1.27.1、JDK 21、Android SDK 36 / Build Tools 36.0.0 / NDK 28.2.13676358、Swift 6.3.3。工具放在项目 `.tools/`，不用修改系统 Go/JDK。

```sh
git clone https://github.com/xiaojohn-eng/JunGo.git
cd JunGo
python3 scripts/bootstrap.py go java android
export JAVA_HOME="$PWD/.tools/jdk"
export ANDROID_HOME="$PWD/.tools/android-sdk"
# 阅读并接受 Android SDK 许可，然后安装构建所需组件。
"$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$ANDROID_HOME" \
  "platform-tools" "platforms;android-36" "build-tools;36.0.0" "ndk;28.2.13676358"
export PATH="$PWD/.tools/go/bin:$PWD/.tools/bin:$PATH"
export GOPROXY=https://goproxy.cn,direct
GOBIN="$PWD/.tools/bin" go install golang.org/x/mobile/cmd/gomobile@v0.0.0-20260908204917-8b95e45f8d3e
GOBIN="$PWD/.tools/bin" go install golang.org/x/mobile/cmd/gobind@v0.0.0-20260908204917-8b95e45f8d3e
sh scripts/build-core.sh
JUNGO_RELEASE_SANITIZE=1 sh scripts/build-android-core.sh
JAVA_HOME="$PWD/.tools/jdk" ANDROID_HOME="$PWD/.tools/android-sdk" \
  android/gradlew -p android -PpreviewSigning=true :app:assembleRelease :app:testReleaseUnitTest :app:lintRelease
sh scripts/build-macos.sh
```

精简后的 APK 在 `android/app/build/outputs/apk/release/app-release.apk`。`-PpreviewSigning=true` 明确使用本机预览签名；仅能覆盖由同一证书签名的旧预览版并保留数据。不传该参数时 release 保持未签名。开发调试仍可使用 `:app:assembleDebug`。Mac 包使用 ad-hoc 签名，未经过 Apple 公证，首次打开需按 [安装指南](docs/download-install.md) 在系统设置中确认。Linux 分别生成 amd64 和 arm64 二进制；Mac 包当前面向 Apple Silicon。

## 首次使用

1. 按 [部署说明](docs/deployment.md) 在自己的服务器运行控制与中继服务。服务身份和 SHA-256 证书指纹绑定到配对信息；设备端不注册账号。
2. 在服务器运行 `jungo pair-code` 生成一次性配对信息。分别为 Mac、Linux、安卓生成独立配对码；每个码只能使用一次。
3. Mac 菜单栏中加入网络，选择主动共享的目录，设置只读或可写。Linux 使用同样的后台引擎和本地 RPC。
4. Android 导入配对信息后建立设备连接，即可在 App 内互发消息和文件。公网代理需另行导入自己的 Clash YAML 或 HTTPS 订阅。其他 App 访问设备使用系统 VPN；关闭公网代理不影响设备消息和文件连接。
5. 在“设备”中选择 Mac/Linux 的共享目录；上传或下载后可在传输列表暂停、恢复或取消。恢复时重新验证源文件版本；主动暂停或取消的任务不会自动开始。
6. 在“会话”中选择自己的另一台设备，发送文字或文件。Mac 可拖入文件；安卓可用系统“分享”发送到军哥互联。收到的文件先保存在应用专用收件箱，点击“另存为”选择目标。详见 [设备会话](docs/chat.md)。

访问设备上的 SSH 或测试网站，需要在 Mac/Linux 添加明确的本机 TCP 服务映射，例如私网端口 22 → `127.0.0.1:22`。当前 userspace 实现不会自动暴露主机所有端口，也不提供家庭网段路由。文件服务在设备私网的 8443 端口，受独立设备身份认证保护。

## 网络与文件行为

- 私网 `100.96.0.0/16` 和 `.jungo.internal` 优先于规则、全局、直连以及应用公网绕过。未知、离线、撤销或关闭组网的私网目标不会降级发给普通代理。
- 一个 Android VPN 同时承接公网代理和私网。底层控制、WireGuard 和代理 socket 使用 protect，避免回流。
- 直连探测失败时通过自建服务器转发 WireGuard 密文。控制服务管理公钥与设备授权；设备私钥只保存在本地私有状态目录。
- 文件服务通过 WireGuard、固定设备 TLS 指纹和 X25519 派生的请求认证访问。控制服务的 bearer token 不会发送给文件节点。流式访问会重新检查撤销状态。
- 新增聊天收件箱后，安卓也可在组网内接收消息和附件；只暴露应用私有收件箱，不开放其他手机目录。文字与文件卡片使用同一会话，文件内容复用持久传输队列。
- 文件上传分块确认，下载使用 ETag / Range / If-Match，完整文件再校验 SHA-256。同名上传默认生成新名称；覆盖要求显式参数。符号链接越界、路径穿越和只读写入被拒绝。
- Android 使用系统文件授权和前台服务。被系统终止后保留已确认断点；用户强制停止后需要重新打开应用。不能承诺无限后台运行。

## 测试与源码

```sh
.tools/go/bin/go test -tags=cmfa,with_gvisor -race ./internal/... ./mobile ./cmd/jungo
sh macos/test.sh
# 真实 WireGuard + HTTPS 的双向 5 GiB 桌面集成测试，占用约 15 GiB 临时空间
JUNGO_LARGE_TEST=1 .tools/go/bin/go test -tags=cmfa,with_gvisor \
  -timeout=60m -run TestLargeFiveGiBTransfer -v ./internal/engine
```

JunGo 自有代码采用 [GNU GPL v3](LICENSE)（SPDX: GPL-3.0-only）。底层包含 mihomo（GPL-3.0）、wireguard-go、gVisor 等开源组件。第三方许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)；交付二进制时同时保留相应源码、构建脚本和依赖版本。

## 公开发布范围

预览版 Release 提供安装包、完整源码归档和 SHA-256 校验清单。曾引起二进制再分发冲突的 `go-lzo` 已从构建依赖移除；OpenVPN 的 `comp-lzo: yes/adaptive` 会明确拒绝，详见 [第三方声明](THIRD_PARTY_NOTICES.md)。安装包不含服务器地址、配对码、个人配置或运行数据。

`python3 scripts/package-release.py --source-only` 只归档当前 Git HEAD 的源码；`--all` 打包经验证的各平台二进制、许可证及部署资源。构建缓存、运行数据和未跟踪文件不会进入源码归档；脚本遇到已提交的敏感文件路径会拒绝打包。
