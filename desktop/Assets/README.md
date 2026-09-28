# SRICS Next 应用图标

`AppIcon.png` 是项目内保存的透明 PNG 原图（1254 × 1254）。使用内置 `image_gen` 生成，2026-09-29；不是 CLI/API 回退。打包脚本使用系统 sips 生成各尺寸 PNG，再用 iconutil 生成 `AppIcon.icns`，不依赖生成器目录。

视觉：黑色圆角底、银白折页构成的 S、克制的蓝色高光。对应资料收藏与页面归档，并沿用现有 Global 配色。没有文字、小锁或细小功能图案。

## 完整生成提示词

```text
Use case: logo-brand. Asset type: production macOS application icon for SRICS Next, a private personal media library and encrypted archive. Create one exceptionally polished native Mac app icon, square 1024 by 1024 composition, straight-on and perfectly centered. A graphite-black continuous rounded-square tile occupies about 84 percent of the canvas, with genuinely transparent space outside its rounded silhouette. The tile has very subtle satin depth and a softly highlighted upper edge, restrained and precise, not glossy plastic. In its center a bold luminous ivory/silver abstract S is formed from two broad folded archive-card ribbons, suggesting neatly stacked pages in a secure personal collection. The S must read immediately at small Dock sizes, with simple flowing geometric contours, spacious negative space and absolutely no tiny line work. One extremely restrained icy cyan glint on an inner fold may echo the app's existing #00a4e8 accent. Visual language: minimal premium international productivity software, mostly black and off-white, calm proportions, slight tactile dimensionality, elegant lighting, clean antialiased edges. No wordmark, no written text, no extra letters, no padlock, no busy filing cabinet illustration, no border frames, no background scene, no perspective tilt, no floor, no grid, no multiple variations, no mockup, no watermark. The artwork itself must be usable directly as an app icon with real alpha transparency around the tile, not a checkerboard drawn into the image.
```
