import { ref } from "vue";
import { sendTransfer, transferID } from "./transfers";
export const uploadProgress = ref("");
import { api, changed, jsonBody } from "./api";
export type Upload = {
  id: string;
  module: string;
  name: string;
  tags: string[];
  files: { name: string; size: number; sourceHash?: string; page?: unknown }[];
  state: string;
  error: string;
  created: string;
};
export const uploads = ref<Upload[]>([]),
  running = ref(""),
  uploadError = ref("");
let controller: AbortController | undefined;
const sources = new Map<string, File[]>();
function replace(up: Upload) {
  uploads.value = [up, ...uploads.value.filter((v) => v.id !== up.id)];
}
export async function loadUploads() {
  uploads.value = await api<Upload[]>("/api/uploads");
}
export async function createUpload(
  module: string,
  name: string,
  tags: string[],
  files: File[],
  id = crypto.randomUUID().replaceAll("-", ""),
) {
  const up = await api<Upload>("/api/uploads", {
    method: "POST",
    body: jsonBody({
      id,
      module,
      name,
      tags,
      files: files.map((f) => ({ name: f.name, size: f.size })),
    }),
  });
  sources.set(id, files);
  replace(up);
  return id;
}
export async function resumeUpload(up: Upload, files: File[]) {
  const mapped = up.files.map((f) =>
    files.find((v) => v.name === f.name && v.size === f.size),
  );
  if (mapped.some((v) => !v) || files.length !== up.files.length)
    throw new Error("请重新选择这个任务原来的文件，名称和数量需要一致。");
  sources.set(up.id, mapped as File[]);
  await runUpload(up.id);
}
export async function runUpload(id: string) {
  if (running.value) throw new Error("请等待当前上传完成，或先暂停它。");
  const files = sources.get(id);
  if (!files) throw new Error("请重新选择原来的文件以继续上传。");
  running.value = id;
  uploadError.value = "";
  controller = new AbortController();
  try {
    for (let i = 0; i < files.length; i++) {
      const file = files[i]!;
      const task = uploads.value.find((t) => t.id === id)!;
      replace(
        await sendTransfer(
          file,
          {
            id: await transferID(id, i),
            module: task.module,
            parent: id,
            index: i,
          },
          api,
          controller.signal,
          (text) =>
            (uploadProgress.value = `${i + 1}/${files.length} · ${text}`),
        ),
      );
    }
    await api(`/api/uploads/${id}/finish`, {
      method: "POST",
      signal: controller.signal,
    });
    sources.delete(id);
    await loadUploads();
    changed();
  } catch (e) {
    uploadError.value =
      (e as Error).name === "AbortError"
        ? "上传已暂停。点击继续可重试；重新选择原文件可从已保存的分块继续。"
        : (e as Error).message;
    await loadUploads().catch(() => {});
    throw new Error(uploadError.value);
  } finally {
    running.value = "";
    controller = undefined;
  }
}
export function pauseUpload() {
  controller?.abort();
}
export function clearUploadMemory() {
  pauseUpload();
  sources.clear();
  uploads.value = [];
}
