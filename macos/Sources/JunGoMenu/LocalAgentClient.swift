import Foundation

struct LocalEndpoint: Decodable {
    let url: String
    let token: String

    func validatedRPCURL() throws -> URL {
        guard let components = URLComponents(string: url),
              components.scheme == "http",
              ["127.0.0.1", "::1", "[::1]"].contains(components.host ?? ""),
              let port = components.port, (1...65535).contains(port),
              components.user == nil, components.password == nil,
              components.query == nil, components.fragment == nil,
              components.path.isEmpty || components.path == "/",
              !token.isEmpty, token.count <= 4096,
              !token.unicodeScalars.contains(where: { $0.value < 32 || $0.value == 127 }),
              let base = components.url
        else { throw AgentError.invalidEndpoint }
        return base.appendingPathComponent("v1/rpc")
    }
}

enum AgentError: LocalizedError {
    case invalidEndpoint
    case missingAgent
    case unavailable
    case invalidResponse
    case message(String)

    var errorDescription: String? {
        switch self {
        case .invalidEndpoint: return "后台连接配置无效。仅允许本机回环地址。"
        case .missingAgent: return "安装包中缺少 jungo 后台程序，请重新安装完整的军哥互联应用。"
        case .unavailable: return "本机后台暂时不可用，请点击重新连接。"
        case .invalidResponse: return "后台返回了无法识别的内容，请检查应用和后台版本是否一致。"
        case .message(let text): return text
        }
    }
}

final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

final class LocalAgentClient {
    private let endpoint: LocalEndpoint
    private let session: URLSession
    private let delegate = NoRedirectDelegate()
    private let snapshots = SnapshotDecoder()

    init(endpoint: LocalEndpoint) throws {
        _ = try endpoint.validatedRPCURL()
        self.endpoint = endpoint
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 12
        configuration.timeoutIntervalForResource = 90
        configuration.urlCache = nil
        configuration.httpCookieStorage = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        // Never forward a local bearer token through an HTTP proxy.
        configuration.connectionProxyDictionary = [:]
        session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    }

    deinit { session.invalidateAndCancel() }

    func call(_ method: String, params: [String: Any] = [:]) async throws -> Data {
        var request = URLRequest(url: try endpoint.validatedRPCURL())
        request.timeoutInterval = method == "state" ? 2 : (method == "importProfile" ? 90 : 45)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(endpoint.token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["method": method, "params": params])
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw AgentError.invalidResponse }
        guard (200...299).contains(response.statusCode) else {
            let body = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
            let nested = body?["error"] as? [String: Any]
            let message = nested?["message"] as? String ?? body?["error"] as? String ?? body?["message"] as? String
            throw AgentError.message(message ?? "后台请求失败（\(response.statusCode)）。")
        }
        return data
    }

    func enqueueFile(to deviceID: String, url: URL) async throws {
        guard url.isFileURL else { throw AgentError.message("只能发送本机文件。") }
        let info = try url.resourceValues(forKeys: [.isRegularFileKey])
        guard info.isRegularFile == true else { throw AgentError.message("聊天当前支持单个或多个文件；请先将文件夹压缩后发送。") }
        _ = try await call("chatSendFile", params: ["deviceId": deviceID, "source": url.path, "name": url.lastPathComponent])
    }

    func readState() async throws -> StateProjection? {
        let data = try await call("state")
        return try await snapshots.decodeState(data)
    }

    func acceptState(_ data: Data) async throws -> StateProjection? { try await snapshots.decodeState(data) }

    func readChats() async throws -> ChatProjection? {
        let version = await snapshots.chatVersion
        let data = try await call("chatList", params: version.isEmpty ? [:] : ["version": version])
        return try await snapshots.decodeChats(data)
    }

    static func decodeState(_ data: Data) throws -> RootState {
        try JSONDecoder().decode(StateResponse.self, from: data).state
    }
}

private struct StateResponse: Decodable {
    let state: RootState
    enum CodingKeys: String, CodingKey { case state }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        state = values.contains(.state) ? try values.decode(RootState.self, forKey: .state) : try RootState(from: decoder)
    }
}
