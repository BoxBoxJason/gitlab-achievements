<script setup lang="ts">
import { computed, ref } from 'vue'

import { api, ApiError } from '@/api/client'
import EmptyState from '@/components/EmptyState.vue'
import GlAlert from '@/components/GlAlert.vue'
import PageHeader from '@/components/PageHeader.vue'
import { refreshMe, session } from '@/composables/session'
import { formatAbsolute } from '@/lib/format'

const participation = computed(() => session.me?.participation)
const confirming = ref(false)
const busy = ref(false)
const failure = ref('')

// Somebody can be left out for reasons that aren't theirs to change, and
// should learn which rather than wonder why they aren't ranked.
const leftOutBecause = computed(() => {
  const p = participation.value
  if (!p?.excluded || p.opted_out) return ''
  if (p.reason === 'bot') return 'GitLab flags your account as a bot, and this instance leaves bots out.'
  if (p.reason === 'configured') return "This instance's operator has listed your account as left out."
  return 'Your account is left out on this instance.'
})

async function change(action: 'out' | 'in') {
  busy.value = true
  failure.value = ''
  try {
    await (action === 'out' ? api.optOut() : api.optIn())
    confirming.value = false
    await refreshMe()
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : 'The change could not be saved.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <PageHeader title="Participation" />

  <EmptyState v-if="session.status === 'anonymous'" title="There's no signing in here">
    This instance doesn't use sign-in, so it can't tell who is asking. To be left out, ask its operator to add you to
    <code class="num">EXCLUDE_USERS</code>.
  </EmptyState>

  <EmptyState v-else-if="!participation" title="Opting out isn't available on this instance">
    Ask the instance's operator if you want to be left out.
  </EmptyState>

  <section v-else class="grid gap-x-10 gap-y-4 border-t border-line pt-6 md:grid-cols-[18rem_minmax(0,1fr)]" aria-labelledby="take-part">
    <div>
      <h2 id="take-part" class="text-base">Take part in achievements</h2>
      <p class="mt-1 text-fg-subtle">
        While you take part, your GitLab activity earns achievements and EXP, and you appear on the leaderboard.
      </p>
    </div>

    <div class="space-y-4">
      <GlAlert v-if="leftOutBecause" title="You're left out">{{ leftOutBecause }}</GlAlert>

      <template v-else-if="participation.opted_out">
        <p class="flex flex-wrap items-center gap-2">
          <span class="badge">Opted out</span>
          <span v-if="participation.opted_out_at" class="text-fg-subtle">since {{ formatAbsolute(participation.opted_out_at) }}</span>
        </p>
        <p class="text-fg-subtle">
          You're not on the leaderboard and your activity isn't counted. What you had earned is kept, and counting picks up from
          there when you opt back in. Activity from while you were out doesn't count.
        </p>
        <button type="button" class="btn btn-confirm" :disabled="busy" @click="change('in')">Opt back in</button>
      </template>

      <template v-else>
        <p><span class="badge badge-success">Taking part</span></p>

        <div v-if="confirming" class="rounded-lg border border-line p-4">
          <p class="font-semibold text-fg-strong">Opt out of achievements?</p>
          <ul class="mt-2 list-disc space-y-1 pl-5 text-fg-subtle">
            <li>You leave the leaderboard, and your profile here stops being shown.</li>
            <li>Your activity stops being counted from now on.</li>
            <li>Achievements already on your GitLab profile stay there. You can hide them on GitLab.</li>
          </ul>
          <div class="mt-4 flex gap-2">
            <button type="button" class="btn btn-danger" :disabled="busy" @click="change('out')">Opt out</button>
            <button type="button" class="btn" :disabled="busy" @click="confirming = false">Cancel</button>
          </div>
        </div>

        <template v-else-if="participation.opt_out_allowed">
          <button type="button" class="btn btn-danger" @click="confirming = true">Opt out</button>
        </template>

        <p v-else class="text-fg-subtle">Opting out is turned off on this instance. Ask its operator if you want to be left out.</p>
      </template>

      <GlAlert v-if="failure" variant="danger" title="Your choice wasn't saved">{{ failure }}</GlAlert>
    </div>
  </section>
</template>
