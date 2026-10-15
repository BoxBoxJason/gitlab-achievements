<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <div class="flex flex-col items-center px-4 py-12 text-center">
    <slot name="art" />
    <h2 class="mt-4 text-base">{{ title }}</h2>
    <p class="mt-1 max-w-md text-fg-subtle"><slot /></p>
    <div v-if="$slots.actions" class="mt-4 flex gap-2"><slot name="actions" /></div>
  </div>
</template>
