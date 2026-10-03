<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import EmptyState from "./EmptyState.vue";
import DocumentEditor from "./DocumentEditor.vue";
import { api, fileSize, type Item } from "./api";

const props = defineProps<{ itemId?: string }>();
const items = ref<Item[]>([]), query = ref(""), tags = ref<string[]>([]), selected = ref<string[]>([]);
const total = ref(0), next = ref(""), snapshot = ref(0), loading = ref(false), error = ref("");
let generation = 0, stopped = false, timer: ReturnType<typeof setTimeout> | undefined;
async function load(more = false) {
  clearTimeout(timer);
  const version = ++generation;
  loading.value = true;
  error.value = "";
  if (!more) { items.value = []; next.value = ""; snapshot.value = 0; }
  try {
    const params = new URLSearchParams({ module: "documents", q: query.value });
    selected.value.forEach(tag => params.append("tag", tag));
    if (more) { params.set("after", next.value); params.set("snapshot", String(snapshot.value)); }
    const data = await api<{ items: Item[]; total: number; next: string; snapshot: number; tags: string[] }>("/api/library?" + params);
    if (stopped || version !== generation) return;
    items.value = more ? [...items.value, ...data.items] : data.items;
    total.value = data.total; next.value = data.next; snapshot.value = data.snapshot; tags.value = data.tags;
  } catch (e) { if (!stopped && version === generation) error.value = (e as Error).message; }
  finally { if (version === generation) loading.value = false; }
}
function toggleTag(tag: string) {
  selected.value = selected.value.includes(tag) ? selected.value.filter(t => t !== tag) : [...selected.value, tag];
  void load();
}
function reset() { query.value = ""; selected.value = []; void load(); }
watch(query, () => { ++generation; clearTimeout(timer); timer = setTimeout(() => void load(), 250); });
onMounted(() => { if (!props.itemId) void load(); });
onUnmounted(() => { stopped = true; ++generation; clearTimeout(timer); });
</script>

<template>
  <DocumentEditor v-if="itemId" :id="itemId" />
  <template v-else>
    <section class="page-heading"><div><h1>文档</h1></div><a href="#/library/documents/new" class="button primary"><Icon name="edit" />新建文档</a></section>
    <div class="collection-toolbar">
      <label class="search-field"><Icon name="search" /><input v-model="query" type="search" placeholder="搜索文档名称" aria-label="搜索文档名称" /></label>
      <span class="collection-total" role="status">{{ loading ? '读取中…' : total + ' 篇' }}</span>
    </div>
    <div v-if="tags.length" class="tag-filters" aria-label="标签分类">
      <button :aria-pressed="selected.length === 0" @click="selected = []; load()">全部</button>
      <button v-for="tag in tags" :key="tag" :aria-pressed="selected.includes(tag)" @click="toggleTag(tag)">{{ tag }}</button>
    </div>
    <div v-if="error" class="notice warning" role="alert"><span>{{ error }}</span><button class="button small secondary" :disabled="loading" @click="load()">重新读取</button></div>
    <div v-if="items.length" class="novel-grid document-grid">
      <a v-for="item in items" :key="item.id" :href="'#/library/documents/' + item.id" class="document-card panel">
        <div class="document-card-top"><span class="module-icon"><Icon name="text" /></span><span class="quiet-badge">Markdown</span></div>
        <h2>{{ item.name }}</h2>
        <div v-if="item.tags.length" class="content-tags"><span v-for="tag in item.tags" :key="tag">{{ tag }}</span></div>
        <div class="document-card-footer"><span>{{ fileSize(item.pages[0]?.size || 0) }}</span><Icon name="arrow" /></div>
      </a>
    </div>
    <EmptyState v-else-if="!loading && !error" icon="text" :title="query || selected.length ? '没有匹配的文档' : '暂无文档'" :description="query || selected.length ? '试试其他名称或标签。' : ''">
      <button v-if="query || selected.length" class="button small secondary" @click="reset">清除筛选</button>
      <a v-else href="#/library/documents/new" class="button small secondary"><Icon name="edit" />新建文档</a>
    </EmptyState>
    <div v-if="next" class="load-more"><button class="button secondary" :disabled="loading" @click="load(true)">{{ loading ? '读取中…' : '加载更多' }}</button></div>
  </template>
</template>
