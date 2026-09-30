import { computed, onMounted, onUnmounted, ref } from "vue";

import { expireSession, csrf } from "./api";

type CheckStatus = "pending" | "running" | "passed" | "failed" | "blocked";
type Release = { version: string; build: number; label: string; commit: string; dirty: boolean; builtAt: string };
export type Report = {
  status: "idle" | "running" | "passed" | "failed";
  startedAt: string;
  finishedAt?: string;
  versions: Record<string, string>;
  checks: { id: string; label: string; status: CheckStatus; detail?: string; durationMs: number }[];
};

export function useVerification() {
  const release = ref<Release | null>(null);
  const report = ref<Report | null>(null);
  const connection = ref<"connecting" | "online" | "offline">("connecting");
  const error = ref("");
  const starting = ref(false);
  const syncing = ref(false);
  const refreshing = ref(false);
  const busy = computed(() => starting.value || syncing.value || report.value?.status === "running");
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pending: Promise<void> | undefined;
  let pollController: AbortController | undefined;
  let startController: AbortController | undefined;

  function schedule() {
    clearTimeout(timer);
    if (!stopped && !starting.value) {
      timer = setTimeout(refresh, busy.value ? (document.hidden ? 2000 : 800) : 10000);
    }
  }

  function refresh(): Promise<void> {
    if (stopped) return Promise.resolve();
    if (pending) return pending;
    clearTimeout(timer);
    refreshing.value = true;
    pollController = new AbortController();
    const timeout = setTimeout(() => pollController?.abort(), 6000);
    pending = (async () => {
      try {
        const response = await fetch("/api/status", { cache: "no-store", signal: pollController!.signal });
        if (response.status === 401) expireSession();
        if (!response.ok) throw new Error("服务状态不可用");
        const data = await response.json();
        if (!stopped) {
          report.value = data.report;
          release.value = data.release || null;
          connection.value = "online";
          syncing.value = false;
        }
      } catch {
        if (!stopped) connection.value = "offline";
      } finally {
        clearTimeout(timeout);
        refreshing.value = false;
        pending = undefined;
        schedule();
      }
    })();
    return pending;
  }

  async function start() {
    if (busy.value || connection.value !== "online") return;
    starting.value = true;
    error.value = "";
    clearTimeout(timer);
    // Finish any older read before posting, so it cannot overwrite the new run.
    await pending;
    if (stopped) return;
    startController = new AbortController();
    const timeout = setTimeout(() => startController?.abort(), 8000);
    try {
      const response = await fetch("/api/verification", {
        method: "POST", headers: { "X-SRICS-Request": "verification", "X-SRICS-CSRF": csrf.value }, signal: startController.signal,
      });
      // Another tab may have started first. Join that run instead of reporting an error.
      if (!response.ok && response.status !== 409) throw new Error("启动失败，请稍后重试。");
      report.value = null;
      syncing.value = true;
    } catch (e) {
      error.value = e instanceof Error && e.name !== "AbortError"
        ? e.message : "未收到启动结果，正在重新同步。";
    } finally {
      clearTimeout(timeout);
      await refresh();
      if (report.value?.status === "running") error.value = "";
      starting.value = false;
      schedule();
    }
  }

  function resume() { if (!document.hidden && !starting.value) void refresh(); }
  onMounted(() => {
    void refresh();
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("online", resume);
  });
  onUnmounted(() => {
    stopped = true;
    clearTimeout(timer);
    pollController?.abort();
    startController?.abort();
    document.removeEventListener("visibilitychange", resume);
    window.removeEventListener("online", resume);
  });
  return { report, release, connection, error, starting, syncing, refreshing, busy, refresh, start };
}
