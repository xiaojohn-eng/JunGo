import AppKit
import ServiceManagement
import SwiftUI

struct SettingsRoot: View {
    @EnvironmentObject var store: AppStore
    @State private var surfaceID = UUID()

    var body: some View {
        NavigationSplitView {
            List(selection: Binding<SettingsSection?>(get: { store.section }, set: { store.section = $0 ?? .devices })) {
                ForEach(SettingsSection.allCases) { section in
                    Label(section.rawValue, systemImage: section.symbol).tag(section)
                }
            }
            .listStyle(.sidebar).navigationSplitViewColumnWidth(min: 165, ideal: 185, max: 220)
        } detail: {
            VStack(alignment: .leading, spacing: 0) {
                if let error = store.visibleError {
                    HStack(alignment: .top) {
                        ErrorBanner(message: error)
                        if store.operationError != nil {
                            Button { store.operationError = nil } label: { Image(systemName: "xmark") }.buttonStyle(.plain).help("关闭提示")
                        }
                    }.padding([.horizontal, .top], 20)
                }
                switch store.section {
                case .devices: DevicesSettings()
                case .chats: ChatsSettings()
                case .shares: SharesSettings()
                case .general: GeneralSettings()
                }
            }.frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .onAppear { store.setSurfaceVisible(surfaceID, visible: true) }
        .onDisappear { store.setSurfaceVisible(surfaceID, visible: false) }
        .frame(minWidth: 730, minHeight: 490)
        .sheet(isPresented: $store.showPairing) { PairingSheet().environmentObject(store) }
    }
}

struct DevicesSettings: View {
    @EnvironmentObject var store: AppStore
    @State private var revoking: Peer?
    @State private var editingName = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("设备").font(.largeTitle.weight(.semibold))
                    Text("在自己的设备之间，安全连接与传输。").foregroundStyle(.secondary)
                }
                GroupBox {
                    HStack(alignment: .top, spacing: 15) {
                        Image(systemName: "laptopcomputer").font(.system(size: 35)).foregroundStyle(.secondary)
                        VStack(alignment: .leading, spacing: 7) {
                            Text(store.state.device.name.isEmpty ? "这台 Mac" : store.state.device.name).font(.headline)
                            HStack(spacing: 6) { StatusDot(active: store.connected); Text(store.statusTitle).font(.callout) }
                            if !store.state.device.ip.isEmpty { Text(store.state.device.ip).font(.system(.callout, design: .monospaced)).foregroundStyle(.secondary).textSelection(.enabled) }
                        }
                        Spacer()
                        if store.state.paired {
                            Button("修改名称…") { editingName = true }
                                .disabled(store.busy || !store.backendAvailable)
                            Toggle("私有组网", isOn: Binding(get: { store.state.meshEnabled }, set: { value in Task { await store.setMesh(value) } }))
                                .toggleStyle(.switch).fixedSize().disabled(store.busy || !store.backendAvailable)
                        } else {
                            Button("配对这台 Mac…") { store.showPairing = true }.buttonStyle(.borderedProminent).disabled(!store.backendAvailable)
                        }
                    }.padding(8)
                }
                if !store.backendAvailable {
                    Button("重新连接后台") { Task { await store.reconnect() } }.disabled(store.connecting)
                }
                HStack {
                    Text("已配对设备").font(.headline)
                    Spacer()
                    if store.state.paired { Button("配对新设备…") { store.showPairing = true }.disabled(store.busy || !store.backendAvailable) }
                }
                if store.state.peers.isEmpty {
                    GroupBox { EmptyState(symbol: "laptopcomputer.and.iphone", title: "还没有其他设备", description: "完成配对后，设备会出现在这里。连接状态来自本机后台。") }
                } else {
                    GroupBox {
                        VStack(spacing: 0) {
                            ForEach(Array(store.state.peers.enumerated()), id: \.element.id) { index, peer in
                                HStack(spacing: 13) {
                                    Image(systemName: "desktopcomputer").font(.title2).frame(width: 34)
                                    VStack(alignment: .leading, spacing: 5) {
                                        Text(peer.name).font(.headline)
                                        Text(peer.ip.isEmpty ? "暂无私有地址" : peer.ip).font(.system(.caption, design: .monospaced)).foregroundStyle(.secondary).textSelection(.enabled)
                                    }
                                    Spacer()
                                    HStack(spacing: 5) { StatusDot(active: store.backendAvailable && peer.isReachable); Text(store.backendAvailable ? peer.connectionLabel : "状态未更新").font(.callout).foregroundStyle(.secondary) }
                                    Menu {
                                        Button("复制私有地址") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(peer.ip, forType: .string) }.disabled(peer.ip.isEmpty)
                                        Button("撤销设备…", role: .destructive) { revoking = peer }
                                    } label: { Image(systemName: "ellipsis") }.menuStyle(.borderlessButton).frame(width: 22)
                                }.padding(12)
                                if index < store.state.peers.count - 1 { Divider() }
                            }
                        }
                    }
                }
                HostServicesSettings()
                if !store.state.transfers.isEmpty {
                    Text("文件传输").font(.headline)
                    GroupBox { LazyVStack(spacing: 17) { ForEach(store.state.transfers) { TransferRow(transfer: $0) } }.padding(8) }
                }
            }.padding(26)
        }
        .alert("撤销这台设备？", isPresented: Binding(get: { revoking != nil }, set: { if !$0 { revoking = nil } })) {
            Button("取消", role: .cancel) { revoking = nil }
            Button("撤销设备", role: .destructive) {
                if let peer = revoking { Task { await store.revoke(peer) } }
                revoking = nil
            }
        } message: { Text("\(revoking?.name ?? "此设备") 将失去组网和共享文件的访问权限。重新接入需要新的配对码。") }
        .sheet(isPresented: $editingName) { RenameDeviceSheet().environmentObject(store) }
    }
}

private struct RenameDeviceSheet: View {
    @EnvironmentObject var store: AppStore
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""

    private var proposedName: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("修改这台 Mac 的名称").font(.title2.weight(.semibold))
            Text("新名称会显示在其他已配对设备上。").font(.callout).foregroundStyle(.secondary)
            TextField("设备名称", text: $name)
            if let error = store.operationError { ErrorBanner(message: error) }
            HStack {
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("保存名称") {
                    Task { if await store.renameDevice(proposedName) { dismiss() } }
                }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(store.busy || !store.backendAvailable || proposedName.isEmpty || proposedName.lengthOfBytes(using: .utf8) > 128 || proposedName == store.state.device.name)
            }
        }
        .padding(25).frame(width: 430)
        .onAppear { name = store.state.device.name; store.operationError = nil }
    }
}

private struct FolderChoice: Identifiable { let url: URL; var id: String { url.path } }

struct SharesSettings: View {
    @EnvironmentObject var store: AppStore
    @State private var folderChoice: FolderChoice?
    @State private var removing: SharedDirectory?

    private func chooseDirectory() {
        let panel = NSOpenPanel()
        panel.title = "选择共享目录"
        panel.prompt = "选择此目录"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = true
        panel.resolvesAliases = true
        if panel.runModal() == .OK, let url = panel.url { folderChoice = FolderChoice(url: url) }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("共享目录").font(.largeTitle.weight(.semibold))
                    Text("只有在这里添加的文件夹才可被已配对设备访问。").foregroundStyle(.secondary)
                }
                if store.state.shares.isEmpty {
                    GroupBox { EmptyState(symbol: "folder.badge.plus", title: "尚未共享任何目录", description: "从这台 Mac 上选择一个文件夹，并设置访问权限。") }
                } else {
                    GroupBox {
                        VStack(spacing: 0) {
                            ForEach(Array(store.state.shares.enumerated()), id: \.element.id) { index, share in
                                HStack(spacing: 13) {
                                    Image(systemName: "folder.fill").font(.system(size: 34)).foregroundStyle(.cyan)
                                    VStack(alignment: .leading, spacing: 5) {
                                        Text(share.name).font(.headline)
                                        Text(share.path).font(.callout).foregroundStyle(.secondary).lineLimit(2).textSelection(.enabled)
                                    }
                                    Spacer()
                                    Text(share.readOnly ? "只读" : "可上传").font(.caption)
                                        .foregroundStyle(share.readOnly ? Color.secondary : Color.accentColor)
                                        .padding(.horizontal, 10).padding(.vertical, 5)
                                        .background(share.readOnly ? Color.secondary.opacity(0.10) : Color.accentColor.opacity(0.10), in: Capsule())
                                    Menu {
                                        Button("在 Finder 中显示") { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: share.path)]) }
                                        Button("移除共享…", role: .destructive) { removing = share }
                                    } label: { Image(systemName: "ellipsis") }.menuStyle(.borderlessButton).frame(width: 22)
                                }.padding(12)
                                if index < store.state.shares.count - 1 { Divider() }
                            }
                        }
                    }
                }
                HStack {
                    Text("已配对设备可按你授予的权限浏览与传输。").font(.callout).foregroundStyle(.secondary)
                    Spacer()
                    Button("添加文件夹…", action: chooseDirectory).buttonStyle(.borderedProminent).controlSize(.large)
                        .disabled(store.busy || !store.backendAvailable)
                }
            }.padding(26)
        }
        .sheet(item: $folderChoice) { choice in AddShareSheet(directory: choice.url).environmentObject(store) }
        .alert("移除共享目录？", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } })) {
            Button("取消", role: .cancel) { removing = nil }
            Button("移除共享", role: .destructive) {
                if let share = removing { Task { await store.perform("shareRemove", params: ["id": share.id]) } }
                removing = nil
            }
        } message: { Text("其他设备将无法再访问“\(removing?.name ?? "此目录")”。Mac 上的原始文件会保留。") }
    }
}

struct AddShareSheet: View {
    @EnvironmentObject var store: AppStore
    @Environment(\.dismiss) private var dismiss
    let directory: URL
    @State private var name = ""
    @State private var readOnly = true

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("添加共享目录").font(.title2.weight(.semibold))
            Text(directory.path).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
            TextField("显示名称", text: $name)
            Picker("访问权限", selection: $readOnly) {
                Text("只读：允许浏览与下载").tag(true)
                Text("可上传：允许浏览、下载与上传").tag(false)
            }.pickerStyle(.radioGroup)
            if let error = store.operationError { ErrorBanner(message: error) }
            HStack {
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("添加共享") {
                    Task {
                        if await store.perform("shareAdd", params: ["name": name.trimmingCharacters(in: .whitespacesAndNewlines), "path": directory.path, "readOnly": readOnly]) { dismiss() }
                    }
                }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(store.busy || name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }.padding(25).frame(width: 480).onAppear { name = directory.lastPathComponent; store.operationError = nil }
    }
}

struct GeneralSettings: View {
    @EnvironmentObject var store: AppStore
    @State private var administratorToken = ""

    var body: some View {
        Form {
            Section {
                Toggle("登录后启动军哥互联", isOn: Binding(get: { store.loginStatus == .enabled }, set: { value in Task { await store.setLoginEnabled(value) } }))
                if store.loginStatus == .requiresApproval {
                    Text("登录项需要在系统设置中批准，当前尚未启用。").foregroundStyle(.orange)
                    Button("打开系统登录项设置") { SMAppService.openSystemSettingsLoginItems() }
                } else if store.loginStatus == .notFound {
                    Text("系统未识别此应用的登录项。请先将完整应用放入“应用程序”文件夹，再开启此选项。").foregroundStyle(.secondary)
                }
            } header: { Text("启动") }
            Section {
                Text("管理凭据用于生成新配对码与撤销设备，保存在这台 Mac 的系统钥匙串中。").font(.callout).foregroundStyle(.secondary)
                SecureField("控制服务管理凭据（可选）", text: $administratorToken)
                HStack {
                    Button("保存到钥匙串") { if store.saveAdministratorToken(administratorToken) { administratorToken = "" } }.disabled(administratorToken.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    Button("移除已保存凭据", role: .destructive) { store.removeAdministratorToken() }
                }
                if let notice = store.administratorNotice { Text(notice).font(.caption).foregroundStyle(.secondary) }
            } header: { Text("设备管理") }
            Section {
                LabeledContent("后台状态", value: store.backendAvailable ? "可用" : "未连接")
                LabeledContent("版本", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "开发版本")
                Button("重新连接后台") { Task { await store.reconnect() } }.disabled(store.connecting)
                Text("退出菜单栏界面后，后台组网与文件共享服务继续运行。可在“设备”中关闭私有组网。").font(.callout).foregroundStyle(.secondary)
            } header: { Text("关于军哥互联") }
        }.formStyle(.grouped).onAppear { store.refreshLoginStatus() }
    }
}
