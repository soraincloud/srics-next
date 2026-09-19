<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import Icon from "./Icon.vue";

type Check = {
  id: string;
  label: string;
  status: string;
  detail?: string;
  durationMs: number;
};
type Report = {
  status: string;
  startedAt: string;
  finishedAt?: string;
  versions: Record<string, string>;
  checks: Check[];
};
const view = ref("overview");
const selected = ref("");
const report = ref<Report | null>(null);
const error = ref("");
const connected = ref(false);
const starting = ref(false);
let timer: ReturnType<typeof setTimeout> | undefined;
let stopped = false;
let polling = false;
let pollController: AbortController | undefined;

const modules = [
  {
    id: "comics",
    name: "漫画",
    icon: "book",
    color: "peach",
    stage: "M2",
    sub: "一目录，一本收藏",
    features: [
      "文件夹上传与数字页序",
      "名称、标签与组合搜索",
      "第一页预览、连续阅读",
      "无损 WebP、整本 ZIP 下载",
    ],
  },
  {
    id: "novels",
    name: "小说",
    icon: "text",
    color: "sand",
    stage: "M3",
    sub: "阅读，也留住写下的文字",
    features: [
      "名称、标签与组合搜索",
      "章节新增、编辑、删除和排序",
      "自动保存、冲突检查和修订",
      "独立阅读模式、TXT 导出",
    ],
  },
  {
    id: "images",
    name: "图片",
    icon: "image",
    color: "sage",
    stage: "M2",
    sub: "随手收下，随机遇见",
    features: [
      "批量上传与原件下载",
      "列表与随机照片墙",
      "同轮不重复、独立随机池",
      "无需名称、标签或搜索",
    ],
  },
  {
    id: "photos",
    name: "个人照片",
    icon: "camera",
    color: "blue",
    stage: "M3",
    sub: "原样保存生活的片段",
    features: [
      "原始字节与元数据保留",
      "批量上传、简单列表",
      "上传与备份状态分开显示",
      "原件取回、回收站恢复",
    ],
  },
  {
    id: "private",
    name: "私密照片",
    icon: "lock",
    color: "lavender",
    stage: "M4",
    sub: "只在解锁之后可见",
    features: [
      "原件、名称和预览加密",
      "解锁后的列表与随机浏览",
      "各设备独立解锁、闲置锁定",
      "加密备份与原件取回",
    ],
  },
  {
    id: "files",
    name: "个人文件",
    icon: "folder",
    color: "rose",
    stage: "M4",
    sub: "重要资料，妥善安放",
    features: [
      "整个模块默认加密",
      "可设置名称、按名称搜索",
      "批量上传与原件下载",
      "删除、回收站与恢复",
    ],
  },
];
const stages = [
  {
    id: "M0",
    name: "工程与恢复验证",
    detail: "无损转换、加密存储、SQLite 快照与 restic 恢复",
    state: "本次交付",
  },
  {
    id: "M1",
    name: "公共基础",
    detail: "登录、上传任务、存储检查、回收站与备份状态",
    state: "下一阶段",
  },
  {
    id: "M2",
    name: "漫画与图片",
    detail: "文件夹导入、阅读下载、列表与随机照片墙",
    state: "待开发",
  },
  {
    id: "M3",
    name: "小说与个人照片",
    detail: "章节编辑、可靠保存、阅读与原件备份",
    state: "待开发",
  },
  {
    id: "M4",
    name: "私密内容",
    detail: "保险库会话、加密名称、受保护的列表和预览",
    state: "待开发",
  },
  {
    id: "M5",
    name: "发布与恢复验收",
    detail: "简单安装、后台启动、云端备份与整机恢复",
    state: "待开发",
  },
];
const activeModule = computed(() =>
  modules.find((m) => m.id === selected.value),
);
const busy = computed(
  () => starting.value || report.value?.status === "running",
);
const passed = computed(
  () => report.value?.checks.filter((c) => c.status === "passed").length || 0,
);
const total = computed(() => report.value?.checks.length || 11);
const percent = computed(() => Math.round((passed.value / total.value) * 100));
const statusText = computed(
  () =>
    ({
      idle: "等待首次验证",
      running: "验证进行中",
      passed: "恢复验证通过",
      failed: "验证需要处理",
    })[report.value?.status || "idle"],
);
const finished = computed(() =>
  report.value?.finishedAt
    ? new Date(report.value.finishedAt).toLocaleString("zh-CN", {
        hour12: false,
      })
    : "尚未运行",
);
const statusLabel = (s: string) =>
  ({
    pending: "待验证",
    running: "进行中",
    passed: "通过",
    failed: "失败",
    blocked: "未执行",
  })[s] || s;
function navigate(next: string) {
  view.value = next;
  selected.value = "";
  window.scrollTo({ top: 0, behavior: "smooth" });
}
async function refresh() {
  if (polling || stopped) return;
  polling = true;
  pollController = new AbortController();
  try {
    const response = await fetch("/api/status", {
      cache: "no-store",
      signal: pollController.signal,
    });
    if (!response.ok) throw new Error("无法读取服务状态");
    const data = await response.json();
    report.value = data.report;
    connected.value = true;
  } catch (e) {
    if (!stopped) connected.value = false;
  }
  polling = false;
  if (!stopped) {
    if (timer) clearTimeout(timer);
    timer = setTimeout(
      refresh,
      report.value?.status === "running" ? 800 : 3000,
    );
  }
}
async function startVerification() {
  if (busy.value) return;
  view.value = "verification";
  selected.value = "";
  error.value = "";
  starting.value = true;
  try {
    const response = await fetch("/api/verification", {
      method: "POST",
      headers: { "X-SRICS-Request": "verification" },
    });
    if (!response.ok)
      throw new Error(
        response.status === 409
          ? "已有验证任务在运行。"
          : "验证未能启动，请确认服务仍在运行。",
      );
    if (timer) clearTimeout(timer);
    if (report.value) report.value.status = "running";
    await refresh();
  } catch (e) {
    error.value = e instanceof Error ? e.message : "连接失败，请重试。";
  } finally {
    starting.value = false;
  }
}
function downloadReport() {
  if (!report.value || !report.value.finishedAt) return;
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(report.value, null, 2)], {
      type: "application/json",
    }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = `srics-verification-${report.value.finishedAt.replace(/[:.]/g, "-")}.json`;
  link.click();
  URL.revokeObjectURL(url);
}
onMounted(refresh);
onUnmounted(() => {
  stopped = true;
  if (timer) clearTimeout(timer);
  pollController?.abort();
});
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <a
        class="brand"
        href="#"
        aria-label="SRICS Next 概览"
        @click.prevent="navigate('overview')"
        ><span class="brand-mark"><i></i><i></i><i></i></span
        ><span>SRICS <small>NEXT</small></span></a
      >
      <div class="workspace">
        <span class="workspace-avatar">S</span>
        <div>我的资料库<small>PERSONAL ARCHIVE</small></div>
        <span class="local-dot"></span>
      </div>
      <nav aria-label="主要导航">
        <button
          :class="{ active: view === 'overview' && !selected }"
          @click="navigate('overview')"
        >
          <Icon name="home" />概览
        </button>
        <p class="nav-label">资料空间</p>
        <button
          v-for="m in modules"
          :key="m.id"
          :class="{ active: selected === m.id }"
          @click="
            selected = m.id;
            view = 'module';
          "
        >
          <Icon :name="m.icon" />{{ m.name
          }}<span v-if="m.stage === 'M4'" class="mini-lock"
            ><Icon name="lock"
          /></span>
        </button>
        <p class="nav-label">工具</p>
        <button
          :class="{ active: view === 'verification' }"
          @click="navigate('verification')"
        >
          <Icon name="shield" />验证中心<span v-if="busy" class="pulse"></span>
        </button>
        <button
          :class="{ active: view === 'progress' }"
          @click="navigate('progress')"
        >
          <Icon name="steps" />开发进度
        </button>
      </nav>
      <div class="sidebar-foot">
        <span class="connection" :class="{ offline: !connected }"></span
        >{{ connected ? "本机服务已连接" : "正在连接本机服务"
        }}<small>v0.1 · M0 开发预览</small>
      </div>
    </aside>

    <main>
      <header class="topbar">
        <div>
          <span class="breadcrumb">我的资料库</span><span class="slash">/</span
          >{{
            activeModule?.name ||
            {
              overview: "概览",
              verification: "验证中心",
              progress: "开发进度",
            }[view]
          }}
        </div>
        <span class="mode-pill"><span></span>仅本机访问</span>
      </header>
      <div class="page">
        <template v-if="view === 'overview'">
          <div class="page-heading">
            <div>
              <p class="eyebrow">A PLACE FOR WHAT MATTERS</p>
              <h1>让收藏与生活，各有所归。</h1>
              <p class="subtitle">六个独立空间，一个属于你的资料库。</p>
            </div>
            <span class="edition">01<span>FOUNDATION</span></span>
          </div>
          <section class="hero">
            <div class="hero-copy">
              <span class="section-kicker"
                ><span class="tiny-square"></span>第一步 ·
                验证数据能够回来</span
              >
              <h2>保存之后，<br />也要能够安心取回。</h2>
              <p>
                底层验证已接入。使用自动生成的测试文件，走完转换、加密、备份与恢复的完整过程。
              </p>
              <button
                class="primary"
                :disabled="busy || !connected"
                @click="startVerification"
              >
                <Icon
                  :name="busy ? 'refresh' : 'shield'"
                  :class="{ spin: busy }"
                />{{ busy ? "验证正在运行" : "运行恢复验证"
                }}<Icon name="arrow" /></button
              ><span class="hero-note">仅处理合成样本，不读取你的个人文件</span>
            </div>
            <div class="archive-art" aria-hidden="true">
              <div class="art-orbit"></div>
              <div class="art-card card-back">
                <div class="art-lines"></div>
              </div>
              <div class="art-card card-middle"><Icon name="image" /></div>
              <div class="art-card card-front">
                <Icon name="lock" /><span>YOUR ARCHIVE</span>
                <div class="art-line"></div>
              </div>
              <div class="art-check"><Icon name="check" /></div>
              <span class="art-caption">KEEP IT. RECOVER IT.</span>
            </div>
          </section>
          <div class="overview-metrics">
            <div>
              <span class="metric-label">当前阶段</span
              ><strong>M0 <small>基础验证</small></strong>
            </div>
            <div>
              <span class="metric-label">最近验证</span
              ><strong class="metric-status" :class="report?.status">{{
                statusText
              }}</strong>
            </div>
            <div>
              <span class="metric-label">验证项目</span
              ><strong
                >{{ passed }} <small>/ {{ total }} 项通过</small></strong
              >
            </div>
          </div>
          <div class="section-heading">
            <h2>你的六个资料空间</h2>
            <span>功能范围已确定 · 业务功能待开发</span>
          </div>
          <section class="module-grid" aria-label="六类资料空间">
            <button
              v-for="m in modules"
              :key="m.id"
              class="module-card"
              @click="
                selected = m.id;
                view = 'module';
              "
            >
              <span class="module-icon" :class="m.color"
                ><Icon :name="m.icon" /></span
              ><span class="module-stage">{{ m.stage }}</span>
              <h3>{{ m.name }}</h3>
              <p>{{ m.sub }}</p>
              <span class="module-footer"
                >查看功能约定<Icon name="arrow"
              /></span>
            </button>
          </section>
          <div class="quiet-note">
            <Icon name="lock" />
            <p>
              个人文件与私密照片将默认加密。当前预览版提供底层验证，尚不接收真实资料。
            </p>
          </div>
        </template>

        <template v-else-if="view === 'verification'">
          <div class="page-heading">
            <div>
              <p class="eyebrow">TRUST, VERIFIED</p>
              <h1>验证中心</h1>
              <p class="subtitle">用一次真正的恢复，检查数据保存的完整过程。</p>
            </div>
            <button
              class="primary"
              :disabled="busy || !connected"
              @click="startVerification"
            >
              <Icon
                :name="busy ? 'refresh' : 'shield'"
                :class="{ spin: busy }"
              />{{ busy ? "验证中…" : "运行验证" }}
            </button>
          </div>
          <p v-if="error" class="error-message" role="alert">{{ error }}</p>
          <div v-if="!connected" class="error-message" role="alert">
            无法连接本机服务。启动服务后，这里会自动重新连接。
          </div>
          <section class="verification-summary" aria-live="polite">
            <div class="status-orb" :class="report?.status">
              <Icon
                :name="
                  report?.status === 'passed'
                    ? 'check'
                    : report?.status === 'failed'
                      ? 'close'
                      : 'shield'
                "
              />
            </div>
            <div class="summary-text">
              <h2>{{ statusText }}</h2>
              <p>
                {{
                  busy
                    ? "每项结果均来自实际执行，请保持服务运行。"
                    : `最近完成：${finished}`
                }}
              </p>
            </div>
            <div class="fraction">
              {{ passed }}<span>/ {{ total }}</span>
            </div>
            <div class="progress-track">
              <div :style="{ width: percent + '%' }"></div>
            </div>
          </section>
          <div class="verification-notice">
            样本与临时备份会在验证后清理。这验证的是本机技术流程；云端、独立硬盘与整机部署将在发布阶段验收。
          </div>
          <section class="check-list" aria-label="验证步骤">
            <article
              v-for="(check, index) in report?.checks || []"
              :key="check.id"
              class="check-row"
              :class="check.status"
            >
              <span class="check-number"
                ><Icon v-if="check.status === 'passed'" name="check" /><Icon
                  v-else-if="check.status === 'failed'"
                  name="close"
                /><span v-else>{{
                  String(index + 1).padStart(2, "0")
                }}</span></span
              >
              <div>
                <h3>{{ check.label }}</h3>
                <p v-if="check.detail">{{ check.detail }}</p>
              </div>
              <span class="check-result"
                >{{ statusLabel(check.status)
                }}<small v-if="check.durationMs"
                  >{{ (check.durationMs / 1000).toFixed(1) }} s</small
                ></span
              >
            </article>
          </section>
          <div class="report-footer">
            <p>报告不包含口令、私钥或样本内容。服务重启后页面记录会清空。</p>
            <button
              class="secondary"
              :disabled="!report?.finishedAt || busy"
              @click="downloadReport"
            >
              下载验证报告<Icon name="arrow" />
            </button>
          </div>
          <details
            v-if="report && Object.keys(report.versions).length"
            class="version-details"
          >
            <summary>查看本次验证环境</summary>
            <dl>
              <template v-for="(value, key) in report.versions" :key="key"
                ><dt>{{ key }}</dt>
                <dd>{{ value }}</dd></template
              >
            </dl>
          </details>
        </template>

        <template v-else-if="view === 'progress'">
          <div class="page-heading">
            <div>
              <p class="eyebrow">BUILT ONE STEP AT A TIME</p>
              <h1>开发进度</h1>
              <p class="subtitle">
                六类核心功能均属于第一版。每一步都有明确的交付与验收。
              </p>
            </div>
          </div>
          <section class="roadmap">
            <article
              v-for="s in stages"
              :key="s.id"
              :class="{ current: s.id === 'M0' }"
            >
              <span class="stage-number">{{ s.id }}</span>
              <div>
                <h2>{{ s.name }}</h2>
                <p>{{ s.detail }}</p>
              </div>
              <span class="stage-state">{{ s.state }}</span>
            </article>
          </section>
          <div class="quiet-note">
            <Icon name="shield" />
            <p>
              首版正式使用前，需要完成登录、局域网
              HTTPS、各设备解锁权限及独立备份恢复验收。
            </p>
          </div>
        </template>

        <template v-else-if="activeModule">
          <button class="back-link" @click="navigate('overview')">
            ← 返回概览
          </button>
          <div class="module-detail">
            <span class="module-icon large" :class="activeModule.color"
              ><Icon :name="activeModule.icon"
            /></span>
            <p class="eyebrow">{{ activeModule.stage }} · 待开发</p>
            <h1>{{ activeModule.name }}</h1>
            <p class="subtitle">{{ activeModule.sub }}</p>
            <ul>
              <li v-for="f in activeModule.features" :key="f">
                <span></span>{{ f }}
              </li>
            </ul>
            <div class="detail-note">
              这是已确定的功能范围，当前尚未开放上传与管理。
            </div>
            <button class="secondary" @click="navigate('progress')">
              查看开发顺序<Icon name="arrow" />
            </button>
          </div>
        </template>
        <footer class="page-footer">
          <span>SRICS NEXT</span><span>私有存储 · 简单使用 · 可验证恢复</span>
        </footer>
      </div>
    </main>
  </div>
</template>
