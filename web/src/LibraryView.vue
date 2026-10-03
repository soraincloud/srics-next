<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import PageActions from "./PageActions.vue";
import SegmentedControl from "./SegmentedControl.vue";
import EmptyState from "./EmptyState.vue";
import { useGalleryNavigation } from "./gallery";
import UploadPanel from "./UploadPanel.vue";
import {
  api,
  canPreview,
  changed,
  fileSize,
  jsonBody,
  previewURL,
  type Item,
} from "./api";
const uploader = ref<InstanceType<typeof UploadPanel>>();
const props = defineProps<{
  module: string;
  itemId?: string;
  name: string;
  sub: string;
}>();
const items = ref<Item[]>([]),
  detail = ref<Item>(),
  loading = ref(false),
  error = ref(""),
  notice = ref(""),
  total = ref(0),
  query = ref(""),
  selectedTags = ref<string[]>([]),
  tags = ref<string[]>([]),
  random = ref(false),
  next = ref(""),
  seed = ref(""),
  snapshot = ref(0),
  selection = ref<string[]>([]),
  selecting = ref(false),
  selectionMessage = ref(""),
  page = ref(0),
  saving = ref(false);
const editDialog = ref<HTMLDialogElement>(),
  viewer = ref<HTMLDialogElement>(),
  editing = ref<Item>(),
  editName = ref(""),
  editTags = ref(""),
  viewing = ref<Item>(),
  sentinel = ref<HTMLElement>();
let generation = 0,
  searchTimer: ReturnType<typeof setTimeout> | undefined,
  progressTimer: ReturnType<typeof setTimeout> | undefined,
  observer: IntersectionObserver | undefined,
  readObserver: IntersectionObserver | undefined;
const backupSavedAt = ref("");
const isComic = computed(() => props.module === "comics");
const { index: viewerIndex, moving: viewerMoving, canNext: viewerCanNext, advance: nextImage } =
  useGalleryNavigation(items, viewing, next, loading, () => load(true));
const selectedURL = computed(
  () => "/api/download?" + selection.value.map((id) => "id=" + id).join("&"),
);
async function load(more = false) {
  if (more && (loading.value || !next.value)) return;
  const current = ++generation;
  loading.value = true;
  error.value = "";
  if (!more) {
    next.value = "";
    snapshot.value = 0;
    seed.value = "";
    selection.value = [];
    selecting.value = false;
    selectionMessage.value = "";
  }
  try {
    if (props.itemId) {
      const result = await api<Item>(`/api/items/${props.itemId}`);
      if (current !== generation) return;
      if (result.deleted || result.module !== props.module)
        throw new Error("该内容已移到回收站或不属于此空间");
      detail.value = result;
      page.value = result.progress;
      await nextTick();
      void observePages();
      return;
    }
    detail.value = undefined;
    const params = new URLSearchParams({
      module: props.module,
      q: query.value,
      random: random.value ? "1" : "0",
    });
    for (const tag of selectedTags.value) params.append("tag", tag);
    if (more) {
      params.set("after", next.value);
      params.set("snapshot", String(snapshot.value));
      params.set("seed", seed.value);
    }
    const result = await api<{
      items: Item[];
      total: number;
      next: string;
      seed: string;
      snapshot: number;
      tags: string[];
    }>("/api/library?" + params);
    const backup = props.module === "photos" ? await api("/api/backup") : null;
    if (current !== generation) return;
    backupSavedAt.value = backup?.last?.savedAt || "";
    items.value = more ? [...items.value, ...result.items] : result.items;
    total.value = result.total;
    next.value = result.next;
    seed.value = result.seed;
    snapshot.value = result.snapshot;
    tags.value = result.tags;
  } catch (e) {
    if (current === generation) {
      error.value = (e as Error).message;
      detail.value = undefined;
    }
  } finally {
    if (current === generation) loading.value = false;
  }
}
function search() {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => load(), 250);
}
function toggleTag(tag: string) {
  selectedTags.value = selectedTags.value.includes(tag)
    ? selectedTags.value.filter((t) => t !== tag)
    : [...selectedTags.value, tag];
  void load();
}
function toggleSelectionMode() {
  selecting.value = !selecting.value;
  selection.value = [];
  selectionMessage.value = "";
}
function selectLoaded() {
  selection.value = items.value.slice(0, 100).map(item => item.id);
  selectionMessage.value = items.value.length > 100 ? "每次最多选择 100 项，已选前 100 项。" : "";
}
function toggleSelect(id: string) {
  selecting.value = true;
  selectionMessage.value = "";
  if (!selection.value.includes(id) && selection.value.length >= 100) {
    selectionMessage.value = "每次最多选择 100 项，请先取消部分选择。";
    return;
  }
  selection.value = selection.value.includes(id)
    ? selection.value.filter((v) => v !== id)
    : [...selection.value, id].slice(0, 100);
}
async function trash(targets: Item[]) {
  saving.value = true;
  error.value = "";
  try {
    for (const item of targets)
      await api(`/api/items/${item.id}`, { method: "DELETE" });
    notice.value = `已将 ${targets.length} 项移到回收站，可随时恢复。`;
    viewer.value?.close();
    viewing.value = undefined;
    changed();
    if (props.itemId) location.hash = `#/library/${props.module}`;
    else await load();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    saving.value = false;
  }
}
function edit(item: Item) {
  editing.value = item;
  editName.value = item.name;
  editTags.value = item.tags.join("，");
  editDialog.value?.showModal();
}
async function saveEdit() {
  if (!editing.value) return;
  saving.value = true;
  error.value = "";
  try {
    await api(`/api/items/${editing.value.id}`, {
      method: "PATCH",
      body: jsonBody({
        name: editName.value,
        tags: editTags.value
          .split(/[,，]/)
          .map((t) => t.trim())
          .filter(Boolean),
        revision: editing.value.revision,
      }),
    });
    editDialog.value?.close();
    await load();
    changed();
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    saving.value = false;
  }
}
function openImage(item: Item) {
  if (selecting.value) { toggleSelect(item.id); return; }
  error.value = "";
  viewing.value = item;
  viewer.value?.showModal();
}
function persistProgress() {
  clearTimeout(progressTimer);
  const id = detail.value?.id,
    p = page.value;
  progressTimer = setTimeout(() => {
    if (id)
      void api(`/api/items/${id}/progress`, {
        method: "PUT",
        body: jsonBody({ page: p }),
      }).catch(() => {
        notice.value = "阅读进度暂未保存，连接恢复后继续滚动可重试。";
      });
  }, 400);
}
function jumpToPage(n: number) {
  if (!detail.value || !Number.isFinite(n)) return;
  page.value = Math.max(0, Math.min(n, detail.value.pages.length - 1));
  persistProgress();
  document
    .getElementById("comic-page-" + page.value)
    ?.scrollIntoView({ block: "start", behavior: "instant" });
}
function observePages(restore = true) {
  readObserver?.disconnect();
  const id = detail.value?.id;
  if (!id || !isComic.value) return;
  const target = page.value;
  if (restore && target > 0)
    document
      .getElementById("comic-page-" + target)
      ?.scrollIntoView({ block: "start", behavior: "instant" });
  const visiblePages = new Set<number>();
  const toolbarBottom = document.querySelector(".toolbar")?.getBoundingClientRect().bottom || 0;
  const readingLine = Math.min(window.innerHeight - 1, Math.max(Math.round(window.innerHeight * 0.4), Math.ceil(toolbarBottom) + 16));
  readObserver = new IntersectionObserver(
    (entries) => {
      if (detail.value?.id !== id) return;
      for (const entry of entries) {
        const index = Number((entry.target as HTMLElement).dataset.page);
        if (entry.isIntersecting) visiblePages.add(index);
        else visiblePages.delete(index);
      }
      const current = Math.min(...visiblePages);
      if (Number.isFinite(current) && current !== page.value) {
        page.value = current;
        persistProgress();
      }
    },
    { rootMargin: `-${readingLine}px 0px -${window.innerHeight - readingLine - 1}px 0px` },
  );
  document
    .querySelectorAll(".comic-page")
    .forEach((el) => readObserver!.observe(el));
}
function resizeReader() {
  observePages(false);
}
function refresh() {
  if (!props.itemId) void load();
}
watch(
  () => [props.module, props.itemId],
  () => {
    clearTimeout(progressTimer);
    viewer.value?.close();
    viewing.value = undefined;
    detail.value = undefined;
    items.value = [];
    readObserver?.disconnect();
    query.value = "";
    selectedTags.value = [];
    random.value = false;
    notice.value = "";
    void load();
  },
);
watch(sentinel, (el) => {
  observer?.disconnect();
  if (el) {
    observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting && next.value && !loading.value)
          void load(true);
      },
      { rootMargin: "300px" },
    );
    observer.observe(el);
  }
});
onMounted(() => {
  void load();
  window.addEventListener("library-changed", refresh);
  window.addEventListener("resize", resizeReader);
});
onUnmounted(() => {
  generation++;
  clearTimeout(searchTimer);
  clearTimeout(progressTimer);
  observer?.disconnect();
  readObserver?.disconnect();
  window.removeEventListener("library-changed", refresh);
  window.removeEventListener("resize", resizeReader);
});
</script>
<template>
  <section v-if="!detail" class="page-heading">
    <div>
      <h1>{{ name }}</h1>
      <p class="subtitle">{{ sub }}</p>
    </div>
    <span class="quiet-badge"
      >{{ total }} {{ isComic ? "本漫画" : module === "photos" ? "张照片" : "张图片" }}</span
    >
  </section>
  <p v-if="notice" class="action-notice" role="status">
    {{ notice }} <a href="#/trash">查看回收站<Icon name="chevron-right" /></a>
  </p>
  <p v-if="error" class="notice warning" role="alert">
    {{ error
    }}<button class="button small secondary" @click="load()">重新加载</button>
  </p>
  <template v-if="!itemId">
    <div class="collection-toolbar">
      <div v-if="isComic" class="search-field">
        <Icon name="search" /><input
          v-model="query"
          type="search"
          placeholder="搜索漫画名称"
          aria-label="搜索漫画名称"
          @input="search"
        />
      </div>
      <SegmentedControl v-else-if="module === 'images'" aria-label="图片浏览方式">
        <button
          :aria-pressed="!random"
          @click="
            random = false;
            load();
          "
        >
          列表浏览</button
        ><button
          :aria-pressed="random"
          @click="
            random = true;
            load();
          "
        >
          <Icon name="shuffle" />随机浏览
        </button>
      </SegmentedControl>
      <p v-else class="subtle-copy">保留照片原件与元数据</p>
      <button v-if="random" class="button secondary small" @click="load()">
        <Icon name="refresh" />换一批
      </button>
      <button v-if="!isComic && items.length" class="button secondary small"
        :aria-pressed="selecting" :disabled="saving || loading" @click="toggleSelectionMode">
        {{ selecting ? "完成选择" : "选择图片" }}
      </button>
      <UploadPanel ref="uploader" :key="module" :module="module" />
    </div>
    <div
      v-if="isComic && tags.length"
      class="tag-filters"
      aria-label="标签筛选"
    >
      <button
        :aria-pressed="!selectedTags.length"
        @click="
          selectedTags = [];
          load();
        "
      >
        全部标签</button
      ><button
        v-for="tag in tags"
        :key="tag"
        :aria-pressed="selectedTags.includes(tag)"
        @click="toggleTag(tag)"
      >
        {{ tag }}</button
      ><span v-if="selectedTags.length">同时满足所选标签</span>
    </div>
    <div v-if="selecting" class="selection-bar panel">
      <span role="status">已选 {{ selection.length }} / 100 项</span>
      <button class="text-link" :disabled="saving || loading" @click="selectLoaded">选择已加载项</button>
      <a v-if="selection.length" :href="selectedURL" class="button small secondary"
        ><Icon name="download" />打包下载</a
      ><button
        class="button small secondary"
        :disabled="saving || !selection.length"
        @click="trash(items.filter((i) => selection.includes(i.id)))"
      >
        <Icon name="trash" />移到回收站</button
      ><button class="text-link" :disabled="saving || !selection.length" @click="selection = []; selectionMessage = ''">清空选择</button>
    </div>
    <p v-if="selectionMessage" class="subtle-copy selection-feedback" role="status">{{ selectionMessage }}</p>
    <div
      v-if="items.length"
      :class="[
        'collection-grid',
        { 'comic-grid': isComic, 'photo-wall': random, 'is-selecting': selecting },
      ]"
      :aria-busy="loading"
    >
      <article
        v-for="item in items"
        :key="item.id"
        :class="[
          'content-card',
          { 'is-selected': selection.includes(item.id) },
        ]"
      >
        <a v-if="isComic" :href="`#/library/comics/${item.id}`" class="cover"
          ><img
            :src="previewURL(item)"
            :alt="item.name + '，第一页'"
            loading="lazy"
        /></a>
        <button
          v-else
          class="cover image-open"
          :aria-label="(selecting ? '选择 ' : '查看 ') + item.pages[0]?.name"
          :aria-pressed="selecting ? selection.includes(item.id) : undefined"
          :disabled="saving"
          @click="openImage(item)"
        >
          <img
            v-if="canPreview(item.pages[0])"
            :src="previewURL(item)"
            alt=""
            loading="lazy"
          /><span v-else class="file-placeholder"
            ><Icon name="image" /><span
              >{{ item.pages[0]?.name
              }}<small>原件已保存 · 下载查看</small></span
            ></span
          >
        </button>
        <label v-if="!isComic" class="card-select"
          ><input
            type="checkbox"
            :checked="selection.includes(item.id)"
            :disabled="saving"
            :aria-label="'选择 ' + item.pages[0]?.name"
            @change="toggleSelect(item.id)"
        /></label>
        <div v-if="isComic" class="content-card-info">
          <a :href="`#/library/comics/${item.id}`"
            ><h2>{{ item.name }}</h2></a
          >
          <div class="content-tags">
            <button v-for="tag in item.tags" :key="tag" @click="toggleTag(tag)">
              {{ tag }}
            </button>
          </div>
          <div class="content-meta">
            <span>{{ item.pages.length }} 页</span
            ><button
              class="icon-button"
              :aria-label="'编辑 ' + item.name"
              @click="edit(item)"
            >
              <Icon name="edit" /></button
            ><a
              class="icon-button"
              :href="`/api/items/${item.id}/download`"
              :aria-label="'下载 ' + item.name"
              ><Icon name="download"
            /></a>
          </div>
        </div>
        <div v-else class="photo-card-meta">
          <span
            >{{ fileSize(item.pages[0]?.size || 0)
            }}<small v-if="module === 'photos'" class="photo-backup-state">{{
              backupSavedAt && new Date(item.created) <= new Date(backupSavedAt)
                ? "最近备份已包含"
                : "已上传 · 待备份"
            }}</small></span
          ><a
            class="icon-button"
            :href="`/api/items/${item.id}/download`"
            aria-label="下载原件"
            ><Icon name="download"
          /></a>
        </div>
      </article>
    </div>
    <EmptyState
      v-else-if="!loading && !error"
      :icon="query || selectedTags.length ? 'search' : isComic ? 'book' : module === 'photos' ? 'camera' : 'image'"
      :title="query || selectedTags.length ? '没有匹配的漫画' : isComic ? '暂无漫画' : module === 'photos' ? '暂无照片' : '暂无图片'"
      :description="query || selectedTags.length ? '换个名称或清除筛选后再试。' : isComic ? '导入图片文件夹，按顺序连续阅读。' : module === 'photos' ? '上传后保留照片原件与元数据。' : '上传图片后，可以在这里浏览。'"
    >
      <button v-if="query || selectedTags.length" class="button secondary small" @click="query = ''; selectedTags = []; load()">清除筛选</button>
      <button v-else class="button secondary small" @click="uploader?.open()"><Icon name="upload" />{{ isComic ? '导入漫画' : module === 'photos' ? '上传照片' : '上传图片' }}</button>
    </EmptyState>
    <div v-if="next" ref="sentinel" class="load-more">
      <button class="button secondary" :disabled="loading" @click="load(true)">
        {{ loading ? "正在加载…" : "加载更多" }}
      </button>
    </div>
    <p v-else-if="loading" class="loading-copy" role="status">正在读取资料…</p>
    <p v-else-if="items.length" class="page-footnote">
      {{
        random ? "这一轮已全部浏览，点击「换一批」重新排列。" : "已显示全部内容"
      }}
    </p>
  </template>
  <template v-else-if="detail">
    <section class="page-heading reader-heading">
      <div>
        <h1>{{ detail.name }}</h1>
        <div class="content-tags">
          <span v-for="tag in detail.tags" :key="tag">{{ tag }}</span>
        </div>
      </div>
    </section>
    <PageActions>
        <button class="button secondary small" @click="edit(detail)">
          <Icon name="edit" />编辑</button
        ><a
          :href="`/api/items/${detail.id}/download`"
          class="button primary small"
          ><Icon name="download" />下载整本</a
        ><button
          class="icon-button"
          :disabled="saving"
          aria-label="将漫画移到回收站"
          @click="trash([detail])"
        >
          <Icon name="trash" />
        </button>
    </PageActions>
    <div class="reader-controls" role="group" aria-label="阅读页码">
      <label>
        <input
          type="number"
          :value="page + 1"
          min="1"
          :max="detail.pages.length"
          aria-label="跳转页码"
          @change="jumpToPage(Number(($event.target as HTMLInputElement).value) - 1)"
        />
        <span>/ {{ detail.pages.length }}</span>
      </label>
    </div>
    <div class="continuous-reader">
      <figure
        v-for="(_, index) in detail.pages"
        :id="'comic-page-' + index"
        :key="index"
        class="comic-page"
        :data-page="index"
      >
        <img
          :src="previewURL(detail, index, false)"
          :alt="'第 ' + (index + 1) + ' 页'"
          loading="lazy"
        />
      </figure>
    </div>
  </template>
  <dialog ref="editDialog" class="edit-dialog">
    <header class="dialog-header">
      <h2>编辑漫画</h2>
      <button
        class="icon-button"
        aria-label="关闭编辑"
        :disabled="saving"
        @click="editDialog?.close()"
      >
        <Icon name="close" />
      </button>
    </header>
    <form class="form-stack" @submit.prevent="saveEdit">
      <label>名称<input v-model="editName" required maxlength="160" /></label
      ><label>标签<input v-model="editTags" placeholder="用逗号分隔" /></label>
      <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
      <button class="button primary" :disabled="saving">
        {{ saving ? "正在保存…" : "保存修改" }}
      </button>
    </form>
  </dialog>
  <dialog
    ref="viewer"
    class="image-viewer"
    @close="viewing = undefined"
    @click="$event.target === viewer && viewer?.close()"
    @keydown.left.prevent="nextImage(-1)"
    @keydown.right.prevent="nextImage(1)"
  >
    <template v-if="viewing"
      ><header class="dialog-header">
        <span>{{ viewerIndex + 1 }} / {{ total }}</span>
        <div class="row-actions">
          <a
            class="button secondary small"
            :href="`/api/items/${viewing.id}/download`"
            ><Icon name="download" />下载原件</a
          ><button
            class="icon-button"
            :disabled="saving"
            aria-label="移到回收站"
            @click="trash([viewing])"
          >
            <Icon name="trash" /></button
          ><button
            class="icon-button"
            aria-label="关闭大图"
            @click="viewer?.close()"
          >
            <Icon name="close" />
          </button>
        </div>
      </header>
      <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
      <p v-if="viewerMoving" class="subtle-copy" role="status">正在加载下一张…</p>
      <div class="viewer-image">
        <button
          class="icon-button"
          aria-label="上一张"
          :disabled="viewerIndex <= 0 || viewerMoving || loading"
          @click="nextImage(-1)"
        >
          <Icon name="chevron-left" /></button
        ><img
          v-if="canPreview(viewing.pages[0])"
          :src="previewURL(viewing, 0, false)"
          :alt="viewing.pages[0]?.name"
        />
        <p v-else>原件已保存，此格式请下载后查看。</p>
        <button
          class="icon-button"
          aria-label="下一张"
          :disabled="!viewerCanNext || viewerMoving || loading"
          @click="nextImage(1)"
        >
          <Icon name="chevron-right" />
        </button>
      </div>
      <p class="viewer-caption">
        {{ viewing.pages[0]?.name }} ·
        {{ fileSize(viewing.pages[0]?.size || 0) }}
      </p></template
    >
  </dialog>
</template>
