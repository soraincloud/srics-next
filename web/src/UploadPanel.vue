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
  type Upload,
} from "./uploads";
const props = defineProps<{ module: string }>();
const dialog = ref<HTMLDialogElement>(),
  input = ref<HTMLInputElement>(),
  selected = ref<File[]>([]),
  name = ref(""),
  tags = ref(""),
  error = ref(""),
  working = ref(false),
  resume = ref<Upload>();
const batchIDs = ref<string[]>([]);
const invalid = computed(() =>
  selected.value.filter(
    (f) =>
      f.size === 0 ||
      f.size > 64 * 1024 * 1024 ||
      (props.module === "comics" &&
        (f.webkitRelativePath.split("/").length > 2 ||
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
  ).filter((f) => !f.name.startsWith("."));
  selected.value = files.sort(
    (a, b) =>
      a.name.localeCompare(b.name, "en", { numeric: true }) ||
      (a.name < b.name ? -1 : 1),
  );
  if (!resume.value)
    name.value =
      files[0]?.webkitRelativePath.split("/")[0] || files[0]?.name || "";
  error.value = "";
}
async function open(task?: Upload) {
  resume.value = task;
  batchIDs.value = [];
  name.value = task?.name || "";
  tags.value = task?.tags.join("，") || "";
  selected.value = [];
  error.value = "";
  dialog.value?.showModal();
  await nextTick();
  input.value?.focus();
}
async function start() {
  if (working.value || !selected.value.length || invalid.value.length) return;
  working.value = true;
  error.value = "";
  try {
    if (resume.value) {
      await resumeUpload(resume.value, selected.value);
    } else if (props.module === "comics") {
      batchIDs.value[0] ||= crypto.randomUUID().replaceAll("-", "");
      const id = await createUpload(
        props.module,
        name.value,
        tags.value
          .split(/[,，]/)
          .map((s) => s.trim())
          .filter(Boolean),
        selected.value,
        batchIDs.value[0],
      );
      await runUpload(id);
    } else {
      for (let index = 0; index < selected.value.length; index++) {
        const file = selected.value[index]!;
        batchIDs.value[index] ||= crypto.randomUUID().replaceAll("-", "");
        const id = await createUpload(
          props.module,
          file.name,
          [],
          [file],
          batchIDs.value[index],
        );
        if (uploads.value.find((task) => task.id === id)?.state !== "complete")
          await runUpload(id);
      }
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
</script>
<template>
  <div class="upload-actions">
    <button class="button primary" @click="open()">
      <Icon name="upload" />{{ module === "comics" ? "导入漫画" : "上传图片" }}
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
              ? "导入一本漫画"
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
      <label class="file-picker"
        ><Icon :name="module === 'comics' ? 'folder' : 'image'" /><strong>{{
          module === "comics" ? "选择漫画文件夹" : "选择一张或多张图片"
        }}</strong
        ><span
          >{{
            module === "comics"
              ? "一层图片目录，页序按文件名中的数字排列"
              : "保留原始文件及元数据"
          }}
          · 单文件最多 64 MB</span
        ><input
          ref="input"
          type="file"
          :webkitdirectory="module === 'comics' || undefined"
          multiple
          :accept="
            module === 'comics' || module === 'photos'
              ? undefined
              : 'image/jpeg,image/png,image/webp,image/gif'
          "
          :disabled="working"
          @change="choose"
      /></label>
      <template v-if="selected.length">
        <p class="selection-summary">
          已选择 {{ selected.length }} 个文件 ·
          {{ fileSize(selected.reduce((n, f) => n + f.size, 0)) }}
        </p>
        <div v-if="invalid.length" class="notice warning">
          <div>
            <strong>这些文件需要处理</strong>
            <ul>
              <li v-for="f in invalid" :key="f.webkitRelativePath + f.name">
                {{ f.webkitRelativePath || f.name }}
              </li>
            </ul>
            <p>不支持嵌套目录、空文件、超大文件或此漫画格式。</p>
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
        <template v-if="module === 'comics'"
          ><label
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
      <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
      <p v-if="working" class="save-status" aria-live="polite">
        正在保存并校验，请保留此页面。每个成功的文件都会记录进度。
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
