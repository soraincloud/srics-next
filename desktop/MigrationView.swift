import SwiftUI
import AppKit

struct MigrationView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject var model: Launcher
    @State private var directory = ""
    @State private var message = ""
    @State private var busy = false
    @State private var completed = false
    @State private var failed = false
    private var source: String { model.status?.config.data ?? model.config.data }

    private func choose() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false; panel.canChooseDirectories = true
        panel.canCreateDirectories = true; panel.allowsMultipleSelection = false
        panel.message = "选择新位置，将在其中新建资料库目录。不会覆盖现有目录。"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        let formatter = DateFormatter(); formatter.dateFormat = "yyyyMMdd-HHmmss"
        directory = url.appendingPathComponent("SRICS-library-" + formatter.string(from: Date())).path
        message = ""; failed = false
    }
    private func migrate() {
        guard !busy && !directory.isEmpty else { return }
        guard let payload = try? JSONEncoder().encode(["directory": directory]) else { return }
        let process = RecoveryProcess()
        busy = true; model.busy = true; AppDelegate.recoveryBusy = true; failed = false
        message = "正在停止服务、复制并校验。耗时取决于资料大小，请勿退出或断开磁盘。"
        Task {
            do {
                let status = try await Task.detached { try process.run("migrate-library", payload: payload, as: ServiceStatus.self) }.value
                model.status = status; model.config = status.config; model.port = String(status.config.port)
                model.message = "资料目录已迁移并校验。原目录保留，点击“启动服务”使用新位置。"
                model.failed = false; completed = true
                message = "迁移完成，资料索引与文件校验通过。"
            } catch {
                failed = true; message = error.localizedDescription
                if let status = try? await Task.detached(operation: { try runManager("info") }).value {
                    model.status = status; model.config = status.config; model.port = String(status.config.port)
                    message += "\n当前资料目录：\(status.config.data)"
                    if !status.running { message += "\n服务已停止，可修正后重试或重新启动。" }
                }
            }
            busy = false; model.busy = false; AppDelegate.recoveryBusy = false
        }
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            GlobalHeading(title: "迁移资料目录", icon: "externaldrive")
            if !completed {
                VStack(alignment: .leading, spacing: 8) {
                    Text("当前目录").font(.system(size: 12, weight: .medium))
                    Text(source).font(.system(size: 12)).foregroundStyle(GlobalPalette.muted).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                }
                VStack(alignment: .leading, spacing: 8) {
                    HStack { Text("新目录").font(.system(size: 12, weight: .medium)); Spacer(); Button("选择位置…") { choose() } }
                    Text(directory.isEmpty ? "尚未选择" : directory).font(.system(size: 12)).foregroundStyle(GlobalPalette.muted).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                }.disabled(busy)
                Text("迁移会暂停访问，复制数据库、原件、私密文件、回收站和上传进度。校验通过后才切换路径；登录密码、保险库口令与恢复密钥记录保持原样。").font(.callout).foregroundStyle(GlobalPalette.muted)
                Text("新位置需容纳完整资料库，建议使用 APFS 磁盘。原目录会保留，不会自动删除。备份仓库仍使用原来的位置。").font(.caption).foregroundStyle(GlobalPalette.muted)
            }
            if !message.isEmpty { Text(message).font(.callout).foregroundStyle(failed ? Color.red : GlobalPalette.muted).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) }
            if completed {
                Text("新目录：\(model.config.data)").font(.callout).textSelection(.enabled)
                Text("确认新位置能正常浏览、解锁私密资料并完成一次备份后，再自行处理旧副本。").font(.caption).foregroundStyle(GlobalPalette.muted)
            }
            HStack {
                if busy { ProgressView().controlSize(.small) }
                Spacer()
                Button(completed ? "完成" : "取消") { dismiss() }.disabled(busy)
                if !completed { Button("停止服务并迁移") { migrate() }.buttonStyle(GlobalButtonStyle(primary: true)).disabled(busy || directory.isEmpty) }
            }
        }
        .padding(28).frame(width: 580).background(GlobalPalette.background).foregroundStyle(GlobalPalette.ink)
        .buttonStyle(GlobalButtonStyle()).interactiveDismissDisabled(busy)
    }
}
