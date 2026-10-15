<script setup lang="ts">
// GitLab's own icons (@gitlab/svgs), imported one by one so the bundle holds
// only those this frontend draws rather than the whole sprite.
import { computed } from 'vue'

import chart from '@gitlab/svgs/dist/sprite_icons/chart.svg?raw'
import catalogCheckmark from '@gitlab/svgs/dist/sprite_icons/catalog-checkmark.svg?raw'
import checkCircleFilled from '@gitlab/svgs/dist/sprite_icons/check-circle-filled.svg?raw'
import chevronLeft from '@gitlab/svgs/dist/sprite_icons/chevron-left.svg?raw'
import chevronLgRight from '@gitlab/svgs/dist/sprite_icons/chevron-lg-right.svg?raw'
import chevronRight from '@gitlab/svgs/dist/sprite_icons/chevron-right.svg?raw'
import close from '@gitlab/svgs/dist/sprite_icons/close.svg?raw'
import code from '@gitlab/svgs/dist/sprite_icons/code.svg?raw'
import externalLink from '@gitlab/svgs/dist/sprite_icons/external-link.svg?raw'
import hamburger from '@gitlab/svgs/dist/sprite_icons/hamburger.svg?raw'
import informationO from '@gitlab/svgs/dist/sprite_icons/information-o.svg?raw'
import leave from '@gitlab/svgs/dist/sprite_icons/leave.svg?raw'
import lock from '@gitlab/svgs/dist/sprite_icons/lock.svg?raw'
import monitor from '@gitlab/svgs/dist/sprite_icons/monitor.svg?raw'
import moon from '@gitlab/svgs/dist/sprite_icons/moon.svg?raw'
import preferences from '@gitlab/svgs/dist/sprite_icons/preferences.svg?raw'
import sun from '@gitlab/svgs/dist/sprite_icons/sun.svg?raw'
import tanuki from '@gitlab/svgs/dist/sprite_icons/tanuki.svg?raw'
import user from '@gitlab/svgs/dist/sprite_icons/user.svg?raw'
import warning from '@gitlab/svgs/dist/sprite_icons/warning.svg?raw'

const icons = {
  'catalog-checkmark': catalogCheckmark,
  chart,
  'check-circle-filled': checkCircleFilled,
  'chevron-left': chevronLeft,
  'chevron-lg-right': chevronLgRight,
  'chevron-right': chevronRight,
  close,
  code,
  'external-link': externalLink,
  hamburger,
  'information-o': informationO,
  leave,
  lock,
  monitor,
  moon,
  preferences,
  sun,
  tanuki,
  user,
  warning,
} as const

export type IconName = keyof typeof icons

const props = withDefaults(defineProps<{ name: IconName; size?: 12 | 14 | 16 | 24 }>(), { size: 16 })

// The files carry a fixed size, or none, and no fill; both are set here.
const svg = computed(() =>
  icons[props.name].replace(
    /<svg([^>]*)>/,
    (_, attributes: string) =>
      `<svg${attributes.replace(/\s(width|height)="[^"]*"/g, '')} width="${props.size}" height="${props.size}" fill="currentColor" aria-hidden="true" focusable="false">`,
  ),
)
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -- the SVGs come from @gitlab/svgs, at build time -->
  <span class="inline-flex shrink-0" v-html="svg" />
</template>
