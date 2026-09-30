import AppKit

@main struct PasswordInputChecks {
    @MainActor static func main() {
        _ = NSApplication.shared
        let control = PasswordInputControl()
        var model = ""
        control.onText = { model = $0 }
        func update(_ value: String, revealed: Bool = false, enabled: Bool = true) {
            control.update(text: value, revealed: revealed, enabled: enabled, title: "Test", placeholder: "")
        }
        update("")
        // Each committed keystroke must reach the model and survive a form refresh.
        for value in ["r", "ru", "run", "run123"] {
            control.secure.stringValue = value
            control.controlTextDidChange(Notification(name: NSControl.textDidChangeNotification, object: control.secure))
            precondition(model == value)
            update(model)
            precondition(control.secure.stringValue == value)
        }
        update(model, revealed: true)
        precondition(control.plain.stringValue == model && control.secure.stringValue.isEmpty)
        update(model, revealed: false)
        precondition(control.secure.stringValue == model && control.plain.stringValue.isEmpty)
        // Never normalize Unicode or trim meaningful whitespace during synchronization.
        for value in [" 测试-🔑-ru ", "caf\u{e9}", "cafe\u{301}"] {
            update(value)
            precondition(control.secure.stringValue.utf8.elementsEqual(value.utf8))
        }
        update("", enabled: false)
        precondition(control.secure.stringValue.isEmpty && control.plain.stringValue.isEmpty)
        precondition(!control.secure.isEnabled && !control.plain.isEnabled)
        update("restored", enabled: true)
        precondition(control.secure.isEnabled && control.secure.stringValue == "restored")
        print("PASS: stable native password synchronization, visibility toggles, exact UTF-8, clearing and disabling")
    }
}
