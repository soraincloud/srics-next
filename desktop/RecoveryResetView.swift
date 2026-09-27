import SwiftUI

struct RecoveryResetView: View {
    @Environment(\.dismiss) private var dismiss
    let source: RecoverySource
    let result: RecoveryResult
    let onReset: () -> Void
    @State private var password = ""
    @State private var repeated = ""
    @State private var vaultPassword = ""
    @State private var vaultRepeated = ""
    @State private var busy = false
    @State private var message = ""
    @State private var process: RecoveryProcess?
    private var valid: Bool {
        (12...72).contains(password.utf8.count) && password == repeated && (!result.vaultPresent || ((12...1024).contains(vaultPassword.utf8.count) && vaultPassword == vaultRepeated))
    }
    private func reset() {
        guard valid && !busy else { return }
        let request = RecoveryRequest(source: source, directory: result.directory, newPassword: password, newVaultPassword: vaultPassword)
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let command = RecoveryProcess(); process = command; busy = true; message = "正在验证恢复密钥并保存新密码…"; AppDelegate.recoveryBusy = true
        Task {
            do {
                let response = try await Task.detached { try command.run("recovery-reset-passwords", payload: payload, as: RecoveryResponse.self) }.value
                guard response.passwordsReset == true else { throw CommandError(message: "未确认密码已保存") }
                password = ""; repeated = ""; vaultPassword = ""; vaultRepeated = ""
                onReset(); busy = false; process = nil; AppDelegate.recoveryBusy = false; dismiss()
            } catch { message = error.localizedDescription; busy = false; process = nil; AppDelegate.recoveryBusy = false }
        }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            GlobalHeading(title: "设置恢复后的密码", icon: "lock.rotation")
            Text("使用恢复密钥验证身份，只修改已恢复的副本。原资料库和历史备份保持原样。").font(.callout).foregroundStyle(.secondary)
            SecureField("新登录密码（至少 12 字节）", text: $password)
            SecureField("再次输入登录密码", text: $repeated)
            if result.vaultPresent {
                Divider()
                SecureField("新私密区密码（至少 12 字节）", text: $vaultPassword)
                SecureField("再次输入私密区密码", text: $vaultRepeated)
            }
            Text("这不会撤销恢复密钥。云端凭据与日常备份口令仍需在本机配置中重新设置。").font(.caption).foregroundStyle(.secondary)
            if !message.isEmpty { Text(message).font(.callout).textSelection(.enabled) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.disabled(busy)
                Button("验证并保存密码") { reset() }.disabled(busy || !valid).buttonStyle(RecoveryActionStyle())
            }
        }.padding(24).frame(width: 600).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
            .buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle()).interactiveDismissDisabled(busy)
    }
}
