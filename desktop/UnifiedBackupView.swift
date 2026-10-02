import SwiftUI
import AppKit

struct UnifiedCredentials: Encodable, Sendable {
    var accessKeyId = ""
    var secretAccessKey = ""
    var sessionToken = ""
}
struct UnifiedBackupRequest: Encodable, Sendable {
    var directory = ""
    var cloud: S3Connection? = nil
    var credentials = UnifiedCredentials()
    var file = ""
}
struct UnifiedBackupResult: Decodable, Sendable { var file: String; var bytes: Int64; var destinations: [String]; var warning: String? }
struct UnifiedBackupResponse: Decodable, Sendable { var config: LocalConfig; var result: UnifiedBackupResult? }

struct UnifiedBackupView: View {
    @Environment(\.dismiss) private var dismiss
    let config: LocalConfig
    let vaultConfigured: Bool
    var exportOnly = false
    let onSaved: () -> Void
    @State private var step = 0
    @State private var showKey = false
    @State private var directory = ""
    @State private var cloudEnabled = false
    @State private var connection = S3Connection()
    @State private var credentials = UnifiedCredentials()
    @State private var file = ""
    @State private var busy = false
    @State private var failed = false
    @State private var message = ""
    @State private var result: UnifiedBackupResult?
    @State private var command: RecoveryProcess?
    private func chooseDirectory() {
        let p = NSOpenPanel(); p.canChooseFiles = false; p.canChooseDirectories = true; p.canCreateDirectories = true
        p.prompt = "选择备份位置"; p.message = "可选择独立硬盘或 OneDrive 等网盘的同步目录。请将恢复 JSON 单独保管。"
        if p.runModal() == .OK, let url = p.url { directory = url.path }
    }
    private func chooseFile() {
        let p = NSSavePanel(); let date = DateFormatter(); date.dateFormat = "yyyyMMdd-HHmmss"
        p.nameFieldStringValue = "SRICS-\(date.string(from: Date())).sricsbackup"; p.prompt = "选择位置"
        p.message = "保存与自动备份相同格式的完整加密文件。不会覆盖已有备份。"
        if p.runModal() == .OK, let url = p.url { file = url.pathExtension == "sricsbackup" ? url.path : url.appendingPathExtension("sricsbackup").path }
    }
    private func run() {
        guard !busy else { return }
        let request = UnifiedBackupRequest(directory: directory, cloud: cloudEnabled ? connection : nil, credentials: credentials, file: exportOnly ? file : "")
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let action = exportOnly ? "unified-backup-run" : "unified-backup-configure"
        let process = RecoveryProcess(); command = process; busy = true; failed = false; AppDelegate.recoveryBusy = true
        message = !exportOnly && cloudEnabled ? "正在创建备份并上传，随后完整下载读回校验，请保持窗口打开…" : "正在创建备份并完整读取校验，请保持窗口打开…"
        Task {
            do {
                let response = try await Task.detached { try process.run(action, payload: payload, as: UnifiedBackupResponse.self) }.value
                result = response.result; step = 2; credentials = UnifiedCredentials()
                message = response.result?.warning ?? "备份已保存并通过完整读取校验。"
                onSaved()
                if let result, !result.file.hasPrefix("s3://") { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: result.file)]) }
            } catch { failed = true; message = error.localizedDescription; onSaved() }
            busy = false; command = nil; AppDelegate.recoveryBusy = false
        }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: exportOnly ? "另存一份备份" : "设置加密备份", icon: "externaldrive.badge.timemachine")
            if !exportOnly { Text(step == 0 ? "1 · 保存并验证恢复 JSON" : step == 1 ? (config.unified == nil ? "2 · 保存位置与首次备份" : "备份保存位置") : "3 · 设置完成").font(.callout).foregroundStyle(.secondary) }
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    if step == 0 && !exportOnly {
                        GlobalCard("先保管恢复钥匙", icon: "key.horizontal") {
                            Text("整个资料库只需一份恢复 JSON。它用于机器与密码全部丢失时解密备份，包括私密原文件。")
                            Text("只需设置一次；修改密码、更换保存位置不用重新生成。").font(.caption).foregroundStyle(.secondary)
                            Button("保存并验证恢复 JSON…") { showKey = true }.buttonStyle(RecoveryActionStyle())
                        }
                    } else if step < 2 {
                        GlobalCard("恢复 JSON 已验证", icon: "checkmark.shield") {
                            Text("继续保管原 JSON。备份时无需拿出它，也不需要输入保险库口令。")
                            if !exportOnly { Button("重新验证已有 JSON…") { showKey = true } }
                        }
                        GlobalCard(exportOnly ? "保存这一份备份" : "备份保存位置", icon: "externaldrive") {
                            if exportOnly {
                                HStack { Text(file.isEmpty ? "尚未选择位置" : file).font(.callout).lineLimit(3).textSelection(.enabled); Spacer(); Button("选择位置…") { chooseFile() } }
                            } else {
                                HStack { Text(directory.isEmpty ? "未选择本地位置" : directory).font(.callout).lineLimit(3).textSelection(.enabled); Spacer(); Button("选择目录…") { chooseDirectory() } }
                                if !directory.isEmpty { Button("仅使用云端保存") { directory = ""; cloudEnabled = true } }
                                Text("独立硬盘、OneDrive 同步目录和普通目录都保存相同的 .sricsbackup 文件。网盘同步完成后才有异地副本。").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        if !exportOnly {
                            GlobalCard("云端保存（可选）", icon: "cloud") {
                                Toggle("保存到 S3 兼容存储", isOn: $cloudEnabled)
                                if cloudEnabled {
                                    TextField("HTTPS Endpoint", text: $connection.endpoint)
                                    HStack { TextField("区域，例如 us-east-1", text: $connection.region); TextField("存储桶", text: $connection.bucket) }
                                    TextField("专用目录，例如 srics/backups", text: $connection.prefix)
                                    TextField("Access Key ID", text: $credentials.accessKeyId)
                                    PasswordField(title: "Secret Access Key", text: $credentials.secretAccessKey, placeholder: config.unified?.cloud == nil ? "云端账号提供的访问密钥" : "留空继续使用已保存的账号")
                                    DisclosureGroup("高级连接选项") {
                                        PasswordField(title: "Session Token（可选）", text: $credentials.sessionToken)
                                        Picker("寻址方式", selection: $connection.lookup) { Text("自动").tag("auto"); Text("Path").tag("path"); Text("DNS").tag("dns") }
                                        TextField("自定义 CA 文件（可选）", text: $connection.caFile)
                                    }
                                    Text("访问密钥由本机程序保管，不需要准备凭据 JSON。新备份使用专用目录，请不要填写旧版仓库的目录。").font(.caption).foregroundStyle(.secondary)
                                }
                            }
                        }
                        Text("每份备份包含六类资料、数据库、修订与回收站。文件不会二次转换；完整备份需要与资料大小相近的额外空间。").font(.caption).foregroundStyle(.secondary)
                    } else if let result {
                        GlobalCard("备份已完成", icon: "checkmark.circle") {
                            ForEach(result.destinations, id: \.self) { Text($0).font(.callout).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) }
                            Text(ByteCountFormatter.string(fromByteCount: result.bytes, countStyle: .file)).font(.caption).foregroundStyle(.secondary)
                            Text("恢复时选择一份完整 .sricsbackup 和原恢复 JSON。无需原机器、登录密码、保险库口令或本机配置。")
                        }
                    }
                }.disabled(busy)
            }
            if !message.isEmpty { Text(message).font(.callout).foregroundStyle(failed ? Color.red : Color.secondary).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) }
            HStack {
                if busy { ProgressView().controlSize(.small); Button("取消任务") { command?.cancel() } }
                Spacer()
                Button(step == 2 ? "完成" : "关闭") { credentials = UnifiedCredentials(); dismiss() }.disabled(busy)
                if step == 1 || (exportOnly && step < 2) {
                    Button(exportOnly ? "创建并保存备份" : "保存设置并创建备份") { run() }.buttonStyle(RecoveryActionStyle())
                        .disabled(busy || (exportOnly ? file.isEmpty : directory.isEmpty && !cloudEnabled))
                }
            }
        }.padding(24).frame(width: 720, height: 700).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle()).tint(GlobalPalette.ink).interactiveDismissDisabled(busy)
        .sheet(isPresented: $showKey) { RecoveryKeyView(config: config, target: "unified", vaultConfigured: vaultConfigured) { step = 1; message = "" } }
        .task {
            directory = config.unified?.directory ?? ""; cloudEnabled = config.unified?.cloud != nil
            connection = config.unified?.cloud ?? S3Connection(); if config.unified == nil { connection.prefix = "srics/backups" }
            if exportOnly { step = 1; return }
            do {
                let payload = try JSONEncoder().encode(RecoveryKeyRequest(target: "unified", file: "", vaultPassword: ""))
                let response = try await Task.detached { try RecoveryProcess().run("unified-key-status", payload: payload, as: RecoveryKeyResponse.self) }.value
                if response.info?.state == "verified" { step = 1 }
            } catch { failed = true; message = error.localizedDescription }
        }
        .onDisappear { credentials = UnifiedCredentials() }
    }
}
