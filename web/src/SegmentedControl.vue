<script setup lang="ts">
import { onMounted, onUpdated, onBeforeUnmount, ref } from "vue";

const root = ref<HTMLDivElement>();
const indicator = ref<HTMLSpanElement>();
let observer: ResizeObserver | undefined;
let frame = 0;

// Measure the actual button: labels and button widths differ between screens.
function alignIndicator() {
  const container = root.value;
  const marker = indicator.value;
  const selected = container?.querySelector<HTMLButtonElement>('button[aria-pressed="true"]');
  if (!container || !marker) return;
  if (!selected) {
    container.classList.remove("has-indicator");
    return;
  }
  marker.style.transform = `translate(${selected.offsetLeft}px, ${selected.offsetTop}px)`;
  marker.style.width = `${selected.offsetWidth}px`;
  marker.style.height = `${selected.offsetHeight}px`;
  container.classList.add("has-indicator");
}

onMounted(() => {
  alignIndicator();
  // Initial placement is immediate; only subsequent selections slide.
  frame = requestAnimationFrame(() => {
    frame = requestAnimationFrame(() => root.value?.classList.add("motion-ready"));
  });
  observer = new ResizeObserver(alignIndicator);
  if (root.value) {
    observer.observe(root.value);
    root.value.querySelectorAll("button").forEach(button => observer?.observe(button));
  }
});
onUpdated(alignIndicator);
onBeforeUnmount(() => {
  observer?.disconnect();
  cancelAnimationFrame(frame);
});
</script>

<template>
  <div ref="root" class="segmented" role="group">
    <span ref="indicator" class="segmented-indicator" aria-hidden="true"></span>
    <slot />
  </div>
</template>
