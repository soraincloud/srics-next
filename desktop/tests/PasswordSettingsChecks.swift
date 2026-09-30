import Foundation

@main struct PasswordSettingsChecks {
    static func main() {
        let login = "synthetic-login-123"
        let vault = "synthetic-private-456"
        let valid = PasswordSettings(login: login, loginConfirmation: login, vault: vault, vaultConfirmation: vault)
        precondition(valid.issues(loginExists: false, vaultExists: false).isEmpty)
        var mismatch = valid
        mismatch.loginConfirmation = "different-login-123"
        precondition(mismatch.issues(loginExists: false, vaultExists: false).map(\.field) == ["repeatedPassword"])
        mismatch = valid; mismatch.vaultConfirmation = "different-private-456"
        precondition(mismatch.issues(loginExists: false, vaultExists: false).map(\.field) == ["repeatedVaultPassword"])
        precondition(PasswordSettings().issues(loginExists: false, vaultExists: false).first?.field == "password")
        precondition(PasswordSettings().issues(loginExists: true, vaultExists: true).isEmpty)
        precondition(PasswordSettings(loginConfirmation: "typed-only-confirmation").hasDraft)
        // These compare equal in Swift but produce different bcrypt inputs.
        let composed = "synthetic-caf\u{e9}-123"
        let decomposed = "synthetic-cafe\u{301}-123"
        precondition(composed == decomposed)
        let unicode = PasswordSettings(login: composed, loginConfirmation: decomposed)
        precondition(unicode.issues(loginExists: false, vaultExists: false).first?.field == "repeatedPassword")
        let exact = PasswordSettings(login: decomposed, loginConfirmation: decomposed)
        precondition(exact.issues(loginExists: false, vaultExists: false).isEmpty)
        let spaced = " synthetic-login-123 "
        precondition(PasswordSettings(login: spaced, loginConfirmation: spaced).issues(loginExists: false, vaultExists: false).isEmpty)
        let multiline = "synthetic-login\n123"
        precondition(PasswordSettings(login: multiline, loginConfirmation: multiline).issues(loginExists: false, vaultExists: false).first?.field == "password")
        let long = String(repeating: "中", count: 25)
        precondition(PasswordSettings(login: long, loginConfirmation: long).issues(loginExists: false, vaultExists: false).first?.field == "password")
        print("PASS: password field attribution, exact UTF-8 confirmation, draft tracking, and length validation")
    }
}
