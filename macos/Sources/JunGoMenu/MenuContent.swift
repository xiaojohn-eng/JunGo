import AppKit
import SwiftUI

struct MenuContent: View {
    @EnvironmentObject var store: AppStore
    @State private var surfaceID = UUID()
    @Environment(\.openWindow) private var openWindow

    private func settings(_ section: SettingsSection) {
        store.section = section
        openWindow(id: "settings")
        NSApp.activate(ignoringOtherApps: true)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 15) {
            HStack(spacing: 11) {
                Image(systemName: "link").font(.system(size: 22, weight: .semibold))
                    .foregroundStyle(.white).frame(width: 43, height: 43)
                    .background(Color.accentColor, in: RoundedRectangle(cornerRadius: 11))
                VStack(alignment: .leading, spacing: 3) {
                    Text("军哥互联").font(.headline)
                    Text("连接自己的设备与文件").font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button { settings(.general) } label: { Image(systemName: "gearshape") }
                    .buttonStyle(.plain).help("设置")
            }

            HStack(alignment: .top, spacing: 10) {
                StatusDot(active: store.connected).padding(.top, 6)
                VStack(alignment: .leading, spacing: 4) {
                    Text(store.statusTitle).font(.headline).foregroundStyle(store.connected ? Color.green : Color.primary)
                    if store.state.paired {
                        Text(store.state.device.name.isEmpty ? "这台 Mac" : store.state.device.name)
                        if !store.state.device.ip.isEmpty {
                            Text("私有地址 \(store.state.device.ip)").font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    } else { Text("配对后访问你的设备与共享文件。").font(.caption).foregroundStyle(.secondary) }
                }
                Spacer()
                if store.connecting { ProgressView().controlSize(.small) }
            }

            if let error = store.visibleError {
                Text(error).font(.caption).foregroundStyle(.orange).lineLimit(3)
            }
            if !store.backendAvailable {
                Button("重新连接") { Task { await store.reconnect() } }.disabled(store.connecting)
            }

            Divider()
            HStack { Text("已配对设备").font(.caption).foregroundStyle(.secondary); Spacer(); Text("\(store.state.peers.count)").font(.caption).foregroundStyle(.secondary) }
            if store.state.peers.isEmpty {
                Text("暂无已配对设备").font(.callout).foregroundStyle(.secondary).padding(.vertical, 4)
            } else {
                ForEach(Array(store.state.peers.prefix(4))) { peer in
                    Button { settings(.devices) } label: {
                        HStack(spacing: 11) {
                            Image(systemName: "desktopcomputer").font(.title3).frame(width: 27)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(peer.name).foregroundStyle(.primary)
                                HStack(spacing: 5) { StatusDot(active: store.backendAvailable && peer.isReachable); Text(store.backendAvailable ? peer.connectionLabel : "状态未更新").font(.caption).foregroundStyle(.secondary) }
                            }
                            Spacer()
                            Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary)
                        }
                    }.buttonStyle(.plain)
                }
                if store.state.peers.count > 4 { Button("查看全部 \(store.state.peers.count) 台设备…") { settings(.devices) }.buttonStyle(.link) }
            }
            Button {
                settings(.devices)
                store.showPairing = true
            } label: { Label(store.state.paired ? "配对新设备" : "配对这台 Mac", systemImage: "plus.circle.fill").frame(maxWidth: .infinity) }
                .controlSize(.large).disabled(!store.backendAvailable)

            if !store.state.transfers.isEmpty {
                Divider()
                Text("传输任务").font(.caption).foregroundStyle(.secondary)
                ForEach(Array(store.state.transfers.prefix(2))) { transfer in
                    TransferRow(transfer: transfer, compact: true)
                }
            }
            Divider()
            Button { settings(.chats) } label: { Label("设备聊天与发文件…", systemImage: "bubble.left.and.bubble.right").frame(maxWidth: .infinity) }.controlSize(.large)
            HStack {
                Button { settings(.shares) } label: { Label("共享目录…", systemImage: "folder").frame(maxWidth: .infinity) }
                Button { settings(.general) } label: { Label("设置…", systemImage: "gearshape").frame(maxWidth: .infinity) }
            }.controlSize(.large)
            Button { NSApp.terminate(nil) } label: { Label("退出菜单栏", systemImage: "power").font(.callout) }
                .buttonStyle(.plain).help("关闭菜单栏界面，后台组网与共享服务继续运行。")
        }.padding(18).frame(width: 350)
        .onAppear { store.setSurfaceVisible(surfaceID, visible: true) }
        .onDisappear { store.setSurfaceVisible(surfaceID, visible: false) }
    }
}

struct TransferRow: View {
    @EnvironmentObject var store: AppStore
    let transfer: Transfer
    var compact = false

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "doc").font(.title3).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 5) {
                Text(transfer.name).lineLimit(1)
                ProgressView(value: transfer.progress).tint(.accentColor)
                HStack {
                    Text(transfer.label)
                    Spacer()
                    Text("\(ByteCountFormatter.string(fromByteCount: transfer.completed, countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: transfer.size, countStyle: .file))")
                }.font(.caption2).foregroundStyle(.secondary)
                if let error = transfer.error, !error.isEmpty {
                    Text(error).font(.caption2).foregroundStyle(.orange).lineLimit(compact ? 2 : 4)
                }
            }
            if transfer.isActive || transfer.canResume {
                Button {
                    Task { await store.perform("transferAction", params: ["id": transfer.id, "action": transfer.canResume ? "resume" : "pause"]) }
                } label: { Image(systemName: transfer.canResume ? "play.circle" : "pause.circle").font(.title3) }
                    .buttonStyle(.plain).disabled(store.busy || !store.backendAvailable)
                    .help(transfer.canResume ? "继续传输" : "暂停传输")
                if !compact {
                    Button {
                        Task { await store.perform("transferAction", params: ["id": transfer.id, "action": "cancel"]) }
                    } label: { Image(systemName: "xmark.circle").font(.title3) }
                        .buttonStyle(.plain).disabled(store.busy || !store.backendAvailable).help("取消传输")
                }
            }
        }
    }
}
