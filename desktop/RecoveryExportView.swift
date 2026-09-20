import SwiftUI
import AppKit

struct RecoveredItem: Decodable, Identifiable, Sendable {
    var id: String
    var name: String
    var module: String
    var `private`: Bool
    var category: String { ["comics":"漫画", "novels":"小说", "images":"图片", "photos":"个人照片", "private":"私密照片", "files":"个人文件"][module] ?? module }
}
@MainActor final class RecoveryExportModel: ObservableObject {
    @Published var items: [RecoveredItem] = []
    @Published var selected: String?
    @Published var password = ""
    @Published var filter = ""
    @Published var busy = false
    @Published var message = ""
    @Published var failed = false
    let source: RecoverySource
    let directory: String
    private var process: RecoveryProcess?
    init(source: RecoverySource, directory: String) { self.source = source; self.directory = directory }
    var filtered: [RecoveredItem] { items.filter { filter.isEmpty || $0.name.localizedCaseInsensitiveContains(filter) } }
    func load() { perform(item: nil, destination: "") }
    func cancel() { process?.cancel() }
    func export() {
        guard let item = items.first(where: { $0.id == selected }) else { return }
        let panel = NSOpenPanel(); panel.canChooseFiles = false; panel.canChooseDirectories = true; panel.canCreateDirectories = true; panel.allowsMultipleSelection = false
        panel.prompt = "取回到此目录"; panel.message = item.private ? "将解密这项文件并保存到所选位置，请选择私密的存放位置。" : "将保存一份文件副本；漫画为 ZIP，小说为 TXT。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        perform(item: item, destination: url.path)
    }
    private func perform(item: RecoveredItem?, destination: String) {
        guard !busy else { return }
        let request = RecoveryRequest(source: source, directory: directory, vaultPassword: password, itemID: item?.id ?? "", private: item?.private ?? false, exportDirectory: destination)
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let command = RecoveryProcess(); process = command; busy = true; AppDelegate.recoveryBusy = true
        failed = false; message = item == nil ? "正在校验并读取目录…" : "正在校验并取回文件…"
        Task {
            do {
                let response = try await Task.detached { try command.run(item == nil ? "recovery-items" : "recovery-export", payload: payload, as: RecoveryResponse.self) }.value
                if let path = response.exported { message = "已取回：\(path)"; NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)]) }
                else { items = response.items ?? []; selected = nil; message = "已读取 \(items.count) 项。" }
            } catch { message = error.localizedDescription; failed = true }
            process = nil; busy = false; AppDelegate.recoveryBusy = false
        }
    }
}
struct RecoveryExportView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: RecoveryExportModel
    let hasVault: Bool
    init(source: RecoverySource, directory: String, hasVault: Bool) { _model = StateObject(wrappedValue: RecoveryExportModel(source: source, directory: directory)); self.hasVault = hasVault }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("取回单项文件").font(.title2.weight(.semibold))
            Text("从已校验的恢复副本取回文件，当前资料库保持不变。").foregroundStyle(.secondary)
            if hasVault { HStack { SecureField("备份时的保险库口令（读取私密文件）", text: $model.password); Button("读取私密目录") { model.load() } }.disabled(model.busy) }
            TextField("按名称筛选", text: $model.filter).textFieldStyle(.roundedBorder)
            List(model.filtered, selection: $model.selected) { item in HStack { Text(item.name); Spacer(); Text(item.category).foregroundStyle(.secondary) }.tag(item.id) }.disabled(model.busy)
            Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : Color.secondary).textSelection(.enabled)
            HStack { if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }; Spacer(); Button("关闭") { model.password = ""; dismiss() }.disabled(model.busy); Button("取回所选文件…") { model.export() }.buttonStyle(RecoveryActionStyle()).disabled(model.busy || model.selected == nil) }
        }.padding(24).frame(width: 620, height: 560).interactiveDismissDisabled(model.busy).task { model.load() }
    }
}
