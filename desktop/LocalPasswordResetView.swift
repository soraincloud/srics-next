import SwiftUI

struct LocalPasswordResetView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject var model: Launcher
    @State private var password = ""
    @State private var repeated = ""
    @State private var errors: [String: String] = [:]
    @State private var busy = false
    @State private var message = ""

    private func reset() {
        guard !busy else { return }
        let settings = PasswordSettings(login: password, loginConfirmation: repeated)
        let issues = settings.issues(loginExists: false, vaultExists: false)
        errors = Dictionary(issues.map { ($0.field, $0.message) }, uniquingKeysWith: { first, _ in first })
        message = issues.first?.message ?? ""
        guard errors.isEmpty else { return }
        do {
            let payload = try JSONEncoder().encode(["password": password])
            busy = true; model.busy = true; AppDelegate.recoveryBusy = true
            message = "正在停止服务并重设登录密码…"
            Task {
                do {
                    let result = try await Task.detached { try runManager("reset-passwords", payload: payload) }.value
                    model.status = result
                    model.password = ""; model.repeatedPassword = ""; model.currentPassword = ""
                    model.fieldErrors = [:]; model.invalidField = nil; model.failed = false
                    model.message = "登录密码已重设。点击“启动服务”后使用新登录密码。保险库口令保持不变。"
                    password = ""; repeated = ""
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
            GlobalHeading(title: "重设登录密码", icon: "lock.rotation")
            Text("无需旧登录密码。操作会停止本机服务，并退出所有已登录设备。").font(.callout).foregroundStyle(GlobalPalette.muted)
            VStack(alignment: .leading, spacing: 18) {
                PasswordField(title: "新登录密码", text: $password, placeholder: "至少 12 个字符，最多 72 字节", error: errors["password"])
                PasswordField(title: "确认登录密码", text: $repeated, error: errors["repeatedPassword"])
            }.disabled(busy)
            Text("此处仅重设浏览器登录密码，不修改保险库口令。").font(.caption).foregroundStyle(GlobalPalette.muted)
            if !message.isEmpty { Text(message).font(.caption).foregroundStyle(busy ? GlobalPalette.muted : .red).fixedSize(horizontal: false, vertical: true) }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button("取消") { dismiss() }.disabled(busy)
                Button("重设登录密码") { reset() }.buttonStyle(GlobalButtonStyle(primary: true)).disabled(busy)
            }
        }
        .padding(28).frame(width: 570).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .buttonStyle(GlobalButtonStyle()).interactiveDismissDisabled(busy)
    }
}
