import XCTest
@testable import JunGoMenu

final class ModelTests: XCTestCase {
    func testLocalEndpointRejectsExternalAndRedirectLikeAddresses() throws {
        let accepted = LocalEndpoint(url: "http://127.0.0.1:49812", token: "local-secret")
        XCTAssertEqual(try accepted.validatedRPCURL().absoluteString, "http://127.0.0.1:49812/v1/rpc")
        let ipv6 = LocalEndpoint(url: "http://[::1]:49812", token: "local-secret")
        XCTAssertNoThrow(try ipv6.validatedRPCURL())
        for url in ["http://example.com:80", "https://127.0.0.1:1234", "http://127.0.0.1", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/external", "http://127.0.0.1:1234?redirect=example.com", "http://127.0.0.1:1234#fragment"] {
            XCTAssertThrowsError(try LocalEndpoint(url: url, token: "secret").validatedRPCURL(), url)
        }
        XCTAssertThrowsError(try LocalEndpoint(url: "http://127.0.0.1:1234", token: "").validatedRPCURL())
        XCTAssertThrowsError(try LocalEndpoint(url: "http://127.0.0.1:1234", token: "bad\r\nheader").validatedRPCURL())
    }

    func testActualAndWrappedStateDecodeWithoutSampleDevices() throws {
        let data = Data(#"{"paired":true,"device":{"name":"我的 Mac","ip":"100.96.0.2"},"peers":[{"id":"p1","name":"手机","ip":"100.96.0.3","path":"relay"}],"meshEnabled":true,"vpnRunning":true,"shares":[],"transfers":[]}"#.utf8)
        let state = try LocalAgentClient.decodeState(data)
        XCTAssertEqual(state.device.name, "我的 Mac")
        XCTAssertEqual(state.peers.first?.connectionLabel, "中继")
        XCTAssertTrue(state.vpnRunning)
        let wrapped = Data(("{\"state\":" + String(decoding: data, as: UTF8.self) + "}").utf8)
        XCTAssertEqual(try LocalAgentClient.decodeState(wrapped), state)
        let idle = try LocalAgentClient.decodeState(Data(#"{"paired":false}"#.utf8))
        XCTAssertTrue(idle.peers.isEmpty)
        XCTAssertTrue(idle.transfers.isEmpty)
        XCTAssertFalse(idle.meshEnabled)
        XCTAssertThrowsError(try LocalAgentClient.decodeState(Data(#"{"error":"unauthorized"}"#.utf8)))
    }

    func testUnknownPeerStatusCannotAppearConnected() throws {
        let data = Data(#"{"id":"peer","name":"Mac","path":"unexpected"}"#.utf8)
        let peer = try JSONDecoder().decode(Peer.self, from: data)
        XCTAssertFalse(peer.isReachable)
        XCTAssertEqual(peer.connectionLabel, "不可达")
    }

    func testTransfersPreservePauseAndCancellation() throws {
        let paused = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","name":"file","status":"paused","completed":2,"size":10}"#.utf8))
        XCTAssertFalse(paused.isActive)
        XCTAssertTrue(paused.canResume)
        XCTAssertEqual(paused.progress, 0.2)
        let cancelled = try JSONDecoder().decode(Transfer.self, from: Data(#"{"id":"task","status":"cancelled","completed":200,"size":10}"#.utf8))
        XCTAssertFalse(cancelled.isActive)
        XCTAssertFalse(cancelled.canResume)
        XCTAssertEqual(cancelled.progress, 1)
    }

    func testPairingDatesAcceptServerTimestampFormats() throws {
        for timestamp in ["2026-09-22T10:20:30Z", "2026-09-22T10:20:30.123456Z"] {
            let object: [String: String] = ["server":"https://example.invalid", "fingerprint":String(repeating:"a",count:64), "serviceId":"service", "code":"code", "expiresAt":timestamp, "payload":"{\"code\":\"real-server-code\"}"]
            let result = try JSONDecoder().decode(PairingResult.self, from: JSONSerialization.data(withJSONObject:object))
            XCTAssertNotNil(result.expiration)
            XCTAssertEqual(result.payload, object["payload"])
        }
    }
}
