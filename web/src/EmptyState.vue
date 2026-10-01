<script setup lang="ts">
import Icon from "./Icon.vue";
defineProps<{ icon: string; title: string; description?: string; compact?: boolean }>();
</script>

<template>
  <section class="empty-state" :class="{ 'empty-state-compact': compact }">
    <span class="empty-state-icon" aria-hidden="true"><Icon :name="icon" /></span>
    <h2>{{ title }}</h2>
    <p v-if="description">{{ description }}</p>
    <div v-if="$slots.default" class="empty-state-actions"><slot /></div>
  </section>
</template>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 300px;
  padding: 48px 24px;
  text-align: center;
  border: 2px solid var(--line);
  border-radius: 24px;
  background: var(--surface);
}
.empty-state-icon {
  display: grid;
  place-items: center;
  flex: none;
  width: 48px;
  height: 48px;
  margin-bottom: 18px;
  border-radius: 15px;
  background: var(--surface-soft);
  color: var(--secondary);
}
.empty-state-icon svg { width: 23px; height: 23px; stroke-width: 1.6; }
.empty-state h2 { margin: 0; color: var(--strong); font-size: 16px; line-height: 1.5; font-weight: 650; }
.empty-state p { max-width: 32em; margin: 9px 0 0; color: var(--secondary); font-size: 13px; line-height: 1.8; text-wrap: pretty; }
.empty-state-actions { display: flex; justify-content: center; flex-wrap: wrap; gap: 10px; margin-top: 22px; }
.empty-state-compact { min-height: 180px; padding: 28px 20px; border: 0; background: transparent; }
.empty-state-compact .empty-state-icon { width: 40px; height: 40px; margin-bottom: 12px; border-radius: 12px; }
.empty-state-compact h2 { font-size: 14px; }
@media (max-width: 720px) {
  .empty-state { min-height: 260px; padding: 36px 20px; border-radius: 20px; }
  .empty-state-compact { min-height: 180px; padding: 24px 12px; }
}
</style>
