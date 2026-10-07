export type ParagraphAnchor = { paragraph: number; fraction: number };
type Rect = { top: number; bottom: number; height: number };
const unit = (value: number) => Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0));

// Find the paragraph crossing the reading line, including empty lines. A
// paragraph-relative offset survives changes of viewport width and font size.
export function paragraphAtLine(count: number, rect: (index: number) => Rect, line: number): ParagraphAnchor {
  if (!count) return { paragraph: 0, fraction: 0 };
  let low = 0, high = count - 1;
  while (low < high) {
    const mid = Math.floor((low + high) / 2);
    if (rect(mid).bottom <= line) low = mid + 1;
    else high = mid;
  }
  const r = rect(low);
  return { paragraph: low, fraction: unit((line - r.top) / Math.max(1, r.height)) };
}

export function clampAnchor(anchor: ParagraphAnchor, count: number): ParagraphAnchor {
  const paragraph = Math.max(0, Math.min(Math.max(0, count - 1), Math.trunc(anchor.paragraph) || 0));
  return { paragraph, fraction: unit(anchor.fraction) };
}

// Only one request runs at a time. Scroll events coalesce to the newest position
// so a slow response cannot overwrite a later chapter. Failed writes can retry.
export function createReadingWriter<T>(send: (value: T) => Promise<void>) {
  let pending: T | undefined, running: Promise<boolean> | undefined;
  const flush = (): Promise<boolean> => {
    if (running) return running;
    if (pending === undefined) return Promise.resolve(true);
    running = (async () => {
      while (pending !== undefined) {
        const value = pending;
        pending = undefined;
        try { await send(value); }
        catch {
          if (pending === undefined) pending = value;
          return false;
        }
      }
      return true;
    })().finally(() => { running = undefined; });
    return running;
  };
  return { queue(value: T) { pending = value; }, flush };
}
