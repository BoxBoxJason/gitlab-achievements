<script setup lang="ts">
import { computed } from 'vue'

import { roman } from '@/lib/roman'

// A criteria's tiers as a row of pixels, one per tier, filled up to the one
// reached: the same grid as the art, and readable at a glance down a list.
const props = defineProps<{ reached: number; total: number }>()

const label = computed(() =>
  props.reached > 0 ? `Tier ${roman(props.reached)} of ${roman(props.total)}` : `No tier yet, of ${props.total}`,
)
</script>

<template>
  <span class="inline-flex items-center gap-[2px]" role="img" :aria-label="label" :title="label">
    <span
      v-for="tier in total"
      :key="tier"
      class="size-[7px]"
      :class="tier <= reached ? (tier === reached ? 'bg-success-strong' : 'bg-success') : 'bg-tier-off'"
    />
  </span>
</template>
