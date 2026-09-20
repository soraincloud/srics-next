<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import { api, changed, fileSize, type Item } from "./api";
const props = defineProps<{ page: string }>();
const items = ref<Item[]>([]),
  backup = ref<any>(),
  error = ref(""),
  busy = ref(false),
  next = ref(""),
  snapshot = ref(0);
const names: Record<string, string> = {
  comics: "漫画",
  novels: "小说",
  images: "图片",
  photos: "个人照片",
};
let timer: ReturnType<typeof setTimeout> | undefined,
  stopped = false;
async function load(more = false) {
  try {
    if (props.page === "trash") {
      const data = await api(
        `/api/library?module=all&trash=1${more ? "&after=" + next.value + "&snapshot=" + snapshot.value : ""}`,
      );
      items.value = more ? [...items.value, ...data.items] : data.items;
      next.value = data.next;
      snapshot.value = data.snapshot;
    } else {
      backup.value = await api("/api/backup");
      if (!stopped) {
        clearTimeout(timer);
        timer = setTimeout(() => load(), backup.value.running ? 1500 : 30000);
      }
    }
  } catch (e) {
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
async function start() {
  busy.value = true;
  error.value = "";
  try {
    await api("/api/backup", { method: "POST" });
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
watch(
  () => props.page,
  () => {
    clearTimeout(timer);
    error.value = "";
    void load();
  },
);
onMounted(() => load());
onUnmounted(() => {
  stopped = true;
  clearTimeout(timer);
});
</script>
<template>
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
  <p v-if="error" class="notice warning" role="alert">{{ error }}</p>
  <template v-if="page === 'trash'">
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
          <Icon name="refresh" />恢复
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
      当前开发版暂不自动清空，也不永久删除原件。历史备份中的副本独立保留。
    </p>
  </template>
  <template v-else-if="backup">
    <section class="backup-card panel">
      <span class="verification-emblem"><Icon name="shield" /></span>
      <div>
        <p class="eyebrow">资料库加密备份</p>
        <h2>
          {{
            backup.running
              ? "正在保存备份"
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
                ? "支持本地目录或独立硬盘上的 restic 加密仓库。"
                : "包含全部六类资料、历史版本与回收站。私密内容保持加密，无需解锁保险库。"
          }}
        </p>
      </div>
      <button
        class="button primary"
        :disabled="!backup.configured || backup.running || busy"
        @click="start"
      >
        <Icon name="shield" />{{ backup.running ? "备份中…" : "立即备份" }}
      </button>
    </section>
    <dl class="backup-details panel">
      <dt>自动备份</dt>
      <dd>
        {{
          backup.dailyAt
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
      <dd>本机 SRICS Next 程序 · 加密备份</dd>
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
        需用独立目录执行恢复命令并核对；样本验证不代表真实资料已完成恢复。
      </dd>
    </dl>
    <p v-if="backup.last?.error" class="notice warning">
      {{ backup.last.error }}
    </p>
    <section v-if="!backup.configured" class="backup-setup panel">
      <h2>备份配置</h2>
      <p>在本机 SRICS Next 程序中选择备份目录和口令文件，保存并启动服务。</p>
      <p>请使用独立硬盘存放备份。云端存储将在选定服务商后接入。</p>
    </section>
    <p class="page-footnote">
      只有完整快照和数据检查都成功，才会记录本次成功状态。上传成功不等于已备份。
    </p>
  </template>
</template>
