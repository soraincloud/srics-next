import AppKit
import SwiftUI

// Keep AppKit's editor alive while SwiftUI refreshes the surrounding form.
// Secure input must not enter a Pinyin composition session between keystrokes.
final class DirectSecureTextField: NSSecureTextField {
    override func becomeFirstResponder() -> Bool {
        let accepted = super.becomeFirstResponder()
        if accepted, let editor = currentEditor() as? NSTextView {
            editor.allowedInputSourceLocales = [NSAllRomanInputSourcesLocaleIdentifier]
            editor.inputContext?.allowedInputSourceLocales = [NSAllRomanInputSourcesLocaleIdentifier]
        }
        return accepted
    }
}

final class PasswordInputControl: NSView, NSTextFieldDelegate {
    let secure = DirectSecureTextField()
    let plain = NSTextField()
    private(set) var revealed = false
    private var lastText = ""
    var onText: (String) -> Void = { _ in }
    var onFocus: (Bool) -> Void = { _ in }
    private weak var editingView: NSTextView?
    private var applying = false
    private var pendingText: String?
    var active: NSTextField { revealed ? plain : secure }

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        for field in [secure, plain] {
            field.isBordered = false
            field.isBezeled = false
            field.drawsBackground = false
            field.focusRingType = .none
            field.font = .systemFont(ofSize: 13)
            field.textColor = .labelColor
            field.isEditable = true
            field.isSelectable = true
            field.usesSingleLineMode = true
            field.lineBreakMode = .byClipping
            field.delegate = self
            field.translatesAutoresizingMaskIntoConstraints = false
            field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
            addSubview(field)
            NSLayoutConstraint.activate([
                field.leadingAnchor.constraint(equalTo: leadingAnchor),
                field.trailingAnchor.constraint(equalTo: trailingAnchor),
                field.centerYAnchor.constraint(equalTo: centerYAnchor)
            ])
        }
        plain.isHidden = true
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }
    override var intrinsicContentSize: NSSize { NSSize(width: NSView.noIntrinsicMetric, height: 18) }
    private func equal(_ a: String, _ b: String) -> Bool { a.utf8.elementsEqual(b.utf8) }

    func update(text: String, revealed nextRevealed: Bool, enabled: Bool, title: String, placeholder: String) {
        applying = true
        defer { applying = false }
        // Only an actual external model change may replace the live editor.
        // In particular, unrelated form updates must not replace marked text.
        let externalChange = !equal(text, lastText) && pendingText == nil
        let previous = active
        let editor = previous.currentEditor() as? NSTextView
        let hadFocus = editor != nil
        let selection = editor?.selectedRange()
        if nextRevealed != revealed {
            if hadFocus { window?.makeFirstResponder(nil) }
            let value = externalChange ? text : previous.stringValue
            revealed = nextRevealed
            secure.isHidden = revealed
            plain.isHidden = !revealed
            active.stringValue = value
            lastText = value
            if hadFocus && enabled {
                window?.makeFirstResponder(active)
                if let selection, let nextEditor = active.currentEditor() as? NSTextView {
                    let start = min(selection.location, nextEditor.string.utf16.count)
                    nextEditor.setSelectedRange(NSRange(location: start, length: min(selection.length, nextEditor.string.utf16.count - start)))
                }
            }
        } else if externalChange {
            // A deliberate clear/disable must also clear uncommitted input.
            if let editor, editor.hasMarkedText() {
                if !text.isEmpty && enabled { return }
                editor.inputContext?.discardMarkedText()
            }
            if !equal(active.stringValue, text) { active.stringValue = text }
            lastText = text
        }
        for field in [secure, plain] {
            field.placeholderString = placeholder
            field.setAccessibilityLabel(title)
            field.isEnabled = enabled
        }
        if !enabled && hadFocus { window?.makeFirstResponder(nil) }
        // Do not retain an extra copy of a password in the inactive field.
        (revealed ? secure : plain).stringValue = ""
    }
    private func publish(_ field: NSTextField) {
        // Mirror the live editor without writing it back. This includes marked
        // text: an unrelated form refresh must leave IME composition untouched.
        let value = field.currentEditor()?.string ?? field.stringValue
        guard !equal(value, lastText) else { return }
        lastText = value
        if applying {
            pendingText = value
            DispatchQueue.main.async { [weak self] in
                guard let self, let latest = self.pendingText else { return }
                self.pendingText = nil
                self.onText(latest)
            }
        } else { onText(value) }
    }
    func controlTextDidChange(_ notification: Notification) {
        guard let field = notification.object as? NSTextField else { return }
        publish(field)
    }
    func controlTextDidBeginEditing(_ notification: Notification) {
        if let field = notification.object as? NSTextField, let editor = field.currentEditor() as? NSTextView {
            editingView = editor
            editor.isAutomaticTextReplacementEnabled = false
            editor.isAutomaticSpellingCorrectionEnabled = false
            editor.isAutomaticQuoteSubstitutionEnabled = false
            editor.isAutomaticDashSubstitutionEnabled = false
            editor.allowedInputSourceLocales = field === secure ? [NSAllRomanInputSourcesLocaleIdentifier] : nil
            editor.inputContext?.allowedInputSourceLocales = editor.allowedInputSourceLocales
        }
        reportFocus()
    }
    func controlTextDidEndEditing(_ notification: Notification) {
        if let field = notification.object as? NSTextField { publish(field) }
        editingView?.allowedInputSourceLocales = nil
        editingView?.inputContext?.allowedInputSourceLocales = nil
        editingView = nil
        reportFocus()
    }
    private func reportFocus() {
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.onFocus(self.active.currentEditor() != nil)
        }
    }
}

struct NativePasswordInput: NSViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool
    let revealed: Bool
    let enabled: Bool
    let title: String
    let placeholder: String
    func makeNSView(context: Context) -> PasswordInputControl { PasswordInputControl() }
    func updateNSView(_ view: PasswordInputControl, context: Context) {
        view.onText = { if !text.utf8.elementsEqual($0.utf8) { text = $0 } }
        view.onFocus = { focused = $0 }
        view.update(text: text, revealed: revealed, enabled: enabled, title: title, placeholder: placeholder)
    }
}
