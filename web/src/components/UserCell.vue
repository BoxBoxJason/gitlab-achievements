<script setup lang="ts">
import { computed } from 'vue'

import UserAvatar from './UserAvatar.vue'

const props = defineProps<{ username: string; avatarUrl: string; userId: number; you?: boolean }>()

// A user first seen through a payload that didn't name them has no username
// yet; the API resolves them by GitLab ID until one arrives.
const profileRef = computed(() => props.username || String(props.userId))
</script>

<template>
  <RouterLink
    :to="{ name: 'user', params: { ref: profileRef } }"
    class="group flex w-fit min-w-0 max-w-full items-center gap-2 text-fg-strong hover:no-underline"
  >
    <UserAvatar :username="username" :src="avatarUrl" :user-id="userId" :size="24" />
    <span class="truncate font-semibold group-hover:underline">{{ username || `User ${userId}` }}</span>
    <span v-if="you" class="badge badge-info hidden sm:inline-flex">You</span>
  </RouterLink>
</template>
