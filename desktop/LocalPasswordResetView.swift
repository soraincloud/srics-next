import SwiftUI
import AppKit
import UniformTypeIdentifiers

enum PasswordResetKind: String, Identifiable {
    case login, vault
    var id: String { rawValue }
}

struct LocalPasswordResetView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject var model: Launcher
    let kind: PasswordResetKind
    @State private var resetLogin = false
    @State private var resetVault = false
    @State private var password = ""
    @State private var repeated = ""
    @State private var vaultPassword = ""
    @State private var vaultRepeated = ""
    @State private var recoveryFile = ""
    @State private var errors: [String: String] = [:]
    @State private var busy = false
    @State private var message = ""

    private func chooseKey() {
        let panel = NSOpenPanel()
        panel.allowedContentTypes = [.json]
        panel.allowsMultipleSelection = false
        panel.canChooseDirectories = false
        panel.message = "选择为此资料库保存的恢复 JSON。"
        if panel.runModal() == .OK, let url = panel.url { recoveryFile = url.path; errors["recoveryKeyFile"] = nil }
    }
    private func reset() {
        guard !busy else { return }
        let settings = PasswordSettings(login: resetLogin ? password : "", loginConfirmation: resetLogin ? repeated : "", vault: resetVault ? vaultPassword : "", vaultConfirmation: resetVault ? vaultRepeated : "")
        var issues = settings.issues(loginExists: !resetLogin, vaultExists: false)
        if resetVault && vaultPassword.isEmpty { issues.append(.init(field: "vaultPassword", message: "请输入新的保险库口令。")) }
        if resetVault && recoveryFile.isEmpty { issues.append(.init(field: "recoveryKeyFile", message: "请选择此资料库的恢复 JSON。")) }
        errors = Dictionary(issues.map { ($0.field, $0.message) }, uniquingKeysWith: { first, _ in first })
        message = issues.first?.message ?? ""
        guard errors.isEmpty, resetLogin || resetVault else { return }
        struct Request: Encodable { let password: String; let vaultPassword: String; let recoveryKeyFile: String }
        do {
            let payload = try JSONEncoder().encode(Request(password: resetLogin ? password : "", vaultPassword: resetVault ? vaultPassword : "", recoveryKeyFile: resetVault ? recoveryFile : ""))
            busy = true; model.busy = true; AppDelegate.recoveryBusy = true
            message = "正在停止服务并重设密码…"
            Task {
                do {
                    let result = try await Task.detached { try runManager("reset-passwords", payload: payload) }.value
                    model.status = result
                    if resetLogin { model.password = ""; model.repeatedPassword = ""; model.currentPassword = "" }
                    if resetVault { model.vaultPassword = ""; model.repeatedVaultPassword = ""; model.currentVaultPassword = "" }
                    model.fieldErrors = [:]; model.invalidField = nil; model.failed = false
                    model.message = "\(resetLogin && resetVault ? "登录密码与保险库口令" : resetLogin ? "登录密码" : "保险库口令")已重设。点击“启动服务”后使用新密码。"
                    password = ""; repeated = ""; vaultPassword = ""; vaultRepeated = ""; recoveryFile = ""
                    busy = false; model.busy = false; AppDelegate.recoveryBusy = false
                    dismiss()
                } catch {
                    message = error.localizedDescription
                    if let field = (error as? CommandError)?.field { errors[field] = message }
                    if let status = try? await Task.detached(operation: { try runManager("info") }).value {
                        model.status = status
                        if !status.running { message += " 服务当前已停止，可修正后重试。" }
                    }
                    busy = false; model.busy = false; AppDelegate.recoveryBusy = false
                }
            }
        } catch { message = error.localizedDescription }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "重设密码", icon: "lock.rotation")
            Text("无需旧密码。重设会停止本机服务，并退出所有已登录设备。").font(.callout).foregroundStyle(GlobalPalette.muted)
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    Toggle("重设登录密码", isOn: $resetLogin)
                    if resetLogin {
                        PasswordField(title: "新登录密码", text: $password, placeholder: "至少 12 个字符，最多 72 字节", error: errors["password"])
                        PasswordField(title: "确认登录密码", text: $repeated, error: errors["repeatedPassword"])
                    }
                    if model.status?.vaultSet == true {
                        Divider()
                        Toggle("重设保险库口令", isOn: $resetVault)
                        if resetVault {
                            HStack(spacing: 12) {
                                VStack(alignment: .leading, spacing: 5) {
                                    Text("恢复 JSON").font(.system(size: 12, weight: .medium))
                                    Text(recoveryFile.isEmpty ? "尚未选择" : URL(fileURLWithPath: recoveryFile).lastPathComponent)
                                        .font(.caption).foregroundStyle(GlobalPalette.muted).lineLimit(1).truncationMode(.middle).help(recoveryFile)
                                }
                                Spacer()
                                Button(recoveryFile.isEmpty ? "选择文件…" : "更换…") { chooseKey() }
                            }
                            if let error = errors["recoveryKeyFile"] { Text(error).font(.caption).foregroundStyle(.red) }
                            PasswordField(title: "新保险库口令", text: $vaultPassword, placeholder: "12–1024 字节", error: errors["vaultPassword"])
                            PasswordField(title: "确认保险库口令", text: $vaultRepeated, error: errors["repeatedVaultPassword"])
                            Text("保留原加密密钥与全部私密资料。没有匹配的恢复 JSON 时，无法绕过旧口令解密。").font(.caption).foregroundStyle(GlobalPalette.muted)
                        }
                    }
                    Text("原文件与历史备份保持原样；恢复 JSON 继续有效。历史备份仍使用对应的旧口令或恢复 JSON。").font(.caption).foregroundStyle(GlobalPalette.muted)
                }.padding(1).disabled(busy)
            }.frame(maxHeight: 500)
            if !message.isEmpty { Text(message).font(.caption).foregroundStyle(busy ? GlobalPalette.muted : .red).fixedSize(horizontal: false, vertical: true) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.disabled(busy)
                Button("重设所选密码") { reset() }.buttonStyle(GlobalButtonStyle(primary: true)).disabled(busy || (!resetLogin && !resetVault))
            }
        }
        .padding(28).frame(width: 570).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .buttonStyle(GlobalButtonStyle()).toggleStyle(.switch).tint(GlobalPalette.ink)
        .interactiveDismissDisabled(busy)
        .onAppear { resetLogin = kind == .login; resetVault = kind == .vault }
    }
}
