<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { session } from '@/composables/session'

const props = withDefaults(
  defineProps<{ username: string; src?: string; userId?: number; size?: 24 | 32 | 48 | 64 | 96 }>(),
  { src: '', userId: 0, size: 24 },
)

const failed = ref(false)
watch(
  () => props.src,
  () => (failed.value = false),
)

// GitLab answers with absolute avatar URLs, but an instance behind some
// proxies reports them relative to itself.
const url = computed(() => {
  if (!props.src || failed.value) return ''
  if (/^(https?:|data:)|^\/\//.test(props.src)) return props.src

  const base = session.instance?.gitlab_url
  return base ? new URL(props.src, base).toString() : ''
})

// GitLab's identicon: the initial on one of seven tints, picked by user ID,
// so somebody with no uploaded avatar looks here the way they look there.
const tints = [
  ['#fcf1ef', '#ae1800'],
  ['#f4f0ff', '#5943b6'],
  ['#f1f1ff', '#4b4ba3'],
  ['#e9f3fc', '#0b5cad'],
  ['#ecf4ee', '#24663b'],
  ['#fdf1dd', '#8f4700'],
  ['#f0f0f0', '#4c4b51'],
] as const

const tint = computed(() => tints[props.userId % tints.length] ?? tints[0])
const initial = computed(() => props.username.charAt(0).toUpperCase())
const fontSize = computed(() => `${Math.round(props.size * 0.42)}px`)
</script>

<template>
  <img
    v-if="url"
    :src="url"
    :width="size"
    :height="size"
    :alt="''"
    loading="lazy"
    referrerpolicy="no-referrer"
    class="block shrink-0 rounded-full bg-bg-strong object-cover"
    :style="{ width: `${size}px`, height: `${size}px` }"
    @error="failed = true"
  />
  <span
    v-else
    class="inline-flex shrink-0 select-none items-center justify-center rounded-full font-semibold"
    :style="{ width: `${size}px`, height: `${size}px`, fontSize, background: tint[0], color: tint[1] }"
    aria-hidden="true"
    >{{ initial }}</span
  >
</template>
