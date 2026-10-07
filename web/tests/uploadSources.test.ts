import test from "node:test";
import assert from "node:assert/strict";
import { matchUploadSources } from "../src/uploadSources.ts";

test("numbered images can resume after the source was renamed", () => {
  const source = new File(["same-image-bytes"], "renamed.webp");
  const task = { module: "images", autoName: true, files: [{ name: "IMG-000001", size: source.size }] };
  assert.deepEqual(matchUploadSources(task, [source]), [source]);
  assert.throws(() => matchUploadSources(task, []));
  assert.throws(() => matchUploadSources(task, [source, source]));
  assert.throws(() => matchUploadSources(task, [new File(["short"], "source.webp")]));
});

test("existing images and personal photos still require their original name", () => {
  const source = new File(["same-image-bytes"], "original.webp");
  const renamed = new File([source], "renamed.webp");
  for (const task of [
    { module: "images", files: [{ name: source.name, size: source.size }] },
    { module: "photos", autoName: true, files: [{ name: source.name, size: source.size }] },
  ]) {
    assert.deepEqual(matchUploadSources(task, [source]), [source]);
    assert.throws(() => matchUploadSources(task, [renamed]));
  }
});

test("comic source reselection retains the registered page order", () => {
  const first = new File(["first"], "00001.webp");
  const second = new File(["second"], "00002.webp");
  const task = { module: "comics", files: [first, second].map((f) => ({ name: f.name, size: f.size })) };
  assert.deepEqual(matchUploadSources(task, [second, first]), [first, second]);
});
