import Foundation
import ImageIO

// Only stdin/stdout are used: private photo bytes never become a temp file.
func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

let args = CommandLine.arguments
guard args.count == 3, let edge = Int(args[1]), (0...2560).contains(edge),
      let pixelLimit = Int(args[2]), pixelLimit > 0 else { fail("Invalid preview limits") }
var input = Data()
do {
    while let chunk = try FileHandle.standardInput.read(upToCount: 64 * 1024), !chunk.isEmpty {
        guard input.count <= 64 * 1024 * 1024 - chunk.count else { fail("Image exceeds input limit") }
        input.append(chunk)
    }
} catch { fail("Unable to read image") }
guard let source = CGImageSourceCreateWithData(input as CFData, [kCGImageSourceShouldCache: false] as CFDictionary),
      let type = CGImageSourceGetType(source) as String? else { fail("Unsupported image") }
let types = [
    "public.jpeg": "image/jpeg", "public.png": "image/png",
    "org.webmproject.webp": "image/webp", "com.compuserve.gif": "image/gif",
    "public.heic": "image/heic", "public.heics": "image/heic", "public.heif": "image/heif",
    "public.avif": "image/avif", "public.avis": "image/avif",
    "com.microsoft.bmp": "image/bmp", "public.tiff": "image/tiff",
]
guard let mime = types[type], CGImageSourceGetCount(source) > 0 else { fail("Unsupported image type") }
let index = CGImageSourceGetPrimaryImageIndex(source)
guard let props = CGImageSourceCopyPropertiesAtIndex(source, index, nil) as? [CFString: Any],
      let width = (props[kCGImagePropertyPixelWidth] as? NSNumber)?.intValue,
      let height = (props[kCGImagePropertyPixelHeight] as? NSNumber)?.intValue,
      width > 0, height > 0 else { fail("Invalid image dimensions") }
guard width <= pixelLimit / height else { fail("Image exceeds pixel limit") }
let depth = (props[kCGImagePropertyDepth] as? NSNumber)?.intValue ?? 0
let auxiliaryTypes = [kCGImageAuxiliaryDataTypeDepth, kCGImageAuxiliaryDataTypeDisparity,
    kCGImageAuxiliaryDataTypePortraitEffectsMatte, kCGImageAuxiliaryDataTypeHDRGainMap]
let auxiliary = auxiliaryTypes.contains { CGImageSourceCopyAuxiliaryDataInfoAtIndex(source, index, $0) != nil }
if edge == 0 && (depth < 1 || depth > 8 || CGImageSourceGetCount(source) != 1 || auxiliary) {
    fail("Image has high bit depth or auxiliary data; preserve original")
}
let options: [CFString: Any] = [
    kCGImageSourceCreateThumbnailFromImageAlways: true,
    kCGImageSourceCreateThumbnailWithTransform: true,
    kCGImageSourceThumbnailMaxPixelSize: edge,
    kCGImageSourceShouldCacheImmediately: true,
]
guard let image = edge == 0 ? CGImageSourceCreateImageAtIndex(source, index, nil)
                           : CGImageSourceCreateThumbnailAtIndex(source, index, options as CFDictionary)
else { fail("Unable to decode image") }
if edge > 0 && (image.width > edge || image.height > edge) { fail("Invalid preview size") }
let output = NSMutableData()
guard let dest = CGImageDestinationCreateWithData(output, "public.png" as CFString, 1, nil) else { fail("Unable to create preview") }
CGImageDestinationAddImage(dest, image, edge == 0 ? props as CFDictionary
    : [kCGImagePropertyOrientation: 1] as CFDictionary)
guard CGImageDestinationFinalize(dest), output.length <= (edge == 0 ? 512 : 32) * 1024 * 1024 else { fail("Unable to encode preview") }
do {
    let header = try JSONSerialization.data(withJSONObject: ["mime": mime, "width": width, "height": height,
        "depth": depth, "images": CGImageSourceGetCount(source), "auxiliary": auxiliary])
    FileHandle.standardOutput.write(header)
    FileHandle.standardOutput.write(Data([10]))
    FileHandle.standardOutput.write(output as Data)
} catch { fail("Unable to write preview") }
