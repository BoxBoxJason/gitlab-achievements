<script setup lang="ts">
// An achievement's art. The source images are 96px pixel art, so they are
// only drawn at sizes dividing or multiplying that evenly, unsmoothed.
withDefaults(defineProps<{ src?: string; size?: 24 | 32 | 48 | 96; locked?: boolean }>(), {
  src: '',
  size: 48,
  locked: false,
})
</script>

<template>
  <img
    v-if="src"
    :src="src"
    :width="size"
    :height="size"
    alt=""
    loading="lazy"
    class="pixelated shrink-0"
    :class="locked && 'opacity-35 grayscale'"
  />
  <span
    v-else
    class="inline-block shrink-0 rounded border border-dashed border-line-strong"
    :style="{ width: `${size}px`, height: `${size}px` }"
    aria-hidden="true"
  />
</template>
