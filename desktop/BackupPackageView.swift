import SwiftUI
import AppKit

struct BackupPackageRequest: Encodable, Sendable {
    var file: String
    var recoveryKeyFile: String
}
struct BackupPackageResponse: Decodable, Sendable {
    var file: String
    var snapshot: String
}

@MainActor final class BackupPackageModel: ObservableObject {
    @Published var file = ""
    @Published var recoveryKeyFile = ""
    @Published var busy = false
    @Published var failed = false
    @Published var message = ""
    @Published var exported = ""
    private var command: RecoveryProcess?
    func chooseFile() {
        let panel = NSSavePanel()
        let date = DateFormatter(); date.dateFormat = "yyyyMMdd-HHmmss"
        panel.nameFieldStringValue = "SRICS-\(date.string(from: Date())).sricsbackup"
        panel.title = "保存完整加密备份包"; panel.prompt = "选择位置"
        panel.message = "可选 OneDrive 同步目录。不会覆盖已有备份；请预留足够磁盘空间。"
        if panel.runModal() == .OK, let url = panel.url {
            file = url.pathExtension == "sricsbackup" ? url.path : url.appendingPathExtension("sricsbackup").path
        }
    }
    func chooseKey() {
        let panel = NSOpenPanel(); panel.canChooseDirectories = false; panel.allowsMultipleSelection = false
        panel.title = "选择已验证的恢复 JSON"; panel.prompt = "选择"
        if panel.runModal() == .OK, let url = panel.url { recoveryKeyFile = url.path }
    }
    func perform() {
        guard !busy, !file.isEmpty, !recoveryKeyFile.isEmpty,
              let payload = try? JSONEncoder().encode(BackupPackageRequest(file: file, recoveryKeyFile: recoveryKeyFile)) else { return }
        let process = RecoveryProcess(); command = process
        busy = true; failed = false; message = "正在校验恢复钥匙、创建最新备份并导出。完成前请保持窗口打开；大资料库可能需要较长时间。"
        AppDelegate.recoveryBusy = true
        Task {
            do {
                let response = try await Task.detached { try process.run("backup-package-export", payload: payload, as: BackupPackageResponse.self) }.value
                exported = response.file
                message = "已导出并通过文件读取校验。请等 OneDrive 上传完成，再将恢复 JSON 单独保存。"
                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: exported)])
            } catch { failed = true; message = error.localizedDescription }
            command = nil; busy = false; AppDelegate.recoveryBusy = false
        }
    }
    func cancel() { command?.cancel() }
}

struct BackupPackageView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model = BackupPackageModel()
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "导出完整加密备份包", icon: "shippingbox")
            Text("包含数据库、漫画、小说、图片、照片与私密原文件，以及此仓库已有的恢复点。文件内容和名称均保持加密，WebP 不会重新转换。")
                .foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if model.exported.isEmpty {
                GlobalCard("1 · 选择恢复钥匙", icon: "key.horizontal") {
                    Text("选择本地备份位置已验证的 JSON。它只用于确认这个包可以独立恢复，绝不会放进备份包。")
                    HStack { Text(model.recoveryKeyFile.isEmpty ? "尚未选择" : model.recoveryKeyFile).font(.callout).foregroundStyle(.secondary).lineLimit(2); Spacer(); Button("选择 JSON…") { model.chooseKey() } }
                }
                .disabled(model.busy)
                GlobalCard("2 · 保存备份包", icon: "externaldrive") {
                    Text("选择本地目录或 OneDrive 同步目录。也可以导出后手动上传；不需要 S3 配置。")
                    HStack { Text(model.file.isEmpty ? "尚未选择" : model.file).font(.callout).foregroundStyle(.secondary).lineLimit(2); Spacer(); Button("选择位置…") { model.chooseFile() } }
                    Text("包内保存加密仓库，不额外压缩。需要与现有加密仓库相近的可用空间。").font(.caption).foregroundStyle(.secondary)
                }
                .disabled(model.busy)
            } else {
                GlobalCard("备份包已保存", icon: "checkmark.circle") {
                    Text(model.exported).font(.callout).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    Text("将来安装 App → 从备份恢复 → 加密备份包，选择此包与匹配的恢复 JSON，即可恢复并校验全部资料。")
                    Button("在 Finder 中查看") { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: model.exported)]) }
                }
            }
            Spacer(minLength: 0)
            if !model.message.isEmpty { Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : Color.secondary).fixedSize(horizontal: false, vertical: true).textSelection(.enabled) }
            HStack {
                if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }
                Spacer()
                Button("关闭") { dismiss() }.disabled(model.busy)
                if model.exported.isEmpty { Button("创建备份并导出") { model.perform() }.buttonStyle(GlobalButtonStyle(primary: true)).disabled(model.busy || model.file.isEmpty || model.recoveryKeyFile.isEmpty) }
            }
        }.padding(24).frame(width: 700, height: 620).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .buttonStyle(GlobalButtonStyle()).interactiveDismissDisabled(model.busy)
    }
}
