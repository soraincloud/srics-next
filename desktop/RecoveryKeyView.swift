import SwiftUI
import AppKit

struct RecoveryKeyInfo: Decodable, Sendable {
    let id: String
    let repositoryID: String
    let state: String
    let createdAt: String
    let verifiedAt: String?
    let snapshot: String?
    let vaultPresent: Bool
    var label: String { state == "verified" ? "已验证 · 可用于恢复" : state == "outdated" ? "配置已变化 · 需要重新验证或生成" : "已导出 · 等待验证与备份" }
}
struct RecoveryKeyResponse: Decodable, Sendable { var info: RecoveryKeyInfo?; var file: String? }
struct RecoveryKeyRequest: Encodable, Sendable { let target: String; let file: String; let vaultPassword: String }

@MainActor final class RecoveryKeyModel: ObservableObject {
    @Published var target = "local"
    @Published var password = ""
    @Published var info: RecoveryKeyInfo?
    @Published var busy = false
    @Published var failed = false
    @Published var message = ""
    private var process: RecoveryProcess?
    init(config: LocalConfig) { if config.backupRepository.isEmpty && config.cloud.enabled { target = "cloud" } }
    func cancel() { process?.cancel() }
    func generate() {
        let panel = NSSavePanel()
        panel.title = "导出恢复密钥"; panel.nameFieldStringValue = "SRICS-恢复密钥-\(target)-\(UUID().uuidString.prefix(8)).json"
        panel.message = "另存到安全位置，随后重新选择文件验证。持有此文件和备份即可读取资料。"
        panel.canCreateDirectories = true
        guard panel.runModal() == .OK, let url = panel.url else { return }
        perform("recovery-key-generate", file: url.path)
    }
    func confirm() {
        let panel = NSOpenPanel(); panel.canChooseFiles = true; panel.canChooseDirectories = false; panel.allowsMultipleSelection = false
        panel.prompt = "验证并备份"; panel.message = "重新选择已保存的恢复密钥文件。会验证密钥、创建新备份并完整读取校验。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        perform("recovery-key-confirm", file: url.path)
    }
    func perform(_ action: String, file: String = "") {
        guard !busy else { return }
        let request = RecoveryKeyRequest(target: target, file: file, vaultPassword: action == "recovery-key-generate" ? password : "")
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let command = RecoveryProcess(); process = command; busy = true; failed = false; AppDelegate.recoveryBusy = true
        message = action == "recovery-key-confirm" ? "正在验证密钥并创建备份，随后会完整读取校验。请保持窗口打开…" : action == "recovery-key-generate" ? "正在校验当前口令并导出恢复文件…" : "正在读取状态…"
        Task {
            do {
                let response = try await Task.detached { try command.run(action, payload: payload, as: RecoveryKeyResponse.self) }.value
                info = response.info
                if action == "recovery-key-generate" { password = ""; message = "已导出。请点击“选择恢复文件并验证”，完成新备份后启用。" }
                else if action == "recovery-key-confirm" { message = "恢复密钥和新备份均已验证。请离线保管恢复文件，之后可重新启动服务。" }
                else { message = info == nil ? "此备份目标尚未设置恢复密钥。" : "" }
            } catch { failed = true; message = error.localizedDescription }
            busy = false; process = nil; AppDelegate.recoveryBusy = false
        }
    }
}

struct RecoveryKeyView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: RecoveryKeyModel
    init(config: LocalConfig) { _model = StateObject(wrappedValue: RecoveryKeyModel(config: config)) }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "备份应急恢复密钥", icon: "key.horizontal")
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    GlobalCard("备份目标", icon: "externaldrive") {
                        Picker("设置范围", selection: $model.target) { Text("本地 / 独立硬盘").tag("local"); Text("云端 S3").tag("cloud") }
                            .onChange(of: model.target) { _ in model.info = nil; model.perform("recovery-key-status") }
                        Text("每个备份仓库单独设置。请先保存配置并完成一次普通备份。").font(.caption).foregroundStyle(.secondary)
                    }
                    if let info = model.info {
                        GlobalCard("最近的密钥记录", icon: "checkmark.shield") {
                            Text(info.label).font(.headline)
                            Text("编号：\(info.id.prefix(16))").font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                            Text(info.vaultPresent ? "覆盖普通资料与私密区" : "覆盖普通资料；以后启用私密区需重新生成密钥").font(.caption)
                            if let snapshot = info.snapshot { Text("已验证恢复点：\(snapshot.prefix(16))").font(.caption).textSelection(.enabled) }
                            if let date = info.verifiedAt { Text("最近验证：\(date)").font(.caption).foregroundStyle(.secondary) }
                        }
                    }
                    GlobalCard("生成与验证", icon: "key.horizontal") {
                        PasswordField(title: "当前保险库口令", text: $model.password, placeholder: "未启用私密区可留空")
                        Text("当前保险库口令仅用于建立私密资料的备份恢复能力，不会存入 JSON。备份口令从已保存配置读取；应急密钥独立随机生成，不修改任何日常密码。").font(.caption).foregroundStyle(.secondary)
                        HStack {
                            Button("1 · 生成并导出…") { model.generate() }
                            Button("2 · 选择恢复文件并验证…") { model.confirm() }.buttonStyle(RecoveryActionStyle())
                        }
                    }
                    Text("恢复文件不要放在资料库或云端备份目录里。建议另存离线副本；也可以将 JSON 文件打印保管。云端账户凭据仍需另外保存。更换备份仓库后需重新设置；恢复时只选择标记支持此密钥的恢复点。").font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            }.disabled(model.busy)
            Divider()
            if !model.message.isEmpty { Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : Color.secondary).textSelection(.enabled) }
            HStack {
                if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }
                Spacer()
                Button("关闭") { model.password = ""; dismiss() }.disabled(model.busy)
            }
        }.padding(24).frame(width: 720, height: 660).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
            .tint(GlobalPalette.ink).buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle())
            .interactiveDismissDisabled(model.busy).task { model.perform("recovery-key-status") }
    }
}
