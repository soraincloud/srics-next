import SwiftUI
import AppKit

struct PasswordField: View {
    let title: String
    @Binding var text: String
    var placeholder = ""
    var error: String?
    @State private var revealed = false
    @Environment(\.isEnabled) private var enabled

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text(title).font(.system(size: 12, weight: .medium))
            HStack(spacing: 8) {
                Group {
                    if revealed { TextField(placeholder, text: $text) }
                    else { SecureField(placeholder, text: $text) }
                }
                .autocorrectionDisabled(true)
                .textContentType(.password)
                .accessibilityLabel(title)
                .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(error == nil ? Color.clear : Color.red, lineWidth: 1))
                Button { revealed.toggle() } label: {
                    Image(systemName: revealed ? "eye.slash" : "eye").frame(width: 24, height: 24)
                }
                .buttonStyle(.plain)
                .accessibilityLabel((revealed ? "隐藏" : "显示") + title)
                .help((revealed ? "隐藏" : "显示") + title)
            }
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
