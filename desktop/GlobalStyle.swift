import SwiftUI
import AppKit

// Shared with the web app's Lumoswitch Global-inspired monochrome palette.
enum GlobalPalette {
    private static func adaptive(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(NSColor(name: nil) { appearance in
            let value = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light
            return NSColor(srgbRed: CGFloat((value >> 16) & 255) / 255,
                           green: CGFloat((value >> 8) & 255) / 255,
                           blue: CGFloat(value & 255) / 255, alpha: 1)
        })
    }
    static let background = adaptive(0xf1f3f7, 0x0a0a0c)
    static let surface = adaptive(0xffffff, 0x121317)
    static let soft = adaptive(0xf6f7f9, 0x1b1d23)
    static let ink = adaptive(0x050505, 0xffffff)
    static let inverse = adaptive(0xffffff, 0x0a0a0c)
    static let muted = adaptive(0x6b7280, 0xa4acb9)
    static let line = adaptive(0xe1e3e8, 0x2b2f38)
    static let border = adaptive(0xcdd1d8, 0x4a505d)
}

struct GlobalButtonStyle: ButtonStyle {
    var primary = false
    @Environment(\.isEnabled) private var enabled
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 12, weight: .semibold))
            .padding(.horizontal, 16).frame(minHeight: 36)
            .foregroundStyle(primary ? GlobalPalette.inverse : GlobalPalette.ink)
            .background(Capsule().fill(primary ? GlobalPalette.ink : GlobalPalette.surface))
            .overlay(Capsule().strokeBorder(primary ? GlobalPalette.ink : GlobalPalette.border, lineWidth: 1.5))
            .opacity(enabled ? (configuration.isPressed ? 0.7 : 1) : 0.4)
            .contentShape(Capsule())
    }
}

struct GlobalTextFieldStyle: TextFieldStyle {
    func _body(configuration: TextField<Self._Label>) -> some View {
        configuration.textFieldStyle(.plain)
            .font(.system(size: 13)).padding(.horizontal, 12).padding(.vertical, 10)
            .background(RoundedRectangle(cornerRadius: 10).fill(GlobalPalette.surface))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(GlobalPalette.border, lineWidth: 1))
    }
}

struct GlobalCard<Content: View>: View {
    let title: String
    let icon: String
    let content: Content
    init(_ title: String, icon: String, @ViewBuilder content: () -> Content) {
        self.title = title; self.icon = icon; self.content = content()
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            HStack(spacing: 10) {
                Image(systemName: icon).font(.system(size: 15, weight: .semibold)).frame(width: 22)
                Text(title).font(.system(size: 15, weight: .bold)).accessibilityAddTraits(.isHeader)
            }.foregroundStyle(GlobalPalette.ink)
            VStack(alignment: .leading, spacing: 16) { content }
                .font(.system(size: 13)).frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(24).frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 20).fill(GlobalPalette.surface))
        .overlay(RoundedRectangle(cornerRadius: 20).strokeBorder(GlobalPalette.line, lineWidth: 1.5))
    }
}

struct GlobalHeading: View {
    let title: String
    let icon: String
    var body: some View {
        HStack(spacing: 14) {
            Image(systemName: icon).font(.system(size: 20, weight: .semibold))
                .frame(width: 44, height: 44)
                .background(RoundedRectangle(cornerRadius: 13).fill(GlobalPalette.soft))
                .overlay(RoundedRectangle(cornerRadius: 13).strokeBorder(GlobalPalette.line, lineWidth: 1.5))
            Text(title).font(.system(size: 24, weight: .bold)).accessibilityAddTraits(.isHeader)
        }.foregroundStyle(GlobalPalette.ink)
    }
}

enum LauncherPane: String, CaseIterable, Identifiable {
    case service = "服务配置", security = "密码与保险库", backup = "备份设置", storage = "空间管理", updates = "版本与更新"
    var id: String { rawValue }
    var icon: String {
        switch self {
        case .service: return "externaldrive"
        case .security: return "lock.shield"
        case .backup: return "arrow.triangle.2.circlepath"
        case .storage: return "internaldrive"
        case .updates: return "square.and.arrow.down"
        }
    }
}
