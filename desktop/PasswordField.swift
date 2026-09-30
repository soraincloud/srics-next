import SwiftUI
import AppKit

struct PasswordField: View {
    let title: String
    @Binding var text: String
    var placeholder = ""
    var error: String?
    @State private var revealed = false
    @State private var hovering = false
    @State private var focused = false
    @Environment(\.isEnabled) private var enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text(title).font(.system(size: 12, weight: .medium))
            HStack(spacing: 4) {
                NativePasswordInput(text: $text, focused: $focused, revealed: revealed, enabled: enabled, title: title, placeholder: placeholder)
                    .frame(height: 18)
                    .help("隐藏时使用英文直接输入。中文口令可先显示再输入，或粘贴后核对。")
                    .padding(.leading, 12).padding(.vertical, 11)
                Button { revealed.toggle() } label: {
                    Image(systemName: revealed ? "eye.slash" : "eye")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(hovering || revealed ? GlobalPalette.ink : GlobalPalette.muted)
                        .frame(width: 32, height: 30)
                        .background(RoundedRectangle(cornerRadius: 7).fill(hovering || revealed ? GlobalPalette.soft : Color.clear))
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .padding(.trailing, 5)
                .onHover { hovering = $0 }
                .accessibilityLabel((revealed ? "隐藏" : "显示") + title)
                .help((revealed ? "隐藏" : "显示") + title)
            }
            .background(RoundedRectangle(cornerRadius: 10).fill(GlobalPalette.surface))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(error != nil ? Color.red : focused ? GlobalPalette.ink : GlobalPalette.border, lineWidth: 1))
            if let error { Text(error).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true) }
            if !text.isEmpty && !text.contains("\n") && !text.contains("\r") && (text.first?.isWhitespace == true || text.last?.isWhitespace == true) {
                Text("首尾含空白字符，输入时也必须保留；可点眼睛按钮核对。").font(.caption).foregroundStyle(.secondary)
            }
        }
        .onChange(of: enabled) { if !$0 { revealed = false } }
        .onChange(of: text.isEmpty) { if $0 { revealed = false } }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didResignActiveNotification)) { _ in revealed = false }
    }
}
