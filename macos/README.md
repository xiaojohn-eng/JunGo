# 军哥互联 Mac 菜单栏客户端

原生 SwiftUI 应用，macOS 13+。提供菜单栏连接状态、设备列表、文件传输任务、
设备配对、共享目录设置，以及已配对设备间的文字与文件聊天。Mac 首版负责私有组网与文件共享，不包含公网代理开关。
界面只展示后台返回的数据，未连接与未配对状态不会填入示例设备或进度。

## 构建

需要 macOS 和 Apple Command Line Tools 的 Swift，无需打开 Xcode。

```sh
swift build --package-path macos
./macos/test.sh
```

还可对已经启动的测试后台执行 `./macos/test.sh --endpoint /path/to/local-api.json`，
验证真实 Swift URLSession→后台 RPC；输出不会包含本机令牌。此命令本身不启动后台。

完整安装包先准备 Darwin 版 `dist/jungo`，然后运行：

```sh
./scripts/build-macos.sh
```

输出 `dist/军哥互联.app.zip`（内含已签名 `.app`）和当前架构的 `.dmg`。脚本只构建和签名，**不会启动应用、
安装登录项或启动后台**。`JUNGO_BINARY` 可指定已构建的后台可执行文件；
`JUNGO_CREATE_DMG=0` 仅生成 `.app.zip`。默认 ad-hoc 签名，只用于本机开发与验证；
使用 `JUNGO_SIGN_IDENTITY` 选择本机已有的开发者签名身份。面向其他电脑发布时还
需要独立完成 Developer ID 签名与 Apple 公证，脚本不会声称公证已经完成。

签名与镜像制作在系统临时目录完成。Documents 等文件提供器目录可能在验证后给松散
`.app` 再次回填不允许签名的 FinderInfo，因此始终交付已验证应用的 `.app.zip` 和 DMG，
不在受管理目录中保留会被改写的松散应用副本。优先从 DMG 拖入“应用程序”文件夹安装。
包内 `Resources/backend-source.sha256` 记录复制后、重新签名前的 Go 后台校验值；
重新签名会改变 Mach-O 文件字节，因此应使用这个校验值对应原始 `dist/jungo`。

## 后台与 RPC

用户启动应用时，首先探测
`~/Library/Application Support/JunGo/local-api.json` 中的已运行后台。可用则复用；
否则启动安装包中的：

```text
Contents/MacOS/jungo agent --state "$HOME/Library/Application Support/JunGo" --local-api
```

开发时可用 `JUNGO_AGENT_PATH` 指定后台可执行文件。退出菜单栏后，后台继续运行；
关闭组网通过设备面板的开关明确操作。界面可见或有活动传输时约每秒查询一次实际状态；窗口隐藏且无活动传输时，状态查询降为约每 5 秒、聊天查询约每 10 秒，重新打开界面立即刷新。连接失败会显示
错误及“重新连接”。不会静默杀掉其他进程或自动反复启动后台。

端点文件为 `{url:"http://127.0.0.1:<port>",token:"..."}`，也支持 IPv6 `::1`。
客户端只接受数字回环地址，拒绝外部地址、URL 用户信息、查询和片段，并禁止跟随
HTTP 重定向。调用方式：

```json
{"method":"state","params":{}}
```

使用 `POST /v1/rpc` 与 `Authorization: Bearer <local-token>`。`state`/变更响应直接
返回 RootState，也兼容 `{state: RootState}`。状态至少包含 `paired`；其他字段为
`device {name,ip}`、`peers [{id,name,ip,path}]`、`meshEnabled`、`proxyEnabled`、
`meshRunning`、`vpnRunning`、`shares [{id,name,path,readOnly}]`、`transfers [{id,name,status,completed,size}]`
及可选 `error`。未知设备路径状态显示为不可达。
Mac 连接指示使用 `meshRunning`；不把 Android 专用的 `vpnRunning` 当成 Mac 连接条件。

实现的变更方法：

- `pair {server,fingerprint,serviceId?,code,name}`：HTTPS 地址和 64 位 SHA-256 指纹。
- `network {mesh,proxy:false}`：Mac 仅显示组网开关。
- `shareAdd {name,path,readOnly}`、`shareRemove {id}`。
- `serviceAdd {port,network:"tcp",target:"127.0.0.1:PORT"}`、`serviceRemove {port}`。
- `transferAction {id,action:pause|resume|cancel}`：只由明确的用户操作触发。
- `createPairing {adminToken}`、`revokeDevice {deviceId,adminToken}`。
- `chatList {version?}`：首次返回 `{version,messages:[...]}`，版本一致时返回 `{version,unchanged:true}`；旧后台返回完整列表时仍兼容。后台重启后的新版本会触发完整刷新。
- `chatSendText {deviceId,text}`、`chatSendFile {deviceId,source,name}`。
- `chatSaveFile {id,destination}`：返回实际保存任务 `Transfer`，包含 `id`、`destination`。

`createPairing` 返回 `{server,fingerprint,serviceId,code,expiresAt,payload}`，
`expiresAt` 为 RFC3339 时间。二维码使用 CoreImage 对后台真实 `payload` 编码，
不会生成示意码；过期后禁用复制并标记失效。新设备可粘贴 payload JSON 自动填入。

## 用户授权与凭据

默认没有共享目录。用户通过 `NSOpenPanel` 明确选目录，再确认只读或允许上传；
移除共享不删除原始文件。系统隐私权限仍由 macOS 控制，权限问题由后台如实报告。

管理凭据仅通过 Security.framework 保存到系统 Keychain，不写 UserDefaults、
配置文件、日志或应用包。只在生成配对码或撤销设备时取出并发给本机管理 RPC。
后台不得把该管理凭据写入持久配置，也不得转发到设备文件服务。

“设备”中的“本机服务”只暴露用户明确添加的 TCP 端口，目标限定为本机回环地址；
8443 保留给文件服务。它不开放整台 Mac 的所有端口，也不接管家庭局域网路由。
添加 SSH/Web 映射不会自动启动 SSH/Web 服务。移除映射不停止本机原始服务。

“聊天”按设备分会话，支持最多 4096 个 Unicode 码点的文字、通过文件选择器或 Finder
拖入一个或多个文件，以及文件卡片的实际状态、进度、暂停与取消。文件夹请先压缩后
通过聊天发送。离线设备的消息保持待发送状态，不会提前显示已送达。
收到的文件只有后台确认完整接收后才能“另存为”。用户通过 `NSSavePanel` 明确选择
本机位置，已有非空目标由后台拒绝覆盖。聊天数据中的相对收件箱路径不会转为本机
文件 URL；“在访达中显示”只对用户选过位置、对应保存任务完成的文件开放。

登录后启动使用 `SMAppService.mainApp`，仅在用户操作开关时注册或注销。界面读取
真实系统状态；需要系统批准时显示提示和系统设置入口，不假装已经开启。

## 验证边界

测试脚本直接编译实际模型/RPC 源码，使用仅依赖 Foundation 的检查程序，因此不需要
完整 Xcode 提供的 XCTest 或额外测试运行库。自动检查覆盖回环端点校验、状态解析、未知连接状态、暂停/取消显示语义，以及真实
配对时间格式、聊天卡片及保存后的 Reveal 路径约束。构建不等于端到端组网、文件传输或系统登录项验收；安装后的这些场景
需与真实后台和设备共同验证。当前仅提供 Mac 配套端，不生成 iPhone/iPad 应用。


## 性能实现与复现

- 大响应的 JSON 解码、版本缓存与消息索引构建由 `SnapshotDecoder` actor 处理，避免在 MainActor 中解析完整历史。
- 状态响应直接进行一次 Decodable 解析，继续兼容直接状态与 `state` 包装；内容无变化时不发布新的界面状态。
- 会话、按设备消息及任务 ID 均建立索引；输入、窗口重绘和任务进度变化不再重复扫描整份历史。日期解析器复用并受锁保护，会话排序比较器不再创建格式器。
- 多文件发送逐项入队，批次结束后统一刷新；后项失败时已接受项目仍保留并刷新。文件检查在非 MainActor 方法中进行。
- 聊天与设备传输列表使用惰性布局；可见聊天/活动传输仍维持原有约 1 秒刷新间隔，打开窗口立即刷新。
- `test.sh` 除原 6 项检查外，新增 4 项检查覆盖首次空状态、等价状态抑制、版本/后台重启/旧接口兼容、历史索引以及可见性轮询策略。

可在机器没有并行编译或重负载时运行合成基准：

```sh
bash macos/performance.sh
```

基准不连接用户后台，不读取消息或凭据，不启动安装好的应用。它使用 20,000 条生成消息、100 台生成设备、5,000 个生成任务，Release `-O` 编译后各测 5 次中位数；同一程序保留旧状态解码、扫描与格式器逻辑作对照。索引创建成本与重复查询成本分别记录；增量版本减少未变化响应的成本，不能消除有新消息时全量快照的解码成本。数字是相同合成负载的局部耗时，不能解释为整机帧率、常驻内存或电量的同倍改善。
