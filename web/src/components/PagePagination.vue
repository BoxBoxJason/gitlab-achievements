<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { formatNumber } from '@/lib/format'

import GlIcon from './GlIcon.vue'

// GitLab's pagination: previous and next either side of the page numbers,
// with gaps where a long range is elided. Pages are ?page= in the URL, and
// extra carries anything else a later page must keep (a fixed window).
const props = withDefaults(
  defineProps<{ total: number; pageSize: number; page: number; extra?: Record<string, string> }>(),
  { extra: () => ({}) },
)

const route = useRoute()
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))

const items = computed(() => {
  const last = pages.value
  const current = props.page
  const wanted = new Set([1, last, current - 1, current, current + 1].filter((n) => n >= 1 && n <= last))
  const sorted = [...wanted].sort((a, b) => a - b)

  const out: (number | 'gap')[] = []
  sorted.forEach((n, index) => {
    const previous = sorted[index - 1]
    if (previous !== undefined && n - previous > 1) out.push('gap')
    out.push(n)
  })

  return out
})

const to = (page: number) => ({
  query: { ...route.query, ...props.extra, page: page === 1 ? undefined : String(page) },
})

const first = computed(() => (props.page - 1) * props.pageSize + 1)
const last = computed(() => Math.min(props.total, props.page * props.pageSize))
</script>

<template>
  <div v-if="total > 0" class="flex flex-wrap items-center justify-between gap-3">
    <p class="text-fg-subtle">
      <span class="num">{{ formatNumber(first) }}–{{ formatNumber(last) }}</span> of
      <span class="num">{{ formatNumber(total) }}</span>
    </p>
    <nav v-if="pages > 1" aria-label="Pagination">
      <ul class="flex items-center gap-1">
        <li>
          <RouterLink v-if="page > 1" :to="to(page - 1)" class="btn" rel="prev">
            <GlIcon name="chevron-left" />Prev
          </RouterLink>
          <span v-else class="btn" aria-disabled="true"><GlIcon name="chevron-left" />Prev</span>
        </li>
        <li v-for="(item, index) in items" :key="index" class="hidden sm:block">
          <span v-if="item === 'gap'" class="px-2 text-fg-muted">…</span>
          <RouterLink
            v-else
            :to="to(item)"
            class="btn num min-w-8 px-2"
            :class="item === page && 'btn-confirm'"
            :aria-current="item === page ? 'page' : undefined"
            >{{ item }}</RouterLink
          >
        </li>
        <li>
          <RouterLink v-if="page < pages" :to="to(page + 1)" class="btn" rel="next">
            Next<GlIcon name="chevron-right" />
          </RouterLink>
          <span v-else class="btn" aria-disabled="true">Next<GlIcon name="chevron-right" /></span>
        </li>
      </ul>
    </nav>
  </div>
</template>
