<script setup lang="ts">
import EmptyState from '@/components/EmptyState.vue'
</script>

<template>
  <EmptyState title="Page not found">
    Nothing lives at this address.
    <template #actions>
      <RouterLink :to="{ name: 'leaderboard' }" class="btn">Go to the leaderboard</RouterLink>
    </template>
  </EmptyState>
</template>
