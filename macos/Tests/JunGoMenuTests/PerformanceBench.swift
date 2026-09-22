import Foundation

// Synthetic fixtures only; this never connects to the user's running daemon.
@main
struct PerformanceBench {
    @inline(never) static func measure(_ name: String, repeats: Int = 5, _ work: () throws -> Int) rethrows {
        var times: [Double] = [], checksum = 0
        for _ in 0..<repeats {
            let begin = DispatchTime.now().uptimeNanoseconds
            checksum &+= try work()
            times.append(Double(DispatchTime.now().uptimeNanoseconds - begin) / 1_000_000)
        }
        let median = times.sorted()[times.count / 2]
        print("\(name): median_ms=\(String(format: "%.3f", median)) repeats=\(repeats) checksum=\(checksum)")
    }
    static func fixtures(messageCount: Int, peerCount: Int, transferCount: Int) throws -> (Data, Data) {
        let peers = (0..<peerCount).map { ["id":"peer\($0)", "name":"测试设备\($0)", "ip":"100.96.0.\($0 + 2)", "path":"relay"] }
        let messages: [[String: Any]] = (0..<messageCount).map { index -> [String: Any] in
            let date = String(format:"2026-09-%02dT10:%02d:%02d.%06dZ",1 + index / 86400,index / 60 % 60,index % 60,index % 1000000)
            return ["id":"message\(index)", "deviceId":"peer\(index % peerCount)", "direction":"incoming", "kind":"text", "text":"Synthetic message \(index) 用于本机性能测试", "status":"received", "created":date]
        }
        let transfers: [[String: Any]] = (0..<transferCount).map { index in ["id":"task\(index)","name":"file\(index).bin","status":"complete","completed":1024,"size":1024] }
        return (try JSONSerialization.data(withJSONObject: ["paired":true,"peers":peers,"transfers":transfers]), try JSONSerialization.data(withJSONObject: ["messages":messages]))
    }
    // Exact pre-optimization conversation query from ChatsSettings, independent of later production indexes.
    static func legacyConversations(_ peers: [Peer], _ messages: [ChatMessage]) -> [String] {
        let ids = Set(peers.map(\.id)).union(messages.map(\.deviceId))
        return ids.map { id in (id: id, peer: peers.first { $0.id == id }, latest: messages.last { $0.deviceId == id }) }
            .sorted { lhs, rhs in
                let a = lhs.latest.flatMap { legacyDate($0.created) } ?? .distantPast, b = rhs.latest.flatMap { legacyDate($0.created) } ?? .distantPast
                return a == b ? lhs.id < rhs.id : a > b
            }.map(\.id)
    }
    static func legacyDate(_ value: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: value) ?? ISO8601DateFormatter().date(from: value)
    }
    static func legacyState(_ data: Data) throws -> RootState {
        if let object = try JSONSerialization.jsonObject(with: data) as? [String: Any], let state = object["state"] as? [String: Any] {
            return try JSONDecoder().decode(RootState.self, from: JSONSerialization.data(withJSONObject: state))
        }
        return try JSONDecoder().decode(RootState.self, from: data)
    }
    static func main() async throws {
        let (stateData, chatData) = try fixtures(messageCount: 20000, peerCount: 100, transferCount: 5000)
        let state = try LocalAgentClient.decodeState(stateData)
        let messages = try JSONDecoder().decode(ChatList.self, from: chatData).messages
        print("fixture messages=20000 peers=100 transfers=5000 state_bytes=\(stateData.count) chat_bytes=\(chatData.count)")
        if CommandLine.arguments.contains("--legacy") {
            try measure("decode-state") { try legacyState(stateData).transfers.count }
            try measure("decode-chat") { try JSONDecoder().decode(ChatList.self, from: chatData).messages.count }
            measure("conversation-query") { legacyConversations(state.peers, messages).count }
            measure("selected-history-100x") { (0..<100).reduce(0) { sum, number in sum + messages.filter { $0.deviceId == "peer\(number % 100)" }.count } }
            measure("transfer-lookup-1000x") { (0..<1000).reduce(0) { sum, number in sum + (state.transfers.first { $0.id == "task\(number + 4000)" } == nil ? 0 : 1) } }
            return
        }
        try measure("decode-state") { try LocalAgentClient.decodeState(stateData).transfers.count }
        try measure("decode-chat") { try JSONDecoder().decode(ChatList.self, from: chatData).messages.count }
        measure("conversation-index-build") { ChatIndex(messages).byDevice.count }
        let index = ChatIndex(messages)
        measure("conversation-query") { index.conversations(peers: state.peers).count }
        measure("selected-history-100x") { (0..<100).reduce(0) { sum, number in sum + (index.byDevice["peer\(number % 100)"]?.count ?? 0) } }
        let transfers = Dictionary(uniqueKeysWithValues: state.transfers.map { ($0.id, $0) })
        measure("transfer-lookup-1000x") { (0..<1000).reduce(0) { sum, index in sum + (transfers["task\(index + 4000)"] == nil ? 0 : 1) } }
        let cache = SnapshotDecoder()
        _ = try await cache.decodeState(stateData)
        _ = try await cache.decodeChats(chatData)
        let sameState = stateData.withUnsafeBytes { Data($0) }
        let sameChats = chatData.withUnsafeBytes { Data($0) }
        try await measureAsync("unchanged-state") { try await cache.decodeState(sameState) == nil ? 1 : 0 }
        try await measureAsync("unchanged-chat-old-daemon") { try await cache.decodeChats(sameChats) == nil ? 1 : 0 }
        var versionedObject = try JSONSerialization.jsonObject(with: chatData) as! [String: Any]
        versionedObject["version"] = "fixture:1"
        let versioned = try JSONSerialization.data(withJSONObject: versionedObject)
        _ = try await cache.decodeChats(versioned)
        let unchanged = Data(#"{"version":"fixture:1","unchanged":true}"#.utf8)
        try await measureAsync("unchanged-chat-versioned") { try await cache.decodeChats(unchanged) == nil ? 1 : 0 }
        print("unchanged_chat_bytes=\(unchanged.count)")
    }

    @inline(never) static func measureAsync(_ name: String, repeats: Int = 5, _ work: () async throws -> Int) async rethrows {
        var times: [Double] = [], checksum = 0
        for _ in 0..<repeats {
            let begin = DispatchTime.now().uptimeNanoseconds
            checksum &+= try await work()
            times.append(Double(DispatchTime.now().uptimeNanoseconds - begin) / 1_000_000)
        }
        print("\(name): median_ms=\(String(format: "%.3f", times.sorted()[times.count / 2])) repeats=\(repeats) checksum=\(checksum)")
    }
}
