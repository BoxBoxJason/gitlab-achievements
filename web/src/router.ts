import { createRouter, createWebHistory, type RouteLocationRaw } from 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    /** The trail above the page, ending in the page itself. */
    crumbs?: (route: import('vue-router').RouteLocationNormalizedLoaded) => { label: string; to?: RouteLocationRaw }[]
  }
}

// None of these may start with a prefix the server keeps for itself (/api/,
// /oauth/, /webhooks/, /healthz, /readyz): those never reach the frontend.
export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior: (to, from, saved) => saved ?? (to.path !== from.path ? { top: 0 } : undefined),
  routes: [
    {
      path: '/',
      name: 'leaderboard',
      component: () => import('./views/LeaderboardView.vue'),
      meta: { crumbs: () => [{ label: 'Leaderboard' }] },
    },
    {
      path: '/achievements',
      name: 'catalog',
      component: () => import('./views/CatalogView.vue'),
      meta: { crumbs: () => [{ label: 'Achievements' }] },
    },
    {
      path: '/achievements/:criteria',
      name: 'criteria',
      component: () => import('./views/CriteriaView.vue'),
      meta: { crumbs: (route) => [{ label: 'Achievements', to: { name: 'catalog' } }, { label: String(route.params.criteria) }] },
    },
    {
      path: '/users/:ref',
      name: 'user',
      component: () => import('./views/UserView.vue'),
      meta: {
        crumbs: (route) => [
          { label: 'Leaderboard', to: { name: 'leaderboard' } },
          { label: String(route.params.ref) },
        ],
      },
    },
    {
      path: '/participation',
      name: 'participation',
      component: () => import('./views/ParticipationView.vue'),
      meta: { crumbs: () => [{ label: 'Participation' }] },
    },
    {
      path: '/:missing(.*)*',
      name: 'missing',
      component: () => import('./views/MissingView.vue'),
      meta: { crumbs: () => [{ label: 'Not found' }] },
    },
  ],
})
