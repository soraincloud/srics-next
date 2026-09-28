# SRICS Next 应用图标

当前设计为简洁的屏幕轮廓：浅色玻璃底、白色机身、蓝色屏幕；不使用文字或金属折页。矢量图形手工绘制，玻璃高光、折射、阴影、深浅色和着色外观交给 Apple Icon Composer 渲染。本版未使用图像生成器。

- `AppIcon.icon`：可直接在 Icon Composer 中打开的分层源文件；包含 `Display.svg` 与 `Screen.svg`，两组分别调节玻璃材质。画布为 1024 × 1024。
- `AppIcon.png`：Icon Composer 导出的默认外观预览，不是应用运行时图标源。
- `scripts/package-macos.sh`：用 Xcode 的 `actool` 编译 `.icon`，将 `Assets.car` 和兼容用的 `AppIcon.icns` 放入应用，并合并编译器生成的图标声明。保留系统对分层和外观的支持。

本版使用 Xcode / Icon Composer 27.0 制作，打包需要该版本或更新版本。仅安装完成的 `.app` 不需要 Xcode 或 Icon Composer。

默认外观预览的本机导出命令：

```sh
"$(xcode-select -p)/../Applications/Icon Composer.app/Contents/Executables/ictool" \
  desktop/Assets/AppIcon.icon --export-image \
  --output-file desktop/Assets/AppIcon.png --platform macOS \
  --rendition Default --width 1024 --height 1024 --scale 1
```

已检查 Default、Dark、TintedLight 和 TintedDark 外观，以及应用编译、图标元数据和签名。此前的黑色折页 S 图标保留在 Git 历史中。

参考：[Apple Icon Composer](https://developer.apple.com/icon-composer/)。
