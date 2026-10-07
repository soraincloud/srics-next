<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { api, changed, jsonBody, type Item } from "./api";
import { newID, parseTags, type Novel } from "./novels";
import Icon from "./Icon.vue";
import EmptyState from "./EmptyState.vue";
import NovelReader from "./NovelReader.vue";
import SegmentedControl from "./SegmentedControl.vue";
const props = defineProps<{ itemId?: string }>();
const items = ref<Item[]>([]),
  novel = ref<Novel>(),
  query = ref(""),
  tags = ref<string[]>([]),
  selected = ref<string[]>([]);
const status = ref<"all" | "unfinished" | "completed">("all");
const filtered = computed(() =>
  !!query.value.trim() || !!selected.value.length || status.value !== "all",
);
const emptyTitle = computed(() =>
  query.value.trim() || selected.value.length
    ? "没有匹配的小说"
    : status.value === "completed"
      ? "暂无已完结小说"
      : status.value === "unfinished" ? "暂无未完结小说" : "暂无小说",
);
const total = ref(0),
  next = ref(""),
  snapshot = ref(0),
  loading = ref(false),
  error = ref(""),
  busy = ref(false);
const dialog = ref<HTMLDialogElement>(),
  name = ref(""),
  tagText = ref(""),
  formError = ref("");
let createId = "",
  generation = 0,
  timer: ReturnType<typeof setTimeout> | undefined;
async function load(more = false) {
  const current = ++generation;
  loading.value = true;
  error.value = "";
  try {
    if (props.itemId) {
      const data = await api<Novel>("/api/novels/" + props.itemId);
      if (current === generation) novel.value = data;
    } else {
      const params = new URLSearchParams({ module: "novels", q: query.value });
      if (status.value !== "all") params.set("status", status.value);
      for (const t of selected.value) params.append("tag", t);
      if (more) {
        params.set("after", next.value);
        params.set("snapshot", String(snapshot.value));
      }
      const data = await api("/api/library?" + params);
      if (current !== generation) return;
      items.value = more ? [...items.value, ...data.items] : data.items;
      total.value = data.total;
      next.value = data.next;
      snapshot.value = data.snapshot;
      tags.value = data.tags;
    }
  } catch (e) {
    if (current === generation) error.value = (e as Error).message;
  } finally {
    if (current === generation) loading.value = false;
  }
}
function search() {
  clearTimeout(timer);
  timer = setTimeout(() => load(), 250);
}
function filterStatus(value: typeof status.value) {
  if (status.value === value) return;
  status.value = value;
  clearTimeout(timer);
  void load();
}
function clearFilters() {
  query.value = "";
  selected.value = [];
  status.value = "all";
  clearTimeout(timer);
  void load();
}
function toggleTag(t: string) {
  selected.value = selected.value.includes(t)
    ? selected.value.filter((v) => v !== t)
    : [...selected.value, t];
  void load();
}
function edit() {
  name.value = novel.value?.item.name || "";
  tagText.value = novel.value?.item.tags.join("，") || "";
  formError.value = "";
  createId = newID();
  dialog.value?.showModal();
}
function statusChanged(item: Item) {
  if (novel.value) novel.value.item = item;
}
async function save() {
  busy.value = true;
  formError.value = "";
  try {
    const item = await api<Item>(
      novel.value ? "/api/items/" + novel.value.item.id : "/api/novels",
      {
        method: novel.value ? "PATCH" : "POST",
        body: jsonBody(
          novel.value
            ? {
                name: name.value,
                tags: parseTags(tagText.value),
                revision: novel.value.item.revision,
              }
            : {
                id: createId,
                name: name.value,
                tags: parseTags(tagText.value),
              },
        ),
      },
    );
    dialog.value?.close();
    changed();
    if (props.itemId) await load();
    else location.hash = "#/library/novels/" + item.id;
  } catch (e) {
    formError.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function trash() {
  if (!novel.value) return;
  busy.value = true;
  error.value = "";
  try {
    await api("/api/items/" + novel.value.item.id, { method: "DELETE" });
    changed();
    location.hash = "#/library/novels";
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
onMounted(() => load());
onUnmounted(() => {
  generation++;
  clearTimeout(timer);
});
</script>
<template>
  <template v-if="!itemId">
    <section class="page-heading">
      <h1>小说</h1>
      <button class="button primary" @click="edit">
        <Icon name="edit" />新建小说
      </button>
    </section>
    <div class="collection-toolbar">
      <SegmentedControl class="novel-status-filter" aria-label="小说状态筛选">
        <button :aria-pressed="status === 'all'" @click="filterStatus('all')">全部</button>
        <button :aria-pressed="status === 'unfinished'" @click="filterStatus('unfinished')"><span class="novel-status-dot" aria-hidden="true"></span>未完结</button>
        <button :aria-pressed="status === 'completed'" @click="filterStatus('completed')"><span class="novel-status-dot completed" aria-hidden="true"></span>已完结</button>
      </SegmentedControl>
      <div class="novel-list-search"><label class="search-field"
        ><Icon name="search" /><input
          v-model="query"
          type="search"
          aria-label="搜索小说名称"
          placeholder="搜索名称"
          @input="search" /></label
      ><span class="subtle-copy" role="status">{{ total }} 本</span></div>
    </div>
    <div v-if="tags.length" class="tag-filters" aria-label="标签筛选">
      <button
        v-for="t in tags"
        :key="t"
        :aria-pressed="selected.includes(t)"
        @click="toggleTag(t)"
      >
        {{ t }}</button
      ><button
        v-if="selected.length"
        @click="
          selected = [];
          load();
        "
      >
        清除标签
      </button><span v-if="selected.length">同时满足所选标签</span>
    </div>
    <div v-if="items.length" class="novel-grid">
      <a
        v-for="item in items"
        :key="item.id"
        :href="'#/library/novels/' + item.id"
        class="novel-card panel"
      >
        <div class="novel-card-heading">
          <span class="module-icon"><Icon name="text" /></span>
          <span class="novel-status" :class="{ completed: item.completed }">{{ item.completed ? '已完结' : '未完结' }}</span>
        </div>
        <h2>{{ item.name }}</h2>
        <div class="content-tags">
          <span v-for="t in item.tags" :key="t">{{ t }}</span>
        </div>
        <span class="novel-card-footer">打开小说<Icon name="arrow" /></span>
      </a>
    </div>
    <EmptyState
      v-if="!loading && !items.length && !error"
      :icon="filtered ? 'search' : 'text'"
      :title="emptyTitle"
      :description="filtered ? '调整名称、标签或完结状态后再试。' : '新建小说后，可以添加和编辑章节。'"
    >
      <button v-if="filtered" class="button secondary small" @click="clearFilters">清除筛选</button>
      <button v-else class="button secondary small" @click="edit"><Icon name="edit" />新建小说</button>
    </EmptyState>
    <button
      v-if="next"
      class="button secondary novel-more"
      :disabled="loading"
      @click="load(true)"
    >
      加载更多
    </button>
  </template>
  <NovelReader
    v-else-if="novel"
    :novel="novel"
    @reload="load"
    @edit="edit"
    @trash="trash"
    @status="statusChanged"
  />
  <p v-if="loading && !novel" class="subtle-copy" role="status">正在加载…</p>
  <p v-if="error" class="notice warning" role="alert">
    {{ error }}
    <button class="button small secondary" @click="load()">重试</button>
  </p>
  <dialog
    ref="dialog"
    class="app-dialog"
    @cancel="busy && $event.preventDefault()"
  >
    <form class="form-stack" @submit.prevent="save">
      <div class="dialog-heading">
        <h2>{{ novel ? "编辑小说" : "新建小说" }}</h2>
        <button
          type="button"
          class="icon-button"
          aria-label="关闭"
          :disabled="busy"
          @click="dialog?.close()"
        >
          <Icon name="close" />
        </button>
      </div>
      <label
        >名称<input v-model="name" required maxlength="160" autofocus
      /></label>
      <label>标签<input v-model="tagText" placeholder="用逗号分隔" /></label>
      <p v-if="formError" class="inline-error" role="alert">{{ formError }}</p>
      <button class="button primary" :disabled="busy">
        {{ busy ? "保存中…" : "保存" }}
      </button>
    </form>
  </dialog>
</template>
