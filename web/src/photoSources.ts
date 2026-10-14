const maxPhoto = 64 * 1024 * 1024;
const imagePattern = /\.(heic|heif|jpe?g|png)$/i;
const stem = (name: string) => name.replace(/\.[^.]+$/, "").normalize("NFC").toLowerCase();
const crcTable = Array.from({ length: 256 }, (_, n) => {
  for (let i = 0; i < 8; i++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1;
  return n >>> 0;
});

async function livePhoto(image: File, movie: File, signal?: AbortSignal): Promise<File> {
  if (image.size + movie.size > maxPhoto) throw new Error("实况照片与配套视频合计最多 64 MiB。");
  const parts: BlobPart[] = [], directory: Uint8Array<ArrayBuffer>[] = [];
  let offset = 0, directorySize = 0;
  for (const file of [image, movie]) {
    signal?.throwIfAborted();
    const name = new TextEncoder().encode(file.name);
    const bytes = new Uint8Array(await file.arrayBuffer());
    signal?.throwIfAborted();
    let crc = 0xffffffff;
    for (let i = 0; i < bytes.length; i++) {
      crc = crcTable[(crc ^ bytes[i]!) & 255]! ^ (crc >>> 8);
      if (i > 0 && i % (4 * 1024 * 1024) === 0) {
        await new Promise(resolve => setTimeout(resolve, 0));
        signal?.throwIfAborted();
      }
    }
    signal?.throwIfAborted();
    crc = (crc ^ 0xffffffff) >>> 0;
    const header = new Uint8Array(30 + name.length), h = new DataView(header.buffer);
    h.setUint32(0, 0x04034b50, true); h.setUint16(4, 20, true);
    h.setUint16(6, 0x800, true); h.setUint16(12, 0x21, true);
    h.setUint32(14, crc, true); h.setUint32(18, bytes.length, true); h.setUint32(22, bytes.length, true);
    h.setUint16(26, name.length, true); header.set(name, 30);
    const central = new Uint8Array(46 + name.length), c = new DataView(central.buffer);
    c.setUint32(0, 0x02014b50, true); c.setUint16(4, 20, true); c.setUint16(6, 20, true);
    c.setUint16(8, 0x800, true); c.setUint16(14, 0x21, true);
    c.setUint32(16, crc, true); c.setUint32(20, bytes.length, true); c.setUint32(24, bytes.length, true);
    c.setUint16(28, name.length, true); c.setUint32(42, offset, true); central.set(name, 46);
    parts.push(header, bytes); directory.push(central);
    offset += header.length + bytes.length; directorySize += central.length;
  }
  const end = new Uint8Array(22), e = new DataView(end.buffer);
  e.setUint32(0, 0x06054b50, true); e.setUint16(8, 2, true); e.setUint16(10, 2, true);
  e.setUint32(12, directorySize, true); e.setUint32(16, offset, true);
  const file = new File([...parts, ...directory, end], image.name.replace(/\.[^.]+$/, ".livephoto.zip"), { type: "application/zip", lastModified: 0 });
  if (file.size > maxPhoto) throw new Error("实况照片两部分及归档合计最多 64 MiB。");
  signal?.throwIfAborted();
  return file;
}

// Pair by the filenames used by Apple's unmodified-original export. Preserve
// every byte and name; ambiguous or missing partners never get silently skipped.
export async function preparePhotoSources(files: File[], signal?: AbortSignal): Promise<File[]> {
  const images = new Map<string, File[]>(), movies = new Map<string, File[]>();
  for (const f of files) {
    const table = imagePattern.test(f.name) ? images : /\.mov$/i.test(f.name) ? movies : undefined;
    if (table) table.set(stem(f.name), [...(table.get(stem(f.name)) || []), f]);
  }
  for (const [key, videos] of movies) {
    if (videos.length !== 1 || images.get(key)?.length !== 1)
      throw new Error(`“${videos[0]!.name}”需要唯一的同名照片配对，请同时选择照片与 MOV；独立视频可保存到文件。`);
  }
  const result: File[] = [];
  for (const f of files) {
    signal?.throwIfAborted();
    if (/\.mov$/i.test(f.name)) continue;
    const movie = imagePattern.test(f.name) ? movies.get(stem(f.name))?.[0] : undefined;
    result.push(movie ? await livePhoto(f, movie, signal) : f);
  }
  signal?.throwIfAborted();
  return result;
}
