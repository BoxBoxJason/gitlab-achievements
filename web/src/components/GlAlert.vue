<script setup lang="ts">
import { computed } from 'vue'

import GlIcon, { type IconName } from './GlIcon.vue'

const props = withDefaults(defineProps<{ variant?: 'info' | 'warning' | 'danger' | 'success'; title?: string }>(), {
  variant: 'info',
  title: '',
})

const style = computed(
  () =>
    ({
      info: { box: 'bg-info-bg', icon: 'text-info', name: 'information-o' },
      warning: { box: 'bg-warning-bg', icon: 'text-warning', name: 'warning' },
      danger: { box: 'bg-danger-bg', icon: 'text-danger', name: 'warning' },
      success: { box: 'bg-success-bg', icon: 'text-success', name: 'check-circle-filled' },
    })[props.variant] as { box: string; icon: string; name: IconName },
)
</script>

<template>
  <div class="flex gap-3 rounded-lg px-4 py-3" :class="style.box" :role="variant === 'danger' ? 'alert' : 'status'">
    <GlIcon :name="style.name" class="mt-0.5" :class="style.icon" />
    <div class="min-w-0">
      <p v-if="title" class="font-semibold text-fg-strong">{{ title }}</p>
      <div class="text-fg"><slot /></div>
    </div>
  </div>
</template>
