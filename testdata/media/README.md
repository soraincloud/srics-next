# Synthetic WebP preservation fixtures

These 16 × 12 images were generated locally for tests; they contain no user data.
The source is an 8-bit RGBA PNG with pixels `(x*17, y*23, (x*13+y*7)%256, ((x+y)%4)*85)`.

- `lossless-alpha.webp`: cwebp `-lossless -exact`.
- `lossy-alpha.webp`: cwebp `-q 55`, exercising existing lossy WebP as well as alpha.
- `lossless-bare.webp`: the first fixture's VP8L image chunk in a simple RIFF
  container, without metadata, to exercise separate thumbnail generation.

The first two containers have a VP8X header, EXIF (little-endian TIFF with an empty IFD),
synthetic XMP, and an unknown `JUNK` chunk with an odd payload length. Tests must
preserve the entire file, including metadata, unknown chunks and padding, not
merely equivalent decoded pixels. Encoder availability must not affect imports.
