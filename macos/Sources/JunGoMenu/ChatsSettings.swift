import AppKit
import SwiftUI
import UniformTypeIdentifiers

struct ChatsSettings: View {
    @EnvironmentObject var store: AppStore
    @State private var selectedID: String?
    @State private var drafts: [String: String] = [:]
    @State private var dropTarget = false

    private var conversations: [ChatConversation] { store.conversations }
    private var selected: ChatConversation? { conversations.first { $0.id == selectedID } }
    private var conversationMessages: [ChatMessage] { store.chatIndex.byDevice[selectedID ?? ""] ?? [] }
    private var draft: Binding<String> {
        Binding(get: { drafts[selectedID ?? ""] ?? "" }, set: { drafts[selectedID ?? ""] = $0 })
    }
    private var canSend: Bool { selected?.peer != nil && store.backendAvailable && store.state.paired && !store.busy }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("聊天").font(.largeTitle.weight(.semibold))
                    Text("给自己的设备发送文字和文件。").foregroundStyle(.secondary)
                }
                Spacer()
            }.padding(24)
            if let error = store.chatError { ErrorBanner(message: "无法更新聊天：\(error)").padding([.horizontal, .bottom], 24) }
            Divider()
            if conversations.isEmpty {
                EmptyState(symbol: "bubble.left.and.bubble.right", title: "先配对另一台设备", description: "配对后可以在设备之间发送消息、拖入文件，传输任务会保留进度。")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                HStack(spacing: 0) {
                    conversationList.frame(width: 180)
                    Divider()
                    if let selected {
                        conversationBody(selected)
                    } else {
                        EmptyState(symbol: "bubble.left", title: "选择一台设备", description: "所有会话都在已配对的个人设备之间。")
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                }
            }
        }
        .task { if selectedID == nil { selectedID = conversations.first?.id }; await store.refreshChats() }
        .onChange(of: store.state.peers.map(\.id)) { _ in if selectedID == nil { selectedID = conversations.first?.id } }
    }

    private var conversationList: some View {
        ScrollView {
            LazyVStack(spacing: 4) {
                ForEach(conversations) { conversation in
                    Button { selectedID = conversation.id } label: {
                        VStack(alignment: .leading, spacing: 7) {
                            HStack(spacing: 6) {
                                Image(systemName: "desktopcomputer").foregroundStyle(Color.accentColor)
                                Text(conversation.name).font(.headline).lineLimit(1)
                            }
                            Text(conversation.preview.replacingOccurrences(of: "\n", with: " ")).font(.caption).foregroundStyle(.secondary).lineLimit(2)
                            if let peer = conversation.peer {
                                HStack(spacing: 4) { StatusDot(active: store.backendAvailable && peer.isReachable); Text(store.backendAvailable ? peer.connectionLabel : "状态未更新").font(.caption2).foregroundStyle(.secondary) }
                            }
                        }.padding(11).frame(maxWidth: .infinity, alignment: .leading)
                            .background(selectedID == conversation.id ? Color.accentColor.opacity(0.12) : Color.clear, in: RoundedRectangle(cornerRadius: 9))
                    }.buttonStyle(.plain)
                }
            }.padding(8)
        }.background(Color(nsColor: .controlBackgroundColor))
    }

    private func conversationBody(_ conversation: ChatConversation) -> some View {
        VStack(spacing: 0) {
            HStack {
                Text(conversation.name).font(.headline)
                Spacer()
                Text(conversation.peer.map { store.backendAvailable ? $0.connectionLabel : "状态未更新" } ?? "仅查看历史")
                    .font(.caption).foregroundStyle(.secondary)
            }.padding(15)
            Divider()
            ScrollViewReader { reader in
                ScrollView {
                    LazyVStack(spacing: 17) {
                        if conversationMessages.isEmpty {
                            Text("消息和文件只发送给这台设备。").font(.callout).foregroundStyle(.secondary).padding(.top, 28)
                        }
                        ForEach(conversationMessages) { message in ChatMessageBubble(message: message).id(message.id) }
                        Color.clear.frame(height: 1).id("conversation-bottom")
                    }.padding(17)
                }
                .onAppear { reader.scrollTo("conversation-bottom", anchor: .bottom) }
                .onChange(of: selectedID) { _ in reader.scrollTo("conversation-bottom", anchor: .bottom) }
                .onChange(of: conversationMessages.last?.id) { _ in reader.scrollTo("conversation-bottom", anchor: .bottom) }
            }
            Divider()
            composer(conversation)
        }
        .background(Color(nsColor: .textBackgroundColor))
        .overlay {
            if dropTarget {
                RoundedRectangle(cornerRadius: 10).fill(Color.accentColor.opacity(0.12)).overlay {
                    Label("松开发送文件", systemImage: "arrow.down.doc").font(.title2.weight(.semibold)).padding(20).background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
                }.padding(6).allowsHitTesting(false)
            }
        }
        .onDrop(of: [UTType.fileURL.identifier], isTargeted: $dropTarget) { providers in
            guard canSend else { return false }
            let recipient = conversation.id
            Task {
                var urls: [URL] = []
                for provider in providers {
                    if let url = await droppedURL(provider), url.isFileURL { urls.append(url) }
                }
                if urls.isEmpty { store.operationError = "未能读取拖入的本机文件，请使用文件按钮重新选择。" }
                else { await store.sendFiles(to: recipient, urls: urls) }
            }
            return true
        }
    }

    private func composer(_ conversation: ChatConversation) -> some View {
        VStack(spacing: 8) {
            TextEditor(text: draft).font(.body).frame(minHeight: 62, maxHeight: 92)
                .disabled(!canSend).accessibilityLabel("发送到\(conversation.name)的消息")
            HStack {
                Button { chooseFiles(to: conversation.id) } label: { Label("文件", systemImage: "paperclip") }.disabled(!canSend)
                Text("可拖入多个文件").font(.caption).foregroundStyle(.secondary)
                Spacer()
                if draft.wrappedValue.unicodeScalars.count > 4096 { Text("最多 4096 字符").font(.caption).foregroundStyle(.red) }
                Button("发送") {
                    let recipient = conversation.id, content = draft.wrappedValue
                    Task { if await store.sendText(to: recipient, text: content), drafts[recipient] == content { drafts[recipient] = "" } }
                }
                .buttonStyle(.borderedProminent).keyboardShortcut(.return, modifiers: .command)
                .disabled(!canSend || draft.wrappedValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || draft.wrappedValue.unicodeScalars.count > 4096)
            }
        }.padding(12)
    }

    private func chooseFiles(to deviceID: String) {
        let panel = NSOpenPanel()
        panel.title = "发送文件到这台设备"; panel.prompt = "发送"
        panel.canChooseFiles = true; panel.canChooseDirectories = false; panel.allowsMultipleSelection = true
        panel.begin { response in
            if response == .OK { Task { @MainActor in await store.sendFiles(to: deviceID, urls: panel.urls) } }
        }
    }

    private func droppedURL(_ provider: NSItemProvider) async -> URL? {
        await withCheckedContinuation { continuation in
            provider.loadDataRepresentation(forTypeIdentifier: UTType.fileURL.identifier) { data, _ in
                continuation.resume(returning: data.flatMap { URL(dataRepresentation: $0, relativeTo: nil) })
            }
        }
    }
}

private struct ChatMessageBubble: View {
    @EnvironmentObject var store: AppStore
    let message: ChatMessage

    private var transfer: Transfer? { store.transfersByID[message.transferId] }
    private var savedTransfer: Transfer? { store.chatSaves[message.id].flatMap { saved in store.transfersByID[saved.transferID] } }

    var body: some View {
        HStack(alignment: .top) {
            if message.isOutgoing { Spacer(minLength: 30) }
            VStack(alignment: message.isOutgoing ? .trailing : .leading, spacing: 5) {
                Group {
                    if message.isFile { fileCard }
                    else { Text(message.text).textSelection(.enabled).padding(12) }
                }
                .background(message.isOutgoing ? Color.accentColor.opacity(0.14) : Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 12))
                HStack(spacing: 6) {
                    if let date = message.date { Text(date, style: .time) }
                    Text(message.statusLabel)
                }.font(.caption2).foregroundStyle(.secondary)
                if !message.error.isEmpty { Text(message.error).font(.caption).foregroundStyle(.orange).textSelection(.enabled) }
            }.frame(maxWidth: 370, alignment: message.isOutgoing ? .trailing : .leading)
            if !message.isOutgoing { Spacer(minLength: 30) }
        }
    }

    private var fileCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "doc.fill").font(.system(size: 27)).foregroundStyle(Color.accentColor)
                VStack(alignment: .leading, spacing: 4) {
                    Text(message.name).font(.headline).lineLimit(3).textSelection(.enabled)
                    Text(ByteCountFormatter.string(fromByteCount: message.size, countStyle: .file)).font(.caption).foregroundStyle(.secondary)
                }
            }
            if !["complete", "cancelled"].contains(message.status) {
                ProgressView(value: message.progress)
                Text("\(ByteCountFormatter.string(fromByteCount: message.completed, countStyle: .file)) / \(ByteCountFormatter.string(fromByteCount: message.size, countStyle: .file))")
                    .font(.caption2).foregroundStyle(.secondary)
            }
            if message.isOutgoing, let transfer, transfer.isActive || transfer.canResume {
                HStack {
                    Button(transfer.canResume ? "继续" : "暂停") { action(transfer.canResume ? "resume" : "pause", transferID: transfer.id) }
                    Button("取消", role: .destructive) { action("cancel", transferID: transfer.id) }
                }.disabled(store.busy || !store.backendAvailable)
            }
            if message.canSave {
                if let save = store.chatSaves[message.id], let url = save.completedURL(transfer: store.transfersByID[save.transferID]) {
                    Button("在访达中显示") { NSWorkspace.shared.activateFileViewerSelecting([url]) }
                } else if let savedTransfer, savedTransfer.isActive || savedTransfer.canResume {
                    Text("保存到所选位置").font(.caption).foregroundStyle(.secondary)
                    TransferRow(transfer: savedTransfer)
                } else {
                    Button("另存为…") { chooseDestination() }.disabled(store.busy || !store.backendAvailable)
                }
            }
        }.padding(13).frame(minWidth: 210, maxWidth: 340, alignment: .leading)
    }

    private func action(_ action: String, transferID: String) {
        Task { await store.perform("transferAction", params: ["id": transferID, "action": action]) }
    }
    private func chooseDestination() {
        let panel = NSSavePanel()
        panel.title = "保存收到的文件"; panel.prompt = "保存"
        panel.message = "请选择新的文件名；已有非空文件不会被覆盖。"
        panel.nameFieldStringValue = URL(fileURLWithPath: message.name).lastPathComponent
        panel.canCreateDirectories = true
        panel.begin { response in
            if response == .OK, let url = panel.url { Task { @MainActor in await store.saveChatFile(message, to: url) } }
        }
    }
}
