<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import ActionConfirm from "./ActionConfirm.vue";
const confirmation = ref<InstanceType<typeof ActionConfirm>>(),
  storage = ref<any>(),
  retentionPlan = ref<any>();
import { api, changed, fileSize, type Item } from "./api";
const props = defineProps<{ page: string }>();
const items = ref<Item[]>([]),
  backup = ref<any>(),
  target = ref("local"),
  error = ref(""),
  busy = ref(false),
  next = ref(""),
  snapshot = ref(0);
type BackupSnapshot = {
  id: string;
  time: string;
  files?: number;
  bytes?: number;
};
const history = ref<BackupSnapshot[]>([]),
  historyLoaded = ref(false),
  historyBusy = ref(false),
  historyError = ref(""),
  historyNext = ref(""),
  historyTotal = ref(0);
let historyVersion = 0;
async function loadHistory(more = false) {
  const version = ++historyVersion;
  historyBusy.value = true;
  historyError.value = "";
  if (!more) {
    history.value = [];
    historyLoaded.value = false;
    historyNext.value = "";
  }
  try {
    const data = await api(
      `/api/backup/snapshots?target=${target.value}${more ? "&after=" + historyNext.value : ""}`,
    );
    if (stopped || version !== historyVersion) return;
    history.value = more
      ? [...history.value, ...data.snapshots]
      : data.snapshots;
    historyNext.value = data.next;
    historyTotal.value = data.total;
    historyLoaded.value = true;
  } catch (e) {
    if (!stopped && version === historyVersion)
      historyError.value = (e as Error).message;
  } finally {
    if (version === historyVersion) historyBusy.value = false;
  }
}
const names: Record<string, string> = {
  comics: "漫画",
  novels: "小说",
  images: "图片",
  photos: "个人照片",
};
let timer: ReturnType<typeof setTimeout> | undefined,
  stopped = false,
  loadVersion = 0;
async function load(more = false) {
  const version = ++loadVersion;
  try {
    if (props.page === "trash") {
      storage.value = await api("/api/storage");
      const data = await api(
        `/api/library?module=all&trash=1${more ? "&after=" + next.value + "&snapshot=" + snapshot.value : ""}`,
      );
      items.value = more ? [...items.value, ...data.items] : data.items;
      next.value = data.next;
      snapshot.value = data.snapshot;
    } else {
      const result = await api(`/api/backup?target=${target.value}`);
      if (stopped || version !== loadVersion) return;
      backup.value = result;
      if (!stopped) {
        clearTimeout(timer);
        timer = setTimeout(() => load(), backup.value.busy ? 1500 : 30000);
      }
    }
  } catch (e) {
    if (stopped || version !== loadVersion) return;
    error.value = (e as Error).message;
  }
}
async function restore(id: string) {
  busy.value = true;
  error.value = "";
  try {
    await api(`/api/items/${id}/restore`, { method: "POST" });
    await load();
    changed();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function purge(id: string) {
  if (
    !(await confirmation.value?.ask(
      "彻底删除这项资料？",
      "将删除当前资料库中的原件、预览和修订，无法从回收站恢复。历史备份中的副本仍按备份规则保留。",
    ))
  )
    return;
  busy.value = true;
  try {
    await api(`/api/items/${id}/purge`, { method: "POST" });
    await load();
    changed();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function previewRetention() {
  busy.value = true;
  try {
    retentionPlan.value = await api(
      `/api/backup/retention?target=${target.value}`,
    );
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function cleanRetention() {
  if (
    !retentionPlan.value ||
    !(await confirmation.value?.ask(
      "清理历史备份？",
      `将永久删除预览中的 ${retentionPlan.value.remove.length} 个恢复点，保留 ${retentionPlan.value.keep.length} 个。此操作不能撤销，当前资料不受影响。`,
    ))
  )
    return;
  busy.value = true;
  try {
    await api(`/api/backup/retention?target=${target.value}`, {
      method: "POST",
      body: JSON.stringify({ token: retentionPlan.value.token }),
    });
    retentionPlan.value = undefined;
    await load();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function start() {
  busy.value = true;
  error.value = "";
  try {
    await api(`/api/backup?target=${target.value}`, { method: "POST" });
    await load();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
function date(value: string) {
  return new Date(value).toLocaleString("zh-CN", { hour12: false });
}
watch([() => props.page, target], () => {
  clearTimeout(timer);
  error.value = "";
  backup.value = undefined;
  retentionPlan.value = undefined;
  historyVersion++;
  history.value = [];
  historyLoaded.value = false;
  historyBusy.value = false;
  historyError.value = "";
  historyNext.value = "";
  void load();
});
onMounted(() => load());
onUnmounted(() => {
  stopped = true;
  clearTimeout(timer);
});
</script>
<template>
  <ActionConfirm ref="confirmation" />
  <section class="page-heading">
    <div>
      <h1>{{ page === "trash" ? "回收站" : "备份中心" }}</h1>
      <p class="subtitle">
        {{
          page === "trash"
            ? "恢复已删除的资料。"
            : "创建和检查资料库的加密备份。"
        }}
      </p>
    </div>
  </section>
  <div v-if="page !== 'trash'" class="collection-toolbar">
    <div class="segmented" role="group" aria-label="备份目标">
      <button :aria-pressed="target === 'local'" @click="target = 'local'">
        本地 / 独立硬盘
      </button>
      <button :aria-pressed="target === 'cloud'" @click="target = 'cloud'">
        云端
      </button>
    </div>
  </div>
  <p v-if="error" class="notice warning" role="alert">{{ error }}</p>
  <template v-if="page === 'trash'">
    <p v-if="storage" class="subtle-copy">
      资料文件占用 {{ fileSize(storage.usage.bytes) }} · 磁盘可用
      {{ fileSize(storage.usage.free) }}
    </p>
    <p v-if="storage?.error" class="notice warning">{{ storage.error }}</p>
    <div v-if="items.length" class="trash-list panel">
      <div v-for="item in items" :key="item.id" class="trash-row">
        <span class="module-icon"
          ><Icon
            :name="
              item.module === 'comics'
                ? 'book'
                : item.module === 'novels'
                  ? 'text'
                  : 'image'
            "
        /></span>
        <div>
          <strong>{{ item.name }}</strong>
          <p>
            {{ names[item.module] }} ·
            <template v-if="item.module !== 'novels'"
              >{{
                fileSize(item.pages.reduce((n, p) => n + p.size, 0))
              }}
              · </template
            >删除于
            {{ date(item.deleted) }}
          </p>
        </div>
        <button
          class="button small secondary"
          :disabled="busy"
          @click="restore(item.id)"
        >
          <Icon name="refresh" />恢复</button
        ><button
          class="button small secondary"
          :disabled="busy"
          @click="purge(item.id)"
        >
          彻底删除
        </button>
      </div>
    </div>
    <section v-else class="collection-empty panel">
      <span class="empty-icon"><Icon name="trash" /></span>
      <h2>回收站是空的</h2>
      <p>
        已删除的小说、漫画、图片和照片可在这里恢复。章节在各本小说的“已删除”目录恢复。
      </p>
    </section>
    <button v-if="next" class="button secondary" @click="load(true)">
      加载更多
    </button>
    <p class="page-footnote">
      {{
        storage?.trashDays
          ? `保留 ${storage.trashDays} 天后自动彻底删除；私密回收站在解锁后清理。`
          : "自动清理未开启，可在本机程序中设置保留天数。"
      }}历史备份中的副本独立保留。
    </p>
  </template>
  <template v-else-if="backup">
    <section class="backup-card panel">
      <span class="verification-emblem"><Icon name="shield" /></span>
      <div>
        <p class="eyebrow">
          {{ target === "cloud" ? "云端加密备份" : "本地加密备份" }}
        </p>
        <h2>
          {{
            backup.running
              ? "正在处理备份"
              : !backup.configured
                ? "未配置备份"
                : backup.last?.status === "passed"
                  ? "最近一次备份已完成"
                  : backup.last?.status === "failed"
                    ? "最近一次备份未完成"
                    : "还没有创建备份"
          }}
        </h2>
        <p>
          {{
            backup.running
              ? "页面可以关闭，任务会在服务端继续。"
              : !backup.configured
                ? target === "cloud"
                  ? "在本机程序中配置 S3 兼容存储。"
                  : "选择本地目录或独立硬盘保存加密备份。"
                : "包含全部六类资料、历史版本与回收站。私密内容保持加密，无需解锁保险库。"
          }}
        </p>
      </div>
      <button
        class="button primary"
        :disabled="!backup.configured || backup.busy || busy"
        @click="start"
      >
        <Icon name="shield" />{{ backup.running ? "处理中…" : "立即备份" }}
      </button>
    </section>
    <p v-if="backup.busy && !backup.running" class="page-footnote">
      另一个目标正在备份，完成后可执行此目标。
    </p>
    <dl class="backup-details panel">
      <dt>备份位置</dt>
      <dd>{{ backup.destination || "未配置" }}</dd>
      <dt>自动备份</dt>
      <dd>
        {{
          backup.configured && backup.dailyAt
            ? `每天 ${backup.dailyAt}（主机时区 ${backup.timeZone}）`
            : "未开启"
        }}
      </dd>
      <template v-if="backup.nextRunAt">
        <dt>
          {{ backup.last?.status === "failed" ? "下次重试" : "下次执行" }}
        </dt>
        <dd>{{ date(backup.nextRunAt) }}</dd>
      </template>
      <template v-if="backup.last?.attemptedAt">
        <dt>最近尝试</dt>
        <dd>{{ date(backup.last.attemptedAt) }}</dd>
      </template>
      <dt>配置位置</dt>
      <dd>
        本机 SRICS Next 程序 ·
        {{ target === "cloud" ? "云端加密备份" : "本地 / 独立硬盘备份" }}
      </dd>
    </dl>
    <dl v-if="backup.last?.savedAt" class="backup-details panel">
      <dt>最近成功备份的内容截止时间</dt>
      <dd>{{ date(backup.last.savedAt) }}</dd>
      <dt>备份数据完整性检查</dt>
      <dd>
        {{ backup.last.readVerified ? "已读取全部备份并通过检查" : "尚未检查" }}
      </dd>
      <dt>快照编号</dt>
      <dd class="mono">{{ backup.last.snapshot }}</dd>
      <dt>实际恢复演练</dt>
      <dd>
        在本机程序中选择“从备份恢复”，恢复到新目录并校验。结果保存在恢复目录中；私密内容仍需使用保险库口令解锁确认。
      </dd>
    </dl>
    <section
      v-if="backup.configured"
      class="backup-history panel"
      aria-label="历史备份"
    >
      <div class="backup-history-heading">
        <h2>
          历史备份<span v-if="historyLoaded">{{ historyTotal }}</span>
        </h2>
        <button
          class="button small secondary"
          :disabled="historyBusy"
          @click="loadHistory()"
        >
          <Icon name="refresh" />{{
            historyBusy ? "读取中…" : historyLoaded ? "刷新" : "查看历史"
          }}
        </button>
      </div>
      <p v-if="historyError" class="notice warning" role="alert">
        {{ historyError }}
      </p>
      <template v-if="historyLoaded">
        <ol v-if="history.length" class="backup-snapshots">
          <li v-for="entry in history" :key="entry.id">
            <div class="snapshot-summary">
              <time :datetime="entry.time">{{ date(entry.time) }}</time
              ><span v-if="entry.bytes != null"
                >{{ fileSize(entry.bytes)
                }}<template v-if="entry.files != null">
                  · {{ entry.files }} 个备份文件</template
                ></span
              >
            </div>
            <code>{{ entry.id }}</code>
            <span
              v-if="
                entry.id === backup.last?.snapshot && backup.last?.readVerified
              "
              class="snapshot-check"
              >已通过完整读取检查</span
            >
          </li>
        </ol>
        <p v-else class="subtle-copy">这个目标还没有资料库快照。</p>
        <button
          v-if="historyNext"
          class="button small secondary"
          :disabled="historyBusy"
          @click="loadHistory(true)"
        >
          加载更多
        </button>
      </template>
      <p class="backup-history-note">
        在本机程序中选择恢复点。文件数量包含索引与预览，不等于资料条目数；列出快照不代表已通过恢复验证。
      </p>
    </section>
    <section v-if="backup.configured" class="panel backup-history">
      <div class="backup-history-heading">
        <h2>保留策略</h2>
        <button
          v-if="backup.retention?.enabled"
          class="button small secondary"
          :disabled="busy || backup.busy"
          @click="previewRetention"
        >
          预览清理
        </button>
      </div>
      <p class="subtle-copy">
        {{
          backup.retention?.enabled
            ? `保留最近 ${backup.retention.daily} 个每日版本及 ${backup.retention.monthly} 个月度版本（UTC），至少保留最新快照；新备份校验成功后自动执行。`
            : "保留全部历史快照。可在本机程序中启用保留策略。"
        }}
      </p>
      <div v-if="retentionPlan">
        <p>
          保留 {{ retentionPlan.keep.length }} 个 · 可清理
          {{ retentionPlan.remove.length }} 个
        </p>
        <details v-if="retentionPlan.remove.length">
          <summary>查看将删除的恢复点</summary>
          <ol class="backup-snapshots">
            <li v-for="entry in retentionPlan.remove" :key="entry.id">
              <time>{{ date(entry.time) }}</time
              ><code>{{ entry.id }}</code>
            </li>
          </ol>
        </details>
        <button
          v-if="retentionPlan.remove.length"
          class="button secondary"
          :disabled="busy || backup.busy"
          @click="cleanRetention"
        >
          执行本次清理
        </button>
      </div>
      <p v-if="backup.last?.cleanupAt" class="subtle-copy">
        最近清理：{{ date(backup.last.cleanupAt) }}
      </p>
      <p v-if="backup.last?.cleanupError" class="notice warning">
        {{ backup.last.cleanupError }}
      </p>
    </section>
    <p v-if="backup.last?.error" class="notice warning">
      {{ backup.last.error }}
    </p>
    <section v-if="!backup.configured" class="backup-setup panel">
      <h2>备份配置</h2>
      <p>
        {{
          target === "cloud"
            ? "填写 Endpoint、区域、存储桶与专用前缀，选择凭据和备份口令文件；检查连接后保存并启动服务。"
            : "选择备份目录和口令文件，保存并启动服务。请使用独立硬盘存放正式备份。"
        }}
      </p>
    </section>
    <p class="page-footnote">
      只有完整快照和数据检查都成功，才会记录本次成功状态。上传成功不等于已备份。
    </p>
  </template>
</template>
