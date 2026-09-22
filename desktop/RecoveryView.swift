import SwiftUI
import AppKit

struct RecoveryActionStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        GlobalButtonStyle(primary: true).makeBody(configuration: configuration)
    }
}

struct BackupSnapshot: Decodable, Identifiable, Sendable {
    var id: String
    var time: String
    var files: UInt64?
    var bytes: UInt64?
    var dateLabel: String {
        let f = ISO8601DateFormatter(); f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let date = f.date(from: time) ?? ISO8601DateFormatter().date(from: time) else { return time }
        return date.formatted(date: .numeric, time: .standard)
    }
}
struct RecoverySource: Codable, Equatable, Sendable {
    var target = "local"
    var repository = ""
    var passwordFile = ""
    var cloud = CloudConfig()
    init(config: LocalConfig) {
        repository = config.backupRepository; passwordFile = config.backupPasswordFile; cloud = config.cloud
        if repository.isEmpty && cloud.enabled { target = "cloud" }
    }
}
struct RecoveryRequest: Encodable, Sendable {
    let source: RecoverySource
    var snapshot = ""
    var directory = ""
    var vaultPassword = ""
    var itemID = ""
    var `private` = false
    var exportDirectory = ""
}
struct RecoveryResult: Decodable, Sendable {
    let snapshot: String
    let directory: String
    let verifiedAt: String
    let vaultPresent: Bool
}
struct RecoveryResponse: Decodable, Sendable {
    var items: [RecoveredItem]?
    var exported: String?
    var snapshots: [BackupSnapshot]?
    var result: RecoveryResult?
    var activated: Bool
}

// A cancellable, separate process keeps long restores off the UI thread.
final class RecoveryProcess: @unchecked Sendable {
    private let lock = NSLock()
    private var process: Process?
    private var cancelled = false
    func cancel() {
        lock.lock(); defer { lock.unlock() }
        cancelled = true
        if let process, process.isRunning { process.terminate() }
    }
    func run<T: Decodable>(_ action: String, payload: Data, as type: T.Type) throws -> T {
        guard let resources = Bundle.main.resourceURL else { throw CommandError(message: "程序包不完整") }
        let p = Process(), input = Pipe(), output = Pipe(), failure = Pipe()
        p.executableURL = resources.appendingPathComponent("bin/srics")
        p.arguments = ["manager", action]
        p.standardInput = input; p.standardOutput = output; p.standardError = failure
        lock.lock()
        if cancelled { lock.unlock(); throw CommandError(message: "任务已取消") }
        do { try p.run(); process = p; lock.unlock() }
        catch { lock.unlock(); throw error }
        defer { lock.lock(); process = nil; lock.unlock() }
        try input.fileHandleForWriting.write(contentsOf: payload)
        try input.fileHandleForWriting.close()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        let errors = failure.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        lock.lock(); let wasCancelled = cancelled; lock.unlock()
        if wasCancelled { throw CommandError(message: "任务已取消。已生成的目录会保留，重试请选择新目录。") }
        guard p.terminationStatus == 0 else { throw CommandError(message: String(data: errors, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "恢复操作失败") }
        return try JSONDecoder().decode(type, from: data)
    }
}

@MainActor final class AppDelegate: NSObject, NSApplicationDelegate {
    static var recoveryBusy = false
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard Self.recoveryBusy else { return .terminateNow }
        let alert = NSAlert()
        alert.messageText = "备份或恢复任务正在进行"
        alert.informativeText = "请等待完成，或取消当前任务后退出。"
        alert.addButton(withTitle: "继续等待")
        alert.runModal()
        return .terminateCancel
    }
}

@MainActor final class RecoveryModel: ObservableObject {
    @Published var source: RecoverySource
    @Published var snapshots: [BackupSnapshot] = []
    @Published var snapshot: String? = nil
    @Published var directory = ""
    @Published var step = 0
    @Published var busy = false
    @Published var failed = false
    @Published var message = ""
    @Published var result: RecoveryResult?
    @Published var activated = false
    private var command: RecoveryProcess?
    init(config: LocalConfig) { source = RecoverySource(config: config) }
    func perform(_ action: String) {
        guard !busy else { return }
        if action == "recovery-restore" && (snapshot == nil || directory.isEmpty) { return }
        guard let payload = try? JSONEncoder().encode(RecoveryRequest(source: source, snapshot: snapshot ?? "", directory: result?.directory ?? directory)) else { return }
        let process = RecoveryProcess(); command = process
        busy = true; failed = false; message = action == "recovery-snapshots" ? "正在读取备份历史…" : action == "recovery-activate" ? "正在复核并切换资料库…" : action == "recovery-inspect" ? "正在校验恢复目录…" : "正在恢复文件并校验，请保持窗口打开…"
        AppDelegate.recoveryBusy = true
        Task {
            do {
                let response = try await Task.detached {
                    if action == "recovery-activate" { _ = try runManager("stop") }
                    return try process.run(action, payload: payload, as: RecoveryResponse.self)
                }.value
                if action == "recovery-snapshots" {
                    snapshots = response.snapshots ?? []; snapshot = snapshots.first?.id
                    step = 1; message = snapshots.isEmpty ? "这个目标还没有资料库快照。" : ""
                } else {
                    result = response.result; activated = response.activated; step = 2
                    message = activated ? "已切换资料库。关闭此窗口后启动服务，使用备份时的登录密码。" : "恢复完成，索引与文件校验通过。"
                }
            } catch { failed = true; message = error.localizedDescription }
            command = nil; busy = false; AppDelegate.recoveryBusy = false
        }
    }
    func cancel() { command?.cancel() }
    func openRecovered() {
        let panel = NSOpenPanel(); panel.canChooseFiles = false; panel.canChooseDirectories = true; panel.allowsMultipleSelection = false
        panel.prompt = "选择恢复目录"; panel.message = "选择包含 .srics-recovery.json 的已恢复资料库。先校验，再决定是否启用。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        directory = url.path; result = nil; perform("recovery-inspect")
    }
    func chooseDestination() {
        let panel = NSOpenPanel(); panel.canChooseFiles = false; panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false; panel.canCreateDirectories = true
        panel.prompt = "选择存放位置"; panel.message = "将在此位置新建恢复目录，不覆盖已有文件。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        let date = DateFormatter(); date.dateFormat = "yyyyMMdd-HHmmss"
        directory = url.appendingPathComponent("SRICS-restored-\(date.string(from: Date()))-\(UUID().uuidString.prefix(6))").path
    }
}

struct RecoveryView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var model: RecoveryModel
    @State private var showExport = false
    let onActivated: () -> Void
    init(config: LocalConfig, onActivated: @escaping () -> Void) {
        _model = StateObject(wrappedValue: RecoveryModel(config: config)); self.onActivated = onActivated
    }
    @ViewBuilder private func pathField(_ title: String, _ binding: Binding<String>, directory: Bool = false) -> some View {
        LabeledContent(title) {
            HStack {
                TextField(title + "路径", text: binding).labelsHidden().textFieldStyle(GlobalTextFieldStyle()).accessibilityLabel(title + "路径")
                Button("选择…") {
                    let panel = NSOpenPanel(); panel.canChooseDirectories = directory; panel.canChooseFiles = !directory
                    panel.allowsMultipleSelection = false; panel.prompt = "选择"
                    if panel.runModal() == .OK, let url = panel.url { binding.wrappedValue = url.path }
                }
            }
        }.accessibilityElement(children: .contain)
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                GlobalHeading(title: "从备份恢复", icon: "arrow.counterclockwise")
                Spacer()
                Text(model.step == 0 ? "1 · 备份来源" : model.step == 1 ? "2 · 恢复位置" : "3 · 恢复结果").font(.caption).foregroundStyle(.secondary)
            }.padding(24)
            Divider()
            if model.step == 0 {
                ScrollView {
                    VStack(alignment: .leading, spacing: 18) {
                        GlobalCard("备份来源", icon: "externaldrive") {
                            Picker("存储类型", selection: $model.source.target) { Text("本地 / 独立硬盘").tag("local"); Text("云端 S3").tag("cloud") }
                            if model.source.target == "local" {
                                pathField("备份目录", $model.source.repository, directory: true)
                                pathField("备份口令文件", $model.source.passwordFile)
                            } else {
                                TextField("Endpoint", text: $model.source.cloud.connection.endpoint)
                                TextField("区域", text: $model.source.cloud.connection.region)
                                TextField("存储桶", text: $model.source.cloud.connection.bucket)
                                TextField("专用前缀", text: $model.source.cloud.connection.prefix)
                                Picker("寻址方式", selection: $model.source.cloud.connection.lookup) { Text("自动").tag("auto"); Text("Path").tag("path"); Text("DNS（OSS）").tag("dns") }
                                pathField("凭据 JSON", $model.source.cloud.credentialsFile)
                                pathField("备份口令文件", $model.source.cloud.passwordFile)
                                pathField("自定义 CA（可选）", $model.source.cloud.connection.caFile)
                            }
                        }
                        Text("填写备份时使用的连接信息和独立备份口令文件（权限 600）。这些设置仅用于本次恢复。").font(.caption).foregroundStyle(.secondary)
                    }.padding(24)
                }.disabled(model.busy)
            } else if model.step == 1 {
                VStack(alignment: .leading, spacing: 16) {
                    Text("选择恢复点").font(.headline)
                    List(model.snapshots, selection: $model.snapshot) { entry in
                        VStack(alignment: .leading, spacing: 7) {
                            Text(entry.dateLabel)
                            Text(entry.id).font(.system(size: 10, design: .monospaced)).foregroundStyle(.secondary).textSelection(.enabled)
                            if let bytes = entry.bytes { Text(ByteCountFormatter.string(fromByteCount: Int64(clamping: bytes), countStyle: .file)).font(.caption).foregroundStyle(.secondary) }
                        }.padding(.vertical, 6).tag(entry.id)
                    }.frame(minHeight: 160).scrollContentBackground(.hidden)
                    .background(GlobalPalette.surface).clipShape(RoundedRectangle(cornerRadius: 16))
                    .overlay(RoundedRectangle(cornerRadius: 16).strokeBorder(GlobalPalette.line, lineWidth: 1.5))
                    HStack { Text("恢复到新目录").font(.headline); Spacer(); Button("选择存放位置…") { model.chooseDestination() } }
                    Text(model.directory.isEmpty ? "选择磁盘上的存放位置，将自动生成一个新目录。" : model.directory).font(.callout).foregroundStyle(.secondary).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    Text("恢复后自动校验。原资料保持不变，云端恢复可能产生下载费用。").font(.caption).foregroundStyle(.secondary)
                }.padding(24).disabled(model.busy)
            } else if let result = model.result {
                VStack(alignment: .leading, spacing: 20) {
                    Label(model.activated ? "资料库已启用" : "恢复校验通过", systemImage: "checkmark.circle").font(.title2)
                    Text(result.directory).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    Text("快照：\(result.snapshot)").font(.system(size: 11, design: .monospaced)).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    Text("校验记录已保存为 .srics-recovery.json。").font(.caption).foregroundStyle(.secondary)
                    if result.vaultPresent { Text("私密文件保持加密。请使用备份时的保险库口令解锁确认。").font(.callout) }
                    if !model.activated { Text("启用会切换资料目录，保留原资料。启用时会先停止现有服务，之后使用备份时的登录密码。").font(.callout).foregroundStyle(.secondary) }
                    HStack { Button("在 Finder 中查看") { NSWorkspace.shared.open(URL(fileURLWithPath: result.directory)) }; if !model.activated { Button("取回单项文件…") { showExport = true } } }
                    Spacer()
                }.padding(24)
            }
            Divider()
            VStack(alignment: .leading, spacing: 14) {
                if !model.message.isEmpty { Text(model.message).foregroundStyle(model.failed ? Color.red : Color.secondary).font(.callout).fixedSize(horizontal: false, vertical: true).textSelection(.enabled) }
                HStack {
                    if model.busy { ProgressView().controlSize(.small); Button("取消任务") { model.cancel() } }
                    else if model.step == 0 { Button("打开恢复目录…") { model.openRecovered() } }
                    else if model.step == 1 { Button("上一步") { model.step = 0; model.message = "" } }
                    Spacer()
                    Button("关闭") { if model.activated { onActivated() }; dismiss() }.disabled(model.busy)
                    if model.step == 0 { Button("读取备份历史") { model.perform("recovery-snapshots") }.disabled(model.busy).buttonStyle(RecoveryActionStyle()) }
                    else if model.step == 1 { Button("恢复并校验") { model.perform("recovery-restore") }.disabled(model.busy || model.snapshot == nil || model.directory.isEmpty).buttonStyle(RecoveryActionStyle()) }
                    else if !model.activated { Button("停止服务并启用") { model.perform("recovery-activate") }.disabled(model.busy).buttonStyle(RecoveryActionStyle()) }
                }
            }.padding(24)
        }.frame(width: 740, height: 700).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .tint(GlobalPalette.ink).buttonStyle(GlobalButtonStyle()).textFieldStyle(GlobalTextFieldStyle())
        .interactiveDismissDisabled(model.busy)
        .sheet(isPresented: $showExport) { if let result = model.result { RecoveryExportView(source: model.source, directory: result.directory, hasVault: result.vaultPresent) } }
    }
}
