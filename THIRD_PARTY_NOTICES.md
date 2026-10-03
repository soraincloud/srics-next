# 第三方组件与素材

SRICS Next 的原创部分使用 [AGPL-3.0-only](LICENSE)。以下组件保留各自的版权和许可证，项目许可证不替换第三方许可。

| 组件 | 来源及版本记录 | 许可证 |
| --- | --- | --- |
| Vue 及其运行时组件 | [vuejs/core](https://github.com/vuejs/core)，版本见 `web/package-lock.json` | MIT |
| markdown-it 及其运行时依赖 | [markdown-it/markdown-it](https://github.com/markdown-it/markdown-it)，版本见 `web/package-lock.json` | MIT；依赖保留各自许可 |
| age / hpke | [FiloSottile/age](https://github.com/FiloSottile/age)、[FiloSottile/hpke](https://github.com/FiloSottile/hpke)，版本见 `go.mod` | BSD-3-Clause |
| go-sqlite3 | [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)，版本见 `go.mod`；随附 SQLite | MIT；SQLite 为公有领域 |
| minio-go | [minio/minio-go](https://github.com/minio/minio-go)，版本见 `go.mod` | Apache-2.0 |
| Go 工具链及 Go 模块 | `go.mod` / `go.sum` | 各组件的原始许可，随构建收录 |
| restic | [restic/restic](https://github.com/restic/restic)，固定 0.19.1；仅替换依赖清单，见 `scripts/restic/` | BSD-2-Clause；依赖保留各自许可 |
| cwebp / libwebp | [WebP](https://developers.google.com/speed/webp)，macOS 包包含构建机实际安装版本 | BSD 风格许可；依赖保留各自许可 |
| 部分界面图标路径 | [Feather Icons](https://github.com/feathericons/feather)，SRICS 使用并调整部分线条图标 | MIT，全文见 [LICENSES/Feather.txt](LICENSES/Feather.txt) |

应用屏幕图标的分层 SVG 为项目内绘制，采用项目许可证；Icon Composer 是构建工具，不随程序分发。测试 WebP 为程序生成的合成像素，见 [测试素材说明](testdata/media/README.md)。项目不随附用户照片、漫画、小说或资料。

`scripts/prepare-open-source.py` 从已安装的固定 Go / npm 依赖和 restic 的构建记录收录许可证全文，生成网页可获取的 `/legal/THIRD_PARTY_NOTICES.txt`。macOS 打包另外收录实际随包工具和动态库的许可，保存在 `Contents/Resources/licenses/`。安装包内的记录对应具体构建，不依赖本文件中概括的版本描述。
