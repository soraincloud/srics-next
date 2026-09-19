import { ref, watch } from "vue";

const system = window.matchMedia("(prefers-color-scheme: dark)");
let preference: string | null = null;
try {
  preference = localStorage.getItem("srics-theme");
} catch {
  /* Storage may be disabled. */
}
const explicit = ref(preference === "light" || preference === "dark");
const theme = ref(
  preference === "light" || preference === "dark"
    ? preference
    : system.matches
      ? "dark"
      : "light",
);
watch(
  theme,
  (value) => {
    document.documentElement.dataset.theme = value;
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute("content", value === "dark" ? "#0a0a0c" : "#f1f3f7");
  },
  { immediate: true },
);
function toggleTheme() {
  explicit.value = true;
  theme.value = theme.value === "dark" ? "light" : "dark";
  try {
    localStorage.setItem("srics-theme", theme.value);
  } catch {
    /* Keep the selection for this visit. */
  }
}
function followSystem(event: MediaQueryListEvent) {
  if (!explicit.value) theme.value = event.matches ? "dark" : "light";
}
system.addEventListener("change", followSystem);

export function useTheme() {
  return { theme, toggleTheme };
}
