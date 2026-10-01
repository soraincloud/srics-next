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
    @Published var step = 0
    @Published var target = "local"
    @Published var password = ""
    @Published var info: RecoveryKeyInfo?
    @Published var busy = false
    @Published var failed = false
    @Published var message = ""
    var requiresConfirmation = false
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
        step = 1
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
                if action == "recovery-key-generate" { password = ""; step = 1; message = "恢复 JSON 已保存。请重新选择它，确认文件可读并启用恢复能力。" }
                else if action == "recovery-key-confirm" { step = 2; message = "恢复密钥和新备份均已验证。请离线保管恢复文件，之后可重新启动服务。" }
                else { step = info?.state == "verified" ? (requiresConfirmation ? 1 : 2) : info?.state == "exported" ? 1 : 0; message = "" }
            } catch { failed = true; message = error.localizedDescription }
            busy = false; process = nil; AppDelegate.recoveryBusy = false
        }
    }
}

struct RecoveryKeyView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: RecoveryKeyModel
    private let fixedTarget: Bool
    private let vaultConfigured: Bool
    private let onVerified: (() -> Void)?
    init(config: LocalConfig, target: String? = nil, vaultConfigured: Bool = true, onVerified: (() -> Void)? = nil) {
        let value = RecoveryKeyModel(config: config)
        value.requiresConfirmation = onVerified != nil
        if let target { value.target = target }
        _model = StateObject(wrappedValue: value)
        fixedTarget = target != nil; self.vaultConfigured = vaultConfigured; self.onVerified = onVerified
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "保存应急恢复钥匙", icon: "key.horizontal")
            Text("当机器与密码全部丢失时，用完整备份和这个 JSON 取回资料。它不用于日常登录或修改密码。")
                .font(.callout).foregroundStyle(GlobalPalette.muted)
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    if !fixedTarget {
                        GlobalCard("为哪个备份设置", icon: "externaldrive") {
                            Picker("备份位置", selection: $model.target) { Text("本地 / 独立硬盘").tag("local"); Text("云端").tag("cloud") }
                                .onChange(of: model.target) { _ in model.info = nil; model.password = ""; model.step = 0; model.perform("recovery-key-status") }
                            Text("每个备份仓库单独设置。请先通过备份向导完成一次备份。").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    if model.step == 0 {
                        GlobalCard("1 · 导出恢复 JSON", icon: "square.and.arrow.up") {
                            if vaultConfigured {
                                PasswordField(title: "验证现有保险库口令", text: $model.password, placeholder: "用于让应急钥匙也能恢复私密资料")
                                Text("只在这一步使用：先解锁现有私密密钥，再建立应急恢复方式。不会修改口令，也不会把口令写入 JSON。").font(.caption).foregroundStyle(.secondary)
                            } else {
                                Text("当前未启用私密区。这份 JSON 用于恢复普通资料；以后启用私密区，需重新生成并验证。")
                            }
                            Text("程序随机生成恢复钥匙，请另存到这台 Mac 以外的安全位置。不要放入资料目录或备份仓库。").font(.callout)
                            Button("生成并保存 JSON…") { model.generate() }.buttonStyle(RecoveryActionStyle())
                                .disabled(vaultConfigured && model.password.isEmpty)
                        }
                    } else if model.step == 1 {
                        GlobalCard("2 · 确认文件可用并启用", icon: "checkmark.shield") {
                            Text("重新选择刚保存的恢复 JSON。程序会验证它能解锁资料，创建一份支持这把钥匙的备份，并完整读取校验。")
                            Text("只有完成这一步，才能依靠这个文件恢复。中途失败可使用同一 JSON 重试，无需重新生成。").font(.caption).foregroundStyle(.secondary)
                            Button("选择已保存的 JSON，验证并启用…") { model.confirm() }.buttonStyle(RecoveryActionStyle())
                            Button("未保留导出的文件，重新生成") { model.step = 0; model.message = "" }
                        }
                    } else {
                        GlobalCard("恢复钥匙已验证", icon: "checkmark.shield") {
                            Text(model.info?.vaultPresent == true ? "可恢复普通资料与私密资料。" : "可恢复普通资料；尚未覆盖私密区。")
                            if let id = model.info?.id { Text("钥匙编号：\(id.prefix(16))").font(.caption.monospaced()).textSelection(.enabled) }
                            Text("独立保管此 JSON 和完整备份。恢复时无需原登录密码、保险库口令或日常备份钥匙；云端账号访问方式仍需另存。").font(.callout)
                            Text("只适用于标记支持这把钥匙的恢复点。更换仓库，或之后首次启用私密区，需要重新设置。").font(.caption).foregroundStyle(.secondary)
                            Button("重新验证已有 JSON…") { model.confirm() }
                            Button("生成另一份恢复钥匙…") { model.password = ""; model.step = 0; model.message = "" }
                        }
                    }
                }
            }.disabled(model.busy)
            if !model.message.isEmpty { Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : Color.secondary).textSelection(.enabled) }
            HStack {
                if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }
                Spacer()
                Button(model.step == 2 ? "完成" : "暂时关闭") {
                    model.password = ""
                    if model.step == 2 { onVerified?() }
                    dismiss()
                }.disabled(model.busy)
            }
        }.padding(24).frame(width: 720, height: 640).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
            .tint(GlobalPalette.ink).buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle())
            .interactiveDismissDisabled(model.busy).task { model.perform("recovery-key-status") }
            .onDisappear { model.password = "" }
    }
}
