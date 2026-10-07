import assert from "node:assert/strict";
import { test } from "node:test";
import { clampAnchor, createReadingWriter, paragraphAtLine } from "../src/novelReading.ts";

test("paragraph position follows the reading line at different font sizes", () => {
  const rect = (height: number) => (i: number) => ({ top: i * height, bottom: (i + 1) * height, height });
  assert.deepEqual(paragraphAtLine(8, rect(40), 150), { paragraph: 3, fraction: .75 });
  assert.deepEqual(paragraphAtLine(8, rect(80), 300), { paragraph: 3, fraction: .75 });
  assert.deepEqual(paragraphAtLine(8, rect(40), -20), { paragraph: 0, fraction: 0 });
  assert.deepEqual(paragraphAtLine(8, rect(40), 500), { paragraph: 7, fraction: 1 });
  assert.deepEqual(paragraphAtLine(8, rect(40), 160), { paragraph: 4, fraction: 0 });
});
test("a shortened or empty chapter clamps the old location", () => {
  assert.deepEqual(clampAnchor({ paragraph: 30, fraction: .5 }, 2), { paragraph: 1, fraction: .5 });
  assert.deepEqual(clampAnchor({ paragraph: -2, fraction: NaN }, 0), { paragraph: 0, fraction: 0 });
});
test("slow progress requests serialize and keep only the newest pending chapter", async () => {
  const sent: string[] = [];
  let release!: () => void;
  const writer = createReadingWriter<string>(async value => {
    sent.push(value);
    if (value === "chapter-1") await new Promise<void>(r => { release = r; });
  });
  writer.queue("chapter-1");
  const pending = writer.flush();
  writer.queue("chapter-2");
  writer.queue("chapter-3");
  assert.equal(writer.flush(), pending);
  assert.deepEqual(sent, ["chapter-1"]);
  release();
  assert.equal(await pending, true);
  assert.deepEqual(sent, ["chapter-1", "chapter-3"]);
});
test("a failed save can retry without discarding a newer position", async () => {
  let fail!: () => void;
  const sent: string[] = [];
  const writer = createReadingWriter<string>(async value => {
    sent.push(value);
    if (sent.length === 1) await new Promise<void>((_, reject) => { fail = () => reject(new Error("offline")); });
  });
  writer.queue("old");
  const pending = writer.flush();
  writer.queue("latest");
  fail();
  assert.equal(await pending, false);
  assert.equal(await writer.flush(), true);
  assert.deepEqual(sent, ["old", "latest"]);
});
