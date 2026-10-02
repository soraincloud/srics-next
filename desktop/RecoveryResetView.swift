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
        !password.contains("\n") && !password.contains("\r") && !vaultPassword.contains("\n") && !vaultPassword.contains("\r") && (12...72).contains(password.utf8.count) && password.utf8.elementsEqual(repeated.utf8) && (!result.vaultPresent || ((12...1024).contains(vaultPassword.utf8.count) && vaultPassword.utf8.elementsEqual(vaultRepeated.utf8)))
    }
    private func reset() {
        guard valid && !busy else { return }
        let request = RecoveryRequest(source: source, directory: result.directory, newPassword: password, newVaultPassword: vaultPassword)
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let command = RecoveryProcess(); process = command; busy = true; message = "正在解锁备份中的私密资料并配置恢复副本…"; AppDelegate.recoveryBusy = true
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
            GlobalHeading(title: "配置恢复后的资料库", icon: "lock.rotation")
            Text("为恢复后的资料库设置新的登录密码与保险库口令。原资料库不受影响；恢复 JSON 只用于从备份恢复资料。").font(.callout).foregroundStyle(.secondary)
            PasswordField(title: "新登录密码", text: $password, placeholder: "12–72 字节，用于浏览器登录", error: password.contains("\n") || password.contains("\r") ? "登录密码不能包含换行符。" : nil)
            PasswordField(title: "确认登录密码", text: $repeated, error: !repeated.isEmpty && !password.utf8.elementsEqual(repeated.utf8) ? "两次登录密码不一致。" : nil)
            if result.vaultPresent {
                Divider()
                PasswordField(title: "新保险库口令", text: $vaultPassword, placeholder: "12–1024 字节，用于解锁私密资料", error: vaultPassword.contains("\n") || vaultPassword.contains("\r") ? "保险库口令不能包含换行符。" : nil)
                PasswordField(title: "确认保险库口令", text: $vaultRepeated, error: !vaultRepeated.isEmpty && !vaultPassword.utf8.elementsEqual(vaultRepeated.utf8) ? "两次保险库口令不一致。" : nil)
            }
            Text("继续保管原恢复 JSON。启用恢复后的资料库后，重新选择备份位置；使用云端时重新填写云账号访问密钥，无需设置备份口令。").font(.caption).foregroundStyle(.secondary)
            if !message.isEmpty { Text(message).font(.callout).textSelection(.enabled) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.disabled(busy)
                Button("保存新资料库密码") { reset() }.disabled(busy || !valid).buttonStyle(RecoveryActionStyle())
            }
        }.padding(24).frame(width: 600).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
            .buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle()).interactiveDismissDisabled(busy)
    }
}
