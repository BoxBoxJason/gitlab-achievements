<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { api, ApiError } from '@/api/client'
import type { Detail } from '@/api/types'
import ApiLink from '@/components/ApiLink.vue'
import EmptyState from '@/components/EmptyState.vue'
import GlAlert from '@/components/GlAlert.vue'
import GlIcon from '@/components/GlIcon.vue'
import PixelIcon from '@/components/PixelIcon.vue'
import ProgressBar from '@/components/ProgressBar.vue'
import SkeletonRows from '@/components/SkeletonRows.vue'
import TierPips from '@/components/TierPips.vue'
import UserAvatar from '@/components/UserAvatar.vue'
import { loadCatalog } from '@/composables/catalog'
import { isMe, session } from '@/composables/session'
import { useRequest } from '@/composables/useRequest'
import { groupByCategory, standingsOf, type Standing } from '@/lib/catalog'
import { formatAbsolute, formatNumber, formatRelative } from '@/lib/format'
import { roman } from '@/lib/roman'

const route = useRoute()
const ref = computed(() => String(route.params.ref))

const { data, error, loading } = useRequest(ref, async (value) => {
  const [catalog, user] = await Promise.all([loadCatalog(), api.user(value)])
  return { catalog, user }
})

const user = computed<Detail | undefined>(() => data.value?.user)
const mine = computed(() => (user.value ? isMe(user.value.gitlab_user_id) : false))
const missing = computed(() => error.value instanceof ApiError && error.value.status === 404)

const standings = computed(() =>
  data.value ? standingsOf(data.value.catalog, data.value.user.counters, data.value.user.awards) : new Map<string, Standing>(),
)
const groups = computed(() =>
  data.value
    ? groupByCategory(data.value.catalog).map((group) => ({
        ...group,
        rows: group.criteria.map((criteria) => standings.value.get(criteria.criteria_key)!),
      }))
    : [],
)

const tiersEarned = computed(() => user.value?.awards.length ?? 0)
const tiersTotal = computed(() => data.value?.catalog.criteria.reduce((sum, c) => sum + c.tiers.length, 0) ?? 0)
const started = computed(() => [...standings.value.values()].filter((s) => s.reached).length)

const recent = computed(() =>
  [...(user.value?.awards ?? [])].sort((a, b) => b.awarded_at.localeCompare(a.awarded_at)).slice(0, 6),
)

const iconOf = computed(() => new Map(data.value?.catalog.criteria.map((c) => [c.criteria_key, c.icon_url ?? '']) ?? []))

// The awards GitLab holds for this user that they have not put on their
// profile: only they can, from the email GitLab sent with each award.
const hidden = computed(
  () => new Set(user.value?.awards.filter((a) => a.status === 'accepted' && !a.shown_on_profile).map((a) => a.criteria_key)),
)

const profileURL = computed(() =>
  session.instance && user.value ? new URL(user.value.username, session.instance.gitlab_url.replace(/\/?$/, '/')).toString() : '',
)
</script>

<template>
  <EmptyState v-if="missing" :title="`No achievements recorded for ${ref}`">
    Nobody by that name or ID has activity counted here. People left out of achievements (bots, accounts the operator listed,
    and anyone who opted out) don't appear either.
    <template #actions>
      <RouterLink :to="{ name: 'leaderboard' }" class="btn">Go to the leaderboard</RouterLink>
    </template>
  </EmptyState>

  <GlAlert v-else-if="error" variant="danger" class="mt-4" title="This profile didn't load">{{ error.message }}</GlAlert>

  <SkeletonRows v-else-if="loading || !user" :rows="8" />

  <template v-else>
    <header class="flex flex-wrap items-center gap-x-6 gap-y-4 pb-6 pt-2">
      <UserAvatar :username="user.username" :src="user.avatar_url" :user-id="user.gitlab_user_id" :size="96" />
      <div class="min-w-[10rem] flex-1">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="break-words text-2xl leading-8">{{ user.username }}</h1>
          <span v-if="mine" class="badge badge-info">You</span>
        </div>
        <p class="text-fg-subtle">@{{ user.username }}</p>
      </div>
      <div class="flex items-center gap-2">
        <a v-if="profileURL" :href="profileURL" class="btn" target="_blank" rel="noopener">
          GitLab profile <GlIcon name="external-link" :size="14" />
        </a>
        <ApiLink :href="`/api/v1/users/${encodeURIComponent(user.username)}`" />
      </div>
    </header>

    <dl class="grid grid-cols-3 divide-x divide-line rounded-lg border border-line">
      <div class="px-4 py-3">
        <dt class="text-fg-subtle">Rank</dt>
        <dd class="num mt-0.5 text-lg font-semibold text-fg-strong sm:text-xl">
          <RouterLink :to="{ name: 'leaderboard', query: user.rank > 20 ? { page: String(Math.ceil(user.rank / 20)) } : {} }" class="text-fg-strong">
            #{{ formatNumber(user.rank) }}
          </RouterLink>
        </dd>
      </div>
      <div class="px-4 py-3">
        <dt class="text-fg-subtle">EXP</dt>
        <dd class="num mt-0.5 text-lg font-semibold text-fg-strong sm:text-xl">{{ formatNumber(user.exp_total) }}</dd>
      </div>
      <div class="px-4 py-3">
        <dt class="text-fg-subtle">Tiers</dt>
        <dd class="mt-0.5 whitespace-nowrap text-lg font-semibold sm:text-xl text-fg-strong">
          <span class="num">{{ tiersEarned }}</span><span class="num text-base font-normal text-fg-muted"> / {{ tiersTotal }}</span>
        </dd>
      </div>
    </dl>

    <section v-if="recent.length > 0" class="mt-8" aria-labelledby="recent">
      <h2 id="recent" class="text-base">Recently reached</h2>
      <ol class="mt-3 divide-y divide-line-subtle rounded-lg border border-line">
        <li v-for="award in recent" :key="`${award.criteria_key}-${award.tier}`" class="flex items-center gap-3 px-4 py-2.5">
          <PixelIcon :src="iconOf.get(award.criteria_key)" :size="24" />
          <p class="flex min-w-0 flex-1 items-baseline gap-2">
            <RouterLink
              :to="{ name: 'criteria', params: { criteria: award.criteria_key } }"
              class="shrink-0 font-semibold text-fg-strong"
              >{{ award.name }}</RouterLink
            >
            <span class="truncate text-fg-subtle">{{ award.description }}</span>
          </p>
          <span class="num shrink-0 text-success">+{{ formatNumber(award.exp_reward) }}</span>
          <time :datetime="award.awarded_at" :title="formatAbsolute(award.awarded_at)" class="hidden w-28 shrink-0 text-right text-fg-subtle sm:block">{{
            formatRelative(award.awarded_at)
          }}</time>
        </li>
      </ol>
    </section>

    <section class="mt-8" aria-labelledby="progress">
      <div class="flex items-baseline justify-between gap-4">
        <h2 id="progress" class="text-base">Achievements</h2>
        <p class="text-fg-subtle"><span class="num">{{ started }}</span> of <span class="num">{{ standings.size }}</span> started</p>
      </div>

      <GlAlert v-if="mine && hidden.size > 0" class="mt-3">
        {{ hidden.size === 1 ? 'One achievement isn\'t' : `${hidden.size} achievements aren't` }} on your GitLab profile yet. GitLab keeps
        awards hidden until you accept them, from the link in the email it sent you with each one. They're marked with a lock below.
      </GlAlert>

      <div v-for="group in groups" :key="group.key" class="mt-6">
        <h3 class="text-sm font-semibold text-fg-subtle">{{ group.name }}</h3>
        <ul class="mt-2 divide-y divide-line-subtle rounded-lg border border-line">
          <li v-for="row in group.rows" :key="row.criteria.criteria_key">
            <RouterLink
              :to="{ name: 'criteria', params: { criteria: row.criteria.criteria_key } }"
              class="flex items-center gap-4 px-4 py-3 text-fg hover:bg-bg-hover hover:no-underline"
            >
              <PixelIcon :src="row.criteria.icon_url" :size="48" :locked="!row.reached" />
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span class="font-semibold text-fg-strong">{{ row.criteria.title }}</span>
                  <span v-if="row.reached" class="badge badge-success num" :title="row.reached.name">{{ roman(row.reached.tier) }}</span>
                  <span
                    v-if="mine && hidden.has(row.criteria.criteria_key)"
                    class="text-fg-muted"
                    title="Not on your GitLab profile until you accept it"
                  >
                    <GlIcon name="lock" :size="14" />
                    <span class="sr-only">Not on your GitLab profile until you accept it</span>
                  </span>
                </div>
                <p class="mt-0.5 truncate text-fg-subtle">
                  {{ row.next ? `Next: ${row.next.description}` : 'Every tier reached.' }}
                </p>
                <div class="mt-2 flex items-center gap-3 sm:hidden">
                  <TierPips :reached="row.reached?.tier ?? 0" :total="row.criteria.tiers.length" />
                  <span class="num text-xs text-fg-subtle">
                    {{ formatNumber(row.count) }}<template v-if="row.next"> / {{ formatNumber(row.next.threshold) }}</template>
                  </span>
                </div>
              </div>
              <div class="hidden w-56 shrink-0 sm:block">
                <div class="flex items-center justify-between gap-2">
                  <TierPips :reached="row.reached?.tier ?? 0" :total="row.criteria.tiers.length" />
                  <span class="num text-xs text-fg-subtle">
                    {{ formatNumber(row.count) }}<template v-if="row.next"> / {{ formatNumber(row.next.threshold) }}</template>
                  </span>
                </div>
                <ProgressBar class="mt-2" :value="row.progress" :label="`Progress to the next ${row.criteria.title} tier`" />
              </div>
            </RouterLink>
          </li>
        </ul>
      </div>
    </section>
  </template>
</template>
