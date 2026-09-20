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
struct LocalConfig: Codable, Equatable, Sendable {
    var data = ""
    var port = 19473
    var lanAddress = ""
    var backupRepository = ""
    var backupPasswordFile = ""
    var backupDailyAt = ""
    var cloud = CloudConfig()
}
struct ServiceStatus: Codable, Sendable {
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
                if status == nil { config = result.config; port = String(result.config.port); vaultIdleMinutes = result.vaultIdleMinutes }
                status = result
            } catch { message = error.localizedDescription; failed = true }
            busy = false
        }
    }
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
        busy = true; failed = false; message = ""
        let request = payload
        Task {
            do {
                var result = try await Task.detached { try runManager(action, payload: request) }.value
                status = result; config = result.config; port = String(result.config.port)
                if action == "configure" { password = ""; repeatedPassword = ""; currentPassword = ""; vaultPassword = ""; repeatedVaultPassword = ""; currentVaultPassword = "" }
                if startAfter { result = try await Task.detached { try runManager("start") }.value; status = result }
                message = action == "stop" ? "服务已停止" : result.running ? "服务正在后台运行" : "配置已保存"
                if result.running && (action == "start" || startAfter), let url = URL(string: result.url) { NSWorkspace.shared.open(url) }
            } catch { message = error.localizedDescription; failed = true }
            busy = false
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
    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 12) {
                Image(systemName: "externaldrive").font(.system(size: 27, weight: .medium))
                VStack(alignment: .leading, spacing: 5) {
                    Text("SRICS Next").font(.title2.weight(.semibold))
                    HStack(spacing: 6) {
                        Circle().fill(model.running ? Color.green : Color.secondary).frame(width: 6, height: 6)
                        Text(model.running ? "运行中 · \(model.status?.url ?? "")" : model.saved ? "已停止" : "本机配置").font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                Button { model.refresh() } label: { Image(systemName: "arrow.clockwise") }.help("刷新状态").disabled(model.busy)
            }.padding(24)
            Divider()
            Form {
                Section("服务") {
                    LabeledContent("资料目录") {
                        HStack { TextField("目录路径", text: $model.config.data).textFieldStyle(.roundedBorder).labelsHidden()
                            Button("选择…") { model.choose("data") }
                        }.disabled(model.saved)
                    }
                    LabeledContent("端口") { TextField("19473", text: $model.port).frame(width: 95).textFieldStyle(.roundedBorder).labelsHidden() }
                    Picker("访问范围", selection: $model.config.lanAddress) {
                        Text("仅本机").tag("")
                        ForEach(Array(Set((model.status?.lanAddresses ?? []) + (model.config.lanAddress.isEmpty ? [] : [model.config.lanAddress]))).sorted(), id: \.self) { address in
                            Text("局域网 · \(address)").tag(address)
                        }
                    }
                    if !model.config.lanAddress.isEmpty {
                        Text("局域网使用 HTTPS。保存后导出公共证书，在访问设备上安装并信任；建议在路由器中固定此 IP。").font(.caption).foregroundStyle(.secondary)
                    }
                    if model.saved { Text("资料目录已固定，迁移与恢复需单独操作。").font(.caption).foregroundStyle(.secondary) }
                }.disabled(model.busy || model.running)
                Section(model.passwordSet ? "修改登录密码（可选）" : "登录密码") {
                    if model.passwordSet { SecureField("当前密码", text: $model.currentPassword) }
                    SecureField(model.passwordSet ? "新密码，留空则保留" : "至少 12 个字符", text: $model.password)
                    SecureField("再次输入密码", text: $model.repeatedPassword)
                }.disabled(model.busy || model.running)
                Section(model.status?.vaultSet == true ? "保险库（已设置）" : "保险库口令（可选）") {
                    if model.status?.vaultSet == true { SecureField("当前保险库口令", text: $model.currentVaultPassword) }
                    SecureField(model.status?.vaultSet == true ? "新口令，留空则保留" : "独立口令，至少 12 字节", text: $model.vaultPassword)
                    SecureField("再次输入保险库口令", text: $model.repeatedVaultPassword)
                    Stepper("闲置 \(model.vaultIdleMinutes) 分钟后锁定", value: $model.vaultIdleMinutes, in: 1...60)
                    Text(model.status?.vaultSet == true ? "历史备份仍需对应的旧口令。修改口令不会撤销旧备份。" : "用于私密照片与个人文件，请独立保存。遗失后无法通过登录密码找回。").font(.caption).foregroundStyle(.secondary)
                }.disabled(model.busy || model.running)
                Section("本地 / 独立硬盘备份（可选）") {
                    LabeledContent("备份目录") {
                        HStack { TextField("未配置", text: $model.config.backupRepository).textFieldStyle(.roundedBorder).labelsHidden()
                            Button("选择…") { model.choose("backup") }
                        }
                    }
                    LabeledContent("口令文件") {
                        HStack { TextField("未配置", text: $model.config.backupPasswordFile).textFieldStyle(.roundedBorder).labelsHidden()
                            Button("选择…") { model.choose("passwordFile") }
                        }
                    }
                    Text("使用独立备份口令，文件权限需为 600。请另存一份恢复口令。").font(.caption).foregroundStyle(.secondary)
                }.disabled(model.busy || model.running)
                Section("云端加密备份（S3 兼容）") {
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
                            HStack { TextField("JSON · 权限 600", text: $model.config.cloud.credentialsFile).textFieldStyle(.roundedBorder).labelsHidden().accessibilityLabel("凭据文件路径"); Button("选择…") { model.choose("cloudCredentials") } }
                        }.accessibilityElement(children: .contain)
                        LabeledContent("备份口令文件") {
                            HStack { TextField("权限 600", text: $model.config.cloud.passwordFile).textFieldStyle(.roundedBorder).labelsHidden().accessibilityLabel("备份口令文件路径"); Button("选择…") { model.choose("cloudPassword") } }
                        }.accessibilityElement(children: .contain)
                        LabeledContent("自定义 CA") {
                            HStack { TextField("可选 PEM 文件", text: $model.config.cloud.connection.caFile).textFieldStyle(.roundedBorder).labelsHidden().accessibilityLabel("自定义 CA路径"); Button("选择…") { model.choose("cloudCA") } }
                        }.accessibilityElement(children: .contain)
                        Text("凭据 JSON 包含 accessKeyId、secretAccessKey，可选 sessionToken。存放在资料库和本地备份目录之外。").font(.caption).foregroundStyle(.secondary)
                        Button("检查云端连接") { model.checkCloud() }
                        Text("先创建存储桶，使用专用前缀。每次备份会读取全部云端备份进行检查，可能产生下载费用。").font(.caption).foregroundStyle(.secondary)
                    }
                }.disabled(model.busy || model.running)
                Section("备份计划") {
                    Toggle("每天自动备份", isOn: Binding(get: { !model.config.backupDailyAt.isEmpty }, set: { model.config.backupDailyAt = $0 ? "03:00" : "" }))
                    if !model.config.backupDailyAt.isEmpty {
                        LabeledContent("每天执行时间") {
                            TextField("03:00", text: $model.config.backupDailyAt).frame(width: 95).textFieldStyle(.roundedBorder).labelsHidden()
                        }
                        Text("使用本机时区，依次备份本地和云端。漏跑补做，失败每小时重试，不自动删除历史快照。").font(.caption).foregroundStyle(.secondary)
                    }
                }.disabled(model.busy || model.running)
            }.formStyle(.grouped)
            Divider()
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
                    Spacer()
                    if model.busy { ProgressView().controlSize(.small) }
                    if model.running {
                        Button("停止服务") { model.perform("stop") }
                        Button("打开资料库") { if let url = URL(string: model.status?.url ?? "") { NSWorkspace.shared.open(url) } }.buttonStyle(.borderedProminent).tint(.primary)
                    } else {
                        Button("保存配置") { model.perform("configure") }
                        Button(model.saved && !model.changed ? "启动服务" : "保存并启动") {
                            if model.saved && !model.changed { model.perform("start") }
                            else { model.perform("configure", startAfter: true) }
                        }.buttonStyle(.borderedProminent).tint(.primary)
                    }
                }.disabled(model.busy || model.status == nil)
                Text(model.running ? "关闭本窗口后，服务继续运行。修改配置前请先停止服务。" : "启动后在浏览器中管理资料。").font(.caption).foregroundStyle(.secondary)
            }.padding(20)
        }.frame(width: 650, height: 800).task { model.refresh() }
    }
}
@main struct SRICSLauncherApp: App {
    var body: some Scene {
        Window("SRICS Next", id: "main") { LauncherView() }
            .windowResizability(.contentSize)
            .defaultPosition(.center)
            .commands { CommandGroup(replacing: .newItem) {} }
    }
}
