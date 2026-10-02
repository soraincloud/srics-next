# 开发、构建与维护

日常开发使用 `develop`，`main` 保存验收版本。维护者通过 GitHub Desktop 推送；外部贡献通过 PR 提交到 `develop`。GitHub Actions 仅 `workflow_dispatch` 手动触发，普通推送和 PR 不运行构建。

## 环境

- Go 1.27.1，或可自动下载该工具链的 Go；需要 C 编译器，因为服务使用 SQLite CGO。
- Node.js 22.12+ 或 24+、npm、Python 3.9+。
- macOS App 打包需要完整 Xcode 27+、Icon Composer 和 actool；运行环境以最终随包组件的最高要求为准。当前交付为 Apple Silicon、macOS 27+。
- `brew install webp` 提供 cwebp。restic 0.19.1 由脚本按固定源码校验值及 `scripts/restic/` 的依赖清单构建，不修改系统 Homebrew。

## 从源码构建

```sh
brew install webp
./scripts/build.sh
./scripts/check.sh
bash scripts/check-security.sh
./scripts/package-macos.sh --skip-build
```

在 Git 检出中构建前先把新增项目源文件加入 Git。构建脚本只收录受 Git 管理的源码；未跟踪的私人文件不进入源码包。从网页登录页下载的源码包解开后，也可直接运行上述脚本，无需原仓库的 Git 历史；包内 `SOURCE_BUILD.json` 记录源码、版本与提交。依赖按锁定清单下载，首次构建需要网络。

`build.sh` 生成网页、固定 restic、源码包和许可，再编译服务并核对源文件未变化。产物在 `bin/`；App 在 `dist/SRICS Next.app`。源码、版本与声明嵌入服务，提供 `/legal/source.tar.gz` 下载，App 也保存相同副本。不要绕过此脚本分发缺少源码与许可的二进制。

修改代码、添加文件或改变可执行权限后需重新构建；源码校验失败时不能直接用 `--skip-build` 打包。版本身份以已验证的源码快照为准，源码导出后重建不会误用父目录其他仓库的提交。

## 检查与发布

`check.sh` 包含前端测试、Swift 密码输入检查、Go vet、竞态测试、恢复集成测试及合成自检；`check-security.sh` 检查应用依赖和固定 restic 的可达漏洞。涉及资料写入的修改还需用独立资料库完成失败与恢复验收。

完整 App 的 Icon Composer 27 打包目前在本机执行；当前 GitHub `macos-14` runner 没有通过该图标构建环境验收，云端工作流只执行源码与服务检查，不承担 App 发布。不要把本机通过当作云端已经通过。没有维护者明确安排，不重复运行 GitHub 任务。

发布前按 [开源发布记录](OPEN_SOURCE_RELEASE.md) 检查许可、源码包、脱敏与实际安装条件。版本及构建号规则见 [VERSIONING.md](VERSIONING.md)。发布包保持程序与资料分离，更新流程不改写用户资料。
