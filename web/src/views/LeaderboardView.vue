<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { api, PAGE_SIZE } from '@/api/client'
import type { Period } from '@/api/types'
import ApiLink from '@/components/ApiLink.vue'
import EmptyState from '@/components/EmptyState.vue'
import GlAlert from '@/components/GlAlert.vue'
import NavTabs from '@/components/NavTabs.vue'
import PageHeader from '@/components/PageHeader.vue'
import PagePagination from '@/components/PagePagination.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import UserCell from '@/components/UserCell.vue'
import { isMe, session } from '@/composables/session'
import { useRequest } from '@/composables/useRequest'
import { formatAbsolute, formatNumber, hostOf } from '@/lib/format'

const route = useRoute()

// The windows are rolling, so they are named by their length rather than
// as calendar weeks or months, which would start on a different day for
// every reader.
const periods: { period: Period; label: string; empty: string }[] = [
  { period: 'all', label: 'All time', empty: '' },
  { period: 'year', label: 'Last 365 days', empty: 'in the last 365 days' },
  { period: 'month', label: 'Last 30 days', empty: 'in the last 30 days' },
  { period: 'week', label: 'Last 7 days', empty: 'in the last 7 days' },
  { period: 'day', label: 'Last 24 hours', empty: 'in the last 24 hours' },
]

const period = computed<Period>(() => {
  const asked = route.query.period
  return periods.some((p) => p.period === asked) ? (asked as Period) : 'all'
})
const page = computed(() => Math.max(1, Number(route.query.page) || 1))
// Later pages of a window rank from the moment page 1 was ranked, so the
// window does not slide between pages.
const since = computed(() => (typeof route.query.since === 'string' ? route.query.since : undefined))

const { data, error, loading } = useRequest(
  () => [period.value, page.value, since.value] as const,
  ([p, n, s]) => api.leaderboard(p, (n - 1) * PAGE_SIZE, p === 'all' ? undefined : s),
)

const tabs = computed(() =>
  periods.map((p) => ({
    label: p.label,
    to: { query: p.period === 'all' ? {} : { period: p.period } },
    active: p.period === period.value,
  })),
)

const ranked = computed(() => period.value !== 'all')
const paging = computed((): Record<string, string> => (ranked.value && data.value?.since ? { since: data.value.since } : {}))
const emptyWindow = computed(() => periods.find((p) => p.period === period.value)?.empty ?? '')

const apiHref = computed(() =>
  period.value === 'all' ? '/api/v1/leaderboard' : `/api/v1/leaderboard?period=${period.value}`,
)

function medal(rank: number): string {
  return rank <= 3 ? 'text-fg-strong font-semibold' : 'text-fg-subtle'
}
</script>

<template>
  <PageHeader title="Leaderboard">
    <template v-if="session.instance">EXP from achievements earned on {{ hostOf(session.instance.gitlab_url) }}.</template>
    <template v-else>EXP from achievements earned on this GitLab instance.</template>
    <template #actions><ApiLink :href="apiHref" /></template>
  </PageHeader>

  <NavTabs :tabs="tabs" label="Period" />

  <GlAlert v-if="error" variant="danger" class="mt-4" title="The leaderboard didn't load">{{ error.message }}</GlAlert>

  <div v-else class="mt-4 overflow-hidden rounded-lg border border-line">
    <table class="w-full table-fixed">
      <colgroup>
        <col class="w-14 sm:w-16" />
        <col />
        <col v-if="ranked" class="w-28 sm:w-32" />
        <col class="w-24 sm:w-32" :class="ranked && 'hidden sm:table-column'" />
      </colgroup>
      <thead class="bg-bg-subtle text-left">
        <tr class="border-b border-line">
          <th scope="col" class="px-4 py-2.5 font-semibold text-fg-strong">Rank</th>
          <th scope="col" class="px-4 py-2.5 font-semibold text-fg-strong">User</th>
          <th v-if="ranked" scope="col" class="px-4 py-2.5 text-right font-semibold text-fg-strong">Earned</th>
          <th scope="col" class="px-4 py-2.5 text-right font-semibold text-fg-strong" :class="ranked && 'hidden sm:table-cell'">
            {{ ranked ? 'Total' : 'EXP' }}
          </th>
        </tr>
      </thead>
      <tbody v-if="!loading && data && data.entries.length > 0" class="divide-y divide-line-subtle">
        <tr
          v-for="entry in data.entries"
          :key="entry.gitlab_user_id"
          class="hover:bg-bg-hover"
          :class="isMe(entry.gitlab_user_id) && 'bg-info-bg/60'"
        >
          <td class="num px-4 py-2.5" :class="medal(entry.rank)">{{ entry.rank }}</td>
          <td class="px-4 py-2.5">
            <UserCell
              :username="entry.username"
              :avatar-url="entry.avatar_url"
              :user-id="entry.gitlab_user_id"
              :you="isMe(entry.gitlab_user_id)"
            />
          </td>
          <td v-if="ranked" class="num px-4 py-2.5 text-right font-semibold text-success">+{{ formatNumber(entry.exp_earned ?? 0) }}</td>
          <td class="num px-4 py-2.5 text-right" :class="ranked ? 'hidden text-fg-subtle sm:table-cell' : 'font-semibold text-fg-strong'">
            {{ formatNumber(entry.exp_total) }}
          </td>
        </tr>
      </tbody>
    </table>

    <SkeletonRows v-if="loading" :rows="10" />

    <EmptyState v-else-if="data && data.entries.length === 0 && data.total > 0" :title="`There's no page ${page}`">
      The ranking ends before this page.
      <template #actions>
        <RouterLink :to="{ query: { ...route.query, page: undefined } }" class="btn">Go to the first page</RouterLink>
      </template>
    </EmptyState>

    <EmptyState v-else-if="data && data.entries.length === 0 && ranked" :title="`Nobody earned a tier ${emptyWindow}`">
      EXP is counted when a tier is reached, dated by the activity that reached it.
      <template #actions>
        <RouterLink :to="{ query: {} }" class="btn">Show all time</RouterLink>
      </template>
    </EmptyState>

    <EmptyState v-else-if="data && data.entries.length === 0" title="Nobody has earned anything yet">
      Rankings appear as soon as anyone's GitLab activity reaches a first tier.
    </EmptyState>
  </div>

  <div v-if="data && data.total > 0" class="mt-4 space-y-2">
    <PagePagination :total="data.total" :page-size="PAGE_SIZE" :page="page" :extra="paging" />
    <p v-if="data.since" class="text-xs text-fg-muted">Ranked from {{ formatAbsolute(data.since) }}.</p>
  </div>
</template>
