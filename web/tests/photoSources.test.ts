import test from "node:test";
import assert from "node:assert/strict";
import { preparePhotoSources } from "../src/photoSources.ts";
import { prepareTransfer, sendTransfer, type Transfer } from "../src/transfers.ts";

test("photo originals remain unchanged; a matching MOV becomes one reproducible item", async () => {
  const image = new File(["synthetic-heic-original"], "旅行.HEIC"), video = new File(["synthetic-video-with-audio"], "旅行.MOV");
  const ordinary = new File(["ordinary-original"], "plain.png");
  const [bundle, kept] = await preparePhotoSources([image, video, ordinary]);
  const repeated = (await preparePhotoSources([video, image]))[0]!;
  assert.equal(kept, ordinary);
  assert.equal(bundle!.name, "旅行.livephoto.zip");
  const bytes = new Uint8Array(await bundle!.arrayBuffer());
  assert.deepEqual(bytes, new Uint8Array(await repeated.arrayBuffer()));
  const view = new DataView(bytes.buffer);
  let off = 0;
  for (const file of [image, video]) {
    assert.equal(view.getUint32(off, true), 0x04034b50);
    assert.equal(view.getUint16(off + 8, true), 0); // stored, never compressed
    const size = view.getUint32(off + 18, true), nameSize = view.getUint16(off + 26, true);
    assert.equal(new TextDecoder().decode(bytes.subarray(off + 30, off + 30 + nameSize)), file.name);
    const body = bytes.subarray(off + 30 + nameSize, off + 30 + nameSize + size);
    assert.deepEqual(body, new Uint8Array(await file.arrayBuffer()));
    off += 30 + nameSize + size;
  }
  assert.equal(view.getUint32(off, true), 0x02014b50);
});

test("missing, ambiguous and cancelled photo pairs fail without dropping files", async () => {
  const mov = new File(["movie"], "same.mov");
  await assert.rejects(preparePhotoSources([mov]), /同名照片/);
  await assert.rejects(preparePhotoSources([mov, new File(["a"], "same.heic"), new File(["b"], "same.jpg")]), /唯一/);
  const c = new AbortController(); c.abort();
  await assert.rejects(preparePhotoSources([new File(["image"], "alone.heic")], c.signal), { name: "AbortError" });
});

test("cancelling while a small original is being read never returns a ready upload", async () => {
  const c = new AbortController();
  const image = new File(["image"], "same.heic"), video = new File(["video"], "same.mov");
  const read = image.arrayBuffer.bind(image);
  Object.defineProperty(image, "arrayBuffer", { value: async () => {
    const bytes = await read();
    c.abort();
    return bytes;
  }});
  await assert.rejects(preparePhotoSources([image, video], c.signal), { name: "AbortError" });
});

test("equivalent Unicode filenames pair without renaming either original", async () => {
  for (const [imageName, movieName] of [["caf\u00e9.heic", "cafe\u0301.MOV"], ["\u0130.heic", "i\u0307.MOV"], ["\u039f\u03a3.heic", "\u03bf\u03c2.MOV"]]) {
  const image = new File(["image"], imageName), movie = new File(["video"], movieName);
  const [bundle] = await preparePhotoSources([image, movie]);
  const bytes = new Uint8Array(await bundle!.arrayBuffer());
  const names: string[] = [];
  const view = new DataView(bytes.buffer);
  for (let off = 0; names.length < 2;) {
    const length = view.getUint16(off + 26, true), size = view.getUint32(off + 18, true);
    names.push(new TextDecoder().decode(bytes.subarray(off + 30, off + 30 + length)));
    off += 30 + length + size;
  }
  assert.deepEqual(names, [image.name, movie.name]);
  }
});

test("reselecting both live photo originals resumes the saved chunks", async () => {
  const image = new File([new Uint8Array(9 * 1024 * 1024)], "same.heic");
  const movie = new File(["video-original"], "same.MOV");
  const [first] = await preparePhotoSources([image, movie]);
  const [again] = await preparePhotoSources([movie, image]);
  const task = { id: "saved-live-photo", module: "private", parent: "", index: 0 };
  let saved: Transfer | undefined;
  const sent: string[] = [];
  const request = async <T>(path: string, options: RequestInit = {}): Promise<T> => {
    if (path === "/api/vault/transfers") {
      const registration = JSON.parse(String(options.body)) as Transfer;
      if (saved) {
        assert.equal(registration.name, saved.name);
        assert.equal(registration.size, saved.size);
        assert.deepEqual(registration.hashes, saved.hashes);
      } else saved = { ...registration, created: "", state: "pending", done: [0] };
      return saved as T;
    }
    sent.push(path);
    return {} as T;
  };
  const signal = new AbortController().signal;
  await prepareTransfer(first!, task, request, signal, () => {});
  const prepared = await prepareTransfer(again!, task, request, signal, () => {});
  await sendTransfer(again!, task, request, signal, () => {}, prepared);
  assert.deepEqual(sent, ["/api/vault/transfers/saved-live-photo/chunks/1", "/api/vault/transfers/saved-live-photo/finish"]);
});
