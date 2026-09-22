# 军哥互联 Android

原生 Kotlin / Jetpack Compose，包名 `com.junge.connect`，Android 12+。
编译目标 Android 16 / API 36；Gradle 8.14.3、AGP 8.13.2、Kotlin 2.2.10。

## 构建

先按根项目说明生成真实 Go JNI 库，放到 `app/libs/jungo.aar`。随后在仓库根目录运行：

```sh
JAVA_HOME="$PWD/.tools/jdk" ANDROID_HOME="$PWD/.tools/android-sdk" \
  android/gradlew -p android :app:assembleDebug :app:lintDebug :app:testDebugUnitTest
```

APK：`app/build/outputs/apk/debug/app-debug.apk`。Debug 签名只用于个人设备测试；
发布签名需独立管理，不把 keystore 或密码写入仓库。Gradle wrapper 校验分发包 SHA-256。
`coreApiCheck` 仅用于 native AAR 生成前检查 API 签名，明确禁止打包 APK；不是可运行构建。

## 原生桥接与生命周期

`JunGoApplication` 持有唯一 `NativeRepository` / `mobile.Engine`。
Compose 只观察真实 native state，不合成已连接、速度或传输进度。
VPN 和传输的前台服务维持后台观察；界面不可见且没有服务时停止 UI 轮询。
开合折叠、旋转和 Activity 重建不重建引擎、TUN 或任务。

`JunGoVpnService` 创建唯一双栈 TUN：172.19.0.1/30、fdfe:dcba:9876::1/126、
DNS 172.19.0.2、MTU 1280、IPv4/IPv6 默认路由。
detach 后 fd 的完整所有权归 Go，包括 native 启动失败路径。
平台回调用 `VpnService.protect` 排除底层 socket，用
`ConnectivityManager.getConnectionOwnerUid` 把连接归属交给内核规则处理。
仅本应用排除 TUN 作为 native 控制/DNS socket 的附加防递归措施。
用户选择绕过代理的第三方应用仍进入 TUN，以保留私网访问。

系统文件选择器授予并持久保留内容 URI 权限。Go 通过 `openURI` 获取可寻址 fd，
直接分块读写，不先把 GB 文件复制到缓存。不能持久授权或 seek 的提供方需向用户
显示错误。文件夹递归枚举为逐文件的持久 native 任务；同名上传默认保留两份，
文件夹下载显式选择不冲突的新名字。取消文件选择器不会创建任务。

后台传输使用 `dataSync` 前台服务；系统时限回调暂停仍在运行的任务并提示恢复。
Android 系统强制停止、设备省电和内容提供方限制需要真机验收，不能保证无限后台执行。

## 折叠适配

界面适配折叠屏内外屏与普通手机。
实际布局按窗口宽度和 `WindowInfoTracker` 提供的折叠特征决定，绝不由 PPI 推算 dp。

- 小于 600dp：单栏、底部导航；文件详情可返回设备列表。
- 600–839dp：导航栏；必要时按实际折叠边界分为两栏。
- 至少 840dp：导航栏、设备列表和文件详情双栏。
- 真实分隔折痕/铰链占用独立空隙，横向折叠时用上下分区。
- 页面、设备选择、共享目录和路径使用可保存状态；支持系统深浅色、滚动及键盘 inset。

真机验收矩阵：外屏/内屏及旋转、字体 100%/150%/200%、系统显示缩放、分屏拖动、
DeX 自由窗口、软键盘、传输中开合、锁屏、VPN 撤销、网络切换、后台时限。
截图设计不是这些真机测试的替代品。

## 设备会话与系统分享

「会话」只面向已配对的个人设备。文字有实际送达状态，没有已读回执；
文件卡片复用 native 持久任务，显示实际字节、暂停、取消和失败信息。
接收文件进入应用私有收件箱；用户通过系统文件选择器另存到可见目录。
Android 不开放其他手机目录。组网关闭时明确提示；离线内容由 native 排队。

ACTION_SEND / ACTION_SEND_MULTIPLE 先选择目标设备。单条文字限制与 Go 对齐为 4096 个 Unicode 码点；系统分享最多接收 16384 个码点，超过单条长度时明确提示分条发送，并在每条入队后持久保存余下内容，最后再加入附件。可持久访问的 URI 保存授权；
只有临时授权的文件用 1 MiB 缓冲流式复制到私有 outbox，复制期间维持前台服务，
不足空间或授权失效显示真实错误。复制完成前系统终止进程需要重新分享；
完成后待分享清单落盘，发送后源文件保留至任务完成/取消再清理。
原子入队与 Android 本地待分享清单跨进程不是同一事务，进程在极窄窗口崩溃可能留下
一个已入队项在待分享清单中；恢复时应核对会话，避免再次发送同一份文件。

当前 APK 构建 ABI 为 arm64-v8a。`network-probe` 是仅用于 QA 的独立 UID HTTP 探针，
不属于产品；不要随产品安装包交付。APK 内 JNI 采用未压缩、16 KiB 对齐布局。

网络探针使用独立应用 UID，
因此验证流量确实经过 VpnService，不会被本应用的 TUN 排除项绕过。
