# 下载与部署 JunGo 0.2.3-preview

从 [JunGo Releases](https://github.com/xiaojohn-eng/JunGo/releases) 下载与你的设备架构对应的文件。`SHA256SUMS.txt` 列出每个发布文件的 SHA-256；下载后可用 `shasum -a 256 -c SHA256SUMS.txt`（Mac）或 `sha256sum -c SHA256SUMS.txt`（Linux）核对。源码和完整许可证资源在同一 Release；应用包内也附有第三方声明与许可证。

此预览包没有写入任何现成服务器、设备身份、配对码或代理订阅。要与其他设备连接，先部署自己的控制服务，或使用你已管理的 JunGo 控制服务为每台设备生成独立的一次性配对码。

## Android

下载 `jungo-v0.2.3-preview-android-arm64.apk`，在 Android 12+、arm64 设备上打开安装。系统可能要求允许当前下载应用安装 APK。安装后输入自己控制服务提供的一次性配对信息，再按界面授权 VPN 与所需文件访问。APK 包含 Go 后台、界面资源和第三方许可文件。

此包使用预览签名，只支持与同一签名的旧预览包直接覆盖更新。若系统提示签名不一致，先保存应用内需要保留的数据；卸载旧应用会清除其私有会话、收件箱和任务状态。不要把这份预览包当作 Play 商店正式签名版本。

## Apple Silicon Mac

下载 `jungo-v0.2.3-preview-macos-arm64.dmg`，打开后把“军哥互联.app”拖到“应用程序”。ZIP 提供相同应用的备用下载形式。应用包含菜单栏界面、Go 后台、图标和许可证资源；首次启动后在“设备”中配对并启用私有组网。

这份预览包使用 ad-hoc 签名，尚未经过 Apple 公证。若 macOS 阻止首次打开，先尝试启动一次，然后在“系统设置 → 隐私与安全性”中对“军哥互联”选择“仍要打开”；只有核对来源与校验值后才这样操作。具体界面以 [Apple 官方说明](https://support.apple.com/102445)为准。更新旧版时保留 `~/Library/Application Support/JunGo`；安装新应用后重启 Mac，使此前已在后台运行的 Go 代理也换成新版。

## Linux 服务器

下载与 `uname -m` 对应的 `jungo-v0.2.3-preview-linux-amd64.tar.gz`（`x86_64`）或 `jungo-v0.2.3-preview-linux-arm64.tar.gz`（`aarch64`）。包内含 `jungo`、两个 systemd 服务模板、安装指南及许可证。以下命令在解压目录执行，示例使用 TCP 9443 与 UDP 3478：

```sh
sudo install -m 755 jungo /usr/local/bin/jungo
id jungo >/dev/null 2>&1 || sudo useradd --system --home /var/lib/jungo-control --shell /usr/sbin/nologin jungo
sudo install -m 644 deploy/jungo-control.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now jungo-control.service
sudo systemctl status jungo-control.service
```

在防火墙开放 TCP 9443 和 UDP 3478，并准备一个设备可访问的服务器地址。首次启动会在 `/var/lib/jungo-control` 私有目录生成 TLS 身份、管理令牌和注册表。保留并备份该目录，不能将其提交到 GitHub。为每台设备单独生成一次性配对信息：

```sh
sudo -u jungo /usr/local/bin/jungo pair-code \
  --state /var/lib/jungo-control --server https://YOUR_SERVER:9443
```

输出含一次性秘密，只在你控制的渠道交给对应设备。若还要把该 Linux 主机加入组网，可安装 `deploy/jungo-device.service` 并用独立的 `/var/lib/jungo-device` 状态目录；完整步骤见 [自托管部署说明](deployment.md)。更新服务前备份状态目录，替换程序后重启相应服务；不要同时运行两个进程写同一状态目录。

## 预览版范围

- Mac 包面向 Apple Silicon，Android APK 面向 arm64；Linux 提供 amd64 和 arm64。
- `comp-lzo: yes` 或 `adaptive` 的 OpenVPN 配置会被明确拒绝；未使用 LZO 的 OpenVPN 仍可用。
- 安装包不会自动创建服务器、开放共享目录或启用本机 SSH。首次配对、文件授权和服务映射由设备持有人配置。
