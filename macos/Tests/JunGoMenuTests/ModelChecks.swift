import Foundation

// Compile the actual model/RPC source files with this small executable harness.
// It needs Foundation only, so it runs with Command Line Tools installations
// which do not ship a working XCTest/Swift Testing runtime.
private struct CheckFailure: Error, CustomStringConvertible { let description: String }
private func expect(_ value: @autoclosure () throws -> Bool, _ message: String) throws {
    if try !value() { throw CheckFailure(description: message) }
}
private func expectThrows(_ label: String, _ body: () throws -> Void) throws {
    do { try body() } catch { return }
    throw CheckFailure(description: "Expected an invalid value to be rejected: \(label)")
}

@main
struct ModelChecks {
    static func main() async {
        do {
            try run()
            try await performanceChecks()
            if CommandLine.arguments.count == 3, CommandLine.arguments[1] == "--endpoint" {
                let data = try Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[2]))
                let endpoint = try JSONDecoder().decode(LocalEndpoint.self, from: data)
                let client = try LocalAgentClient(endpoint: endpoint)
                let state = try LocalAgentClient.decodeState(try await client.call("state"))
                print("PASS: live Swift → daemon RPC (paired=\(state.paired), peers=\(state.peers.count))")
                let chats = try JSONDecoder().decode(ChatList.self, from: try await client.call("chatList"))
                print("PASS: live Swift → daemon chatList (messages=\(chats.messages.count))")
            }
        }
        catch { fputs("FAIL: \(error)\n", stderr); exit(1) }
    }
    static func run() throws {
        try localEndpointRejectsExternalAndRedirectLikeAddresses()
        try actualAndWrappedStateDecodeWithoutSampleDevices()
        try unknownPeerStatusCannotAppearConnected()
        try transfersPreservePauseAndCancellation()
        try pairingDatesAcceptServerTimestampFormats()
        try chatCardsAndExplicitSaveLocations()
        print("PASS: 6 Mac client checks (endpoint safety, real state, offline status, transfer states, pairing expiration, chat save safety)")
    }

    static func performanceChecks() async throws {
        let cache = SnapshotDecoder()
        let initial = try await cache.decodeState(Data(#"{"paired":false}"#.utf8))
        try expect(initial != nil, "The initial empty state must still replace a previous connection")
        let same = try await cache.decodeState(Data(#"{"paired":false}"#.utf8))
        try expect(same == nil, "Unchanged state should not publish a UI update")
        let equivalent = try await cache.decodeState(Data(#"{ "paired": false, "peers": [] }"#.utf8))
        try expect(equivalent == nil, "Equivalent states must not invalidate the UI")
        let active = try await cache.decodeState(Data(#"{"paired":true,"activeIncoming":1,"transfers":[{"id":"t","status":"paused"}]}"#.utf8))
        try expect(active?.hasActiveTransfers == true && active?.transfersByID["t"]?.canResume == true, "Incoming transfers or indexed task state were lost")

        let unknown = SnapshotDecoder()
        do { _ = try await unknown.decodeChats(Data(#"{"version":"epoch:1","unchanged":true}"#.utf8)); throw CheckFailure(description: "Unchanged without a snapshot must fail") }
        catch is CheckFailure { throw CheckFailure(description: "Unchanged without a snapshot must fail") } catch {}
        let full = Data(#"{"version":"epoch:1","messages":[{"id":"a","deviceId":"phone","direction":"incoming","kind":"text","text":"old","created":"2026-09-22T10:00:00Z"},{"id":"b","deviceId":"phone","direction":"incoming","kind":"text","text":"new","created":"2026-09-22T10:00:01.000001Z"},{"id":"history","deviceId":"removed","direction":"incoming","kind":"text","text":"history","created":"2026-09-22T10:00:02Z"}]}"#.utf8)
        let chats = try await cache.decodeChats(full)
        try expect(chats?.index.byDevice["phone"]?.map(\.id) == ["a", "b"], "Conversation index changed message order")
        let peers = try JSONDecoder().decode([Peer].self, from: Data(#"[{"id":"phone","name":"手机"},{"id":"idle","name":"空会话"}]"#.utf8))
        let conversations = chats!.index.conversations(peers: peers)
        try expect(conversations.map(\.id) == ["removed", "phone", "idle"], "History-only and empty peer conversations must remain ordered")
        try expect(conversations.first { $0.id == "phone" }?.latest?.id == "b", "Latest message mismatch")
        let unchanged = try await cache.decodeChats(Data(#"{"version":"epoch:1","unchanged":true}"#.utf8))
        try expect(unchanged == nil, "Unchanged must preserve history without publishing")
        let oldDaemon = try await cache.decodeChats(Data(#"{"messages":[]}"#.utf8))
        let fallbackVersion = await cache.chatVersion
        try expect(oldDaemon?.index.messages.isEmpty == true && fallbackVersion.isEmpty, "Old daemon compatibility must reset the version and accept full snapshots")
        let restarted = try await cache.decodeChats(Data(#"{"version":"new-epoch:1","messages":[]}"#.utf8))
        let nextVersion = await cache.chatVersion
        try expect(restarted == nil && nextVersion == "new-epoch:1", "Restart version must update even when message content is unchanged")

        try expect(PollingPolicy.interval(visible: true, activeTransfers: false) == 1_000_000_000, "Visible chats must retain one-second polling")
        try expect(PollingPolicy.interval(visible: false, activeTransfers: true) == 1_000_000_000, "Active transfers must retain one-second polling")
        try expect(PollingPolicy.interval(visible: false, activeTransfers: false) == 5_000_000_000, "Idle hidden windows should reduce wakeups")
        try expect(!PollingPolicy.refreshChats(visible: false, activeTransfers: false, sinceLastRefresh: 5), "Hidden idle chat history should not refresh every state poll")
        try expect(PollingPolicy.refreshChats(visible: true, activeTransfers: false, sinceLastRefresh: 0), "Visible message freshness must not be throttled")
        print("PASS: 4 Mac performance checks (unchanged state, version cache and restart fallback, conversation indexes, visibility polling)")
    }

    static func localEndpointRejectsExternalAndRedirectLikeAddresses() throws {
        let accepted = LocalEndpoint(url: "http://127.0.0.1:49812", token: "local-secret")
        try expect(accepted.validatedRPCURL().absoluteString == "http://127.0.0.1:49812/v1/rpc", "Wrong loopback RPC URL")
        _ = try LocalEndpoint(url: "http://[::1]:49812", token: "local-secret").validatedRPCURL()
        for url in ["http://example.com:80", "https://127.0.0.1:1234", "http://127.0.0.1", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/external", "http://127.0.0.1:1234?redirect=example.com", "http://127.0.0.1:1234#fragment"] {
            try expectThrows(url) { _ = try LocalEndpoint(url: url, token: "secret").validatedRPCURL() }
        }
        try expectThrows("empty token") { _ = try LocalEndpoint(url: "http://127.0.0.1:1234", token: "").validatedRPCURL() }
        try expectThrows("newline token") { _ = try LocalEndpoint(url: "http://127.0.0.1:1234", token: "bad\r\nheader").validatedRPCURL() }
        try expectThrows("tab token") { _ = try LocalEndpoint(url: "http://127.0.0.1:1234", token: "bad\theader").validatedRPCURL() }
    }

    static func actualAndWrappedStateDecodeWithoutSampleDevices() throws {
        let data = Data(#"{"paired":true,"device":{"name":"我的 Mac","ip":"100.96.0.2"},"peers":[{"id":"p1","name":"手机","ip":"100.96.0.3","path":"relay"}],"meshEnabled":true,"meshRunning":true,"vpnRunning":false,"shares":[],"transfers":[],"services":[{"port":22,"target":"127.0.0.1:22","network":"tcp"}]}"#.utf8)
        let state = try LocalAgentClient.decodeState(data)
        try expect(state.device.name == "我的 Mac", "Device name mismatch")
        try expect(state.peers.first?.connectionLabel == "中继", "Relay status mismatch")
        try expect(state.meshRunning && !state.vpnRunning, "Mac mesh was incorrectly coupled to Android VPN state")
        try expect(state.services.first?.target == "127.0.0.1:22", "Explicit service mapping was lost")
        let wrapped = Data(("{\"state\":" + String(decoding: data, as: UTF8.self) + "}").utf8)
        try expect(LocalAgentClient.decodeState(wrapped) == state, "Wrapped state mismatch")
        let idle = try LocalAgentClient.decodeState(Data(#"{"paired":false}"#.utf8))
        try expect(idle.peers.isEmpty && idle.transfers.isEmpty && !idle.meshEnabled, "Idle state contains fake activity")
        try expectThrows("error response as state") { _ = try LocalAgentClient.decodeState(Data(#"{"error":"unauthorized"}"#.utf8)) }
    }

    static func unknownPeerStatusCannotAppearConnected() throws {
        let peer = try JSONDecoder().decode(Peer.self, from: Data(#"{"id":"peer","name":"Mac","path":"unexpected"}"#.utf8))
        try expect(!peer.isReachable && peer.connectionLabel == "不可达", "Unknown peer was shown online")
    }

    static func transfersPreservePauseAndCancellation() throws {
        let paused = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","name":"file","status":"paused","completed":2,"size":10}"#.utf8))
        try expect(!paused.isActive && paused.canResume && paused.progress == 0.2, "Paused task state mismatch")
        let cancelled = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","status":"cancelled","completed":200,"size":10}"#.utf8))
        try expect(!cancelled.isActive && !cancelled.canResume && cancelled.progress == 1, "Cancelled task can auto-resume or progress is unbounded")
        let hashing = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","status":"hashing"}"#.utf8))
        try expect(hashing.isActive && hashing.label == "校验文件中", "Real engine hashing state was not recognized")
        let complete = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","status":"complete"}"#.utf8))
        try expect(!complete.isActive && complete.label == "已完成", "Real engine completion state was not recognized")
    }

    static func pairingDatesAcceptServerTimestampFormats() throws {
        for timestamp in ["2026-09-22T10:20:30Z", "2026-09-22T10:20:30.123456Z"] {
            let object: [String: String] = ["server":"https://example.invalid", "fingerprint":String(repeating:"a",count:64), "serviceId":"service", "code":"code", "expiresAt":timestamp, "payload":"{\"code\":\"real-server-code\"}"]
            let result = try JSONDecoder().decode(PairingResult.self, from: JSONSerialization.data(withJSONObject:object))
            try expect(result.expiration != nil && result.payload == object["payload"], "Pairing response changed or expiration is invalid")
        }
    }

    static func chatCardsAndExplicitSaveLocations() throws {
        let incoming = try JSONDecoder().decode(ChatMessage.self, from: Data(#"{"id":"m1","deviceId":"phone","direction":"incoming","kind":"file","name":"photo.jpg","size":10,"completed":10,"status":"complete","created":"2026-09-22T10:20:30.123456Z","path":"/untrusted/remote/path"}"#.utf8))
        try expect(incoming.canSave && incoming.progress == 1 && incoming.date != nil, "Received file card state mismatch")
        let paused = try JSONDecoder().decode(ChatMessage.self, from: Data(#"{"id":"m2","deviceId":"phone","direction":"outgoing","kind":"file","name":"photo.jpg","size":10,"completed":2,"status":"paused"}"#.utf8))
        try expect(!paused.canSave && paused.statusLabel == "已暂停", "Outgoing paused file can be saved as a received file")
        let save = ChatSaveSelection(transferID: "copy1", selectedURL: URL(fileURLWithPath: "/Users/example/Downloads/photo.jpg"))
        let unrelated = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"other","status":"complete","destination":"/Users/example/Downloads/photo.jpg"}"#.utf8))
        try expect(save.completedURL(transfers: [unrelated]) == nil, "Unrelated completed transfer authorized Reveal")
        let renamed = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"copy1","status":"complete","destination":"/Users/example/Downloads/photo (1).jpg"}"#.utf8))
        try expect(save.completedURL(transfers: [renamed])?.lastPathComponent == "photo (1).jpg", "Safe keep-both name was not accepted")
        let escaped = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"copy1","status":"complete","destination":"/Users/example/Downloads/../../private/photo.jpg"}"#.utf8))
        try expect(save.completedURL(transfers: [escaped]) == nil, "Reveal escaped the user-selected destination directory")
        let incomplete = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"copy1","status":"failed"}"#.utf8))
        try expect(save.completedURL(transfers: [incomplete]) == nil, "Failed save authorized Reveal")
    }
}
