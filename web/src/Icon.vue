<script setup lang="ts">
defineProps<{ name: string }>();
const paths: Record<string, string[]> = {
  sun: ["M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0", "M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5 19 19M5 19l1.5-1.5M17.5 6.5 19 5"],
  moon: ["M20.8 13A9 9 0 0 1 11 3.2 9 9 0 1 0 20.8 13Z"],
  library: ["M3 3h7v7H3Z", "M14 3h7v7h-7Z", "M3 14h7v7H3Z", "M14 14h7v7h-7Z"],
  info: ["M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0", "M12 11v6M12 7h.01"],
  computer: ["M3 4h18v13H3Z", "M8 21h8M12 17v4"],
  clock: ["M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0", "M12 7v5l3 2"],
  download: ["M12 3v12m-4-4 4 4 4-4", "M4 15v6h16v-6"],
  play: ["m8 4 12 8-12 8Z"],
  "chevron-right": ["m9 5 7 7-7 7"],
  "chevron-left": ["m15 5-7 7 7 7"],
  home: ["M3 10 12 3l9 7v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1Z", "M9 21v-8h6v8"],
  book: [
    "M12 5v16",
    "M12 5C8 2 4 3 2 4v15c3-1 7-1 10 2 3-3 7-3 10-2V4c-2-1-6-2-10 1Z",
  ],
  text: ["M5 3h14v18H5Z", "M8 8h8M8 12h8M8 16h5"],
  image: ["M3 3h18v18H3Z", "M3 16l6-6 5 5 3-3 4 4", "M16 7h.01"],
  camera: ["M3 7h4l2-3h6l2 3h4v13H3Z", "M16 13a4 4 0 1 1-8 0 4 4 0 0 1 8 0"],
  lock: ["M5 10h14v11H5Z", "M8 10V6a4 4 0 0 1 8 0v4", "M12 14v3"],
  folder: ["M3 6h7l2 3h9v12H3Z", "M3 6V3h7l2 3h8v3"],
  shield: ["M12 2 3 6v6c0 5 9 10 9 10s9-5 9-10V6Z", "m8 12 3 3 5-6"],
  steps: ["M4 6h4M4 12h8M4 18h16", "M16 3v8m-3-3 3 3 3-3"],
  arrow: ["M4 12h16m-6-6 6 6-6 6"],
  check: ["m5 12 4 4L19 6"],
  close: ["m6 6 12 12M6 18 18 6"],
  refresh: ["M20 7a9 9 0 1 0 1 8", "M20 3v5h-5"],
};
</script>
<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path v-for="(d, i) in paths[name] || paths.folder" :key="i" :d="d" />
  </svg>
</template>
