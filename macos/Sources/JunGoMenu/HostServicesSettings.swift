import SwiftUI

struct HostServicesSettings: View {
    @EnvironmentObject var store: AppStore
    @State private var showAdd = false
    @State private var removing: ServiceMapping?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("本机服务").font(.headline)
                Spacer()
                Button("添加服务…") { showAdd = true }.disabled(store.busy || !store.backendAvailable)
            }
            Text("只开放你添加的本机 TCP 服务，其他端口不会通过组网开放。").font(.callout).foregroundStyle(.secondary)
            if store.state.services.isEmpty {
                GroupBox {
                    Text("尚未开放本机服务。需要从手机访问 SSH 或 Web 服务时，可在这里添加对应端口。")
                        .font(.callout).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading).padding(10)
                }
            } else {
                GroupBox {
                    VStack(spacing: 0) {
                        ForEach(Array(store.state.services.enumerated()), id: \.element.id) { index, mapping in
                            HStack(spacing: 12) {
                                Image(systemName: "network").font(.title3).foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(store.state.device.ip.isEmpty ? "私网端口 \(mapping.port)" : "\(store.state.device.ip):\(mapping.port)")
                                        .font(.system(.callout, design: .monospaced)).textSelection(.enabled)
                                    Text("\(mapping.network.uppercased()) → \(mapping.target)").font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                                }
                                Spacer()
                                Button("移除…", role: .destructive) { removing = mapping }.disabled(store.busy || !store.backendAvailable)
                            }.padding(10)
                            if index < store.state.services.count - 1 { Divider() }
                        }
                    }
                }
            }
        }
        .sheet(isPresented: $showAdd) { AddServiceSheet().environmentObject(store) }
        .alert("移除此服务？", isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } })) {
            Button("取消", role: .cancel) { removing = nil }
            Button("移除服务", role: .destructive) {
                if let mapping = removing { Task { await store.perform("serviceRemove", params: ["port": mapping.port]) } }
                removing = nil
            }
        } message: { Text("其他设备将无法通过私网端口 \(removing?.port ?? 0) 访问此服务。本机服务本身会继续运行。") }
    }
}

struct AddServiceSheet: View {
    @EnvironmentObject var store: AppStore
    @Environment(\.dismiss) private var dismiss
    @State private var meshPort = ""
    @State private var localPort = ""
    @State private var validationError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 19) {
            Text("开放本机服务").font(.title2.weight(.semibold))
            Text("把这台 Mac 上的一个 TCP 服务开放给已配对设备。").font(.callout).foregroundStyle(.secondary)
            Form {
                LabeledContent("协议", value: "TCP")
                TextField("私网访问端口", text: $meshPort, prompt: Text("例如 22 或 8080"))
                HStack {
                    Text("127.0.0.1:").font(.system(.body, design: .monospaced)).foregroundStyle(.secondary)
                    TextField("本机服务端口", text: $localPort, prompt: Text("例如 22 或 8080"))
                }
            }
            Text("请先在本机启动对应服务。添加端口不会开启系统远程登录或启动 Web 服务。8443 用于文件共享，不能添加为自定义服务。")
                .font(.caption).foregroundStyle(.secondary)
            if let error = validationError ?? store.operationError { ErrorBanner(message: error) }
            HStack {
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("添加服务") { add() }.buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                    .disabled(store.busy || !store.backendAvailable)
            }
        }.padding(25).frame(width: 500).onAppear { store.operationError = nil }
    }

    private func add() {
        guard let external = Int(meshPort.trimmingCharacters(in: .whitespacesAndNewlines)),
              let local = Int(localPort.trimmingCharacters(in: .whitespacesAndNewlines)),
              (1...65535).contains(external), (1...65535).contains(local), external != 8443 else {
            validationError = "端口应为 1–65535 的整数，私网端口不能使用 8443。"
            return
        }
        validationError = nil
        Task {
            if await store.perform("serviceAdd", params: ["port": external, "network": "tcp", "target": "127.0.0.1:\(local)"]) { dismiss() }
        }
    }
}
