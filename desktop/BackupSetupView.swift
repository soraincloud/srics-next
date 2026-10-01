import SwiftUI
import AppKit

enum BackupSetupTarget: String, Identifiable {
    case local, cloud
    var id: String { rawValue }
}

struct BackupCredentials: Encodable, Sendable {
    var accessKeyId: String
    var secretAccessKey: String
    var sessionToken: String
}
struct BackupSetupRequest: Encodable, Sendable {
    var target: String
    var mode: String
    var repository: String
    var cloud: CloudConfig
    var credentials: BackupCredentials?
    var password: String
    var passwordFile: String
}
struct BackupSetupResponse: Decodable, Sendable {
    var config: LocalConfig
    var snapshot: String?
}

@MainActor final class BackupSetupModel: ObservableObject {
    @Published var config: LocalConfig
    @Published var target: String
    @Published var mode: String
    @Published var step = 0
    @Published var busy = false
    @Published var prepared = false
    @Published var message = ""
    @Published var failed = false
    @Published var accessKey = ""
    @Published var secretKey = ""
    @Published var sessionToken = ""
    @Published var importCredentials = false
    @Published var oldPassword = ""
    @Published var oldPasswordFile = ""
    @Published var useOldFile = false
    private var process: RecoveryProcess?
    let original: LocalConfig
    init(config: LocalConfig, target: String) {
        self.config = config; self.original = config; self.target = target
        mode = (target == "local" ? !config.backupRepository.isEmpty : config.cloud.enabled) ? "current" : "new"
        if self.config.cloud.connection.prefix.isEmpty { self.config.cloud.connection.prefix = "srics/main" }
        if self.config.cloud.connection.lookup.isEmpty { self.config.cloud.connection.lookup = "auto" }
    }
    var configured: Bool { target == "local" ? !original.backupRepository.isEmpty : original.cloud.enabled }
    var destination: String { target == "local" ? config.backupRepository : "\(config.cloud.connection.bucket) / \(config.cloud.connection.prefix)" }
    func changeTarget() {
        config = original
        if config.cloud.connection.prefix.isEmpty { config.cloud.connection.prefix = "srics/main" }
        if config.cloud.connection.lookup.isEmpty { config.cloud.connection.lookup = "auto" }
        mode = configured ? "current" : "new"; message = ""; failed = false
    }
    func choose(_ kind: String) {
        let panel = NSOpenPanel(); panel.canChooseFiles = kind != "directory"; panel.canChooseDirectories = kind == "directory"
        panel.canCreateDirectories = kind == "directory"; panel.allowsMultipleSelection = false
        panel.prompt = "选择"
        panel.message = kind == "directory" ? "新建备份请选择独立磁盘上的空目录。" : kind == "credentials" ? "导入云服务商访问凭据 JSON。" : "选择旧版备份使用的口令文件，不是应急恢复 JSON。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        if kind == "directory" { config.backupRepository = url.path }
        else if kind == "credentials" { config.cloud.credentialsFile = url.path }
        else if kind == "ca" { config.cloud.connection.caFile = url.path }
        else { oldPasswordFile = url.path }
    }
    func advance() {
        message = ""; failed = false
        if mode != "current" {
            if target == "local" && config.backupRepository.isEmpty { message = "请选择备份目录。" }
            if target == "cloud" {
                if config.cloud.connection.endpoint.isEmpty || config.cloud.connection.bucket.isEmpty { message = "请填写云端服务地址和存储桶。" }
                else if importCredentials ? config.cloud.credentialsFile.isEmpty : (accessKey.isEmpty || secretKey.isEmpty) { message = "请填写云服务商的 Access Key ID 和 Secret Access Key，或导入已有凭据。" }
            }
            if mode == "existing" && (useOldFile ? oldPasswordFile.isEmpty : oldPassword.isEmpty) { message = "连接已有备份需要原备份加密密码或旧版口令文件。" }
        }
        guard message.isEmpty else { failed = true; return }
        step = 1
    }
    func runBackup() {
        guard !busy else { return }
        let request = BackupSetupRequest(target: target, mode: mode, repository: config.backupRepository, cloud: config.cloud,
            credentials: target == "cloud" && mode != "current" && !importCredentials ? BackupCredentials(accessKeyId: accessKey, secretAccessKey: secretKey, sessionToken: sessionToken) : nil,
            password: useOldFile ? "" : oldPassword, passwordFile: useOldFile ? oldPasswordFile : "")
        guard let payload = try? JSONEncoder().encode(request) else { return }
        let command = RecoveryProcess(); process = command; busy = true; failed = false; AppDelegate.recoveryBusy = true
        message = prepared ? "正在备份并完整读取校验，请保持窗口打开…" : "正在检查备份位置并保存加密设置…"
        Task {
            do {
                if !prepared {
                    let response = try await Task.detached { try command.run("backup-setup", payload: payload, as: BackupSetupResponse.self) }.value
                    config = response.config; prepared = true
                    oldPassword = ""; secretKey = ""; sessionToken = ""
                }
                message = "正在创建备份并完整读取校验。数据较多时需要一些时间…"
                let check = try JSONEncoder().encode(["target": target])
                _ = try await Task.detached { try command.run("backup-setup-check", payload: check, as: BackupSetupResponse.self) }.value
                message = ""; step = 2
            } catch { failed = true; message = error.localizedDescription }
            busy = false; process = nil; AppDelegate.recoveryBusy = false
        }
    }
    func cancel() { process?.cancel() }
}

struct BackupSetupView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: BackupSetupModel
    @State private var showKey = false
    let vaultConfigured: Bool
    let onClose: () -> Void
    init(config: LocalConfig, target: String, vaultConfigured: Bool, onClose: @escaping () -> Void) {
        _model = StateObject(wrappedValue: BackupSetupModel(config: config, target: target))
        self.vaultConfigured = vaultConfigured; self.onClose = onClose
    }
    private let steps = ["选择位置", "创建备份", "保存恢复钥匙", "完成"]
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "设置加密备份", icon: "shield.lefthalf.filled")
            HStack(spacing: 12) {
                ForEach(steps.indices, id: \.self) { i in
                    HStack(spacing: 7) {
                        Text(i < model.step ? "✓" : "\(i+1)").font(.caption.bold()).frame(width: 24, height: 24)
                            .background(Circle().fill(i == model.step ? GlobalPalette.ink : GlobalPalette.soft))
                            .foregroundStyle(i == model.step ? GlobalPalette.inverse : GlobalPalette.muted)
                        Text(steps[i]).font(.caption).foregroundStyle(i == model.step ? GlobalPalette.ink : GlobalPalette.muted)
                    }
                    if i < steps.count-1 { Spacer(minLength: 0) }
                }
            }.accessibilityElement(children: .combine)
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    if model.step == 0 { destinationForm }
                    if model.step == 1 {
                        GlobalCard("确认并创建备份", icon: "externaldrive.badge.timemachine") {
                            Text(model.destination).textSelection(.enabled)
                            Text(model.mode == "new" ? "程序会自动生成独立加密钥匙并保存在本机。无需输入、记住或创建额外的备份密码文件。" : "使用此仓库原有的加密钥匙，不会替换仓库或删除旧备份。")
                            Text("接下来会保存此备份配置，创建一份包含数据库和原文件的备份，并完整读取校验。私密原件保持加密。").foregroundStyle(GlobalPalette.muted)
                            if model.target == "cloud" { Text("云端完整校验会读取备份数据，可能产生流量费用。").font(.caption).foregroundStyle(GlobalPalette.muted) }
                        }
                    }
                    if model.step == 2 {
                        GlobalCard("备份已校验，再保存应急恢复钥匙", icon: "key.horizontal") {
                            Text("如果这台 Mac 和所有密码都丢失，完整备份与匹配的恢复 JSON 可以取回普通和私密资料。")
                            Text("JSON 请另存到这台 Mac 以外的安全位置。它和程序自动保存的日常备份钥匙用途不同。").foregroundStyle(GlobalPalette.muted)
                            Button("保存并验证恢复 JSON…") { showKey = true }.buttonStyle(RecoveryActionStyle())
                            Text("这里只能在恢复文件验证成功后完成设置；导出后中断，可再次进入继续验证。").font(.caption).foregroundStyle(GlobalPalette.muted)
                        }
                    }
                    if model.step == 3 {
                        GlobalCard("备份与应急恢复已就绪", icon: "checkmark.shield") {
                            Text(model.destination).textSelection(.enabled)
                            Text("备份已完成读取校验，恢复 JSON 已验证。日常备份钥匙由此 Mac 自动保管。")
                            Text("请独立保管恢复 JSON；云端备份还需保留云账号访问方式。只有已完成备份的内容才能恢复。").foregroundStyle(GlobalPalette.muted)
                            Text("关闭后可在“备份计划”中设置每天自动备份，再启动服务。").font(.caption).foregroundStyle(GlobalPalette.muted)
                        }
                    }
                }
            }.disabled(model.busy)
            if !model.message.isEmpty { Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : GlobalPalette.muted).textSelection(.enabled) }
            HStack {
                if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }
                else if model.step == 1 && !model.prepared { Button("上一步") { model.step = 0 } }
                Spacer()
                Button(model.step == 3 ? "完成" : "暂时关闭") { dismiss() }.disabled(model.busy)
                if model.step == 0 { Button("下一步") { model.advance() }.buttonStyle(RecoveryActionStyle()) }
                if model.step == 1 { Button(model.busy ? "正在处理…" : model.prepared ? "重试备份与校验" : "保存并创建备份") { model.runBackup() }.buttonStyle(RecoveryActionStyle()).disabled(model.busy) }
            }
        }.padding(24).frame(width: 760, height: 720)
            .background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink).tint(GlobalPalette.ink)
            .buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle())
            .interactiveDismissDisabled(model.busy)
            .onDisappear { model.oldPassword = ""; model.secretKey = ""; model.sessionToken = ""; onClose() }
            .sheet(isPresented: $showKey) {
                RecoveryKeyView(config: model.config, target: model.target, vaultConfigured: vaultConfigured) { model.step = 3 }
            }
    }
    @ViewBuilder private var destinationForm: some View {
        GlobalCard("备份放在哪里", icon: "externaldrive") {
            Picker("位置", selection: $model.target) { Text("云端").tag("cloud"); Text("本地 / 独立硬盘").tag("local") }
                .pickerStyle(.segmented).onChange(of: model.target) { _ in model.changeTarget() }
            Picker("设置方式", selection: $model.mode) {
                if model.configured { Text("使用已保存的设置").tag("current") }
                Text("新建加密备份").tag("new")
                Text("连接已有备份").tag("existing")
            }
            if model.mode == "current" {
                Text(model.destination).textSelection(.enabled)
                Text("沿用此 Mac 保存的钥匙，继续验证备份并设置应急恢复。").font(.caption).foregroundStyle(GlobalPalette.muted)
            } else if model.target == "local" {
                Text(model.mode == "new" ? "请选择空目录，建议位于独立硬盘。" : "选择原备份仓库目录。")
                HStack { TextField("备份目录", text: $model.config.backupRepository); Button("选择…") { model.choose("directory") } }
            } else {
                Text("使用已有的 S3 兼容存储桶；新备份请填写新的专用目录。程序不会创建存储桶。").font(.caption).foregroundStyle(GlobalPalette.muted)
                TextField("服务地址，例如 https://s3.example.com", text: $model.config.cloud.connection.endpoint)
                HStack { TextField("区域，例如 us-east-1", text: $model.config.cloud.connection.region); TextField("存储桶名称", text: $model.config.cloud.connection.bucket) }
                TextField("桶内目录，例如 srics/main", text: $model.config.cloud.connection.prefix)
                Toggle("导入已有云端凭据文件", isOn: $model.importCredentials)
                if model.importCredentials {
                    HStack { Text(model.config.cloud.credentialsFile.isEmpty ? "未选择凭据 JSON" : model.config.cloud.credentialsFile).lineLimit(2); Spacer(); Button("选择…") { model.choose("credentials") } }
                } else {
                    TextField("Access Key ID", text: $model.accessKey)
                    PasswordField(title: "Secret Access Key", text: $model.secretKey)
                    Text("以上由云服务商提供，用于访问存储桶，不是 SRICS 的登录密码或保险库口令。").font(.caption).foregroundStyle(GlobalPalette.muted)
                }
                DisclosureGroup("高级连接设置") {
                    Picker("寻址方式", selection: $model.config.cloud.connection.lookup) { Text("自动").tag("auto"); Text("Path").tag("path"); Text("DNS（OSS）").tag("dns") }
                    PasswordField(title: "Session Token（可选）", text: $model.sessionToken)
                    HStack { TextField("自定义 CA（可选）", text: $model.config.cloud.connection.caFile); Button("选择…") { model.choose("ca") } }
                }
            }
        }
        if model.mode == "existing" {
            GlobalCard("解锁已有备份", icon: "lock.open") {
                Text("已有仓库必须使用当初备份时的独立加密密码。不能使用登录密码或保险库口令。只有恢复 JSON 时，请退出此向导并使用“从备份恢复”。").font(.caption).foregroundStyle(GlobalPalette.muted)
                Toggle("使用旧版口令文件", isOn: $model.useOldFile)
                if model.useOldFile { HStack { Text(model.oldPasswordFile.isEmpty ? "尚未选择" : model.oldPasswordFile).lineLimit(2); Spacer(); Button("选择口令文件…") { model.choose("password") } } }
                else { PasswordField(title: "原备份加密密码", text: $model.oldPassword) }
            }
        } else if model.mode == "new" {
            Text("无需另设备份密码，程序自动生成并管理。最后一步会让你单独保存应急恢复 JSON。").font(.callout).foregroundStyle(GlobalPalette.muted)
        }
    }
}
