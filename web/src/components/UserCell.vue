<script setup lang="ts">
import UserAvatar from './UserAvatar.vue'

defineProps<{ username: string; avatarUrl: string; userId: number; you?: boolean }>()
</script>

<template>
  <RouterLink
    :to="{ name: 'user', params: { ref: username } }"
    class="group flex w-fit min-w-0 max-w-full items-center gap-2 text-fg-strong hover:no-underline"
  >
    <UserAvatar :username="username" :src="avatarUrl" :user-id="userId" :size="24" />
    <span class="truncate font-semibold group-hover:underline">{{ username }}</span>
    <span v-if="you" class="badge badge-info hidden sm:inline-flex">You</span>
  </RouterLink>
</template>
