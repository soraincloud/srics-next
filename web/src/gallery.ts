import { computed, ref, type Ref } from "vue";

// Preserve the open photo while loading the next page; closing or locking the
// viewer must invalidate the pending move instead of reopening private content.
export function useGalleryNavigation<T extends { id: string }>(
  items: Ref<T[]>, viewing: Ref<T | undefined>, next: Ref<string>,
  loading: Ref<boolean>, loadMore: () => Promise<void>,
) {
  const moving = ref(false);
  const index = computed(() => items.value.findIndex(item => item.id === viewing.value?.id));
  const canNext = computed(() => index.value >= 0 && (index.value < items.value.length - 1 || !!next.value));
  async function advance(delta: number) {
    if (moving.value || loading.value || !viewing.value || index.value + delta < 0) return;
    const source = viewing.value;
    moving.value = true;
    try {
      if (index.value + delta >= items.value.length && next.value) await loadMore();
      if (viewing.value !== source) return;
      const position = items.value.findIndex(item => item.id === source.id);
      const target = position >= 0 ? items.value[position + delta] : undefined;
      if (target) viewing.value = target;
    } finally { moving.value = false; }
  }
  return { index, moving, canNext, advance };
}
