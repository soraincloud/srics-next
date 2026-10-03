# 版本与构建维护

当前交付：**v0.3.0-rc23 · Build 29**。

## 唯一来源与显示

`internal/buildinfo/release.json` 是产品版本与构建号的唯一代码来源。Go 服务嵌入该文件；网页从运行中的服务读取，macOS 打包从随包服务读取，窗口直接读取包内 `Contents/Resources/release.json`，即使尚未配置资料库也能显示。

- **版本号** `0.3.0-rc23`：当前第 23 个候选版本；验收通过后再去掉 `rc`，不能通过改文案冒充正式版。
- **构建号** `29`：每次交付新包均递增，永不复用旧构建号。重打包或修补同一候选版也需递增。
- **代码版本**：Go 构建信息中的真实 Git 提交；未提交修改会被明确标记。无 Git 元数据时显示“未记录”，不伪造提交。
- **构建时间**：构建脚本记录 UTC 时间。直接 `go build` 没有时间注入时显示“未记录”。

App 和网页主要显示 `v0.3.0-rc23 · Build 29`，详细页附代码版本与构建时间。macOS 标准字段 `CFBundleShortVersionString` 使用三段数字 `0.3.0`，`CFBundleVersion` 为 `29`；完整候选版本保存在 `SRICSVersion` 和包内版本记录中。

## 每次更新

1. 在 `develop` 修改代码、运行相关检查。编辑 `release.json`，递增候选版本 / 修复版本及构建号，更新 `CHANGELOG.md`、README 和安装说明。
2. 提交通过检查的代码后再运行 `scripts/build.sh`，让交付包对应干净、可定位的 Git 提交。服务 `version --json` 的 `dirty` 应为 `false`。
3. 指定独立输出目录，例如 `SRICS_APP_OUTPUT="$PWD/dist/0.3.0-rc23-build29/SRICS Next.app" ./scripts/package-macos.sh --skip-build`。打包会拒绝版本声明与服务二进制不一致的组合。
4. 校验 App 签名及版本：包内 `release.json`、随包 `srics version --json`、Info.plist 的构建号应一致；核对网页来自实际运行的新版服务。
5. 通过 GitHub Desktop 推送 `develop`，不创建 PR、不触发 GitHub 构建。直接交付未压缩应用，在 Finder 中选中；保留旧包供回退参考。

正式版本遵循 `主版本.次版本.修复版本`：兼容性修复递增修复号，兼容的新功能递增次版本；存在不兼容变更时明确说明迁移和回退条件。候选版本逐次递增 `rc`，不以构建号替代功能版本的变更记录。

用户更新前仍需“停止服务并准备更新”，完成备份后退出旧程序，再替换并启动。单纯看到新版窗口不代表后台已切换；网页显示的是实际运行的服务版本。
