import SwiftUI
import AppKit

struct S3Connection: Codable, Equatable, Sendable {
    var endpoint = ""
    var region = ""
    var bucket = ""
    var prefix = "srics/main"
    var lookup = "auto"
    var caFile = ""
}
struct CloudConfig: Codable, Equatable, Sendable {
    var enabled = false
    var connection = S3Connection()
    var credentialsFile = ""
    var passwordFile = ""
}
struct RetentionConfig: Codable, Equatable, Sendable { var enabled = false; var daily = 0; var monthly = 0 }
struct UnifiedConfig: Codable, Equatable, Sendable {
 var repository = ""
 var passwordFile = ""
 var directory = ""
 var cloud: S3Connection? = nil
 var credentialsFile: String? = nil
}
struct LocalConfig: Codable, Equatable, Sendable {
    var autoStart = false
    var autoRestart = false
    var trashDays = 0
    var retention = RetentionConfig()
    var data = ""
    var port = 19473
    var lanAddress = ""
    var backupRepository = ""
    var backupPasswordFile = ""
    var backupDailyAt = ""
    var unified: UnifiedConfig? = nil
    var cloud = CloudConfig()
}
struct ReleaseInfo: Codable, Sendable {
    var version: String
    var build: Int
    var label: String
    var commit: String
    var dirty: Bool
    var builtAt: String

    static let bundled: ReleaseInfo? = {
        guard let url = Bundle.main.url(forResource: "release", withExtension: "json"),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(ReleaseInfo.self, from: data)
    }()

    var codeLabel: String { (commit.isEmpty ? "未记录" : String(commit.prefix(12))) + (dirty ? "（含未提交修改）" : "") }
    var buildTimeLabel: String { builtAt.isEmpty ? "未记录" : builtAt.replacingOccurrences(of: "T", with: " ").replacingOccurrences(of: "Z", with: "") }
}
struct ServiceStatus: Codable, Sendable {
    var version: String
    var release: ReleaseInfo?
    var updateBackup: String
    var config: LocalConfig
    var saved: Bool
    var running: Bool
    var passwordSet: Bool
    var loginVerified: Bool?
    var vaultSet: Bool
    var vaultIdleMinutes: Int
    var url: String
    var log: String
    var lanAddresses: [String]
    var certificate: String
    var certFingerprint: String
    var networkError: String
    var dataError: String?
    var cloudCheck: String
}
struct ConfigureRequest: Encodable, Sendable {
    var config: LocalConfig
    var password: String
    var currentPassword: String
    var vaultPassword: String
    var currentVaultPassword: String
    var vaultIdleMinutes: Int
}
struct CommandError: LocalizedError, Sendable {
    let message: String
    var field: String? = nil
    var errorDescription: String? { message }
}
struct ManagerFailure: Decodable { let message: String; let field: String }

// Credentials travel over a private stdin pipe, never arguments, logs or preferences.
func runManager(_ action: String, payload: Data? = nil) throws -> ServiceStatus {
    guard let resources = Bundle.main.resourceURL else { throw CommandError(message: "程序包不完整") }
    let process = Process()
    process.executableURL = resources.appendingPathComponent("bin/srics")
    process.arguments = ["manager", action]
    if action == "start", payload != nil { process.arguments?.append("--verify-login") }
    let input = Pipe(), output = Pipe(), failure = Pipe()
    process.standardInput = input
    process.standardOutput = output
    process.standardError = failure
    try process.run()
    if let payload { try input.fileHandleForWriting.write(contentsOf: payload) }
    try input.fileHandleForWriting.close()
    let bytes = output.fileHandleForReading.readDataToEndOfFile()
    let errorBytes = failure.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else {
        if let failure = try? JSONDecoder().decode(ManagerFailure.self, from: errorBytes) {
            throw CommandError(message: failure.message, field: failure.field)
        }
        throw CommandError(message: String(data: errorBytes, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "srics: ", with: "") ?? "操作失败")
    }
    return try JSONDecoder().decode(ServiceStatus.self, from: bytes)
}

@MainActor final class Launcher: ObservableObject {
    @Published var config = LocalConfig()
    @Published var port = "19473"
    @Published var status: ServiceStatus?
    @Published var currentPassword = ""
    @Published var password = ""
    @Published var repeatedPassword = ""
    @Published var vaultPassword = ""
    @Published var repeatedVaultPassword = ""
    @Published var currentVaultPassword = ""
    @Published var vaultIdleMinutes = 10
    @Published var busy = false
    @Published var message = ""
    @Published var failed = false
    @Published var fieldErrors: [String: String] = [:]
    @Published var invalidField: String?
    @Published var validationAttempt = 0
    var running: Bool { status?.running == true }
    var saved: Bool { status?.saved == true }
    var passwordSet: Bool { status?.passwordSet == true }
    var passwords: PasswordSettings { PasswordSettings(current: currentPassword, login: password, loginConfirmation: repeatedPassword, currentVault: currentVaultPassword, vault: vaultPassword, vaultConfirmation: repeatedVaultPassword) }
    var changed: Bool { config != status?.config || port != String(status?.config.port ?? 19473) || passwords.hasDraft || vaultIdleMinutes != (status?.vaultIdleMinutes ?? 10) }
    func showIssues(_ issues: [PasswordIssue]) {
        fieldErrors = Dictionary(issues.map { ($0.field, $0.message) }, uniquingKeysWith: { first, _ in first })
        invalidField = issues.first?.field
        message = issues.first?.message ?? "请检查填写内容。"
        failed = true; validationAttempt += 1
    }

    func refresh() {
        guard !busy else { return }
        busy = true
        Task {
            do {
                let result = try await Task.detached { try runManager("info") }.value
                if status == nil || !changed { config = result.config; port = String(result.config.port); vaultIdleMinutes = result.vaultIdleMinutes }
                status = result
            } catch { message = error.localizedDescription; failed = true }
            busy = false
        }
    }
    private var updateProcess: RecoveryProcess?
    func cancelUpdate() { updateProcess?.cancel() }
    func perform(_ action: String, startAfter: Bool = false) {
        guard !busy else { return }
        var payload: Data?
        let loginToVerify = action == "configure" ? password : ""
        if action == "configure" {
            fieldErrors = [:]
            let issues = passwords.issues(loginExists: passwordSet, vaultExists: status?.vaultSet == true)
            guard issues.isEmpty else { showIssues(issues); return }
            guard let number = Int(port), (1024...65535).contains(number) else { showIssues([.init(field: "port", message: "服务端口需在 1024–65535 之间。")]); return }
            config.port = number
            do { payload = try JSONEncoder().encode(ConfigureRequest(config: config, password: password, currentPassword: currentPassword, vaultPassword: vaultPassword, currentVaultPassword: currentVaultPassword, vaultIdleMinutes: vaultIdleMinutes)) }
            catch { message = "配置无法读取"; failed = true; return }
        }
        busy = true; failed = false; message = action == "prepare-update" ? "正在停止服务、备份并保留旧程序，请等待完成…" : ""
        if action == "prepare-update" { AppDelegate.recoveryBusy = true }
        let request = payload
        let updater: RecoveryProcess? = action == "prepare-update" ? RecoveryProcess() : nil
        updateProcess = updater
        Task {
            do {
                var result = try await Task.detached { if let updater { return try updater.run(action, payload: Data(), as: ServiceStatus.self) }; return try runManager(action, payload: request) }.value
                status = result; config = result.config; port = String(result.config.port)
                if action == "configure" { password = ""; repeatedPassword = ""; currentPassword = ""; vaultPassword = ""; repeatedVaultPassword = ""; currentVaultPassword = "" }
                if startAfter {
                    let check = loginToVerify.isEmpty ? nil : try JSONEncoder().encode(["password": loginToVerify])
                    result = try await Task.detached { try runManager("start", payload: check) }.value; status = result
                    if !loginToVerify.isEmpty && result.loginVerified != true { throw CommandError(message: "服务未确认登录密码验证成功，请刷新状态后重试。") }
                }
                message = action == "stop" ? "服务已停止" : result.running ? "服务正在后台运行" : "配置已保存"
                if startAfter && !loginToVerify.isEmpty { message = "登录密码已保存并通过登录验证，服务正在后台运行。" }
                if action == "prepare-update" { message = "更新备份已校验，旧程序与记录保存在：\(result.updateBackup)。退出本窗口后替换 .app，再打开并启动。"; NSWorkspace.shared.open(URL(fileURLWithPath: result.updateBackup)) }
                if result.running && (action == "start" || startAfter), let url = URL(string: result.url) { NSWorkspace.shared.open(url) }
            } catch {
                message = error.localizedDescription; failed = true
                if let field = (error as? CommandError)?.field { showIssues([.init(field: field, message: error.localizedDescription)]) }
                if startAfter { status = try? await Task.detached { try runManager("info") }.value }
                if action == "prepare-update" { status = try? await Task.detached { try runManager("info") }.value }
            }
            updateProcess = nil; busy = false; if action == "prepare-update" { AppDelegate.recoveryBusy = false }
        }
    }
    func choose(_ field: String) {
        let panel = NSOpenPanel()
        panel.canChooseFiles = field != "data" && field != "backup"
        panel.canChooseDirectories = !panel.canChooseFiles
        panel.canCreateDirectories = field == "backup"
        panel.allowsMultipleSelection = false
        panel.prompt = "选择"
        panel.message = field == "data" ? "选择存放位置，将使用其中的 SRICS-library 子目录。" : field == "backup" ? "选择用于加密备份的目录。" : "选择仅本人可读的备份口令文件。"
        if field == "cloudCredentials" { panel.message = "选择云端凭据 JSON 文件，权限需为 600。" }
        if field == "cloudCA" { panel.message = "选择用于验证私有 S3 服务的 PEM CA 证书。" }
        guard panel.runModal() == .OK, let url = panel.url else { return }
        if field == "data" { config.data = url.appendingPathComponent("SRICS-library").path }
        else if field == "backup" { config.backupRepository = url.path }
        else if field == "cloudCredentials" { config.cloud.credentialsFile = url.path }
        else if field == "cloudPassword" { config.cloud.passwordFile = url.path }
        else if field == "cloudCA" { config.cloud.connection.caFile = url.path }
        else { config.backupPasswordFile = url.path }
    }
    func checkCloud() {
        guard !busy else { return }
        struct Request: Encodable { let config: LocalConfig }
        guard let payload = try? JSONEncoder().encode(Request(config: config)) else { return }
        busy = true; failed = false; message = "正在检查云端连接…"
        Task {
            do { let result = try await Task.detached { try runManager("cloud-check", payload: payload) }.value; message = result.cloudCheck }
            catch { message = error.localizedDescription; failed = true }
            busy = false
        }
    }
}

struct LauncherView: View {
    @StateObject private var model = Launcher()
    @State private var showRecovery = false
    @State private var showUnifiedBackup = false
    @State private var showRecoveryKey = false
    @State private var showBackupPackage = false
    @State private var backupSetupTarget: BackupSetupTarget?
    @State private var showMigration = false
    @State private var showLoginReset = false
    @State private var pane: LauncherPane = .service

    private var header: some View {
        HStack(spacing: 12) {
            Image("AppIcon").resizable().interpolation(.high)
                .frame(width: 42, height: 42).accessibilityHidden(true)
            Text("SRICS").font(.system(size: 23, weight: .heavy))
            Text("Next").font(.system(size: 12, weight: .semibold)).foregroundStyle(GlobalPalette.muted)
            Rectangle().fill(GlobalPalette.line).frame(width: 1, height: 22).padding(.horizontal, 8)
            Text("本机配置").font(.system(size: 13)).foregroundStyle(GlobalPalette.muted)
            Spacer()
            HStack(spacing: 7) {
                Circle().fill(model.running ? Color.green : GlobalPalette.muted).frame(width: 6, height: 6)
                Text(model.running ? "运行中" : model.saved ? "已停止" : "尚未配置").font(.system(size: 12, weight: .medium))
            }
            Button { model.refresh() } label: { Image(systemName: "arrow.clockwise") }
                .help("刷新状态").accessibilityLabel("刷新状态").disabled(model.busy)
        }.padding(.horizontal, 28).padding(.vertical, 20).background(GlobalPalette.surface)
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("工作区").font(.system(size: 11, weight: .semibold)).foregroundStyle(GlobalPalette.muted)
                .padding(.leading, 20).padding(.bottom, 12)
            ForEach(LauncherPane.allCases) { item in
                Button { pane = item } label: {
                    HStack(spacing: 10) {
                        Capsule().fill(pane == item ? GlobalPalette.ink : Color.clear).frame(width: 3, height: 16)
                        Image(systemName: item.icon).font(.system(size: 15)).frame(width: 20)
                        Text(item.rawValue).font(.system(size: 13, weight: pane == item ? .semibold : .medium))
                        Spacer(minLength: 0)
                    }
                    .foregroundStyle(pane == item ? GlobalPalette.ink : GlobalPalette.muted)
                    .padding(.horizontal, 17).frame(height: 44)
                    .background(Capsule().fill(pane == item ? GlobalPalette.soft : Color.clear))
                    .contentShape(Capsule())
                }.buttonStyle(.plain).accessibilityAddTraits(pane == item ? [.isSelected] : [])
            }
            Spacer()
            Divider().padding(.vertical, 12)
            Text(ReleaseInfo.bundled?.label ?? model.status?.release?.label ?? "版本信息不可用").font(.system(size: 11, weight: .medium))
                .foregroundStyle(GlobalPalette.muted).padding(.leading, 20)
        }.padding(.horizontal, 14).padding(.vertical, 28).frame(width: 204).background(GlobalPalette.surface)
    }

    @ViewBuilder private var sections: some View {
        if pane == .service {
            GlobalCard("服务", icon: "externaldrive") {
                LabeledContent("资料目录") {
                    HStack { TextField("目录路径", text: $model.config.data).textFieldStyle(GlobalTextFieldStyle()).labelsHidden()
                        Button("选择…") { model.choose("data") }
                    }.disabled(model.saved)
                }.accessibilityElement(children: .contain)
                LabeledContent("端口") { TextField("19473", text: $model.port).frame(width: 95).textFieldStyle(GlobalTextFieldStyle()).labelsHidden() }.id("port")
                if let error = model.fieldErrors["port"] { Text(error).font(.caption).foregroundStyle(.red) }
                Picker("访问范围", selection: $model.config.lanAddress) {
                    Text("仅本机").tag("")
                    ForEach(Array(Set((model.status?.lanAddresses ?? []) + (model.config.lanAddress.isEmpty ? [] : [model.config.lanAddress]))).sorted(), id: \.self) { address in
                        Text("局域网 · \(address)").tag(address)
                    }
                }
                if !model.config.lanAddress.isEmpty {
                    Text("局域网使用 HTTPS。保存后导出公共证书，在访问设备上安装并信任；建议在路由器中固定此 IP。").font(.caption).foregroundStyle(.secondary)
                }
                Toggle("登录电脑后自动启动", isOn: $model.config.autoStart)
                Toggle("异常退出后自动重启", isOn: $model.config.autoRestart)
                if model.saved { Text("更换资料位置，请使用下方的“迁移资料目录”。").font(.caption).foregroundStyle(.secondary) }
            }.disabled(model.busy || model.running)
        }
        if pane == .service {
            GlobalCard("迁移与换设备", icon: "externaldrive.badge.timemachine") {
                if model.saved {
                    Button("迁移资料目录…") { showMigration = true }.disabled(model.busy || model.changed)
                    Text(model.changed ? "请先保存当前更改，再迁移资料目录。" : "将完整资料库复制到新位置，校验后切换。原目录保留。").font(.caption).foregroundStyle(.secondary)
                    Divider()
                }
                Text("换到新 Mac：旧机停止写入后完成最终备份。在新机安装 App，用一份完整备份文件与恢复 JSON 恢复。云端文件先下载完整。").font(.callout)
                Button("从备份迁入这台 Mac…") { showRecovery = true }.disabled(model.busy || (model.saved && model.changed))
                Text("资料和密码随备份恢复。新机需重新设置备份路径、云端凭据和局域网 HTTPS；确认可用前保留旧机资料。").font(.caption).foregroundStyle(.secondary)
            }
        }
        if pane == .security && model.passwordSet {
            GlobalCard("忘记登录密码", icon: "lock.rotation") {
                Button("重设登录密码…") { showLoginReset = true }
                Text("在本机重新设置浏览器登录密码，不影响保险库口令。").font(.caption).foregroundStyle(.secondary)
            }.disabled(model.busy)
        }
        if pane == .security {
            GlobalCard(model.passwordSet ? "修改登录密码（可选）" : "登录密码", icon: "key") {
                Text("用于浏览器登录资料库；保险库口令不用于此处登录。").font(.caption).foregroundStyle(.secondary)
                if model.passwordSet { PasswordField(title: "当前登录密码", text: $model.currentPassword, error: model.fieldErrors["currentPassword"]).id("currentPassword") }
                PasswordField(title: model.passwordSet ? "新登录密码" : "登录密码", text: $model.password, placeholder: model.passwordSet ? "留空则保留原密码" : "至少 12 个字符，最多 72 字节", error: model.fieldErrors["password"]).id("password")
                PasswordField(title: "确认登录密码", text: $model.repeatedPassword, placeholder: "再次输入登录密码", error: model.fieldErrors["repeatedPassword"]).id("repeatedPassword")
            }.disabled(model.busy || model.running)
        }
        if pane == .security {
            GlobalCard(model.status?.vaultSet == true ? "修改保险库口令（可选）" : "设置保险库口令（可选）", icon: "lock.shield") {
                if model.status?.vaultSet == true { PasswordField(title: "当前保险库口令", text: $model.currentVaultPassword, error: model.fieldErrors["currentVaultPassword"]).id("currentVaultPassword") }
                PasswordField(title: model.status?.vaultSet == true ? "新保险库口令" : "保险库口令", text: $model.vaultPassword, placeholder: model.status?.vaultSet == true ? "留空则保留原口令" : "可选，12–1024 字节", error: model.fieldErrors["vaultPassword"]).id("vaultPassword")
                PasswordField(title: "确认保险库口令", text: $model.repeatedVaultPassword, placeholder: "再次输入保险库口令", error: model.fieldErrors["repeatedVaultPassword"]).id("repeatedVaultPassword")
                Stepper("闲置 \(model.vaultIdleMinutes) 分钟后锁定", value: $model.vaultIdleMinutes, in: 1...60)
                Text(model.status?.vaultSet == true ? "修改需验证当前保险库口令。忘记口令时，请通过独立的“从备份恢复”流程取回资料；只能取回已完成备份的内容。" : "用于日常解锁私密照片与个人文件，请独立设置并保管。").font(.caption).foregroundStyle(.secondary)
            }.disabled(model.busy || model.running)
        }
        if pane == .backup {
            GlobalCard("加密备份", icon: "externaldrive.badge.timemachine") {
                Text("所有备份都保存为 .sricsbackup 文件。恢复时，只需一份完整备份和资料库的恢复 JSON。")
                if let unified = model.config.unified {
                    if !unified.directory.isEmpty { LabeledContent("保存位置", value: unified.directory).textSelection(.enabled) }
                    if let cloud = unified.cloud { LabeledContent("云端位置", value: "\(cloud.bucket) / \(cloud.prefix)") }
                } else { Text("首次设置：保存恢复 JSON → 验证文件 → 选择位置 → 创建第一份备份。").font(.callout).foregroundStyle(.secondary) }
                HStack {
                    Button(model.config.unified == nil ? "开始设置…" : "管理备份…") { showUnifiedBackup = true }
                    if model.config.unified != nil { Button("另存一份备份…") { showBackupPackage = true } }
                }.disabled(!model.saved || model.changed || model.running || model.busy)
                if model.running { Text("网页中可直接点击立即备份。修改位置或另存文件前，请先停止服务。").font(.caption).foregroundStyle(.secondary) }
                else if !model.saved || model.changed { Text("请先保存当前资料库配置。").font(.caption).foregroundStyle(.secondary) }
                Text("登录密码用于网页，保险库口令用于私密资料；恢复 JSON 单独离线保管，只用于灾难恢复。").font(.caption).foregroundStyle(.secondary)
            }
            GlobalCard("自动备份", icon: "clock") {
                Toggle("每天自动备份", isOn: Binding(get: { !model.config.backupDailyAt.isEmpty }, set: { model.config.backupDailyAt = $0 ? "03:00" : "" }))
                    .disabled(model.config.unified == nil)
                if !model.config.backupDailyAt.isEmpty {
                    LabeledContent("每天执行时间") { TextField("03:00", text: $model.config.backupDailyAt).frame(width: 95).labelsHidden() }
                    Text("使用本机时区。漏跑会补做，失败每小时重试。每天产出同一种备份文件，无需输入密码或选择 JSON。").font(.caption).foregroundStyle(.secondary)
                }
                Text("新备份会完整读取校验后才显示成功。保留所有日期版本；完整备份需要额外空间。").font(.caption).foregroundStyle(.secondary)
            }.disabled(model.busy || model.running)
            if model.config.unified == nil && (!model.config.backupRepository.isEmpty || model.config.cloud.enabled) {
                DisclosureGroup("旧版备份设置") {
                    Text("已有仓库和旧 JSON 会保留。完成新设置后，自动备份改用统一格式；旧备份从恢复窗口的兼容入口读取。").font(.caption).foregroundStyle(.secondary)
                    HStack { Button("旧本地仓库…") { backupSetupTarget = .local }; Button("旧云端仓库…") { backupSetupTarget = .cloud }; Button("旧恢复 JSON…") { showRecoveryKey = true } }
                }.disabled(model.running || model.busy || model.changed)
            }
        }
        if pane == .storage {
            GlobalCard("空间管理", icon: "internaldrive") {
                Toggle("自动清理回收站", isOn: Binding(get: { model.config.trashDays > 0 }, set: { model.config.trashDays = $0 ? 30 : 0 }))
                if model.config.trashDays > 0 { Stepper("回收站保留 \(model.config.trashDays) 天", value: $model.config.trashDays, in: 1...3650); Text("到期后永久删除。私密资料在解锁后清理，历史备份独立保留。").font(.caption).foregroundStyle(.secondary) }
                if model.config.unified == nil { Toggle("清理过期历史备份", isOn: Binding(get: { model.config.retention.enabled }, set: { model.config.retention.enabled = $0; if $0 && model.config.retention.daily == 0 { model.config.retention.daily = 30; model.config.retention.monthly = 12 } }))
                if model.config.retention.enabled {
                    Stepper("每日版本：\(model.config.retention.daily)", value: $model.config.retention.daily, in: 1...3650)
                    Stepper("月度版本：\(model.config.retention.monthly)", value: $model.config.retention.monthly, in: 0...120)
                    Text("按 UTC 日/月保留最新版本；新备份通过校验后永久删除多余快照。至少保留最新一份。网页可预览清理内容。").font(.caption).foregroundStyle(.secondary)
                }
                } else { Text("备份文件保留全部日期版本。确认有可用备份后，可在保存位置手动整理旧文件。").font(.caption).foregroundStyle(.secondary) }
            }.disabled(model.busy || model.running)
        }
        if pane == .updates {
            GlobalCard("版本与更新", icon: "square.and.arrow.down") {
                if let release = ReleaseInfo.bundled ?? model.status?.release {
                    LabeledContent("当前版本", value: "v\(release.version)")
                    LabeledContent("构建编号", value: "Build \(release.build)")
                    LabeledContent("代码版本", value: release.codeLabel).textSelection(.enabled)
                    LabeledContent("构建时间（UTC）", value: release.buildTimeLabel).textSelection(.enabled)
                }
                Button("停止服务并准备更新") { model.perform("prepare-update") }.disabled(model.busy || !model.saved)
                if model.busy && AppDelegate.recoveryBusy { Button("取消更新准备") { model.cancelUpdate() } }
                Text("先完成加密备份并保留旧程序，再退出、替换 .app 并重新启动；失败时不替换程序。需要已完成的备份设置。").font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 12) {

            if let error = model.status?.dataError, !error.isEmpty {
                Text(error).font(.callout).foregroundStyle(.red).textSelection(.enabled)
            }
            if let status = model.status, !status.networkError.isEmpty {
                Text(status.networkError).font(.caption).foregroundStyle(.red)
            }
            if let status = model.status, !status.certificate.isEmpty {
                HStack {
                    Button("导出 HTTPS 公共证书…") {
                        let panel = NSSavePanel()
                        panel.nameFieldStringValue = "SRICS-Local-CA.cer"
                        guard panel.runModal() == .OK, let target = panel.url else { return }
                        do {
                            try Data(contentsOf: URL(fileURLWithPath: status.certificate)).write(to: target, options: .atomic)
                            model.message = "公共证书已导出。请在访问设备上安装并信任。"; model.failed = false
                        } catch { model.message = error.localizedDescription; model.failed = true }
                    }.disabled(model.busy)
                    Spacer()
                }
                Text("SHA-256：\(status.certFingerprint)").font(.system(size: 10, design: .monospaced)).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if !model.message.isEmpty { Text(model.message).font(.callout).foregroundStyle(model.failed ? Color.red : Color.secondary).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) }
            HStack {
                if let log = model.status?.log {
                    Button("运行日志") { NSWorkspace.shared.open(URL(fileURLWithPath: log)) }.buttonStyle(.link)
                }
                Button("从备份恢复…") { showRecovery = true }
                Spacer()
                if model.busy { ProgressView().controlSize(.small) }
                if model.running {
                    Button("停止服务") { model.perform("stop") }
                    Button("打开资料库") { if let url = URL(string: model.status?.url ?? "") { NSWorkspace.shared.open(url) } }.buttonStyle(RecoveryActionStyle())
                } else {
                    Button("保存配置") { model.perform("configure") }.disabled(model.saved && !model.changed)
                    Button(model.saved && !model.changed ? "启动服务" : "保存并启动") {
                        if model.saved && !model.changed { model.perform("start") }
                        else { model.perform("configure", startAfter: true) }
                    }.buttonStyle(RecoveryActionStyle())
                }
            }.disabled(model.busy || model.status == nil)
            Text(model.running ? "关闭本窗口后，服务继续运行。修改配置前请先停止服务。" : model.saved && model.changed ? "有未保存的更改；保存并启动后生效。" : "启动后在浏览器中管理资料。").font(.caption).foregroundStyle(.secondary)
        }.padding(20).background(GlobalPalette.surface)
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            HStack(spacing: 0) {
                sidebar
                Divider()
                VStack(spacing: 0) {
                    ScrollViewReader { proxy in
                    ScrollView {
                        VStack(alignment: .leading, spacing: 22) {
                            HStack {
                                GlobalHeading(title: pane.rawValue, icon: pane.icon)
                                Spacer()
                            }
                            if pane == .service, model.running, let url = model.status?.url {
                                Text(url).font(.system(size: 12, design: .monospaced))
                                    .foregroundStyle(GlobalPalette.muted).textSelection(.enabled)
                            }
                            sections
                        }.padding(28)
                    }.id(pane)
                    .onChange(of: model.validationAttempt) { _ in
                        guard let field = model.invalidField else { return }
                        pane = field == "port" ? .service : .security
                        DispatchQueue.main.async { proxy.scrollTo(field, anchor: .center) }
                    }
                    }
                    Divider()
                    footer
                }
            }
        }
        .frame(width: 980, height: 760)
        .background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .tint(GlobalPalette.ink).buttonStyle(GlobalButtonStyle())
        .textFieldStyle(GlobalTextFieldStyle()).toggleStyle(.switch)
        .task { model.refresh() }
        .sheet(isPresented: $showRecovery) { RecoveryView(config: model.config) { model.status = nil; model.refresh() } }
        .sheet(isPresented: $showMigration) { MigrationView(model: model) }
        .sheet(isPresented: $showBackupPackage) { UnifiedBackupView(config: model.config, vaultConfigured: model.status?.vaultSet == true, exportOnly: true) { model.status = nil; model.refresh() } }
        .sheet(isPresented: $showUnifiedBackup) { UnifiedBackupView(config: model.config, vaultConfigured: model.status?.vaultSet == true) { model.status = nil; model.refresh() } }
        .sheet(isPresented: $showRecoveryKey) { RecoveryKeyView(config: model.config, vaultConfigured: model.status?.vaultSet == true) }
        .sheet(item: $backupSetupTarget) { target in BackupSetupView(config: model.config, target: target.rawValue, vaultConfigured: model.status?.vaultSet == true) { model.status = nil; model.refresh() } }
        .sheet(isPresented: $showLoginReset) { LocalPasswordResetView(model: model) }
    }
}
@main struct SRICSLauncherApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    var body: some Scene {
        Window("SRICS Next", id: "main") { LauncherView() }
            .windowResizability(.contentSize)
            .defaultPosition(.center)
            .commands { CommandGroup(replacing: .newItem) {} }
    }
}
