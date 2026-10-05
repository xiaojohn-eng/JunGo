# 验证与已知限制

发布源码来自 0.2.3-preview。原始实机日志、截图、设备状态和部署记录属于私有运行信息，不随仓库公开。

## 可复现检查

在仓库根目录安装 README 所列工具链后执行：

```sh
.tools/go/bin/go test -race -tags=cmfa,with_gvisor ./cmd/... ./internal/... ./mobile/...
sh macos/test.sh
bash macos/performance.sh
JAVA_HOME="$PWD/.tools/jdk" ANDROID_HOME="$PWD/.tools/android-sdk" \
  android/gradlew -p android :app:testDebugUnitTest :app:lintDebug
```

Android 检查需先生成 JNI AAR；Mac 检查需 macOS/Swift。仪器测试需自行连接测试设备。涉及配对或网络状态变更的设备测试，应仅在自有测试环境中执行；一次性测试输入不应提交 Git。

本版开发验收包括 Go race 检查、Android 38 项 JVM 测试、Mac 10 项模型/性能检查，以及折叠尺寸布局与文件收发检查。这些记录不替代在自己的硬件和网络上的验证。Release 的 AndroidJUnitRunner 与裁剪后的共享依赖存在兼容问题；精简 Release 的关键链路采用独立 UID 探针与实际 UI 检查，不能将 Debug 仪器检查算作 Release 仪器检查。

## 已知边界

- 本轮未完成手机 5 GB 持续传输、长时间锁屏耗电和移动网络吞吐专项验收；桌面大文件测试不能代替手机验收。
- Android 受系统后台限制；强制停止后需要重新打开应用。Mac/Linux 只开放明确共享的目录和服务映射。
- 聊天增量日志每次确认前 fsync，周期压缩仍需写完整历史。传输任务元数据仍用完整原子快照。
- 聊天 v2 快照依赖同目录 journal。降级前必须用新版成功正常关闭并生成完整 v1 快照；不能删除 journal 或清空数据来强行降级。
- Android 预览 APK 使用预览签名，Mac 包使用 ad-hoc 签名且未经过 Apple 公证；此 Release 面向自行管理服务器和设备的预览使用。生产发布签名、Apple 公证和生产运维需要独立完成。
- OpenVPN LZO 压缩配置 `yes/adaptive` 不受支持，会在加载时明确报错；未启用 LZO 的 OpenVPN 不受影响。
