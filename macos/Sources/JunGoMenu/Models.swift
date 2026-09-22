import Foundation

struct Device: Decodable, Equatable {
    var id: String = ""
    var name: String = ""
    var ip: String = ""

    init() {}
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decodeIfPresent(String.self, forKey: .id) ?? ""
        name = try values.decodeIfPresent(String.self, forKey: .name) ?? ""
        ip = try values.decodeIfPresent(String.self, forKey: .ip) ?? ""
    }
    enum CodingKeys: String, CodingKey { case id, name, ip }
}

struct Peer: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let ip: String
    let path: String

    enum CodingKeys: String, CodingKey { case id, name, ip, path }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        name = try values.decodeIfPresent(String.self, forKey: .name) ?? "未命名设备"
        ip = try values.decodeIfPresent(String.self, forKey: .ip) ?? ""
        path = try values.decodeIfPresent(String.self, forKey: .path) ?? "unavailable"
    }

    var connectionLabel: String {
        switch path {
        case "direct": return "直连"
        case "relay": return "中继"
        default: return "不可达"
        }
    }
    var isReachable: Bool { path == "direct" || path == "relay" }
}

struct SharedDirectory: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let path: String
    let readOnly: Bool
}

struct ServiceMapping: Decodable, Identifiable, Equatable {
    let port: Int
    let target: String
    let network: String
    var id: String { "\(network):\(port)" }
}

struct Transfer: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let status: String
    let completed: Int64
    let size: Int64
    let error: String?
    let destination: String?

    enum CodingKeys: String, CodingKey { case id, name, status, completed, size, error, destination }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        name = try values.decodeIfPresent(String.self, forKey: .name) ?? "文件传输"
        status = try values.decodeIfPresent(String.self, forKey: .status) ?? "unknown"
        completed = max(0, try values.decodeIfPresent(Int64.self, forKey: .completed) ?? 0)
        size = max(0, try values.decodeIfPresent(Int64.self, forKey: .size) ?? 0)
        error = try values.decodeIfPresent(String.self, forKey: .error)
        destination = try values.decodeIfPresent(String.self, forKey: .destination)
    }
    var progress: Double { size > 0 ? min(1, Double(completed) / Double(size)) : 0 }
    var isActive: Bool { ["running", "uploading", "downloading", "transferring", "pending", "queued", "hashing", "waiting"].contains(status) }
    var canResume: Bool { ["paused", "failed", "interrupted"].contains(status) }
    var label: String {
        switch status {
        case "running", "uploading", "downloading", "transferring": return "传输中"
        case "pending", "queued": return "等待传输"
        case "hashing": return "校验文件中"
        case "waiting": return "等待网络恢复"
        case "paused": return "已暂停"
        case "failed": return "传输失败"
        case "interrupted": return "连接中断"
        case "completed", "complete": return "已完成"
        case "cancelled": return "已取消"
        default: return "状态未知"
        }
    }
}

struct RootState: Decodable, Equatable {
    var paired = false
    var device = Device()
    var peers: [Peer] = []
    var meshEnabled = false
    var meshRunning = false
    var proxyEnabled = false
    var vpnRunning = false
    var activeIncoming = 0
    var transfers: [Transfer] = []
    var shares: [SharedDirectory] = []
    var services: [ServiceMapping] = []
    var error: String?
    var server: String?

    init() {}
    enum CodingKeys: String, CodingKey {
        case paired, device, peers, meshEnabled, meshRunning, proxyEnabled, vpnRunning, activeIncoming, transfers, shares, services, error, server
    }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        // A successful but unrelated/error JSON object is not a valid state.
        paired = try values.decode(Bool.self, forKey: .paired)
        device = try values.decodeIfPresent(Device.self, forKey: .device) ?? Device()
        peers = try values.decodeIfPresent([Peer].self, forKey: .peers) ?? []
        meshEnabled = try values.decodeIfPresent(Bool.self, forKey: .meshEnabled) ?? false
        meshRunning = try values.decodeIfPresent(Bool.self, forKey: .meshRunning) ?? false
        proxyEnabled = try values.decodeIfPresent(Bool.self, forKey: .proxyEnabled) ?? false
        vpnRunning = try values.decodeIfPresent(Bool.self, forKey: .vpnRunning) ?? false
        activeIncoming = max(0, try values.decodeIfPresent(Int.self, forKey: .activeIncoming) ?? 0)
        transfers = try values.decodeIfPresent([Transfer].self, forKey: .transfers) ?? []
        shares = try values.decodeIfPresent([SharedDirectory].self, forKey: .shares) ?? []
        services = try values.decodeIfPresent([ServiceMapping].self, forKey: .services) ?? []
        error = try values.decodeIfPresent(String.self, forKey: .error)
        server = try values.decodeIfPresent(String.self, forKey: .server)
    }
}

struct PairingResult: Decodable {
    let server: String
    let fingerprint: String
    let serviceId: String
    let code: String
    let expiresAt: String
    let payload: String

    var expiration: Date? {
        wireDate(expiresAt)
    }
}

enum SettingsSection: String, CaseIterable, Identifiable {
    case devices = "设备"
    case chats = "聊天"
    case shares = "共享目录"
    case general = "通用"
    var id: String { rawValue }
    var symbol: String {
        switch self { case .devices: return "laptopcomputer.and.iphone"; case .chats: return "bubble.left.and.bubble.right"; case .shares: return "folder"; case .general: return "gearshape" }
    }
}

struct ChatMessage: Decodable, Identifiable, Equatable {
    let id: String
    let deviceId: String
    let direction: String
    let kind: String
    let text: String
    let name: String
    let size: Int64
    let completed: Int64
    let status: String
    let transferId: String
    let created: String
    let error: String

    enum CodingKeys: String, CodingKey { case id, deviceId, direction, kind, text, name, size, completed, status, transferId, created, error }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        deviceId = try values.decode(String.self, forKey: .deviceId)
        direction = try values.decode(String.self, forKey: .direction)
        kind = try values.decode(String.self, forKey: .kind)
        text = try values.decodeIfPresent(String.self, forKey: .text) ?? ""
        name = try values.decodeIfPresent(String.self, forKey: .name) ?? "文件"
        size = max(0, try values.decodeIfPresent(Int64.self, forKey: .size) ?? 0)
        completed = max(0, try values.decodeIfPresent(Int64.self, forKey: .completed) ?? 0)
        status = try values.decodeIfPresent(String.self, forKey: .status) ?? "unknown"
        transferId = try values.decodeIfPresent(String.self, forKey: .transferId) ?? ""
        created = try values.decodeIfPresent(String.self, forKey: .created) ?? ""
        error = try values.decodeIfPresent(String.self, forKey: .error) ?? ""
        // A wire inbox path is deliberately not decoded into the UI model.
    }
    var isOutgoing: Bool { direction == "outgoing" }
    var isFile: Bool { kind == "file" }
    var progress: Double { size > 0 ? min(1, Double(completed) / Double(size)) : 0 }
    var canSave: Bool { !isOutgoing && isFile && status == "complete" }
    var statusLabel: String {
        switch status {
        case "queued": return "等待发送"
        case "waiting": return "等待对方或网络恢复"
        case "running": return isOutgoing ? "正在发送" : "正在接收"
        case "hashing": return "正在校验"
        case "paused": return "已暂停"
        case "cancelled": return "已取消"
        case "failed": return "发送或接收失败"
        case "complete": return isOutgoing ? "已送达" : "已接收"
        case "sent": return "已送达"
        case "received": return "已接收"
        default: return "状态未知"
        }
    }
    var date: Date? {
        wireDate(created)
    }
}

struct ChatList: Decodable {
    let messages: [ChatMessage]
    let version: String?
    let unchanged: Bool
    private enum CodingKeys: String, CodingKey { case messages, version, unchanged }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        version = try values.decodeIfPresent(String.self, forKey: .version)
        unchanged = try values.decodeIfPresent(Bool.self, forKey: .unchanged) ?? false
        messages = unchanged ? [] : try values.decode([ChatMessage].self, forKey: .messages)
    }
}

struct ChatSaveSelection {
    let transferID: String
    let selectedURL: URL

    func completedURL(transfers: [Transfer]) -> URL? {
        completedURL(transfer: transfers.first { $0.id == transferID })
    }

    func completedURL(transfer: Transfer?) -> URL? {
        guard selectedURL.isFileURL, let transfer,
              transfer.id == transferID, transfer.status == "complete" else { return nil }
        guard let destination = transfer.destination, !destination.isEmpty else { return selectedURL }
        let actual = URL(fileURLWithPath: destination).standardizedFileURL
        // Only a backend save task tied to this explicit panel selection can
        // reveal a file, and collision renames must stay inside that directory.
        guard destination.hasPrefix("/"), actual.deletingLastPathComponent() == selectedURL.standardizedFileURL.deletingLastPathComponent() else { return nil }
        return actual
    }
}
