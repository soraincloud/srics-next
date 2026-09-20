<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import Icon from "./Icon.vue";
import LibraryView from "./LibraryView.vue";
import PrivateView from "./PrivateView.vue";
import { installVaultLifecycle, vaultOpen, lockVault } from "./vault";
import NovelView from "./NovelView.vue";
import { canNavigate } from "./navigation";
import LibraryTools from "./LibraryTools.vue";
import { api, authenticated, fileSize } from "./api";
import { clearUploadMemory } from "./uploads";
import { modules, stages } from "./catalog";
import { useVerification } from "./useVerification";
import { useTheme } from "./useTheme";

const { report, connection, error, starting, syncing, refreshing, busy, refresh, start } = useVerification();
const { theme, toggleTheme } = useTheme();
const route = ref(window.location.hash || "#/");
const main = ref<HTMLElement>();
const expanded = ref(new Set<string>());
const activeModule = computed(() => modules.find((item) => route.value === "#/library/" + item.id || route.value.startsWith("#/library/" + item.id + "/")));
const itemId = computed(() => route.value.split("/")[3]);
const available = ["comics", "images", "photos", "novels", "private", "files"];
const stats = ref<{counts:Record<string,number>;size:number}>({counts:{},size:0});
async function loadStats(){ try { stats.value = await api("/api/library/stats"); } catch {} }
async function logout(){ if (!(await canNavigate())) return; try { await api("/api/auth/logout",{method:"POST"}); clearUploadMemory(); authenticated.value=false; } catch { window.alert("退出失败，请检查连接后重试。"); } }
const page = computed(() => activeModule.value ? "module"
  : route.value === "#/verify" ? "verify" : route.value === "#/about" ? "about"
  : route.value === "#/trash" ? "trash" : route.value === "#/backup" ? "backup" : route.value === "#/" ? "library" : "missing");
const title = computed(() => activeModule.value?.name
  || ({ library: "资料库", trash: "回收站", backup: "备份中心", verify: "恢复验证", about: "关于此版本", missing: "页面不存在" } as Record<string, string>)[page.value]);
const inLibrary = computed(() => page.value === "library" || page.value === "module");
const passed = computed(() => report.value?.checks.filter((item) => item.status === "passed").length || 0);
const total = computed(() => report.value?.checks.length || 11);
const currentCheck = computed(() => report.value?.checks.find((item) => item.status === "running"));
const failedCheck = computed(() => report.value?.checks.find((item) => item.status === "failed"));
const complete = computed(() => !busy.value && ["passed", "failed"].includes(report.value?.status || ""));
const status = computed(() => connection.value === "offline" ? "offline"
  : starting.value || syncing.value ? "running" : report.value?.status || "idle");
const statusTitle = computed(() => ({
  idle: "尚未运行验证",
  running: starting.value ? "正在启动验证" : syncing.value ? "正在同步验证进度" : "正在验证恢复能力",
  passed: "恢复验证通过",
  failed: "有一项验证需要处理",
  offline: "与本机服务的连接已中断",
})[status.value]);
const statusDescription = computed(() => {
  if (status.value === "offline") return "连接恢复后会自动同步结果，你也可以手动重试。";
  if (status.value === "running") return currentCheck.value
    ? "正在检查：" + currentCheck.value.label : "可以离开这个页面，验证会继续在本机运行。";
  if (status.value === "passed") return "本轮样本已完成加密备份，并在移除源数据后成功恢复。";
  if (status.value === "failed") return failedCheck.value?.label || "展开下方结果查看原因，处理后可以重新验证。";
  return "用临时样本检查转换、加密、备份和恢复的完整流程。";
});
const actionLabel = computed(() => starting.value ? "正在启动…" : busy.value ? "验证进行中…"
  : report.value?.status === "failed" ? "重新验证" : report.value?.status === "passed" ? "再次验证" : "开始验证");
const finished = computed(() => report.value?.finishedAt
  ? new Date(report.value.finishedAt).toLocaleString("zh-CN", {
      month: "long", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false,
    }) : "");
const duration = computed(() => {
  if (!report.value?.finishedAt) return "";
  return ((new Date(report.value.finishedAt).getTime() - new Date(report.value.startedAt).getTime()) / 1000).toFixed(1) + " 秒";
});
const checkLabels: Record<string, string> = { pending: "等待", running: "进行中", passed: "通过", failed: "失败", blocked: "未执行" };
const checkHints: Record<string, string> = {
  tools: "确认图片编码器与备份工具可以运行",
  pixels: "核对转换前后的像素与照片元数据",
  formats: "保留已有 WebP，识别不支持的格式",
  encryption: "检查文件、名称与密钥的加密保存",
  tamper: "确认错误口令和损坏数据被拒绝",
  storage: "核对文件与索引的写入和取回",
  privacy: "检查测试目录中的敏感明文",
  snapshot: "为同一恢复点保存一致的索引",
  backup: "在保险库锁定时执行加密备份",
  check: "读取并校验全部测试备份数据",
  restore: "移除本轮源数据，再从备份中取回",
};

let stopVault: (()=>void)|undefined;
let changingRoute = false;
async function routeChanged() {
  if (changingRoute) return;
  const destination = window.location.hash || "#/";
  changingRoute = true;
  const allowed = await canNavigate();
  changingRoute = false;
  if (!allowed) {
    history.replaceState(null, "", route.value);
    return;
  }
  history.replaceState(null, "", destination);
  route.value = destination;
  await nextTick();
  window.scrollTo({ top: 0, behavior: "instant" });
  main.value?.focus({ preventScroll: true });
}
function skipToContent() { main.value?.focus(); }
function toggleCheck(id: string) {
  const next = new Set(expanded.value);
  next.has(id) ? next.delete(id) : next.add(id);
  expanded.value = next;
}
function downloadReport() {
  if (!complete.value || !report.value) return;
  const url = URL.createObjectURL(new Blob([JSON.stringify(report.value, null, 2)], { type: "application/json" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "srics-verification-" + (report.value.finishedAt || "report").replace(/[:.]/g, "-") + ".json";
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
watch(title, (value) => { document.title = value + " · SRICS Next"; }, { immediate: true });
watch(() => report.value?.startedAt, () => { expanded.value = new Set(); });
watch(() => failedCheck.value?.id, (id) => { if (id) expanded.value = new Set([...expanded.value, id]); });
onMounted(() => { stopVault=installVaultLifecycle(); window.addEventListener("hashchange", routeChanged); window.addEventListener("library-changed", loadStats); void loadStats(); });
onUnmounted(() => { stopVault?.(); clearUploadMemory(); window.removeEventListener("hashchange", routeChanged); window.removeEventListener("library-changed", loadStats); });
</script>

<template>
  <button class="skip-link" @click="skipToContent">跳到主要内容</button>
  <div class="app-shell">
    <header class="app-header">
      <a class="brand" href="#/" aria-label="SRICS Next 资料库">
        <span class="brand-mark"><Icon name="library" /></span>
        <span>SRICS<span class="brand-next">Next</span></span>
      </a>
      <span class="header-divider"></span><span class="header-label">个人资料库</span>
      <div class="header-actions">
        <span class="connection" :class="connection"><span class="connection-dot"></span>{{ connection === "online" ? "已连接" : connection === "offline" ? "连接已中断" : "正在连接" }}</span>
        <button v-if="vaultOpen" class="icon-button" aria-label="锁定保险库" title="锁定保险库" @click="lockVault().catch(()=>{})"><Icon name="lock" /></button>
        <button class="icon-button logout-button" aria-label="退出登录" title="退出登录" @click="logout"><Icon name="logout" /></button>
        <button class="theme-toggle" :aria-label="theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'" :title="theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'" @click="toggleTheme"><Icon :name="theme === 'dark' ? 'sun' : 'moon'" /></button>
      </div>
    </header>
    <aside class="sidebar">
      <div class="workspace-identity"><span class="workspace-avatar">S</span><div><strong>我的空间</strong><span>本机资料库 <span class="version-label">DEV</span></span></div></div>
      <nav class="sidebar-nav" aria-label="主导航">
        <a href="#/" class="nav-item" :aria-current="page === 'library' ? 'page' : undefined">
          <Icon name="library" /><span>资料库</span>
        </a>
        <p class="nav-label">资料分类</p>
        <a v-for="item in modules.slice(0, 4)" :key="item.id" :href="'#/library/' + item.id"
          class="nav-item" :aria-current="activeModule?.id === item.id ? 'page' : undefined">
          <Icon :name="item.icon" /><span>{{ item.name }}</span>
        </a>
        <p class="nav-label">私密空间</p>
        <a v-for="item in modules.slice(4)" :key="item.id" :href="'#/library/' + item.id"
          class="nav-item" :aria-current="activeModule?.id === item.id ? 'page' : undefined">
          <Icon :name="item.icon" /><span>{{ item.name }}</span>
        </a>
      </nav>
      <div class="sidebar-bottom">
        <a href="#/backup" class="nav-item" :aria-current="page === 'backup' ? 'page' : undefined"><Icon name="shield" /><span>备份中心</span></a>
        <a href="#/trash" class="nav-item" :aria-current="page === 'trash' ? 'page' : undefined"><Icon name="trash" /><span>回收站</span></a>
        <a href="#/verify" class="nav-item" :aria-current="page === 'verify' ? 'page' : undefined">
          <Icon name="shield" /><span>样本验证</span>
          <span v-if="busy" class="nav-activity" aria-label="验证进行中"></span>
        </a>
        <a href="#/about" class="nav-item" :aria-current="page === 'about' ? 'page' : undefined">
          <Icon name="info" /><span>关于此版本</span><span class="version-label">DEV</span>
        </a>
        <div class="local-note"><Icon name="computer" /><span>本机开发版</span></div>
      </div>
    </aside>

    <div class="workspace">
      <header class="toolbar">
        <div class="breadcrumb">
          <a v-if="activeModule" href="#/" class="back-link"><Icon name="chevron-left" />资料库</a>
          <span v-if="activeModule" class="breadcrumb-divider">/</span>
          <span>{{ title }}</span>
        </div>
      </header>

      <main ref="main" class="main-content" tabindex="-1" :aria-label="title">
        <div v-if="connection === 'offline'" class="notice warning" role="alert">
          <Icon name="info" />
          <div><strong>暂时无法连接服务</strong><p>请确认 SRICS 仍在运行。恢复连接后，进度会自动更新。</p></div>
          <button class="button small secondary" :disabled="refreshing" @click="refresh">
            {{ refreshing ? "连接中…" : "重新连接" }}
          </button>
        </div>

        <template v-if="page === 'library'">
          <section class="page-heading">
            <div><h1>我的资料库</h1></div>
            <a href="#/backup" class="button primary"><Icon name="shield" />备份中心<Icon name="arrow" /></a>
          </section>

          <section class="library-overview panel"><div class="library-stat"><strong>{{ Object.values(stats.counts).reduce((n,v)=>n+v,0) }}</strong><span>项普通资料</span></div><div class="library-stat"><strong>{{ fileSize(stats.size) }}</strong><span>原件大小</span></div></section>

          <div class="section-heading library-section-heading"><h2>资料空间</h2><span class="quiet-badge">6 个独立空间</span></div>
          <div class="library-grid">
            <a v-for="item in modules" :key="item.id" :href="'#/library/' + item.id"
              class="library-card" :aria-label="item.name + (available.includes(item.id) ? '，打开资料空间' : '，查看功能计划')">
              <div class="card-top">
                <span class="module-icon"><Icon :name="item.icon" /></span>
                <span class="coming-soon" :class="{available:available.includes(item.id)}">{{ ['private','files'].includes(item.id) ? '保险库' : (stats.counts[item.id] || 0) + ' 项' }}</span>
              </div>
              <h2>{{ item.name }}</h2>
              <p class="card-description">{{ item.sub }}</p>
              <div class="card-meta"><span>{{ item.kind }}</span><span class="card-arrow"><Icon name="arrow" /></span></div>
            </a>
          </div>
          <p class="library-note"><Icon name="info" />私密照片与个人文件需要独立解锁。<a href="#/about">查看开发计划<Icon name="chevron-right" /></a></p>
          <p class="page-footnote"><Icon name="computer" />仅在本机运行 · 文件保存在你的设备上</p>
        </template>

        <template v-else-if="page === 'verify'">
          <section class="page-heading">
            <div><h1>恢复验证</h1><p class="subtitle">检查从文件保存到备份取回的完整流程。</p></div>
          </section>
          <section class="verification-summary panel" :class="status" aria-labelledby="verification-heading">
            <div class="summary-top">
              <div class="verification-emblem" :class="status">
                <span v-if="status === 'running'" class="spinner"></span>
                <Icon v-else :name="status === 'passed' ? 'check' : status === 'failed' || status === 'offline' ? 'info' : 'shield'" />
              </div>
              <div class="summary-copy" aria-live="polite" aria-atomic="true">
                <h2 id="verification-heading">{{ statusTitle }}</h2><p>{{ statusDescription }}</p>
              </div>
            </div>
            <div v-if="busy || complete" class="verification-progress">
              <div class="progress-label"><span>{{ passed }} / {{ total }} 项通过</span><span>{{ busy ? "请保持本机服务运行" : "用时 " + duration }}</span></div>
              <progress :value="passed" :max="total" aria-label="验证通过的项目数"></progress>
            </div>
            <div class="summary-actions">
              <button class="button primary" :disabled="busy || connection !== 'online'" @click="start">
                <Icon v-if="!busy" :name="complete ? 'refresh' : 'play'" />{{ actionLabel }}
              </button>
              <button v-if="complete" class="button secondary" @click="downloadReport"><Icon name="download" />下载验证报告</button>
              <span v-if="finished && !busy" class="last-run">完成于 {{ finished }}</span>
              <span v-else class="last-run">仅使用临时样本，不会读取你的文件</span>
            </div>
            <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
          </section>

          <section v-if="report?.checks.length" class="check-section" aria-labelledby="checks-heading">
            <div class="section-heading"><h2 id="checks-heading">验证项目</h2><span>点击项目查看详情</span></div>
            <div class="check-list panel">
              <div v-for="(check, index) in report.checks" :key="check.id" class="check-item" :class="check.status">
                <button class="check-toggle" :aria-expanded="expanded.has(check.id)" :aria-controls="'check-' + check.id" @click="toggleCheck(check.id)">
                  <span class="check-indicator">
                    <Icon v-if="check.status === 'passed'" name="check" />
                    <Icon v-else-if="check.status === 'failed'" name="close" />
                    <span v-else-if="check.status === 'running'" class="spinner"></span>
                    <span v-else>{{ String(index + 1).padStart(2, "0") }}</span>
                  </span>
                  <span class="check-copy"><strong>{{ check.label }}</strong><span>{{ checkHints[check.id] }}</span></span>
                  <span class="check-status">{{ checkLabels[check.status] }}</span>
                  <Icon class="disclosure" name="chevron-right" />
                </button>
                <div v-if="expanded.has(check.id)" :id="'check-' + check.id" class="check-detail">
                  <p>{{ check.detail || (check.status === "pending" ? "开始后将按顺序执行。" : check.status === "running" ? "正在执行，结果会自动更新。" : check.status === "blocked" ? "前面的项目未通过，本项没有执行。" : "本项未返回详细信息。") }}</p>
                  <span v-if="['passed', 'failed'].includes(check.status)">耗时 {{ check.durationMs }} ms</span>
                </div>
              </div>
            </div>
          </section>
          <p class="page-footnote">这是合成样本的验证结果；实际资料备份状态请查看备份中心。</p>
          <details v-if="report && Object.keys(report.versions || {}).length" class="environment panel">
            <summary>运行环境<span>技术信息</span><Icon name="chevron-right" /></summary>
            <dl><template v-for="(value, key) in report.versions" :key="key"><dt>{{ key }}</dt><dd>{{ value }}</dd></template></dl>
          </details>
        </template>

        <PrivateView v-else-if="activeModule && ['private','files'].includes(activeModule.id)" :key="activeModule.id" :module="activeModule.id" />
        <NovelView v-else-if="activeModule?.id === 'novels'" :key="itemId || 'novel-list'" :item-id="itemId" />
        <LibraryView v-else-if="activeModule && available.includes(activeModule.id)" :module="activeModule.id" :item-id="itemId" :name="activeModule.name" :sub="activeModule.sub" />
        <LibraryTools v-else-if="page === 'trash' || page === 'backup'" :page="page" />

        <template v-else-if="activeModule">
          <section class="page-heading module-heading">
            <span class="module-icon large"><Icon :name="activeModule.icon" /></span>
            <div><p class="eyebrow">我的资料库</p><h1>{{ activeModule.name }}</h1><p class="subtitle">{{ activeModule.sub }}</p></div>
          </section>
          <section class="module-empty panel">
            <span class="empty-icon"><Icon name="clock" /></span>
            <h2>功能开发中</h2>
            <p>{{ activeModule.name }}暂未开放。</p>
            <a href="#/about" class="button primary">查看开发计划<Icon name="chevron-right" /></a>
            <a href="#/" class="text-link">返回资料库</a>
          </section>
          <section class="planned-features" aria-labelledby="features-heading">
            <div class="section-heading"><h2 id="features-heading">将会支持</h2><span>已确定的功能范围</span></div>
            <ul class="feature-grid"><li v-for="(feature, index) in activeModule.features" :key="feature"><span>{{ String(index + 1).padStart(2, "0") }}</span>{{ feature }}</li></ul>
          </section>
        </template>

        <template v-else-if="page === 'about'">
          <section class="page-heading"><div><h1>关于此版本</h1></div><span class="quiet-badge">DEV</span></section>
          <section class="about-intro panel">
            <span class="brand-mark"><Icon name="library" /></span>
            <div><h2>SRICS Next</h2><p>当前可以导入漫画、浏览图片、保存照片原件，以及从回收站恢复内容。登录、可重试上传和本地加密备份已接入；小说编辑与阅读、私密照片和个人文件也已接入。</p></div>
          </section>
          <section aria-labelledby="roadmap-heading">
            <div class="section-heading"><h2 id="roadmap-heading">开发计划</h2><span>当前阶段 M5</span></div>
            <ol class="roadmap panel">
              <li v-for="stage in stages" :key="stage.id" :class="{ current: stage.id === 'M5' }">
                <span class="stage-number">{{ stage.id }}</span><div><h3>{{ stage.name }}</h3><p>{{ stage.detail }}</p></div><span class="stage-state">{{ stage.state }}</span>
              </li>
            </ol>
          </section>
          <div class="about-notes">
            <div><Icon name="computer" /><h3>运行方式</h3><p>通过本机程序配置和启停服务，选择局域网 HTTPS、本地与 S3 云端加密备份。</p></div>
            <div><Icon name="shield" /><h3>恢复验证</h3><p>验证报告可下载留存。服务重启后，最近一次结果会清空。</p></div>
          </div>
          <a href="#/verify" class="text-link">打开恢复验证<Icon name="arrow" /></a>
        </template>

        <section v-else class="module-empty panel">
          <span class="empty-icon"><Icon name="folder" /></span><h1>找不到这个页面</h1>
          <p>链接可能已失效，请从资料库重新进入。</p><a href="#/" class="button primary">返回资料库</a>
        </section>
      </main>
    </div>

    <nav class="mobile-tabs" aria-label="底部导航">
      <a href="#/" :aria-current="inLibrary ? 'page' : undefined"><Icon name="library" /><span>资料库</span></a>
      <a href="#/backup" :aria-current="page === 'backup' ? 'page' : undefined"><Icon name="shield" /><span>备份</span></a>
      <a href="#/trash" :aria-current="page === 'trash' ? 'page' : undefined"><Icon name="trash" /><span>回收站</span></a>
    </nav>
  </div>
</template>
