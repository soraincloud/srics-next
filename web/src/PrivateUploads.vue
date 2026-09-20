<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted } from "vue";
import { vaultAPI, vaultOpen } from "./vault";
import { sendTransfer, prepareTransfer, type Transfer } from "./transfers";
const props = defineProps<{ module: string }>();
const emit = defineEmits<{ complete: [] }>();
type Task = Partial<Transfer> & {
  id: string;
  name: string;
  size: number;
  file?: File;
  prepared?: Transfer;
  message: string;
};
const tasks = ref<Task[]>([]),
  busy = ref(false),
  error = ref(""),
  visible = ref(true),
  picker = ref<HTMLInputElement>(),
  resume = ref<Task>();
let abort = new AbortController();
let generation = 0;
async function load() {
  if (!vaultOpen.value) return;
  const epoch = generation;
  try {
    const data = await vaultAPI<Transfer[]>("/api/vault/transfers");
    if (epoch === generation)
      tasks.value = data
        .filter((t) => t.module === props.module)
        .map((t) => ({
          ...t,
          message: `已保存 ${t.done.length}/${t.hashes.length} 块`,
        }));
  } catch (e) {
    if (epoch === generation) error.value = (e as Error).message;
  }
}
function clear() {
  generation++;
  abort.abort();
  tasks.value = [];
  error.value = "";
  busy.value = false;
  resume.value = undefined;
}
watch(vaultOpen, (open) => {
  if (open) void load();
  else clear();
});
onMounted(load);
onUnmounted(clear);
function chooseFiles(task?: Task) {
  resume.value = task;
  visible.value = true;
  picker.value?.click();
}
defineExpose({ chooseFiles });
async function choose(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  input.value = "";
  error.value = "";
  if (!files.length) return;
  if (busy.value) return;
  if (resume.value) {
    const t = resume.value;
    if (
      files.length !== 1 ||
      files[0]!.name !== t.name ||
      files[0]!.size !== t.size
    ) {
      error.value = "请选择该任务原来的单个文件";
      return;
    }
    t.file = files[0];
  } else {
    if (files.length + tasks.value.length > 200) {
      error.value = "每批最多 200 项";
      return;
    }
    for (const file of files) {
      if (
        file.size >
        (props.module === "private"
          ? 64 * 1024 * 1024
          : 10 * 1024 * 1024 * 1024)
      ) {
        error.value = "文件超过大小限制";
        return;
      }
    }
    tasks.value.push(
      ...files.map((file) => ({
        id: crypto.randomUUID().replaceAll("-", ""),
        name: file.name,
        size: file.size,
        file,
        message: "等待上传",
      })),
    );
  }
  await run();
}
async function run() {
  if (busy.value) return;
  busy.value = true;
  abort = new AbortController();
  const epoch = generation;
  try {
    // Register the whole queue before transferring bodies, so a refresh keeps waiting tasks too.
    for (const task of tasks.value) {
      if (!task.file || task.state === "complete") continue;
      task.prepared = await prepareTransfer(
        task.file,
        { id: task.id, module: props.module, parent: "", index: 0 },
        vaultAPI,
        abort.signal,
        (text) => (task.message = text),
      );
      task.state = task.prepared.state;
      task.message =
        task.state === "complete" ? "已完成" : "任务已保存，等待上传";
      if (task.state === "complete") task.file = undefined;
    }
    for (const task of tasks.value) {
      if (!task.file || task.state === "complete") continue;
      abort.signal.throwIfAborted();
      task.message = "准备文件…";
      await sendTransfer(
        task.file,
        { id: task.id, module: props.module, parent: "", index: 0 },
        vaultAPI,
        abort.signal,
        (text) => (task.message = text),
        task.prepared,
      );
      task.state = "complete";
      task.message = "已完成";
      task.file = undefined;
    }
  } catch (e) {
    if (epoch === generation)
      error.value =
        (e as Error).name === "AbortError"
          ? "已暂停，已保存分块可以继续。"
          : (e as Error).message;
  } finally {
    if (epoch === generation) {
      busy.value = false;
      emit("complete");
    }
  }
}
async function cancel(task: Task) {
  try {
    if (task.state)
      await vaultAPI(`/api/vault/transfers/${task.id}`, { method: "DELETE" });
    tasks.value = tasks.value.filter((t) => t.id !== task.id);
  } catch (e) {
    error.value = (e as Error).message;
  }
}
</script>
<template>
  <input
    ref="picker"
    type="file"
    hidden
    :multiple="!resume"
    :accept="
      module === 'private'
        ? 'image/jpeg,image/png,image/webp,image/gif'
        : undefined
    "
    @change="choose"
  />
  <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
  <section
    v-if="vaultOpen && tasks.length && visible"
    class="panel private-uploads"
    aria-label="私密上传任务"
  >
    <div class="section-heading">
      <h2>上传任务</h2>
      <button v-if="busy" class="button small secondary" @click="abort.abort()">
        暂停</button
      ><button v-else class="button small secondary" @click="visible = false">
        收起
      </button>
    </div>
    <div v-for="task in tasks" :key="task.id" class="private-upload-row">
      <span>{{ task.name }}</span
      ><small>{{ task.message }}</small
      ><template v-if="!busy && task.state !== 'complete'"
        ><button class="button small secondary" @click="chooseFiles(task)">
          选择原文件继续</button
        ><button class="button small secondary" @click="cancel(task)">
          取消任务
        </button></template
      >
    </div>
    <button
      v-if="!busy && tasks.some((t) => t.file && t.state !== 'complete')"
      class="button secondary"
      @click="run"
    >
      继续上传
    </button>
    <p class="vault-hint">
      {{
        module === "private" ? "原件最大 64 MiB。" : "单文件最大 10 GiB。"
      }}分块和任务名称均加密保存。锁定或刷新后，重新选择原文件即可续传。
    </p>
  </section>
</template>
