<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { vaultImage, releaseVaultImage } from "./vault";
const props = defineProps<{ id: string; original?: boolean }>();
const src = ref(""),
  failed = ref(false),
  host = ref<HTMLElement>();
const controller = new AbortController();
let observer: IntersectionObserver | undefined;
async function load() {
  failed.value = false;
  try {
    src.value = await vaultImage(props.id, props.original, controller.signal);
  } catch (e) {
    if (!controller.signal.aborted) failed.value = true;
  }
}
onMounted(() => {
  if (props.original) {
    void load();
    return;
  }
  observer = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        observer?.disconnect();
        void load();
      }
    },
    { rootMargin: "150px" },
  );
  if (host.value) observer.observe(host.value);
});
onUnmounted(() => {
  observer?.disconnect();
  controller.abort();
  releaseVaultImage(src.value);
});
</script>
<template>
  <span ref="host" class="private-image"
    ><img v-if="src" :src="src" alt="私密照片" draggable="false" /><button
      v-else-if="failed"
      class="text-link"
      @click.stop="load"
    >
      重试预览</button
    ><span v-else class="private-image-loading">加载中</span></span
  >
</template>
