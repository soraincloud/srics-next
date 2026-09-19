import { ref } from "vue";
export const csrf = ref("");
export const authenticated = ref(false);
export const protectUnsavedSession = ref(false);
export const sessionExpired = ref(false);
export function expireSession() {
  sessionExpired.value = true;
  if (!protectUnsavedSession.value) authenticated.value = false;
}
export class APIError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}
export async function api<T = any>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set("X-SRICS-Request", "app");
  if (csrf.value) headers.set("X-SRICS-CSRF", csrf.value);
  if (typeof options.body === "string")
    headers.set("Content-Type", "application/json");
  let response: Response;
  try {
    response = await fetch(path, { ...options, headers, cache: "no-store" });
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") throw error;
    throw new APIError("无法连接服务，请确认本机程序正在运行后重试", 0);
  }
  const data = await response.json().catch(() => ({}));
  if (response.status === 401 && !path.startsWith("/api/auth/")) {
    expireSession();
  }
  if (!response.ok)
    throw new APIError(data.error || "操作失败，请稍后再试", response.status);
  return data as T;
}
export const jsonBody = (value: unknown) => JSON.stringify(value);
export const fileSize = (n: number) =>
  n < 1024
    ? n + " B"
    : n < 1048576
      ? (n / 1024).toFixed(0) + " KB"
      : n < 1073741824
        ? (n / 1048576).toFixed(1) + " MB"
        : (n / 1073741824).toFixed(2) + " GB";
export type Page = {
  name: string;
  object: string;
  size: number;
  sha256: string;
  mime: string;
  thumb: string;
};
export type Item = {
  id: string;
  module: string;
  name: string;
  tags: string[];
  created: string;
  deleted: string;
  revision: number;
  seq: number;
  pages: Page[];
  progress: number;
};
export const previewURL = (item: Item, page = 0, thumb = true) =>
  `/api/items/${item.id}/pages/${page}${thumb ? "?thumb=1" : ""}`;
export const canPreview = (page?: Page) =>
  !!page &&
  ["image/jpeg", "image/png", "image/webp", "image/gif"].includes(page.mime);
export function changed() {
  window.dispatchEvent(new Event("library-changed"));
}
