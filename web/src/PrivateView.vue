<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch, nextTick } from "vue";
import Icon from "./Icon.vue";
import PrivateImage from "./PrivateImage.vue";
import { fileSize, jsonBody, APIError } from "./api";
import {
  vaultOpen,
  vaultConfigured,
  vaultIdle,
  vaultAPI,
  vaultStatus,
  unlockVault,
  lockVault,
} from "./vault";
type Item = {
  id: string;
  module: string;
  name: string;
  original: string;
  size: number;
  created: string;
  deleted: string;
  revision: number;
};
type Upload = { id: string; file: File; state: string; error: string };
const props = defineProps<{ module: string }>();
const isPhoto = computed(() => props.module === "private");
const title = computed(() => (isPhoto.value ? "私密照片" : "个人文件"));
const password = ref(""),
  error = ref(""),
  loading = ref(false),
  unlocking = ref(false),
  checked = ref(false),
  query = ref(""),
  trash = ref(false),
  random = ref(false);
const items = ref<Item[]>([]),
  total = ref(0),
  next = ref(""),
  seed = ref(""),
  snapshot = ref(0),
  selection = ref<string[]>([]),
  selecting = ref(false),
  queue = ref<Upload[]>([]),
  uploading = ref(false),
  uploadVisible = ref(false);
const picker = ref<HTMLInputElement>(),
  viewer = ref<HTMLDialogElement>(),
  viewing = ref<Item>(),
  editor = ref<HTMLDialogElement>(),
  editing = ref<Item>(),
  editName = ref(""),
  saving = ref(false);
const controller = new AbortController();
let loadGeneration = 0;
let searchTimer: ReturnType<typeof setTimeout> | undefined;
const batchURL = computed(
  () =>
    "/api/vault/download?" + selection.value.map((id) => "id=" + id).join("&"),
);
function showError(e: unknown) {
  if (e instanceof Error && e.name !== "AbortError") error.value = e.message;
}
function clearPrivate() {
  loadGeneration++;
  items.value = [];
  total.value = 0;
  selection.value = [];
  selecting.value = false;
  query.value = "";
  queue.value = [];
  uploadVisible.value = false;
  uploading.value = false;
  password.value = "";
  viewing.value = undefined;
  editing.value = undefined;
  editName.value = "";
  viewer.value?.close();
  editor.value?.close();
  error.value = "";
}
watch(vaultOpen, (open) => {
  if (open) void load();
  else clearPrivate();
});
async function unlock() {
  unlocking.value = true;
  error.value = "";
  const value = password.value;
  password.value = "";
  try {
    await unlockVault(value);
  } catch (e) {
    showError(e);
  } finally {
    unlocking.value = false;
  }
}
async function lock() {
  try {
    await lockVault();
  } catch {
    error.value = "页面已隐藏。未能连接服务，服务器将在闲置时间到达后锁定。";
  }
}
async function load(more = false) {
  if (!vaultOpen.value) return;
  if (more && (loading.value || !next.value)) return;
  const epoch = ++loadGeneration;
  loading.value = true;
  error.value = "";
  if (!more) {
    next.value = "";
    seed.value = "";
    snapshot.value = 0;
    selection.value = [];
    selecting.value = false;
    items.value = [];
  }
  try {
    const result = await vaultAPI<{
      items: Item[];
      total: number;
      next: string;
      seed: string;
      snapshot: number;
    }>("/api/vault/list", {
      method: "POST",
      signal: controller.signal,
      body: jsonBody({
        module: props.module,
        q: query.value,
        trash: trash.value,
        random: random.value,
        seed: seed.value,
        snapshot: snapshot.value,
        after: more ? next.value : "",
      }),
    });
    if (epoch !== loadGeneration) return;
    items.value = more ? [...items.value, ...result.items] : result.items;
    total.value = result.total;
    next.value = result.next;
    seed.value = result.seed;
    snapshot.value = result.snapshot;
  } catch (e) {
    showError(e);
  } finally {
    if (epoch === loadGeneration) loading.value = false;
  }
}
watch([trash, random], () => void load());
function search() {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void load(), 250);
}
function toggleSelectionMode() {
  selecting.value = !selecting.value;
  selection.value = [];
}
function toggle(id: string) {
  if (isPhoto.value) selecting.value = true;
  selection.value = selection.value.includes(id)
    ? selection.value.filter((v) => v !== id)
    : [...selection.value, id].slice(0, 100);
}
async function openPhoto(it: Item) {
  error.value = "";
  viewing.value = it;
  await nextTick();
  viewer.value?.showModal();
}
async function rename(it: Item) {
  editing.value = it;
  editName.value = it.name;
  await nextTick();
  editor.value?.showModal();
}
async function change(it: Item, action: string, name = "") {
  error.value = "";
  saving.value = true;
  try {
    await vaultAPI(`/api/vault/items/${it.id}`, {
      method: "PATCH",
      signal: controller.signal,
      body: jsonBody({ action, name, revision: it.revision }),
    });
    editor.value?.close();
    editing.value = undefined;
    editName.value = "";
    if (viewing.value?.id === it.id) {
      viewer.value?.close();
      viewing.value = undefined;
    }
    await load();
  } catch (e) {
    showError(e);
  } finally {
    saving.value = false;
  }
}
function choose(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  input.value = "";
  error.value = "";
  if (files.length + queue.value.length > 200) {
    error.value = "每批最多上传 200 个文件";
    return;
  }
  for (const file of files) {
    queue.value.push({
      id: crypto.randomUUID().replaceAll("-", ""),
      file,
      state: "等待上传",
      error: "",
    });
  }
  uploadVisible.value = true;
  void upload();
}
async function upload() {
  if (uploading.value) return;
  uploading.value = true;
  try {
    for (const task of queue.value) {
      if (!vaultOpen.value || controller.signal.aborted) break;
      if (task.state === "已完成") continue;
      task.state = "上传中";
      task.error = "";
      const limit = isPhoto.value ? 64 * 1024 * 1024 : 10 * 1024 * 1024 * 1024;
      if (task.file.size > limit) {
        task.state = "失败";
        task.error = isPhoto.value ? "照片最大 64 MiB" : "文件最大 10 GiB";
        continue;
      }
      try {
        await vaultAPI(`/api/vault/items/${task.id}`, {
          method: "PUT",
          signal: controller.signal,
          body: task.file,
          headers: {
            "X-File-Name": encodeURIComponent(task.file.name),
            "X-File-Size": String(task.file.size),
            "X-File-Module": props.module,
          },
        });
        task.state = "已完成";
      } catch (e) {
        task.state = "失败";
        task.error = e instanceof Error ? e.message : "上传失败";
        if (e instanceof APIError && (e.status === 423 || e.status === 401))
          break;
      }
    }
  } finally {
    uploading.value = false;
    if (vaultOpen.value && !controller.signal.aborted) await load();
  }
}
onMounted(async () => {
  try {
    await vaultStatus();
    checked.value = true;
    if (vaultOpen.value && !loading.value) await load();
  } catch (e) {
    checked.value = true;
    showError(e);
  }
});
onUnmounted(() => {
  controller.abort();
  clearTimeout(searchTimer);
  clearPrivate();
});
</script>
<template>
  <section class="page-heading">
    <div>
      <h1>{{ title }}</h1>
    </div>
    <div v-if="vaultOpen" class="private-heading-actions">
      <span class="quiet-badge"><Icon name="lock" />已解锁</span
      ><button class="button secondary" @click="lock">锁定</button>
    </div>
  </section>
  <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
  <section v-if="!vaultOpen" class="panel vault-gate">
    <span class="vault-gate-icon"><Icon name="lock" /></span>
    <h2>保险库已锁定</h2>
    <p v-if="!checked">正在检查配置…</p>
    <form v-else-if="vaultConfigured" @submit.prevent="unlock">
      <label for="vault-password">保险库口令</label
      ><input
        id="vault-password"
        v-model="password"
        type="password"
        autocomplete="off"
        required
        :disabled="unlocking"
        placeholder="输入独立口令"
      />
      <button class="button primary" :disabled="unlocking || !password">
        {{ unlocking ? "正在解锁…" : "解锁" }}<Icon name="arrow" />
      </button>
      <p class="vault-hint">闲置 {{ vaultIdle }} 分钟后自动锁定</p>
    </form>
    <p v-else>请在本机配置窗口中设置保险库口令。</p>
  </section>
  <template v-else>
    <div class="private-toolbar">
      <div class="view-switch">
        <button
          :class="{ active: !trash && !random }"
          @click="
            trash = false;
            random = false;
          "
        >
          列表</button
        ><button
          v-if="isPhoto"
          :class="{ active: random && !trash }"
          @click="
            trash = false;
            random = true;
          "
        >
          随机浏览</button
        ><button :class="{ active: trash }" @click="trash = true">
          回收站
        </button>
      </div>
      <div class="private-toolbar-actions">
        <button
          v-if="isPhoto && !trash && items.length"
          class="button secondary"
          :aria-pressed="selecting"
          :aria-label="selecting ? '完成选择' : '选择照片'"
          @click="toggleSelectionMode"
        >
          {{ selecting ? "完成" : "选择" }}
        </button>
        <button
          v-if="random && !trash"
          class="button secondary"
          @click="load()"
        >
          <Icon name="shuffle" />换一批</button
        ><button v-if="!trash" class="button primary" @click="picker?.click()">
          <Icon name="upload" />上传
        </button>
      </div>
      <input
        ref="picker"
        type="file"
        :accept="
          isPhoto ? 'image/jpeg,image/png,image/webp,image/gif' : undefined
        "
        multiple
        hidden
        @change="choose"
      />
    </div>
    <div v-if="!isPhoto" class="private-search">
      <Icon name="search" /><input
        v-model="query"
        type="search"
        aria-label="按文件名称搜索"
        placeholder="搜索文件名称"
        autocomplete="off"
        @input="search"
      />
    </div>
    <section
      v-if="uploadVisible"
      class="panel private-uploads"
      aria-label="私密上传任务"
    >
      <div class="section-heading">
        <h2>{{ uploading ? "正在加密上传" : "上传任务" }}</h2>
        <button
          v-if="!uploading"
          class="text-link"
          @click="
            queue = [];
            uploadVisible = false;
          "
        >
          收起
        </button>
      </div>
      <div v-for="task in queue" :key="task.id" class="private-upload-row">
        <span>{{ task.file.name }}</span
        ><small :class="{ 'inline-error': task.state === '失败' }">{{
          task.error || task.state
        }}</small>
      </div>
      <button
        v-if="!uploading && queue.some((t) => t.state === '失败')"
        class="button secondary"
        @click="upload"
      >
        重试失败项
      </button>
      <p class="vault-hint">
        {{
          isPhoto
            ? "原件最大 64 MiB，支持 JPEG、PNG、WebP、GIF。"
            : "单个文件最大 10 GiB。"
        }}锁定或离开此页会取消未完成的上传；重试会重新传输整个文件。
      </p>
    </section>
    <div class="section-heading private-count">
      <span
        >{{ trash ? "回收站 · " : "" }}{{ total }} 项<span
          v-if="selection.length"
        >
          · 已选 {{ selection.length }} 项</span
        ></span
      ><a
        v-if="selection.length && !trash"
        class="button secondary"
        :href="batchURL"
        ><Icon name="download" />打包下载</a
      >
    </div>
    <div
      v-if="isPhoto && !trash"
      class="private-gallery"
      :class="{ 'private-random': random, 'is-selecting': selecting }"
    >
      <article
        v-for="it in items"
        :key="it.id"
        class="private-photo-card"
        :class="{ 'is-selected': selection.includes(it.id) }"
      >
        <button
          class="private-photo-open"
          :aria-label="selecting ? '选择照片' : '查看私密照片'"
          :aria-pressed="selecting ? selection.includes(it.id) : undefined"
          @click="selecting ? toggle(it.id) : openPhoto(it)"
        >
          <PrivateImage :id="it.id" /></button
        ><label class="private-photo-select"
          ><input
            type="checkbox"
            :checked="selection.includes(it.id)"
            aria-label="选择照片"
            @change="toggle(it.id)"
        /></label>
        <div v-if="!selecting" class="private-photo-actions">
          <a
            :href="`/api/vault/items/${it.id}/download`"
            class="icon-button"
            aria-label="下载照片"
            ><Icon name="download" /></a
          ><button
            class="icon-button"
            aria-label="将照片移到回收站"
            :disabled="saving"
            @click="change(it, 'trash')"
          >
            <Icon name="trash" />
          </button>
        </div>
      </article>
    </div>
    <div v-else-if="items.length" class="panel private-file-list">
      <article v-for="it in items" :key="it.id" class="private-file-row">
        <input
          v-if="!trash"
          type="checkbox"
          :checked="selection.includes(it.id)"
          :aria-label="`选择 ${it.name}`"
          @change="toggle(it.id)"
        /><Icon :name="isPhoto ? 'image' : 'folder'" />
        <div class="private-file-name">
          <strong>{{ isPhoto ? "私密照片" : it.name }}</strong
          ><span
            >{{ fileSize(it.size) }} ·
            {{
              new Date(trash ? it.deleted : it.created).toLocaleDateString(
                "zh-CN",
              )
            }}</span
          >
        </div>
        <div class="private-file-actions">
          <button
            v-if="trash"
            class="button secondary"
            :disabled="saving"
            @click="change(it, 'restore')"
          >
            恢复</button
          ><template v-else
            ><button
              class="icon-button"
              :aria-label="`重命名 ${it.name}`"
              @click="rename(it)"
            >
              <Icon name="edit" /></button
            ><a
              class="icon-button"
              :href="`/api/vault/items/${it.id}/download`"
              :aria-label="`下载 ${it.name}`"
              ><Icon name="download" /></a
            ><button
              class="icon-button"
              :aria-label="`删除 ${it.name}`"
              :disabled="saving"
              @click="change(it, 'trash')"
            >
              <Icon name="trash" /></button
          ></template>
        </div>
      </article>
    </div>
    <div v-if="!loading && !items.length" class="panel private-empty">
      <Icon :name="trash ? 'trash' : isPhoto ? 'image' : 'folder'" />
      <p>{{ query ? "没有匹配的文件" : trash ? "回收站为空" : "暂无内容" }}</p>
      <button
        v-if="!query && !trash"
        class="button secondary"
        @click="picker?.click()"
      >
        选择文件
      </button>
    </div>
    <div class="private-more">
      <span v-if="loading">正在加载…</span
      ><button v-else-if="next" class="button secondary" @click="load(true)">
        加载更多
      </button>
    </div>
    <dialog
      ref="viewer"
      class="private-viewer"
      @close="viewing = undefined"
      @click="$event.target === viewer && viewer?.close()"
    >
      <template v-if="viewing"
        ><div class="private-viewer-toolbar">
          <span>私密照片</span>
          <button class="icon-button" aria-label="锁定并关闭预览" @click="lock">
            <Icon name="lock" />
          </button>
          <a
            class="icon-button"
            :href="`/api/vault/items/${viewing.id}/download`"
            aria-label="下载原件"
            ><Icon name="download"
          /></a>
          <button
            class="icon-button"
            aria-label="将这张照片移到回收站"
            :disabled="saving"
            @click="change(viewing, 'trash')"
          >
            <Icon name="trash" />
          </button>
          <button
            class="icon-button"
            aria-label="关闭预览"
            @click="viewer?.close()"
          >
            <Icon name="close" />
          </button>
        </div>
        <p v-if="error" class="inline-error private-viewer-error" role="alert">
          {{ error }}
        </p>
        <PrivateImage :key="viewing.id" :id="viewing.id" original
      /></template>
    </dialog>
    <dialog
      ref="editor"
      class="private-editor"
      @close="
        editing = undefined;
        editName = '';
      "
    >
      <form
        v-if="editing"
        @submit.prevent="change(editing, 'rename', editName)"
      >
        <h2>修改名称</h2>
        <label for="private-name">文件名称</label
        ><input
          id="private-name"
          v-model="editName"
          maxlength="160"
          required
          autocomplete="off"
        />
        <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
        <div class="private-editor-actions">
          <button
            type="button"
            class="button secondary"
            @click="editor?.close()"
          >
            取消</button
          ><button
            class="button primary"
            :disabled="saving || !editName.trim()"
          >
            保存
          </button>
        </div>
      </form>
    </dialog>
  </template>
</template>
