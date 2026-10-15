<script setup lang="ts">
import GlIcon from './GlIcon.vue'

// The endpoint a page is drawn from, for whoever wants the numbers rather
// than the page. Opens the JSON the browser's own session can read.
defineProps<{ href: string }>()
</script>

<template>
  <a :href="href" target="_blank" rel="noopener" class="btn" :title="`Open ${href}`">
    <GlIcon name="code" />
    <span>API</span>
  </a>
</template>
