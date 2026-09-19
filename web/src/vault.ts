import { ref, watch } from "vue";
import {
  api,
  APIError,
  csrf,
  authenticated,
  sessionExpired,
  expireSession,
} from "./api.ts";
export const vaultOpen = ref(false);
export const vaultConfigured = ref(false);
export const vaultExpires = ref(0);
export const vaultIdle = ref(10);
let controller = new AbortController();
let generation = 0;
const urls = new Set<string>();
export function forgetVault() {
  generation++;
  controller.abort();
  controller = new AbortController();
  vaultOpen.value = false;
  vaultExpires.value = 0;
  for (const url of urls) URL.revokeObjectURL(url);
  urls.clear();
}
function accept(status: any, explicitUnlock = false) {
  vaultConfigured.value = status.configured;
  vaultIdle.value = status.idleMinutes;
  if (!status.unlocked) {
    forgetVault();
    return;
  }
  // A failed lock request must not let a later status poll reopen the UI.
  if (!vaultOpen.value && generation > 0 && !explicitUnlock) return;
  vaultExpires.value = Date.parse(status.expires);
  vaultOpen.value = true;
}
export async function vaultStatus() {
  const epoch = generation;
  const status = await api("/api/vault/status");
  if (epoch === generation) accept(status);
}
export async function unlockVault(password: string) {
  const epoch = generation;
  const status = await api("/api/vault/unlock", {
    method: "POST",
    body: JSON.stringify({ password }),
  });
  if (epoch === generation) accept(status, true);
}
export async function lockVault() {
  forgetVault();
  await api("/api/vault/lock", { method: "POST" });
}
export async function vaultAPI<T = any>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const epoch = generation;
  const signal = options.signal
    ? AbortSignal.any([controller.signal, options.signal])
    : controller.signal;
  try {
    const result = await api<T>(path, { ...options, signal });
    if (epoch !== generation || !vaultOpen.value)
      throw new DOMException("已锁定", "AbortError");
    return result;
  } catch (e) {
    if (e instanceof APIError && (e.status === 423 || e.status === 401))
      forgetVault();
    throw e;
  }
}
export async function vaultImage(
  id: string,
  original = false,
  signal?: AbortSignal,
) {
  const epoch = generation;
  const res = await fetch(
    `/api/vault/items/${id}/${original ? "download" : "preview"}`,
    {
      cache: "no-store",
      signal: signal
        ? AbortSignal.any([controller.signal, signal])
        : controller.signal,
      headers: { "X-SRICS-Request": "app", "X-SRICS-CSRF": csrf.value },
    },
  );
  if (!res.ok) {
    if (res.status === 401) expireSession();
    if (res.status === 401 || res.status === 423) forgetVault();
    throw new Error("预览无法加载，请重试");
  }
  const blob = await res.blob();
  if (epoch !== generation || !vaultOpen.value || signal?.aborted)
    throw new DOMException("已锁定", "AbortError");
  const url = URL.createObjectURL(blob);
  urls.add(url);
  return url;
}
export function releaseVaultImage(url?: string) {
  if (url) {
    URL.revokeObjectURL(url);
    urls.delete(url);
  }
}
export function installVaultLifecycle() {
  let lastTouch = 0,
    touching = false,
    checking = false;
  const activity = (event: Event) => {
    if (
      !event.isTrusted ||
      document.visibilityState !== "visible" ||
      !vaultOpen.value ||
      touching ||
      Date.now() - lastTouch < 10000
    )
      return;
    touching = true;
    lastTouch = Date.now();
    const epoch = generation;
    void vaultAPI("/api/vault/activity", { method: "POST" })
      .then((status) => {
        if (epoch === generation) accept(status);
      })
      .catch(() => {})
      .finally(() => (touching = false));
  };
  const events = ["pointerdown", "keydown", "wheel", "touchstart"];
  for (const event of events)
    window.addEventListener(event, activity, { passive: true });
  const timer = setInterval(() => {
    if (vaultOpen.value && Date.now() >= vaultExpires.value) forgetVault();
    if (!vaultOpen.value || checking) return;
    checking = true;
    void vaultStatus()
      .catch(() => {})
      .finally(() => (checking = false));
  }, 2000);
  const stop = watch([authenticated, sessionExpired], ([ok, expired]) => {
    if (!ok || expired) forgetVault();
  });
  const leaving = () => {
    if (!vaultOpen.value) return;
    forgetVault();
    void fetch("/api/vault/lock", {
      method: "POST",
      keepalive: true,
      headers: { "X-SRICS-Request": "app", "X-SRICS-CSRF": csrf.value },
    }).catch(() => {});
  };
  const returning = () => {
    if (!vaultOpen.value) return;
    if (Date.now() >= vaultExpires.value) forgetVault();
    else void vaultStatus().catch(() => {});
  };
  window.addEventListener("pageshow", returning);
  document.addEventListener("visibilitychange", returning);
  window.addEventListener("pagehide", leaving);
  return () => {
    clearInterval(timer);
    stop();
    for (const event of events) window.removeEventListener(event, activity);
    window.removeEventListener("pagehide", leaving);
    window.removeEventListener("pageshow", returning);
    document.removeEventListener("visibilitychange", returning);
    forgetVault();
  };
}
