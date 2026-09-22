import AppKit
import CoreImage.CIFilterBuiltins
import SwiftUI

struct PairingSheet: View {
    @EnvironmentObject var store: AppStore
    @Environment(\.dismiss) private var dismiss
    @State private var server = ""
    @State private var fingerprint = ""
    @State private var serviceID = ""
    @State private var code = ""
    @State private var name = Host.current().localizedName ?? "我的 Mac"
    @State private var pastedPayload = ""
    @State private var payloadError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack {
                Text(store.state.paired ? "配对新设备" : "配对这台 Mac").font(.title2.weight(.semibold))
                Spacer()
                Button { dismiss() } label: { Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary).font(.title3) }.buttonStyle(.plain).keyboardShortcut(.cancelAction)
            }
            if store.state.paired {
                if let pairing = store.pairingResult {
                    PairingCodeContent(pairing: pairing)
                    Button("生成新的配对码") { Task { await store.createPairing() } }.disabled(store.busy)
                } else {
                    Text("使用钥匙串中的管理凭据生成一次性配对码，在另一台设备上输入或扫描。").foregroundStyle(.secondary)
                    Button { Task { await store.createPairing() } } label: {
                        HStack { if store.busy { ProgressView().controlSize(.small) }; Text("生成一次性配对码") }
                    }.buttonStyle(.borderedProminent).disabled(store.busy || !store.backendAvailable)
                    Button("设置管理凭据…") { store.section = .general; dismiss() }.buttonStyle(.link)
                }
            } else {
                Text("填写控制服务提供的连接信息，或粘贴配对信息自动填充。").foregroundStyle(.secondary)
                HStack {
                    TextField("粘贴配对信息（JSON，可选）", text: $pastedPayload)
                    Button("填入") { importPayload() }.disabled(pastedPayload.isEmpty)
                }
                if let error = payloadError { Text(error).font(.caption).foregroundStyle(.orange) }
                Form {
                    TextField("服务地址", text: $server, prompt: Text("https://your-server.example"))
                    TextField("证书 SHA-256 指纹", text: $fingerprint, prompt: Text("64 位十六进制字符")).font(.system(.body, design: .monospaced))
                    TextField("服务 ID（可选）", text: $serviceID)
                    TextField("一次性配对码", text: $code)
                    TextField("设备名称", text: $name)
                }
                HStack {
                    Spacer()
                    if store.busy { ProgressView().controlSize(.small) }
                    Button("连接并配对") {
                        Task { if await store.pair(server: server, fingerprint: fingerprint, serviceID: serviceID, code: code, name: name) { dismiss() } }
                    }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                        .disabled(store.busy || !store.backendAvailable)
                }
            }
            if let error = store.operationError { ErrorBanner(message: error) }
        }
        .padding(26).frame(width: 580)
        .onAppear { store.operationError = nil; store.pairingResult = nil; server = store.state.server ?? "" }
    }

    private func importPayload() {
        do {
            guard let data = pastedPayload.data(using: .utf8),
                  let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let parsedServer = object["server"] as? String,
                  let parsedFingerprint = object["fingerprint"] as? String,
                  let parsedCode = object["code"] as? String else { throw AgentError.invalidResponse }
            server = parsedServer; fingerprint = parsedFingerprint; code = parsedCode
            serviceID = object["serviceId"] as? String ?? ""
            payloadError = nil
        } catch { payloadError = "配对信息应包含 server、fingerprint 和 code。" }
    }
}

struct PairingCodeContent: View {
    let pairing: PairingResult

    private var qrImage: NSImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(pairing.payload.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage?.transformed(by: CGAffineTransform(scaleX: 7, y: 7)),
              let image = CIContext().createCGImage(output, from: output.extent) else { return nil }
        return NSImage(cgImage: image, size: NSSize(width: output.extent.width, height: output.extent.height))
    }

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            let expired = pairing.expiration.map { context.date >= $0 } ?? false
            HStack(spacing: 25) {
                Group {
                    if let image = qrImage {
                        Image(nsImage: image).interpolation(.none).resizable().scaledToFit().padding(10).background(.white)
                            .opacity(expired ? 0.25 : 1)
                    } else { Text("无法生成二维码，请使用配对码。").foregroundStyle(.secondary).multilineTextAlignment(.center) }
                }.frame(width: 190, height: 190)
                Divider().frame(height: 185)
                VStack(alignment: .leading, spacing: 14) {
                    Text("在军哥互联 Android 客户端输入服务地址、证书指纹和以下一次性配对码。").font(.callout)
                    Text(pairing.code).font(.system(size: 25, weight: .semibold, design: .monospaced)).foregroundStyle(expired ? Color.secondary : Color.accentColor).textSelection(.enabled)
                    if let expiration = pairing.expiration {
                        Text(expired ? "此配对码已过期，请重新生成。" : "有效至 \(expiration.formatted(date: .omitted, time: .shortened)) · 仅可使用一次")
                            .font(.caption).foregroundStyle(expired ? Color.orange : Color.secondary)
                    } else { Text("有效期以控制服务为准 · 仅可使用一次").font(.caption).foregroundStyle(.secondary) }
                    Button("复制配对信息") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(pairing.payload, forType: .string)
                    }.disabled(expired)
                }
            }
        }
    }
}
