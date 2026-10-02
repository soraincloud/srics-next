<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
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
const showPassword = ref(false);
const capsLock = ref(false);
function hidePassword() { showPassword.value = false; }
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
    showPassword.value = false;
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
onMounted(() => { void check(); window.addEventListener("blur", hidePassword); });
onUnmounted(() => window.removeEventListener("blur", hidePassword));
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
      <div class="auth-heading">
        <span class="module-icon large"
          ><Icon :name="configured ? 'lock' : 'library'"
        /></span>
        <h1>{{ !ready ? "正在连接" : configured ? "登录" : "尚未配置" }}</h1>
      </div>
      <p v-if="ready && !configured" class="subtitle">请打开本机的 SRICS Next 程序，设置登录密码并启动服务。</p>
      <form v-if="ready && configured" class="form-stack" @submit.prevent="submit">
        <div class="login-password-group">
        <label for="login-password">登录密码</label>
        <div class="password-input">
          <input
            id="login-password"
            v-model="password"
            :type="showPassword ? 'text' : 'password'"
            autocomplete="current-password"
            autocapitalize="none"
            :spellcheck="false"
            :aria-invalid="error ? true : undefined"
            :aria-describedby="error ? 'login-password-hint login-password-error' : 'login-password-hint'"
            @input="error = ''"
            @keydown="capsLock = $event.getModifierState('CapsLock')"
            @keyup="capsLock = $event.getModifierState('CapsLock')"
            @blur="capsLock = false"
            minlength="1"
            maxlength="72"
            required
            placeholder="请输入密码"
            autofocus
        />
        <button type="button" class="password-toggle" :aria-label="showPassword ? '隐藏登录密码' : '显示登录密码'" :aria-pressed="showPassword" :title="showPassword ? '隐藏登录密码' : '显示登录密码'" @click="showPassword = !showPassword"><Icon :name="showPassword ? 'eye-off' : 'eye'" /></button>
        </div>
        <p id="login-password-hint" class="password-hint">使用本机程序中设置的登录密码；保险库口令用于解锁私密资料。</p>
        <p v-if="capsLock" class="password-hint" role="status">大写锁定已开启</p>
        <p v-if="error" id="login-password-error" class="inline-error" role="alert">{{ error }}</p>
        </div>
        <button class="button primary" :disabled="busy">
          <span>{{ busy ? "登录中…" : "登录" }}</span><Icon name="arrow" />
        </button>
      </form>
      <template v-else-if="error"
        ><p class="inline-error" role="alert">{{ error }}</p>
        <button class="button secondary" @click="check">
          重新连接
        </button></template
      >
      <button v-if="ready && !configured" class="button secondary" @click="check">刷新状态</button>
      <footer class="open-source-links" aria-label="开源许可与源码">
        <span>© 2026 SRICS Next contributors</span>
        <a href="/legal/LICENSE.txt" target="_blank" rel="noopener">AGPL-3.0</a>
        <a href="/legal/source.tar.gz" download>下载源码</a>
        <a href="/legal/NOTICE.txt" target="_blank" rel="noopener">版权与免责说明</a>
      </footer>
    </main>
  </div>
</template>
