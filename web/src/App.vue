<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import AppBreadcrumbs from '@/components/AppBreadcrumbs.vue'
import AppMark from '@/components/AppMark.vue'
import AppSidebar from '@/components/AppSidebar.vue'
import GlIcon from '@/components/GlIcon.vue'
import { pageCrumbs } from '@/composables/crumbs'
import { loadSession, session, signOut } from '@/composables/session'
import SignInView from '@/views/SignInView.vue'

const route = useRoute()
const drawer = ref(false)

const crumbs = computed(() => pageCrumbs.value ?? route.meta.crumbs?.(route) ?? [])

watch(
  () => route.fullPath,
  () => (drawer.value = false),
)

// The tab title follows the page, as GitLab's does: page, then app.
watch(
  crumbs,
  (trail) => {
    const page = trail[trail.length - 1]?.label
    document.title = page ? `${page} · Achievements` : 'Achievements'
  },
  { immediate: true },
)

void loadSession()
</script>

<template>
  <div v-if="session.status === 'loading'" class="grid min-h-dvh place-items-center" role="status" aria-label="Loading">
    <AppMark :size="32" class="animate-pulse" />
  </div>

  <SignInView v-else-if="session.status === 'signed-out'" />

  <main v-else-if="session.status === 'forbidden' || session.status === 'error'" class="grid min-h-dvh place-items-center px-4">
    <div class="w-full max-w-sm text-center">
      <AppMark :size="48" class="mx-auto" />
      <template v-if="session.status === 'forbidden'">
        <h1 class="mt-6 text-xl">This instance is restricted to a group</h1>
        <p class="mt-2 text-fg-subtle">
          Your GitLab account isn't a member of the group allowed to see these achievements. Ask an owner of that group to add
          you, or sign in with another account.
        </p>
        <button type="button" class="btn mt-6" @click="signOut">Sign out</button>
      </template>
      <template v-else>
        <h1 class="mt-6 text-xl">Achievements are unavailable</h1>
        <p class="mt-2 text-fg-subtle">{{ session.error }} Try again in a moment.</p>
        <button type="button" class="btn mt-6" @click="loadSession">Try again</button>
      </template>
    </div>
  </main>

  <div v-else class="min-h-dvh lg:pl-60">
    <a href="#content" class="btn btn-confirm sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-50">Skip to content</a>

    <aside class="fixed inset-y-0 left-0 z-30 hidden w-60 border-r border-line lg:block">
      <AppSidebar />
    </aside>

    <Transition
      enter-from-class="opacity-0"
      leave-to-class="opacity-0"
      enter-active-class="transition-opacity duration-150"
      leave-active-class="transition-opacity duration-150"
    >
      <div v-if="drawer" class="fixed inset-0 z-40 bg-black/40 lg:hidden" @click="drawer = false" />
    </Transition>
    <Transition
      enter-from-class="-translate-x-full"
      leave-to-class="-translate-x-full"
      enter-active-class="transition-transform duration-200 ease-out"
      leave-active-class="transition-transform duration-150 ease-in"
    >
      <aside v-if="drawer" class="fixed inset-y-0 left-0 z-50 w-64 border-r border-line shadow-xl lg:hidden" aria-label="Menu">
        <AppSidebar @navigate="drawer = false" />
      </aside>
    </Transition>

    <div class="sticky top-0 z-20 flex h-12 items-center gap-3 border-b border-line bg-bg/95 px-4 backdrop-blur lg:px-6">
      <button type="button" class="btn h-8 w-8 px-0 lg:hidden" :aria-expanded="drawer" @click="drawer = !drawer">
        <GlIcon :name="drawer ? 'close' : 'hamburger'" />
        <span class="sr-only">Menu</span>
      </button>
      <AppBreadcrumbs :crumbs="crumbs" />
    </div>

    <main id="content" class="mx-auto max-w-[1100px] px-4 pb-16 pt-4 lg:px-6">
      <RouterView />
    </main>
  </div>
</template>
