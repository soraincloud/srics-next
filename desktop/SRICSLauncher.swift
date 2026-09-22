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
    var cloud = CloudConfig()
}
struct ServiceStatus: Codable, Sendable {
    var version: String
    var updateBackup: String
    var config: LocalConfig
    var saved: Bool
    var running: Bool
    var passwordSet: Bool
    var vaultSet: Bool
    var vaultIdleMinutes: Int
    var url: String
    var log: String
    var lanAddresses: [String]
    var certificate: String
    var certFingerprint: String
    var networkError: String
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
    var errorDescription: String? { message }
}

// Credentials travel over a private stdin pipe, never arguments, logs or preferences.
func runManager(_ action: String, payload: Data? = nil) throws -> ServiceStatus {
    guard let resources = Bundle.main.resourceURL else { throw CommandError(message: "程序包不完整") }
    let process = Process()
    process.executableURL = resources.appendingPathComponent("bin/srics")
    process.arguments = ["manager", action]
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
        throw CommandError(message: String(data: errorBytes, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "操作失败")
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
    var running: Bool { status?.running == true }
    var saved: Bool { status?.saved == true }
    var passwordSet: Bool { status?.passwordSet == true }
    var changed: Bool { config != status?.config || port != String(status?.config.port ?? 19473) || !password.isEmpty || !vaultPassword.isEmpty || vaultIdleMinutes != (status?.vaultIdleMinutes ?? 10) }

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
        if action == "configure" {
            guard vaultPassword == repeatedVaultPassword else { message = "两次保险库口令不一致"; failed = true; return }
            guard password == repeatedPassword else { message = "两次密码不一致"; failed = true; return }
            guard let number = Int(port), (1024...65535).contains(number) else { message = "端口需在 1024–65535 之间"; failed = true; return }
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
                if startAfter { result = try await Task.detached { try runManager("start") }.value; status = result }
                message = action == "stop" ? "服务已停止" : result.running ? "服务正在后台运行" : "配置已保存"
                if action == "prepare-update" { message = "更新备份已校验，旧程序与记录保存在：\(result.updateBackup)。退出本窗口后替换 .app，再打开并启动。"; NSWorkspace.shared.open(URL(fileURLWithPath: result.updateBackup)) }
                if result.running && (action == "start" || startAfter), let url = URL(string: result.url) { NSWorkspace.shared.open(url) }
            } catch {
                message = error.localizedDescription; failed = true
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
    @State private var pane: LauncherPane = .service

    private var header: some View {
        HStack(spacing: 12) {
            Image(systemName: "square.grid.2x2").font(.system(size: 19, weight: .semibold))
                .foregroundStyle(GlobalPalette.inverse).frame(width: 42, height: 42)
                .background(Circle().fill(GlobalPalette.ink))
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
            Text(model.status?.version ?? "SRICS Next").font(.system(size: 11, weight: .medium))
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
                LabeledContent("端口") { TextField("19473", text: $model.port).frame(width: 95).textFieldStyle(GlobalTextFieldStyle()).labelsHidden() }
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
                if model.saved { Text("资料目录已固定，迁移与恢复需单独操作。").font(.caption).foregroundStyle(.secondary) }
            }.disabled(model.busy || model.running)
        }
        if pane == .security {
            GlobalCard(model.passwordSet ? "修改登录密码（可选）" : "登录密码", icon: "key") {
                if model.passwordSet { SecureField("当前密码", text: $model.currentPassword) }
                SecureField(model.passwordSet ? "新密码，留空则保留" : "至少 12 个字符", text: $model.password)
                SecureField("再次输入密码", text: $model.repeatedPassword)
            }.disabled(model.busy || model.running)
        }
        if pane == .security {
            GlobalCard(model.status?.vaultSet == true ? "保险库（已设置）" : "保险库口令（可选）", icon: "lock.shield") {
                if model.status?.vaultSet == true { SecureField("当前保险库口令", text: $model.currentVaultPassword) }
                SecureField(model.status?.vaultSet == true ? "新口令，留空则保留" : "独立口令，至少 12 字节", text: $model.vaultPassword)
                SecureField("再次输入保险库口令", text: $model.repeatedVaultPassword)
                Stepper("闲置 \(model.vaultIdleMinutes) 分钟后锁定", value: $model.vaultIdleMinutes, in: 1...60)
                Text(model.status?.vaultSet == true ? "历史备份仍需对应的旧口令。修改口令不会撤销旧备份。" : "用于私密照片与个人文件，请独立保存。遗失后无法通过登录密码找回。").font(.caption).foregroundStyle(.secondary)
            }.disabled(model.busy || model.running)
        }
        if pane == .backup {
            GlobalCard("本地 / 独立硬盘备份（可选）", icon: "externaldrive.badge.timemachine") {
                LabeledContent("备份目录") {
                    HStack { TextField("未配置", text: $model.config.backupRepository).textFieldStyle(GlobalTextFieldStyle()).labelsHidden().accessibilityLabel("备份目录路径")
                        Button("选择…") { model.choose("backup") }
                    }
                }.accessibilityElement(children: .contain)
                LabeledContent("口令文件") {
                    HStack { TextField("未配置", text: $model.config.backupPasswordFile).textFieldStyle(GlobalTextFieldStyle()).labelsHidden().accessibilityLabel("备份口令文件路径")
                        Button("选择…") { model.choose("passwordFile") }
                    }
                }.accessibilityElement(children: .contain)
                Text("使用独立备份口令，文件权限需为 600。请另存一份恢复口令。").font(.caption).foregroundStyle(.secondary)
            }.disabled(model.busy || model.running)
        }
        if pane == .backup {
            GlobalCard("云端加密备份（S3 兼容）", icon: "cloud") {
                Toggle("启用云端备份", isOn: $model.config.cloud.enabled)
                if model.config.cloud.enabled {
                    TextField("Endpoint，例如 https://s3.example.com", text: $model.config.cloud.connection.endpoint)
                    TextField("区域，例如 us-east-1", text: $model.config.cloud.connection.region)
                    TextField("存储桶", text: $model.config.cloud.connection.bucket)
                    TextField("专用前缀，例如 srics/main", text: $model.config.cloud.connection.prefix)
                    Picker("寻址方式", selection: $model.config.cloud.connection.lookup) {
                        Text("自动").tag("auto")
                        Text("Path").tag("path")
                        Text("DNS（OSS）").tag("dns")
                    }
                    LabeledContent("凭据文件") {
                        HStack { TextField("JSON · 权限 600", text: $model.config.cloud.credentialsFile).textFieldStyle(GlobalTextFieldStyle()).labelsHidden().accessibilityLabel("凭据文件路径"); Button("选择…") { model.choose("cloudCredentials") } }
                    }.accessibilityElement(children: .contain)
                    LabeledContent("备份口令文件") {
                        HStack { TextField("权限 600", text: $model.config.cloud.passwordFile).textFieldStyle(GlobalTextFieldStyle()).labelsHidden().accessibilityLabel("备份口令文件路径"); Button("选择…") { model.choose("cloudPassword") } }
                    }.accessibilityElement(children: .contain)
                    LabeledContent("自定义 CA") {
                        HStack { TextField("可选 PEM 文件", text: $model.config.cloud.connection.caFile).textFieldStyle(GlobalTextFieldStyle()).labelsHidden().accessibilityLabel("自定义 CA路径"); Button("选择…") { model.choose("cloudCA") } }
                    }.accessibilityElement(children: .contain)
                    Text("凭据 JSON 包含 accessKeyId、secretAccessKey，可选 sessionToken。存放在资料库和本地备份目录之外。").font(.caption).foregroundStyle(.secondary)
                    Button("检查云端连接") { model.checkCloud() }
                    Text("先创建存储桶，使用专用前缀。每次备份会读取全部云端备份进行检查，可能产生下载费用。").font(.caption).foregroundStyle(.secondary)
                }
            }.disabled(model.busy || model.running)
        }
        if pane == .backup {
            GlobalCard("备份计划", icon: "clock") {
                Toggle("每天自动备份", isOn: Binding(get: { !model.config.backupDailyAt.isEmpty }, set: { model.config.backupDailyAt = $0 ? "03:00" : "" }))
                if !model.config.backupDailyAt.isEmpty {
                    LabeledContent("每天执行时间") {
                        TextField("03:00", text: $model.config.backupDailyAt).frame(width: 95).textFieldStyle(GlobalTextFieldStyle()).labelsHidden()
                    }
                    Text("使用本机时区，依次备份本地和云端。漏跑补做，失败每小时重试。").font(.caption).foregroundStyle(.secondary)
                }
            }.disabled(model.busy || model.running)
        }
        if pane == .storage {
            GlobalCard("空间管理", icon: "internaldrive") {
                Toggle("自动清理回收站", isOn: Binding(get: { model.config.trashDays > 0 }, set: { model.config.trashDays = $0 ? 30 : 0 }))
                if model.config.trashDays > 0 { Stepper("回收站保留 \(model.config.trashDays) 天", value: $model.config.trashDays, in: 1...3650); Text("到期后永久删除。私密资料在解锁后清理，历史备份独立保留。").font(.caption).foregroundStyle(.secondary) }
                Toggle("清理过期历史备份", isOn: Binding(get: { model.config.retention.enabled }, set: { model.config.retention.enabled = $0; if $0 && model.config.retention.daily == 0 { model.config.retention.daily = 30; model.config.retention.monthly = 12 } }))
                if model.config.retention.enabled {
                    Stepper("每日版本：\(model.config.retention.daily)", value: $model.config.retention.daily, in: 1...3650)
                    Stepper("月度版本：\(model.config.retention.monthly)", value: $model.config.retention.monthly, in: 0...120)
                    Text("按 UTC 日/月保留最新版本；新备份通过校验后永久删除多余快照。至少保留最新一份。网页可预览清理内容。").font(.caption).foregroundStyle(.secondary)
                }
            }.disabled(model.busy || model.running)
        }
        if pane == .updates {
            GlobalCard("版本与更新", icon: "square.and.arrow.down") {
                Text("当前版本：\(model.status?.version ?? "—")").font(.caption)
                Button("停止服务并准备更新") { model.perform("prepare-update") }.disabled(model.busy || !model.saved)
                if model.busy && AppDelegate.recoveryBusy { Button("取消更新准备") { model.cancelUpdate() } }
                Text("先完成加密备份并保留旧程序，再退出、替换 .app 并重新启动；失败时不替换程序。需要已配置的可读备份仓库。").font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 12) {

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
