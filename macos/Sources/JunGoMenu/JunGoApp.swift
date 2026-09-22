import AppKit
import SwiftUI

@main
@MainActor
struct JunGoMenuApp: App {
    @StateObject private var store: AppStore

    init() {
        let model = AppStore()
        _store = StateObject(wrappedValue: model)
        Task { await model.start() }
    }

    var body: some Scene {
        MenuBarExtra {
            MenuContent().environmentObject(store)
        } label: {
            Image(systemName: store.connected ? "link.circle.fill" : "link")
                .accessibilityLabel("军哥互联，\(store.statusTitle)")
        }
        .menuBarExtraStyle(.window)

        WindowGroup("军哥互联", id: "settings") {
            SettingsRoot().environmentObject(store)
        }
        .defaultSize(width: 860, height: 610)
        .windowResizability(.contentMinSize)
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

struct StatusDot: View {
    var active: Bool
    var body: some View { Circle().fill(active ? Color.green : Color.secondary.opacity(0.6)).frame(width: 8, height: 8) }
}

struct ErrorBanner: View {
    let message: String
    var body: some View {
        Label(message, systemImage: "exclamationmark.triangle")
            .font(.callout).foregroundStyle(.orange)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12).background(Color.orange.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
            .textSelection(.enabled)
    }
}

struct EmptyState: View {
    let symbol: String
    let title: String
    let description: String
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.system(size: 38, weight: .light)).foregroundStyle(.secondary)
            Text(title).font(.headline)
            Text(description).font(.callout).foregroundStyle(.secondary).multilineTextAlignment(.center)
        }.frame(maxWidth: .infinity).padding(30)
    }
}
