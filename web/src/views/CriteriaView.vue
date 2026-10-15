<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { api, ApiError, PAGE_SIZE } from '@/api/client'
import ApiLink from '@/components/ApiLink.vue'
import EmptyState from '@/components/EmptyState.vue'
import GlAlert from '@/components/GlAlert.vue'
import PageHeader from '@/components/PageHeader.vue'
import PagePagination from '@/components/PagePagination.vue'
import PixelIcon from '@/components/PixelIcon.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import UserCell from '@/components/UserCell.vue'
import { loadCatalog } from '@/composables/catalog'
import { useCrumbs } from '@/composables/crumbs'
import { isMe, session } from '@/composables/session'
import { useRequest } from '@/composables/useRequest'
import { categories, standingOf } from '@/lib/catalog'
import { formatNumber } from '@/lib/format'
import { roman } from '@/lib/roman'

const route = useRoute()
const key = computed(() => String(route.params.criteria))
const page = computed(() => Math.max(1, Number(route.query.page) || 1))

const catalog = useRequest(() => 0, () => loadCatalog())
const criteria = computed(() => catalog.data.value?.criteria.find((c) => c.criteria_key === key.value))

const ranking = useRequest(
  () => [key.value, page.value] as const,
  ([k, n]) => api.criteriaLeaderboard(k, (n - 1) * PAGE_SIZE),
)

const missing = computed(
  () => (catalog.data.value && !criteria.value) || (ranking.error.value instanceof ApiError && ranking.error.value.status === 404),
)

useCrumbs(() =>
  criteria.value ? [{ label: 'Achievements', to: { name: 'catalog' } }, { label: criteria.value.title }] : undefined,
)

const category = computed(() => categories.find((c) => c.key === criteria.value?.category)?.name)
const tiers = computed(() => [...(criteria.value?.tiers ?? [])].sort((a, b) => a.tier - b.tier))

const mine = computed(() => {
  const record = session.me?.record
  if (!record || !criteria.value) return undefined
  const count = record.counters.find((c) => c.criteria_key === key.value)?.count ?? 0
  return standingOf(criteria.value, count, record.awards)
})
</script>

<template>
  <EmptyState v-if="missing" :title="`There's no achievement called ${key}`">
    It may have been retired from the catalog.
    <template #actions>
      <RouterLink :to="{ name: 'catalog' }" class="btn">See every achievement</RouterLink>
    </template>
  </EmptyState>

  <GlAlert v-else-if="catalog.error.value" variant="danger" title="This achievement didn't load">{{ catalog.error.value.message }}</GlAlert>

  <SkeletonRows v-else-if="!criteria" :rows="8" />

  <template v-else>
    <PageHeader :title="criteria.title">
      <template #art><PixelIcon :src="criteria.icon_url" :size="96" class="-my-1 hidden sm:block" /></template>
      <p>{{ tiers[0]?.description }}</p>
      <p class="mt-2 flex flex-wrap items-center gap-2">
        <span v-if="category" class="badge">{{ category }}</span>
        <span class="text-fg-muted">
          {{ tiers.length }} tiers, from <span class="num">{{ formatNumber(tiers[0]?.threshold ?? 0) }}</span> to
          <span class="num">{{ formatNumber(tiers[tiers.length - 1]?.threshold ?? 0) }}</span>
        </span>
      </p>
      <template #actions><ApiLink :href="`/api/v1/leaderboard/criteria/${encodeURIComponent(key)}`" /></template>
    </PageHeader>

    <div class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_18rem]">
      <section aria-labelledby="ranking">
        <h2 id="ranking" class="sr-only">Ranking</h2>
        <GlAlert v-if="ranking.error.value" variant="danger" title="The ranking didn't load">{{ ranking.error.value.message }}</GlAlert>
        <div v-else class="overflow-hidden rounded-lg border border-line">
          <table class="w-full table-fixed">
            <colgroup>
              <col class="w-14 sm:w-16" />
              <col />
              <col class="w-24 sm:w-28" />
              <col class="w-16 sm:w-20" />
            </colgroup>
            <thead class="bg-bg-subtle text-left">
              <tr class="border-b border-line">
                <th scope="col" class="px-4 py-2.5 font-semibold text-fg-strong">Rank</th>
                <th scope="col" class="px-4 py-2.5 font-semibold text-fg-strong">User</th>
                <th scope="col" class="px-4 py-2.5 text-right font-semibold text-fg-strong">Count</th>
                <th scope="col" class="px-4 py-2.5 text-right font-semibold text-fg-strong">Tier</th>
              </tr>
            </thead>
            <tbody v-if="!ranking.loading.value && ranking.data.value" class="divide-y divide-line-subtle">
              <tr
                v-for="entry in ranking.data.value.entries"
                :key="entry.gitlab_user_id"
                class="hover:bg-bg-hover"
                :class="isMe(entry.gitlab_user_id) && 'bg-info-bg/60'"
              >
                <td class="num px-4 py-2.5" :class="entry.rank <= 3 ? 'font-semibold text-fg-strong' : 'text-fg-subtle'">{{ entry.rank }}</td>
                <td class="px-4 py-2.5">
                  <UserCell :username="entry.username" :avatar-url="entry.avatar_url" :user-id="entry.gitlab_user_id" :you="isMe(entry.gitlab_user_id)" />
                </td>
                <td class="num px-4 py-2.5 text-right font-semibold text-fg-strong">{{ formatNumber(entry.count) }}</td>
                <td class="num px-4 py-2.5 text-right text-fg-subtle">{{ entry.tier > 0 ? roman(entry.tier) : '–' }}</td>
              </tr>
            </tbody>
          </table>
          <SkeletonRows v-if="ranking.loading.value" :rows="8" />
          <EmptyState v-else-if="ranking.data.value?.entries.length === 0" title="Nobody has started this one">
            The first person whose activity counts toward it will top this ranking.
          </EmptyState>
        </div>
        <PagePagination v-if="ranking.data.value" class="mt-4" :total="ranking.data.value.total" :page-size="PAGE_SIZE" :page="page" />
      </section>

      <aside aria-labelledby="tiers" class="rounded-lg border border-line">
        <h2 id="tiers" class="border-b border-line bg-bg-subtle px-4 py-2.5 text-sm">Tiers</h2>
        <ol class="divide-y divide-line-subtle">
          <li
            v-for="tier in tiers"
            :key="tier.tier"
            class="flex items-center gap-3 px-4 py-2"
            :class="mine?.next?.tier === tier.tier && 'bg-info-bg/60'"
            :title="`${tier.name}: ${tier.description}`"
          >
            <span
              class="num w-11 shrink-0 font-semibold"
              :class="mine?.reached && tier.tier <= mine.reached.tier ? 'text-success' : 'text-fg-subtle'"
              >{{ roman(tier.tier) }}</span
            >
            <span class="num flex-1 text-fg-strong">{{ formatNumber(tier.threshold) }}</span>
            <span class="num text-fg-subtle">{{ formatNumber(tier.exp_reward) }} EXP</span>
          </li>
        </ol>
        <p v-if="mine" class="border-t border-line px-4 py-2.5 text-fg-subtle">
          You're at <span class="num font-semibold text-fg-strong">{{ formatNumber(mine.count) }}</span
          ><template v-if="mine.next">, {{ formatNumber(mine.next.threshold - mine.count) }} from {{ roman(mine.next.tier) }}</template
          ><template v-else>, every tier reached</template>.
        </p>
      </aside>
    </div>
  </template>
</template>
