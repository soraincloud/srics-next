<script setup lang="ts">
import { onMounted, ref } from "vue";
import App from "./App.vue";
import Icon from "./Icon.vue";
import { api, authenticated, csrf, jsonBody } from "./api";
import { useTheme } from "./useTheme";
const { theme, toggleTheme } = useTheme();
const ready = ref(false),
  configured = ref(false),
  busy = ref(false),
  password = ref(""),
  repeat = ref(""),
  error = ref("");
async function check() {
  error.value = "";
  try {
    const state = await api("/api/auth");
    configured.value = state.configured;
    authenticated.value = state.authenticated;
    csrf.value = state.csrf;
    ready.value = true;
  } catch (e) {
    error.value = (e as Error).message;
  }
}
async function submit() {
  if (busy.value) return;
  error.value = "";
  if (!configured.value && password.value !== repeat.value) {
    error.value = "两次密码不一致";
    return;
  }
  busy.value = true;
  try {
    const state = await api(
      configured.value ? "/api/auth/login" : "/api/auth/setup",
      { method: "POST", body: jsonBody({ password: password.value }) },
    );
    csrf.value = state.csrf;
    authenticated.value = true;
    configured.value = true;
    password.value = repeat.value = "";
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
onMounted(check);
</script>
<template>
  <App v-if="authenticated" />
  <div v-else class="auth-layout">
    <header class="auth-header">
      <a href="#/" class="brand"
        ><span class="brand-mark"><Icon name="library" /></span>SRICS<span
          class="brand-next"
          >Next</span
        ></a
      ><button
        class="theme-toggle"
        :aria-label="theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'"
        @click="toggleTheme"
      >
        <Icon :name="theme === 'dark' ? 'sun' : 'moon'" />
      </button>
    </header>
    <main class="auth-card panel">
      <span class="module-icon large"
        ><Icon :name="configured ? 'lock' : 'library'"
      /></span>
      <h1>
        {{
          !ready
            ? "正在连接资料库"
            : configured
              ? "欢迎回来。"
              : "你的收藏，从这里开始。"
        }}
      </h1>
      <p class="subtitle">
        {{
          configured
            ? "输入登录密码，回到你的个人资料库。"
            : "设置一个登录密码，开始保存漫画、图片与照片。"
        }}
      </p>
      <form v-if="ready" class="form-stack" @submit.prevent="submit">
        <label
          >登录密码<input
            v-model="password"
            type="password"
            :autocomplete="configured ? 'current-password' : 'new-password'"
            :minlength="configured ? 1 : 12"
            maxlength="72"
            required
            :placeholder="configured ? '请输入密码' : '至少 12 个字符'"
            autofocus
        /></label>
        <label v-if="!configured"
          >再次输入<input
            v-model="repeat"
            type="password"
            autocomplete="new-password"
            required
            placeholder="确认登录密码"
        /></label>
        <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
        <button class="button primary" :disabled="busy">
          {{ busy ? "请稍候…" : configured ? "进入资料库" : "创建我的资料库"
          }}<Icon name="arrow" />
        </button>
      </form>
      <template v-else-if="error"
        ><p class="inline-error" role="alert">{{ error }}</p>
        <button class="button secondary" @click="check">
          重新连接
        </button></template
      >
      <p class="auth-note">仅在本机运行 · 数据保存在你的设备上</p>
    </main>
    <p class="auth-footer">SRICS Next · 让保存和取回，都有把握。</p>
  </div>
</template>
