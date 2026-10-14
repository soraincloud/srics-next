# 图像与视频样本

此目录只包含项目测试用的合成素材，不含个人照片。

`synthetic-photo.png/heic/avif/bmp/tiff` 是 48 × 32 的合成渐变，方向元数据为 6，EXIF 日期固定为 2000-01-01。`synthetic-photo.MOV` 为程序生成的短 H.264 视频，配对标识为合成字符串；它用于验证文件保存、网页媒体读取和恢复，不是相机拍摄的实况照片，也不验证 Apple 照片应用自动配对。

原有 WebP 样本为 16 × 12，源 RGBA 像素是 `(x*17, y*23, (x*13+y*7)%256, ((x+y)%4)*85)`：

- `lossless-alpha.webp` 使用 `cwebp -lossless -exact`。
- `lossy-alpha.webp` 使用 `cwebp -q 55`，验证已有有损 WebP 与透明通道仍原字节保留。
- `lossless-bare.webp` 为第一份样本的 VP8L 图像块，使用不带元数据的简单 RIFF 容器，验证独立缩略图。

前两份容器包含 VP8X、EXIF（空 IFD 的小端 TIFF）、合成 XMP 和带奇数长度负载的未知 JUNK 块。测试应保留整份文件的元数据、未知块和填充；编码器是否可用不影响已有 WebP 的导入。所有测试素材遵循项目许可证。
