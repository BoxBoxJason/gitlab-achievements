<script setup lang="ts">
import { computed } from 'vue'

import { roman } from '@/lib/roman'
import { tierBand, type TierBand } from '@/lib/tier'

// A criteria's tiers as a row of pixels, one per tier, filled up to the one
// reached: the same grid as the art, and readable at a glance down a list.
// Each reached pixel takes its tier's band colour, the one its badge
// carries on GitLab, so the row and the profile tell the same story.
const props = defineProps<{ reached: number; total: number }>()

const label = computed(() =>
  props.reached > 0 ? `Tier ${roman(props.reached)} of ${roman(props.total)}` : `No tier yet, of ${props.total}`,
)

const bandClass: Record<TierBand, string> = {
  bronze: 'bg-band-bronze border-band-bronze-edge',
  silver: 'bg-band-silver border-band-silver-edge',
  gold: 'bg-band-gold border-band-gold-edge',
  purple: 'bg-band-purple border-band-purple-edge',
}
</script>

<template>
  <span class="inline-flex items-center gap-[2px]" role="img" :aria-label="label" :title="label">
    <span
      v-for="tier in total"
      :key="tier"
      class="size-[7px]"
      :class="tier <= reached ? ['border', bandClass[tierBand(tier)]] : 'bg-tier-off'"
    />
  </span>
</template>
