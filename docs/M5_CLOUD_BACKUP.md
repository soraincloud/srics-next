# M5 云端加密备份

2026-09-20，实施记录。本批接入通用 S3 兼容存储；供应商、区域、存储桶和真实凭据由实际部署配置。不会将本地测试备份标记为云端副本。

## 本批范围

- 本机程序配置 HTTPS Endpoint、区域、存储桶、专用前缀、寻址方式、凭据文件与云端备份口令文件。
- 云端与本地备份同时保留，各自显示目的地、最近成功快照、错误及下次执行时间。沿用每日计划与失败重试，同一时刻只执行一项备份。
- 本机提供只读连接检查。只有检查确认存储桶已存在、专用前缀为空后才初始化新仓库；遇到非空前缀、错误口令或权限错误时停止。
- 云端上传 restic 加密内容，包含 SQLite 一致性快照、全部对象及保险库密钥包装。凭据正文、独立备份口令文件、HTTPS 私钥不进入资料库快照或网页响应。
- 恢复命令支持独立 S3 配置与凭据，写入不存在的新目录，恢复后核对索引与对象校验值。

## 凭据与配置边界

凭据通过本机选择的 JSON 文件读取（权限 600），字段 `accessKeyId`、`secretAccessKey`、可选 `sessionToken`。文件必须位于资料目录和本地备份目录之外，不写入 Git，不能同时用作备份口令文件。restic 子进程只接收当前目标明确配置的凭据，忽略环境中其他云账号、角色或调试设置。首次实际部署时使用限定到目标桶 / 前缀的权限，包括读取、列举、写入及删除 restic 临时锁对象；不要授予创建桶或修改桶权限。

Endpoint 使用 HTTPS 并正常验证服务器证书；不支持关闭验证。私有 S3 服务可配置 PEM CA 文件。选择 OSS 时使用对应区域与 DNS 寻址，其他兼容服务按供应商要求选择。

每次云端备份结束执行完整读取检查，会产生云端读取请求 / 下载流量；费用取决于选定服务商。云服务选择和实际费用尚未确认。本批不删除历史快照，不配置公共访问，不修改桶权限。

## 配置与恢复

停止服务，在本机窗口展开“云端加密备份”，填写存储连接并选择两个独立文件。凭据文件示例（占位值，不能直接用于访问）：

```json
{"accessKeyId":"YOUR_ACCESS_KEY_ID","secretAccessKey":"YOUR_SECRET_ACCESS_KEY"}
```

备份口令文件为单独的纯文本口令，至少 12 字节。两个文件权限均为 600。点击“检查云端连接”只检查桶与前缀，已有仓库还会检查解密口令；检查成功不等于写入或恢复已验证。保存并启动后，在网页备份中心切换“本地 / 独立硬盘”和“云端”，分别手动执行或等待每日计划。

独立恢复不需要原主机 config.json。准备一份不含秘密的 S3 连接 JSON，例如：

```json
{"endpoint":"https://s3.example.com","region":"us-east-1","bucket":"your-bucket","prefix":"srics/main","lookup":"path","caFile":""}
```

```sh
./bin/srics restore \
  --s3-config '/恢复材料/s3-connection.json' \
  --credentials-file '/恢复材料/s3-credentials.json' \
  --password-file '/恢复材料/cloud-password.txt' \
  --snapshot '云端备份中心显示的完整快照编号' \
  --target '/Volumes/Restore/SRICS-restored'
```

目标目录必须不存在；恢复后核对索引和对象哈希。私密资料还需独立保险库口令。云端与本地使用各自的快照编号，不混用。

## 验证

使用隔离的本地 HTTPS S3 协议测试服务与合成凭据，运行真实 restic 初始化、上传、完整性检查和恢复；测试服务验证 SigV4 请求签名。覆盖错误凭据、错误口令、非空前缀、丢失仓库、凭据路径与权限、目标状态隔离及请求互斥，检查上传对象不包含明文测试内容或文件名。真实云端上传和跨设备恢复在提供目标后验收。

`./scripts/check.sh` 全部通过（前端 9 项测试、TypeScript/Vite、Go vet/race、集成恢复与 11 项 M0 验证）。macOS 包编译与启动器生命周期检查通过。手工验收本机云端字段及未填写配置提示、网页目标状态隔离、390px 窄屏布局；新版演示库手动本地备份及全量读取检查成功。

参考：[restic S3 配置](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html#s3-compatible-storage)、[restic 仓库探测与退出码](https://restic.readthedocs.io/en/stable/075_scripting.html#checking-if-a-repository-is-already-initialized)。

本机恢复窗口与历史快照浏览已补齐，见 [M5 恢复记录](M5_RECOVERY.md)；上面的命令行入口继续保留。
