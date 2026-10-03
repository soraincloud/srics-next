// Mirror the live editor, including marked text. Only an external model change
// may write back to it; an unrelated render must not restore an older value.
export function passwordBinding(
  read: () => string,
  write: (value: string) => void,
  publish: (value: string) => void,
  initial: string,
) {
  let lastPublished = initial;
  let composing = false;
  function sync() {
    const value = read();
    if (value !== lastPublished) {
      lastPublished = value;
      publish(value);
    }
    return value;
  }
  return {
    sync,
    reconcile(value: string) {
      if (value === lastPublished) return false;
      write(value);
      lastPublished = value;
      if (!value) composing = false;
      return true;
    },
    composition(active: boolean) { composing = active; sync(); },
    readForSubmit() { return composing ? undefined : sync(); },
    blocksEnter(event: { key: string; keyCode: number; isComposing: boolean }) {
      return event.key === "Enter" && (composing || event.isComposing || event.keyCode === 229);
    },
    forget() { lastPublished = ""; composing = false; },
  };
}

export function hasChinesePunctuation(value: string) {
  return /[。．，、；：！？“”‘’（）【】＂＇]/u.test(value);
}
