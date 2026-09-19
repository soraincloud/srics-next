# 本机启动程序

2026-09-19。当前 macOS 开发包：`dist/SRICS Next.app`，由 `scripts/package-macos.sh` 构建，包含 SwiftUI 配置窗口、内嵌网页的 Go 服务、cwebp / restic 及其非系统动态库。

## 使用

1. 双击 `SRICS Next.app`（或仓库中的 `start.command`）。首次设置登录密码，至少 12 个字符、最多 72 字节。没有默认密码。
2. 默认沿用 `~/Library/Application Support/SRICS Next/library`。首次选择其他存放位置时，使用其中的 `SRICS-library` 子目录。保存配置后目录固定；迁移、恢复不会作为修改路径操作隐式进行。
3. 可选配置独立硬盘上的备份目录和独立口令文件。口令文件需为普通文件、权限 600、至少 12 字节。备份目录不能与资料目录相互包含，口令文件必须位于这两个目录之外；符号链接也检查真实路径。
4. 点击“保存并启动”，程序打开浏览器。后续只需“启动服务”。关闭配置窗口或终端不停止服务；需要停止时重新打开程序并点击“停止服务”。
5. 修改端口、密码或备份配置前先停止服务。已有登录密码留空保留，修改时需填写旧密码。启动后浏览器原会话失效，重新登录即可。

当前仅监听本机 `127.0.0.1`。后台服务在当前登录会话中运行，不注册开机或登录自启。重启电脑后重新打开程序并启动即可。移动或更新程序包前先停止服务。

原有默认资料库直接沿用，不覆盖密码。已存在且无法识别的目录拒绝覆盖；已配置资料盘缺失拒绝创建空库。旧开发命令 `srics serve` 若仍在运行，需要先结束旧进程释放端口和资料库锁。

## 配置与日志

- `~/Library/Application Support/SRICS Next/config.json`：资料目录、端口、备份路径，权限 600，不含登录密码和备份口令正文。
- 登录密码：仅 bcrypt 散列存于资料库数据库；通过本机 stdin 管道设置，不放进命令参数、偏好设置或日志。
- `service.plist`：后台运行参数。由 launchctl 在用户 GUI 域加载，服务标识按配置路径派生；没有 root 权限、KeepAlive 或开机自启。
- `service.log`：运行日志，配置窗口可打开。启动检查端口、密码和依赖，等待 HTTP 就绪；失败不会显示为启动成功。
- `.manager.lock`：防止并发配置、启停操作；资料库进程锁仍防止运行中改密码或重复使用同一库。

网页 `/api/auth/setup` 已关闭，创建和修改密码仅本机配置命令可执行。网页保留登录、退出、认证与 CSRF 校验。

## 开发与验证

```sh
./scripts/check.sh
./scripts/package-macos.sh --skip-build
python3 scripts/check-launcher.py 'dist/SRICS Next.app/Contents/Resources/bin/srics'
```

CLI 本机接口：`srics manager info|configure|start|stop [--config /absolute/config.json]`。`configure` 从标准输入读取 JSON：`config` 包含 `data`、`port`、`backupRepository`、`backupPasswordFile`，另有 `password` 与 `currentPassword`。真实口令不得写入脚本、shell 历史或仓库。图形窗口是日常使用入口。

自动化测试覆盖无密码拒绝启动、弱密码拒绝、旧密码验证、配置失败不替换密码、配置文件权限、运行中配置拒绝、资料盘缺失、旧库兼容、路径嵌套与符号链接、端口占用、后台存活、重复启停、网页创建密码拒绝、登录及重启会话失效。打包检查所有非系统动态库已包含，并执行签名验证；运行测试清空 Homebrew 路径以验证随包依赖。

当前程序包仅临时签名，未完成 Apple 公证，也未做其他机器 / OS 版本兼容性验收。网页功能边界不变：小说、独立私密保险库、云端与自动备份、局域网 HTTPS 仍待后续实现。
