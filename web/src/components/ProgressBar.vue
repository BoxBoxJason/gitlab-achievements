<script setup lang="ts">
defineProps<{ value: number; label: string }>()
</script>

<template>
  <span
    class="block h-1 w-full overflow-hidden rounded-full bg-bg-strong"
    role="progressbar"
    :aria-label="label"
    aria-valuemin="0"
    aria-valuemax="100"
    :aria-valuenow="Math.round(value * 100)"
  >
    <span class="block h-full rounded-full" :class="value >= 1 ? 'bg-success-strong' : 'bg-primary'" :style="{ width: `${value * 100}%` }" />
  </span>
</template>
