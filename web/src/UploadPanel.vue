<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import Icon from "./Icon.vue";
import { api, fileSize } from "./api";
import {
  createUpload,
  loadUploads,
  pauseUpload,
  resumeUpload,
  runUpload,
  running,
  uploads,
  uploadProgress,
  type Upload,
} from "./uploads";
const props = defineProps<{ module: string }>();
const dialog = ref<HTMLDialogElement>(),
  input = ref<HTMLInputElement>(),
  picker = ref<HTMLButtonElement>(),
  selected = ref<File[]>([]),
  name = ref(""),
  tags = ref(""),
  error = ref(""),
  working = ref(false),
  resume = ref<Upload>();
const pickerLabel = computed(() =>
  props.module === "comics"
    ? batchComic.value ? "选择漫画总目录" : "选择漫画文件夹"
    : props.module === "attachments"
      ? "选择一个或多个文件"
      : "选择一张或多张图片",
);
const batchIDs = ref<string[]>([]),
  batchComic = ref(false),
  batchNames = ref<Record<string, string>>({});
const groups = computed(() => {
  const grouped = new Map<string, File[]>();
  for (const file of selected.value) {
    const key = batchComic.value
      ? file.webkitRelativePath.split("/")[1] || file.name
      : name.value;
    if (!grouped.has(key)) grouped.set(key, []);
    grouped.get(key)!.push(file);
  }
  return Array.from(grouped, ([key, files]) => ({ key, files }));
});
const invalid = computed(() =>
  selected.value.filter(
    (f) =>
      (f.size === 0 && props.module !== "attachments") ||
      f.size > (props.module === "attachments" ? 10 * 1024 ** 3 : 64 * 1024 ** 2) ||
      (props.module === "comics" &&
        ((batchComic.value
          ? f.webkitRelativePath.split("/").length !== 3
          : f.webkitRelativePath.split("/").length > 2) ||
          !/\.(webp|jpe?g|png)$/i.test(f.name))),
  ),
);
const pending = computed(() =>
  uploads.value.filter(
    (u) =>
      u.module === props.module && !["complete", "cancelled"].includes(u.state),
  ),
);
function choose(event: Event) {
  batchIDs.value = [];
  const files = Array.from(
    (event.target as HTMLInputElement).files || [],
  ).filter((f) => props.module === "attachments" || !f.name.startsWith("."));
  selected.value = files.sort(
    (a, b) =>
      a.name.localeCompare(b.name, "en", { numeric: true }) ||
      (a.name < b.name ? -1 : 1),
  );
  if (!resume.value)
    name.value =
      (files[0]?.webkitRelativePath.split("/")[0] || files[0]?.name || "").slice(0, 160);
  batchNames.value = Object.fromEntries(
    groups.value.map((g) => [g.key, g.key]),
  );
  error.value = "";
}
async function open(task?: Upload) {
  resume.value = task;
  batchComic.value = false;
  batchIDs.value = [];
  name.value = task?.name || "";
  tags.value = task?.tags.join("，") || "";
  selected.value = [];
  error.value = "";
  dialog.value?.showModal();
  await nextTick();
  // Reset the native selection too, so choosing the same source again emits
  // a change after reopening or when resuming a paused task.
  if (input.value) input.value.value = "";
  picker.value?.focus();
}
function selectFiles() {
  // Clearing the native value permits reselecting the same files. The visible
  // selection remains intact if the system chooser is cancelled.
  if (input.value) {
    input.value.value = "";
    input.value.click();
  }
}
async function start() {
  if (working.value || !selected.value.length || invalid.value.length) return;
  working.value = true;
  error.value = "";
  try {
    if (resume.value) {
      await resumeUpload(resume.value, selected.value);
    } else {
      const batches =
        props.module === "comics"
          ? groups.value
          : selected.value.map((file) => ({ key: file.name, files: [file] }));
      const ids: string[] = [];
      for (let i = 0; i < batches.length; i++) {
        const group = batches[i]!;
        batchIDs.value[i] ||= crypto.randomUUID().replaceAll("-", "");
        ids.push(
          await createUpload(
            props.module,
            props.module === "comics"
              ? batchComic.value
                ? batchNames.value[group.key] || group.key
                : name.value
              : props.module === "attachments" && batches.length === 1 ? name.value : group.key.slice(0, 160),
            props.module === "comics"
              ? tags.value
                  .split(/[,，]/)
                  .map((s) => s.trim())
                  .filter(Boolean)
              : [],
            group.files,
            batchIDs.value[i],
          ),
        );
      }
      for (const id of ids)
        if (uploads.value.find((task) => task.id === id)?.state !== "complete")
          await runUpload(id);
    }
    dialog.value?.close();
    selected.value = [];
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    working.value = false;
  }
}
async function cancel(task: Upload) {
  try {
    await api(`/api/uploads/${task.id}`, { method: "DELETE" });
    await loadUploads();
  } catch (e) {
    error.value = (e as Error).message;
  }
}
onMounted(() => loadUploads().catch((e) => (error.value = e.message)));
defineExpose({ open: () => open() });
</script>
<template>
  <div class="upload-actions">
    <button class="button primary" @click="open()">
      <Icon name="upload" />{{ module === "comics" ? "导入漫画" : module === "attachments" ? "上传文件" : module === "photos" ? "上传照片" : "上传图片" }}
    </button>
  </div>
  <section
    v-if="pending.length"
    class="upload-pending panel"
    aria-label="待完成上传"
  >
    <div v-for="task in pending" :key="task.id" class="task-row">
      <Icon :name="running === task.id ? 'clock' : 'upload'" />
      <div>
        <strong>{{ task.name }}</strong>
        <p>
          {{ task.files.filter((f) => f.page).length }} /
          {{ task.files.length }} 个文件已保存<span v-if="running === task.id">
            · 上传中</span
          >
        </p>
        <p v-if="task.error" class="inline-error">{{ task.error }}</p>
      </div>
      <button
        v-if="running === task.id"
        class="button small secondary"
        @click="pauseUpload"
      >
        暂停</button
      ><template v-else
        ><button
          class="button small secondary"
          :disabled="!!running"
          @click="open(task)"
        >
          选择原文件继续</button
        ><button
          class="icon-button"
          aria-label="取消上传任务"
          @click="cancel(task)"
        >
          <Icon name="close" /></button
      ></template>
    </div>
  </section>
  <dialog
    ref="dialog"
    class="upload-dialog"
    @cancel="working && $event.preventDefault()"
  >
    <header class="dialog-header">
      <div>
        <p class="eyebrow">{{ resume ? "继续上传" : "添加到资料库" }}</p>
        <h2>
          {{
            module === "comics"
              ? batchComic
                ? "批量导入漫画"
                : "导入一本漫画"
              : module === "attachments"
                ? "上传文件"
              : module === "photos"
                ? "保存照片原件"
                : "上传图片"
          }}
        </h2>
      </div>
      <button
        class="icon-button"
        aria-label="关闭上传窗口"
        :disabled="working"
        @click="dialog?.close()"
      >
        <Icon name="close" />
      </button>
    </header>
    <form class="form-stack" @submit.prevent="start">
      <label v-if="module === 'comics' && !resume" class="batch-choice"
        ><input
          v-model="batchComic"
          type="checkbox"
          :disabled="working || !!selected.length"
        />批量导入多个漫画文件夹（选择它们的共同上级目录）</label
      >
      <button
        ref="picker"
        type="button"
        class="file-picker"
        :disabled="working"
        @click="selectFiles"
      >
        <Icon :name="module === 'comics' || module === 'attachments' ? 'folder' : 'image'" />
        <strong>{{ selected.length ? `重新${pickerLabel}` : pickerLabel }}</strong>
        <span v-if="selected.length" class="selection-summary" role="status">
          已选择 {{ selected.length }} 个文件 ·
          {{ fileSize(selected.reduce((n, f) => n + f.size, 0)) }}
        </span>
        <span v-else>{{
            module === "comics"
              ? batchComic
                ? "选择包含各本漫画文件夹的总目录，页序按数字排列"
                : "一层图片目录，页序按文件名中的数字排列"
              : module === "images" ? "PNG / JPEG 无损转 WebP，WebP / GIF 保留原件" : "保留原始文件及元数据"
          }}
          · 单文件最多 {{ module === 'attachments' ? '10 GiB' : '64 MiB' }}</span>
      </button>
      <p v-if="module === 'images' && !resume" class="subtle-copy">WebP 与 GIF 混合保存，统一按 IMG-000001 等编号命名，不保留原文件名。</p>
      <input
          ref="input"
          type="file"
          hidden
          :webkitdirectory="module === 'comics' || undefined"
          multiple
          :accept="
            module === 'comics' || module === 'photos' || module === 'attachments'
              ? undefined
              : 'image/jpeg,image/png,image/webp,image/gif'
          "
          :disabled="working"
          @change="choose"
      />
      <template v-if="selected.length">
        <div v-if="invalid.length" class="notice warning">
          <div>
            <strong>这些文件需要处理</strong>
            <ul>
              <li v-for="f in invalid" :key="f.webkitRelativePath + f.name">
                {{ f.webkitRelativePath || f.name }}
              </li>
            </ul>
            <p>
              {{ module === 'attachments' ? '请检查文件大小，单文件最多 10 GiB。' : module === 'comics' ? '请检查目录层级、空文件、超大文件或格式。批量模式只接受“总目录 / 漫画目录 / 图片”三层结构。' : '请检查空文件、超大文件或格式，单文件最多 64 MiB。' }}
            </p>
            <button
              v-if="!resume"
              type="button"
              class="button small secondary"
              @click="selected = selected.filter((f) => !invalid.includes(f))"
            >
              明确排除这些文件
            </button>
          </div>
        </div>
        <template v-if="module === 'comics'">
          <div v-if="batchComic" class="form-stack">
            <label v-for="group in groups" :key="group.key"
              >{{ group.key }} · {{ group.files.length }} 页<input
                v-model="batchNames[group.key]"
                required
                maxlength="160"
                :disabled="working || !!batchIDs.length"
            /></label>
          </div>
          <label v-else
            >漫画名称<input
              v-model="name"
              required
              maxlength="160"
              :disabled="working || !!resume || !!batchIDs.length"
              placeholder="设置一个方便找到的名称" /></label
          ><label
            >标签<input
              v-model="tags"
              :disabled="working || !!resume || !!batchIDs.length"
              placeholder="用逗号分隔，例如：日常，冒险"
          /></label>
          <details class="file-order">
            <summary>查看导入页序</summary>
            <ol>
              <li v-for="(f, i) in selected" :key="f.name">
                {{ String(i + 1).padStart(3, "0") }} · {{ f.name }}
              </li>
            </ol>
          </details></template
        >
      </template>
      <label v-if="module === 'attachments' && selected.length === 1 && !resume">
        文件名称<input v-model="name" required maxlength="160" :disabled="working || !!batchIDs.length" />
      </label>
      <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
      <p v-if="working" class="save-status" aria-live="polite">
        {{ uploadProgress || "正在准备上传…" }}。分块进度已保存，刷新后可继续。
      </p>
      <div class="dialog-actions">
        <button
          v-if="working"
          type="button"
          class="button secondary"
          @click="pauseUpload"
        >
          暂停上传</button
        ><button
          class="button primary"
          :disabled="
            working || !!running || !selected.length || !!invalid.length
          "
        >
          {{
            working
              ? "正在上传…"
              : resume || batchIDs.length
                ? "继续上传"
                : "开始导入"
          }}<Icon name="arrow" />
        </button>
      </div>
    </form>
  </dialog>
</template>
