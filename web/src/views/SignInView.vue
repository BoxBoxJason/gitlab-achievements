<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import { loginURL } from '@/api/client'
import AppMark from '@/components/AppMark.vue'
import GlIcon from '@/components/GlIcon.vue'

const route = useRoute()
const href = computed(() => loginURL(route.fullPath))
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-bg-subtle px-4">
    <div class="w-full max-w-sm">
      <div class="rounded-lg border border-line bg-bg px-6 py-8 text-center shadow-sm">
        <AppMark :size="48" class="mx-auto" />
        <h1 class="mt-5 text-xl">Sign in to Achievements</h1>
        <p class="mt-2 text-fg-subtle">See the leaderboard, what you've earned, and what's left to earn on this GitLab instance.</p>
        <a :href="href" class="btn btn-confirm mt-6 w-full">
          <GlIcon name="tanuki" />
          Sign in with GitLab
        </a>
      </div>
      <p class="mt-4 text-center text-xs text-fg-muted">GitLab asks you to authorize this app, then sends you back here.</p>
    </div>
  </main>
</template>
