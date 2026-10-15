<script setup lang="ts">
// The app's mark: a pixel trophy in the tanuki's three oranges, drawn on a
// 16px grid like the achievement art it stands for.
withDefaults(defineProps<{ size?: number }>(), { size: 24 })

// [x, y, width, height, fill] on the 16px grid.
const pixels: [number, number, number, number, string][] = [
  [3, 1, 10, 1, '#fca326'],
  [1, 2, 2, 1, '#e24329'],
  [13, 2, 2, 1, '#e24329'],
  [1, 3, 1, 2, '#e24329'],
  [14, 3, 1, 2, '#e24329'],
  [2, 5, 1, 1, '#e24329'],
  [13, 5, 1, 1, '#e24329'],
  [3, 2, 10, 4, '#fc6d26'],
  [4, 6, 8, 1, '#fc6d26'],
  [5, 7, 6, 1, '#fc6d26'],
  [4, 2, 1, 4, '#fca326'],
  [10, 2, 2, 5, '#e24329'],
  [9, 7, 2, 1, '#e24329'],
  [7, 8, 2, 3, '#e24329'],
  [5, 11, 6, 1, '#fc6d26'],
  [4, 12, 8, 2, '#e24329'],
  [5, 12, 2, 1, '#fca326'],
]
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 16 16" shape-rendering="crispEdges" aria-hidden="true">
    <rect v-for="([x, y, w, h, fill], index) in pixels" :key="index" :x="x" :y="y" :width="w" :height="h" :fill="fill" />
  </svg>
</template>
