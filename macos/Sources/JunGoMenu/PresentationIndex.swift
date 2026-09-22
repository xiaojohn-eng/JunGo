import Foundation

struct ChatConversation: Identifiable, Equatable {
    let id: String
    let peer: Peer?
    let latest: ChatMessage?
    let latestDate: Date
    var name: String { peer?.name ?? "历史设备 · \(id.prefix(6))" }
    var preview: String { latest.map { $0.isFile ? "文件：\($0.name)" : $0.text } ?? "开始与这台设备聊天" }
}

/// Build only when chat content changes, not on every keystroke/progress redraw.
struct ChatIndex {
    let messages: [ChatMessage]
    let byDevice: [String: [ChatMessage]]
    private let latest: [String: ChatMessage]
    private let latestDates: [String: Date]

    init(_ messages: [ChatMessage] = []) {
        self.messages = messages
        var grouped: [String: [ChatMessage]] = [:]
        var latest: [String: ChatMessage] = [:]
        for message in messages {
            grouped[message.deviceId, default: []].append(message)
            latest[message.deviceId] = message
        }
        byDevice = grouped
        self.latest = latest
        latestDates = latest.mapValues { $0.date ?? .distantPast }
    }

    func conversations(peers: [Peer]) -> [ChatConversation] {
        let peerLookup = Dictionary(peers.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        return Set(peerLookup.keys).union(latest.keys).map { id in
            let message = latest[id]
            return ChatConversation(id: id, peer: peerLookup[id], latest: message, latestDate: latestDates[id] ?? .distantPast)
        }.sorted { a, b in a.latestDate == b.latestDate ? a.id < b.id : a.latestDate > b.latestDate }
    }
}

struct StateProjection {
    let state: RootState
    let transfersByID: [String: Transfer]
    let hasActiveTransfers: Bool
}
struct ChatProjection {
    let index: ChatIndex
}

/// Actor isolation keeps large JSON decoding and index construction off MainActor.
/// Raw-response checks also make old daemons without chat versions inexpensive when idle.
actor SnapshotDecoder {
    private var stateData: Data?
    private var chatData: Data?
    private var state = RootState()
    private var index = ChatIndex()
    private var hasChats = false
    private(set) var chatVersion = ""

    func decodeState(_ data: Data) throws -> StateProjection? {
        if stateData == data { return nil }
        let decoded = try LocalAgentClient.decodeState(data)
        let changed = stateData == nil || decoded != state
        stateData = data
        guard changed else { return nil }
        state = decoded
        return StateProjection(state: decoded,
                               transfersByID: Dictionary(decoded.transfers.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first }),
                               hasActiveTransfers: decoded.activeIncoming > 0 || decoded.transfers.contains { $0.isActive })
    }

    func decodeChats(_ data: Data) throws -> ChatProjection? {
        if chatData == data { return nil }
        let response = try JSONDecoder().decode(ChatList.self, from: data)
        if response.unchanged {
            guard hasChats, !chatVersion.isEmpty, response.version == chatVersion else { throw AgentError.invalidResponse }
            chatData = data
            return nil
        }
        chatVersion = response.version ?? ""
        chatData = data
        if hasChats && response.messages == index.messages { return nil }
        index = ChatIndex(response.messages)
        hasChats = true
        return ChatProjection(index: index)
    }
}

struct PollingPolicy {
    static func interval(visible: Bool, activeTransfers: Bool) -> UInt64 {
        visible || activeTransfers ? 1_000_000_000 : 5_000_000_000
    }
    static func refreshChats(visible: Bool, activeTransfers: Bool, sinceLastRefresh: TimeInterval) -> Bool {
        visible || activeTransfers || sinceLastRefresh >= 10
    }
}

/// Formatters are expensive to construct; protect the shared parsers across executors.
private enum WireDateParser {
    static let lock = NSLock()
    static let fractional: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()
    static let whole = ISO8601DateFormatter()
    static func parse(_ value: String) -> Date? {
        guard !value.isEmpty else { return nil }
        lock.lock(); defer { lock.unlock() }
        return fractional.date(from: value) ?? whole.date(from: value)
    }
}

func wireDate(_ value: String) -> Date? { WireDateParser.parse(value) }
