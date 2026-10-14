<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { vaultImage, releaseVaultImage } from "./vault";
const props = defineProps<{ id: string }>();
const src = ref(""), failed = ref(false);
const controller = new AbortController();
onMounted(async () => {
  try { src.value = await vaultImage(props.id, true, controller.signal, true); }
  catch { if (!controller.signal.aborted) failed.value = true; }
});
onUnmounted(() => { controller.abort(); releaseVaultImage(src.value); });
</script>
<template>
  <video v-if="src && !failed" :src="src" controls playsinline preload="metadata" @error="failed = true" />
  <p v-else-if="failed">浏览器无法播放此视频，请下载原件查看。</p>
  <span v-else>加载动态画面…</span>
</template>
