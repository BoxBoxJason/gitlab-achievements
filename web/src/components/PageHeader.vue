<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3 pb-4 pt-2">
    <div class="flex min-w-0 items-start gap-4">
      <slot name="art" />
      <div class="min-w-0">
        <h1 class="break-words text-2xl leading-8">{{ title }}</h1>
        <div v-if="$slots.default" class="mt-1 max-w-2xl text-fg-subtle"><slot /></div>
      </div>
    </div>
    <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2"><slot name="actions" /></div>
  </header>
</template>
