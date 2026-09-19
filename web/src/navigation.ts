// A component may protect unfinished edits from in-app navigation and logout.
let guard: (() => boolean | Promise<boolean>) | undefined;
export function setNavigationGuard(next: () => boolean | Promise<boolean>) {
  guard = next;
  return () => {
    if (guard === next) guard = undefined;
  };
}
export async function canNavigate() {
  return guard?.() ?? true;
}
