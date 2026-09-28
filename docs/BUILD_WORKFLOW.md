# 本机开发与 GitHub 构建

2026-09-29 起，日常工作使用 `develop`，`main` 保留已验收版本。用 GitHub Desktop 推送；不创建 PR。检查和 `.app` 打包在本机执行，交付未压缩应用并在 Finder 中选中。

`.github/workflows/check.yml` 仅保留 `workflow_dispatch`。普通推送、分支发布、合并和 PR 都不触发工作流；没有用户明确要求，不点击 Run workflow 或 Re-run jobs。不通过关闭邮件通知来掩盖自动构建。

已核对的最后一次自动任务为 [#46](https://github.com/soraincloud/srics-next/actions/runs/36466971479)，对应 `be2fb92`，耗时 6 分 13 秒并失败。日志为 `TestLockInvalidatesAlreadyPendingUnlock` 等待解锁结束超过 10 秒；本机使用原样测试及 `-race -count=1` 复查通过（测试 8.63 秒），未复现云端超时，未因此放宽断言或宣称云端已通过。该任务已结束，没有重新运行。主分支停止自动触发的修复和 `develop` 发布后，远端工作流仍为 46 次。

下一次确实需要云端打包前，先检查 runner 与 Xcode：新的分层图标由 Icon Composer 27 制作，当前工作流的 `macos-14` 环境不应被当作已验证的图标构建环境。先更新为支持相应 Xcode 的 runner，再处理上述超时的环境差异，最后由用户决定是否消耗 GitHub 额度运行。当前 build 10 已在本机使用 Xcode 27 成功打包并验证签名。
