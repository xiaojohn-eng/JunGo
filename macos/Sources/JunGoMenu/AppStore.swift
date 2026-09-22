import AppKit
import Foundation
import ServiceManagement
import SwiftUI

@MainActor
final class AppStore: ObservableObject {
    @Published private(set) var state = RootState()
    @Published private(set) var backendAvailable = false
    @Published private(set) var connecting = false
    @Published private(set) var busy = false
    @Published var operationError: String?
    @Published private(set) var connectionError: String?
    @Published var section: SettingsSection = .devices
    @Published var showPairing = false
    @Published var pairingResult: PairingResult?
    @Published private(set) var loginStatus = SMAppService.mainApp.status
    @Published var administratorNotice: String?
    @Published private(set) var messages: [ChatMessage] = []
    @Published private(set) var chatError: String?
    @Published private(set) var chatSaves: [String: ChatSaveSelection] = [:]

    let stateDirectory: URL
    private var client: LocalAgentClient?
    private var process: Process?
    private var pollTask: Task<Void, Never>?
    private var launchInProgress = false
    private var started = false
    private var visibleSurfaces = Set<UUID>()
    private var refreshTask: Task<Void, Never>?
    private var chatRefreshTask: Task<Void, Never>?
    private var lastChatRefresh: TimeInterval = 0
    private var hasActiveTransfers = false
    private(set) var chatIndex = ChatIndex()
    private(set) var conversations: [ChatConversation] = []
    private(set) var transfersByID: [String: Transfer] = [:]

    init() {
        stateDirectory = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/JunGo", isDirectory: true)
    }

    var statusTitle: String {
        if connecting { return "正在连接后台" }
        if !backendAvailable { return "后台未连接" }
        if !state.paired { return "尚未配对" }
        if !state.meshEnabled { return "组网已关闭" }
        if let error = state.error, !error.isEmpty { return "连接需要处理" }
        if state.meshRunning { return "已连接" }
        return "正在建立连接"
    }
    var connected: Bool { backendAvailable && state.paired && state.meshEnabled && state.meshRunning && (state.error?.isEmpty ?? true) }
    var visibleError: String? { operationError ?? connectionError ?? state.error.flatMap { $0.isEmpty ? nil : $0 } }

    func start() async {
        guard !started else { return }
        started = true
        await reconnect()
        restartPolling()
    }

    func setSurfaceVisible(_ id: UUID, visible: Bool) {
        let changed = visible ? visibleSurfaces.insert(id).inserted : visibleSurfaces.remove(id) != nil
        guard changed, started else { return }
        restartPolling()
        if visible { Task { await refresh(force: true) } }
    }

    private func restartPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let interval = self.map({ PollingPolicy.interval(visible: !$0.visibleSurfaces.isEmpty, activeTransfers: $0.hasActiveTransfers) }) else { break }
                do { try await Task.sleep(nanoseconds: interval) } catch { break }
                guard let self, !Task.isCancelled else { break }
                if !self.launchInProgress && !self.busy {
                    let chats = PollingPolicy.refreshChats(visible: !self.visibleSurfaces.isEmpty, activeTransfers: self.hasActiveTransfers,
                                                          sinceLastRefresh: ProcessInfo.processInfo.systemUptime - self.lastChatRefresh)
                    await self.refresh(includeChats: chats)
                }
            }
        }
    }

    private func apply(_ projection: StateProjection) {
        transfersByID = projection.transfersByID
        hasActiveTransfers = projection.hasActiveTransfers
        if projection.state.peers != state.peers { conversations = chatIndex.conversations(peers: projection.state.peers) }
        state = projection.state
    }

    func reconnect() async {
        guard !launchInProgress else { return }
        launchInProgress = true
        connecting = true
        connectionError = nil
        defer { connecting = false; launchInProgress = false }
        if await connectExisting() { return }
        do {
            if process?.isRunning != true { try launchAgent() }
            for _ in 0..<30 {
                try await Task.sleep(nanoseconds: 300_000_000)
                if await connectExisting() { return }
                if let process, !process.isRunning {
                    throw AgentError.message("本机后台已退出（状态 \(process.terminationStatus)）。请检查安装或重新连接。")
                }
            }
            throw AgentError.unavailable
        } catch {
            backendAvailable = false
            connectionError = error.localizedDescription
        }
    }

    private func connectExisting() async -> Bool {
        do {
            let data = try Data(contentsOf: stateDirectory.appendingPathComponent("local-api.json"))
            let endpoint = try JSONDecoder().decode(LocalEndpoint.self, from: data)
            let candidate = try LocalAgentClient(endpoint: endpoint)
            if let updated = try await candidate.readState() { apply(updated) }
            client = candidate
            backendAvailable = true
            connectionError = nil
            await refreshChats()
            return true
        } catch { return false }
    }

    private func launchAgent() throws {
        let override = ProcessInfo.processInfo.environment["JUNGO_AGENT_PATH"]
        let executable = override.map(URL.init(fileURLWithPath:)) ?? Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/jungo")
        guard FileManager.default.isExecutableFile(atPath: executable.path) else { throw AgentError.missingAgent }
        try FileManager.default.createDirectory(at: stateDirectory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        let child = Process()
        child.executableURL = executable
        child.arguments = ["agent", "--state", stateDirectory.path, "--local-api"]
        child.standardInput = FileHandle.nullDevice
        child.standardOutput = FileHandle.nullDevice
        child.standardError = FileHandle.nullDevice
        try child.run()
        process = child
    }

    func refresh(force: Bool = false, includeChats: Bool = true) async {
        if let current = refreshTask {
            await current.value
            if force { await refresh(includeChats: includeChats) }
            return
        }
        guard let client else { return }
        let work = Task { [weak self] in
            guard let self else { return }
            defer { self.refreshTask = nil }
            do {
                let updated = try await client.readState()
                guard self.client === client else { return }
                if let updated { self.apply(updated) }
                if !self.backendAvailable { self.backendAvailable = true }
                if self.connectionError != nil { self.connectionError = nil }
                if includeChats { await self.refreshChats(force: force) }
            } catch {
                guard self.client === client else { return }
                if self.backendAvailable { self.backendAvailable = false }
                if self.connectionError != error.localizedDescription { self.connectionError = error.localizedDescription }
            }
        }
        refreshTask = work
        await work.value
    }

    func refreshChats(force: Bool = false) async {
        if let current = chatRefreshTask {
            await current.value
            if force { await refreshChats() }
            return
        }
        guard let client, backendAvailable else { return }
        let work = Task { [weak self] in
            guard let self else { return }
            defer { self.chatRefreshTask = nil }
            do {
                let updated = try await client.readChats()
                guard self.client === client else { return }
                if let updated {
                    self.chatIndex = updated.index
                    self.conversations = updated.index.conversations(peers: self.state.peers)
                    self.messages = updated.index.messages
                }
                self.lastChatRefresh = ProcessInfo.processInfo.systemUptime
                if self.chatError != nil { self.chatError = nil }
            } catch {
                guard self.client === client else { return }
                if self.chatError != error.localizedDescription { self.chatError = error.localizedDescription }
            }
        }
        chatRefreshTask = work
        await work.value
    }

    func sendText(to deviceID: String, text: String) async -> Bool {
        guard !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, text.unicodeScalars.count <= 4096 else {
            operationError = "消息不能为空，且最多 4096 个字符。"; return false
        }
        return await perform("chatSendText", params: ["deviceId": deviceID, "text": text])
    }

    func sendFiles(to deviceID: String, urls: [URL]) async {
        guard !busy else { operationError = "上一项操作尚未完成，请稍后再发送文件。"; return }
        guard let client, backendAvailable else { operationError = AgentError.unavailable.localizedDescription; return }
        guard !urls.isEmpty else { return }
        busy = true; operationError = nil
        defer { busy = false }
        do {
            for url in urls { try await client.enqueueFile(to: deviceID, url: url) }
        } catch { operationError = error.localizedDescription }
        // One refresh for the batch, including any files accepted before a later source failed.
        await refresh(force: true)
    }

    func saveChatFile(_ message: ChatMessage, to url: URL) async {
        guard !busy, message.canSave else { return }
        guard let client, backendAvailable else { operationError = AgentError.unavailable.localizedDescription; return }
        busy = true; operationError = nil
        defer { busy = false }
        do {
            let response = try await client.call("chatSaveFile", params: ["id": message.id, "destination": url.path])
            let transfer = try JSONDecoder().decode(Transfer.self, from: response)
            chatSaves[message.id] = ChatSaveSelection(transferID: transfer.id, selectedURL: url)
            await refresh(force: true)
        } catch { operationError = error.localizedDescription }
    }

    @discardableResult
    func perform(_ method: String, params: [String: Any] = [:]) async -> Bool {
        guard !busy else { return false }
        guard let client, backendAvailable else {
            operationError = AgentError.unavailable.localizedDescription
            return false
        }
        busy = true
        operationError = nil
        defer { busy = false }
        do {
            let data = try await client.call(method, params: params)
            guard self.client === client else { return true }
            if let updated = try? await client.acceptState(data) { apply(updated) }
            else { await refresh(force: true) }
            return true
        } catch { operationError = error.localizedDescription; return false }
    }

    func setMesh(_ enabled: Bool) async {
        _ = await perform("network", params: ["mesh": enabled, "proxy": false])
    }

    func pair(server: String, fingerprint: String, serviceID: String, code: String, name: String) async -> Bool {
        let server = server.trimmingCharacters(in: .whitespacesAndNewlines)
        let fingerprint = fingerprint.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let code = code.trimmingCharacters(in: .whitespacesAndNewlines)
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let url = URL(string: server), url.scheme == "https", url.host != nil,
              url.user == nil, url.password == nil, url.fragment == nil else {
            operationError = "请输入有效的 HTTPS 控制服务地址。"; return false
        }
        guard fingerprint.count == 64, fingerprint.allSatisfy({ $0.isHexDigit && $0.isASCII }) else {
            operationError = "证书 SHA-256 指纹应为 64 位十六进制字符。"; return false
        }
        guard !code.isEmpty, !name.isEmpty else { operationError = "请输入一次性配对码和设备名称。"; return false }
        var params: [String: Any] = ["server": server, "fingerprint": fingerprint, "code": code, "name": name]
        if !serviceID.isEmpty { params["serviceId"] = serviceID.trimmingCharacters(in: .whitespacesAndNewlines) }
        return await perform("pair", params: params)
    }

    func createPairing() async {
        guard !busy else { return }
        guard let client, backendAvailable else { operationError = AgentError.unavailable.localizedDescription; return }
        busy = true; operationError = nil; pairingResult = nil
        defer { busy = false }
        do {
            guard let token = try KeychainStore.read(), !token.isEmpty else {
                throw AgentError.message("请先在“通用”中将控制服务管理凭据保存到钥匙串。")
            }
            let data = try await client.call("createPairing", params: ["adminToken": token])
            let result = try JSONDecoder().decode(PairingResult.self, from: data)
            guard !result.payload.isEmpty, !result.code.isEmpty else { throw AgentError.invalidResponse }
            pairingResult = result
        } catch { operationError = error.localizedDescription }
    }

    func revoke(_ peer: Peer) async {
        do {
            guard let token = try KeychainStore.read(), !token.isEmpty else {
                throw AgentError.message("撤销设备需要钥匙串中的控制服务管理凭据。")
            }
            _ = await perform("revokeDevice", params: ["deviceId": peer.id, "adminToken": token])
        } catch { operationError = error.localizedDescription }
    }

    func saveAdministratorToken(_ token: String) -> Bool {
        do { try KeychainStore.save(token.trimmingCharacters(in: .whitespacesAndNewlines)); administratorNotice = "管理凭据已保存到系统钥匙串。"; return true }
        catch { operationError = error.localizedDescription; return false }
    }
    func removeAdministratorToken() {
        do { try KeychainStore.remove(); administratorNotice = "已移除本机钥匙串中的管理凭据。" }
        catch { operationError = error.localizedDescription }
    }

    func refreshLoginStatus() { loginStatus = SMAppService.mainApp.status }
    func setLoginEnabled(_ enabled: Bool) async {
        do {
            if enabled { try SMAppService.mainApp.register() }
            else { try await SMAppService.mainApp.unregister() }
        } catch { operationError = "无法更新登录项：\(error.localizedDescription)" }
        refreshLoginStatus()
    }
}
