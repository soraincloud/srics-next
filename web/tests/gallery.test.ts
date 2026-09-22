import test from 'node:test';
import assert from 'node:assert/strict';
import { ref } from 'vue';
import { useGalleryNavigation } from '../src/gallery.ts';

function gallery(count = 40) {
  const items = ref(Array.from({ length: count }, (_, i) => ({ id: String(i) })));
  const viewing = ref<{ id: string } | undefined>(items.value[count - 1]);
  const next = ref('next-page');
  const loading = ref(false);
  return { items, viewing, next, loading };
}

test('next crosses a loaded-page boundary without closing the viewer', async () => {
  const state = gallery();
  let loads = 0;
  const nav = useGalleryNavigation(state.items, state.viewing, state.next, state.loading, async () => {
    loads++;
    state.items.value.push({ id: '40' });
    state.next.value = '';
  });
  assert.equal(nav.canNext.value, true);
  await nav.advance(1);
  assert.equal(loads, 1);
  assert.equal(state.viewing.value?.id, '40');
  assert.equal(nav.index.value, 40);
  assert.equal(nav.canNext.value, false);
  await nav.advance(-1);
  assert.equal(state.viewing.value?.id, '39');
  assert.equal(loads, 1);
});

test('rapid next presses start just one page request', async () => {
  const state = gallery();
  let finish!: () => void;
  let loads = 0;
  const nav = useGalleryNavigation(state.items, state.viewing, state.next, state.loading, () => {
    loads++;
    return new Promise<void>(resolve => { finish = resolve; });
  });
  const pending = nav.advance(1);
  await nav.advance(1);
  assert.equal(loads, 1);
  state.items.value.push({ id: '40' });
  finish();
  await pending;
  assert.equal(state.viewing.value?.id, '40');
  assert.equal(nav.moving.value, false);
});

for (const action of ['close', 'lock', 'open-another'] as const) {
  test(`a pending page request cannot replace the viewer after ${action}`, async () => {
    const state = gallery();
    let finish!: () => void;
    const nav = useGalleryNavigation(state.items, state.viewing, state.next, state.loading,
      () => new Promise<void>(resolve => { finish = resolve; }));
    const pending = nav.advance(1);
    state.viewing.value = action === 'open-another' ? state.items.value[0] : undefined;
    if (action === 'lock') state.items.value = [];
    state.items.value.push({ id: '40' });
    finish();
    await pending;
    assert.equal(state.viewing.value?.id, action === 'open-another' ? '0' : undefined);
  });
}

test('a failed load keeps the current photo and permits retry', async () => {
  const state = gallery();
  let attempts = 0;
  const nav = useGalleryNavigation(state.items, state.viewing, state.next, state.loading, async () => {
    if (++attempts === 1) throw new Error('offline');
    state.items.value.push({ id: '40' });
  });
  await assert.rejects(nav.advance(1), /offline/);
  assert.equal(state.viewing.value?.id, '39');
  assert.equal(nav.moving.value, false);
  await nav.advance(1);
  assert.equal(state.viewing.value?.id, '40');
});

test('previous at the first photo and next during loading do not move', async () => {
  const state = gallery(1);
  let loads = 0;
  const nav = useGalleryNavigation(state.items, state.viewing, state.next, state.loading, async () => { loads++; });
  await nav.advance(-1);
  state.loading.value = true;
  await nav.advance(1);
  assert.equal(loads, 0);
  assert.equal(state.viewing.value?.id, '0');
});
