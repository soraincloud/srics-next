<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import PageActions from "./PageActions.vue";
import EmptyState from "./EmptyState.vue";
import ActionConfirm from "./ActionConfirm.vue";
import SegmentedControl from "./SegmentedControl.vue";
import { api, APIError, changed, csrf, jsonBody, protectUnsavedSession, sessionExpired, type Item } from "./api";
import { newID, parseTags } from "./novels";
import { setNavigationGuard } from "./navigation";
import { renderMarkdown } from "./markdown";

const props = defineProps<{ id: string }>();
type Document = { item: Item; body: string };
const saved = ref<Document>(), name = ref(""), tagText = ref(""), body = ref("");
const loading = ref(props.id !== "new"), busy = ref(false), editing = ref(props.id === "new"), mode = ref("edit");
const error = ref(""), nameError = ref(""), tagError = ref(""), bodyError = ref(""), conflict = ref(false);
const confirmation = ref<InstanceType<typeof ActionConfirm>>(), markdown = ref<HTMLElement>(), nameInput = ref<HTMLInputElement>();
const login = ref<HTMLDialogElement>(), password = ref(""), passwordVisible = ref(false), loginError = ref("");
const draftId = newID();
const dirty = computed(() => name.value !== (saved.value?.item.name || "") || body.value !== (saved.value?.body || "") || JSON.stringify(parseTags(tagText.value)) !== JSON.stringify(saved.value?.item.tags || []));
const html = computed(() => renderMarkdown(body.value));
const bodyBytes = computed(() => new TextEncoder().encode(body.value).length);
const saveStatus = computed(() => busy.value ? "保存中…" : error.value ? "保存失败，草稿仍在当前页面" : dirty.value ? "尚未保存" : saved.value ? "已保存" : "新文档");
const composing = ref(false);
let stopped = false, unguard = () => {}, leaving: Promise<boolean> | undefined;
watch([dirty, busy], () => { protectUnsavedSession.value = dirty.value || busy.value; }, { flush: "sync" });

function accept(document: Document) {
  saved.value = document; name.value = document.item.name; tagText.value = document.item.tags.join("，"); body.value = document.body;
  error.value = ""; conflict.value = false;
}
async function load() {
  loading.value = true; error.value = "";
  try { const document = await api<Document>("/api/documents/" + props.id); if (!stopped) accept(document); }
  catch (e) { if (!stopped) error.value = (e as Error).message; }
  finally { if (!stopped) loading.value = false; }
}
async function beginEdit() {
  editing.value = true; mode.value = "edit";
  await nextTick();
  window.scrollTo({ top: 0, behavior: "instant" });
  nameInput.value?.focus({ preventScroll: true });
}
function validate() {
  nameError.value = !name.value.trim() || [...name.value.trim()].length > 160 || /[\0\r\n]/.test(name.value) ? "名称需为 1–160 个字符，不能包含换行" : "";
  const tags = parseTags(tagText.value);
  tagError.value = tags.length > 20 || tags.some(tag => [...tag].length > 32 || /[\0\r\n]/.test(tag)) ? "最多 20 个标签，每个不超过 32 字" : "";
  bodyError.value = bodyBytes.value > 512 * 1024 || body.value.includes("\0") ? "正文最多 512 KiB，不能含空字符" : "";
  if (nameError.value) nameInput.value?.focus();
  return !nameError.value && !tagError.value && !bodyError.value;
}
async function save(read = false) {
  if (busy.value || composing.value || !validate()) return false;
  if (saved.value && !dirty.value) { if (read) editing.value = false; return true; }
  busy.value = true; error.value = "";
  try {
    const id = saved.value?.item.id || draftId;
    const document = await api<Document>(saved.value ? "/api/documents/" + id : "/api/documents", {
      method: saved.value ? "PUT" : "POST",
      body: jsonBody({ id, name: name.value, tags: parseTags(tagText.value), body: body.value, revision: saved.value?.item.revision || 0 }),
    });
    if (stopped) return false;
    accept(document); changed();
    if (props.id === "new") window.location.hash = "#/library/documents/" + document.item.id;
    else if (read) editing.value = false;
    return true;
  } catch (e) {
    error.value = (e as Error).message;
    conflict.value = e instanceof APIError && e.status === 409;
    return false;
  } finally { busy.value = false; }
}
async function leave() {
  if (busy.value) return false;
  if (!dirty.value) return true;
  if (leaving) return leaving;
  leaving = (async () => {
    const discard = await confirmation.value?.ask("放弃未保存的修改？", "当前文档的修改尚未保存。你可以取消并继续编辑，或确认放弃。") || false;
    if (discard) { name.value = saved.value?.item.name || ""; tagText.value = saved.value?.item.tags.join("，") || ""; body.value = saved.value?.body || ""; }
    return discard;
  })();
  try { return await leaving; } finally { leaving = undefined; }
}
async function trash() {
  if (!saved.value || busy.value || !(await confirmation.value?.ask("移入回收站？", "这篇文档可以从回收站恢复。"))) return;
  busy.value = true; error.value = "";
  try { await api("/api/items/" + saved.value.item.id, { method: "DELETE" }); changed(); window.location.hash = "#/library/documents"; }
  catch (e) { error.value = (e as Error).message; }
  finally { busy.value = false; }
}
function downloadDraft() {
  const url = URL.createObjectURL(new Blob([body.value], { type: "text/markdown;charset=utf-8" }));
  const a = document.createElement("a"); a.href = url; a.download = (name.value.trim() || "未命名文档").replace(/[\\/:*?"<>|\r\n]/g, "_") + ".md"; a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
function anchorClick(event: MouseEvent) {
  const a = (event.target as Element).closest("a");
  const href = a?.getAttribute("href");
  if (href?.startsWith("#")) {
    event.preventDefault();
    try { markdown.value?.querySelector("#" + CSS.escape("md-" + decodeURIComponent(href.slice(1))))?.scrollIntoView({ behavior: "smooth", block: "start" }); } catch {}
  }
}
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value || busy.value) { event.preventDefault(); event.returnValue = ""; } }
function keydown(event: KeyboardEvent) {
  if (editing.value && (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") { event.preventDefault(); if (!event.isComposing) void save(); }
}
async function reauthenticate() {
  if (busy.value) return;
  busy.value = true; loginError.value = "";
  try {
    const state = await api("/api/auth/login", { method: "POST", body: jsonBody({ password: password.value }) });
    csrf.value = state.csrf; sessionExpired.value = false; password.value = ""; passwordVisible.value = false; login.value?.close(); error.value = "";
  } catch (e) { loginError.value = (e as Error).message; }
  finally { busy.value = false; }
}
onMounted(() => {
  unguard = setNavigationGuard(leave);
  window.addEventListener("beforeunload", beforeUnload); window.addEventListener("keydown", keydown);
  if (props.id !== "new") void load(); else nameInput.value?.focus();
});
onUnmounted(() => {
  stopped = true; unguard(); protectUnsavedSession.value = false;
  window.removeEventListener("beforeunload", beforeUnload); window.removeEventListener("keydown", keydown);
});
</script>

<template>
  <ActionConfirm ref="confirmation" />
  <section class="page-heading document-heading">
    <div><h1>{{ editing ? saved ? '编辑文档' : '新建文档' : saved?.item.name || '文档' }}</h1></div>
  </section>
  <PageActions v-if="!loading && (saved || editing)">
      <template v-if="editing"><span class="save-status" role="status">{{ saveStatus }}</span><button class="button secondary" :disabled="busy" @click="save(true)">保存并阅读</button><button class="button primary" :disabled="busy" @click="save()"><Icon name="check" />{{ busy ? '保存中…' : '保存' }}</button></template>
      <template v-else><a :href="'/api/items/' + saved?.item.id + '/download'" class="button secondary"><Icon name="download" />下载 MD</a><button class="button primary" @click="beginEdit"><Icon name="edit" />编辑</button><button class="icon-button" aria-label="删除文档" title="移入回收站" :disabled="busy" @click="trash"><Icon name="trash" /></button></template>
  </PageActions>
  <div v-if="error" class="notice warning" role="alert"><div><strong>{{ error }}</strong><p v-if="editing">草稿已保留。你可以下载草稿，再处理连接或版本问题。</p></div><button v-if="editing" class="button small secondary" @click="downloadDraft">下载草稿</button><button v-else class="button small secondary" :disabled="loading" @click="load">重新读取</button></div>
  <p v-if="conflict && saved" class="subtle-copy">另一处已修改这篇文档。<a :href="'#/library/documents/' + saved.item.id" target="_blank" rel="noopener" class="text-link">在新窗口查看已保存版本</a></p>
  <div v-if="sessionExpired && editing" class="notice warning"><span>登录已过期，请重新登录后保存；当前草稿仍保留。</span><button class="button small secondary" @click="login?.showModal()">重新登录</button></div>
  <p v-if="loading" class="subtle-copy" role="status">正在读取文档…</p>
  <template v-else-if="editing">
    <fieldset class="document-editor-fields" :disabled="busy">
      <div class="document-metadata panel form-stack">
        <div><label for="document-name">名称</label><input id="document-name" ref="nameInput" v-model="name" maxlength="160" placeholder="文档名称" :aria-invalid="!!nameError" :aria-describedby="nameError ? 'document-name-error' : undefined" @input="nameError = ''" /><p v-if="nameError" id="document-name-error" class="inline-error" role="alert">{{ nameError }}</p></div>
        <div><label for="document-tags">标签</label><input id="document-tags" v-model="tagText" placeholder="用逗号分隔，例如：笔记，工作" :aria-invalid="!!tagError" :aria-describedby="tagError ? 'document-tags-error' : undefined" @input="tagError = ''" /><p v-if="tagError" id="document-tags-error" class="inline-error" role="alert">{{ tagError }}</p></div>
      </div>
      <div class="document-edit-toolbar"><SegmentedControl aria-label="文档编辑模式"><button :aria-pressed="mode === 'edit'" @click="mode = 'edit'">编辑</button><button :aria-pressed="mode === 'preview'" @click="mode = 'preview'">预览</button></SegmentedControl><span>{{ bodyBytes.toLocaleString() }} / 524,288 字节</span></div>
      <textarea v-if="mode === 'edit'" id="document-body" v-model="body" class="document-source" aria-label="Markdown 正文" placeholder="使用 Markdown 编写正文…" :spellcheck="false" :aria-invalid="!!bodyError" :aria-describedby="bodyError ? 'document-body-error' : undefined" @input="bodyError = ''" @compositionstart="composing = true" @compositionend="composing = false" />
      <article v-else ref="markdown" class="markdown-content panel" aria-label="Markdown 预览" @click="anchorClick"><div v-if="body" v-html="html"></div><p v-else class="document-blank">暂无正文</p></article>
      <p v-if="bodyError" id="document-body-error" class="inline-error" role="alert">{{ bodyError }}</p>
    </fieldset>
    <div class="document-editor-footer"><span>Markdown · Command / Ctrl + S 保存</span><button class="text-link" @click="downloadDraft">下载草稿</button></div>
  </template>
  <template v-else-if="saved">
    <div v-if="saved.item.tags.length" class="content-tags document-reading-tags"><span v-for="tag in saved.item.tags" :key="tag">{{ tag }}</span></div>
    <article ref="markdown" class="markdown-content panel" aria-label="文档正文" @click="anchorClick"><div v-if="body" v-html="html"></div><p v-else class="document-blank">暂无正文</p></article>
  </template>
  <EmptyState v-else-if="!error" icon="text" title="找不到这篇文档"><a href="#/library/documents" class="button small secondary">返回文档列表</a></EmptyState>
  <dialog ref="login" class="upload-dialog" @close="password = ''; passwordVisible = false">
    <form class="form-stack" @submit.prevent="reauthenticate"><h2>重新登录</h2><label for="document-login">登录密码</label><div class="password-input"><input id="document-login" v-model="password" :type="passwordVisible ? 'text' : 'password'" autocomplete="current-password" :spellcheck="false" maxlength="72" required /><button type="button" class="password-toggle" :aria-label="passwordVisible ? '隐藏密码' : '显示密码'" :aria-pressed="passwordVisible" @click="passwordVisible = !passwordVisible"><Icon :name="passwordVisible ? 'eye-off' : 'eye'" /></button></div><p v-if="loginError" class="inline-error" role="alert">{{ loginError }}</p><div class="dialog-actions"><button type="button" class="button secondary" :disabled="busy" @click="login?.close()">取消</button><button class="button primary" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button></div></form>
  </dialog>
</template>
