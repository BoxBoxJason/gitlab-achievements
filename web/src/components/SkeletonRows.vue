<script setup lang="ts">
// GitLab's skeleton loader: grey bars where rows will be, so the page holds
// its shape while it loads.
withDefaults(defineProps<{ rows?: number; avatar?: boolean }>(), { rows: 8, avatar: true })
</script>

<template>
  <div role="status" aria-label="Loading" class="divide-y divide-line-subtle">
    <div v-for="row in rows" :key="row" class="flex items-center gap-3 px-4 py-3">
      <span class="skeleton h-3 w-6" />
      <span v-if="avatar" class="skeleton size-6 rounded-full" />
      <span class="skeleton h-3" :style="{ width: `${120 + ((row * 47) % 90)}px` }" />
      <span class="skeleton ml-auto h-3 w-14" />
    </div>
  </div>
</template>

<style scoped>
.skeleton {
  display: block;
  border-radius: 0.25rem;
  background: linear-gradient(90deg, var(--c-skeleton) 25%, var(--c-bg-strong) 50%, var(--c-skeleton) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s linear infinite;
}

@keyframes shimmer {
  from {
    background-position: 100% 0;
  }
  to {
    background-position: -100% 0;
  }
}
</style>
