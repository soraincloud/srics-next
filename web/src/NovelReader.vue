<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import EmptyState from "./EmptyState.vue";
import ActionConfirm from "./ActionConfirm.vue";
const confirmation = ref<InstanceType<typeof ActionConfirm>>();
import {
  api,
  APIError,
  changed,
  csrf,
  jsonBody,
  protectUnsavedSession,
  sessionExpired,
} from "./api";
import { setNavigationGuard } from "./navigation";
import { newID, type Chapter, type ChapterVersion, type Novel } from "./novels";
const props = defineProps<{ novel: Novel }>();
const emit = defineEmits<{ reload: []; edit: []; trash: [] }>();
const chapter = ref<Chapter>(),
  editing = ref(false),
  title = ref(""),
  body = ref("");
const loading = ref(false),
  saving = ref(false),
  actionBusy = ref(false),
  error = ref(""),
  saveError = ref(""),
  conflict = ref<Chapter>();
const deleted = ref(false),
  fontSize = ref(18),
  reader = ref<HTMLElement>();
const newDialog = ref<HTMLDialogElement>(),
  newTitle = ref(""),
  newError = ref(""),
  historyDialog = ref<HTMLDialogElement>();
const versions = ref<ChapterVersion[]>([]),
  version = ref<ChapterVersion>(),
  historyError = ref("");
const discardDialog = ref<HTMLDialogElement>(),
  discardMessage = ref("");
const loginDialog = ref<HTMLDialogElement>(),
  password = ref(""),
  loginError = ref("");
const dirty = computed(
  () =>
    !!chapter.value &&
    (title.value !== chapter.value.title ||
      body.value !== (chapter.value.body || "")),
);
const blocked = computed(
  () => loading.value || saving.value || actionBusy.value,
);
const index = computed(() =>
  props.novel.chapters.findIndex((c) => c.id === chapter.value?.id),
);
const saveStatus = computed(() =>
  saving.value
    ? "保存中…"
    : saveError.value
      ? "保存失败，正文已保留在当前页面"
      : dirty.value
        ? "尚未保存"
        : "已保存",
);
const chapterPath = (id = chapter.value?.id) =>
  "/api/novels/" + props.novel.item.id + "/chapters/" + id;
let timer: ReturnType<typeof setTimeout> | undefined,
  generation = 0,
  createId = "",
  composing = false,
  stopped = false;
let unguard: () => void = () => {};
function schedule() {
  clearTimeout(timer);
  if (
    !stopped &&
    editing.value &&
    dirty.value &&
    !saveError.value &&
    !saving.value &&
    !composing
  )
    timer = setTimeout(() => void save(true), 1500);
}
watch([title, body], schedule, { flush: "sync" });
watch(
  [dirty, saving],
  () => {
    protectUnsavedSession.value = dirty.value || saving.value;
  },
  { flush: "sync" },
);
let discardPending: Promise<boolean> | undefined;
let resolveDiscard: ((value: boolean) => void) | undefined;
function askDiscard(message: string) {
  if (discardPending) return discardPending;
  discardMessage.value = message;
  discardPending = new Promise<boolean>((resolve) => {
    resolveDiscard = resolve;
  });
  discardDialog.value?.showModal();
  return discardPending;
}
function answerDiscard(value: boolean) {
  discardDialog.value?.close();
  resolveDiscard?.(value);
  resolveDiscard = undefined;
  discardPending = undefined;
}
async function leave() {
  if (saving.value) {
    error.value = "正在保存，请稍候。";
    return false;
  }
  if (!dirty.value) return true;
  clearTimeout(timer);
  if (
    !(await askDiscard("当前章节有未保存的修改。放弃后将无法恢复这些修改。"))
  ) {
    schedule();
    return false;
  }
  title.value = chapter.value?.title || "";
  body.value = chapter.value?.body || "";
  clearTimeout(timer);
  saveError.value = "";
  conflict.value = undefined;
  return true;
}
function beforeUnload(e: BeforeUnloadEvent) {
  if (dirty.value || saving.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
async function choose(id: string, edit = editing.value, scroll = true) {
  if (blocked.value || !(await leave())) return;
  const current = ++generation;
  loading.value = true;
  error.value = "";
  saveError.value = "";
  conflict.value = undefined;
  try {
    const c = await api<Chapter>(chapterPath(id));
    if (stopped || current !== generation) return;
    if (c.deleted) throw new Error("该章节已删除，请从已删除章节中恢复");
    chapter.value = c;
    title.value = c.title;
    body.value = c.body || "";
    editing.value = edit;
    clearTimeout(timer);
    if (!edit) await markRead(id);
    await nextTick();
    if (scroll)
      reader.value?.scrollIntoView({ block: "start", behavior: "instant" });
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    if (current === generation) loading.value = false;
  }
}
async function markRead(id: string) {
  try {
    await api("/api/novels/" + props.novel.item.id + "/progress", {
      method: "PUT",
      body: jsonBody({ chapter: id }),
    });
  } catch {
    error.value = "阅读进度暂未保存，重新选择章节可重试。";
  }
}
async function save(automatic = false) {
  clearTimeout(timer);
  if (!chapter.value || saving.value) return false;
  if (!dirty.value) return true;
  if (!title.value.trim()) {
    saveError.value = "请填写章节标题";
    return false;
  }
  const c = chapter.value,
    sentTitle = title.value,
    sentBody = body.value;
  saving.value = true;
  saveError.value = "";
  try {
    const saved = await api<Chapter>(chapterPath(c.id), {
      method: "PUT",
      body: jsonBody({
        title: sentTitle,
        body: sentBody,
        revision: c.revision,
        automatic,
      }),
    });
    if (stopped) return false;
    chapter.value = saved;
    if (title.value === sentTitle) title.value = saved.title;
    conflict.value = undefined;
    if (c.title !== saved.title) emit("reload");
    changed();
    return true;
  } catch (e) {
    saveError.value = (e as Error).message;
    if (e instanceof APIError && e.status === 409) {
      try {
        conflict.value = await api<Chapter>(chapterPath(c.id));
      } catch {}
      saveError.value =
        "此章节已在另一处修改，自动保存已暂停。你的文字仍在编辑器中，请先对照服务器版本。";
    }
    return false;
  } finally {
    saving.value = false;
    if (!saveError.value && !stopped) schedule();
  }
}
async function mode(edit: boolean) {
  if (edit === editing.value || blocked.value) return;
  if (!edit && dirty.value && (!(await save()) || dirty.value)) return;
  editing.value = edit;
  error.value = "";
  if (!edit && chapter.value) await markRead(chapter.value.id);
}
async function editNovel() {
  if (!blocked.value && (await leave())) emit("edit");
}
async function trashNovel() {
  if (!blocked.value && (await leave())) emit("trash");
}
async function beginChapter() {
  if (blocked.value || !(await leave())) return;
  createId = newID();
  newTitle.value = "";
  newError.value = "";
  newDialog.value?.showModal();
}
async function createChapter() {
  actionBusy.value = true;
  newError.value = "";
  try {
    const c = await api<Chapter>(
      "/api/novels/" + props.novel.item.id + "/chapters",
      {
        method: "POST",
        body: jsonBody({
          id: createId,
          title: newTitle.value,
          body: "",
          revision: props.novel.item.revision,
        }),
      },
    );
    newDialog.value?.close();
    emit("reload");
    chapter.value = c;
    title.value = c.title;
    body.value = c.body || "";
    editing.value = true;
    deleted.value = false;
    saveError.value = "";
    clearTimeout(timer);
  } catch (e) {
    newError.value = (e as Error).message;
    if (e instanceof APIError && e.status === 409) emit("reload");
  } finally {
    actionBusy.value = false;
  }
}
async function move(c: Chapter, delta: number) {
  if (blocked.value) return;
  const ids = props.novel.chapters.map((v) => v.id),
    from = ids.indexOf(c.id),
    to = from + delta;
  if (to < 0 || to >= ids.length) return;
  [ids[from], ids[to]] = [ids[to]!, ids[from]!];
  actionBusy.value = true;
  error.value = "";
  try {
    await api("/api/novels/" + props.novel.item.id + "/order", {
      method: "PUT",
      body: jsonBody({ ids, revision: props.novel.item.revision }),
    });
    emit("reload");
  } catch (e) {
    error.value = (e as Error).message;
    emit("reload");
  } finally {
    actionBusy.value = false;
  }
}
async function purgeChapter(c: Chapter) {
  if (
    blocked.value ||
    !(await confirmation.value?.ask(
      "彻底删除章节？",
      "章节正文和全部修订将从当前资料库永久删除，不能撤销。",
    ))
  )
    return;
  actionBusy.value = true;
  try {
    await api(chapterPath(c.id) + "/purge", {
      method: "POST",
      body: jsonBody({ revision: c.revision }),
    });
    emit("reload");
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    actionBusy.value = false;
  }
}
async function trashChapter(c: Chapter, restore = false) {
  if (blocked.value || !(await leave())) return;
  actionBusy.value = true;
  error.value = "";
  try {
    await api(chapterPath(c.id) + (restore ? "/restore" : ""), {
      method: restore ? "POST" : "DELETE",
      body: jsonBody({ revision: c.revision }),
    });
    if (!restore && chapter.value?.id === c.id) {
      chapter.value = undefined;
      title.value = "";
      body.value = "";
      clearTimeout(timer);
    }
    emit("reload");
    changed();
  } catch (e) {
    error.value = (e as Error).message;
    emit("reload");
  } finally {
    actionBusy.value = false;
  }
}
async function history() {
  if (!chapter.value || blocked.value) return;
  historyError.value = "";
  version.value = undefined;
  versions.value = [];
  historyDialog.value?.showModal();
  try {
    versions.value = await api<ChapterVersion[]>(chapterPath() + "/versions");
  } catch (e) {
    historyError.value = (e as Error).message;
  }
}
async function previewVersion(v: ChapterVersion) {
  historyError.value = "";
  try {
    version.value = await api<ChapterVersion>(
      chapterPath() + "/versions/" + v.revision,
    );
  } catch (e) {
    historyError.value = (e as Error).message;
  }
}
async function restoreVersion() {
  if (!version.value || !chapter.value || blocked.value) return;
  if (dirty.value) {
    historyError.value = "请先保存当前草稿，再恢复历史版本。";
    return;
  }
  actionBusy.value = true;
  historyError.value = "";
  try {
    const c = await api<Chapter>(
      chapterPath() + "/versions/" + version.value.revision,
      { method: "POST", body: jsonBody({ revision: chapter.value.revision }) },
    );
    chapter.value = c;
    title.value = c.title;
    body.value = c.body || "";
    saveError.value = "";
    conflict.value = undefined;
    clearTimeout(timer);
    historyDialog.value?.close();
    emit("reload");
  } catch (e) {
    historyError.value = (e as Error).message;
  } finally {
    actionBusy.value = false;
  }
}
function downloadDraft() {
  const url = URL.createObjectURL(
    new Blob([title.value + "\n\n" + body.value], {
      type: "text/plain;charset=utf-8",
    }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = (title.value || "章节") + "-未保存草稿.txt";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
async function useServer() {
  if (
    !conflict.value ||
    !(await askDiscard(
      "使用服务器版本替换当前草稿？需要保留的文字请先复制或下载。",
    ))
  )
    return;
  chapter.value = conflict.value;
  title.value = conflict.value.title;
  body.value = conflict.value.body || "";
  conflict.value = undefined;
  saveError.value = "";
  clearTimeout(timer);
}
async function login() {
  actionBusy.value = true;
  loginError.value = "";
  try {
    const state = await api("/api/auth/login", {
      method: "POST",
      body: jsonBody({ password: password.value }),
    });
    csrf.value = state.csrf;
    sessionExpired.value = false;
    password.value = "";
    loginDialog.value?.close();
    saveError.value = "";
    await save();
  } catch (e) {
    loginError.value = (e as Error).message;
  } finally {
    actionBusy.value = false;
  }
}
function resizeFont(delta: number) {
  fontSize.value = Math.max(14, Math.min(28, fontSize.value + delta));
  try {
    localStorage.setItem("srics-novel-font", String(fontSize.value));
  } catch {}
}
function keydown(e: KeyboardEvent) {
  if (
    editing.value &&
    (e.metaKey || e.ctrlKey) &&
    e.key.toLowerCase() === "s"
  ) {
    e.preventDefault();
    void save();
  }
}
function compositionStart() {
  composing = true;
}
function compositionEnd() {
  composing = false;
  schedule();
}
onMounted(() => {
  unguard = setNavigationGuard(leave);
  window.addEventListener("beforeunload", beforeUnload);
  window.addEventListener("keydown", keydown);
  try {
    const size = Number(localStorage.getItem("srics-novel-font"));
    if (size >= 14 && size <= 28) fontSize.value = size;
  } catch {}
  const initial =
    props.novel.chapters.find((c) => c.id === props.novel.reading) ||
    props.novel.chapters[0];
  if (initial) void choose(initial.id, false, false);
});
onUnmounted(() => {
  stopped = true;
  generation++;
  clearTimeout(timer);
  resolveDiscard?.(false);
  unguard();
  protectUnsavedSession.value = false;
  window.removeEventListener("beforeunload", beforeUnload);
  window.removeEventListener("keydown", keydown);
});
</script>
<template>
  <ActionConfirm ref="confirmation" />
  <section class="page-heading reader-heading novel-heading">
    <div>
      <a href="#/library/novels" class="back-link"
        ><Icon name="chevron-left" />返回小说</a
      >
      <h1>{{ novel.item.name }}</h1>
      <div class="content-tags">
        <span v-for="t in novel.item.tags" :key="t">{{ t }}</span>
      </div>
    </div>
    <div class="row-actions">
      <button
        class="button small secondary"
        :disabled="blocked"
        @click="editNovel"
      >
        <Icon name="edit" />编辑信息</button
      ><a
        :href="'/api/items/' + novel.item.id + '/download'"
        class="button small secondary"
        ><Icon name="download" />导出 TXT</a
      ><button
        class="icon-button"
        aria-label="将小说移到回收站"
        :disabled="blocked"
        @click="trashNovel"
      >
        <Icon name="trash" />
      </button>
    </div>
  </section>
  <p v-if="error" class="notice warning" role="alert">{{ error }}</p>
  <div class="novel-layout">
    <aside class="chapter-panel panel">
      <div class="chapter-heading">
        <h2>章节</h2>
        <button
          class="button small secondary"
          :disabled="blocked"
          @click="beginChapter"
        >
          新增章节
        </button>
      </div>
      <div class="segmented chapter-tabs">
        <button :aria-pressed="!deleted" @click="deleted = false">
          目录 {{ novel.chapters.length }}</button
        ><button :aria-pressed="deleted" @click="deleted = true">
          已删除 {{ novel.trash.length }}
        </button>
      </div>
      <ol class="chapter-list" aria-label="章节目录">
        <li
          v-for="(c, i) in deleted ? novel.trash : novel.chapters"
          :key="c.id"
          :class="{ selected: !deleted && chapter?.id === c.id }"
        >
          <template v-if="!deleted"
            ><button
              class="chapter-title"
              :aria-current="chapter?.id === c.id ? 'true' : undefined"
              :disabled="blocked"
              @click="choose(c.id)"
            >
              <span>{{ i + 1 }}</span
              >{{ c.title }}
            </button>
            <div v-if="editing" class="chapter-order">
              <button
                :disabled="blocked || i === 0"
                :aria-label="'上移 ' + c.title"
                @click="move(c, -1)"
              >
                ↑</button
              ><button
                :disabled="blocked || i === novel.chapters.length - 1"
                :aria-label="'下移 ' + c.title"
                @click="move(c, 1)"
              >
                ↓
              </button>
            </div></template
          >
          <template v-else
            ><span class="deleted-chapter">{{ c.title }}</span
            ><button
              class="button small secondary"
              :disabled="blocked"
              @click="trashChapter(c, true)"
            >
              恢复</button
            ><button
              class="button small secondary"
              :disabled="blocked"
              @click="purgeChapter(c)"
            >
              彻底删除
            </button></template
          >
        </li>
      </ol>
      <p
        v-if="!(deleted ? novel.trash : novel.chapters).length"
        class="chapter-empty"
      >
        {{ deleted ? "没有已删除章节" : "暂无章节" }}
      </p>
    </aside>
    <section
      ref="reader"
      class="novel-content panel"
      aria-label="章节内容"
      :aria-busy="loading"
    >
      <template v-if="chapter">
        <div class="novel-toolbar">
          <div class="segmented">
            <button
              :aria-pressed="!editing"
              :disabled="blocked"
              @click="mode(false)"
            >
              阅读</button
            ><button
              :aria-pressed="editing"
              :disabled="blocked"
              @click="mode(true)"
            >
              编辑
            </button>
          </div>
          <template v-if="editing"
            ><span class="save-status" role="status">{{ saveStatus }}</span
            ><button
              class="button small primary"
              :disabled="blocked || !dirty"
              @click="save()"
            >
              保存
            </button></template
          >
          <div v-else class="font-controls">
            <button
              class="icon-button"
              aria-label="缩小字号"
              :disabled="fontSize <= 14"
              @click="resizeFont(-2)"
            >
              A−</button
            ><span>{{ fontSize }}</span
            ><button
              class="icon-button"
              aria-label="增大字号"
              :disabled="fontSize >= 28"
              @click="resizeFont(2)"
            >
              A+
            </button>
          </div>
        </div>
        <template v-if="editing">
          <p v-if="saveError" class="notice warning" role="alert">
            {{ saveError }}
          </p>
          <div v-if="sessionExpired" class="notice warning">
            <span>登录已过期，重新登录后可继续保存。</span
            ><button
              class="button small secondary"
              @click="loginDialog?.showModal()"
            >
              重新登录
            </button>
          </div>
          <div class="chapter-editor form-stack">
            <label
              >章节标题<input
                v-model="title"
                maxlength="160"
                :disabled="actionBusy || loading"
                @compositionstart="compositionStart"
                @compositionend="compositionEnd" /></label
            ><label
              >正文<textarea
                v-model="body"
                aria-label="章节正文"
                spellcheck="false"
                :disabled="actionBusy || loading"
                @compositionstart="compositionStart"
                @compositionend="compositionEnd"
              ></textarea>
            </label>
          </div>
          <div class="editor-footer">
            <span>{{ Array.from(body).length.toLocaleString() }} 字</span
            ><button
              class="button small secondary"
              :disabled="blocked"
              @click="history"
            >
              <Icon name="clock" />历史版本</button
            ><button
              v-if="dirty || saveError"
              class="button small secondary"
              @click="downloadDraft"
            >
              下载草稿</button
            ><button
              class="icon-button"
              aria-label="删除当前章节"
              :disabled="blocked"
              @click="trashChapter(chapter)"
            >
              <Icon name="trash" />
            </button>
          </div>
          <section v-if="conflict" class="conflict-panel">
            <h3>服务器版本 · {{ conflict.title }}</h3>
            <pre>{{ conflict.body }}</pre>
            <button class="button small secondary" @click="useServer">
              使用服务器版本
            </button>
          </section>
        </template>
        <article
          v-else
          class="novel-prose"
          :style="{ fontSize: fontSize + 'px' }"
        >
          <h2>{{ chapter.title }}</h2>
          <div>{{ chapter.body || "（本章暂无正文）" }}</div>
        </article>
        <nav class="chapter-navigation" aria-label="章节翻页">
          <button
            class="button small secondary"
            :disabled="blocked || index <= 0"
            @click="choose(novel.chapters[index - 1]!.id)"
          >
            <Icon name="chevron-left" />上一章</button
          ><span>{{ index + 1 }} / {{ novel.chapters.length }}</span
          ><button
            class="button small secondary"
            :disabled="
              blocked || index < 0 || index >= novel.chapters.length - 1
            "
            @click="choose(novel.chapters[index + 1]!.id)"
          >
            下一章<Icon name="chevron-right" />
          </button>
        </nav>
      </template>
      <EmptyState v-else compact icon="text"
        :title="loading ? '正在加载…' : novel.chapters.length ? '选择章节开始阅读' : '暂无章节'"
        :description="!loading && !novel.chapters.length ? '添加章节后，即可编辑正文。' : undefined">
        <button v-if="!loading && !novel.chapters.length" class="button secondary small" @click="beginChapter">新增章节</button>
      </EmptyState>
    </section>
  </div>
  <dialog
    ref="discardDialog"
    class="app-dialog"
    @cancel.prevent="answerDiscard(false)"
  >
    <div class="dialog-heading"><h2>未保存的修改</h2></div>
    <p class="discard-message">{{ discardMessage }}</p>
    <div class="dialog-actions">
      <button class="button secondary" autofocus @click="answerDiscard(false)">
        继续编辑</button
      ><button class="button primary" @click="answerDiscard(true)">
        放弃修改
      </button>
    </div>
  </dialog>
  <dialog
    ref="newDialog"
    class="app-dialog"
    @cancel="actionBusy && $event.preventDefault()"
  >
    <form class="form-stack" @submit.prevent="createChapter">
      <div class="dialog-heading">
        <h2>新增章节</h2>
        <button
          type="button"
          class="icon-button"
          aria-label="关闭"
          :disabled="actionBusy"
          @click="newDialog?.close()"
        >
          <Icon name="close" />
        </button>
      </div>
      <label
        >章节标题<input v-model="newTitle" required maxlength="160" autofocus
      /></label>
      <p v-if="newError" class="inline-error" role="alert">{{ newError }}</p>
      <button class="button primary" :disabled="actionBusy">
        {{ actionBusy ? "保存中…" : "创建并编辑" }}
      </button>
    </form>
  </dialog>
  <dialog
    ref="historyDialog"
    class="app-dialog history-dialog"
    @cancel="actionBusy && $event.preventDefault()"
  >
    <div class="dialog-heading">
      <h2>历史版本</h2>
      <button
        class="icon-button"
        aria-label="关闭历史版本"
        :disabled="actionBusy"
        @click="historyDialog?.close()"
      >
        <Icon name="close" />
      </button>
    </div>
    <p class="subtle-copy">保留最近 20 个版本，连续自动保存合并记录。</p>
    <p v-if="historyError" class="inline-error" role="alert">
      {{ historyError }}
    </p>
    <div class="version-list">
      <button
        v-for="v in versions"
        :key="v.revision"
        class="version-row"
        :aria-pressed="version?.revision === v.revision"
        :disabled="actionBusy"
        @click="previewVersion(v)"
      >
        <strong>{{ v.title }}</strong
        ><span
          >版本 {{ v.revision }} ·
          {{ new Date(v.saved).toLocaleString("zh-CN") }}</span
        >
      </button>
      <p v-if="!versions.length" class="subtle-copy">暂无历史版本</p>
    </div>
    <template v-if="version">
      <pre class="version-preview">{{ version.body || "（空正文）" }}</pre>
      <button
        class="button primary"
        :disabled="blocked || dirty"
        @click="restoreVersion"
      >
        恢复此版本
      </button></template
    >
  </dialog>
  <dialog
    ref="loginDialog"
    class="app-dialog"
    @cancel="actionBusy && $event.preventDefault()"
  >
    <form class="form-stack" @submit.prevent="login">
      <div class="dialog-heading">
        <h2>重新登录</h2>
        <button
          type="button"
          class="icon-button"
          aria-label="关闭登录"
          :disabled="actionBusy"
          @click="loginDialog?.close()"
        >
          <Icon name="close" />
        </button>
      </div>
      <label
        >登录密码<input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
      /></label>
      <p v-if="loginError" class="inline-error" role="alert">
        {{ loginError }}
      </p>
      <button class="button primary" :disabled="actionBusy">
        登录并重试保存
      </button>
    </form>
  </dialog>
</template>
