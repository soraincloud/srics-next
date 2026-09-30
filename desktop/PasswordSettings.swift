import Foundation

struct PasswordIssue {
    let field: String
    let message: String
}

struct PasswordSettings {
    var current = ""
    var login = ""
    var loginConfirmation = ""
    var currentVault = ""
    var vault = ""
    var vaultConfirmation = ""

    var hasDraft: Bool { [current, login, loginConfirmation, currentVault, vault, vaultConfirmation].contains { !$0.isEmpty } }

    func issues(loginExists: Bool, vaultExists: Bool) -> [PasswordIssue] {
        var result: [PasswordIssue] = []
        if !loginExists || !login.isEmpty || !loginConfirmation.isEmpty || !current.isEmpty {
            if login.isEmpty {
                result.append(.init(field: "password", message: "请输入用于浏览器登录的登录密码。"))
            } else if login.contains("\n") || login.contains("\r") {
                result.append(.init(field: "password", message: "登录密码不能包含换行符，请删除粘贴内容中的换行后重新设置。"))
            } else if login.unicodeScalars.count < 12 || login.utf8.count > 72 {
                result.append(.init(field: "password", message: "登录密码至少 12 个字符，最多 72 字节（中文通常占 3 字节）。"))
            }
            // Swift String equality treats different Unicode encodings as equal.
            // Authentication uses the original UTF-8 bytes, so confirmation must too.
            if !login.utf8.elementsEqual(loginConfirmation.utf8) {
                result.append(.init(field: "repeatedPassword", message: "两次登录密码不一致，请检查“登录密码”和“确认登录密码”。"))
            }
            if loginExists && !login.isEmpty && current.isEmpty {
                result.append(.init(field: "currentPassword", message: "修改登录密码前，请输入当前登录密码。"))
            }
        }
        if !vault.isEmpty || !vaultConfirmation.isEmpty || !currentVault.isEmpty {
            if vault.contains("\n") || vault.contains("\r") {
                result.append(.init(field: "vaultPassword", message: "保险库口令不能包含换行符，请删除后重新设置。"))
            } else if !(12...1024).contains(vault.utf8.count) {
                result.append(.init(field: "vaultPassword", message: "保险库口令需为 12–1024 字节，仅用于解锁私密资料。"))
            }
            if !vault.utf8.elementsEqual(vaultConfirmation.utf8) {
                result.append(.init(field: "repeatedVaultPassword", message: "两次保险库口令不一致，请检查“保险库口令”和“确认保险库口令”。"))
            }
            if vaultExists && !vault.isEmpty && currentVault.isEmpty {
                result.append(.init(field: "currentVaultPassword", message: "修改保险库口令前，请输入当前保险库口令。"))
            }
        }
        return result
    }
}
