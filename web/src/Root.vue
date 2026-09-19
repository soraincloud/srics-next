<script setup lang="ts">
import { onMounted, ref } from "vue";
import App from "./App.vue";
import Icon from "./Icon.vue";
import { api, authenticated, csrf, jsonBody, sessionExpired } from "./api";
import { useTheme } from "./useTheme";
const { theme, toggleTheme } = useTheme();
const ready = ref(false),
  configured = ref(false),
  busy = ref(false),
  password = ref(""),
  error = ref("");
async function check() {
  error.value = "";
  try {
    const state = await api("/api/auth");
    configured.value = state.configured;
    authenticated.value = state.authenticated;
    if (state.authenticated) sessionExpired.value = false;
    csrf.value = state.csrf;
    ready.value = true;
  } catch (e) {
    error.value = (e as Error).message;
  }
}
async function submit() {
  if (busy.value || !configured.value) return;
  error.value = "";
  busy.value = true;
  try {
    const state = await api(
      "/api/auth/login",
      { method: "POST", body: jsonBody({ password: password.value }) },
    );
    csrf.value = state.csrf;
    authenticated.value = true;
    sessionExpired.value = false;
    configured.value = true;
    password.value = "";
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
      <h1>{{ !ready ? "正在连接" : configured ? "登录" : "尚未配置" }}</h1>
      <p v-if="ready && !configured" class="subtitle">请打开本机的 SRICS Next 程序，设置登录密码并启动服务。</p>
      <form v-if="ready && configured" class="form-stack" @submit.prevent="submit">
        <label
          >登录密码<input
            v-model="password"
            type="password"
            autocomplete="current-password"
            minlength="1"
            maxlength="72"
            required
            placeholder="请输入密码"
            autofocus
        /></label>
        <p v-if="error" class="inline-error" role="alert">{{ error }}</p>
        <button class="button primary" :disabled="busy">
          {{ busy ? "登录中…" : "登录"
          }}<Icon name="arrow" />
        </button>
      </form>
      <template v-else-if="error"
        ><p class="inline-error" role="alert">{{ error }}</p>
        <button class="button secondary" @click="check">
          重新连接
        </button></template
      >
      <button v-if="ready && !configured" class="button secondary" @click="check">刷新状态</button>
    </main>
  </div>
</template>
