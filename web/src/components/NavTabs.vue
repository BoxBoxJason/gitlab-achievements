<script setup lang="ts">
import { nextTick, useTemplateRef, watch } from 'vue'
import type { RouteLocationRaw } from 'vue-router'

// GitLab's tabs: an underline under the active one, on a hairline that
// runs the width of the content. They are links, so every tab is a URL.
const props = defineProps<{ tabs: { label: string; to: RouteLocationRaw; active: boolean }[]; label: string }>()

// On a narrow screen the tabs scroll sideways; keep the active one in view.
const list = useTemplateRef<HTMLElement>('list')
watch(
  () => props.tabs.findIndex((tab) => tab.active),
  async () => {
    await nextTick()
    const active = list.value?.querySelector<HTMLElement>('[aria-current="page"]')
    const scroller = list.value?.parentElement
    if (active && scroller) scroller.scrollLeft = active.offsetLeft - (scroller.clientWidth - active.offsetWidth) / 2
  },
  { immediate: true },
)
</script>

<template>
  <nav :aria-label="label" class="-mx-4 overflow-x-auto px-4 sm:mx-0 sm:px-0">
    <ul ref="list" class="flex min-w-max border-b border-line">
      <li v-for="tab in tabs" :key="tab.label">
        <RouterLink
          :to="tab.to"
          class="-mb-px block border-b-2 px-4 py-3 leading-5 text-fg hover:border-line-strong hover:no-underline"
          :class="tab.active ? 'border-primary font-semibold text-fg-strong' : 'border-transparent'"
          :aria-current="tab.active ? 'page' : undefined"
          >{{ tab.label }}</RouterLink
        >
      </li>
    </ul>
  </nav>
</template>
