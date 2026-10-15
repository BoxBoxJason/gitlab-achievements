<script setup lang="ts">
import { computed } from 'vue'

import ApiLink from '@/components/ApiLink.vue'
import GlAlert from '@/components/GlAlert.vue'
import PageHeader from '@/components/PageHeader.vue'
import PixelIcon from '@/components/PixelIcon.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import TierPips from '@/components/TierPips.vue'
import { loadCatalog } from '@/composables/catalog'
import { session } from '@/composables/session'
import { useRequest } from '@/composables/useRequest'
import { groupByCategory, standingsOf } from '@/lib/catalog'
import { formatCompact, formatNumber } from '@/lib/format'
import { roman } from '@/lib/roman'

const { data: catalog, error, loading } = useRequest(() => 0, () => loadCatalog())

const groups = computed(() => (catalog.value ? groupByCategory(catalog.value) : []))

// Signed in, the catalog doubles as your own progress through it.
const record = computed(() => session.me?.record ?? null)
const standings = computed(() =>
  catalog.value && record.value ? standingsOf(catalog.value, record.value.counters, record.value.awards) : undefined,
)

// Every criteria pays the same EXP for the same tier, so one ladder speaks
// for the whole catalog.
const ladder = computed(() => catalog.value?.criteria[0]?.tiers ?? [])
const tierCount = computed(() => ladder.value.length)
</script>

<template>
  <PageHeader title="Achievements">
    <template v-if="catalog">
      {{ catalog.criteria.length }} achievements of {{ tierCount }} tiers each, earned from activity on GitLab. A tier pays the same EXP whichever
      achievement it belongs to.
    </template>
    <template #actions><ApiLink href="/api/v1/achievements" /></template>
  </PageHeader>

  <GlAlert v-if="error" variant="danger" title="The catalog didn't load">{{ error.message }}</GlAlert>

  <SkeletonRows v-else-if="loading || !catalog" :rows="10" />

  <template v-else>
    <div class="overflow-x-auto rounded-lg border border-line">
      <table class="w-full min-w-max text-center">
        <tbody>
          <tr class="border-b border-line-subtle">
            <th scope="row" class="bg-bg-subtle px-4 py-2 text-left font-semibold text-fg-strong">Tier</th>
            <td v-for="tier in ladder" :key="tier.tier" class="num px-3 py-2 text-fg-subtle">{{ roman(tier.tier) }}</td>
          </tr>
          <tr>
            <th scope="row" class="bg-bg-subtle px-4 py-2 text-left font-semibold text-fg-strong">EXP</th>
            <td v-for="tier in ladder" :key="tier.tier" class="num px-3 py-2 font-semibold text-fg-strong">{{ formatCompact(tier.exp_reward) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <section v-for="group in groups" :key="group.key" class="mt-8" :aria-labelledby="`category-${group.key}`">
      <h2 :id="`category-${group.key}`" class="text-base">
        {{ group.name }} <span class="badge num ml-1 align-middle">{{ group.criteria.length }}</span>
      </h2>
      <ul class="mt-3 grid gap-3 md:grid-cols-2">
        <li v-for="criteria in group.criteria" :key="criteria.criteria_key">
          <RouterLink
            :to="{ name: 'criteria', params: { criteria: criteria.criteria_key } }"
            class="flex h-full items-start gap-4 rounded-lg border border-line p-4 text-fg hover:border-line-strong hover:bg-bg-hover hover:no-underline"
          >
            <PixelIcon :src="criteria.icon_url" :size="48" />
            <div class="min-w-0 flex-1">
              <div class="flex items-center justify-between gap-2">
                <span class="font-semibold text-fg-strong">{{ criteria.title }}</span>
                <span
                  v-if="standings?.get(criteria.criteria_key)?.reached"
                  class="badge badge-success num"
                  :title="standings.get(criteria.criteria_key)!.reached!.name"
                  >{{ roman(standings.get(criteria.criteria_key)!.reached!.tier) }}</span
                >
              </div>
              <p class="mt-0.5 text-fg-subtle">{{ criteria.tiers[0]?.description }}</p>
              <p class="mt-2 text-xs text-fg-muted">
                <span class="num">{{ formatNumber(criteria.tiers[0]?.threshold ?? 0) }}</span> to
                <span class="num">{{ formatCompact(criteria.tiers[criteria.tiers.length - 1]?.threshold ?? 0) }}</span>
              </p>
              <TierPips
                v-if="standings"
                class="mt-2"
                :reached="standings.get(criteria.criteria_key)?.reached?.tier ?? 0"
                :total="criteria.tiers.length"
              />
            </div>
          </RouterLink>
        </li>
      </ul>
    </section>
  </template>
</template>
