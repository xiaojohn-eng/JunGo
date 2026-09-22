import Foundation
import Security

enum KeychainStore {
    private static let service = "com.junge.connect.mac.admin"
    private static let account = "control-admin"
    private static var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service, kSecAttrAccount as String: account]
    }

    static func read() throws -> String? {
        var lookup = query
        lookup[kSecReturnData as String] = true
        lookup[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(lookup as CFDictionary, &item)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = item as? Data,
              let value = String(data: data, encoding: .utf8) else { throw keychainError(status) }
        return value
    }

    static func save(_ token: String) throws {
        guard !token.isEmpty else { throw AgentError.message("管理凭据不能为空。") }
        let attributes: [String: Any] = [kSecValueData as String: Data(token.utf8)]
        let update = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if update == errSecItemNotFound {
            var item = query
            item[kSecValueData as String] = Data(token.utf8)
            item[kSecAttrLabel as String] = "军哥互联控制服务管理凭据"
            item[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let result = SecItemAdd(item as CFDictionary, nil)
            guard result == errSecSuccess else { throw keychainError(result) }
        } else if update != errSecSuccess { throw keychainError(update) }
    }

    static func remove() throws {
        let result = SecItemDelete(query as CFDictionary)
        guard result == errSecSuccess || result == errSecItemNotFound else { throw keychainError(result) }
    }

    private static func keychainError(_ status: OSStatus) -> AgentError {
        let message = SecCopyErrorMessageString(status, nil) as String? ?? "错误 \(status)"
        return .message("无法访问钥匙串：\(message)")
    }
}
