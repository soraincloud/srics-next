<script setup lang="ts">
import { ref, onUnmounted } from "vue";
const dialog = ref<HTMLDialogElement>(),
  title = ref(""),
  detail = ref("");
let resolve: ((v: boolean) => void) | undefined;
function ask(heading: string, message: string) {
  title.value = heading;
  detail.value = message;
  dialog.value?.showModal();
  return new Promise<boolean>((r) => (resolve = r));
}
function finish(ok: boolean) {
  resolve?.(ok);
  resolve = undefined;
  dialog.value?.close();
}
onUnmounted(() => finish(false));
defineExpose({ ask });
</script>
<template>
  <dialog ref="dialog" class="upload-dialog" @cancel.prevent="finish(false)">
    <form class="form-stack" @submit.prevent="finish(true)">
      <h2>{{ title }}</h2>
      <p class="subtle-copy">{{ detail }}</p>
      <div class="dialog-actions">
        <button
          type="button"
          class="button secondary"
          autofocus
          @click="finish(false)"
        >
          取消</button
        ><button type="submit" class="button primary">确认执行</button>
      </div>
    </form>
  </dialog>
</template>
