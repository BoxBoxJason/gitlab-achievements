<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'

import GlIcon from './GlIcon.vue'

defineProps<{ crumbs: { label: string; to?: RouteLocationRaw }[] }>()
</script>

<template>
  <nav aria-label="Breadcrumb" class="min-w-0">
    <ol class="flex min-w-0 items-center gap-1 text-fg-subtle">
      <li v-for="(crumb, index) in crumbs" :key="index" class="flex min-w-0 items-center gap-1">
        <GlIcon v-if="index > 0" name="chevron-lg-right" :size="12" class="text-fg-muted" />
        <RouterLink v-if="crumb.to && index < crumbs.length - 1" :to="crumb.to" class="truncate text-fg-subtle">{{
          crumb.label
        }}</RouterLink>
        <span v-else class="truncate text-fg-strong" :aria-current="index === crumbs.length - 1 ? 'page' : undefined">{{
          crumb.label
        }}</span>
      </li>
    </ol>
  </nav>
</template>
