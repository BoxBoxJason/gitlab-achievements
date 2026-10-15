<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { session, signOut } from '@/composables/session'
import { theme, type ThemeMode } from '@/composables/theme'
import { hostOf } from '@/lib/format'

import AppMark from './AppMark.vue'
import GlIcon, { type IconName } from './GlIcon.vue'
import UserAvatar from './UserAvatar.vue'

// GitLab's left sidebar: the app and the instance at the top, the places to
// go in the middle, and who you are and how things look at the bottom.
defineEmits<{ navigate: [] }>()

const route = useRoute()

const items = computed(() => {
  const list: { label: string; icon: IconName; to: object; active: boolean }[] = [
    { label: 'Leaderboard', icon: 'chart', to: { name: 'leaderboard' }, active: route.name === 'leaderboard' },
    {
      label: 'Achievements',
      icon: 'catalog-checkmark',
      to: { name: 'catalog' },
      active: route.name === 'catalog' || route.name === 'criteria',
    },
  ]

  const me = session.me
  if (me) {
    list.push(
      {
        label: 'Your achievements',
        icon: 'user',
        to: { name: 'user', params: { ref: me.username } },
        active: route.name === 'user' && String(route.params.ref).toLowerCase() === me.username.toLowerCase(),
      },
      { label: 'Participation', icon: 'preferences', to: { name: 'participation' }, active: route.name === 'participation' },
    )
  }

  return list
})

const modes: { mode: ThemeMode; icon: IconName; label: string }[] = [
  { mode: 'light', icon: 'sun', label: 'Light' },
  { mode: 'dark', icon: 'moon', label: 'Dark' },
  { mode: 'auto', icon: 'monitor', label: 'Match system' },
]

async function leave() {
  await signOut()
}
</script>

<template>
  <div class="flex h-full flex-col bg-sidebar">
    <div class="flex items-center gap-2 px-4 pb-3 pt-4">
      <AppMark :size="24" />
      <div class="min-w-0 leading-tight">
        <p class="font-semibold text-fg-strong">Achievements</p>
        <a
          v-if="session.instance"
          :href="session.instance.gitlab_url"
          class="block truncate text-xs text-fg-subtle"
          target="_blank"
          rel="noopener"
          >{{ hostOf(session.instance.gitlab_url) }}</a
        >
      </div>
    </div>

    <nav aria-label="Main" class="flex-1 overflow-y-auto px-2 py-2">
      <ul class="space-y-0.5">
        <li v-for="item in items" :key="item.label">
          <RouterLink
            :to="item.to"
            class="relative flex h-8 items-center gap-3 rounded-md px-3 text-fg hover:bg-bg-strong hover:no-underline"
            :class="item.active && 'bg-bg-strong font-semibold text-fg-strong'"
            :aria-current="item.active ? 'page' : undefined"
            @click="$emit('navigate')"
          >
            <span v-if="item.active" class="absolute inset-y-1.5 left-0.5 w-[3px] rounded-full bg-primary" aria-hidden="true" />
            <GlIcon :name="item.icon" class="text-fg-subtle" />
            {{ item.label }}
          </RouterLink>
        </li>
      </ul>
    </nav>

    <div class="space-y-3 border-t border-line px-3 py-3">
      <div role="radiogroup" aria-label="Color mode" class="flex rounded-md bg-bg-strong p-0.5">
        <button
          v-for="option in modes"
          :key="option.mode"
          type="button"
          role="radio"
          :aria-checked="theme === option.mode"
          :title="option.label"
          class="flex h-7 flex-1 items-center justify-center rounded text-fg-subtle"
          :class="theme === option.mode && 'bg-bg text-fg-strong shadow-sm'"
          @click="theme = option.mode"
        >
          <GlIcon :name="option.icon" />
          <span class="sr-only">{{ option.label }}</span>
        </button>
      </div>

      <div v-if="session.me" class="flex items-center gap-2">
        <UserAvatar :username="session.me.username" :src="session.me.avatar_url" :user-id="session.me.gitlab_user_id" :size="32" />
        <div class="min-w-0 flex-1 leading-tight">
          <p class="truncate font-semibold text-fg-strong">{{ session.me.username }}</p>
          <p class="truncate text-xs text-fg-subtle">@{{ session.me.username }}</p>
        </div>
        <button type="button" class="btn btn-sm h-7 w-7 px-0" title="Sign out" @click="leave">
          <GlIcon name="leave" />
          <span class="sr-only">Sign out</span>
        </button>
      </div>

      <p v-if="session.instance" class="num truncate px-1 text-xs text-fg-muted" :title="`gitlab-achievements ${session.instance.version}`">
        {{ session.instance.version }}
      </p>
    </div>
  </div>
</template>
