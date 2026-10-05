# 自托管部署与设备接入

以下示例使用占位符，需替换为你自己的服务器地址。仓库不附带已部署服务器、账号、订阅或任何凭据。

## 控制与中继

将与你 Linux 架构相符的 `dist/jungo-linux-amd64` 或 `dist/jungo-linux-arm64` 安装为 `/usr/local/bin/jungo`。创建专用低权限用户 `jungo`，创建该用户可写的 `/var/lib/jungo-control`。将 `deploy/jungo-control.service` 放入 systemd 后启用。

等效前台命令：

```sh
/usr/local/bin/jungo serve --state /var/lib/jungo-control --listen :9443 --stun :3478
```

防火墙开放 TCP 9443（TLS 控制、事件、加密中继）和 UDP 3478（STUN）。直连需要设备之间允许 UDP；不允许时通过 TCP 9443 中继。STUN 与控制服务目前使用同一主机名、固定 UDP 3478。

首次启动在私有状态目录生成 `tls/identity.crt`、`tls/identity.key`、`registry.json` 和 `admin.token`。备份这些文件时保留权限；不要把它们放入共享目录或 Git。控制证书的 SHA-256 指纹由可信渠道传给设备，不依赖公开 CA。默认自签证书有效期五年；更换证书后需要更新可信配对信息。

不要让 TLS 反向代理替换外层证书而仍使用旧指纹。v0.1 最简单的部署是直接访问内置 TLS 端口。中继只接收已注册设备的 WireGuard 加密帧；同一网络的设备可相互访问。

生成新的、一次性配对信息：

```sh
sudo -u jungo /usr/local/bin/jungo pair-code \
  --state /var/lib/jungo-control --server https://YOUR_SERVER:9443
```

输出包含 server、fingerprint、serviceId、code、expiresAt。每台设备使用一个新码；不要把管理 token 当配对码分享。安卓可粘贴完整配对 JSON 或填写字段；Mac 也能在设置中输入。Mac 配对管理页可在用户提供控制服务管理 token 后生成二维码和撤销设备，管理 token 存入本机 Keychain。

控制服务目前是单实例文件存储。不要启动两个进程同时写同一状态目录，也不要横向复制多个活动写实例。

## Linux 文件与本机服务

Linux 设备后台使用独立状态目录 `/var/lib/jungo-device`，不能与控制状态混用。共享目录应在该目录外，例如 `/srv/jungo-share`。

```sh
/usr/local/bin/jungo agent --state /var/lib/jungo-device --local-api
```

本地管理接口仅监听 `127.0.0.1` 的随机端口，随机 bearer token 保存在权限 0600 的 `local-api.json`。CLI 会自动读取，不必输出或手工复制 token。以该设备进程用户运行下列命令：

```sh
jungo rpc --state /var/lib/jungo-device <<'JSON'
{"method":"pair","params":{"server":"https://YOUR_SERVER:9443","fingerprint":"CERTIFICATE_SHA256","serviceId":"SERVICE_ID","code":"ONE_TIME_CODE","name":"Linux 服务器"}}
JSON
jungo rpc --state /var/lib/jungo-device <<'JSON'
{"method":"shareAdd","params":{"name":"共享文件","path":"/srv/jungo-share","readOnly":false}}
JSON
jungo rpc --state /var/lib/jungo-device <<'JSON'
{"method":"serviceAdd","params":{"port":22,"target":"127.0.0.1:22","network":"tcp"}}
JSON
jungo rpc --state /var/lib/jungo-device <<'JSON'
{"method":"network","params":{"mesh":true}}
JSON
jungo rpc --state /var/lib/jungo-device <<'JSON'
{"method":"state"}
JSON
```

`serviceAdd` 的源端口是设备虚拟私网端口，目标仅允许明确的 loopback 地址；端口 8443 保留给文件服务。普通用户无需打开宿主机 TUN 或管理员权限。SSH 服务仍需你自己开启并使用 SSH 本身的认证。用户主动共享的目录仅由该设备进程操作系统权限所允许。

停止组网：`{"method":"network","params":{"mesh":false}}`。按 ID 删除共享：`{"method":"shareRemove","params":{"id":"SHARE_ID"}}`。撤销设备通过控制服务管理接口或 Mac 配对管理页执行；已注册设备的普通 token 没有管理权限。

已配对的 Mac 可在“设备”中修改自身显示名称；本地 RPC 也可调用 `{"method":"renameDevice","params":{"name":"工作 Mac"}}`。控制服务必须支持 `POST /v1/device/name`；先更新控制服务，再更新 Mac 客户端。重命名保留设备身份、私网 IP 与稳定主机名，其他设备同步列表后显示新名称。

Linux systemd 模板见 `deploy/jungo-device.service`。该模板未强制 ReadOnlyPaths / ProtectHome，以便访问你明确选择的目录；可以按实际共享路径进一步限制。默认没有开机自动启动手机 VPN，也不会替用户修改 Mac 登录项。

## 更新与恢复

停止服务后替换二进制，保留私有状态目录。传输任务记录和已确认上传块保留于本地磁盘；恢复连接后队列继续，主动暂停/取消任务保持原状态。不要并行运行两个进程写同一设备状态。

控制连接长时间失效时客户端会移除旧授权快照，私网访问失败关闭；重新连上控制服务后同步有效设备。这里没有云端账号找回流程；丢失设备本地私钥后需要撤销旧设备并重新配对。

v0.1 不提供界面内重置身份入口。已配对状态不能直接输入新码覆盖；撤销后如需重新加入，先保存收到的文件并备份本地数据，再创建新身份：Linux 改用新的 `--state` 目录；Mac 完全退出菜单栏程序及其后台后，将 `~/Library/Application Support/JunGo` 移至备份位置再启动；Android 在系统设置中清除本应用存储后重新打开。Android 清除存储会删除本地会话、收件箱、配置和任务断点；系统目录内已另存的文件不受影响。新身份不会自动继承旧身份的会话和传输任务。
