import type { api } from "./api";
export const chunkSize = 8 * 1024 * 1024;
export type Transfer = {
  id: string;
  module: string;
  name: string;
  size: number;
  parent: string;
  index: number;
  hashes: string[];
  done: number[];
  state: string;
  created: string;
};
type Request = typeof api;
export async function prepareTransfer(
  file: File,
  task: Pick<Transfer, "id" | "module" | "parent" | "index">,
  request: Request,
  signal: AbortSignal,
  progress: (text: string) => void,
) {
  const hashes: string[] = [];
  for (let offset = 0; offset < file.size; offset += chunkSize) {
    signal.throwIfAborted();
    progress(
      `核对文件 ${Math.min(100, Math.round((offset / file.size) * 100))}%`,
    );
    const bytes = await file.slice(offset, offset + chunkSize).arrayBuffer();
    hashes.push(
      Array.from(
        new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)),
        (b) => b.toString(16).padStart(2, "0"),
      ).join(""),
    );
  }
  signal.throwIfAborted();
  const base =
    task.module === "private" || task.module === "files"
      ? "/api/vault/transfers"
      : "/api/transfers";
  const up = await request<Transfer>(base, {
    method: "POST",
    signal,
    body: JSON.stringify({ ...task, name: file.name, size: file.size, hashes }),
  });
  return up;
}
export async function sendTransfer(
  file: File,
  task: Pick<Transfer, "id" | "module" | "parent" | "index">,
  request: Request,
  signal: AbortSignal,
  progress: (text: string) => void,
  prepared?: Transfer,
) {
  const up =
    prepared ?? (await prepareTransfer(file, task, request, signal, progress));
  const base =
    up.module === "private" || up.module === "files"
      ? "/api/vault/transfers"
      : "/api/transfers";
  const hashes = up.hashes;
  const done = new Set(up.done);
  if (up.state !== "complete")
    for (let i = 0; i < hashes.length; i++) {
      signal.throwIfAborted();
      progress(
        `上传 ${Math.round((done.size / Math.max(1, hashes.length)) * 100)}%`,
      );
      if (done.has(i)) continue;
      await request(`${base}/${task.id}/chunks/${i}`, {
        method: "PUT",
        signal,
        body: file.slice(
          i * chunkSize,
          Math.min(file.size, (i + 1) * chunkSize),
        ),
      });
      done.add(i);
    }
  signal.throwIfAborted();
  progress("合并并校验…");
  return await request(`${base}/${task.id}/finish`, { method: "POST", signal });
}
export async function transferID(parent: string, index: number) {
  const h = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(`${parent}:${index}`),
  );
  return Array.from(new Uint8Array(h).slice(0, 16), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
}
